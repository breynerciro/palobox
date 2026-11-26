#!/usr/bin/env python3
"""Pruebas del servicio de descargas de Spotify (sin red y con un spotDL falso)."""

import dataclasses
import json
import sqlite3
import os
import tempfile
import threading
import time
import unittest
from unittest import mock
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

import server
from test_library import biblioteca


class UrlTest(unittest.TestCase):
    def test_limpia_y_canoniza(self):
        casos = {
            "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC": "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
            " https://open.spotify.com/intl-es/track/4uLU6hMCjMI75M1A2tKUQC?si=abc ": "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
            "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX#frag": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX",
            "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
            "https://open.spotify.com/artist/06HL4z0CvFAxyc27GXpf02": "https://open.spotify.com/artist/06HL4z0CvFAxyc27GXpf02",
        }
        for raw, want in casos.items():
            self.assertEqual(server.clean_url(raw), want, raw)

    def test_rechaza_lo_que_no_es_spotify(self):
        for raw in (
            "",
            "   ",
            "http://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",  # sin TLS
            "https://open.spotify.com.evil.example/track/4uLU6hMCjMI75M1A2tKUQC",
            "https://evil.example/open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
            "https://open.spotify.com/track/corto",
            "https://open.spotify.com/episode/4uLU6hMCjMI75M1A2tKUQC",  # no es música
            "https://open.spotify.com/search/daft%20punk",
            "https://spotify.link/abc",
            "daft punk - one more time",
            "https://open.spotify.com/track/" + "a" * 600,
            None,
            123,
        ):
            self.assertIsNone(server.clean_url(raw), repr(raw))


class ServiceTest(unittest.TestCase):
    """Arranca el servicio real y un spotDL falso que solo imprime y escribe lo pedido."""

    CABECERA = """#!/usr/bin/env python3
import json, os, subprocess, sys, time
args = sys.argv[1:]
# Un download de un .spotdl deja en el log las URLs que traía (el archivo se borra al terminar).
todo = [s["url"] for a in args[1:2] if a.endswith(".spotdl") for s in json.load(open(a))]
if os.environ.get("FAKE_LOG"):
    with open(os.environ["FAKE_LOG"], "a") as f:
        f.write(json.dumps({"args": args, "home": os.environ.get("HOME"), "deno": os.environ.get("DENO_DIR"), "todo": todo}) + "\\n")
if args and args[0] == "--version":
    print("4.6.0")
    sys.exit(0)
"""

    OK = """
print("Processing query: " + args[1], flush=True)
print("Found 3 songs in Lista de prueba (Playlist)", flush=True)
for i in range(1, 4):
    print('Downloaded "Artista - Canción %d": https://youtube.example/%d' % (i, i), flush=True)
    print("%d/3 complete" % i, flush=True)
"""

    TOKEN = "token-de-prueba"

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.data = os.path.join(self.tmp.name, "data")
        self.bin = os.path.join(self.tmp.name, "fake-spotdl")
        self.log = os.path.join(self.tmp.name, "calls.jsonl")
        os.environ["FAKE_LOG"] = self.log
        self.addCleanup(os.environ.pop, "FAKE_LOG", None)
        self.script(self.OK)
        self.settings = server.Settings(data_dir=self.data, spotdl_bin=self.bin, threads=2, max_active=1, token=self.TOKEN)
        self.service = server.Service(self.settings)
        self.service.start()
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), server.make_handler(self.service))
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        self.addCleanup(self.teardown)

    def teardown(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.service.stop()

    def script(self, cuerpo):
        # Se escribe aparte y se renombra: el archivo anterior puede seguir en ejecución (un trabajo recién
        # cancelado que aún no muere) y Linux no deja abrirlo para escribir («Text file busy»).
        tmp = self.bin + ".nuevo"
        with open(tmp, "w", encoding="utf-8") as f:
            f.write(self.CABECERA + cuerpo)
        os.chmod(tmp, 0o755)
        os.replace(tmp, self.bin)

    def request(self, method, path, body=None, timeout=10, user="ana", admin=False, token=None):
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Content-Type": "application/json", "Authorization": "Bearer " + (self.TOKEN if token is None else token)}
        if user:
            headers["X-PaloBox-User"] = user
        if admin:
            headers["X-PaloBox-Admin"] = "1"
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as res:
                return res.status, json.loads(res.read() or b"{}")
        except urllib.error.HTTPError as exc:
            with exc:
                return exc.code, json.loads(exc.read() or b"{}")

    def esperar(self, jid, estados=("finished", "error"), timeout=10, intentos=None, user="ana"):
        fin = time.time() + timeout
        while time.time() < fin:
            _, jobs = self.request("GET", "/jobs", user=user)
            for job in jobs:
                if job["id"] == jid and job["status"] in estados and intentos in (None, job["attempts"]):
                    return job
            time.sleep(0.05)
        self.fail(f"el trabajo {jid} no llegó a {estados}")

    def reiniciar(self, settings=None):
        """Como un despliegue: apaga el servicio y arranca otro sobre los mismos datos."""
        self.teardown()
        self.service = server.Service(settings or self.settings)
        self.service.start()
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), server.make_handler(self.service))
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def llamadas(self):
        try:
            with open(self.log, encoding="utf-8") as f:
                return [json.loads(l) for l in f if l.strip()]
        except FileNotFoundError:
            return []

    def test_salud_lista_alta_progreso_y_fin(self):
        code, salud = self.request("GET", "/health")
        self.assertEqual(code, 200)
        self.assertTrue(salud["ok"])
        self.assertEqual(salud["spotdl"], "4.6.0")

        code, job = self.request("POST", "/jobs", {"url": "https://open.spotify.com/intl-es/playlist/37i9dQZF1DXcBWIGoYBM5M?si=x"})
        self.assertEqual(code, 201)
        self.assertEqual(job["url"], "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M")

        # Éxito: deja de ser una fila y queda solo el aviso, para su dueña y hasta que lo vea.
        fin = self.esperar(job["id"])
        self.assertEqual(fin["status"], "finished")
        self.assertEqual((fin["title"], fin["total"], fin["done"], fin["progress"]), ("Lista de prueba", 3, 3, 1.0))
        self.assertEqual((fin["error"], fin["notice"]), ("", "«Lista de prueba» ya está en tu biblioteca."))
        self.assertEqual(self.request("GET", "/jobs", user="pepe")[1], [])
        self.assertEqual(self.request("GET", "/jobs", user="pepe", admin=True)[1], [])  # el aviso es de ana
        self.assertEqual(self.request("DELETE", "/jobs/" + job["id"])[0], 200)  # la app lo mostró
        self.assertEqual(self.request("GET", "/jobs")[1], [])

        llamadas = [c for c in self.llamadas() if c["args"][0] == "download"]
        self.assertEqual(len(llamadas), 1)
        llamada = llamadas[0]
        args = llamada["args"]
        self.assertEqual(args[0], "download")
        self.assertIn("--overwrite", args)
        self.assertEqual(args[args.index("--overwrite") + 1], "skip")
        self.assertEqual(args[args.index("--format") + 1], "m4a")
        self.assertEqual(args[args.index("--bitrate") + 1], "disable")
        self.assertNotIn("--ffmpeg-args", args)  # remuestrear obligaría a recodificar
        self.assertEqual(args[args.index("--threads") + 1], "2")
        self.assertIn("{artist}/{album}/{title}.{output-ext}", args[args.index("--output") + 1])
        self.assertTrue(llamada["home"].startswith(self.settings.state_dir))
        self.assertEqual(llamada["deno"], os.path.join(self.settings.state_dir, "deno"))
        self.assertTrue(os.path.isdir(self.settings.music_dir))

    def test_rechaza_enlaces_y_quita_terminados(self):
        for url in ("", "https://evil.example/track/4uLU6hMCjMI75M1A2tKUQC", "texto"):
            code, body = self.request("POST", "/jobs", {"url": url})
            self.assertEqual(code, 400, url)
            self.assertIn("no sirve", body["error"])
        self.assertEqual(self.request("POST", "/jobs", {})[0], 400)
        self.assertEqual(self.request("DELETE", "/jobs/" + "0" * 16)[0], 404)
        self.assertEqual(self.request("DELETE", "/jobs/no-es-id")[0], 404)

    def test_duplicado_devuelve_el_mismo(self):
        self.script(self.OK + "\ntime.sleep(1)\n")
        url = "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"
        _, uno = self.request("POST", "/jobs", {"url": url})
        _, dos = self.request("POST", "/jobs", {"url": url})
        self.assertEqual(uno["id"], dos["id"])
        self.esperar(uno["id"])

    def test_el_mismo_enlace_de_dos_usuarios_son_dos_trabajos(self):
        self.service.settings = dataclasses.replace(self.settings, max_active=5)
        url = "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"
        _, ana = self.request("POST", "/jobs", {"url": url})
        code, pepe = self.request("POST", "/jobs", {"url": url}, user="pepe")
        self.assertEqual((code, pepe["owner"]), (201, "pepe"))
        self.assertNotEqual(ana["id"], pepe["id"])
        # Cada quien recibe su aviso (y con él su playlist), no solo el primero que la pidió.
        self.esperar(ana["id"], ("finished",))
        self.esperar(pepe["id"], ("finished",), user="pepe")

    def test_error_de_spotdl(self):
        self.script(
            "\nprint('Found 1 songs in Falla (Album)', flush=True)\n"
            "print('No se pudo descargar', file=sys.stderr, flush=True)\n"
            "sys.exit(3)\n"
        )
        _, job = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"})
        # Un fallo de toda la ejecución se reintenta solo una vez y luego queda para el usuario.
        fin = self.esperar(job["id"], ("error",), intentos=2)
        self.assertEqual(fin["error"], "No se pudo completar la descarga.")
        # Falla el plan (save): ni se intenta la descarga, y se reintenta el enlace entero.
        self.assertEqual([(c["args"][0], c["args"][1]) for c in self.llamadas()], [("save", job["url"])] * 2)

    # Una playlist de cinco: cfg dice qué canciones fallan y con qué error de spotDL 4.6.
    # Con save escribe el plan (cfg['plan'] o las cinco canciones) como lo hace spotDL.
    LISTA = """
cfg = json.load(open(os.environ['FAKE_LOG'] + '.cfg'))
if args[0] == 'save':
    plan = cfg.get('plan') or [{'url': u, 'name': 'Canción ' + u[-2:], 'artist': 'Artista', 'artists': ['Artista'],
                                'album_name': 'Tomá', 'duration': 200, 'tracks_count': 5} for u in cfg['tracks']]
    if '/track/' not in args[1]:
        print('Found %d songs in Tomá (Playlist)' % len(plan), flush=True)
    plan = plan[:cfg.get('take')]  # spotDL descarta sin avisar lo que no pudo resolver
    json.dump(plan, open(args[args.index('--save-file') + 1], 'w'))
    sys.exit(cfg.get('save_rc', 0))
qs = args[1:args.index('--output')]
err, save = args[args.index('--save-errors') + 1], args[args.index('--save-file') + 1]
songs = []
for q in qs:
    if q.endswith('.spotdl'):
        songs += [s['url'] for s in json.load(open(q))]
    elif '/track/' in q:
        songs.append(q)
    else:
        songs += cfg['tracks']
        print('Found %d songs in Tomá (Playlist)' % len(cfg['tracks']), flush=True)
lines = []
for i, u in enumerate(songs, 1):
    if u in cfg['fail']:
        lines.append(u + ' - ' + cfg['fail'][u])
    else:
        print('Downloaded "Artista - Canción %s": https://youtube.example/x' % u[-2:], flush=True)
    print('%d/%d complete' % (i, len(songs)), flush=True)
json.dump([{'url': u, 'name': 'Canción ' + u[-2:], 'artist': 'Artista'} for u in songs], open(save, 'w'))
if lines:
    open(err, 'a').write('2026-11-17-10-00-00\\n' + '\\n'.join(lines) + '\\n')
"""
    PLAYLIST = "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"
    TRACKS = [f"https://open.spotify.com/track/{'T' * 20}{i:02d}" for i in range(1, 6)]
    YTDLP = "AudioProviderError: YT-DLP download error - https://music.youtube.com/watch?v=abc"
    SIN_RESULTADOS = "LookupError: No results found for song: Artista - Canción"

    def lista(self, fail, **cfg):
        with open(self.log + ".cfg", "w", encoding="utf-8") as f:
            json.dump({"tracks": self.TRACKS, "fail": fail, **cfg}, f)
        self.script(self.LISTA)

    def pedidas(self):
        """Lo que se le pidió a spotDL en cada descarga (las URLs del .spotdl, si fue uno)."""
        return [c["todo"] or c["args"][1:c["args"].index("--output")] for c in self.llamadas() if c["args"][0] == "download"]

    def test_playlist_terminada_trae_sus_canciones_en_orden(self):
        for n in ("03", "01"):  # lo que quedó en disco (el spotDL de mentira no escribe archivos)
            ruta = os.path.join(self.settings.music_dir, "Artista", "Tomá", f"Canción {n}.m4a")
            os.makedirs(os.path.dirname(ruta), exist_ok=True)
            open(ruta, "wb").close()
        self.lista({})
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        fin = self.esperar(job["id"], ("finished",))
        self.assertEqual(fin["tracks"], ["Artista/Tomá/Canción 01.m4a", "Artista/Tomá/Canción 03.m4a"])
        self.assertIn("Quedó como playlist en Tu biblioteca.", fin["notice"])

    def test_plan_en_el_orden_de_la_playlist(self):
        plan = [{"url": u, "name": f"Canción {i}", "artist": "Artista", "list_position": i} for i, u in enumerate(self.TRACKS, 1)]
        self.lista({}, plan=plan[::-1])
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        self.esperar(job["id"], ("finished",))
        self.assertEqual([s[1] for s in self.service.store.songs(job["id"])], [f"Canción {i}" for i in range(1, 6)])

    def test_plan_incompleto_cuenta_las_que_faltan(self):
        self.lista({}, take=3)
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        fin = self.esperar(job["id"], ("error",), intentos=2)
        self.assertEqual(fin["error"], "2 canciones no se pudieron descargar")
        # Sin URL de las que faltan, el reintento repite el enlace entero (y vuelve a planear).
        self.assertEqual([c["args"][0] for c in self.llamadas()], ["save", "download"] * 2)

    def test_plan_vacio_no_es_exito(self):
        self.lista({}, take=0)
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        fin = self.esperar(job["id"], ("error",), intentos=2)
        self.assertEqual(fin["error"], "5 canciones no se pudieron descargar")
        self.assertEqual([c["args"][0] for c in self.llamadas()], ["save", "save"])

    def test_cancion_suelta_sin_plan_no_es_exito(self):
        # Una canción suelta no anuncia «Found N songs»: sin esa cuenta, el plan vacío también es fallo.
        self.lista({}, take=0)
        _, job = self.request("POST", "/jobs", {"url": "https://open.spotify.com/track/" + "4" * 22})
        fin = self.esperar(job["id"], ("error",), intentos=2)
        self.assertEqual([c["args"][0] for c in self.llamadas()], ["save", "save"])
        self.assertNotIn("ya está", fin.get("notice") or "")

    def test_un_fallo_inesperado_no_detiene_la_cola(self):
        self.service.settings = dataclasses.replace(self.settings, max_active=5)
        real, fallas = self.service.store.progress, []

        def progress(jid, feed):
            if not fallas:
                fallas.append(jid)
                raise sqlite3.OperationalError("database or disk is full")
            return real(jid, feed)

        with mock.patch.object(self.service.store, "progress", side_effect=progress):
            _, uno = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"})
            _, dos = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/" + "2" * 22})
            self.assertEqual(self.esperar(dos["id"], ("finished",))["status"], "finished")
            self.esperar(uno["id"], ("finished",))  # se marcó como error y el reintento automático lo terminó
        self.assertEqual(fallas, [uno["id"]])
        self.assertEqual(self.service.runner.procs, {})
        self.assertEqual(self.request("GET", "/health")[0], 200)
        self.service.stop()
        self.service.runner.thread.join(timeout=10)
        self.assertEqual(self.request("GET", "/health")[0], 503)  # sin trabajador no está sano

    def test_un_error_al_leer_la_cola_no_mata_al_trabajador(self):
        real, fallas = self.service.store.next_queued, []

        def next_queued():
            if not fallas:
                fallas.append(1)
                raise sqlite3.OperationalError("database is locked")
            return real()

        with mock.patch.object(self.service.store, "next_queued", side_effect=next_queued):
            fin = time.time() + 10
            while time.time() < fin and not fallas:
                self.service.runner.wake()
                time.sleep(0.05)
            self.assertEqual(fallas, [1])
            _, job = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"})
            self.assertEqual(self.esperar(job["id"])["status"], "finished")
        self.assertTrue(self.service.runner.thread.is_alive())

    def test_main_espera_al_trabajador_al_apagar(self):
        """Sin esperarlo, su limpieza (drop_partial) perdía la carrera contra la salida del proceso."""
        self.teardown()
        self.script("\ntime.sleep(60)\n")
        job = self.service.store.create("https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX", "ana")
        servicios, senales = [], {}
        real = server.Service

        def apagar():  # el SIGTERM de Docker, cuando el trabajo ya está corriendo
            fin = time.time() + 10
            while time.time() < fin and not (servicios and senales and servicios[0].store.status(job["id"]) == "downloading"):
                time.sleep(0.05)
            senales[server.signal.SIGTERM](server.signal.SIGTERM, None)

        with mock.patch.object(server.Settings, "from_env", return_value=dataclasses.replace(self.settings, port=0)), \
                mock.patch.object(server, "Service", side_effect=lambda s: servicios.append(real(s)) or servicios[0]), \
                mock.patch.object(server.signal, "signal", side_effect=senales.__setitem__), \
                mock.patch.object(server.logging, "basicConfig"), \
                mock.patch.object(server, "drop_partial", side_effect=lambda *a: time.sleep(0.5)):  # recorrer la biblioteca tarda
            threading.Thread(target=apagar, daemon=True).start()
            server.main()
        self.assertFalse(servicios[0].runner.thread.is_alive())
        self.assertEqual(servicios[0].store.status(job["id"]), "downloading")  # queda para reanudar

    def test_doble_clic_crea_un_solo_trabajo(self):
        self.service.settings = dataclasses.replace(self.settings, max_active=5)
        self.script("\ntime.sleep(60)\n")
        real = self.service.store.find_active

        def lenta(url, owner):
            job = real(url, owner)
            time.sleep(0.2)  # sin el candado, los dos pedidos ven la cola vacía
            return job

        url = "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"
        got = []
        with mock.patch.object(self.service.store, "find_active", side_effect=lenta):
            hilos = [threading.Thread(target=lambda: got.append(self.request("POST", "/jobs", {"url": url})[1]["id"])) for _ in range(2)]
            for h in hilos:
                h.start()
            for h in hilos:
                h.join()
        self.assertEqual(len(set(got)), 1)
        self.assertEqual(len(self.service.store.jobs()), 1)
        self.service.cancel(got[0])

    def test_arranque_borra_temporales(self):
        nombres = ["errors-a.txt", "songs-a.spotdl", "plan-a.spotdl", "to-download-a.spotdl", "jobs.sqlite"]
        for n in nombres[:-1]:
            open(os.path.join(self.settings.state_dir, n), "w").close()
        cfg = self.settings.spotdl_config_dir()  # spotDL deja ahí audio crudo (temp) y errores de ffmpeg (errors)
        for sub in ("temp", "errors"):
            os.makedirs(os.path.join(cfg, sub))
            open(os.path.join(cfg, sub, "resto"), "w").close()
        self.reiniciar()
        self.assertEqual([n for n in nombres if os.path.exists(os.path.join(self.settings.state_dir, n))], ["jobs.sqlite"])
        self.assertEqual([s for s in ("temp", "errors") if os.path.exists(os.path.join(cfg, s))], [])

    def test_fallo_transitorio_reintenta_solo_las_fallidas_y_luego_se_agota(self):
        t = self.TRACKS
        self.lista({t[0]: self.YTDLP, t[1]: self.YTDLP, t[2]: self.SIN_RESULTADOS})
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        fin = self.esperar(job["id"], ("error",), intentos=2)
        # El automático ya pidió solo las dos transitorias; la permanente no se vuelve a pedir.
        self.assertEqual(self.pedidas(), [t, [t[0], t[1]]])
        self.assertEqual([c["args"][0] for c in self.llamadas()], ["save", "download", "download"])  # el reintento no planea
        self.assertEqual(fin["title"], "Tomá")
        self.assertEqual(fin["error"], "3 canciones no se pudieron descargar: Canción 03 — Artista, Canción 01 — Artista, Canción 02 — Artista")
        self.assertNotIn("http", json.dumps({k: v for k, v in fin.items() if k != "url"}))
        self.assertEqual(fin["notice"], "")

        # El usuario reintenta: ahora una cede y la otra sigue fallando; con tres intentos se acaba.
        self.lista({t[1]: self.YTDLP, t[2]: self.SIN_RESULTADOS})
        code, body = self.request("POST", f"/jobs/{job['id']}/retry")
        self.assertEqual((code, body["status"], body["attempts"]), (200, "queued", 3))
        fin = self.esperar(job["id"], ("finished",))
        self.assertEqual(self.pedidas()[-1], [t[0], t[1]])
        self.assertEqual(fin["notice"], "2 canciones de «Tomá» no se pudieron descargar: Canción 03 — Artista, Canción 02 — Artista.")
        self.assertEqual(self.request("POST", f"/jobs/{job['id']}/retry")[0], 409)

    def temporales(self):
        return sorted(f for f in os.listdir(self.settings.state_dir) if f.endswith((".spotdl", ".txt")))

    def test_salta_lo_que_ya_esta_en_la_biblioteca(self):
        t = self.TRACKS
        self.lista({})
        tags = biblioteca(self.settings.music_dir, [("Artista", "Tomá", "Canción 02", 201, 5), ("Artista", "Tomá", "Canción 04", 199, 5)])
        with mock.patch("library.read_tags", side_effect=tags.__getitem__):
            _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
            fin = self.esperar(job["id"], ("finished",))
        self.assertEqual(self.pedidas(), [[t[0], t[2], t[4]]])
        self.assertEqual(fin["notice"], "«Tomá» ya está en tu biblioteca. 2 canciones ya estaban en la biblioteca. Quedó como playlist en Tu biblioteca.")
        self.assertEqual(self.temporales(), [])

    def test_todo_ya_estaba_no_descarga(self):
        self.lista({})
        tags = biblioteca(self.settings.music_dir, [("Artista", "Tomá", f"Canción {u[-2:]}", 200, 5) for u in self.TRACKS])
        with mock.patch("library.read_tags", side_effect=tags.__getitem__):
            _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
            fin = self.esperar(job["id"])
        self.assertEqual([c["args"][0] for c in self.llamadas()], ["save"])
        self.assertEqual((fin["status"], fin["error"]), ("finished", ""))
        self.assertEqual(self.service.store.failed(job["id"]), [])
        self.assertEqual(fin["notice"], "«Tomá» ya está en tu biblioteca. 5 canciones ya estaban en la biblioteca. Quedó como playlist en Tu biblioteca.")
        self.assertEqual(self.temporales(), [])

    def test_cancion_suelta_ya_estaba(self):
        u = self.TRACKS[0]
        self.lista({}, plan=[{"url": u, "name": "Nada", "artist": "Penyair", "artists": ["Penyair"], "duration": 180, "tracks_count": 1}])
        tags = biblioteca(self.settings.music_dir, [("Penyair", "La Ley del Todo", "Nada", 180, 12)])
        with mock.patch("library.read_tags", side_effect=tags.__getitem__):
            _, job = self.request("POST", "/jobs", {"url": u})
            fin = self.esperar(job["id"], ("finished",))
        self.assertEqual(fin["notice"], "«Penyair - Nada» ya está en tu biblioteca. 1 canción ya estaba en la biblioteca.")

    def test_si_el_filtro_falla_baja_el_enlace_entero(self):
        self.lista({})
        with mock.patch("server.filter_plan", side_effect=RuntimeError("inesperado")):
            _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
            fin = self.esperar(job["id"], ("finished",))
        self.assertEqual(self.pedidas(), [[self.PLAYLIST]])
        self.assertEqual(fin["notice"], "«Tomá» ya está en tu biblioteca.")
        self.assertEqual(self.temporales(), [])

    def test_reintento_muestra_avance(self):
        self.lista({self.TRACKS[0]: self.YTDLP})
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        self.esperar(job["id"], ("error",), intentos=2)
        self.lista({})
        self.script(self.LISTA.replace("    print('%d/%d complete'", "    time.sleep(1)\n    print('%d/%d complete'"))
        self.assertEqual(self.request("POST", f"/jobs/{job['id']}/retry")[0], 200)
        fin = time.time() + 10
        while time.time() < fin and self.service.store.get(job["id"])["total"] != 1:
            time.sleep(0.05)
        en_curso = self.service.store.get(job["id"])
        self.assertEqual((en_curso["status"], en_curso["title"], en_curso["total"], en_curso["error"]), ("downloading", "Tomá", 1, ""))
        self.assertEqual(self.esperar(job["id"], ("finished",))["notice"], "«Tomá» ya está en tu biblioteca.")

    def test_error_permanente_termina_con_aviso(self):
        t = self.TRACKS
        self.lista({t[3]: self.SIN_RESULTADOS, t[4]: "AudioProviderError: No metadata found for the provided url x"})
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        fin = self.esperar(job["id"], ("finished",))
        self.assertEqual(len(self.pedidas()), 1)  # nada que reintentar
        self.assertEqual(fin["notice"], "2 canciones de «Tomá» no están disponibles: Canción 04 — Artista, Canción 05 — Artista.")

    def test_reintento_ajeno(self):
        self.lista({self.TRACKS[0]: self.YTDLP})
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        self.esperar(job["id"], ("error",), intentos=2)
        code, body = self.request("POST", f"/jobs/{job['id']}/retry", user="pepe")
        self.assertEqual(code, 403)
        self.assertIn("Solo quien", body["error"])
        self.assertEqual(self.service.store.get(job["id"])["status"], "error")
        self.assertEqual(self.request("POST", "/jobs/" + "0" * 16 + "/retry")[0], 404)
        self.assertEqual(self.request("POST", f"/jobs/{job['id']}/retry", user="")[0], 400)
        self.assertEqual(self.request("POST", f"/jobs/{job['id']}/retry", token="otro")[0], 401)
        self.assertEqual(self.request("POST", f"/jobs/{job['id']}/retry", user="pepe", admin=True)[0], 200)

    def test_apagar_no_marca_error_y_deja_el_trabajo_para_reanudar(self):
        self.script(
            "\nprint('Found 2 songs in Lenta (Playlist)', flush=True)\n"
            "time.sleep(60)\n"
        )
        _, job = self.request("POST", "/jobs", {"url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"})
        fin = time.time() + 10
        while time.time() < fin:
            actual = self.service.store.get(job["id"])
            if actual and actual["status"] == "downloading":
                break
            time.sleep(0.05)

        self.service.stop()  # como un reinicio del contenedor
        self.service.runner.thread.join(timeout=10)
        self.assertFalse(self.service.runner.thread.is_alive())

        actual = self.service.store.get(job["id"])
        self.assertEqual(actual["status"], "downloading")  # no se convirtió en error
        self.service.store.resume()
        self.assertEqual(self.service.store.get(job["id"])["status"], "queued")

    def test_cancela_el_grupo_y_libera_la_cola(self):
        self.script(
            "\nprint('Found 2 songs in Lenta (Playlist)', flush=True)\n"
            "hijo = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)'])\n"
            "open(os.environ['FAKE_LOG'] + '.pid', 'w').write(str(hijo.pid))\n"
            "time.sleep(60)\n"
        )
        _, uno = self.request("POST", "/jobs", {"url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"})
        _, dos = self.request("POST", "/jobs", {"url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M2"})
        self.assertEqual(dos, {"error": "Hay demasiadas descargas en cola."})

        fin = time.time() + 10
        pid = 0
        while time.time() < fin and not pid:
            try:
                with open(self.log + ".pid") as f:
                    pid = int(f.read())
            except (FileNotFoundError, ValueError):
                time.sleep(0.05)
        self.assertGreater(pid, 0, "el spotDL falso debía arrancar")

        code, body = self.request("DELETE", "/jobs/" + uno["id"])
        self.assertEqual((code, body), (200, {"ok": True}))
        _, jobs = self.request("GET", "/jobs")
        self.assertEqual(jobs, [])

        fin = time.time() + 10
        while time.time() < fin:
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                break
            time.sleep(0.05)
        else:
            self.fail("el proceso hijo de spotDL quedó vivo")


    def test_sin_token_no_hay_servicio(self):
        for token in ("", "otro-token", self.TOKEN + "x"):
            self.assertEqual(self.request("GET", "/jobs", token=token)[0], 401, token)
            self.assertEqual(self.request("GET", "/health", token=token)[0], 401, token)
            self.assertEqual(self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"}, token=token)[0], 401)
            self.assertEqual(self.request("DELETE", "/jobs/" + "0" * 16, token=token)[0], 401)
        self.assertEqual(self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"}, user="")[0], 400)
        self.assertEqual(self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"}, user="../x")[0], 400)
        self.assertEqual(self.service.store.jobs(), [])

    def test_solo_el_dueno_o_el_admin_quitan_un_trabajo(self):
        self.script("\ntime.sleep(60)\n")
        _, job = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"})
        self.assertEqual(job["owner"], "ana")
        code, body = self.request("DELETE", "/jobs/" + job["id"], user="pepe")
        self.assertEqual(code, 403)
        self.assertIn("Solo quien", body["error"])
        self.assertIsNotNone(self.service.store.get(job["id"]))
        self.assertEqual(self.request("DELETE", "/jobs/" + job["id"], user="pepe", admin=True)[0], 200)

        _, otro = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"})
        self.assertEqual(self.request("DELETE", "/jobs/" + otro["id"])[0], 200)  # ana, su dueña
        self.assertEqual(self.service.store.jobs(), [])

    def test_limite_por_usuario(self):
        self.service.settings = dataclasses.replace(self.settings, max_active=5, max_per_user=2)
        self.script("\ntime.sleep(60)\n")
        for i in range(2):
            self.assertEqual(self.request("POST", "/jobs", {"url": f"https://open.spotify.com/album/{i:022d}"})[0], 201)
        code, body = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/" + "9" * 22})
        self.assertEqual(code, 429)
        self.assertIn("Ya tienes 2", body["error"])
        # Otro usuario todavía puede pedir; repetir un enlace en cola no cuenta como nuevo.
        self.assertEqual(self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/" + "9" * 22}, user="pepe")[0], 201)
        self.assertEqual(self.request("POST", "/jobs", {"url": f"https://open.spotify.com/album/{0:022d}"})[0], 200)
        # Las canciones sueltas van por su propio tope: no las frena el de álbumes ni lo gastan.
        self.service.settings = dataclasses.replace(self.service.settings, max_tracks=3)
        for i in range(3):
            self.assertEqual(self.request("POST", "/jobs", {"url": f"https://open.spotify.com/track/{i:022d}"})[0], 201)
        code, body = self.request("POST", "/jobs", {"url": "https://open.spotify.com/track/" + "9" * 22})
        self.assertEqual(code, 429)
        self.assertIn("Ya tienes 3 canciones", body["error"])
        for j in self.service.store.jobs():
            self.service.cancel(j["id"])

    def test_content_length_invalido(self):
        import http.client
        c = http.client.HTTPConnection("127.0.0.1", self.httpd.server_address[1], timeout=5)
        c.putrequest("POST", "/jobs")
        c.putheader("Authorization", "Bearer " + self.TOKEN)
        c.putheader("X-PaloBox-User", "ana")
        c.putheader("Content-Length", "abc")
        c.endheaders()
        self.assertEqual(c.getresponse().status, 400)
        c.close()

    def test_credenciales_fuera_de_la_linea_de_comandos(self):
        self.teardown()
        settings = dataclasses.replace(self.settings, client_id="id-publico", client_secret="secreto-123")
        os.environ["SPOTIFY_CLIENT_SECRET"] = "secreto-123"
        self.addCleanup(os.environ.pop, "SPOTIFY_CLIENT_SECRET", None)
        self.service = server.Service(settings)
        self.service.start()
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), server.make_handler(self.service))
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        self.script(self.OK + "\nopen(os.environ['FAKE_LOG'] + '.env', 'w').write(str('secreto-123' in ''.join(os.environ.values())))\n")

        cfg = os.path.join(settings.home, ".config", "spotdl", "config.json")
        self.assertEqual(os.stat(cfg).st_mode & 0o777, 0o600)
        with open(cfg, encoding="utf-8") as f:
            self.assertEqual(json.load(f), {"load_config": True, "client_id": "id-publico", "client_secret": "secreto-123"})

        _, job = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"})
        self.esperar(job["id"])
        for args in (c["args"] for c in self.llamadas()):
            self.assertNotIn("secreto-123", " ".join(args))
            self.assertNotIn("--client-secret", args)
        for cmd in ("save", "download"):
            args = [c for c in self.llamadas() if c["args"][0] == cmd][0]["args"]
            self.assertTrue(args[args.index("--lyrics") + 1].startswith("--"), cmd)  # sin proveedores: no busca letras
        with open(self.log + ".env") as f:
            self.assertEqual(f.read(), "False")  # tampoco en el entorno de spotDL

        # Sin credenciales, el archivo desaparece al arrancar.
        dataclasses.replace(settings, client_id="", client_secret="").prepare()
        self.assertFalse(os.path.exists(cfg))

    def test_tiempo_maximo_aunque_spotdl_imprima_sin_parar(self):
        # Antes el reloj solo se miraba tras un segundo de silencio: esto nunca se cortaba.
        self.service.runner.s = dataclasses.replace(self.settings, job_timeout=1)
        self.script("\nwhile True:\n    print('Reintentando...', flush=True)\n    time.sleep(0.05)\n")
        _, job = self.request("POST", "/jobs", {"url": "https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX"})
        fin = self.esperar(job["id"], ("error",), timeout=15, intentos=2)
        self.assertEqual(fin["error"], "No se pudo completar la descarga.")  # el detalle queda en el log


if __name__ == "__main__":
    unittest.main()
