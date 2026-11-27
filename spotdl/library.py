"""La biblioteca en disco: etiquetas de los M4A (lo único que usa mutagen), el índice por artista y
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
    """Artistas (©ART), título, duración y total de pistas del álbum (trkn) de un M4A."""
    from mutagen.mp4 import MP4  # perezoso: viene con spotDL y las pruebas lo sustituyen

    audio = MP4(path)
    tags = audio.tags or {}
    track = (tags.get("trkn") or [(0, 0)])[0]
    return {
        # Los artistas van unidos con «/» (join_artists), pero un nombre puede llevarla («AC/DC»): van ambos.
        "artists": [a for t in tags.get("\xa9ART") or [] for a in (t, *t.split("/"))],
        "title": (tags.get("\xa9nam") or [""])[0],
        "duration": audio.info.length,
        "total": track[1] if len(track) > 1 else 0,
    }


def join_artists(music_dir, since):
    """Deja en una sola etiqueta, unidos con «/», los artistas de los M4A más nuevos que since (ms). spotDL
    escribe un valor por artista y ffprobe, con el que la app arma su índice, solo lee el primero."""
    from mutagen.mp4 import MP4

    for root, _dirs, files in os.walk(music_dir):
        for name in files:
            path = os.path.join(root, name)
            try:
                if not name.lower().endswith(".m4a") or os.path.getmtime(path) * 1000 <= since:
                    continue
                audio = MP4(path)
                artists = (audio.tags or {}).get("\xa9ART") or []
                if len(artists) > 1:
                    audio.tags["\xa9ART"] = ["/".join(artists)]
                    audio.save()
            except Exception as exc:  # ilegible o a medias: drop_partial se encarga
                log.warning("no se pudieron unir los artistas de %s: %s", path, exc)


def index_library(music_dir):
    """(artista, título) normalizados → rutas, según <Artista>/<Álbum>/<Título>.m4a. No lee
    etiquetas: eso se hace solo con los candidatos."""
    index = collections.defaultdict(list)
    for root, _dirs, files in os.walk(music_dir):
        parts = os.path.relpath(root, music_dir).split(os.sep)
        if parts[0] == ".":
            continue
        for name in files:
            stem, ext = os.path.splitext(name)
            if ext.lower() == ".m4a":
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
    """Borra los M4A que dejó a medias un spotDL cortado (cancelar, tiempo máximo, apagar): escribe
    directo a la ruta final y etiqueta después, y al reintentar los saltaría por existir. Solo toca
    archivos más nuevos que el trabajo (since, en ms), sin título en sus etiquetas y con el nombre de una
    de sus canciones (titles): un M4A sin etiquetas copiado por Samba en ese rato no es del trabajo."""
    titles = {norm(t) for t in titles if t}
    if not titles:
        return
    for root, _dirs, files in os.walk(music_dir):
        for name in files:
            path = os.path.join(root, name)
            try:
                if not name.lower().endswith(".m4a") or norm(name[:-4]) not in titles or os.path.getmtime(path) * 1000 <= since:
                    continue
                if not read_tags(path)["title"]:
                    log.warning("se borra un M4A a medias: %s", path)
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
            except Exception as exc:  # un M4A dañado no frena la descarga: cuenta como ausente
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
