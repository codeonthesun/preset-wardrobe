package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHotkeyConflicts(t *testing.T) {
	game, local := t.TempDir(), t.TempDir()
	mods := filepath.Join(game, "Mods")
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	put(filepath.Join(mods, "mods.json"), `{"mods":[{"enabled":true,"name":"IdolRiot-FitCheck"},{"enabled":true,"name":"jrkmods-True_Darkness"},{"enabled":false,"name":"Other-Off"},{"enabled":true,"name":"PresetWardrobe"}],"schema":1}`)
	for _, n := range []string{"IdolRiot-FitCheck", "jrkmods-True_Darkness", "Other-Off", "PresetWardrobe"} {
		put(filepath.Join(mods, n, "reskate-plugin.json"), `{"api":1}`)
	}
	put(filepath.Join(mods, "IdolRiot-FitCheck", "manifest.json"), `{"name":"FitCheck"}`)
	put(filepath.Join(mods, "PresetWardrobe", "manifest.json"), `{"name":"PresetWardrobe"}`)
	put(filepath.Join(mods, "IdolRiot-FitCheck", "plugin", "main.lua"), `local defaults = {speed = 50, shortcut = "F8"}
        local ok, why = reskate.input.bind("fitcheck", selected)
reskate.input.bind("fitcheck", options.shortcut)`)
	put(filepath.Join(mods, "jrkmods-True_Darkness", "plugin", "main.lua"), `pcall(reskate.input.bind, "Toggle True Darkness", "Ctrl+F8")`)
	put(filepath.Join(mods, "Other-Off", "plugin", "main.lua"), `reskate.input.bind("x", "F8")`)
	put(filepath.Join(mods, "PresetWardrobe", "plugin", "main.lua"), `reskate.input.bind(SNAP_ACTION, "F8")`)
	a := &App{cfg: Config{GameDir: game}, localApp: local}
	h := a.workOutHotkeys()
	if h.Key != "F8" || h.Pad != "Y" || len(h.Conflicts) != 1 || h.Conflicts[0].Name != "Fit Check" {
		t.Fatalf("got %+v", h)
	}
	// Fit Check moved to Ctrl+F8 in its own settings: now True Darkness is the one... on Ctrl+F8, not F8: no conflict.
	put(filepath.Join(local, "ReSkate", "Plugins", "IdolRiot-FitCheck", "storage.json"), `{"options":{"shortcut":"Ctrl + F8"}}`)
	if h := a.workOutHotkeys(); len(h.Conflicts) != 0 {
		t.Fatalf("got %+v", h)
	}
	if keyNorm("shift + ctrl+f8") != keyNorm("Ctrl+Shift+F8") {
		t.Fatal("keyNorm")
	}
	if a.pluginDisplayName(filepath.Join(mods, "jrkmods-True_Darkness"), "jrkmods-True_Darkness") != "True Darkness" {
		t.Fatal("name")
	}
}

func TestHotkeySeen(t *testing.T) {
	game := t.TempDir()
	pd := filepath.Join(game, "Mods", ".reskate_plugin_data", "PresetWardrobe")
	os.MkdirAll(pd, 0o755)
	os.WriteFile(filepath.Join(pd, "snap_key_seen.json"), []byte(`{"key":"F7","at":1}`), 0o644)
	a := &App{cfg: Config{GameDir: game}, localApp: t.TempDir()}
	if h := a.workOutHotkeys(); h.Key != "F7" {
		t.Fatalf("got %+v", h)
	}
}
