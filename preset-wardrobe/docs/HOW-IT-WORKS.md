# How it works

Notes for anyone working on the code.

## The game's presets

skate. under ReSkate keeps the player's profile in SQLite at
`%LOCALAPPDATA%\ReSkate\profiles\offline\reskate.sqlite3` (WAL mode). The 10 skater presets
are the rows of `cosmetic_loadouts`, in menu order: `default`, `Loadout02` … `Loadout10`.
Each row's `recipes_json` is a list of groups: group 0 the skater (clothes, body, face),
group 1 the board, group 2 gestures. Each item is `{asset, parameter_bits, slot}`, where
`slot` is a numeric id (see `skaterSlots` and `boardSlots` in `profile.go`).
The selected preset is `selected_preset_index` in `profile_extensions` (section `customization`).

- **Reading** copies the database and its WAL to a temporary folder first, so the live file
  is never touched while the game runs.
- **Writing** happens only while `Skate.exe` isn't running (the game rewrites presets when it
  quits). The database, WAL and shared-memory files are copied to a backup first, then only
  `cosmetic_loadouts` rows are upserted in one transaction, and the WAL is checkpointed.

## Fits

A fit is one preset's recipes, filed under `outfitHash`: a SHA-1 over the sorted skater and
board items (gestures left out), so the same outfit is one fit however the JSON is ordered.
Pictures are filed under the same hash, so they follow an outfit into any rotation.

## The plugin (`PresetWardrobe/`)

A ReSkate Luau plugin. ReSkate names a plugin after its mod folder: `PresetWardrobe` from the zip,
`jrkmods-PresetWardrobe` from Thunderstore. Its data folder is `<game folder>\Mods\.reskate_plugin_data\<that name>\`;
the program finds it with `pluginFolder()` (any mod folder whose manifest is named PresetWardrobe).

| File | Written by | What |
|---|---|---|
| `snaps/fit-NNNNN.png`, `snaps/index.json` (`singles`) | plugin | F8 snaps, with the board parts read at the time |
| `snaps/01.png` … `10.png`, `index.json` (`shots`) | plugin | walk-through snaps by slot |
| `wanted.json` | program | item keys it wants names for |
| `titles.json` | plugin | those names, from `reskate.outfit.title` |
| `added.json` | program | messages the plugin shows in game ("Added … to your Fit Catalogue") |

On the Skaters page `reskate.outfit.worn()` comes back empty and only the board can be read,
so the program places an F8 snap by its deck, other board parts and the selected slot
(`guessSlot`), and asks the player when that isn't conclusive.

## The program's data

`%LOCALAPPDATA%\PresetWardrobe\`: `fits/<hash>.json`, `pictures/<hash>.png`,
`rotations/<id>.json`, `pending/` (snaps waiting for "which slot?"), `backups/<time>/`,
`config.json`, `titles.json`, `PresetWardrobe.log`.

The window is WebView2 showing a page served on `127.0.0.1` (`ui/index.html`). Without the
WebView2 runtime it opens in the default browser and quits when that page has been closed
for a while. POST requests from other origins are refused.

## Building

```sh
go test ./...
tests/plugin/run.sh                      # needs the luau CLI
GOOS=windows go build -ldflags "-H windowsgui" -o PresetWardrobe.exe .
scripts/build-release.sh 0.2.0           # what the release workflow runs: dist/
```

On Linux the program runs for testing: it serves the page and prints its address.
Set `PB_LOCALAPPDATA`, `PB_DATA` and `PB_PORT` to point it at test folders, and
`PB_GAME_RUNNING=1` to pretend the game is open.
