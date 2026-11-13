// Package presetwardrobe holds the ReSkate plugin's files (the PresetWardrobe folder beside this file), so the app
// (in app/) can carry the plugin and install it. The app itself is in app/.
package presetwardrobe

import "embed"

// PluginFS is the PresetWardrobe plugin folder, as it ships.
//
//go:embed all:PresetWardrobe
var PluginFS embed.FS
