#!/usr/bin/env bash
# Prueba de carga contra la copia de prueba que levanta ../e2e.sh; corre EN el servidor (lo envía e2e.sh).
# Cada «usuario» hace una petición por segundo durante SEGUNDOS: mucho más de lo que hace una persona oyendo
# música (una cada varios segundos), así que es el peor caso. Mide p50/p95/p99 por ruta y el consumo del
# contenedor de prueba y del real mientras tanto. No prueba las playlists con Gemini: su límite es la cuota.
set -euo pipefail
B=http://127.0.0.1:$PORT
COOKIE=$(curl -s -o /dev/null -c - -H 'Content-Type: application/json' -H 'Sec-Fetch-Site: same-origin' \
  -d "{\"name\":\"$USER_E2E\",\"pass\":\"$E2E_PASS\"}" $B/api/login | awk '$1 !~ /^#/ || /#HttpOnly/ { if (NF >= 7) print $6 "=" $7 }' | head -1)
[ -n "$COOKIE" ] || { echo "carga: no se pudo entrar"; exit 1; }
get() { curl -s -H "Cookie: $COOKIE" "$B$1"; }
enc() { python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "$1"; }
# Una pista real de un género grande, para que las sugerencias hagan todo su trabajo.
SEED=$(get '/api/music/search?q=grupo%20niche' | python3 -c 'import json, sys; print(json.load(sys.stdin)["tracks"][0]["path"])')
ARTIST=${SEED%%/*}
ALBUM=${SEED%/*}
declare -A RUTA=(
  [sugerencias]="/api/music/radio?path=$(enc "$SEED")"
  [buscar]="/api/music/search?q=amor"
  [inicio]="/api/music?catalog=1" # lo del usuario; el catálogo común va aparte y el navegador lo guarda
  [artista]="/api/music/artist?path=$(enc "$ARTIST")"
  [album]="/api/music/album?path=$(enc "$ALBUM")"
)
stats() { docker stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}' palobox-e2e palobox-palobox-1 2>/dev/null | awk '{ printf "%s cpu=%s mem=%s  ", $1, $2, $3 }'; }
echo "servidor: $(nproc) hilos, carga $(cut -d' ' -f1-3 /proc/loadavg), en reposo: $(stats)"
printf '%-12s %8s %8s %9s %9s %9s %9s %7s  %s\n' ruta usuarios pet/s p50 p95 p99 máximo errores "consumo a mitad de la prueba"
for u in $USUARIOS; do
  for name in sugerencias buscar inicio artista album; do
    ( sleep $((SEGUNDOS / 2)); stats > $T/stats.txt ) &
    read -r rps p50 p95 p99 max errs < <($T/carga -z $SEGUNDOS -c $u -q 1 -cookie "$COOKIE" -url "$B${RUTA[$name]}")
    wait
    printf '%-12s %8s %8s %6s ms %6s ms %6s ms %6s ms %7s  %s\n' $name $u $rps $p50 $p95 $p99 $max $errs "$(cat $T/stats.txt)"
  done
done
echo "al terminar: carga $(cut -d' ' -f1-3 /proc/loadavg), $(stats)"
