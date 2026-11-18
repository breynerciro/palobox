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
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

import server


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


class FeedTest(unittest.TestCase):
    def test_playlist(self):
        f = server.Feed()
        for line in (
            "Processing query: https://open.spotify.com/playlist/x",
            "Found 10 songs in Mix para el carro (Playlist)",
            'Downloaded "Daft Punk - One More Time": https://youtube.example/1',
            "1/10 complete",
            "2/10 complete",
        ):
            f.line(line)
        self.assertEqual((f.title, f.total, f.done), ("Mix para el carro", 10, 2))
        self.assertEqual(f.current, "Daft Punk - One More Time")

    def test_cancion_suelta_sin_lista(self):
        f = server.Feed()
        f.line('Downloaded "Daft Punk - One More Time": https://youtube.example/1')
        f.line("1/1 complete")
        self.assertEqual((f.title, f.total, f.done), ("Daft Punk - One More Time", 1, 1))

    def test_ignora_basura_y_ansi(self):
        f = server.Feed()
        self.assertFalse(f.line(""))
        self.assertFalse(f.line("\x1b[2mDescargando audio…\x1b[0m"))
        self.assertTrue(f.line("Found 2 songs in Álbum ñ (Album)"))
        self.assertEqual(f.title, "Álbum ñ")


class StoreTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.store = server.Store(os.path.join(self.tmp.name, "jobs.sqlite"), keep_finished=2)

    def test_ciclo(self):
        job = self.store.create("https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX")
        self.assertEqual(job["status"], "queued")
        self.assertEqual(job["total"], 0)
        self.assertIsNone(job["progress"])
        self.assertEqual(self.store.active_count(), 1)
        self.assertIsNotNone(self.store.find_active(job["url"]))

        self.assertTrue(self.store.claim(job["id"]))
        self.assertFalse(self.store.claim(job["id"]))  # ya no está en cola
        feed = server.Feed()
        feed.line("Found 2 songs in Disco (Album)")
        feed.line("1/2 complete")
        self.store.progress(job["id"], feed)
        actual = self.store.get(job["id"])
        self.assertEqual((actual["status"], actual["title"], actual["done"]), ("downloading", "Disco", 1))
        self.assertEqual(actual["progress"], 0.5)

        self.store.finish(job["id"], "done")
        actual = self.store.get(job["id"])
        self.assertEqual((actual["status"], actual["progress"], actual["error"]), ("done", 0.5, ""))
        self.assertEqual(self.store.active_count(), 0)

    def test_track_nace_con_total_uno_y_conserva_progreso(self):
        job = self.store.create("https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC")
        self.assertEqual(job["total"], 1)
        self.assertEqual(job["progress"], 0.0)
        self.store.claim(job["id"])
        self.store.progress(job["id"], server.Feed())  # aún sin datos: no borra el total
        self.assertEqual(self.store.get(job["id"])["total"], 1)

    def test_reanuda_y_conserva_lo_terminado(self):
        uno = self.store.create("https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M")
        dos = self.store.create("https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX")
        self.store.claim(uno["id"])
        self.store.progress(uno["id"], server.Feed())
        self.store.resume()
        for jid in (uno["id"], dos["id"]):
            job = self.store.get(jid)
            self.assertEqual((job["status"], job["done"], job["total"], job["current"]), ("queued", 0, 0, ""))

    def test_conserva_solo_los_ultimos_terminados(self):
        ids = [self.store.create(f"https://open.spotify.com/playlist/{i:022d}")["id"] for i in range(4)]
        for jid in ids:
            self.store.claim(jid)
            self.store.finish(jid, "done")
        quedan = [j["id"] for j in self.store.jobs()]
        self.assertEqual(len(quedan), 2)
        self.assertEqual(set(quedan), set(ids[-2:]))

    def test_errores_del_archivo(self):
        path = os.path.join(self.tmp.name, "errores.txt")
        self.assertEqual(server.failures_from(path), [])
        a, b = "https://open.spotify.com/track/" + "a" * 22, "https://open.spotify.com/track/" + "b" * 22
        with open(path, "w", encoding="utf-8") as f:
            f.write(f"2026-11-17-10-00-00\n{a} - AudioProviderError: YT-DLP download error - https://music.youtube.com/watch?v=x\n"
                    f"2026-11-17-11-00-00\n{b} - LookupError: No results found for song: Artista - B\n"
                    "Error occurred while reinitializing song: 429\n")
        got = server.failures_from(path, {a: "A — Artista"})
        self.assertEqual(got, [{"url": a, "name": "A — Artista", "retry": True},
                               {"url": b, "name": "", "retry": False},
                               {"url": "", "name": "", "retry": False}])
        self.assertEqual(server.row_text(got), "3 canciones no se pudieron descargar: A — Artista y 2 más")
        self.assertEqual(server.row_text(got[:1]), "1 canción no se pudo descargar: A — Artista")
        self.assertEqual(server.row_text([]), "No se pudo completar la descarga.")

    def test_reglas_de_resultado(self):
        url = "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"
        trans = {"url": "u", "name": "A — B", "retry": True}
        perm = {"url": "v", "name": "C — D", "retry": False}
        self.assertEqual(server.outcome("Tomá", url, [], False, 1, 3), ("finished", "", "«Tomá» ya está en tu biblioteca."))
        self.assertEqual(server.outcome("Tomá", url, [trans, perm], False, 2, 3)[0], "error")
        self.assertEqual(server.outcome("Tomá", url, [], True, 2, 3)[0], "error")
        self.assertEqual(server.outcome("Tomá", url, [trans, perm], False, 3, 3),
                         ("finished", "2 canciones no se pudieron descargar: A — B, C — D", "2 canciones de «Tomá» no se pudieron descargar: A — B, C — D."))
        self.assertEqual(server.outcome("Tomá", url, [perm], False, 1, 3),
                         ("finished", "1 canción no se pudo descargar: C — D", "1 canción de «Tomá» no está disponible: C — D."))
        self.assertEqual(server.outcome("", "https://open.spotify.com/track/x", [perm], False, 1, 3)[::2],
                         ("finished", "«C — D» no está disponible."))
        self.assertIn("No se pudo descargar «Tomá»", server.outcome("Tomá", url, [], True, 3, 3)[2])


class ServiceTest(unittest.TestCase):
    """Arranca el servicio real y un spotDL falso que solo imprime y escribe lo pedido."""

    CABECERA = """#!/usr/bin/env python3
import json, os, subprocess, sys, time
args = sys.argv[1:]
if os.environ.get("FAKE_LOG"):
    with open(os.environ["FAKE_LOG"], "a") as f:
        f.write(json.dumps({"args": args, "home": os.environ.get("HOME"), "deno": os.environ.get("DENO_DIR")}) + "\\n")
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
        with open(self.bin, "w", encoding="utf-8") as f:
            f.write(self.CABECERA + cuerpo)
        os.chmod(self.bin, 0o755)

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

    def esperar(self, jid, estados=("finished", "error"), timeout=10, intentos=None):
        fin = time.time() + timeout
        while time.time() < fin:
            _, jobs = self.request("GET", "/jobs")
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
        self.assertEqual(args[args.index("--format") + 1], "mp3")
        self.assertEqual(args[args.index("--bitrate") + 1], "128k")
        self.assertEqual(args[args.index("--threads") + 1], "2")
        self.assertIn("{artist}/{album}/{title}.{output-ext}", args[args.index("--output") + 1])
        self.assertTrue(llamada["home"].startswith(self.settings.state_dir))
        self.assertEqual(llamada["deno"], os.path.join(self.settings.state_dir, "deno"))
        self.assertTrue(os.path.isdir(self.settings.music_dir))

    def test_rechaza_enlaces_y_quita_terminados(self):
        for url in ("", "https://evil.example/track/4uLU6hMCjMI75M1A2tKUQC", "texto"):
            code, body = self.request("POST", "/jobs", {"url": url})
            self.assertEqual(code, 400, url)
            self.assertIn("Spotify", body["error"])
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
        descargas = [c["args"] for c in self.llamadas() if c["args"][0] == "download"]
        self.assertEqual([a[1] for a in descargas], [job["url"]] * 2)

    # Una playlist de cinco: cfg dice qué canciones fallan y con qué error de spotDL 4.6.
    LISTA = """
qs = args[1:args.index('--output')]
err, save = args[args.index('--save-errors') + 1], args[args.index('--save-file') + 1]
cfg = json.load(open(os.environ['FAKE_LOG'] + '.cfg'))
songs = []
for q in qs:
    if '/track/' in q:
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

    def lista(self, fail):
        with open(self.log + ".cfg", "w", encoding="utf-8") as f:
            json.dump({"tracks": self.TRACKS, "fail": fail}, f)
        self.script(self.LISTA)

    def pedidas(self):
        """Lo que se le pidió a spotDL en cada ejecución."""
        return [a[1:a.index("--output")] for a in (c["args"] for c in self.llamadas()) if a[0] == "download"]

    def test_fallo_transitorio_reintenta_solo_las_fallidas_y_luego_se_agota(self):
        t = self.TRACKS
        self.lista({t[0]: self.YTDLP, t[1]: self.YTDLP, t[2]: self.SIN_RESULTADOS})
        _, job = self.request("POST", "/jobs", {"url": self.PLAYLIST})
        fin = self.esperar(job["id"], ("error",), intentos=2)
        # El automático ya pidió solo las dos transitorias; la permanente no se vuelve a pedir.
        self.assertEqual(self.pedidas(), [[self.PLAYLIST], [t[0], t[1]]])
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

    def test_toma_en_formato_viejo_queda_reintentable(self):
        """La base de producción: 'done' con el texto crudo de spotDL y su errors-{id}.txt."""
        t = self.TRACKS
        self.teardown()
        self.service.store.db.close()
        for sufijo in ("", "-wal", "-shm"):
            if os.path.exists(self.settings.db_path + sufijo):
                os.remove(self.settings.db_path + sufijo)
        db = sqlite3.connect(self.settings.db_path)
        db.execute("CREATE TABLE jobs(id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', "
                   "status TEXT NOT NULL, current TEXT NOT NULL DEFAULT '', total INTEGER NOT NULL DEFAULT 0, "
                   "done INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, "
                   "owner TEXT NOT NULL DEFAULT '')")
        crudo = f"2 canciones no se pudieron descargar: {t[0]} - AudioProviderError: YT-DLP download error - https://mus"
        db.executemany("INSERT INTO jobs(id, url, title, status, total, done, error, created, owner) VALUES(?, ?, ?, ?, 5, 5, ?, ?, 'ana')", [
            ("aaaaaaaaaaaaaaaa", self.PLAYLIST, "Tomá", "done", crudo, 1),
            ("bbbbbbbbbbbbbbbb", self.PLAYLIST, "Viejo", "done", "", 2),  # éxito viejo: se va
            ("cccccccccccccccc", self.PLAYLIST, "Sin archivo", "done", "22 canciones no se pudieron descargar: x", 3),
            ("dddddddddddddddd", self.PLAYLIST, "En cola", "queued", "", 4),
        ])
        db.commit()
        db.close()
        with open(os.path.join(self.settings.state_dir, "errors-aaaaaaaaaaaaaaaa.txt"), "w", encoding="utf-8") as f:
            f.write(f"2026-11-17-10-00-00\n{t[0]} - {self.YTDLP}\n{t[1]} - {self.YTDLP}\n")
        self.script("\ntime.sleep(60)\n")  # la cola sigue viva pero no termina durante la prueba
        self.reiniciar()
        self.reiniciar()  # la migración corre una sola vez

        jobs = {j["id"]: j for j in self.service.store.jobs("ana")}
        self.assertNotIn("bbbbbbbbbbbbbbbb", jobs)
        self.assertIn(jobs["dddddddddddddddd"]["status"], ("queued", "downloading"))
        toma = jobs["aaaaaaaaaaaaaaaa"]
        self.assertEqual((toma["status"], toma["error"]), ("error", "2 canciones no se pudieron descargar"))
        self.assertEqual(self.service.store.failed(toma["id"]), [{"url": u, "name": "", "retry": True} for u in t[:2]])
        sin = jobs["cccccccccccccccc"]
        self.assertEqual((sin["status"], sin["error"]), ("error", "22 canciones no se pudieron descargar"))

        # Reintentar Tomá pide solo las dos canciones del archivo; sin archivo, el enlace entero.
        self.service.cancel("dddddddddddddddd")
        self.lista({})
        self.assertEqual(self.request("POST", "/jobs/aaaaaaaaaaaaaaaa/retry")[0], 200)
        self.assertEqual(self.esperar("aaaaaaaaaaaaaaaa", ("finished",))["notice"], "«Tomá» ya está en tu biblioteca.")
        self.assertEqual(self.pedidas()[-1], t[:2])
        self.assertEqual(self.request("POST", "/jobs/cccccccccccccccc/retry")[0], 200)
        self.esperar("cccccccccccccccc", ("finished",))
        self.assertEqual(self.pedidas()[-1], [self.PLAYLIST])

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
        args = [c for c in self.llamadas() if c["args"][0] == "download"][0]["args"]
        self.assertNotIn("secreto-123", " ".join(args))
        self.assertNotIn("--client-secret", args)
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


class MigracionTest(unittest.TestCase):
    def test_base_sin_dueno(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "jobs.sqlite")
            db = sqlite3.connect(path)
            db.execute("CREATE TABLE jobs(id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', "
                       "status TEXT NOT NULL, current TEXT NOT NULL DEFAULT '', total INTEGER NOT NULL DEFAULT 0, "
                       "done INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL)")
            db.execute("INSERT INTO jobs(id, url, status, created) VALUES('0123456789abcdef', 'u', 'queued', 1)")
            db.commit()
            db.close()
            store = server.Store(path)
            self.assertEqual(store.get("0123456789abcdef")["owner"], "")
            self.assertEqual(store.create("https://open.spotify.com/track/x", "ana")["owner"], "ana")


if __name__ == "__main__":
    unittest.main()
