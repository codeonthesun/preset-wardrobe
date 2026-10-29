package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A preset as the game writes it: group 0 the skater, group 1 the board, group 2 gestures.
func makeRecipe(top, shoes, deck, gesture string) string {
	groups := []map[string]any{
		{"items": []map[string]any{
			{"asset": "Own_CustHead_00009", "parameter_bits": []int{}, "slot": 870817456},
			{"asset": top, "parameter_bits": []int{}, "slot": 2890690067},
			{"asset": shoes, "parameter_bits": []int{}, "slot": 902165545},
			{"asset": "", "parameter_bits": []int{}, "slot": 2889696005},
		}, "scalar_bits": []int{}, "template_key": 1, "template_version": 1},
		{"items": []map[string]any{
			{"asset": deck, "parameter_bits": []int{}, "slot": 1711740456},
			{"asset": "Own_Truck_Gen_Default_00001", "parameter_bits": []int{}, "slot": 182294718},
		}, "scalar_bits": []int{}, "template_key": 2, "template_version": 1},
		{"items": []map[string]any{
			{"asset": gesture, "parameter_bits": []int{}, "slot": 280870923},
		}, "scalar_bits": []int{}, "template_key": 3, "template_version": 1},
	}
	b, _ := json.Marshal(groups)
	return string(b)
}

func TestOutfitHash(t *testing.T) {
	a := makeRecipe("Own_Hoodie1", "Own_Vans_Era_All_Black", "Own_FAKER", "own_rctn_gesture_all_yay")
	if outfitHash(a) != outfitHash(a) || len(outfitHash(a)) != 16 {
		t.Fatal("hash is not a stable 16-character id")
	}
	// Same outfit written in another item order: same fit.
	var groups []map[string]any
	json.Unmarshal([]byte(a), &groups)
	items := groups[0]["items"].([]any)
	items[0], items[1] = items[1], items[0]
	reordered, _ := json.Marshal(groups)
	if outfitHash(string(reordered)) != outfitHash(a) {
		t.Error("item order changed the hash")
	}
	// Gestures don't make a different fit; clothes and board do.
	if outfitHash(makeRecipe("Own_Hoodie1", "Own_Vans_Era_All_Black", "Own_FAKER", "own_rctn_gesture_all_meh")) != outfitHash(a) {
		t.Error("a gesture changed the hash")
	}
	if outfitHash(makeRecipe("Own_Tee", "Own_Vans_Era_All_Black", "Own_FAKER", "x")) == outfitHash(a) {
		t.Error("a different top kept the same hash")
	}
	if outfitHash(makeRecipe("Own_Hoodie1", "Own_Vans_Era_All_Black", "Own_Blind_Big_Logo", "x")) == outfitHash(a) {
		t.Error("a different deck kept the same hash")
	}
}

func TestPiecesOf(t *testing.T) {
	pieces := piecesOf(makeRecipe("Own_Hoodie1", "Own_Vans_Era_All_Black", "Own_FAKER", "own_rctn_gesture_all_yay"))
	var got []string
	for _, p := range pieces {
		got = append(got, p.Category+"="+p.Key)
	}
	want := "Top=Own_Hoodie1 Shoes=Own_Vans_Era_All_Black Deck=Own_FAKER Trucks=Own_Truck_Gen_Default_00001"
	if strings.Join(got, " ") != want {
		t.Errorf("got %q, want %q (body parts, empty slots and gestures left out, in reading order)", strings.Join(got, " "), want)
	}
}

func TestReadableKey(t *testing.T) {
	for in, want := range map[string]string{
		"Own_Vans_Era_All_Black":                      "Vans Era All Black",
		"own_sb_deck_wheelcolor_SkatePaddygold":       "sb deck wheelcolor SkatePaddygold",
		"Own_23_-_Balenciaga_-_Baggy_Lost_Tape_Denim": "23 - Balenciaga - Baggy Lost Tape Denim",
	} {
		if got := readableKey(in); got != want {
			t.Errorf("readableKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func lineup() []StoredPreset {
	decks := []string{"Own_Girl", "Own_Santa_Cruz", "Own_Luckos", "Own_Flameboy", "Own_Baker",
		"Own_FAKER", "Own_Blind", "Own_Element", "Own_FAKER", "Own_10k"}
	var out []StoredPreset
	for i, d := range decks {
		out = append(out, StoredPreset{PresetID: presetIDs[i], RecipeFormat: 1, ExtensionsJSON: "{}",
			RecipesJSON: makeRecipe("Own_Top"+string(rune('A'+i)), "Own_Shoe", d, "g")})
	}
	return out
}

func TestGuessSlot(t *testing.T) {
	// A deck only one slot has: confident, whatever is selected.
	best, ok := guessSlot(lineup(), 0, []string{"Own_Baker", "Own_DeckGripColor_Gen_Popsicle_00001"})
	if !ok || best[0] != 5 {
		t.Errorf("unique deck: got slot %d confident=%v, want slot 5 confident", best[0], ok)
	}
	// Slots 6 and 9 share a deck: not sure, so the program asks, guessing one of them first.
	best, ok = guessSlot(lineup(), -1, []string{"Own_FAKER"})
	if ok || (best[0] != 6 && best[0] != 9) {
		t.Errorf("shared deck: got slot %d confident=%v, want 6 or 9 and a question", best[0], ok)
	}
	// The same, with slot 9 selected in the game: that settles it.
	best, ok = guessSlot(lineup(), 8, []string{"Own_FAKER"})
	if !ok || best[0] != 9 {
		t.Errorf("shared deck, slot 9 selected: got slot %d confident=%v", best[0], ok)
	}
	// Nothing read at all: only the selection to go on, which isn't enough to be sure.
	if _, ok = guessSlot(lineup(), 2, nil); ok {
		t.Error("no board read: should ask")
	}
}

const schema = `CREATE TABLE cosmetic_loadouts(preset_id TEXT PRIMARY KEY NOT NULL,recipe_format INTEGER NOT NULL CHECK(recipe_format=1),recipes_json TEXT NOT NULL CHECK(json_valid(recipes_json)),extensions_json TEXT NOT NULL CHECK(json_valid(extensions_json))) STRICT, WITHOUT ROWID;
CREATE TABLE profile_extensions(section TEXT PRIMARY KEY NOT NULL,value_json TEXT NOT NULL CHECK(json_valid(value_json))) STRICT, WITHOUT ROWID;
CREATE TABLE rip_score(id INTEGER PRIMARY KEY CHECK(id=1),score INTEGER NOT NULL,level INTEGER NOT NULL) STRICT;
PRAGMA journal_mode=WAL;`

func TestWriteAndReadPresets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reskate.sqlite3")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, p := range lineup() {
		db.Exec(`INSERT INTO cosmetic_loadouts VALUES(?,?,?,?)`, p.PresetID, 1, p.RecipesJSON, p.ExtensionsJSON)
	}
	db.Exec(`INSERT INTO profile_extensions VALUES('customization','{"selected_preset_index":4}')`)
	db.Exec(`INSERT INTO rip_score VALUES(1, 12345, 42)`)
	db.Close()

	got, sel, err := readPresets(path, dir)
	if err != nil || len(got) != 10 || sel != 4 || got[0].PresetID != "default" {
		t.Fatalf("read: %d presets, selected %d, err %v", len(got), sel, err)
	}

	// Write two slots; the other eight and the rest of the profile stay as they were.
	newTop := StoredPreset{PresetID: "Loadout03", RecipeFormat: 1, ExtensionsJSON: "{}", RecipesJSON: makeRecipe("Own_NewTop", "Own_Shoe", "Own_New", "g")}
	newDef := StoredPreset{PresetID: "default", RecipeFormat: 1, ExtensionsJSON: "{}", RecipesJSON: makeRecipe("Own_Other", "Own_Shoe", "Own_New2", "g")}
	backup, err := writePresets(path, filepath.Join(dir, "backups"), []StoredPreset{newTop, newDef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(backup, "reskate.sqlite3")); err != nil {
		t.Error("no backup of the profile was made")
	}
	after, _, err := readPresets(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	before := lineup()
	for i, p := range after {
		switch p.PresetID {
		case "Loadout03":
			if p.RecipesJSON != newTop.RecipesJSON {
				t.Error("slot 3 wasn't written")
			}
		case "default":
			if p.RecipesJSON != newDef.RecipesJSON {
				t.Error("slot 1 wasn't written")
			}
		default:
			if p.RecipesJSON != before[i].RecipesJSON {
				t.Errorf("%s changed but wasn't asked to", p.PresetID)
			}
		}
	}
	db, _ = sql.Open("sqlite3", path)
	defer db.Close()
	var score int
	var check string
	db.QueryRow(`SELECT score FROM rip_score`).Scan(&score)
	db.QueryRow(`PRAGMA integrity_check`).Scan(&check)
	if score != 12345 || check != "ok" {
		t.Errorf("profile damaged: score %d, integrity %q", score, check)
	}
}

func TestPluginFolder(t *testing.T) {
	game := t.TempDir()
	a := &App{cfg: Config{GameDir: game}}
	if a.pluginInstalled() {
		t.Fatal("no plugin installed yet")
	}
	install := func(folder string) {
		dir := filepath.Join(game, "Mods", folder)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "reskate-plugin.json"), []byte(`{"api":1,"entry":"plugin/main.lua"}`), 0o644)
		os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	}
	install("jrkmods-PresetWardrobe") // as Thunderstore installs it
	if got := a.pluginDataDir(); got != filepath.Join(game, "Mods", ".reskate_plugin_data", "jrkmods-PresetWardrobe") {
		t.Errorf("Thunderstore install: data folder %s", got)
	}
	os.RemoveAll(filepath.Join(game, "Mods", "jrkmods-PresetWardrobe"))
	install("Someone-PresetWardrobe-1.0.0") // any other name
	if got := a.pluginFolder(); got != "Someone-PresetWardrobe-1.0.0" {
		t.Errorf("renamed install: got %q", got)
	}
}

func TestSnapsAfterPluginReinstall(t *testing.T) {
	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game, Imported: map[string]int64{}, LastSingle: 40}}
	for _, d := range []string{"fits", "rotations", "pictures", "pending"} {
		os.MkdirAll(filepath.Join(data, d), 0o755)
	}
	plugin := filepath.Join(game, "Mods", "jrkmods-PresetWardrobe")
	os.MkdirAll(plugin, 0o755)
	os.WriteFile(filepath.Join(plugin, "reskate-plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(plugin, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	snaps := filepath.Join(game, "Mods", ".reskate_plugin_data", "jrkmods-PresetWardrobe", "snaps")
	os.MkdirAll(snaps, 0o755)
	// The reinstalled plugin counts from 1 again, below the 40 snaps filed before.
	os.WriteFile(filepath.Join(snaps, "fit-00001.png"), []byte("\x89PNG test"), 0o644)
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(`{"singles":[{"seq":1,"file":"fit-00001.png","worn":["Own_Baker"]}]}`), 0o644)

	events := a.importSnaps(lineup(), 4)
	if len(events) == 0 || !strings.Contains(events[0], "slot 5") {
		t.Fatalf("snap wasn't filed: %v", events)
	}
	if _, err := os.Stat(filepath.Join(snaps, "fit-00001.png")); err == nil {
		t.Error("a filed snap should be removed from the plugin's folder")
	}
	if fits := a.listFits(); len(fits) != 1 {
		t.Errorf("want 1 fit in the catalogue, got %d", len(fits))
	}
	if again := a.importSnaps(lineup(), 4); len(again) != 0 {
		t.Errorf("filed twice: %v", again)
	}
}
