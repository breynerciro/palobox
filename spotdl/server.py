#!/usr/bin/env python3
"""Servicio de descargas de Spotify para PaloBox.

La app Go no ejecuta spotDL: le manda el enlace a este servicio y consulta la cola.
Aquí viven el estado (SQLite propia), el proceso de spotDL y su cancelación, así que
un reinicio del contenedor no pierde los trabajos pendientes.

Solo usa la biblioteca estándar, más mutagen para leer etiquetas MP3 (llega con spotDL):
menos dependencias y una imagen más liviana.
El puerto no se publica; únicamente lo alcanza la app por la red de compose, y toda
petición debe traer el token del módulo (Authorization: Bearer) y la identidad del
usuario que la app ya autenticó (X-PaloBox-User, X-PaloBox-Admin).
"""

import dataclasses
import hmac
import http.server
import json
import logging
import os
import queue
import re
import shutil
import signal
import subprocess
import sys
import threading
import time
import urllib.parse

from library import drop_partial, filter_plan, index_library, playlist_tracks
from results import Feed, failures_from, outcome, song_names
from store import Store

log = logging.getLogger("spotdl.servicio")

# Un enlace de Spotify: track, álbum, playlist o artista. Se descarta el prefijo
# de idioma (/intl-es/) y cualquier parámetro de seguimiento (?si=...).
SPOTIFY = re.compile(
    r"^https://open\.spotify\.com/(?:intl-[A-Za-z-]{2,10}/)?"
    r"(track|album|playlist|artist)/([A-Za-z0-9]{16,40})(?:[/?#].*)?$"
)
JOB_ID = re.compile(r"^[0-9a-f]{16}$")
RETRY = re.compile(r"^/jobs/([0-9a-f]{16})/retry$")
USER = re.compile(r"^[a-z0-9_-]{1,32}$")  # mismo formato que los usuarios de la app

# Estados: queued y downloading corren; error = falló algo y se puede reintentar (fila con
# «Reintentar»); finished = ya no hay nada que hacer y solo queda el aviso para su dueño,
# que no se pinta como fila y se borra cuando la app lo muestra (DELETE).
ACTIVE = ("queued", "downloading")


def clean_url(raw):
    """Devuelve el enlace canónico de Spotify o None si no es válido."""
    if not isinstance(raw, str):
        return None
    raw = raw.strip()
    if not raw or len(raw) > 512:
        return None
    if urllib.parse.urlsplit(raw).hostname != "open.spotify.com":
        return None
    m = SPOTIFY.match(raw)
    if not m:
        return None
    return f"https://open.spotify.com/{m.group(1)}/{m.group(2)}"


@dataclasses.dataclass(frozen=True)
class Settings:
    """Configuración del servicio; los tests la construyen sin variables de entorno."""

    data_dir: str = "/data"
    port: int = 8801
    threads: int = 4
    max_active: int = 5
    max_per_user: int = 2
    keep_finished: int = 30
    max_attempts: int = 3  # el original, un reintento automático y uno pedido por el usuario
    job_timeout: float = 12 * 3600
    spotdl_bin: str = "spotdl"
    cookie_file: str = ""
    client_id: str = ""
    client_secret: str = ""
    token: str = ""  # secreto compartido con la app; sin él el servicio no arranca

    @classmethod
    def from_env(cls):
        def entero(nombre, defecto):
            try:
                return int(os.environ.get(nombre, defecto))
            except ValueError:
                return defecto

        try:
            timeout = float(os.environ.get("JOB_TIMEOUT", 12 * 3600))
        except ValueError:
            timeout = 12 * 3600
        try:
            with open(os.environ.get("TOKEN_FILE", "/run/secrets/spotdl_token"), encoding="utf-8") as f:
                token = f.read().strip()
        except OSError:
            token = ""
        return cls(
            data_dir=os.environ.get("DATA_DIR", "/data"),
            port=entero("PORT", 8801),
            threads=max(1, entero("THREADS", 4)),  # el valor por defecto de spotDL
            max_active=max(1, entero("MAX_ACTIVE", 5)),
            max_per_user=max(1, entero("MAX_PER_USER", 2)),
            job_timeout=timeout,
            spotdl_bin=os.environ.get("SPOTDL_BIN", "spotdl"),
            cookie_file=os.environ.get("SPOTDL_COOKIE_FILE", ""),
            client_id=os.environ.get("SPOTIFY_CLIENT_ID", ""),
            client_secret=os.environ.get("SPOTIFY_CLIENT_SECRET", ""),
            token=token,
        )

    @property
    def music_dir(self):
        return os.path.join(self.data_dir, "media", "musica")

    @property
    def state_dir(self):
        return os.path.join(self.data_dir, ".palobox", "spotdl")

    @property
    def home(self):
        return os.path.join(self.state_dir, "home")

    @property
    def db_path(self):
        return os.path.join(self.state_dir, "jobs.sqlite")

    def prepare(self):
        """Crea las carpetas de trabajo. La app y este servicio corren como 1000:1000."""
        for d in (
            self.music_dir,
            self.state_dir,
            self.home,
            os.path.join(self.state_dir, "deno"),
            os.path.join(self.state_dir, "cache"),
        ):
            os.makedirs(d, mode=0o755, exist_ok=True)
        self.write_spotify_config()

    def spotdl_config_dir(self):
        """La carpeta que spotDL 4.6 usa para config.json (misma prioridad que su get_spotdl_path)."""
        nuevo = os.path.join(self.home, ".config", "spotdl")
        viejo = os.path.join(self.home, ".spotdl")
        return viejo if os.path.isdir(viejo) and not os.path.isdir(nuevo) else nuevo

    def write_spotify_config(self):
        """Las credenciales van en el config.json de spotDL (0600), no en la línea de
        comandos: así no aparecen en `ps` ni en /proc/<pid>/cmdline."""
        path = os.path.join(self.spotdl_config_dir(), "config.json")
        if not (self.client_id and self.client_secret):
            try:
                os.remove(path)  # sin credenciales no debe quedar una copia vieja
            except FileNotFoundError:
                pass
            return
        os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
        tmp = path + ".tmp"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            # shortcut: spotDL 4.6 ignora estas credenciales salvo con use_official_api, y no se activa: en modo
            # desarrollo la API oficial exige Premium y no entrega playlists ajenas; revisar si Spotify lo cambia.
            json.dump({"load_config": True, "client_id": self.client_id, "client_secret": self.client_secret}, f)
        os.replace(tmp, path)



def kill_group(proc):
    """Mata a spotDL y a sus hijos (ffmpeg, yt-dlp) matando todo su grupo."""
    if proc.poll() is not None:
        return
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
    except (ProcessLookupError, PermissionError):
        return
    try:
        proc.wait(timeout=3)  # spotDL atiende SIGTERM, pero puede tardar en cerrar sus hilos
        return
    except subprocess.TimeoutExpired:
        pass
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass
    try:
        proc.wait(timeout=2)
    except subprocess.TimeoutExpired:
        pass



def remove(paths):
    for path in paths:
        try:
            os.remove(path)
        except FileNotFoundError:
            pass


class Runner:
    """Un solo trabajador: el servidor tiene dos núcleos y spotDL ya usa dos hilos."""

    def __init__(self, settings, store):
        self.s = settings
        self.store = store
        self.cv = threading.Condition()
        self.lock = threading.Lock()
        self.procs = {}  # id -> Popen en curso
        self.canceled = set()
        self.stopping = False
        self.thread = threading.Thread(target=self.loop, name="spotdl", daemon=True)

    def start(self):
        self.thread.start()

    def wake(self):
        with self.cv:
            self.cv.notify_all()

    def cancel(self, jid):
        with self.lock:
            self.canceled.add(jid)
            proc = self.procs.get(jid)
        if proc:
            kill_group(proc)
        self.wake()

    def stop(self):
        with self.cv:
            self.stopping = True
            self.cv.notify_all()
        with self.lock:
            procs = list(self.procs.values())
        for proc in procs:
            kill_group(proc)

    def loop(self):
        while True:
            with self.cv:
                if self.stopping:
                    return
            try:
                job = self.store.next_queued()
                if job and not self.store.claim(job["id"]):
                    continue
            except Exception:  # SQLite ocupada o disco lleno: se espera y se vuelve a mirar, el trabajador no muere
                log.exception("no se pudo leer la cola")
                job = None
            if not job:
                # Sin nada pendiente: se limpian las cancelaciones ya inútiles.
                with self.lock:
                    self.canceled.clear()
                with self.cv:
                    if not self.stopping:  # un apagado que llegó justo antes no espera los 5 s
                        self.cv.wait(timeout=5)
                continue
            try:
                self.run_job(job)
            except Exception:  # este es el único trabajador: un fallo no puede dejar la cola parada
                log.exception("trabajo %s: falló sin control", job["id"])
                with self.lock:
                    proc = self.procs.pop(job["id"], None)
                if proc:
                    kill_group(proc)
                try:
                    self.settle(job["id"], [], True)
                except Exception:
                    log.exception("trabajo %s: no se pudo marcar como error", job["id"])

    def run_job(self, job):
        jid, url = job["id"], job["url"]
        failed = self.store.failed(jid)
        retry = [f for f in failed if f["retry"]]
        # Un reintento pide solo las canciones transitorias; si no se sabe cuáles, el enlace entero.
        urls = [f["url"] for f in retry] if retry and all(f["url"] for f in retry) else []
        keep = [f for f in failed if not f["retry"]] if urls else []
        errfile = os.path.join(self.s.state_dir, f"errors-{jid}.txt")
        savefile = os.path.join(self.s.state_dir, f"songs-{jid}.spotdl")
        planfile = os.path.join(self.s.state_dir, f"plan-{jid}.spotdl")
        todofile = os.path.join(self.s.state_dir, f"to-download-{jid}.spotdl")
        temps = (errfile, savefile, planfile, todofile)
        remove(temps)
        # shortcut: tras un corte de luz solo se conocen las canciones de una playlist (set_songs); de un álbum o
        # una canción suelta, el MP3 a medias queda. Guardar el plan de todo trabajo si eso llega a pasar.
        drop_partial(self.s.music_dir, job["created"], [t for _, t in self.store.songs(jid)])  # de una corrida anterior
        cookie = ["--cookie-file", self.s.cookie_file] if self.s.cookie_file else []
        # Las credenciales de Spotify las lee spotDL de su config.json (Settings.write_spotify_config).

        log.info("trabajo %s: empieza %s", jid, url)
        feed = Feed()
        if urls:  # un reintento: spotDL no anuncia lista, se conservan el título y el total pedido
            feed.title, feed.total = job["title"], len(urls)
            self.store.progress(jid, feed)
        started = time.monotonic()  # el tiempo máximo cubre el plan y la descarga
        missing = []
        titles = [t for _, t in self.store.songs(jid)]  # para drop_partial; con el plan se sabe para todo tipo de enlace
        try:
            query, skipped = urls or [url], 0
            if not urls:
                # Primero el plan: la misma canción puede venir como sencillo y dentro de un álbum
                # (ids y rutas distintas), así que --overwrite skip no la detecta. --lyrics sin
                # proveedores: el plan no necesita letras.
                rc = self.spotdl(jid, [
                    "save", url, "--save-file", planfile, "--lyrics",
                    "--threads", str(self.s.threads), "--log-format", "%(message)s", *cookie,
                ], feed, started)
                if rc is None:
                    return
                if rc:
                    return self.settle(jid, failed, True)
                try:
                    with open(planfile, encoding="utf-8") as f:
                        plan = json.load(f)
                    # spotDL descarta en silencio (código 0) lo que no pudo resolver: lo que falte frente a
                    # «Found N songs» cuenta como fallido y reintentable; sin URL, el reintento repite el enlace.
                    # Una canción suelta no anuncia «Found N songs» (feed.total = 0): su plan vacío también es fallo.
                    # Una lista vacía sí anuncia «Found 0 songs» y no tiene nada que bajar.
                    expected = max(feed.total, 1) if "/track/" in url else feed.total
                    missing = [{"url": "", "name": "", "retry": True}] * max(0, expected - len(plan))
                    titles = [s.get("name") for s in plan]
                    plan.sort(key=lambda s: s.get("list_position") or 0)  # spotDL lo arma en orden de llegada
                    if "/playlist/" in url:
                        self.store.set_songs(jid, [[[s.get("artist"), *(s.get("artists") or [])], s.get("name")] for s in plan])
                    todo, skip = filter_plan(plan, index_library(self.s.music_dir))
                    with open(todofile, "w", encoding="utf-8") as f:
                        json.dump(todo, f, ensure_ascii=False)
                    query, skipped = [todofile], len(skip)
                    if not feed.title and len(plan) == 1:  # canción suelta: como la anunciaría «Downloaded»
                        feed.title = f"{plan[0]['artist']} - {plan[0]['name']}"
                        self.store.progress(jid, feed)
                    log.info("trabajo %s: %d por bajar, %d ya en la biblioteca", jid, len(todo), len(skip))
                except Exception:  # bajar de más es mejor que perder canciones
                    log.exception("trabajo %s: falló el filtro de duplicados; se baja el enlace entero", jid)
                    todo = None
                if todo == []:
                    return self.settle(jid, missing, False, skipped)

            rc = self.spotdl(jid, [
                "download", *query,
                "--output", os.path.join(self.s.music_dir, "{artist}/{album}/{title}.{output-ext}"),
                "--format", "mp3",
                "--bitrate", "128k",
                # El formato del flujo continuo de la app (StreamHz): así cada canción sale tal cual, sin convertir.
                "--ffmpeg-args", "-ar 48000 -ac 2",
                # Sin proveedores de letras: la app no las muestra y buscarlas costaba unos 7 s por canción en tres
                # sitios que casi siempre responden con un rechazo.
                "--lyrics",
                "--threads", str(self.s.threads),
                "--overwrite", "skip",
                "--simple-tui",
                "--log-format", "%(message)s",
                "--save-errors", errfile,
                "--save-file", savefile,  # nombres de todas las canciones, para decir cuáles fallaron
                *cookie,
            ], feed, started)
            if rc is None:
                return
            if rc:
                self.settle(jid, failed, True)
            else:
                self.settle(jid, keep + missing + failures_from(errfile, song_names(savefile)), False, skipped)
        finally:
            remove(temps)
            drop_partial(self.s.music_dir, job["created"], titles)

    def spotdl(self, jid, args, feed, started):
        """Corre spotDL e informa el avance. Devuelve su código (o 1 si no arrancó o se pasó del
        tiempo), o None si ya no hay que cerrar el trabajo: se apaga el servicio o se canceló."""
        if self.stopping:  # el servicio se apagó entre reclamar el trabajo y arrancarlo
            return None
        try:
            proc = subprocess.Popen(
                [self.s.spotdl_bin, *args],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                stdin=subprocess.DEVNULL,
                text=True,
                encoding="utf-8",
                errors="replace",
                bufsize=1,
                env=self.child_env(),
                start_new_session=True,  # grupo propio: cancelar mata también a los hijos
            )
        except OSError as exc:  # incluye spotDL sin instalar
            log.error("trabajo %s: no se pudo iniciar spotDL: %s", jid, exc)
            return 1

        with self.lock:
            self.procs[jid] = proc
            canceled = jid in self.canceled or self.stopping
        if canceled:
            kill_group(proc)

        lines = queue.Queue()

        def pump():
            try:
                for line in proc.stdout:
                    lines.put(line)
            finally:
                lines.put(None)

        threading.Thread(target=pump, name=f"spotdl-{jid}", daemon=True).start()
        timed_out = False
        while True:
            # El reloj se revisa en cada vuelta: si spotDL imprime sin parar, igual se corta.
            if not timed_out and time.monotonic() - started > self.s.job_timeout:
                timed_out = True
                kill_group(proc)
            try:
                line = lines.get(timeout=1)
            except queue.Empty:
                continue
            if line is None:
                break
            if feed.line(line):
                self.store.progress(jid, feed)

        proc.wait()
        try:
            proc.stdout.close()
        except OSError:
            pass
        with self.lock:
            self.procs.pop(jid, None)
            canceled = jid in self.canceled
            self.canceled.discard(jid)
        self.store.progress(jid, feed)  # último estado conocido, aun si fue cancelado

        if self.stopping:
            # Se apaga el servicio: el trabajo queda en 'downloading' y al arrancar
            # Store.resume() lo devuelve a la cola (spotDL salta lo ya descargado).
            return None
        if canceled or self.store.status(jid) is None:  # se canceló y se quitó de la lista
            return None
        if timed_out or proc.returncode != 0:
            log.error("trabajo %s: %s; últimas líneas: %s", jid,
                      "tiempo máximo" if timed_out else f"spotDL {args[0]} salió con {proc.returncode}", " | ".join(feed.tail))
            return 1
        return 0

    def settle(self, jid, failed, run_failed, skipped=0):
        """Aplica outcome(); tras el primer intento, lo transitorio se reintenta solo una vez."""
        job = self.store.get(jid)
        if not job:
            return
        status, error, notice = outcome(job["title"], job["url"], failed, run_failed, job["attempts"], self.s.max_attempts, skipped)
        title = next((f["name"] for f in failed if f["name"]), "") if "/track/" in job["url"] else ""
        tracks = []
        if status == "finished":
            try:
                tracks = playlist_tracks(self.store.songs(jid), index_library(self.s.music_dir), self.s.music_dir)
            except Exception:  # la playlist es un extra: la descarga ya quedó
                log.exception("trabajo %s: no se pudo armar la playlist", jid)
            if tracks:
                notice += " Quedó como playlist en Tu biblioteca."
        self.store.finish(jid, status, error, failed, notice, title, tracks)
        if status == "error" and job["attempts"] == 1 and self.store.requeue(jid):
            status = "queued (reintento automático)"
        log.info("trabajo %s: %s, %d fallidas", jid, status, len(failed))

    def child_env(self):
        env = os.environ.copy()
        for k in ("SPOTIFY_CLIENT_ID", "SPOTIFY_CLIENT_SECRET", "TOKEN_FILE"):
            env.pop(k, None)  # spotDL, ffmpeg e yt-dlp no necesitan secretos
        env.update(
            HOME=self.s.home,
            DENO_DIR=os.path.join(self.s.state_dir, "deno"),
            XDG_CACHE_HOME=os.path.join(self.s.state_dir, "cache"),
            NO_COLOR="1",
            TERM="dumb",
            COLUMNS="1000",  # sin esto, rich parte las líneas a 80 columnas y se pierde el progreso
            PYTHONUNBUFFERED="1",
            PYTHONIOENCODING="utf-8",
        )
        return env


class Service:
    def __init__(self, settings):
        settings.prepare()
        self.settings = settings
        self.store = Store(settings.db_path, settings.keep_finished, settings.max_attempts)
        self.runner = Runner(settings, self.store)
        self._versions = None
        self._version_lock = threading.Lock()

    def start(self):
        for sub in ("temp", "errors"):  # audio crudo y ffmpeg_error_*.txt que spotDL deja y nunca borra
            shutil.rmtree(os.path.join(self.settings.spotdl_config_dir(), sub), ignore_errors=True)
        for name in os.listdir(self.settings.state_dir):
            # Restos de ejecuciones cortadas.
            if (name.startswith("errors-") and name.endswith(".txt")) or (
                    name.startswith(("songs-", "plan-", "to-download-")) and name.endswith(".spotdl")):
                try:
                    os.remove(os.path.join(self.settings.state_dir, name))
                except OSError:
                    pass
        self.store.resume()
        self.runner.start()

    def stop(self):
        self.runner.stop()

    def cancel(self, jid):
        job = self.store.get(jid)
        if not job:
            return False
        if job["status"] in ACTIVE:
            self.store.set_status(jid, "canceled")
            self.runner.cancel(jid)
        self.store.delete(jid)
        return True

    def versions(self):
        with self._version_lock:
            if self._versions is None:
                self._versions = {
                    "spotdl": self._version([self.settings.spotdl_bin, "--version"]),
                    "deno": self._version(["deno", "--version"]),
                }
            return self._versions

    def _version(self, cmd):
        try:
            out = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=20,
                env=self.runner.child_env(),  # mismo HOME que las descargas
            ).stdout
        except (OSError, subprocess.SubprocessError):
            return ""
        return out.strip().splitlines()[0].strip() if out.strip() else ""


class Handler(http.server.BaseHTTPRequestHandler):
    service = None  # lo pone make_handler
    server_version = "palobox-spotdl/1"

    def log_message(self, fmt, *args):
        log.debug("%s %s", self.command, self.path)

    def send_json(self, code, body):
        raw = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def route(self):
        return urllib.parse.urlsplit(self.path).path.rstrip("/") or "/"

    def authorized(self):
        """Solo la app (que tiene el token) habla con el servicio; si no, 401."""
        got = self.headers.get("Authorization", "")
        want = "Bearer " + self.service.settings.token
        if self.service.settings.token and hmac.compare_digest(got.encode(), want.encode()):
            return True
        self.send_json(401, {"error": "no autorizado"})
        return False

    def caller(self):
        """Usuario de la app que hace la petición; None (y 400) si falta o no es válido."""
        name = self.headers.get("X-PaloBox-User", "")
        if USER.fullmatch(name):
            return name, self.headers.get("X-PaloBox-Admin") == "1"
        self.send_json(400, {"error": "falta el usuario"})
        return None

    def do_GET(self):
        if not self.authorized():
            return
        path = self.route()
        if path == "/health":
            if not shutil.which(self.service.settings.spotdl_bin):
                return self.send_json(503, {"ok": False, "error": "spotDL no está instalado."})
            if not self.service.runner.thread.is_alive():
                return self.send_json(503, {"ok": False, "error": "El trabajador de descargas se detuvo."})
            return self.send_json(200, {"ok": True, "active": self.service.store.active_count(), **self.service.versions()})
        if path == "/jobs":
            who = self.caller()
            if who:
                self.send_json(200, self.service.store.jobs(*who))
            return
        self.send_json(404, {"error": "no encontrado"})

    def do_POST(self):
        if not self.authorized():
            return
        path = self.route()
        if path != "/jobs" and not RETRY.fullmatch(path):
            return self.send_json(404, {"error": "no encontrado"})
        who = self.caller()
        if not who:
            return
        if m := RETRY.fullmatch(path):
            return self.retry(m.group(1), who)
        try:
            n = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            n = 0
        if n <= 0 or n > 64 * 1024:
            return self.send_json(400, {"error": "petición inválida"})
        try:
            body = json.loads(self.rfile.read(n))
        except (ValueError, UnicodeDecodeError):
            return self.send_json(400, {"error": "petición inválida"})
        url = clean_url(body.get("url") if isinstance(body, dict) else None)
        if not url:
            return self.send_json(400, {"error": "El enlace no es de Spotify."})

        store = self.service.store
        with store.lock:  # revisar y crear de una vez: un doble clic no crea dos trabajos
            found = store.find_active(url, who[0])
            err = "" if found else self.over_limit(who[0])
            job = None if found or err else store.create(url, who[0])
        if found:
            return self.send_json(200, found)
        if err:
            return self.send_json(429, {"error": err})
        self.service.runner.wake()
        self.send_json(201, job)

    def over_limit(self, user):
        store, s = self.service.store, self.service.settings
        if store.active_count(user) >= s.max_per_user:
            return f"Ya tienes {s.max_per_user} descargas en cola; espera a que terminen."
        if store.active_count() >= s.max_active:
            return "Hay demasiadas descargas en cola."
        return ""

    def retry(self, jid, who):
        store = self.service.store
        job = store.get(jid)
        if not job:
            return self.send_json(404, {"error": "no encontrado"})
        if job["owner"] != who[0] and not who[1]:
            return self.send_json(403, {"error": "Solo quien pidió esta descarga puede reintentarla."})
        if job["status"] != "error":
            return self.send_json(409, {"error": "Esta descarga ya no se puede reintentar."})
        with store.lock:
            err = self.over_limit(who[0])
            ok = not err and store.requeue(jid)
        if err:
            return self.send_json(429, {"error": err})
        if not ok:
            return self.send_json(409, {"error": "Esta descarga ya no se puede reintentar."})
        self.service.runner.wake()
        self.send_json(200, store.get(jid))

    def do_DELETE(self):
        if not self.authorized():
            return
        who = self.caller()
        if not who:
            return
        jid = self.route().removeprefix("/jobs/")
        job = self.service.store.get(jid) if JOB_ID.fullmatch(jid) else None
        if not job:
            return self.send_json(404, {"error": "no encontrado"})
        # La música es de todos, pero cada descarga solo la cancela o quita quien la pidió.
        if job["owner"] != who[0] and not who[1]:
            return self.send_json(403, {"error": "Solo quien pidió esta descarga puede cancelarla o quitarla."})
        if not self.service.cancel(jid):
            return self.send_json(404, {"error": "no encontrado"})
        self.send_json(200, {"ok": True})


def make_handler(service):
    return type("HandlerDeServicio", (Handler,), {"service": service})


def main():
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
        stream=sys.stdout,
    )
    settings = Settings.from_env()
    if not settings.token:
        log.error("falta el token del módulo (TOKEN_FILE); no se arranca sin autenticación")
        sys.exit(1)
    service = Service(settings)
    service.start()
    httpd = http.server.ThreadingHTTPServer(("0.0.0.0", settings.port), make_handler(service))
    httpd.daemon_threads = True

    def ship(_signum, _frame):
        log.info("cerrando; se detiene spotDL")
        service.stop()
        threading.Thread(target=httpd.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, ship)
    signal.signal(signal.SIGINT, ship)
    log.info("escuchando en :%d, biblioteca en %s", settings.port, settings.music_dir)
    try:
        httpd.serve_forever()
    finally:
        service.stop()
        # El trabajador borra al salir los MP3 a medias: sin esperarlo quedaban y al reanudar se daban por hechos.
        service.runner.thread.join(timeout=8)  # Docker da 10 s
        httpd.server_close()


if __name__ == "__main__":
    main()
