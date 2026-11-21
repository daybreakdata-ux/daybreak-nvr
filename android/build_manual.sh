#!/bin/bash
# Manual build for Daybreak NVR (Android wrapper, Phase 2).
# Plain Java, no Gradle: aapt2 + javac + d8, same pattern as the
# other apps in this workspace. Produces a signed APK.
set -euo pipefail
cd "$(dirname "$0")"

BT=/home/hatch/Android/Sdk/build-tools/34.0.0
PLAT=/home/hatch/Android/Sdk/platforms/android-34/android.jar
export JAVA_HOME=/home/hatch/tools/jdk-17.0.21+9
export PATH=$JAVA_HOME/bin:$PATH
JAVAC=$JAVA_HOME/bin/javac
KEYTOOL=$JAVA_HOME/bin/keytool
B=build/manual
rm -rf "$B"
mkdir -p "$B/gen" "$B/classes" "$B/dex"

echo "== aapt2 compile+link =="
$BT/aapt2 compile --dir app/src/main/res -o "$B/res.zip"
$BT/aapt2 link -o "$B/base.apk" -I "$PLAT" \
    --manifest app/src/main/AndroidManifest.xml \
    --java "$B/gen" \
    --min-sdk-version 26 --target-sdk-version 34 \
    --version-code 4 --version-name 1.0.3 \
    "$B/res.zip"

echo "== javac =="
find app/src/main/java "$B/gen" -name '*.java' > "$B/sources.txt"
$JAVAC -source 17 -target 17 -encoding UTF-8 \
    -cp "$PLAT" \
    -d "$B/classes" @"$B/sources.txt"

echo "== d8 =="
$JAVA_HOME/bin/jar cf "$B/app-classes.jar" -C "$B/classes" .
$BT/d8 --min-api 26 --lib "$PLAT" --output "$B/dex" "$B/app-classes.jar"

echo "== package =="
cp "$B/base.apk" "$B/daybreak-nvr-unsigned.apk"
cp "$B/dex/classes.dex" "$B/classes.dex"
(cd "$B" && zip -q -0 daybreak-nvr-unsigned.apk classes.dex)

echo "== zipalign + sign =="
KS=/home/hatch/.android/debug.keystore
if [ ! -f "$KS" ]; then
    mkdir -p /home/hatch/.android
    $KEYTOOL -genkeypair -keystore "$KS" -storepass android -keypass android \
        -alias androiddebugkey -keyalg RSA -keysize 2048 -validity 10000 \
        -dname "CN=Android Debug,O=Android,C=US" > /dev/null 2>&1
fi
$BT/zipalign -f -p 4 "$B/daybreak-nvr-unsigned.apk" "$B/daybreak-nvr-aligned.apk"
$BT/apksigner sign --ks "$KS" --ks-pass pass:android --key-pass pass:android \
    --out "$B/daybreak-nvr.apk" "$B/daybreak-nvr-aligned.apk"

cp "$B/daybreak-nvr.apk" ./daybreak-nvr.apk

echo "== done =="
ls -lh "$B/daybreak-nvr.apk" ./daybreak-nvr.apk
