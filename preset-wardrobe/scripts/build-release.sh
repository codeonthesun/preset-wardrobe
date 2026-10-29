#!/usr/bin/env bash
# Builds everything a release ships, into dist/:
#   PresetWardrobe.exe, PresetWardrobe-plugin.zip, PresetWardrobe-<version>.zip, SHA256SUMS.txt
# Usage: scripts/build-release.sh 0.2.0
# Needs Go, zip and sha256sum. GO_WINRES can point at a go-winres binary; otherwise it is run with "go run".
set -euo pipefail

VERSION="${1:?usage: scripts/build-release.sh <version, e.g. 0.2.0>}"
VERSION="${VERSION#v}"
cd "$(dirname "$0")/.."
ROOT="$PWD"
DIST="$ROOT/dist"
rm -rf "$DIST" && mkdir -p "$DIST"

# The plugin's manifest and code carry the same version as the app, for this build only:
# the files are put back as they were when the script ends.
STASH="$(mktemp -d)"
cp PresetWardrobe/manifest.json PresetWardrobe/plugin/main.lua "$STASH/"
restore() { cp "$STASH/manifest.json" PresetWardrobe/manifest.json; cp "$STASH/main.lua" PresetWardrobe/plugin/main.lua; rm -rf "$STASH"; }
trap restore EXIT
python3 - "$VERSION" <<'PY'
import json, re, sys
v = sys.argv[1]
if not re.fullmatch(r"\d+\.\d+\.\d+", v):
    sys.exit("Thunderstore needs a plain x.y.z version, got " + v)
p = "PresetWardrobe/manifest.json"
m = json.load(open(p))
m["version_number"] = v
open(p, "w").write(json.dumps(m, indent=4) + "\n")
p = "PresetWardrobe/plugin/main.lua"
src = open(p).read()
src, n = re.subn(r'local VERSION = "[^"]*"', 'local VERSION = "%s"' % v, src, count=1)
if n != 1:
    sys.exit("couldn't find VERSION in " + p)
open(p, "w").write(src)
PY

# Icon, version info and the Windows manifest for the .exe.
WINRES="${GO_WINRES:-go run github.com/tc-hib/go-winres@v0.3.3}"
$WINRES simply --arch amd64 --out rsrc --manifest gui --icon assets/icon.png \
  --product-name "Preset Wardrobe" --file-description "Preset Wardrobe for ReSkate" \
  --product-version "$VERSION" --file-version "$VERSION" --original-filename PresetWardrobe.exe \
  --copyright "(c) 2026 jrkmods (codeonthesun)"

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-H windowsgui -s -w -X main.appVersion=$VERSION" -o "$DIST/PresetWardrobe.exe" .
rm -f rsrc_windows_amd64.syso

(cd "$ROOT" && zip -qr "$DIST/PresetWardrobe-plugin.zip" PresetWardrobe)
(cd "$DIST" && cp "$ROOT/docs/QUICKSTART.txt" . && \
  zip -q "PresetWardrobe-$VERSION.zip" PresetWardrobe.exe PresetWardrobe-plugin.zip QUICKSTART.txt && rm QUICKSTART.txt)
(cd "$DIST" && sha256sum PresetWardrobe.exe PresetWardrobe-plugin.zip "PresetWardrobe-$VERSION.zip" > SHA256SUMS.txt)
cat "$DIST/SHA256SUMS.txt"
