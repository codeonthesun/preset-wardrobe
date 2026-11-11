#!/usr/bin/env bash
# Runs PresetWardrobe against a fake ReSkate API. Needs the luau CLI (github.com/luau-lang/luau releases).
set -euo pipefail
cd "$(dirname "$0")"
LUAU="${LUAU:-luau}"
"${LUAU}-compile" --binary ../../PresetWardrobe/plugin/main.lua > /dev/null
# The checks cover the features that are switched off for now too, so they're switched on here.
{ cat fake_reskate.luau; echo "do"
  sed -e 's/^local PICTURE_REQUESTS = false/local PICTURE_REQUESTS = true/' \
      -e 's/^local UNSAVED_CHECK = false/local UNSAVED_CHECK = true/' \
      -e 's/^local DEV_TOOLS = false/local DEV_TOOLS = true/' \
      -e 's/^local WALKTHROUGH = false/local WALKTHROUGH = true/' ../../PresetWardrobe/plugin/main.lua
  echo "end"; cat checks.luau; } > /tmp/presetwardrobe-test.luau
"$LUAU" /tmp/presetwardrobe-test.luau
