# PaloBox

Tu nube personal en un solo binario: fotos, videos, archivos y música, servidos desde tu
propio servidor y con una interfaz web que se instala en el celular como app (PWA).

- **Fotos y videos**: subidas grandes por partes, organizadas por fecha de captura, con
  miniaturas (incluye HEIC).
- **Archivos**: una carpeta privada por usuario.
- **Música**: biblioteca compartida con álbumes, artistas, búsqueda, favoritos, listas y cola
  de reproducción.
- **Dispositivos y jam**: eliges en cuál de tus dispositivos suena y lo controlas desde otro, o abres
  un jam para que otras personas escuchen lo mismo a la vez y manejen la cola contigo.
- **Descargas de Spotify** (opcional): pegas un enlace y un servicio aparte lo baja con
  [spotDL](https://github.com/spotDL/spotify-downloader). Ver [`spotdl/README.md`](spotdl/README.md).

Backend en Go con SQLite, interfaz en JavaScript sin frameworks ni paso de compilación.

## Instalación

Necesitas un servidor Linux con Docker y Docker Compose.

```sh
git clone https://github.com/breynerciro/palobox.git && cd palobox

# 1. Carpeta de datos, a nombre del usuario 1000 (el que usan los contenedores).
sudo mkdir -p /srv/data/media/musica /srv/data/.palobox/spotdl
sudo chown -R 1000:1000 /srv/data

# 2. Token compartido entre la app y el servicio de descargas.
mkdir -p secrets && chmod 700 secrets
openssl rand -hex 32 > secrets/spotdl.token

# 3. Configuración (opcional): administrador, carpeta de datos y zona horaria.
cp .env.example .env

# 4. Arrancar y crear el primer usuario (contraseña de 8 caracteres o más).
docker compose up -d --build
docker compose run --rm palobox adduser admin 'una-clave-larga'
```

La app queda en `http://127.0.0.1:8080`. El mismo comando `adduser` crea más usuarios o le
cambia la contraseña a uno que ya existe.

### Acceso desde otros dispositivos

El puerto solo se publica en `127.0.0.1`: la idea es ponerle HTTPS por delante, por ejemplo
con `tailscale serve 8080` o un proxy inverso (Caddy, nginx). Si solo la quieres en tu red
local, cambia `ports` en `compose.yml` a `"8080:8080"`.

### Sin descargas de Spotify

```sh
docker compose up -d --build palobox
```

Y quita `SPOTDL_URL` de `compose.yml`: la sección desaparece de la interfaz.

## Configuración

Variables de `.env`, todas opcionales:

| Variable | Defecto | Uso |
| --- | --- | --- |
| `ADMIN` | `admin` | usuario que ve el panel de administrador |
| `DATA_PATH` | `/srv/data` | carpeta del servidor donde viven los datos |
| `TZ` | `America/Bogota` | zona horaria |
| `DEPLOY_HOST` | — | `usuario@servidor` para `./deploy.sh` |

Dentro de la carpeta de datos:

```
<usuario>/          archivos y fotos de cada usuario
media/musica/       biblioteca de música, compartida
.palobox/           base de datos, miniaturas y temporales
```

Puedes copiar música directamente a `media/musica/`; la app la indexa sola.

## Desarrollo

```sh
go test ./...                              # app (algunas pruebas usan ffmpeg y vips)
cd spotdl && python3 -m unittest           # servicio de descargas, sin red
```

`./deploy.sh usuario@servidor` compila las imágenes en tu equipo y las envía por SSH, para
que el servidor no tenga que guardar el código ni compilar.

## Aviso

El módulo de descargas es para uso personal. Lo que bajes con él y el cumplimiento de los
términos de Spotify y YouTube y de las leyes de derechos de autor de tu país corren por tu
cuenta.

## Licencia

[MIT](LICENSE)
