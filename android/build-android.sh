#!/usr/bin/env bash
# Prepares the Android app: builds the library server (desktop/) for phones
# and copies the interface (app/index.html, app/app.js) into the APK assets.
# Then: ./gradlew assembleRelease (or open android/ in Android Studio).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(dirname "$here")"
jni="$here/app/src/main/jniLibs"
www="$here/app/src/main/assets/www"

rm -rf "$jni" "$www"
mkdir -p "$jni/arm64-v8a" "$jni/armeabi-v7a" "$jni/x86_64" "$www"

cd "$repo/desktop"
flags=(-trimpath -tags timetzdata -ldflags "-s -w -buildid=")
# Almost every phone: 64-bit ARM, built as a real Android program.
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build "${flags[@]}" -o "$jni/arm64-v8a/libnocturne.so" ./cmd/nocturne-server
# Older 32-bit phones and the x86_64 emulator: Go builds these only as plain
# Linux programs without the Android toolchain; they run on Android as well.
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build "${flags[@]}" -o "$jni/armeabi-v7a/libnocturne.so" ./cmd/nocturne-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build "${flags[@]}" -o "$jni/x86_64/libnocturne.so" ./cmd/nocturne-server

cp "$repo/app/index.html" "$repo/app/app.js" "$www/"
ls -la "$jni"/* "$www"
