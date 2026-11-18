#!/usr/bin/env bash
# Compila aquí y envía al servidor solo las imágenes y compose.yml.
# Uso: ./deploy.sh [usuario@servidor]
# pipefail: sin él, un fallo de `docker save` queda tapado por el resto del pipe.
set -euo pipefail
HOST=${1:-breynerciro@192.168.0.18}
DIR=/srv/apps/palobox
# Una sola conexión SSH para todo: la contraseña se pide una vez.
SSH="ssh -o ControlMaster=auto -o ControlPath=/tmp/palobox-deploy-%C -o ControlPersist=60"
cd "$(dirname "$0")"

docker build -t palobox .
docker build -t palobox-spotdl spotdl
docker save palobox palobox-spotdl | gzip | $SSH "$HOST" "mkdir -p $DIR && docker load"
$SSH "$HOST" "cat > $DIR/compose.yml" < compose.yml
# Token app↔spotDL: se genera una sola vez en el servidor y nunca sale de ahí.
# También las carpetas que monta spotDL: si faltan, Docker las crearía como root.
$SSH "$HOST" "set -e; cd $DIR && mkdir -p secrets && chmod 700 secrets
  [ -s secrets/spotdl.token ] || (umask 022; head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \\n' > secrets/spotdl.token)
  mkdir -p /srv/data/media/musica /srv/data/.palobox/spotdl"
# --wait espera al healthcheck de compose.yml; el límite evita quedarse colgado si la app se reinicia en bucle.
if ! $SSH "$HOST" "cd $DIR && docker compose up -d --remove-orphans --wait --wait-timeout 180"; then
  echo "deploy: la versión nueva no arrancó. La imagen anterior NO se borró." >&2
  echo "Logs: ssh $HOST 'cd $DIR && docker compose logs --tail 50'" >&2
  exit 1
fi
# prune: borra la versión anterior de la imagen, que queda sin etiqueta tras cada despliegue.
$SSH "$HOST" "docker image prune -f"
