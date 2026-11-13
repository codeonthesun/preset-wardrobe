# Changelog

Versions follow [semantic versioning](https://semver.org). The release workflow copies the
section for a version into its GitHub release notes, so write each one for players.

## [0.3.1] - 2026-11-12

### Changed
- **Loading into the running game was rebuilt from the ground up.** A fit now goes straight to where skate. itself
  keeps your outfit, so your skater and board change at once, wherever you are: in the world, in menus, and on
  servers. No more replay refresh or black screen.
- **Loads stay put.** A loaded fit is what the game saves, so it's still there after a map change, joining or
  leaving a server, and restarting the game.
- Loads are much faster (a fraction of a second).
- Settings: "After loading into the running game" is now "After loading a fit into the game".
- And more smaller changes.

### Added
- **The Skaters page preview updates too** when you load while it's open.
- **Load into any slot**, not only the one you're wearing. A slot you haven't opened yet this session gets its new
  fit the moment you switch to it.
- **A note in the game after each load:** "Fit applied successfully.", or "Fit couldn't be applied. Check Preset
  Wardrobe for details." It waits until skate. is the window in front, so you won't miss it.
- The **"Loaded. Go back to skate.?"** prompt closes by itself after 8 seconds, with a bar showing the time left.
  It waits while your pointer is on it.
- And more.

### Fixed
- The board didn't change on servers or after a map change, and a map change could put your old fit back.
- Colors and options of loaded items are loaded too (new items used to keep the old items' settings).
- Saved fits could show as "not saved" with no pictures after the game saved them, when the fit had items that
  aren't installed.
- A piece from your previous fit (an arm bandage, say) could stay on when the new fit's item for that slot isn't
  installed. Headwear, eyewear, gloves and accessories are now left empty in that case.
- In-game notes from the app could stop showing after the app was restarted.
- And more smaller fixes.

### Known limits
- **All 10 in a row** is still switched off while it's being fixed.
- **Some features need a controller connected** (spinning snaps, and holding Y to snap).
- The first time you switch to a slot you loaded into this session, its old fit can show for a moment before the
  new one goes on.
- If a mod renames an item, fits saved with the old name show it as not installed. Snap those fits again.

## [0.3.0] - 2026-11-11

### Added
- **Load while skate. is running.** Load a rotation or a single fit with the game open: the slots change right
  away, and the skater on screen refreshes by itself once you're in the world with no menu open.
- **Undo history.** Every load can be undone. Before each load, the lineup it replaces is kept as a
  "Lineup before…" copy in Rotations.
- **Save file backups** (Settings). The app makes a complete copy of skate.'s save before every change it makes,
  and **Put back** restores one. Each change is checked after it's written and undone by itself if it didn't go
  in right.
- **Card on the Skaters page.** The plugin shows whether the preset on screen is in your Fit Catalogue, and lists
  its pieces beside the skater.
- **Hold Y on a controller** on the Skaters page to snap a preset that isn't saved yet, or a saved one without
  pictures. A bar fills while you hold.
- **Your own snap key.** Change it in skate.: Settings > Controls > Plugins' own binds (F8 to start with). The app
  and the plugin show whichever key you set, and the plugin warns you if another plugin uses the same key.
- **Updates.** The app checks GitHub for a newer version when it starts. **Update available** in the header
  downloads it, checks it and restarts. You can turn the check off in Settings > Updates.
- **Mix & match.** Make a new fit from one fit's skater and another fit's board.
- **Right-click a slot's picture** in the In-Game Lineup to swap in a fit, and right-click a picture in the Fit
  Catalogue to favorite it.
- **Spinning previews.** Every snap turns your skater a full circle, so backs, backpacks and the board are all in
  the pictures. Drag a picture to turn it. Snaps also take 4 pictures of the board.
- **HQ pictures.** Sharper copies of each fit's pictures are made slowly in the background, and previews use them
  once they're ready. Click **HQ** in the large viewer to compare with the original.
- **Skater + Board / Skater / Board** views in the Fit Catalogue.
- **Zoom** with the slider beside each picture, or Ctrl + wheel. **Lock preview?** keeps a fit's zoom, position
  and angle.
- **Resizable cards.** Drag a card's corner to make it wider or taller and show more pieces. Each screen remembers
  its own sizes.
- **Changed in game** on fits edited in skate. that haven't been saved to the catalogue.
- **Missing items** show in red, for pieces whose mod isn't installed any more.
- A **New** badge on fits added since you last looked.
- **(?)** help on each screen.

### Changed
- New icon for the app and the plugin.
- Pictures are captured at your screen's own size and saved larger, so they're much sharper. Fits snapped before
  keep their old pictures until you snap them again.
- Settings is split into sections: Game, Loading into the game, Pictures (with how much space they use and
  **Free up space**), Save file backups, Updates and Plugin.
- **Clean up and improve clarity of pictures** (Settings) now covers both the board's lighting and the HQ copies.
  Your original pictures are always kept.
- The sidebar can be resized or hidden.

### Fixed
- The skater drifted in some frames of a spin.
- The app could crash reading a save with many unsaved changes from the running game.
- A damaged settings file is set aside and replaced, instead of stopping the app from starting.

### Known limits
- **All 10 in a row** is switched off while it's being fixed. Snap your presets one at a time for now.
- **Some features need a controller connected.** skate. only turns the skater in the Skaters page preview with a
  controller, so without one a snap takes still pictures only (no spin), and holding Y to snap needs one too.

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
