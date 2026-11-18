# Descargas de Spotify

Servicio propio (no la imagen oficial de spotDL) que recibe enlaces de Spotify, los
resuelve con [spotDL](https://spotdl.github.io/spotify-downloader/) y deja los MP3 en
`/data/media/musica/{artista}/{álbum}/{título}.mp3`. La app Go lo consulta como proxy
autenticado; el servicio no publica puerto, solo se alcanza por la red de compose y exige
el token del módulo en cada petición (ver `../modules.go`).

## API que consume la interfaz (a través de la app)

Todas requieren sesión.

```
GET /api/spotdl
200 {"enabled": true, "items": [job, ...]}
200 {"enabled": false, "items": []}          # SPOTDL_URL sin configurar
503 "El servicio de descargas de Spotify no responde."

POST /api/spotdl   {"url": "https://open.spotify.com/..."}
200 job                                       # también si ya estaba en cola
400 "El enlace no es de Spotify."
429 "Ya tienes 2 descargas en cola; espera a que terminen."
429 "Hay demasiadas descargas en cola."
503

DELETE /api/spotdl/{id}
200 {"ok": true}                              # lo cancela si corre y lo quita de la lista; con un aviso, lo da por visto
400 "Trabajo no válido."
403 "Solo quien pidió esta descarga puede cancelarla o quitarla."
404

POST /api/spotdl/{id}/retry                   # sin cuerpo
200 job                                       # vuelve a la cola pidiendo solo lo que falló
400 "Trabajo no válido."
403 "Solo quien pidió esta descarga puede reintentarla."
404
409 "Esta descarga ya no se puede reintentar."
429                                           # mismos límites que un alta
```

```json
{
  "id": "a1b2c3d4e5f60708",
  "url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
  "title": "Mix para el carro",
  "status": "downloading",
  "current": "Daft Punk - One More Time",
  "total": 42,
  "done": 7,
  "progress": 0.1667,
  "error": "",
  "created": 1763400000000,
  "owner": "ana",
  "attempts": 1,
  "notice": "",
  "mine": true
}
```

- `status`: `queued | downloading` (corre), `error` (algo falló y se puede reintentar: la
  interfaz muestra `error` y «Reintentar»), `finished` (ya no hay nada que hacer).
- `finished` no es una fila: es un aviso para su dueño (`notice`, p. ej. «Tomá» ya está en tu
  biblioteca.) que la interfaz muestra una vez y borra con `DELETE`. Solo lo ve su dueño (los
  trabajos sin dueño, el administrador). Con fallos, `error` trae el resumen y el aviso va en tono de advertencia.
- `error` y `notice` son texto para el usuario («22 canciones no se pudieron descargar:
  Canción — Artista, …»). Nunca traen URLs, excepciones ni trazas; eso queda en el log del servicio.
- `attempts`: ejecuciones pedidas (1 la original); `> 1` es un reintento.
- `progress`: `0..1`, o `null` mientras no se conoce el total. En un reintento, `total` es el
  número de canciones que se vuelven a pedir.
- `owner`: usuario que la pidió; `mine` (lo calcula la app): si este usuario puede
  cancelarla, quitarla o reintentarla (su dueño o el administrador). La música descargada es de todos.
- Un `POST` repetido de una URL que ya está en cola devuelve el mismo trabajo.
- La interfaz consulta cada 4 s mientras haya trabajos activos (aunque cambie de vista, para
  que llegue el aviso) y no bloquea la biblioteca si responde `503`.

### Reintentos

- Máximo 3 ejecuciones por trabajo: la original, **un reintento automático** inmediato si
  quedó algo transitorio, y uno que pide el usuario. Vale la pena el automático: los fallos
  de yt-dlp suelen ceder al repetir y solo cuesta volver a pedir las fallidas.
- Un reintento pide a spotDL solo las URLs de canción que fallaron (spotDL acepta varias);
  las 124 que ya están no se vuelven a pedir. Si no se sabe cuáles fallaron (toda la
  ejecución falló, o un trabajo viejo sin su `errors-{id}.txt`), se repite el enlace entero y
  `--overwrite skip` salta lo descargado.
- Clasificación de cada línea de `--save-errors` (spotDL 4.6: `{url} - {Excepción}: {mensaje}`):
  - **Permanente** (no se reintenta): `LookupError` (sin resultados en el proveedor),
    `AudioProviderError: No metadata found…` (yt-dlp no obtuvo el video) y las líneas sin URL
    de canción (`Song is missing required fields`, `Error occurred while reinitializing song`),
    porque no hay cómo pedir solo esa.
  - **Transitorio**: todo lo demás (`AudioProviderError: YT-DLP download error`, ffmpeg,
    metadatos, red, límites). También es transitorio que la ejecución entera falle (código de
    salida, tiempo máximo).
- Sin nada transitorio, o sin intentos, el trabajo pasa a `finished` con un aviso que dice qué
  quedó sin descargar («3 canciones de «Tomá» no están disponibles: …»). Los nombres salen del
  `--save-file` que spotDL escribe al terminar, sin consultas extra.

Solo se aceptan enlaces `https://open.spotify.com/(intl-xx/)?{track|album|playlist|artist}/ID`.
No hay búsqueda de texto: spotDL resuelve el enlace y busca cada pista en YouTube.

## Servicio interno

Toda petición, incluida `/health`, lleva `Authorization: Bearer <token>` (si no, 401).
`GET /jobs`, `POST` y `DELETE` además llevan `X-PaloBox-User: <usuario>` y, si es el administrador,
`X-PaloBox-Admin: 1`; la app los pone después de validar la sesión.

| Método | Ruta | Uso |
| --- | --- | --- |
| GET | `/health` | `{"ok":true,"active":N,"spotdl":"4.6.0","deno":"..."}`; 503 si falta spotDL |
| GET | `/jobs` | lista de trabajos |
| POST | `/jobs` | `{"url": ...}` → 201 job |
| POST | `/jobs/{id}/retry` | 200 job en cola; 403 ajeno, 409 si no está en `error` o sin intentos, 429 por límites |
| DELETE | `/jobs/{id}` | cancela (mata spotDL, ffmpeg e yt-dlp) y quita de la lista, o descarta un aviso |

La cola vive en `/data/.palobox/spotdl/jobs.sqlite` (WAL). Al arrancar, los trabajos
`queued`/`downloading` vuelven a la cola y spotDL salta lo ya descargado
(`--overwrite skip`). Se conservan los últimos 30 terminados (filas `error` y avisos
`finished` sin ver) y hay un timeout de 12 h.

La base migra sola al arrancar (`PRAGMA user_version`): agrega las columnas `attempts`,
`failed` y `notice` si faltan y, una sola vez, convierte los terminados del formato viejo: un
`done` sin errores se borra (ya se vio), un `done` con canciones fallidas o un `error` quedan
en `error` reintentables (las URLs salen de `errors-{id}.txt` si existe). `queued` y
`downloading` no se tocan.

## Variables de entorno

| Variable | Defecto | Uso |
| --- | --- | --- |
| `DATA_DIR` | `/data` | raíz compartida con la app |
| `PORT` | `8801` | puerto interno |
| `THREADS` | `2` | hilos de spotDL |
| `TOKEN_FILE` | `/run/secrets/spotdl_token` | token compartido con la app; sin él no arranca |
| `MAX_ACTIVE` | `5` | trabajos en cola a la vez |
| `MAX_PER_USER` | `2` | trabajos en cola a la vez por usuario |
| `JOB_TIMEOUT` | `43200` | segundos por trabajo |
| `SPOTDL_BIN` | `spotdl` | binario (los tests usan uno falso) |
| `SPOTIFY_CLIENT_ID` / `SPOTIFY_CLIENT_SECRET` | — | credenciales propias de Spotify; se pasan a spotDL por su `config.json` (0600), nunca por la línea de comandos |
| `SPOTDL_COOKIE_FILE` | — | `cookies.txt` de YouTube si aparecen bloqueos |

## Desarrollo

```sh
python3 -m unittest -v        # pruebas del servicio, sin red
docker build -t palobox-spotdl . # imagen propia
```

El despliegue normal lo hace `../deploy.sh`, que compila y transfiere esta imagen junto
con la de la app. Para actualizar spotDL cuando YouTube cambie, sube la versión en
`requirements.txt` y vuelve a desplegar.
