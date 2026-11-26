#!/usr/bin/env bash
# Genera el APK de PaloBox para Android / Google TV. Uso: ./build.sh  → app-release-signed.apk
# Bubblewrap regenera el proyecto desde twa-manifest.json y borra los cambios hechos a mano,
# por eso lo de TV (ícono en el inicio y banner) se vuelve a aplicar aquí en cada compilación.
set -euo pipefail
cd "$(dirname "$0")"
export JAVA_HOME=${JAVA_HOME_17:-$(mise where java@temurin-17)} # Bubblewrap exige JDK 17
export BUBBLEWRAP_KEYSTORE_PASSWORD=$(cat keystore.pass) BUBBLEWRAP_KEY_PASSWORD=$(cat keystore.pass)
BW="npx -y @bubblewrap/cli@1.27.0"
M=app/src/main/AndroidManifest.xml

$BW update --skipVersionUpgrade
# singleTask: sin él, cada vez que se abre la app se apila otro WebView y los dos suenan a la vez, quitándose el audio el uno al otro.
# tv/: la actividad propia del televisor (no pausa el WebView si hay música) y el servicio que mantiene viva la app en el fondo.
cp tv/*.java app/src/main/java/org/dpdns/palobox/twa/
# Banner 320x180: sin él Google TV no muestra la app en el inicio.
magick -size 320x180 xc:'#090D16' \( ../web/icon-512.png -resize 96x96 \) -gravity west -geometry +28+0 -composite \
  -fill white -font "$(fc-match -f %{file} sans:bold)" -pointsize 34 -gravity west -annotate +140+0 PaloBox app/src/main/res/drawable-xhdpi/banner.png
sed -i 's|android:icon="@mipmap/ic_launcher"|& android:banner="@drawable/banner"|
  s|<category android:name="android.intent.category.LAUNCHER" />|&<category android:name="android.intent.category.LEANBACK_LAUNCHER" />|
  s|</application>|<activity android:name=".TvActivity" android:configChanges="orientation\|screenSize" android:launchMode="singleTask"/><service android:name=".PlaybackService" android:exported="false" android:foregroundServiceType="mediaPlayback"/>\n    &|
  s|<application|<uses-permission android:name="android.permission.FOREGROUND_SERVICE"/><uses-permission android:name="android.permission.FOREGROUND_SERVICE_MEDIA_PLAYBACK"/><uses-feature android:name="android.software.leanback" android:required="false"/><uses-feature android:name="android.hardware.touchscreen" android:required="false"/>\n    &|' $M
grep -q LEANBACK_LAUNCHER $M && grep -q 'android:banner' $M && grep -q software.leanback $M && grep -q singleTask $M && grep -q PlaybackService $M || { echo "build: no se aplicó el parche de TV" >&2; exit 1; }
$BW build --skipPwaValidation
$BW fingerprint list 2>/dev/null || true
