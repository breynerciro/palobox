"""Lo que dice spotDL y lo que se le dice al usuario: la salida en vivo (Feed), las canciones fallidas de
--save-errors y las reglas que deciden si un trabajo se reintenta o termina, con sus textos."""

import collections
import json
import logging
import re

log = logging.getLogger("spotdl.servicio")

FOUND = re.compile(r"^Found (\d+) songs? in (.+?) \([A-Za-z]+\)$")
COMPLETE = re.compile(r"^(\d+)/(\d+) complete$")
DOWNLOADED = re.compile(r'^Downloaded "(.*)": ')
ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
# Una línea de --save-errors de spotDL 4.6 (Downloader.async_search_and_download):
# "{song.url} - {Excepción}: {mensaje}". Las de reinit_song o campos faltantes no traen URL.
TRACK_ERROR = re.compile(r"^(https://open\.spotify\.com/track/[A-Za-z0-9]+) - (\w+): (.*)$")
DATE_LINE = re.compile(r"^\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}$")


class Feed:
    """Lee la salida de spotDL y va acumulando título, total y avance."""

    def __init__(self):
        self.title = ""
        self.current = ""
        self.total = 0
        self.done = 0
        self.tail = collections.deque(maxlen=6)  # últimas líneas, para explicar un fallo

    def line(self, line):
        """Procesa una línea; devuelve True si cambió algo que valga guardar."""
        text = ANSI.sub("", line).strip()
        if not text:
            return False
        self.tail.append(text)
        changed = False

        m = FOUND.match(text)
        if m:
            total, title = int(m.group(1)), m.group(2).strip()
            if total and total != self.total:
                self.total, changed = total, True
            if title and title != self.title:
                self.title, changed = title, True

        m = COMPLETE.search(text)
        if m:
            done, total = int(m.group(1)), int(m.group(2))
            if total != self.total:
                self.total, changed = total, True
            if done != self.done:
                self.done, changed = done, True

        m = DOWNLOADED.match(text)
        if m:
            current = m.group(1).strip()
            if current != self.current:
                self.current, changed = current, True
            if not self.title:  # canción suelta: spotDL no anuncia una lista
                self.title, changed = current, True
        return changed


def permanent(kind, message):
    """Fallos que reintentar no arregla: sin resultados en el proveedor (LookupError de
    Downloader.search), yt-dlp sin datos del video, o un video que no es de un canal oficial
    (spotdl_fast.py). Lo demás (YT-DLP download error, ffmpeg, metadatos, red) suele ceder al reintentar."""
    return kind == "LookupError" or (
        kind == "AudioProviderError" and message.startswith(("No metadata found", "Unofficial source")))


def failures_from(path, names=None):
    """Canciones fallidas según --save-errors. El detalle técnico va al log, nunca a la interfaz."""
    try:
        with open(path, encoding="utf-8") as f:
            lines = [l.strip() for l in f if l.strip()]
    except OSError:
        return []
    out = []
    for line in lines:
        if DATE_LINE.match(line):  # cabecera de cada ejecución (el archivo se abre en modo append)
            continue
        log.warning("canción fallida: %s", line)
        m = TRACK_ERROR.match(line)
        if not m:  # sin URL no hay cómo pedir solo esa canción
            out.append({"url": "", "name": "", "retry": False})
            continue
        url = m.group(1)
        out.append({"url": url, "name": (names or {}).get(url, ""), "retry": not permanent(m.group(2), m.group(3))})
    return out


def song_names(path):
    """url → «Canción — Artista» del --save-file que spotDL escribe al final (sin pedir nada más)."""
    try:
        with open(path, encoding="utf-8") as f:
            songs = json.load(f)
        return {s["url"]: f"{s['name']} — {s['artist']}" for s in songs if s.get("url") and s.get("name")}
    except (OSError, ValueError, TypeError, KeyError):
        return {}


def songs_text(n):
    return "1 canción" if n == 1 else f"{n} canciones"


def names_text(failed, limit=3):
    shown = [f["name"] for f in failed if f["name"]][:limit]
    if not shown:
        return ""
    rest = len(failed) - len(shown)
    return ", ".join(shown) + (f" y {rest} más" if rest else "")


def row_text(failed):
    """Mensaje de una fila reintentable."""
    if not failed:
        return "No se pudo completar la descarga."
    n = len(failed)
    text = songs_text(n) + (" no se pudo descargar" if n == 1 else " no se pudieron descargar")
    names = names_text(failed)
    return f"{text}: {names}" if names else text


def outcome(title, url, failed, run_failed, attempts, max_attempts, skipped=0):
    """(status, error, notice) tras una ejecución: reintentable mientras quede algo transitorio
    y haya intentos; si no, finished con el aviso para su dueño."""
    if (run_failed or any(f["retry"] for f in failed)) and attempts < max_attempts:
        return "error", row_text(failed), ""
    status, error, notice = finished(title, url, failed, run_failed)
    if skipped:
        notice += f" {songs_text(skipped)} ya " + ("estaba" if skipped == 1 else "estaban") + " en la biblioteca."
    return status, error, notice


def finished(title, url, failed, run_failed):
    name = title or (failed[0]["name"] if failed else "") or "tu descarga"
    if not failed and not run_failed:
        return "finished", "", f"«{name}» ya está en tu biblioteca."
    # Con fallos, error conserva el resumen: la interfaz lo usa para el tono del aviso.
    if not failed:
        return "finished", row_text(failed), f"No se pudo descargar «{name}». Puedes pedirla de nuevo más tarde."
    n = len(failed)
    gone = not any(f["retry"] for f in failed)
    verb = ("no está disponible" if n == 1 else "no están disponibles") if gone else (
        "no se pudo descargar" if n == 1 else "no se pudieron descargar")
    if "/track/" in url:
        return "finished", row_text(failed), f"«{name}» {verb}."
    names = names_text(failed)
    return "finished", row_text(failed), f"{songs_text(n)} de «{name}» {verb}" + (f": {names}." if names else ".")
