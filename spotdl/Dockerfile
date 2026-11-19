# Servicio de descargas de Spotify para PaloBox.
# Imagen propia (no la oficial): solo expone la API que consume la app por la red
# de compose, sin publicar el puerto ni guardar credenciales de Spotify.
FROM python:3.12-alpine

# ffmpeg convierte y etiqueta el audio (incluye ffprobe y el codificador MP3).
# tini reparte señales y recoge los procesos hijos (spotDL lanza ffmpeg e yt-dlp).
# deno lo usa yt-dlp para resolver YouTube sin tantos bloqueos.
RUN apk add --no-cache ffmpeg ca-certificates tzdata tini deno

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY server.py store.py library.py results.py spotdl_fast.py ./

# spotdl_fast.py es spotDL con caché de álbumes y artistas (ver el archivo).
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 TZ=America/Bogota DATA_DIR=/data PORT=8801 SPOTDL_BIN=/app/spotdl_fast.py \
    HOME=/data/.palobox/spotdl/home
EXPOSE 8801
# Mismo usuario que la app y el servidor: los MP3 quedan visibles por Samba.
USER 1000:1000
ENTRYPOINT ["/sbin/tini", "--", "python3", "-u", "/app/server.py"]
