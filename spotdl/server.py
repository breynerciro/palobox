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

import collections
import dataclasses
import hmac
import http.server
import json
import logging
import os
import queue
import re
import secrets
import shutil
import signal
import sqlite3
import subprocess
import sys
import threading
import time
import unicodedata
import urllib.parse

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

FOUND = re.compile(r"^Found (\d+) songs? in (.+?) \([A-Za-z]+\)$")
COMPLETE = re.compile(r"^(\d+)/(\d+) complete$")
DOWNLOADED = re.compile(r'^Downloaded "(.*)": ')
ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
# Una línea de --save-errors de spotDL 4.6 (Downloader.async_search_and_download):
# "{song.url} - {Excepción}: {mensaje}". Las de reinit_song o campos faltantes no traen URL.
TRACK_ERROR = re.compile(r"^(https://open\.spotify\.com/track/[A-Za-z0-9]+) - (\w+): (.*)$")
DATE_LINE = re.compile(r"^\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}$")

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
    threads: int = 2
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
            threads=max(1, entero("THREADS", 2)),
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
            json.dump({"load_config": True, "client_id": self.client_id, "client_secret": self.client_secret}, f)
        os.replace(tmp, path)


class Feed:
    """Lee la salida de spotDL y va acumulando título, total y avance."""

    def __init__(self):
        self.title = ""
        self.current = ""
        self.total = 0
        self.done = 0
        self.tail = collections.deque(maxlen=6)  # últimas líneas, para explicar un fallo

    def line(self, line):
        """Procesa una línea; devuelve True si cambió algo que valga guardar."""
        text = ANSI.sub("", line).strip()
        if not text:
            return False
        self.tail.append(text)
        changed = False

        m = FOUND.match(text)
        if m:
            total, title = int(m.group(1)), m.group(2).strip()
            if total and total != self.total:
                self.total, changed = total, True
            if title and title != self.title:
                self.title, changed = title, True

        m = COMPLETE.search(text)
        if m:
            done, total = int(m.group(1)), int(m.group(2))
            if total != self.total:
                self.total, changed = total, True
            if done != self.done:
                self.done, changed = done, True

        m = DOWNLOADED.match(text)
        if m:
            current = m.group(1).strip()
            if current != self.current:
                self.current, changed = current, True
            if not self.title:  # canción suelta: spotDL no anuncia una lista
                self.title, changed = current, True
        return changed


class Store:
    """Cola persistente en SQLite; el servicio es su único dueño."""

    SCHEMA = """
    CREATE TABLE IF NOT EXISTS jobs(
        id TEXT PRIMARY KEY,
        url TEXT NOT NULL,
        title TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL,
        current TEXT NOT NULL DEFAULT '',
        total INTEGER NOT NULL DEFAULT 0,
        done INTEGER NOT NULL DEFAULT 0,
        error TEXT NOT NULL DEFAULT '',
        created INTEGER NOT NULL,
        owner TEXT NOT NULL DEFAULT '',
        attempts INTEGER NOT NULL DEFAULT 1,
        failed TEXT NOT NULL DEFAULT '[]',
        notice TEXT NOT NULL DEFAULT ''
    );
    CREATE INDEX IF NOT EXISTS jobs_created ON jobs(created DESC);
    """
    # Columnas que bases viejas no tienen. Sin dueño, el trabajo solo lo quita el administrador.
    # failed: [{"url", "name", "retry"}]; url vacía con retry = no se sabe cuál, se repite el enlace entero.
    COLUMNS = {
        "owner": "TEXT NOT NULL DEFAULT ''",
        "attempts": "INTEGER NOT NULL DEFAULT 1",
        "failed": "TEXT NOT NULL DEFAULT '[]'",
        "notice": "TEXT NOT NULL DEFAULT ''",
    }

    def __init__(self, path, keep_finished=30, max_attempts=3):
        self.keep = keep_finished
        self.max_attempts = max_attempts
        self.lock = threading.RLock()
        self.db = sqlite3.connect(path, check_same_thread=False, timeout=10)
        self.db.row_factory = sqlite3.Row
        with self.lock:
            self.db.execute("PRAGMA journal_mode=WAL")
            self.db.execute("PRAGMA busy_timeout=5000")
            self.db.executescript(self.SCHEMA)
            cols = [r["name"] for r in self.db.execute("PRAGMA table_info(jobs)")]
            for name, ddl in self.COLUMNS.items():
                if name not in cols:
                    self.db.execute(f"ALTER TABLE jobs ADD COLUMN {name} {ddl}")
            self.db.commit()
            if self.db.execute("PRAGMA user_version").fetchone()[0] < 1:
                self.migrate_v1(os.path.dirname(path))
                self.db.execute("PRAGMA user_version=1")  # en la misma transacción que las filas
            self.db.commit()

    def migrate_v1(self, state_dir):
        """Trabajos terminados con las reglas viejas: el éxito ya se vio y se va; un 'done' con
        canciones fallidas o un 'error' quedan reintentables. La cola activa no se toca."""
        rows = self.db.execute("SELECT * FROM jobs WHERE status NOT IN ('queued', 'downloading')").fetchall()
        for r in rows:
            if r["status"] not in ("done", "error") or (r["status"] == "done" and not r["error"]):
                self.db.execute("DELETE FROM jobs WHERE id=?", (r["id"],))
                continue
            failed = []
            if r["status"] == "done":
                failed = failures_from(os.path.join(state_dir, f"errors-{r['id']}.txt"))
                if not failed:  # sin el archivo solo queda el número del texto viejo
                    m = re.match(r"(\d+) canci", r["error"])
                    failed = [{"url": "", "name": "", "retry": True}] * (int(m.group(1)) if m else 1)
            status, error, notice = outcome(r["title"], r["url"], failed, not failed, 1, self.max_attempts)
            self.db.execute(
                "UPDATE jobs SET status=?, error=?, notice=?, failed=? WHERE id=?",
                (status, error, notice, json.dumps(failed, ensure_ascii=False), r["id"]),
            )

    def resume(self):
        """Lo que quedó a medias vuelve a la cola; spotDL salta lo ya descargado."""
        with self.lock:
            self.db.execute(
                "UPDATE jobs SET status='queued', done=0, total=0, current='', error='' "
                "WHERE status IN ('queued', 'downloading')"
            )
            self.db.commit()

    def jobs(self, user=None, admin=False):
        """La cola completa; los avisos (finished) solo para su dueño, o los sin dueño para el admin."""
        with self.lock:
            rows = self.db.execute(
                "SELECT * FROM jobs WHERE status<>'finished' OR owner=? OR (owner='' AND ?) ORDER BY "
                "CASE WHEN status IN ('queued', 'downloading') THEN 0 ELSE 1 END, "
                "created DESC, rowid DESC",
                (user, admin),
            ).fetchall()
        return [job_json(r) for r in rows]

    def failed(self, jid):
        with self.lock:
            row = self.db.execute("SELECT failed FROM jobs WHERE id=?", (jid,)).fetchone()
        return json.loads(row["failed"]) if row else []

    def get(self, jid):
        with self.lock:
            row = self.db.execute("SELECT * FROM jobs WHERE id=?", (jid,)).fetchone()
        return job_json(row) if row else None

    def status(self, jid):
        with self.lock:
            row = self.db.execute("SELECT status FROM jobs WHERE id=?", (jid,)).fetchone()
        return row["status"] if row else None

    def create(self, url, owner=""):
        jid = secrets.token_hex(8)
        with self.lock:
            self.db.execute(
                "INSERT INTO jobs(id, url, title, status, current, total, done, error, created, owner) "
                "VALUES(?, ?, '', 'queued', '', ?, 0, '', ?, ?)",
                (jid, url, 1 if "/track/" in url else 0, int(time.time() * 1000), owner),
            )
            self.db.commit()
        return self.get(jid)

    def active_count(self, owner=None):
        """Trabajos activos; con owner, solo los de ese usuario."""
        sql = "SELECT COUNT(*) FROM jobs WHERE status IN ('queued', 'downloading')"
        args = ()
        if owner is not None:
            sql, args = sql + " AND owner=?", (owner,)
        with self.lock:
            return self.db.execute(sql, args).fetchone()[0]

    def find_active(self, url):
        with self.lock:
            row = self.db.execute(
                "SELECT * FROM jobs WHERE url=? AND status IN ('queued', 'downloading') "
                "ORDER BY created LIMIT 1",
                (url,),
            ).fetchone()
        return job_json(row) if row else None

    def next_queued(self):
        with self.lock:
            row = self.db.execute(
                "SELECT * FROM jobs WHERE status='queued' ORDER BY created, rowid LIMIT 1"
            ).fetchone()
        return job_json(row) if row else None

    def claim(self, jid):
        """Pasa el trabajo a 'downloading'; False si otro ya lo tomó o se canceló."""
        with self.lock:
            cur = self.db.execute(
                "UPDATE jobs SET status='downloading', done=0, "
                "total=CASE WHEN instr(url, '/track/') > 0 THEN 1 ELSE 0 END, "
                "current='', error='' WHERE id=? AND status='queued'",
                (jid,),
            )
            self.db.commit()
            return cur.rowcount == 1

    def progress(self, jid, feed):
        # Los ceros y los textos vacíos no borran lo que ya se sabía (un track nace con total=1).
        with self.lock:
            self.db.execute(
                "UPDATE jobs SET "
                "title=CASE WHEN ?<>'' THEN ? ELSE title END, "
                "current=CASE WHEN ?<>'' THEN ? ELSE current END, "
                "total=CASE WHEN ?>0 THEN ? ELSE total END, done=? "
                "WHERE id=? AND status='downloading'",
                (feed.title, feed.title, feed.current, feed.current, feed.total, feed.total, feed.done, jid),
            )
            self.db.commit()

    def set_status(self, jid, status):
        with self.lock:
            self.db.execute(
                "UPDATE jobs SET status=? WHERE id=? AND status IN ('queued', 'downloading')",
                (status, jid),
            )
            self.db.commit()

    def finish(self, jid, status, error="", failed=(), notice="", title=""):
        with self.lock:
            self.db.execute(
                "UPDATE jobs SET status=?, error=?, failed=?, notice=?, "
                "title=CASE WHEN title='' THEN ? ELSE title END, "
                "total=CASE WHEN total=0 AND done>0 THEN done ELSE total END "
                "WHERE id=? AND status='downloading'",
                (status, error, json.dumps(list(failed), ensure_ascii=False), notice, title, jid),
            )
            self.db.commit()
            self.prune()

    def requeue(self, jid):
        """Vuelve a la cola un trabajo reintentable; conserva la lista de fallidas para pedir solo esas."""
        with self.lock:
            cur = self.db.execute(
                "UPDATE jobs SET status='queued', attempts=attempts+1, error='', done=0, current='' "
                "WHERE id=? AND status='error' AND attempts<?",
                (jid, self.max_attempts),
            )
            self.db.commit()
            return cur.rowcount == 1

    def delete(self, jid):
        with self.lock:
            self.db.execute("DELETE FROM jobs WHERE id=?", (jid,))
            self.db.commit()

    def prune(self):
        """Conserva solo los últimos terminados."""
        with self.lock:
            self.db.execute(
                "DELETE FROM jobs WHERE status NOT IN ('queued', 'downloading') AND id NOT IN ("
                "  SELECT id FROM jobs WHERE status NOT IN ('queued', 'downloading') "
                "  ORDER BY created DESC, rowid DESC LIMIT ?)",
                (self.keep,),
            )
            self.db.commit()


def job_json(row):
    total, done = row["total"], row["done"]
    progress = round(min(done, total) / total, 4) if total > 0 else None
    return {
        "id": row["id"],
        "url": row["url"],
        "title": row["title"],
        "status": row["status"],
        "current": row["current"],
        "total": total,
        "done": done,
        "progress": progress,
        "error": row["error"],
        "created": row["created"],
        "owner": row["owner"],
        "attempts": row["attempts"],
        "notice": row["notice"],
    }


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


def permanent(kind, message):
    """Fallos que reintentar no arregla: sin resultados en el proveedor (LookupError de
    Downloader.search) o yt-dlp sin datos del video. Lo demás (YT-DLP download error, ffmpeg,
    metadatos, red) suele ceder al reintentar."""
    return kind == "LookupError" or (kind == "AudioProviderError" and message.startswith("No metadata found"))


def failures_from(path, names=None):
    """Canciones fallidas según --save-errors. El detalle técnico va al log, nunca a la interfaz."""
    try:
        with open(path, encoding="utf-8") as f:
            lines = [l.strip() for l in f if l.strip()]
    except OSError:
        return []
    out = []
    for line in lines:
        if DATE_LINE.match(line):  # cabecera de cada ejecución (el archivo se abre en modo append)
            continue
        log.warning("canción fallida: %s", line)
        m = TRACK_ERROR.match(line)
        if not m:  # sin URL no hay cómo pedir solo esa canción
            out.append({"url": "", "name": "", "retry": False})
            continue
        url = m.group(1)
        out.append({"url": url, "name": (names or {}).get(url, ""), "retry": not permanent(m.group(2), m.group(3))})
    return out


def song_names(path):
    """url → «Canción — Artista» del --save-file que spotDL escribe al final (sin pedir nada más)."""
    try:
        with open(path, encoding="utf-8") as f:
            songs = json.load(f)
        return {s["url"]: f"{s['name']} — {s['artist']}" for s in songs if s.get("url") and s.get("name")}
    except (OSError, ValueError, TypeError, KeyError):
        return {}


def norm(s):
    """Texto comparable: sin mayúsculas, tildes, espacios ni signos («Canción» = «CANCION»)."""
    return "".join(c for c in unicodedata.normalize("NFKD", s or "").casefold() if c.isalnum())


def read_tags(path):
    """Artistas (TPE1), título, duración y total de pistas del álbum (TRCK «n/total») de un MP3."""
    from mutagen.mp3 import MP3  # perezoso: viene con spotDL y las pruebas lo sustituyen

    audio = MP3(path)
    tags = audio.tags or {}

    def texts(key):
        frame = tags.get(key)
        return [str(t) for t in frame.text] if frame else []

    total = (texts("TRCK") or [""])[0].partition("/")[2]
    return {
        "artists": [a for t in texts("TPE1") for a in t.split("/")],
        "title": (texts("TIT2") or [""])[0],
        "duration": audio.info.length,
        "total": int(total) if total.isdigit() else 0,
    }


def index_library(music_dir):
    """(artista, título) normalizados → rutas, según <Artista>/<Álbum>/<Título>.mp3. No lee
    etiquetas: eso se hace solo con los candidatos."""
    index = collections.defaultdict(list)
    for root, _dirs, files in os.walk(music_dir):
        parts = os.path.relpath(root, music_dir).split(os.sep)
        if parts[0] == ".":
            continue
        for name in files:
            stem, ext = os.path.splitext(name)
            if ext.lower() == ".mp3":
                index[(norm(parts[0]), norm(stem))].append(os.path.join(root, name))
    return index


def filter_plan(songs, index):
    """(a bajar, saltadas): se salta la canción que ya está en disco con el mismo artista y
    título, ±3 s de duración y un álbum igual o más grande. Si llega un sencillo y en disco está
    la versión de un álbum, se salta aunque duren distinto (radio edit, otra intro). Si solo está
    el sencillo, la versión del álbum sí se baja (la app Go borra luego el sencillo)."""
    tags = {}

    def read(path):
        if path not in tags:
            try:
                tags[path] = read_tags(path)
            except Exception as exc:  # un MP3 dañado no frena la descarga: cuenta como ausente
                log.warning("no se pudieron leer las etiquetas de %s: %s", path, exc)
                tags[path] = None
        return tags[path]

    todo, skipped = [], []
    for song in songs:
        artists = {norm(a) for a in [song.get("artist"), *(song.get("artists") or [])] if norm(a)}
        title = norm(song.get("name"))
        paths = {p for a in artists for p in index.get((a, title), ())}
        # spotDL nombra la carpeta del sencillo como la canción.
        single = norm(song.get("album_name")) == title
        found = any(
            t and norm(t["title"]) == title and artists & {norm(a) for a in t["artists"]}
            and (
                abs(t["duration"] - (song.get("duration") or 0)) <= 3
                and t["total"] >= (song.get("tracks_count") or 0)
                or single and norm(os.path.basename(os.path.dirname(p))) != title
            )
            for p, t in ((p, read(p)) for p in sorted(paths))
        )
        (skipped if found else todo).append(song)
    return todo, skipped


def songs_text(n):
    return "1 canción" if n == 1 else f"{n} canciones"


def names_text(failed, limit=3):
    shown = [f["name"] for f in failed if f["name"]][:limit]
    if not shown:
        return ""
    rest = len(failed) - len(shown)
    return ", ".join(shown) + (f" y {rest} más" if rest else "")


def row_text(failed):
    """Mensaje de una fila reintentable."""
    if not failed:
        return "No se pudo completar la descarga."
    n = len(failed)
    text = songs_text(n) + (" no se pudo descargar" if n == 1 else " no se pudieron descargar")
    names = names_text(failed)
    return f"{text}: {names}" if names else text


def outcome(title, url, failed, run_failed, attempts, max_attempts, skipped=0):
    """(status, error, notice) tras una ejecución: reintentable mientras quede algo transitorio
    y haya intentos; si no, finished con el aviso para su dueño."""
    if (run_failed or any(f["retry"] for f in failed)) and attempts < max_attempts:
        return "error", row_text(failed), ""
    status, error, notice = finished(title, url, failed, run_failed)
    if skipped:
        notice += f" {songs_text(skipped)} ya " + ("estaba" if skipped == 1 else "estaban") + " en la biblioteca."
    return status, error, notice


def finished(title, url, failed, run_failed):
    name = title or (failed[0]["name"] if failed else "") or "tu descarga"
    if not failed and not run_failed:
        return "finished", "", f"«{name}» ya está en tu biblioteca."
    # Con fallos, error conserva el resumen: la interfaz lo usa para el tono del aviso.
    if not failed:
        return "finished", row_text(failed), f"No se pudo descargar «{name}». Puedes pedirla de nuevo más tarde."
    n = len(failed)
    gone = not any(f["retry"] for f in failed)
    verb = ("no está disponible" if n == 1 else "no están disponibles") if gone else (
        "no se pudo descargar" if n == 1 else "no se pudieron descargar")
    if "/track/" in url:
        return "finished", row_text(failed), f"«{name}» {verb}."
    names = names_text(failed)
    return "finished", row_text(failed), f"{songs_text(n)} de «{name}» {verb}" + (f": {names}." if names else ".")


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
            job = self.store.next_queued()
            if not job:
                # Sin nada pendiente: se limpian las cancelaciones ya inútiles.
                with self.lock:
                    self.canceled.clear()
                with self.cv:
                    self.cv.wait(timeout=5)
                continue
            if self.store.claim(job["id"]):
                self.run_job(job)

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
        cookie = ["--cookie-file", self.s.cookie_file] if self.s.cookie_file else []
        # Las credenciales de Spotify las lee spotDL de su config.json (Settings.write_spotify_config).

        log.info("trabajo %s: empieza %s", jid, url)
        feed = Feed()
        if urls:  # un reintento: spotDL no anuncia lista, se conservan el título y el total pedido
            feed.title, feed.total = job["title"], len(urls)
            self.store.progress(jid, feed)
        started = time.monotonic()  # el tiempo máximo cubre el plan y la descarga
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
                    return self.settle(jid, [], False, skipped)

            rc = self.spotdl(jid, [
                "download", *query,
                "--output", os.path.join(self.s.music_dir, "{artist}/{album}/{title}.{output-ext}"),
                "--format", "mp3",
                "--bitrate", "128k",
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
                self.settle(jid, keep + failures_from(errfile, song_names(savefile)), False, skipped)
        finally:
            remove(temps)

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
        self.store.finish(jid, status, error, failed, notice, title)
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
        for name in os.listdir(self.settings.state_dir):
            # Restos de ejecuciones cortadas; Store ya migró lo que necesitaba de ellos.
            if (name.startswith("errors-") and name.endswith(".txt")) or (name.startswith("songs-") and name.endswith(".spotdl")):
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
        if job := store.find_active(url):
            return self.send_json(200, job)
        if err := self.over_limit(who[0]):
            return self.send_json(429, {"error": err})
        job = store.create(url, who[0])
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
        if err := self.over_limit(who[0]):
            return self.send_json(429, {"error": err})
        if not store.requeue(jid):
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
        httpd.server_close()


if __name__ == "__main__":
    main()
