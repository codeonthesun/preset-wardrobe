# Changelog

Versions follow [semantic versioning](https://semver.org). The release workflow copies the
section for a version into its GitHub release notes, so write each one for players.

## [Unreleased]

## [0.2.0] - 2026-10-29

First public release.

### Preset Wardrobe (the program)
- **Fit Catalogue**: every outfit you save, with a picture, searchable by name or piece.
- **Rotations**: pick up to 10 fits, one per slot, and load them into the game with skate. closed.
  Empty slots keep what's in the game.
- **In-Game Lineup**: the 10 presets in the game right now, with "Add to catalogue".
- Before every load the whole profile is backed up, and the lineup is kept as a before-load copy.
  Only the 10 presets are ever changed.
- Fits and rotations can be shared as `.wardrobe` files.

### PresetWardrobe (the in-game plugin)
- **F8** on the Skaters page snaps the selected preset into the Fit Catalogue.
- Walk-through mode snaps all 10 slots in a row.
- Framing box and test picture to line up the shot.
