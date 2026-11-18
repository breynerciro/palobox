#!/usr/bin/env python3
"""spotDL con caché de álbumes y artistas.

spotDL 4.6 lee los metadatos con SpotipyFree (el reproductor web de Spotify, sin credenciales) y
cada consulta tarda varios segundos. Por canción hace tres: la pista, su álbum y su artista, sin
recordar nada, así que un álbum de 10 canciones pide diez veces el mismo álbum y el mismo artista.
Aquí esas dos consultas se recuerdan mientras dura el proceso: menos espera y menos peticiones a
Spotify. Lo demás es spotDL tal cual.
"""
import copy
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


if __name__ == "__main__":
    from SpotipyFree import Spotify
    from spotdl import console_entry_point

    Spotify.album = cached(Spotify.album)
    Spotify.artist = cached(Spotify.artist)
    sys.exit(console_entry_point())
