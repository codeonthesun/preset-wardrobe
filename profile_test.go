package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	backup, err := writePresets(path, filepath.Join(dir, "backups"), []StoredPreset{newTop, newDef}, false, "test")
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

func TestSnapFramesAndSpinPreview(t *testing.T) {
	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game, Imported: map[string]int64{}}}
	for _, d := range []string{"fits", "rotations", "pictures", "pending"} {
		os.MkdirAll(filepath.Join(data, d), 0o755)
	}
	plugin := filepath.Join(game, "Mods", "PresetWardrobe")
	os.MkdirAll(plugin, 0o755)
	os.WriteFile(filepath.Join(plugin, "reskate-plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(plugin, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	snaps := filepath.Join(game, "Mods", ".reskate_plugin_data", "PresetWardrobe", "snaps")
	os.MkdirAll(snaps, 0o755)
	var names []string
	for i := 1; i <= 8; i++ {
		n := fmt.Sprintf("fit-00003-%d.png", i)
		names = append(names, `"`+n+`"`)
		os.WriteFile(filepath.Join(snaps, n), []byte(fmt.Sprintf("\x89PNG frame %d", i)), 0o644)
	}
	index := fmt.Sprintf(`{"singles":[{"seq":3,"file":"fit-00003-8.png","frames":[%s],"worn":["Own_Baker"]}]}`, strings.Join(names, ","))
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(index), 0o644)

	if ev := a.importSnaps(lineup(), 4); len(ev) == 0 {
		t.Fatal("snap with frames wasn't filed")
	}
	h := outfitHash(lineup()[4].RecipesJSON)
	main, _ := os.ReadFile(filepath.Join(data, "pictures", h+".png"))
	if string(main) != "\x89PNG frame 8" {
		t.Errorf("main picture should be the last frame, got %q", main)
	}
	urls := a.frameURLs(h)
	if len(urls) != 8 || !strings.HasPrefix(urls[0], "/pic/"+h+"/1.png") || !strings.HasPrefix(urls[7], "/pic/"+h+"/8.png") {
		t.Errorf("frames in order: %v", urls)
	}
	left, _ := filepath.Glob(filepath.Join(snaps, "fit-00003*"))
	if len(left) != 0 {
		t.Errorf("snap files left in the plugin folder: %v", left)
	}

	// A later flat picture of the same fit replaces the frames too.
	os.WriteFile(filepath.Join(snaps, "fit-00004.png"), []byte("\x89PNG flat"), 0o644)
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(`{"singles":[{"seq":4,"file":"fit-00004.png","worn":["Own_Baker"]}]}`), 0o644)
	a.importSnaps(lineup(), 4)
	if n := len(a.frameURLs(h)); n != 0 {
		t.Errorf("old frames kept after a flat picture: %d", n)
	}
}

// Frames whose figure (a head on a body, plus a board that swings out) was centred on its outline,
// as ReSkate saves them, come out with the head in one place.
func TestAlignFramesOnHead(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i, board := range []int{-60, -30, 0, 30, 60} {
		img := image.NewNRGBA(image.Rect(0, 0, 200, 200))
		// What's drawn: head at x 100, body below, board out to the side by `board`.
		left, right := 90, 110
		if board < 0 {
			left = 100 + board
		} else if board > 0 {
			right = 100 + board
		}
		shift := 100 - (left+right)/2 // ReSkate centres the outline
		fill := func(x0, y0, x1, y1 int) {
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					img.Set(x+shift, y, color.NRGBA{200, 180, 150, 255})
				}
			}
		}
		fill(92, 10, 108, 30)  // head
		fill(90, 30, 110, 180) // body
		if board != 0 {
			bx := 100 + board
			fill(min(100, bx), 100, max(100, bx), 110) // board
		}
		var buf bytes.Buffer
		png.Encode(&buf, img)
		p := filepath.Join(dir, fmt.Sprintf("%d.png", i+1))
		os.WriteFile(p, buf.Bytes(), 0o644)
		paths = append(paths, p)
	}
	out, err := alignFrames(paths)
	if err != nil {
		t.Fatal(err)
	}
	var heads []float64
	for _, b := range out {
		img, _ := png.Decode(bytes.NewReader(b))
		heads = append(heads, headX(img))
	}
	for _, h := range heads {
		if math.Abs(h-heads[0]) > 1 {
			t.Fatalf("heads not lined up: %v", heads)
		}
	}

	// Frames already filed are lined up once, and marked.
	alignDir(dir)
	if _, err := os.Stat(filepath.Join(dir, alignedMark)); err != nil {
		t.Error("folder not marked as lined up")
	}
	if urls := framesIn(dir, "/x"); len(urls) != 5 {
		t.Errorf("the mark file shouldn't count as a frame: %v", urls)
	}
}

// A snap with board views files them under the fit's picture, lined up on the deck, and serves them.
func TestBoardViews(t *testing.T) {
	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game, Imported: map[string]int64{}}}
	for _, d := range []string{"fits", "rotations", "pictures", "pending"} {
		os.MkdirAll(filepath.Join(data, d), 0o755)
	}
	plugin := filepath.Join(game, "Mods", "PresetWardrobe")
	os.MkdirAll(plugin, 0o755)
	os.WriteFile(filepath.Join(plugin, "reskate-plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(plugin, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	snaps := filepath.Join(game, "Mods", ".reskate_plugin_data", "PresetWardrobe", "snaps")
	os.MkdirAll(snaps, 0o755)
	// Board views as ReSkate trims them: centred on the outline, so a side view with its wheels out
	// to one side has its deck off the middle.
	view := func(deckLeft, deckRight, wheels int) []byte {
		img := image.NewNRGBA(image.Rect(0, 0, 200, 200))
		for y := 10; y < 190; y++ {
			for x := deckLeft; x < deckRight; x++ {
				img.Set(x, y, color.NRGBA{40, 40, 40, 255})
			}
		}
		for _, y := range []int{40, 160} { // the wheels, only near the trucks
			for dy := -8; dy < 8; dy++ {
				for x := deckRight; x < deckRight+wheels; x++ {
					img.Set(x, y+dy, color.NRGBA{220, 220, 220, 255})
				}
			}
		}
		var buf bytes.Buffer
		png.Encode(&buf, img)
		return buf.Bytes()
	}
	os.WriteFile(filepath.Join(snaps, "fit-00007.png"), []byte("\x89PNG cover"), 0o644)
	views := [][]byte{view(70, 130, 0), view(85, 95, 30), view(70, 130, 0), view(105, 115, 0)}
	var names []string
	for i, b := range views {
		n := fmt.Sprintf("fit-00007-b%d.png", i+1)
		names = append(names, `"`+n+`"`)
		os.WriteFile(filepath.Join(snaps, n), b, 0o644)
	}
	index := fmt.Sprintf(`{"singles":[{"seq":7,"file":"fit-00007.png","board":[%s],"worn":["Own_Baker"]}]}`, strings.Join(names, ","))
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(index), 0o644)
	if ev := a.importSnaps(lineup(), 4); len(ev) == 0 {
		t.Fatal("snap with board views wasn't filed")
	}
	h := outfitHash(lineup()[4].RecipesJSON)
	urls := a.boardURLs(h)
	if len(urls) != 4 || !strings.HasPrefix(urls[0], "/pic/"+h+"/board/lit/1.png") {
		t.Fatalf("board views (evened-out copies by default): %v", urls)
	}
	a.cfg.PlainBoards = true
	if u := a.boardURLs(h); len(u) != 4 || !strings.HasPrefix(u[0], "/pic/"+h+"/board/1.png") {
		t.Errorf("with the lighting effect off, the originals: %v", u)
	}
	a.cfg.PlainBoards = false
	if n := len(a.frameURLs(h)); n != 0 {
		t.Errorf("board views counted as turn frames: %d", n)
	}
	var xs []float64
	for i := 1; i <= 4; i++ {
		b, err := os.ReadFile(filepath.Join(data, "pictures", h, "board", fmt.Sprintf("%d.png", i)))
		if err != nil {
			t.Fatal(err)
		}
		img, _ := png.Decode(bytes.NewReader(b))
		xs = append(xs, deckX(img))
	}
	for _, x := range xs {
		if math.Abs(x-xs[0]) > 1 {
			t.Errorf("decks not lined up: %v", xs)
			break
		}
	}
	if left, _ := filepath.Glob(filepath.Join(snaps, "fit-00007*")); len(left) != 0 {
		t.Errorf("snap files left in the plugin folder: %v", left)
	}
	if !snapName.MatchString("fit-00007-b4.png") || snapName.MatchString("fit-00007-b5.png") {
		t.Error("board view names")
	}
	// Three of the four views isn't a set: no board views then.
	if got := existingBoard(snaps, []string{"x-b1.png"}); got != nil {
		t.Errorf("a part set of board views was kept: %v", got)
	}
}

// makeAPNG writes frames as ReSkate does: whole frames, the first in IDAT, the rest in fdAT.
func makeAPNG(t *testing.T, imgs []*image.NRGBA) []byte {
	t.Helper()
	var first bytes.Buffer
	png.Encode(&first, imgs[0])
	chunks, err := readChunks(first.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	w.Write(pngSignature)
	writeChunk(&w, "IHDR", chunks[0].data)
	actl := make([]byte, 8)
	binary.BigEndian.PutUint32(actl, uint32(len(imgs)))
	writeChunk(&w, "acTL", actl)
	seq := uint32(0)
	for i, img := range imgs {
		fctl := make([]byte, 26)
		binary.BigEndian.PutUint32(fctl, seq)
		seq++
		writeChunk(&w, "fcTL", fctl)
		var one bytes.Buffer
		png.Encode(&one, img)
		cs, _ := readChunks(one.Bytes())
		for _, c := range cs {
			if c.kind != "IDAT" {
				continue
			}
			if i == 0 {
				writeChunk(&w, "IDAT", c.data)
				continue
			}
			d := make([]byte, 4, 4+len(c.data))
			binary.BigEndian.PutUint32(d, seq)
			seq++
			writeChunk(&w, "fdAT", append(d, c.data...))
		}
	}
	writeChunk(&w, "IEND", nil)
	return w.Bytes()
}

// A turn saved as one animated PNG is split into its frames, kept as the game framed them.
func TestTurnSavedTogether(t *testing.T) {
	var imgs []*image.NRGBA
	for i := 0; i < 24; i++ {
		img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
		img.Set(i, 10, color.NRGBA{255, 0, 0, 255}) // a dot that moves along, frame by frame
		imgs = append(imgs, img)
	}
	frames, err := splitAPNG(makeAPNG(t, imgs))
	if err != nil || len(frames) != 24 {
		t.Fatalf("split: %d frames, %v", len(frames), err)
	}
	for i, f := range frames {
		img, err := png.Decode(bytes.NewReader(f))
		if err != nil {
			t.Fatalf("frame %d: %v", i+1, err)
		}
		if _, _, _, a := img.At(i, 10).RGBA(); a == 0 {
			t.Errorf("frame %d isn't its own picture", i+1)
		}
	}

	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game, Imported: map[string]int64{}}}
	for _, d := range []string{"fits", "rotations", "pictures", "pending"} {
		os.MkdirAll(filepath.Join(data, d), 0o755)
	}
	plugin := filepath.Join(game, "Mods", "PresetWardrobe")
	os.MkdirAll(plugin, 0o755)
	os.WriteFile(filepath.Join(plugin, "reskate-plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(plugin, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	snaps := filepath.Join(game, "Mods", ".reskate_plugin_data", "PresetWardrobe", "snaps")
	os.MkdirAll(snaps, 0o755)
	os.WriteFile(filepath.Join(snaps, "fit-00009.png"), []byte("\x89PNG cover"), 0o644)
	os.WriteFile(filepath.Join(snaps, "fit-00009-turn.png"), makeAPNG(t, imgs), 0o644)
	os.WriteFile(filepath.Join(snaps, "fit-00009-board.png"), makeAPNG(t, imgs[:4]), 0o644)
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(`{"singles":[{"seq":9,"file":"fit-00009.png","turn":"fit-00009-turn.png","board_turn":"fit-00009-board.png","worn":["Own_Baker"]}]}`), 0o644)
	if ev := a.importSnaps(lineup(), 4); len(ev) == 0 {
		t.Fatal("not filed")
	}
	h := outfitHash(lineup()[4].RecipesJSON)
	if n, m := len(a.frameURLs(h)), len(a.boardURLs(h)); n != 24 || m != 4 {
		t.Errorf("filed %d frames and %d board views", n, m)
	}
	b, _ := os.ReadFile(filepath.Join(data, "pictures", h, "7.png"))
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, al := img.At(6, 10).RGBA(); al == 0 {
		t.Error("frame 7 was moved or isn't frame 7")
	}
	if left, _ := filepath.Glob(filepath.Join(snaps, "fit-00009*")); len(left) != 0 {
		t.Errorf("left in the plugin folder: %v", left)
	}
}

// A walk-through's boards, saved in a pass after the outfits, are added to the fits already filed.
func TestWalkThroughBoardsLater(t *testing.T) {
	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game, Imported: map[string]int64{}}}
	for _, d := range []string{"fits", "rotations", "pictures", "pending"} {
		os.MkdirAll(filepath.Join(data, d), 0o755)
	}
	plugin := filepath.Join(game, "Mods", "PresetWardrobe")
	os.MkdirAll(plugin, 0o755)
	os.WriteFile(filepath.Join(plugin, "reskate-plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(plugin, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	snaps := filepath.Join(game, "Mods", ".reskate_plugin_data", "PresetWardrobe", "snaps")
	os.MkdirAll(snaps, 0o755)
	var imgs []*image.NRGBA
	for i := 0; i < 24; i++ {
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		img.Set(i%32, 5, color.NRGBA{255, 255, 255, 255})
		imgs = append(imgs, img)
	}
	os.WriteFile(filepath.Join(snaps, "01.png"), []byte("\x89PNG cover"), 0o644)
	os.WriteFile(filepath.Join(snaps, "01-turn.png"), makeAPNG(t, imgs), 0o644)
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(`{"shots":[{"n":1,"file":"01.png","turn":"01-turn.png"}]}`), 0o644)
	a.importSnaps(lineup(), 0)
	h := outfitHash(lineup()[0].RecipesJSON)
	if n := len(a.frameURLs(h)); n != 24 || len(a.boardURLs(h)) != 0 {
		t.Fatalf("first pass: %d frames, %d board", n, len(a.boardURLs(h)))
	}
	os.WriteFile(filepath.Join(snaps, "01-board.png"), makeAPNG(t, imgs[:4]), 0o644)
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(`{"shots":[{"n":1,"file":"01.png","turn":"01-turn.png","board_turn":"01-board.png"}]}`), 0o644)
	a.importSnaps(lineup(), 0)
	if n, m := len(a.frameURLs(h)), len(a.boardURLs(h)); n != 24 || m != 4 {
		t.Errorf("after the board pass: %d frames, %d board views", n, m)
	}
}

// The app presses the key a fresh request asks for, once, and answers; an old request is left alone.
func TestKeyRequests(t *testing.T) {
	t.Setenv("PB_KEYS_LOG", "1") // away from Windows: pretend the game is in front
	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game, Imported: map[string]int64{}}}
	plugin := filepath.Join(game, "Mods", "PresetWardrobe")
	os.MkdirAll(plugin, 0o755)
	os.WriteFile(filepath.Join(plugin, "reskate-plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(plugin, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	snaps := filepath.Join(game, "Mods", ".reskate_plugin_data", "PresetWardrobe", "snaps")
	os.MkdirAll(snaps, 0o755)
	req := filepath.Join(snaps, "keys.json")
	os.WriteFile(req, []byte(`{"id":4,"key":"c"}`), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(req, old, old)
	go a.watchKeyRequests()
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(snaps, "keys_done.json")); err == nil {
		t.Fatal("an old request was answered")
	}
	os.WriteFile(req, []byte(`{"id":5,"key":"z"}`), 0o644)
	time.Sleep(400 * time.Millisecond)
	b, _ := os.ReadFile(filepath.Join(snaps, "keys_done.json"))
	if string(b) != `{"id":5,"pressed":true}` {
		t.Errorf("answer: %s", b)
	}
}

// The lighting effect brightens a board lit from below without touching the original or what's
// see-through, and evens out top against bottom.
func TestEvenLight(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 200))
	for y := 20; y < 180; y++ {
		v := uint8(20 + (y-20)*80/160) // dark at the top, lighter at the bottom
		for x := 30; x < 70; x++ {
			img.Set(x, y, color.NRGBA{v, v / 2, v / 3, 255})
		}
	}
	out := evenLight(img)
	if _, _, _, a := out.At(5, 5).RGBA(); a != 0 {
		t.Error("the see-through background isn't see-through any more")
	}
	lum := func(c color.Color) float64 {
		r, g, b, _ := c.RGBA()
		return 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	}
	inTop, inBot := lum(img.At(50, 30)), lum(img.At(50, 170))
	outTop, outBot := lum(out.At(50, 30)), lum(out.At(50, 170))
	if outTop <= inTop {
		t.Errorf("the top isn't brighter: %.0f -> %.0f", inTop, outTop)
	}
	if outTop/outBot <= inTop/inBot {
		t.Errorf("top against bottom isn't more even: %.2f -> %.2f", inTop/inBot, outTop/outBot)
	}
	dir := t.TempDir()
	var buf bytes.Buffer
	png.Encode(&buf, img)
	for n := 1; n <= 4; n++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.png", n)), buf.Bytes(), 0o644)
	}
	if err := makeLit(dir); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(filepath.Join(dir, "1.png"))
	if !bytes.Equal(orig, buf.Bytes()) {
		t.Error("the original was changed")
	}
	if !fileExists(filepath.Join(dir, litDir, "4.png")) || !fileExists(filepath.Join(dir, litDir, litMark)) {
		t.Error("lit copies not made")
	}
}

// The edge clean-up drops specks, fills pinholes, keeps a real gap, and feathers the outline.
func TestCleanEdges(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	solid := color.NRGBA{90, 120, 160, 255}
	for y := 20; y < 180; y++ {
		for x := 40 + (y-20)/8; x < 160; x++ { // the left edge is a long, shallow stair-step
			img.Set(x, y, solid)
		}
	}
	img.Set(100, 100, color.NRGBA{}) // a pinhole
	for y := 60; y < 140; y++ {      // a real gap, big enough to keep
		for x := 70; x < 90; x++ {
			img.Set(x, y, color.NRGBA{})
		}
	}
	img.Set(10, 10, solid) // a speck
	out := cleanEdges(img)
	alphaAt := func(x, y int) uint8 { return out.NRGBAAt(x, y).A }
	if alphaAt(10, 10) != 0 {
		t.Error("the speck is still there")
	}
	if alphaAt(100, 100) < 200 {
		t.Error("the pinhole wasn't filled")
	}
	if alphaAt(80, 100) != 0 {
		t.Error("a real gap was filled in")
	}
	if c := out.NRGBAAt(120, 100); c.R != solid.R || c.G != solid.G || c.B != solid.B {
		t.Errorf("the colours changed: %v", c)
	}
	feathered := 0
	for y := 40; y < 160; y++ { // along the stair-stepped edge, where a smooth edge has part-covered pixels
		for x := 30; x < 60; x++ {
			if a := alphaAt(x, y); a > 0 && a < 255 {
				feathered++
			}
		}
	}
	if feathered < 100 {
		t.Errorf("the edge isn't smoothed: only %d part-covered pixels", feathered)
	}
}

// Pieces from a mod say which, from ReSkate's record of where each item comes from.
func TestModItems(t *testing.T) {
	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game}, titles: map[string]string{}}
	os.MkdirAll(filepath.Join(game, "Mods", ".reskate"), 0o755)
	os.WriteFile(filepath.Join(game, "Mods", ".reskate", "reskate-items.json"), []byte(`{"schema":1,"mods":[
		{"mod":"neek-Hoodies","title":"Cool_Hoodies","author":"neek","items":["own_hoodie1"]},
		{"mod":"other","title":"Other","author":"","items":["own_hoodie1","own_faker"]}]}`), 0o644)
	pieces := a.piecesWithTitles(makeRecipe("Own_Hoodie1", "Own_Vans_Era_All_Black", "Own_FAKER", "own_rctn_gesture_all_yay"))
	got := map[string]string{}
	for _, p := range pieces {
		got[p.Key] = p.Mod
	}
	if got["Own_Hoodie1"] != "Cool Hoodies by neek" {
		t.Errorf("hoodie: %q (the first mod listing an item is the one the game uses)", got["Own_Hoodie1"])
	}
	if got["Own_FAKER"] != "Other" || got["Own_Vans_Era_All_Black"] != "" {
		t.Errorf("deck %q, shoes %q", got["Own_FAKER"], got["Own_Vans_Era_All_Black"])
	}
}

// A picture request goes out in requests.json; the snap taken for it updates that fit and clears it.
func TestPictureRequest(t *testing.T) {
	if !pictureRequests {
		t.Skip("switched off for now")
	}
	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game, Imported: map[string]int64{}}, titles: map[string]string{}}
	for _, d := range []string{"fits", "rotations", "pictures", "pending"} {
		os.MkdirAll(filepath.Join(data, d), 0o755)
	}
	plugin := filepath.Join(game, "Mods", "PresetWardrobe")
	os.MkdirAll(plugin, 0o755)
	os.WriteFile(filepath.Join(plugin, "reskate-plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(plugin, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	pdata := filepath.Join(game, "Mods", ".reskate_plugin_data", "PresetWardrobe")
	snaps := filepath.Join(pdata, "snaps")
	os.MkdirAll(snaps, 0o755)
	cur := lineup()
	f, _ := a.addFit(cur[4], false)
	a.addRequest(f.Hash)
	a.syncRequests(cur, func(string) string { return "My fit" })
	b, _ := os.ReadFile(filepath.Join(pdata, "requests.json"))
	if !strings.Contains(string(b), `"name":"My fit","slot":5`) {
		t.Fatalf("requests.json: %s", b)
	}
	os.WriteFile(filepath.Join(snaps, "fit-00011.png"), []byte("\x89PNG new"), 0o644)
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(`{"singles":[{"seq":11,"request":1,"file":"fit-00011.png","worn":["Own_Baker"]}]}`), 0o644)
	ev := a.importSnaps(cur, 4)
	if len(ev) == 0 || !strings.Contains(ev[0], "Updated the pictures of") {
		t.Errorf("events: %v", ev)
	}
	pic, _ := os.ReadFile(filepath.Join(data, "pictures", f.Hash+".png"))
	if string(pic) != "\x89PNG new" {
		t.Error("the requested fit didn't get the new picture")
	}
	if len(a.cfg.Requests) != 0 {
		t.Error("the request wasn't cleared")
	}
	// dismissed in the game: gone from the app too
	a.addRequest(f.Hash)
	id := a.cfg.Requests[0].ID
	os.WriteFile(filepath.Join(pdata, "requests_dismissed.json"), []byte(fmt.Sprintf(`{"ids":[%d]}`, id)), 0o644)
	a.syncRequests(cur, func(string) string { return "My fit" })
	if len(a.cfg.Requests) != 0 {
		t.Error("a dismissed request wasn't dropped")
	}
}

// A snap for a request that shows different clothes doesn't go onto the requested fit.
func TestPictureRequestChangedOutfit(t *testing.T) {
	if !pictureRequests || !unsavedCheck {
		t.Skip("switched off for now")
	}
	game, data := t.TempDir(), t.TempDir()
	a := &App{dataDir: data, cfg: Config{GameDir: game, Imported: map[string]int64{}}, titles: map[string]string{}}
	for _, d := range []string{"fits", "rotations", "pictures", "pending"} {
		os.MkdirAll(filepath.Join(data, d), 0o755)
	}
	plugin := filepath.Join(game, "Mods", "PresetWardrobe")
	os.MkdirAll(plugin, 0o755)
	os.WriteFile(filepath.Join(plugin, "reskate-plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(plugin, "manifest.json"), []byte(`{"name":"PresetWardrobe"}`), 0o644)
	snaps := filepath.Join(game, "Mods", ".reskate_plugin_data", "PresetWardrobe", "snaps")
	os.MkdirAll(snaps, 0o755)
	cur := lineup()
	f, _ := a.addFit(cur[4], false)
	os.WriteFile(filepath.Join(data, "pictures", f.Hash+".png"), []byte("\x89PNG old"), 0o644)
	a.addRequest(f.Hash)
	os.WriteFile(filepath.Join(snaps, "fit-00012.png"), []byte("\x89PNG new"), 0o644)
	os.WriteFile(filepath.Join(snaps, "index.json"), []byte(`{"singles":[{"seq":12,"request":1,"file":"fit-00012.png","worn":["Own_Baker","Own_FAKER"]}]}`), 0o644)
	ev := a.importSnaps(cur, 4)
	pic, _ := os.ReadFile(filepath.Join(data, "pictures", f.Hash+".png"))
	if string(pic) != "\x89PNG old" {
		t.Errorf("the requested fit got a different outfit's picture; events %v", ev)
	}
	found := false
	for _, e := range ev {
		if strings.Contains(e, "hadn't saved") {
			found = true
		}
	}
	if !found {
		t.Errorf("no message saying the snap didn't match: %v", ev)
	}
}

func TestEnhance(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 40, 64))
	for y := 10; y < 60; y++ {
		for x := 12; x < 28; x++ {
			img.Set(x, y, color.NRGBA{200, 40, 40, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	path := filepath.Join(dir, "a.png")
	os.WriteFile(path, buf.Bytes(), 0o644)
	e := newEnhancer(1 << 20)
	data, err := e.get("a", path, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := png.Decode(bytes.NewReader(data))
	if err != nil || got.Bounds().Dx() != 40*upScale || got.Bounds().Dy() != 64*upScale {
		t.Fatalf("enhanced: %v %v", got.Bounds(), err)
	}
	// no dark halo: the outline keeps the colour, only its opacity fades
	c := color.NRGBAModel.Convert(got.At(23*upScale/2, 40*upScale/2)).(color.NRGBA)
	if c.A > 0 && c.A < 255 && c.R < 150 {
		t.Fatalf("halo at the outline: %v", c)
	}
	if again, _ := e.get("a", path, 0); len(again) != len(data) || e.size != len(data) {
		t.Fatal("not kept in memory")
	}
	e.clear()
	if e.size != 0 {
		t.Fatal("not cleared")
	}
}

func TestThumbnail(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 1000, 800))
	for y := 0; y < 800; y++ {
		for x := 300; x < 700; x++ {
			img.Set(x, y, color.NRGBA{200, 40, 40, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	src := filepath.Join(dir, "p.png")
	os.WriteFile(src, buf.Bytes(), 0o644)
	out := thumbnail(src, filepath.Join(dir, "thumbs"), "p")
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	got, err := png.Decode(f)
	f.Close()
	if err != nil || got.Bounds().Dx() != thumbSide || got.Bounds().Dy() != 461 {
		t.Fatalf("thumb: %v %v", got.Bounds(), err)
	}
	if c := color.NRGBAModel.Convert(got.At(thumbSide/2, 100)).(color.NRGBA); c.A != 255 || c.R < 190 || c.G > 50 {
		t.Fatalf("colour/opacity changed: %v", c)
	}
	if c := color.NRGBAModel.Convert(got.At(5, 5)).(color.NRGBA); c.A != 0 {
		t.Fatalf("empty corner: %v", c)
	}
}
