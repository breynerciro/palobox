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
oficial que publica YouTube Music). Si el video elegido no lo es (o no hubo resultado), hay una
segunda oportunidad: se busca la canción en YouTube y se toma, entre los videos de un canal verificado
del propio artista cuyo título la nombre, el de duración más parecida, sin tope de diferencia (puede
ser la versión del videoclip). Si tampoco hay, falla con «Unofficial source» y el servicio la reporta
como no disponible, para reemplazarla a mano. Se apaga con SPOTDL_OFFICIAL=0.

Lo demás es spotDL tal cual.
"""
import copy
import os
import re
import sys
import threading

from library import norm  # la misma comparación que el filtro del servicio; no arrastra dependencias


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
    """Envuelve AudioProvider.get_download_metadata: los datos del video llegan antes de bajar el audio.
    Los datos sin descarga se recuerdan por URL: la segunda oportunidad ya los pidió para decidir."""
    memo, guard = {}, threading.Lock()

    def wrap(self, url, download=False):
        with guard:
            data = None if download else memo.get(url)
        if data is None:
            data = fn(self, url, download)
            if not download:
                with guard:
                    memo[url] = data
        if not official(data):
            raise error(f"Unofficial source: {data.get('channel')} - {url}")
        return data

    return wrap


def pick_official(entries, name, artists, duration):
    """De los resultados de una búsqueda en YouTube, el video oficial de la canción: canal verificado del
    propio artista (el nombre de uno está en el del canal, o al revés) y el título nombra la canción. Entre
    varios, el de duración más parecida. Así no entran reacciones ni covers de otros canales verificados."""
    base = norm(re.sub(r"\s*[\(\[].*?[\)\]]", "", name).split(" - ")[0]) or norm(name)  # sin «(Remaster)», «- En vivo»
    if not base:  # un título vacío estaría «en» cualquier otro
        return None
    who = [a for a in map(norm, artists) if a]
    good = []
    for e in entries or []:
        channel = norm(e.get("channel"))
        if e.get("channel_is_verified") is not True or not e.get("id") or not channel:
            continue
        if not any(a in channel or channel in a for a in who) or base not in norm(e.get("title")):
            continue
        good.append((abs((e.get("duration") or 0) - (duration or 0)), e["id"]))
    return f"https://www.youtube.com/watch?v={min(good)[1]}" if good else None


def second_chance(search, find):
    """Envuelve AudioProvider.search: si lo elegido no es oficial (o no hubo nada), busca la versión oficial.
    Sin alternativa devuelve lo mismo que spotDL, y only_official lo rechaza al pedir sus datos."""

    def wrap(self, song, *args, **kwargs):
        url = rejected = None
        try:
            # spotDL también pide los datos de algunos candidatos mientras busca (para comparar reproducciones),
            # así que el rechazo de only_official puede saltar desde su propia búsqueda.
            url = search(self, song, *args, **kwargs)
            if url:
                self.get_download_metadata(url)
                return url  # ya es oficial
        except Exception as error:  # pylint: disable=broad-except
            if "Unofficial source" not in str(error):
                if url:
                    return url  # otro problema con el elegido (red, video caído): que lo maneje spotDL como siempre
                raise
            rejected = error
        alt = find(song)
        if alt or url or not rejected:
            return alt or url
        raise rejected

    return wrap


def youtube_official(song):
    """Busca la canción en YouTube (resultados planos: una sola petición) y elige con pick_official."""
    from yt_dlp import YoutubeDL

    query = f"ytsearch10:{song.artist} {song.name}"
    try:
        with YoutubeDL({"quiet": True, "no_warnings": True, "extract_flat": True, "skip_download": True}) as ydl:
            found = ydl.extract_info(query, download=False) or {}
    except Exception as error:  # pylint: disable=broad-except
        print(f"segunda oportunidad: la búsqueda de «{song.name}» falló: {error}", file=sys.stderr, flush=True)
        return None  # sin búsqueda no hay segunda oportunidad; la canción queda como la dejó spotDL
    return pick_official(found.get("entries"), song.name, song.artists or [song.artist], song.duration)


if __name__ == "__main__":
    from SpotipyFree import Spotify
    from spotdl import console_entry_point
    from spotdl.providers.audio.base import AudioProvider, AudioProviderError

    Spotify.album = cached(Spotify.album)
    Spotify.artist = cached(Spotify.artist)
    if os.environ.get("SPOTDL_OFFICIAL", "1") != "0":
        AudioProvider.get_download_metadata = only_official(AudioProvider.get_download_metadata, AudioProviderError)
        AudioProvider.search = second_chance(AudioProvider.search, youtube_official)
    sys.exit(console_entry_point())
