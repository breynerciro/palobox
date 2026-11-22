#!/usr/bin/env python3
"""Pruebas de spotdl_fast: caché y fuentes oficiales."""

import threading
import time
import unittest

import results
import spotdl_fast


class CacheTest(unittest.TestCase):
    """spotdl_fast: el álbum y el artista se piden una sola vez por proceso."""

    def test_recuerda_por_clave(self):
        calls = []

        def album(_self, key, *args, **kwargs):
            calls.append((key, args, kwargs))
            time.sleep(0.05)
            return {"id": key, "tracks": []}

        fast = spotdl_fast.cached(album)
        got = []
        hilos = [threading.Thread(target=lambda: got.append(fast(None, "a"))) for _ in range(4)]
        for h in hilos:
            h.start()
        for h in hilos:
            h.join()
        self.assertEqual(len(calls), 1)  # cuatro hilos, una sola consulta
        got[0]["tracks"].append("x")  # cada quien recibe su copia
        self.assertEqual(fast(None, "a"), {"id": "a", "tracks": []})
        fast(None, "b")
        fast(None, "a", limit=50)  # con más argumentos no se guarda
        self.assertEqual([c[0] for c in calls], ["a", "b", "a"])

    def test_la_sesion_pasa_al_cliente_siguiente(self):
        """Cada consulta a Spotify crea un cliente; lo que uno averigua (sesión, tokens) le queda al siguiente."""
        sesiones = []

        class Cliente:
            token = None

            def abrir(self):
                sesiones.append(1)
                self.token = f"token-{len(sesiones)}"

            def consultar(self):
                if self.token is None:
                    self.abrir()
                return self.token

        Cliente.abrir = spotdl_fast.shared(Cliente.abrir, ("token",))
        self.assertEqual([Cliente().consultar() for _ in range(3)], ["token-1"] * 3)
        self.assertEqual(len(sesiones), 1)  # tres clientes, una sola sesión
        viejo = Cliente()
        viejo.token = None  # venció: ese cliente la renueva y los siguientes reciben la nueva
        self.assertEqual(viejo.consultar(), "token-2")
        self.assertEqual(Cliente().consultar(), "token-2")

    def test_solo_fuentes_oficiales(self):
        self.assertTrue(spotdl_fast.official({"channel": "Carin Leon", "channel_is_verified": True}))
        self.assertTrue(spotdl_fast.official({"channel": "Noiseferatu - Topic"}))  # audio oficial de YouTube Music
        self.assertFalse(spotdl_fast.official({"channel": "AmericaEnlaces"}))  # el karaoke «Tono bajo»
        self.assertFalse(spotdl_fast.official({"channel": None, "channel_is_verified": None}))
        datos = {"channel": "AmericaEnlaces"}
        meta = spotdl_fast.only_official(lambda _self, url, download=False: datos, ValueError)
        with self.assertRaises(ValueError) as e:
            meta(None, "https://www.youtube.com/watch?v=x")
        # Así lo escribe spotDL en --save-errors; el servicio no lo reintenta.
        self.assertTrue(results.permanent("AudioProviderError", str(e.exception)))
        datos["channel_is_verified"] = True
        self.assertEqual(meta(None, "u", True), datos)

    def test_segunda_oportunidad(self):
        # Lo que devuelve YouTube al buscar «La Típica» de Alcolirykoz (el álbum dura 162 s).
        vistos = [
            {"id": "fan", "title": "La Típica- Alcolirykoz (Letra)", "channel": "Pineda.715", "duration": 162},
            {"id": "vivo", "title": "AlcolirykoZ - La típica (en vivo) Diciembre 2022", "channel": "AlcolirykoZ", "channel_is_verified": True, "duration": 285},
            {"id": "oficial", "title": "Alcolirykoz - La Típica (Prod. El Arkeólogo)", "channel": "AlcolirykoZ", "channel_is_verified": True, "duration": 197},
            {"id": "otra", "title": "AlcolirykoZ & Armando Hernandez - El Remate", "channel": "AlcolirykoZ", "channel_is_verified": True, "duration": 160},
            {"id": "reaccion", "title": "Alcolirykoz - La Típica // Reacción", "channel": "Disgusting Oficial", "channel_is_verified": True, "duration": 165},
        ]
        url = "https://www.youtube.com/watch?v="
        # Ni el fan (sin verificar), ni otra canción, ni la reacción de otro canal: la oficial más parecida en duración.
        self.assertEqual(spotdl_fast.pick_official(vistos, "La Típica (2.018)", ["Alcolirykoz"], 162), url + "oficial")
        self.assertEqual(spotdl_fast.pick_official(vistos, "Tú", ["Carín León"], 247), None)
        self.assertEqual(spotdl_fast.pick_official(None, "x", ["y"], 1), None)

        class Song:
            pass

        class Provider:
            def get_download_metadata(self, u):
                if u == "fan":
                    raise ValueError("Unofficial source: Pineda.715 - fan")
                if u == "caido":
                    raise ValueError("YT-DLP download error")

        buscar = {"elige": "fan"}
        search = spotdl_fast.second_chance(lambda _self, _song: buscar["elige"], lambda _song: buscar.get("oficial"))
        self.assertEqual(search(Provider(), Song()), "fan")  # sin alternativa: queda la de spotDL y se rechaza después
        buscar["oficial"] = "oficial"
        self.assertEqual(search(Provider(), Song()), "oficial")
        buscar["elige"] = None
        self.assertEqual(search(Provider(), Song()), "oficial")  # spotDL no encontró nada
        buscar["elige"] = "bien"
        self.assertEqual(search(Provider(), Song()), "bien")  # lo oficial no se toca
        buscar["elige"] = "caido"
        self.assertEqual(search(Provider(), Song()), "caido")  # un fallo de red no es motivo para cambiar de video

        # spotDL pide datos de los candidatos mientras busca: el rechazo puede salir de su propia búsqueda.
        def rota(_self, _song):
            raise ValueError("Unofficial source: Rap Metafora - x")

        self.assertEqual(spotdl_fast.second_chance(rota, lambda _song: "oficial")(Provider(), Song()), "oficial")
        with self.assertRaises(ValueError):  # sin alternativa, el mismo rechazo
            spotdl_fast.second_chance(rota, lambda _song: None)(Provider(), Song())

        def sin_red(_self, _song):
            raise OSError("sin red")

        with self.assertRaises(OSError):  # un fallo de la búsqueda no se disfraza
            spotdl_fast.second_chance(sin_red, lambda _song: "oficial")(Provider(), Song())

    def test_titulo_sin_letras_latinas(self):
        bts = [{"id": "dyn", "title": "BTS (방탄소년단) 'Dynamite' Official MV", "channel": "HYBE LABELS", "channel_is_verified": True, "duration": 199},
               {"id": "bom", "title": "BTS (방탄소년단) '봄날 (Spring Day)' Official MV", "channel": "HYBE LABELS", "channel_is_verified": True, "duration": 285}]
        url = "https://www.youtube.com/watch?v="
        # Antes «봄날» quedaba vacío y cualquier título lo «contenía»: se colaba Dynamite.
        self.assertEqual(spotdl_fast.pick_official(bts, "봄날", ["HYBE LABELS"], 199), url + "bom")
        self.assertIsNone(spotdl_fast.pick_official(bts[:1], "봄날", ["HYBE LABELS"], 199))
        self.assertIsNone(spotdl_fast.pick_official(bts, "!!!", ["HYBE LABELS"], 199))  # sin nada comparable


if __name__ == "__main__":
    unittest.main()
