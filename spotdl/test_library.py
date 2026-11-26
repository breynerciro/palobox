#!/usr/bin/env python3
"""Pruebas de la biblioteca en disco (mutagen se sustituye)."""

import os
import sys
import tempfile
import unittest
from unittest import mock

import library


def biblioteca(music_dir, canciones):
    """Crea <Artista>/<Álbum>/<Título>.m4a vacíos y devuelve sus etiquetas por ruta, para
    sustituir read_tags: (artista, álbum, título, duración, total de pistas)."""
    tags = {}
    for artista, album, titulo, dur, total in canciones:
        ruta = os.path.join(music_dir, artista.split("/")[0], album, titulo + ".m4a")
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
                         ["Otra Banda/Disco/Canción Dos.m4a", "Penyair/Nada/Nada.m4a"])

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
        audio = mock.Mock(tags={"\xa9ART": ["AC/DC"], "\xa9nam": ["Thunderstruck"], "trkn": [(1, 10)]})
        audio.info.length = 292.0
        mp4 = mock.Mock(MP4=mock.Mock(return_value=audio))
        with mock.patch.dict(sys.modules, {"mutagen": mock.Mock(), "mutagen.mp4": mp4}):
            tags = library.read_tags("x.m4a")
        self.assertEqual(tags, {"artists": ["AC/DC", "AC", "DC"], "title": "Thunderstruck", "duration": 292.0, "total": 10})

    def test_une_los_artistas_de_lo_nuevo(self):
        for name in ("nueva.m4a", "vieja.m4a", "sola.m4a"):
            open(os.path.join(self.music, name), "w").close()
        os.utime(os.path.join(self.music, "vieja.m4a"), (1, 1))
        audios = {n: mock.Mock(tags={"\xa9ART": a}) for n, a in {"nueva.m4a": ["Eminem", "Rihanna"], "vieja.m4a": ["A", "B"], "sola.m4a": ["Juanes"]}.items()}
        mp4 = mock.Mock(MP4=lambda path: audios[os.path.basename(path)])
        with mock.patch.dict(sys.modules, {"mutagen": mock.Mock(), "mutagen.mp4": mp4}):
            library.join_artists(self.music, 5000)
        self.assertEqual(audios["nueva.m4a"].tags["\xa9ART"], ["Eminem/Rihanna"])
        audios["nueva.m4a"].save.assert_called_once()
        audios["vieja.m4a"].save.assert_not_called()  # anterior al trabajo
        audios["sola.m4a"].save.assert_not_called()  # un solo artista: nada que unir

    def test_borra_solo_m4a_a_medias_del_trabajo(self):
        disco = {"vieja.m4a": "", "a_medias.m4a": "", "buena.m4a": "Buena", "danada.m4a": None, "nota.txt": "", "de_samba.m4a": ""}
        for nombre in disco:
            open(os.path.join(self.music, nombre), "wb").close()
        os.utime(os.path.join(self.music, "vieja.m4a"), (1, 1))  # anterior al trabajo: no es suya

        def leer(path):
            titulo = disco[os.path.basename(path)]
            if titulo is None:
                raise ValueError("dañado")
            return {"title": titulo}

        with mock.patch("library.read_tags", side_effect=leer):
            library.drop_partial(self.music, 1000 * 1000, ["Vieja", "A medias", "Buena", "Dañada", "Nota"])
        # de_samba.m4a tampoco tiene título, pero no es una canción del trabajo
        self.assertEqual(sorted(os.listdir(self.music)), ["buena.m4a", "danada.m4a", "de_samba.m4a", "nota.txt", "vieja.m4a"])

    def test_m4a_ilegible_no_frena(self):
        biblioteca(self.music, [("Penyair", "La Ley del Todo", "Nada", 180, 12)])
        with mock.patch("library.read_tags", side_effect=ValueError("dañado")):
            todo, saltadas = library.filter_plan([self.cancion()], library.index_library(self.music))
        self.assertEqual((len(todo), saltadas), (1, []))


if __name__ == "__main__":
    unittest.main()
