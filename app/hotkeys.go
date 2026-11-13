package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The snap key. On the keyboard it's ReSkate's own bind for the plugin's snapAction (Settings > Controls >
// Plugins' own binds), F8 unless the player changes it there; on a controller it's holding Y on the Skaters page
// (fixed). A plugin can't ask ReSkate which key a bind is on, so the app works it out: the player's own choice from
// their profile, else the plugin's default. It also looks through the other switched-on plugins for one on the same
// key (ReSkate lets two plugins share a key, and then both run). The app's texts show the key, and hotkeys.json in
// the plugin's data folder tells the plugin, for its page (a warning when the key is shared) and its banners.

const (
	snapAction     = "Preset Wardrobe: snap a fit"
	snapDefaultKey = "F8"
	snapPadButton  = "Y"
)

type HotkeyConflict struct {
	Name string `json:"name"` // the other plugin, as people know it ("Fit Check")
	Key  string `json:"key"`
}

type Hotkeys struct {
	Key       string           `json:"key"` // "F8", "Ctrl+F7"; "" when the player cleared it
	Pad       string           `json:"pad"` // "Y": hold it on the Skaters page
	Conflicts []HotkeyConflict `json:"conflicts"`
}

var hotkeyCache struct {
	sync.Mutex
	at  time.Time
	val Hotkeys
}

// hotkeys: what's known now, worked out again every few seconds at most.
func (a *App) hotkeys() Hotkeys {
	hotkeyCache.Lock()
	defer hotkeyCache.Unlock()
	if time.Since(hotkeyCache.at) < 4*time.Second && hotkeyCache.val.Pad != "" {
		return hotkeyCache.val
	}
	hotkeyCache.at = time.Now()
	hotkeyCache.val = a.workOutHotkeys()
	return hotkeyCache.val
}

func (a *App) workOutHotkeys() Hotkeys {
	h := Hotkeys{Key: snapDefaultKey, Pad: snapPadButton, Conflicts: []HotkeyConflict{}}
	bound := a.profileBinds()
	if k, ok := bound[snapAction]; ok {
		h.Key = k
	} else if dir := a.pluginDataDir(); dir != "" {
		// The key the plugin saw the snap come from, last time it was pressed.
		var seen struct {
			Key string `json:"key"`
		}
		if readJSON(filepath.Join(dir, "snap_key_seen.json"), &seen) == nil && seen.Key != "" && len(seen.Key) < 24 {
			h.Key = seen.Key
		}
	}
	if h.Key == "" || a.cfg.GameDir == "" {
		return h
	}
	mine := keyNorm(h.Key)
	for _, p := range a.otherPluginKeys(bound) {
		if keyNorm(p.Key) == mine {
			h.Conflicts = append(h.Conflicts, p)
		}
	}
	return h
}

// keyNorm: "ctrl + f8" and "Ctrl+F8" are the same key; the modifiers in a fixed order.
func keyNorm(k string) string {
	parts := strings.Split(strings.ToUpper(strings.ReplaceAll(k, " ", "")), "+")
	if len(parts) > 1 {
		mods := parts[:len(parts)-1]
		sort.Strings(mods)
		parts = append(mods, parts[len(parts)-1])
	}
	return strings.Join(parts, "+")
}

// profileBinds: the binds the player has set in ReSkate's controls, by action name, from their profile (a copy, so the
// game's file is never held open). Only the ones they changed are there.
func (a *App) profileBinds() map[string]string {
	out := map[string]string{}
	dbPath := a.profileDB()
	if _, err := os.Stat(dbPath); err != nil {
		return out
	}
	tmp, err := os.MkdirTemp("", "pw-binds-")
	if err != nil {
		return out
	}
	defer os.RemoveAll(tmp)
	cp := filepath.Join(tmp, "reskate.sqlite3")
	if copyFile(dbPath, cp) != nil {
		return out
	}
	if _, err := os.Stat(dbPath + "-wal"); err == nil {
		copyFile(dbPath+"-wal", cp+"-wal")
	}
	db, err := sql.Open("sqlite3", cp)
	if err != nil {
		return out
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name, value_json FROM player_settings WHERE scope LIKE '%bind%'`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name, value string
		if rows.Scan(&name, &value) != nil {
			continue
		}
		if k, ok := bindText(value); ok {
			out[name] = k
		}
	}
	return out
}

// bindText: a stored bind's key as people write it, from its JSON ("F7", or {"key": "F7"} and the like). Numbers
// (the game's own binds keep button masks) aren't keys this needs.
func bindText(value string) (string, bool) {
	var v any
	if json.Unmarshal([]byte(value), &v) != nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case map[string]any:
		for _, f := range []string{"key", "keys", "bind", "binding", "combo", "text", "name"} {
			if s, ok := t[f].(string); ok {
				return s, true
			}
		}
	}
	return "", false
}

var (
	// reskate.input.bind("name", "F8") and pcall(reskate.input.bind, "name", "F8")
	bindCallRe = regexp.MustCompile(`reskate\.input\.bind\s*(?:\(|,)\s*["']([^"']+)["']\s*,\s*([^)\n]*)\)`)
	keyLitRe   = regexp.MustCompile(`^\s*["']([^"']+)["']\s*$`)
)

// otherPluginKeys: the keys the other switched-on plugins bind, each the player's own choice where they made one,
// else the plugin's default. A default that isn't written out ("options.shortcut") is looked for as that setting:
// in the plugin's saved storage first, then where its code sets it ("shortcut = \"F8\"").
func (a *App) otherPluginKeys(bound map[string]string) []HotkeyConflict {
	mods := filepath.Join(a.cfg.GameDir, "Mods")
	var list struct {
		Mods []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"mods"`
	}
	if readJSON(filepath.Join(mods, "mods.json"), &list) != nil {
		return nil
	}
	mine := a.pluginFolder()
	var out []HotkeyConflict
	for _, m := range list.Mods {
		if !m.Enabled || m.Name == "" || m.Name == mine || strings.ContainsAny(m.Name, `/\`) {
			continue
		}
		dir := filepath.Join(mods, m.Name)
		if _, err := os.Stat(filepath.Join(dir, "reskate-plugin.json")); err != nil {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(dir, "plugin", "*.lua"))
		var storage map[string]any
		readJSON(filepath.Join(a.localApp, "ReSkate", "Plugins", m.Name, "storage.json"), &storage)
		name := a.pluginDisplayName(dir, m.Name)
		seen := map[string]bool{}
		for _, f := range files {
			st, err := os.Stat(f)
			if err != nil || st.Size() > 4<<20 {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			src := string(b)
			for _, c := range bindCallRe.FindAllStringSubmatch(src, -1) {
				action, arg := c[1], strings.TrimSpace(c[2])
				if action == snapAction || action == "presetwardrobe_snap" || seen[action] {
					continue // a copy of this plugin, or a bind already counted
				}
				key, ok := bound[action]
				if !ok {
					key = defaultKeyOf(arg, src, storage)
				}
				if key != "" {
					seen[action] = true
					out = append(out, HotkeyConflict{Name: name, Key: key})
				}
			}
		}
	}
	return out
}

// defaultKeyOf: the key a bind call's second argument comes to.
func defaultKeyOf(arg, src string, storage map[string]any) string {
	if m := keyLitRe.FindStringSubmatch(arg); m != nil {
		return m[1]
	}
	if arg == "" || arg == "nil" {
		return ""
	}
	field := arg
	if i := strings.LastIndexAny(field, ".:"); i >= 0 {
		field = field[i+1:]
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(field) {
		return ""
	}
	if s := findStored(storage, field, 0); s != "" {
		return s
	}
	if m := regexp.MustCompile(`\b` + field + `\s*=\s*["']([^"']+)["']`).FindStringSubmatch(src); m != nil {
		return m[1]
	}
	return ""
}

// findStored: a string saved under that name anywhere in a plugin's storage (it keeps its settings as tables).
func findStored(v any, field string, depth int) string {
	if depth > 4 {
		return ""
	}
	if m, ok := v.(map[string]any); ok {
		if s, ok := m[field].(string); ok {
			return s
		}
		for _, x := range m {
			if s := findStored(x, field, depth+1); s != "" {
				return s
			}
		}
	}
	return ""
}

// pluginDisplayName: "Fit Check" for a manifest's "FitCheck", "True Darkness" for "True_Darkness".
func (a *App) pluginDisplayName(dir, folder string) string {
	var m struct {
		Name string `json:"name"`
	}
	n := folder
	if readJSON(filepath.Join(dir, "manifest.json"), &m) == nil && m.Name != "" {
		n = m.Name
	} else if i := strings.Index(n, "-"); i >= 0 {
		n = n[i+1:]
	}
	n = strings.ReplaceAll(n, "_", " ")
	var b strings.Builder
	r := []rune(n)
	for i, c := range r {
		if i > 0 && unicode.IsUpper(c) && unicode.IsLower(r[i-1]) {
			b.WriteRune(' ')
		}
		b.WriteRune(c)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// watchHotkeys: hotkeys.json in the plugin's data folder, written when it changes.
func (a *App) watchHotkeys() {
	var last string
	for range time.Tick(3 * time.Second) {
		dir := a.pluginDataDir()
		if dir == "" {
			continue
		}
		b, err := json.Marshal(a.hotkeys())
		if err != nil {
			continue
		}
		path := filepath.Join(dir, "hotkeys.json")
		if string(b) == last && fileExists(path) {
			continue
		}
		if os.MkdirAll(dir, 0o755) != nil {
			continue
		}
		if os.WriteFile(path+".tmp", b, 0o644) == nil && os.Rename(path+".tmp", path) == nil {
			if last != string(b) {
				tl.logf("APP: snap key %s", string(b))
			}
			last = string(b)
		}
	}
}
