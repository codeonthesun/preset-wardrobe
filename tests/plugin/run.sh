#!/usr/bin/env bash
# Runs PresetWardrobe against a fake ReSkate API. Needs the luau CLI (github.com/luau-lang/luau releases).
set -euo pipefail
cd "$(dirname "$0")"
LUAU="${LUAU:-luau}"
"${LUAU}-compile" --binary ../../PresetWardrobe/plugin/main.lua > /dev/null
{ cat fake_reskate.luau; echo "do"; cat ../../PresetWardrobe/plugin/main.lua; echo "end"; cat checks.luau; } > /tmp/presetwardrobe-test.luau
"$LUAU" /tmp/presetwardrobe-test.luau
