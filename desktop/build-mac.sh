#!/bin/bash
# Builds NOCTURNE.app (Apple Silicon + Intel) without Xcode:
#   the window: C + Objective-C runtime, clang + ld64.lld against .tbd stubs
#   the server: Go, cross-compiled
# Usage: ./build-mac.sh <folder with index.html and app.js, e.g. ../app> <output dir>
# The icon is taken from that folder or, if it is not there, from mac/Nocturne.icns.
set -euo pipefail
export PATH=/usr/lib/llvm-18/bin:$PATH
HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="${1:?resources folder}"
OUT="${2:?output folder}"
VERSION="${VERSION:-1.0 RC3.8.21 beta}"
BUILD="${BUILD:-10500}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

APP="$OUT/NOCTURNE.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

# 1. Window
for arch in arm64 x86_64; do
  clang -target $arch-apple-macos12 -std=c11 -fblocks -fno-stack-protector -fno-builtin -O2 \
        -Wall -Wno-unused-parameter -Wno-unused-function -c "$HERE/mac/shell.c" -o "$TMP/shell-$arch.o"
  mkdir -p "$TMP/$arch"
  ld64.lld -arch $arch -platform_version macos 12.0 14.0 -dead_strip -o "$TMP/$arch/NOCTURNE" "$TMP/shell-$arch.o" \
        "$HERE/mac/tbd/libSystem.tbd" "$HERE/mac/tbd/libobjc.tbd" "$HERE/mac/tbd/Foundation.tbd" \
        "$HERE/mac/tbd/AppKit.tbd" "$HERE/mac/tbd/WebKit.tbd"
done
llvm-lipo -create "$TMP/arm64/NOCTURNE" "$TMP/x86_64/NOCTURNE" -output "$APP/Contents/MacOS/NOCTURNE"

# 2. Library server
cd "$HERE"
mkdir -p "$TMP/go-arm64" "$TMP/go-amd64"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$TMP/go-arm64/nocturne-server" ./cmd/nocturne-server
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$TMP/go-amd64/nocturne-server" ./cmd/nocturne-server
llvm-lipo -create "$TMP/go-arm64/nocturne-server" "$TMP/go-amd64/nocturne-server" -output "$APP/Contents/MacOS/nocturne-server"
chmod 755 "$APP/Contents/MacOS/"*

# 3. Resources
cp "$SRC/index.html" "$SRC/app.js" "$APP/Contents/Resources/"
ICNS="$SRC/Nocturne.icns"; [ -f "$ICNS" ] || ICNS="$HERE/mac/Nocturne.icns"
cp "$ICNS" "$APP/Contents/Resources/Nocturne.icns"
sed -e "s/@VERSION@/$VERSION/" -e "s/@BUILD@/$BUILD/" "$HERE/mac/Info.plist" > "$APP/Contents/Info.plist"
printf 'APPL????' > "$APP/Contents/PkgInfo"
echo "built $APP"
