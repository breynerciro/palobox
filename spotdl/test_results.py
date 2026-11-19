#!/usr/bin/env python3
"""Pruebas de la salida de spotDL y de las reglas de resultado."""

import os
import tempfile
import unittest

import results


class FeedTest(unittest.TestCase):
    def test_playlist(self):
        f = results.Feed()
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
        f = results.Feed()
        f.line('Downloaded "Daft Punk - One More Time": https://youtube.example/1')
        f.line("1/1 complete")
        self.assertEqual((f.title, f.total, f.done), ("Daft Punk - One More Time", 1, 1))

    def test_ignora_basura_y_ansi(self):
        f = results.Feed()
        self.assertFalse(f.line(""))
        self.assertFalse(f.line("\x1b[2mDescargando audio…\x1b[0m"))
        self.assertTrue(f.line("Found 2 songs in Álbum ñ (Album)"))
        self.assertEqual(f.title, "Álbum ñ")


class ResultadoTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def test_errores_del_archivo(self):
        path = os.path.join(self.tmp.name, "errores.txt")
        self.assertEqual(results.failures_from(path), [])
        a, b = "https://open.spotify.com/track/" + "a" * 22, "https://open.spotify.com/track/" + "b" * 22
        with open(path, "w", encoding="utf-8") as f:
            f.write(f"2026-11-17-10-00-00\n{a} - AudioProviderError: YT-DLP download error - https://music.youtube.com/watch?v=x\n"
                    f"2026-11-17-11-00-00\n{b} - LookupError: No results found for song: Artista - B\n"
                    "Error occurred while reinitializing song: 429\n")
        got = results.failures_from(path, {a: "A — Artista"})
        self.assertEqual(got, [{"url": a, "name": "A — Artista", "retry": True},
                               {"url": b, "name": "", "retry": False},
                               {"url": "", "name": "", "retry": False}])
        self.assertEqual(results.row_text(got), "3 canciones no se pudieron descargar: A — Artista y 2 más")
        self.assertEqual(results.row_text(got[:1]), "1 canción no se pudo descargar: A — Artista")
        self.assertEqual(results.row_text([]), "No se pudo completar la descarga.")

    def test_reglas_de_resultado(self):
        url = "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"
        trans = {"url": "u", "name": "A — B", "retry": True}
        perm = {"url": "v", "name": "C — D", "retry": False}
        self.assertEqual(results.outcome("Tomá", url, [], False, 1, 3), ("finished", "", "«Tomá» ya está en tu biblioteca."))
        self.assertEqual(results.outcome("Tomá", url, [trans, perm], False, 2, 3)[0], "error")
        self.assertEqual(results.outcome("Tomá", url, [], True, 2, 3)[0], "error")
        self.assertEqual(results.outcome("Tomá", url, [trans, perm], False, 3, 3),
                         ("finished", "2 canciones no se pudieron descargar: A — B, C — D", "2 canciones de «Tomá» no se pudieron descargar: A — B, C — D."))
        self.assertEqual(results.outcome("Tomá", url, [perm], False, 1, 3),
                         ("finished", "1 canción no se pudo descargar: C — D", "1 canción de «Tomá» no está disponible: C — D."))
        self.assertEqual(results.outcome("", "https://open.spotify.com/track/x", [perm], False, 1, 3)[::2],
                         ("finished", "«C — D» no está disponible."))
        self.assertIn("No se pudo descargar «Tomá»", results.outcome("Tomá", url, [], True, 3, 3)[2])


if __name__ == "__main__":
    unittest.main()
