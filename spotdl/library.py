"""La biblioteca en disco: etiquetas de los MP3 (lo único que usa mutagen), el índice por artista y
título, el filtro de lo que ya está y las rutas de una playlist terminada."""

import collections
import logging
import os
import unicodedata

log = logging.getLogger("spotdl.servicio")


def norm(s):
    """Texto comparable: sin mayúsculas, tildes, espacios ni signos («Canción» = «CANCION»)."""
    return "".join(c for c in unicodedata.normalize("NFKD", s or "").casefold() if c.isalnum())


def read_tags(path):
    """Artistas (TPE1), título, duración y total de pistas del álbum (TRCK «n/total») de un MP3."""
    from mutagen.mp3 import MP3  # perezoso: viene con spotDL y las pruebas lo sustituyen

    audio = MP3(path)
    tags = audio.tags or {}

    def texts(key):
        frame = tags.get(key)
        return [str(t) for t in frame.text] if frame else []

    total = (texts("TRCK") or [""])[0].partition("/")[2]
    return {
        # spotDL une los artistas con «/», pero un nombre puede llevarla («AC/DC»): van ambos.
        "artists": [a for t in texts("TPE1") for a in (t, *t.split("/"))],
        "title": (texts("TIT2") or [""])[0],
        "duration": audio.info.length,
        "total": int(total) if total.isdigit() else 0,
    }


def index_library(music_dir):
    """(artista, título) normalizados → rutas, según <Artista>/<Álbum>/<Título>.mp3. No lee
    etiquetas: eso se hace solo con los candidatos."""
    index = collections.defaultdict(list)
    for root, _dirs, files in os.walk(music_dir):
        parts = os.path.relpath(root, music_dir).split(os.sep)
        if parts[0] == ".":
            continue
        for name in files:
            stem, ext = os.path.splitext(name)
            if ext.lower() == ".mp3":
                index[(norm(parts[0]), norm(stem))].append(os.path.join(root, name))
    return index


def playlist_tracks(songs, index, music_dir):
    """Las rutas (relativas a la biblioteca) de las canciones de una playlist, en su orden; las que
    no están en disco (fallaron) se omiten. songs: [[artistas], título]."""
    out = []
    for artists, title in songs:
        paths = next((index[(norm(a), norm(title))] for a in artists if (norm(a), norm(title)) in index), None)
        if paths:
            out.append(os.path.relpath(sorted(paths)[0], music_dir).replace(os.sep, "/"))
    return out


def drop_partial(music_dir, since, titles):
    """Borra los MP3 que dejó a medias un spotDL cortado (cancelar, tiempo máximo, apagar): convierte
    directo a la ruta final y etiqueta después, y al reintentar los saltaría por existir. Solo toca
    archivos más nuevos que el trabajo (since, en ms), sin título en sus etiquetas y con el nombre de una
    de sus canciones (titles): un MP3 sin etiquetas copiado por Samba en ese rato no es del trabajo."""
    titles = {norm(t) for t in titles if t}
    if not titles:
        return
    for root, _dirs, files in os.walk(music_dir):
        for name in files:
            path = os.path.join(root, name)
            try:
                if not name.lower().endswith(".mp3") or norm(name[:-4]) not in titles or os.path.getmtime(path) * 1000 <= since:
                    continue
                if not read_tags(path)["title"]:
                    log.warning("se borra un MP3 a medias: %s", path)
                    os.remove(path)
            except Exception as exc:  # ilegible o ya borrado: no se toca
                log.warning("no se pudo revisar %s: %s", path, exc)


def filter_plan(songs, index):
    """(a bajar, saltadas): se salta la canción que ya está en disco con el mismo artista y
    título, ±3 s de duración y un álbum igual o más grande. Si llega un sencillo y en disco está
    la versión de un álbum, se salta aunque duren distinto (radio edit, otra intro). Si solo está
    el sencillo, la versión del álbum sí se baja (la app Go borra luego el sencillo)."""
    tags = {}

    def read(path):
        if path not in tags:
            try:
                tags[path] = read_tags(path)
            except Exception as exc:  # un MP3 dañado no frena la descarga: cuenta como ausente
                log.warning("no se pudieron leer las etiquetas de %s: %s", path, exc)
                tags[path] = None
        return tags[path]

    todo, skipped = [], []
    for song in songs:
        artists = {norm(a) for a in [song.get("artist"), *(song.get("artists") or [])] if norm(a)}
        title = norm(song.get("name"))
        paths = {p for a in artists for p in index.get((a, title), ())}
        # spotDL nombra la carpeta del sencillo como la canción; Spotify solo llama sencillo a un
        # lanzamiento de 1 a 3 canciones, así que un álbum que se llama como una de ellas no lo es.
        single = norm(song.get("album_name")) == title and (song.get("tracks_count") or 0) <= 3
        found = any(
            t and norm(t["title"]) == title and artists & {norm(a) for a in t["artists"]}
            and (
                abs(t["duration"] - (song.get("duration") or 0)) <= 3
                and t["total"] >= (song.get("tracks_count") or 0)
                or single and (norm(os.path.basename(os.path.dirname(p))) != title or t["total"] > 3)
            )
            for p, t in ((p, read(p)) for p in sorted(paths))
        )
        (skipped if found else todo).append(song)
    return todo, skipped
