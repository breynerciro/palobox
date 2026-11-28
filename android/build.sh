#!/usr/bin/env bash
# Genera el APK de PaloBox para Android (teléfono y Google TV). Uso: ./build.sh  → app-release-signed.apk
# Se firma con la misma llave de siempre (android.keystore): así se instala encima de la versión anterior.
set -euo pipefail
cd "$(dirname "$0")"
export JAVA_HOME=${JAVA_HOME_17:-$(mise where java@temurin-17)}
export ANDROID_HOME=${ANDROID_HOME:-$HOME/Android/Sdk}
export PASS=$(cat keystore.pass)
BT=$(ls -d "$ANDROID_HOME"/build-tools/* | sort -V | tail -1)
./gradlew -q assembleRelease
"$BT/zipalign" -f -p 4 app/build/outputs/apk/release/app-release-unsigned.apk app-release-unsigned-aligned.apk
"$BT/apksigner" sign --ks android.keystore --ks-key-alias palobox --ks-pass env:PASS --key-pass env:PASS \
  --out app-release-signed.apk app-release-unsigned-aligned.apk
echo "listo: $(pwd)/app-release-signed.apk"
