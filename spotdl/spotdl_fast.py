#!/usr/bin/env python3
"""spotDL con caché de álbumes y artistas, y solo con audio de fuentes oficiales.

spotDL 4.6 lee los metadatos con SpotipyFree (el reproductor web de Spotify, sin credenciales) y
cada consulta tarda varios segundos. Por canción hace tres: la pista, su álbum y su artista, sin
recordar nada, así que un álbum de 10 canciones pide diez veces el mismo álbum y el mismo artista.
Aquí esas dos consultas se recuerdan mientras dura el proceso: menos espera y menos peticiones a
Spotify.

spotDL busca cada canción en YouTube Music y, si no encuentra el audio oficial, se queda con el video
que mejor coincida en título y duración: así entran karaokes y versiones de cualquier canal. Aquí
una canción solo se baja si viene de un canal verificado o de un canal «Artista - Topic» (el audio
oficial que publica YouTube Music). Si no, falla con «Unofficial source» y el servicio la reporta
como no disponible, para reemplazarla a mano. Se apaga con SPOTDL_OFFICIAL=0.

Lo demás es spotDL tal cual.
"""
import copy
import os
import sys
import threading


def cached(fn):
    """Recuerda fn(self, clave); con más argumentos (paginación, mercado) no se mete."""
    memo, locks, guard = {}, {}, threading.Lock()

    def wrap(self, key, *args, **kwargs):
        if args or kwargs:
            return fn(self, key, *args, **kwargs)
        with guard:
            lock = locks.setdefault(key, threading.Lock())
        with lock:  # dos hilos que piden lo mismo: el segundo espera la respuesta del primero
            if key not in memo:
                memo[key] = fn(self, key)
            return copy.deepcopy(memo[key])  # quien la recibe puede modificarla sin dañar la guardada

    return wrap


def official(data):
    """El video es del artista: canal verificado, o canal «Artista - Topic» de YouTube Music."""
    return data.get("channel_is_verified") is True or str(data.get("channel") or "").endswith(" - Topic")


def only_official(fn, error):
    """Envuelve AudioProvider.get_download_metadata: los datos del video llegan antes de bajar el audio."""

    def wrap(self, url, download=False):
        data = fn(self, url, download)
        if not official(data):
            raise error(f"Unofficial source: {data.get('channel')} - {url}")
        return data

    return wrap


if __name__ == "__main__":
    from SpotipyFree import Spotify
    from spotdl import console_entry_point
    from spotdl.providers.audio.base import AudioProvider, AudioProviderError

    Spotify.album = cached(Spotify.album)
    Spotify.artist = cached(Spotify.artist)
    if os.environ.get("SPOTDL_OFFICIAL", "1") != "0":
        AudioProvider.get_download_metadata = only_official(AudioProvider.get_download_metadata, AudioProviderError)
    sys.exit(console_entry_point())
