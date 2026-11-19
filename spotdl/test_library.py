#!/usr/bin/env python3
"""Pruebas de la biblioteca en disco (mutagen se sustituye)."""

import os
import sys
import tempfile
import unittest
from unittest import mock

import library


def biblioteca(music_dir, canciones):
    """Crea <Artista>/<Álbum>/<Título>.mp3 vacíos y devuelve sus etiquetas por ruta, para
    sustituir read_tags: (artista TPE1, álbum, título, duración, total de pistas)."""
    tags = {}
    for artista, album, titulo, dur, total in canciones:
        ruta = os.path.join(music_dir, artista.split("/")[0], album, titulo + ".mp3")
        os.makedirs(os.path.dirname(ruta), exist_ok=True)
        open(ruta, "wb").close()
        tags[ruta] = {"artists": artista.split("/"), "title": titulo, "duration": dur, "total": total}
    return tags


class FiltroTest(unittest.TestCase):
    """La misma canción como sencillo y dentro de un álbum: se conserva la del álbum."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.music = tmp.name

    def cancion(self, nombre="Nada", artista="Penyair", dur=180, pistas=1):
        return {"url": "https://open.spotify.com/track/x", "name": nombre, "artist": artista,
                "artists": [artista], "album_name": "x", "duration": dur, "tracks_count": pistas}

    def filtrar(self, disco, cancion):
        tags = biblioteca(self.music, disco)
        with mock.patch("library.read_tags", side_effect=tags.__getitem__):
            todo, saltadas = library.filter_plan([cancion], library.index_library(self.music))
        return "salta" if saltadas else "baja"

    def test_playlist_en_orden_con_lo_que_ya_estaba(self):
        biblioteca(self.music, [("Penyair", "Nada", "Nada", 180, 1), ("Otra Banda", "Disco", "Canción Dos", 200, 9)])
        songs = [[["Otra Banda", "Invitado"], "Canción dos"], [["Nadie"], "No bajó"], [["Invitado", "Penyair"], "Nada"]]
        self.assertEqual(library.playlist_tracks(songs, library.index_library(self.music), self.music),
                         ["Otra Banda/Disco/Canción Dos.mp3", "Penyair/Nada/Nada.mp3"])

    def test_sencillo_en_disco_y_llega_el_album(self):
        self.assertEqual(self.filtrar([("Penyair", "Nada", "Nada", 180, 1)], self.cancion(pistas=12)), "baja")

    def test_album_en_disco_y_llega_el_sencillo(self):
        self.assertEqual(self.filtrar([("Penyair", "La Ley del Todo", "Nada", 181, 12)], self.cancion(pistas=1)), "salta")

    def test_mas_de_3_segundos_de_diferencia(self):
        self.assertEqual(self.filtrar([("Penyair", "La Ley del Todo", "Nada", 184, 12)], self.cancion()), "baja")

    def test_llega_sencillo_con_otra_duracion_y_esta_el_album(self):
        c = self.cancion(dur=244)
        c["album_name"] = "Nada"
        self.assertEqual(self.filtrar([("Penyair", "La Ley del Todo", "Nada", 232, 12)], c), "salta")

    def test_llega_album_con_otra_duracion_y_esta_otro_album(self):
        self.assertEqual(self.filtrar([("Penyair", "Otro Disco", "Nada", 232, 12)], self.cancion(dur=244, pistas=12)), "baja")

    def test_album_que_se_llama_como_la_cancion_no_es_sencillo(self):
        c = self.cancion(nombre="Desde Abajo", dur=244, pistas=19)
        c["album_name"] = "Desde Abajo"
        self.assertEqual(self.filtrar([("Penyair", "Otro Disco", "Desde Abajo", 232, 12)], c), "baja")

    def test_llega_sencillo_y_en_disco_esta_un_album_que_se_llama_como_la_cancion(self):
        c = self.cancion(nombre="Desde Abajo", dur=244)
        c["album_name"] = "Desde Abajo"
        self.assertEqual(self.filtrar([("Penyair", "Desde Abajo", "Desde Abajo", 232, 5)], c), "salta")

    def test_otro_artista(self):
        self.assertEqual(self.filtrar([("Otro", "La Ley del Todo", "Nada", 180, 12)], self.cancion()), "baja")

    def test_tildes_y_mayusculas(self):
        self.assertEqual(self.filtrar([("PENYAIR", "Álbum", "CANCION", 180, 12)], self.cancion(nombre="Canción")), "salta")

    def test_cualquiera_de_los_artistas(self):
        # spotDL guarda TPE1 como «A/B» y la carpeta con el primero; llega con otro primero.
        c = self.cancion(artista="Invitado")
        c["artists"] = ["Invitado", "Penyair"]
        self.assertEqual(self.filtrar([("Penyair/Invitado", "La Ley del Todo", "Nada", 180, 12)], c), "salta")

    def test_lee_artistas_con_barra(self):
        frame = lambda *t: mock.Mock(text=list(t))
        audio = mock.Mock(tags={"TPE1": frame("AC/DC"), "TIT2": frame("Thunderstruck"), "TRCK": frame("1/10")})
        audio.info.length = 292.0
        mp3 = mock.Mock(MP3=mock.Mock(return_value=audio))
        with mock.patch.dict(sys.modules, {"mutagen": mock.Mock(), "mutagen.mp3": mp3}):
            tags = library.read_tags("x.mp3")
        self.assertEqual(tags, {"artists": ["AC/DC", "AC", "DC"], "title": "Thunderstruck", "duration": 292.0, "total": 10})

    def test_borra_solo_mp3_a_medias_del_trabajo(self):
        disco = {"vieja.mp3": "", "a_medias.mp3": "", "buena.mp3": "Buena", "danada.mp3": None, "nota.txt": "", "de_samba.mp3": ""}
        for nombre in disco:
            open(os.path.join(self.music, nombre), "wb").close()
        os.utime(os.path.join(self.music, "vieja.mp3"), (1, 1))  # anterior al trabajo: no es suya

        def leer(path):
            titulo = disco[os.path.basename(path)]
            if titulo is None:
                raise ValueError("dañado")
            return {"title": titulo}

        with mock.patch("library.read_tags", side_effect=leer):
            library.drop_partial(self.music, 1000 * 1000, ["Vieja", "A medias", "Buena", "Dañada", "Nota"])
        # de_samba.mp3 tampoco tiene título, pero no es una canción del trabajo
        self.assertEqual(sorted(os.listdir(self.music)), ["buena.mp3", "danada.mp3", "de_samba.mp3", "nota.txt", "vieja.mp3"])

    def test_mp3_ilegible_no_frena(self):
        biblioteca(self.music, [("Penyair", "La Ley del Todo", "Nada", 180, 12)])
        with mock.patch("library.read_tags", side_effect=ValueError("dañado")):
            todo, saltadas = library.filter_plan([self.cancion()], library.index_library(self.music))
        self.assertEqual((len(todo), saltadas), (1, []))


if __name__ == "__main__":
    unittest.main()
