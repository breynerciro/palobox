#!/usr/bin/env bash
# Pruebas punta a punta (y de carga) ANTES de desplegar, en el servidor pero sin tocar la app real:
# levanta la imagen recién compilada en un contenedor temporal (puerto 8098), con una copia de la base, la
# música montada en solo lectura y un usuario de prueba que solo existe en esa copia. Al terminar lo borra todo.
# Uso: ./e2e.sh [usuario@servidor]        pruebas punta a punta (gasta unas 3 solicitudes de Gemini)
#      E2E_ADB=serial ./e2e.sh             lo mismo, pero dentro del Chrome de un celular Android conectado por adb
#      CARGA=1 ./e2e.sh [usuario@servidor] además, pruebas de carga con p95/p99 y consumo del servidor
# Necesita node y chromium aquí; el generador de carga (webtest/carga.go) se compila aquí y se copia al servidor.
set -euo pipefail
cd "$(dirname "$0")"
[ -f .env ] && . ./.env
HOST=${1:-${DEPLOY_HOST:?Uso: ./e2e.sh usuario@servidor (o DEPLOY_HOST en .env)}}
DIR=/srv/apps/palobox
DATA=${DATA_PATH:-/srv/data}
T=/tmp/palobox-e2e
PORT=8098
USER_E2E=e2eprueba
# La contraseña nace aquí, vive lo que dura la prueba y no se imprime.
export E2E_PASS=$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')
SSH="ssh -o ControlMaster=auto -o ControlPath=/tmp/palobox-e2e-%C -o ControlPersist=120"

docker build -q -t palobox:e2e . >/dev/null
SIG='{{json .Config}}{{.RootFS.Layers}}'
if [ "$(docker image inspect -f "$SIG" palobox:e2e)" != "$($SSH "$HOST" "docker image inspect -f '$SIG' palobox:e2e 2>/dev/null" || true)" ]; then
  echo "e2e: enviando la imagen de prueba"
  docker save palobox:e2e | $SSH "$HOST" "docker load" >/dev/null
fi

cleanup() {
  $SSH "$HOST" "docker rm -f palobox-e2e >/dev/null 2>&1; rm -rf $T; docker rmi palobox:e2e >/dev/null 2>&1" || true
  $SSH -O exit "$HOST" 2>/dev/null || true
}
trap cleanup EXIT

echo "e2e: levantando la copia de prueba en el servidor"
# La base se copia con la API de respaldo de SQLite (la app real sigue escribiendo en la suya). La música va en
# solo lectura: la copia no puede borrar ni renombrar un solo archivo (el índice quita duplicados del disco).
$SSH "$HOST" "set -e
  docker rm -f palobox-e2e >/dev/null 2>&1 || true; rm -rf $T; mkdir -p $T/.palobox $T/media/musica # el punto de montaje ya creado: si lo crea Docker queda de root
  python3 -c \"import sqlite3; s = sqlite3.connect('file:$DATA/.palobox/db.sqlite?mode=ro', uri=True); d = sqlite3.connect('$T/.palobox/db.sqlite'); s.backup(d); d.close()\"
  docker run --rm -v $T:/data palobox:e2e adduser $USER_E2E $E2E_PASS >/dev/null
  docker run -d --name palobox-e2e --cap-drop ALL --security-opt no-new-privileges:true --read-only --tmpfs /tmp -p 127.0.0.1:$PORT:8080 -v $T:/data -v $DATA/media/musica:/data/media/musica:ro \
    -v $DIR/secrets/gemini.key:/run/secrets/gemini_key:ro -e GEMINI_KEY_FILE=/run/secrets/gemini_key -e ADMIN=$USER_E2E -e TZ=${TZ:-America/Bogota} palobox:e2e >/dev/null
  for i in \$(seq 1 60); do wget -qO- http://127.0.0.1:$PORT/api/version >/dev/null 2>&1 && exit 0; sleep 1; done
  echo 'e2e: la copia de prueba no arrancó' >&2; docker logs --tail 20 palobox-e2e >&2; exit 1"

# Túnel: el navegador de aquí ve la copia como http://127.0.0.1:8098.
$SSH -f -N -L $PORT:127.0.0.1:$PORT -o ExitOnForwardFailure=yes "$HOST"
echo "e2e: pruebas punta a punta"
if [ -n "${E2E_ADB:-}" ]; then
  # En un celular Android conectado por adb (E2E_ADB = su serial): su Chrome ve la copia en su propio 127.0.0.1.
  adb -s "$E2E_ADB" reverse tcp:$PORT tcp:$PORT >/dev/null
  adb -s "$E2E_ADB" forward tcp:9335 localabstract:chrome_devtools_remote >/dev/null
  adb -s "$E2E_ADB" shell am start -a android.intent.action.VIEW -d "http://127.0.0.1:$PORT/" com.android.chrome >/dev/null
  export E2E_CDP=http://127.0.0.1:9335
fi
node webtest/e2e.mjs "http://127.0.0.1:$PORT/" "$USER_E2E"

if [ "${CARGA:-}" = 1 ]; then
  echo "e2e: pruebas de carga (el generador corre en el propio servidor: comparte sus 2 núcleos con la app)"
  CGO_ENABLED=0 go build -o /tmp/palobox-carga webtest/carga.go
  scp -q -o ControlPath=/tmp/palobox-e2e-%C /tmp/palobox-carga "$HOST:$T/carga"
  $SSH "$HOST" "T=$T PORT=$PORT USER_E2E=$USER_E2E E2E_PASS=$E2E_PASS USUARIOS='${USUARIOS:-55 110 220}' SEGUNDOS=${SEGUNDOS:-15} bash -s" < webtest/carga.sh
fi
