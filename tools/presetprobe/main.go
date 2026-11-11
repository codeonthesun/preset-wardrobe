// Preset Probe: a throwaway test for Preset Wardrobe. With skate. running, it marks every preset in
// the profile with a harmless tag, then watches which presets the game writes back (a rewrite drops
// the tag), and when. It changes nothing else, and "clean" takes the tags out again.
package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const tag = "presetwardrobe_probe"

var ids = []string{"default", "Loadout02", "Loadout03", "Loadout04", "Loadout05",
	"Loadout06", "Loadout07", "Loadout08", "Loadout09", "Loadout10"}

var logf *os.File

func say(format string, a ...any) {
	line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, a...)
	fmt.Println(line)
	if logf != nil {
		fmt.Fprintln(logf, line)
	}
}

func findDB() string {
	base := filepath.Join(os.Getenv("LOCALAPPDATA"), "ReSkate", "profiles")
	p := filepath.Join(base, "offline", "reskate.sqlite3")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if m, _ := filepath.Glob(filepath.Join(base, "*", "reskate.sqlite3")); len(m) > 0 {
		return m[0]
	}
	return p
}

func copyFile(a, b string) error {
	in, err := os.Open(a)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(b)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

type row struct{ recipes, ext string }

// read takes a copy (with its journal) so the game's file is never locked by reading.
func read(db string) (map[string]row, error) {
	dir, err := os.MkdirTemp("", "presetprobe-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	c := filepath.Join(dir, "p.sqlite3")
	for try := 0; ; try++ {
		if err := copyFile(db, c); err != nil {
			return nil, err
		}
		os.Remove(c + "-wal")
		if _, err := os.Stat(db + "-wal"); err == nil {
			copyFile(db+"-wal", c+"-wal")
		}
		out, err := readFrom(c)
		if err == nil || try == 2 {
			return out, err
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func readFrom(path string) (map[string]row, error) {
	d, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	rs, err := d.Query(`SELECT preset_id, recipes_json, extensions_json FROM cosmetic_loadouts`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string]row{}
	for rs.Next() {
		var id string
		var r row
		if err := rs.Scan(&id, &r.recipes, &r.ext); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rs.Err()
}

func tagOf(ext string) string {
	var m map[string]any
	if json.Unmarshal([]byte(ext), &m) != nil {
		return ""
	}
	s, _ := m[tag].(string)
	return s
}

// setTags puts the tag in (mark) or takes it out (clean) of every preset's extensions.
func setTags(db string, mark bool) error {
	cur, err := read(db)
	if err != nil {
		return err
	}
	d, err := sql.Open("sqlite3", db)
	if err != nil {
		return err
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	d.Exec(`PRAGMA busy_timeout = 5000`)
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	stamp := time.Now().Format("150405")
	for _, id := range ids {
		r, ok := cur[id]
		if !ok {
			continue
		}
		m := map[string]any{}
		json.Unmarshal([]byte(r.ext), &m)
		if mark {
			m[tag] = stamp
		} else {
			delete(m, tag)
		}
		b, _ := json.Marshal(m)
		if _, err := tx.Exec(`UPDATE cosmetic_loadouts SET extensions_json=? WHERE preset_id=?`, string(b), id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// gameDir: where the plugin's data folders are. Preset Wardrobe knows it; otherwise ask.
func gameDir(in *bufio.Reader) string {
	local := os.Getenv("LOCALAPPDATA")
	var cfg struct {
		GameDir string `json:"game_dir"`
	}
	if b, err := os.ReadFile(filepath.Join(local, "PresetWardrobe", "config.json")); err == nil {
		json.Unmarshal(b, &cfg)
	}
	if cfg.GameDir != "" {
		if _, err := os.Stat(filepath.Join(cfg.GameDir, "Mods")); err == nil {
			return cfg.GameDir
		}
	}
	fmt.Print("Game folder (the one with Skate.exe and Mods): ")
	line, _ := in.ReadString('\n')
	return strings.Trim(strings.TrimSpace(line), `"`)
}

// export writes every preset's items to the Preset Test plugin's folder, for it to compare with
// what the game says is worn. Items are lower case; "board" is the board's own group.
func export(db string, in *bufio.Reader) {
	cur, err := read(db)
	if err != nil {
		say("could not read the profile: %v", err)
		return
	}
	game := gameDir(in)
	if _, err := os.Stat(filepath.Join(game, "Mods")); err != nil {
		say("no Mods folder in %q", game)
		return
	}
	data := filepath.Join(game, "Mods", ".reskate_plugin_data")
	dest := ""
	if m, _ := filepath.Glob(filepath.Join(data, "*PresetTest")); len(m) > 0 {
		dest = m[0]
	} else {
		dest = filepath.Join(data, "PresetTest")
	}
	os.MkdirAll(dest, 0o755)
	type slotOut struct {
		Slot   int      `json:"slot"`
		ID     string   `json:"id"`
		Skater []string `json:"skater"`
		Board  []string `json:"board"`
	}
	var slots []slotOut
	for _, id := range ids {
		r, ok := cur[id]
		if !ok {
			continue
		}
		var groups []struct {
			Items []struct {
				Asset string `json:"asset"`
			} `json:"items"`
		}
		json.Unmarshal([]byte(r.recipes), &groups)
		o := slotOut{Slot: slot(id), ID: id, Skater: []string{}, Board: []string{}}
		for gi, g := range groups {
			for _, it := range g.Items {
				a := strings.ToLower(it.Asset)
				if a == "" {
					continue
				}
				if gi == 1 {
					o.Board = append(o.Board, a)
				} else {
					o.Skater = append(o.Skater, a)
				}
			}
		}
		slots = append(slots, o)
	}
	sel := -1
	if d, err := sql.Open("sqlite3", db); err == nil {
		var ext string
		if d.QueryRow(`SELECT value_json FROM profile_extensions WHERE section='customization'`).Scan(&ext) == nil {
			var c struct {
				Selected *int `json:"selected_preset_index"`
			}
			if json.Unmarshal([]byte(ext), &c) == nil && c.Selected != nil {
				sel = *c.Selected + 1
			}
		}
		d.Close()
	}
	b, _ := json.MarshalIndent(map[string]any{"exported": time.Now().Format("15:04:05"), "selected_in_file": sel, "slots": slots}, "", " ")
	if err := os.WriteFile(filepath.Join(dest, "presets.json"), b, 0o644); err != nil {
		say("could not write: %v", err)
		return
	}
	say("exported %d presets (selected in the file: slot %d) to %s", len(slots), sel, filepath.Join(dest, "presets.json"))
}

func slot(id string) int {
	for i, x := range ids {
		if x == id {
			return i + 1
		}
	}
	return 0
}

func show(cur map[string]row) {
	var tagged, untagged []string
	for _, id := range ids {
		if _, ok := cur[id]; !ok {
			continue
		}
		if tagOf(cur[id].ext) != "" {
			tagged = append(tagged, fmt.Sprint(slot(id)))
		} else {
			untagged = append(untagged, fmt.Sprint(slot(id)))
		}
	}
	say("still tagged: slots %s", orNone(tagged))
	say("rewritten by the game (tag gone): slots %s", orNone(untagged))
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

// watch prints each preset the moment the game writes it, until Enter is pressed.
func watch(db string) {
	prev, err := read(db)
	if err != nil {
		say("could not read the profile: %v", err)
		return
	}
	say("watching. Do one thing at a time in the game; each preset it writes shows here. Press Enter to stop.")
	stop := make(chan struct{})
	go func() { bufio.NewReader(os.Stdin).ReadString('\n'); close(stop) }()
	for {
		select {
		case <-stop:
			return
		case <-time.After(700 * time.Millisecond):
		}
		cur, err := read(db)
		if err != nil {
			continue
		}
		for _, id := range ids {
			a, b := prev[id], cur[id]
			if a == b {
				continue
			}
			what := []string{}
			if tagOf(a.ext) != "" && tagOf(b.ext) == "" {
				what = append(what, "tag gone (the game wrote it back)")
			}
			if a.recipes != b.recipes {
				what = append(what, "items changed")
			}
			if len(what) == 0 {
				what = append(what, "extras changed")
			}
			say("slot %d: %s", slot(id), strings.Join(what, ", "))
		}
		prev = cur
	}
}

func main() {
	home, _ := os.UserHomeDir()
	out := filepath.Join(home, "Documents", "PresetProbe")
	os.MkdirAll(out, 0o755)
	logf, _ = os.OpenFile(filepath.Join(out, "probe-log.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	db := findDB()
	if _, err := os.Stat(db); err != nil {
		say("no profile found at %s", db)
		bufio.NewReader(os.Stdin).ReadString('\n')
		return
	}
	say("Preset Probe. Profile: %s", db)
	// a full backup first, whatever happens
	bk := filepath.Join(out, "backup-"+time.Now().Format("2006-01-02_15-04-05"))
	os.MkdirAll(bk, 0o755)
	for _, s := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(db + s); err == nil {
			copyFile(db+s, filepath.Join(bk, "reskate.sqlite3"+s))
		}
	}
	say("backed up the whole profile to %s", bk)
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("  m  mark every preset with the test tag")
		fmt.Println("  w  watch: shows each preset the game writes, as it happens")
		fmt.Println("  c  check which presets still have the tag")
		fmt.Println("  x  clean: take the tags out (do this with the game closed, before you start it again)")
		fmt.Println("  e  export your presets for the Preset Test plugin (does the same as Preset Wardrobe's file read; changes nothing)")
		fmt.Println("  n  add a note to the log (what you're about to do)")
		fmt.Println("  q  quit")
		fmt.Print("> ")
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "m":
			if err := setTags(db, true); err != nil {
				say("could not mark: %v", err)
				break
			}
			say("marked")
			if cur, err := read(db); err == nil {
				show(cur)
			}
		case "w":
			watch(db)
		case "c":
			if cur, err := read(db); err == nil {
				show(cur)
			} else {
				say("could not read: %v", err)
			}
		case "x":
			if err := setTags(db, false); err != nil {
				say("could not clean: %v", err)
			} else {
				say("cleaned: no tags left")
			}
		case "e":
			export(db, in)
		case "n":
			fmt.Print("note: ")
			note, _ := in.ReadString('\n')
			say("NOTE: %s", strings.TrimSpace(note))
		case "q":
			return
		}
	}
}
