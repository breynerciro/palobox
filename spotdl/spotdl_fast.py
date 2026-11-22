#!/usr/bin/env python3
"""spotDL con caché de álbumes y artistas, y solo con audio de fuentes oficiales.

spotDL 4.6 lee los metadatos con SpotipyFree (el reproductor web de Spotify, sin credenciales) y
cada consulta tarda varios segundos. Por canción hace tres: la pista, su álbum y su artista, sin
recordar nada, así que un álbum de 10 canciones pide diez veces el mismo álbum y el mismo artista.
Aquí esas dos consultas se recuerdan mientras dura el proceso: menos espera y menos peticiones a
Spotify.

spotDL busca cada canción en YouTube Music y, si no encuentra el audio oficial, se queda con el video
que mejor coincida en título y duración: así entran karaokes y versiones de cualquier canal. Aquí
se prefiere la fuente oficial: un canal verificado o un canal «Artista - Topic» (el audio oficial que
publica YouTube Music). Si el video elegido no lo es (o no hubo resultado), hay una segunda oportunidad:
se busca la canción en YouTube y se toma, entre los videos de un canal verificado del propio artista cuyo
título la nombre, el de duración más parecida, sin tope de diferencia (puede ser la versión del videoclip).
Solo si tampoco hay se baja lo que eligió spotDL, y queda anotado en el registro como «sin fuente oficial».
Se apaga con SPOTDL_OFFICIAL=0 (siempre lo que elija spotDL).

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


def shared(fn, names):
    """Envuelve un método de spotapi.BaseClient que averigua algo (la sesión, un token): al terminar deja esos
    valores también en la clase. Cada consulta a Spotify crea un cliente nuevo, y así nace con lo que ya averiguó
    el anterior en vez de volver a abrir sesión. El propio cliente renueva lo que vence o lo que Spotify rechaza."""

    def wrap(self, *args, **kwargs):
        out = fn(self, *args, **kwargs)
        for name in names:
            setattr(type(self), name, getattr(self, name))
        return out

    return wrap


def official(data):
    """El video es del artista: canal verificado, o canal «Artista - Topic» de YouTube Music."""
    return data.get("channel_is_verified") is True or str(data.get("channel") or "").endswith(" - Topic")


# Sin fuente oficial se acepta lo que spotDL habría elegido. relaxed apaga el rechazo en este hilo mientras se
# repite su búsqueda; allowed son los videos que quedaron aceptados así, para que la descarga no los rechace.
relaxed = threading.local()
allowed, allowed_guard = set(), threading.Lock()


def only_official(fn, error):
    """Envuelve AudioProvider.get_download_metadata: los datos del video llegan antes de bajar el audio.
    Los datos sin descarga se recuerdan por URL: la segunda oportunidad ya los pidió para decidir, y la
    descarga los reutiliza en vez de volver a consultar el video."""
    memo, guard = {}, threading.Lock()

    def wrap(self, url, download=False):
        with guard:
            known = memo.get(url)
        data = None if download else known
        if download and known is not None:
            # El video ya se consultó para decidir si era el oficial: se baja con esos mismos datos. Pedírselo
            # otra vez a YouTube era resolver de nuevo su protección (un proceso de deno, más de un segundo de
            # procesador por canción). Si con ellos no baja (enlace vencido), sigue el camino de siempre.
            try:
                data = self.audio_handler.process_ie_result(copy.deepcopy(known), download=True)
            except Exception:  # pylint: disable=broad-except
                data = None
        if data is None:
            data = fn(self, url, download)
            if not download:
                with guard:
                    memo[url] = data
        with allowed_guard:
            ok = url in allowed
        if not ok and not getattr(relaxed, "on", False) and not official(data):
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
    Solo si tampoco existe se queda con lo que eligió spotDL, y lo anota en allowed para que se pueda bajar."""

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
        if alt:
            # La eligió pick_official en el canal verificado del artista. Al pedir sus datos completos yt-dlp a
            # veces no repite la marca de verificado («Danny Ocean»): se acepta de una vez para que no se rechace.
            with allowed_guard:
                allowed.add(alt)
            return alt
        if not rejected:
            return url
        # No hay fuente oficial: se baja lo que spotDL habría elegido sin el filtro (puede ser otra versión o un
        # canal cualquiera), solo en este caso. Si el rechazo saltó dentro de su búsqueda, se repite sin él.
        relaxed.on = True
        try:
            url = url or search(self, song, *args, **kwargs)
        finally:
            relaxed.on = False
        if not url:
            raise rejected
        with allowed_guard:
            allowed.add(url)
        print(f"sin fuente oficial: «{getattr(song, 'name', '')}» se baja de {url} ({str(rejected)[:80]})", file=sys.stderr, flush=True)
        return url

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

    from spotapi.client import BaseClient

    Spotify.album = cached(Spotify.album)
    Spotify.artist = cached(Spotify.artist)
    # Cada consulta a Spotify crea un cliente nuevo, y ese cliente baja el reproductor web entero (unos 77
    # archivos de JavaScript) solo para leer de ahí el identificador de la consulta, que es el mismo para todas
    # las del proceso. Recordarlo por nombre de consulta deja cada una en lo suyo (medido: de 79 peticiones a 4).
    # Si Spotify publica otro reproductor a mitad de un trabajo largo, lo que falle se reintenta en un proceso nuevo.
    BaseClient.part_hash = cached(BaseClient.part_hash)
    # Lo mismo con la sesión: sin esto cada consulta abría la página de Spotify y pedía dos tokens antes de preguntar.
    token = ("access_token", "client_id", "access_token_expires_at_ms")
    BaseClient._get_auth_vars = shared(BaseClient._get_auth_vars, token)
    BaseClient.get_session = shared(BaseClient.get_session, ("js_pack", "server_cfg", "client_version", "device_id", *token))
    BaseClient.get_client_token = shared(BaseClient.get_client_token, ("client_token",))
    if os.environ.get("SPOTDL_OFFICIAL", "1") != "0":
        AudioProvider.get_download_metadata = only_official(AudioProvider.get_download_metadata, AudioProviderError)
        AudioProvider.search = second_chance(AudioProvider.search, youtube_official)
    sys.exit(console_entry_point())
