#!/bin/sh
# Сборка NOCTURNE для Windows: build-win.sh RESOURCES_DIR OUT_DIR
# RESOURCES_DIR — папка с app.js и index.html (как в Contents/Resources на Mac),
# иконка берётся из mac/Nocturne.icns (или RESOURCES_DIR/Nocturne.icns).
set -e
RES="${1:?resources}"; OUT="${2:?out}"
HERE="$(cd "$(dirname "$0")" && pwd)"
A="$HERE/cmd/nocturne-win/assets"
mkdir -p "$A" "$OUT"
cp "$RES/app.js" "$RES/index.html" "$A/"
ICNS="$RES/Nocturne.icns"; [ -f "$ICNS" ] || ICNS="$HERE/mac/Nocturne.icns"
python3 "$HERE/tools/winres.py" "$ICNS" "$A"
cp "$A/icon.png" "$A/nocturne.ico" "$OUT/" 2>/dev/null || true
mv "$A"/rsrc_windows_*.syso "$HERE/cmd/nocturne-win/"
cd "$HERE"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o "$OUT/NOCTURNE.exe" ./cmd/nocturne-win
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o "$OUT/NOCTURNE-ARM.exe" ./cmd/nocturne-win
ls -la "$OUT"
