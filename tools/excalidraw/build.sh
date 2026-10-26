#!/bin/sh
# Rebuilds internal/web/static/vendor/excalidraw from npm: Excalidraw (MIT) with React, one ES module and its chunks,
# the CSS, the fonts and the Russian translation. The CJK font Xiaolai (13 MB) is left out — the browser falls back to a system font.
# Run from the repository root: tools/excalidraw/build.sh
set -e
VER=0.18.1
OUT=internal/web/static/vendor/excalidraw
W=$(mktemp -d)
cp tools/excalidraw/entry.js "$W/"
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$W":/w -w /w node:22-alpine sh -c "
  npm init -y >/dev/null &&
  npm install --silent --no-audit --no-fund @excalidraw/excalidraw@$VER react@19.2.0 react-dom@19.2.0 esbuild@0.25.10 &&
  npx esbuild entry.js --bundle --splitting --format=esm --minify --conditions=production --outdir=out --entry-names=excalidraw \
    --chunk-names=[name]-[hash] --asset-names=[name]-[hash] --loader:.woff2=file --define:process.env.NODE_ENV='\"production\"' &&
  cp -r node_modules/@excalidraw/excalidraw/dist/prod/fonts out/fonts && rm -rf out/fonts/Xiaolai"
rm -rf "$OUT"
mkdir -p "$OUT"
cp -r "$W/out/." "$OUT/"
# only the Russian translation is kept (English is built in): the others are 1.6 MB the menu never offers
find "$OUT" -maxdepth 1 -regex '.*/[a-z][a-z][a-z]?-[A-Z][A-Z]-[A-Z0-9]*-[A-Z0-9]*\.js' ! -name 'ru-RU-*' -delete
printf "Excalidraw %s — MIT, (c) 2020 Excalidraw (github.com/excalidraw/excalidraw)\nReact 19.2.0 — MIT, (c) Meta Platforms, Inc.\n" "$VER" > "$OUT/VERSION"
rm -rf "$W"
du -sh "$OUT"
