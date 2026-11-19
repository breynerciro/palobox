#!/usr/bin/env python3
"""Pruebas de la cola en SQLite."""

import os
import sqlite3
import tempfile
import unittest

import results
import store


class StoreTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.store = store.Store(os.path.join(self.tmp.name, "jobs.sqlite"), keep_finished=2)

    def test_ciclo(self):
        job = self.store.create("https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX")
        self.assertEqual(job["status"], "queued")
        self.assertEqual(job["total"], 0)
        self.assertIsNone(job["progress"])
        self.assertEqual(self.store.active_count(), 1)
        self.assertIsNotNone(self.store.find_active(job["url"]))

        self.assertTrue(self.store.claim(job["id"]))
        self.assertFalse(self.store.claim(job["id"]))  # ya no está en cola
        feed = results.Feed()
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
        self.store.progress(job["id"], results.Feed())  # aún sin datos: no borra el total
        self.assertEqual(self.store.get(job["id"])["total"], 1)

    def test_reanuda_y_conserva_lo_terminado(self):
        uno = self.store.create("https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M")
        dos = self.store.create("https://open.spotify.com/album/1ATL5GLyefJaxhQzSPVrLX")
        self.store.claim(uno["id"])
        self.store.progress(uno["id"], results.Feed())
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

    def test_el_tope_de_terminados_es_por_dueno(self):
        def terminado(owner, i):
            jid = self.store.create(f"https://open.spotify.com/playlist/{i:022d}", owner)["id"]
            self.store.claim(jid)
            self.store.finish(jid, "finished", notice="aviso")
            return jid

        suyo = terminado("ana", 0)
        for i in range(1, 6):  # más que keep_finished, todos de otra cuenta
            terminado("pepe", i)
        self.assertEqual([j["id"] for j in self.store.jobs("ana")], [suyo])
        self.assertEqual(len(self.store.jobs("pepe")), 2)


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
            cola = store.Store(path)
            self.assertEqual(cola.get("0123456789abcdef")["owner"], "")
            self.assertEqual(cola.create("https://open.spotify.com/track/x", "ana")["owner"], "ana")


if __name__ == "__main__":
    unittest.main()
