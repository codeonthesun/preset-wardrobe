package main

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// The game keeps 10 skater presets in the cosmetic_loadouts table of
// %LOCALAPPDATA%\ReSkate\profiles\<profile>\reskate.sqlite3, in menu order:
// "default", "Loadout02" ... "Loadout10".
var presetIDs = []string{"default", "Loadout02", "Loadout03", "Loadout04", "Loadout05",
	"Loadout06", "Loadout07", "Loadout08", "Loadout09", "Loadout10"}

// One preset exactly as the game stores it.
type StoredPreset struct {
	PresetID       string `json:"preset_id"`
	RecipeFormat   int    `json:"recipe_format"`
	RecipesJSON    string `json:"recipes_json"`
	ExtensionsJSON string `json:"extensions_json"`
}

type recipeItem struct {
	Asset        string          `json:"asset"`
	ParameterBit json.RawMessage `json:"parameter_bits"`
	Slot         uint64          `json:"slot"`
}

type recipe struct {
	Items []recipeItem `json:"items"`
}

// What a preset is wearing, for showing it.
type Piece struct {
	Category string `json:"category"`
	Key      string `json:"key"`
	Title    string `json:"title"`
	Board    bool   `json:"board,omitempty"`   // part of the board, not the skater
	Mod      string `json:"mod,omitempty"`     // the mod it's from: "<title> by <author>"
	Missing  bool   `json:"missing,omitempty"` // the game said it hasn't got this item
}

// Slot ids seen in the save, and what they hold. Group 0 is the skater, group 1 the board.
var skaterSlots = map[uint64]string{
	2889696005: "Headwear",
	2890690067: "Top",
	3228215319: "Bottoms",
	902165545:  "Shoes",
	902472012:  "Socks",
	841072201:  "Gloves",
	1276597169: "Face",
	3335133427: "Accessory",
	4170730925: "Accessory",
	2601889598: "Accessory",
}

var boardSlots = map[uint64]string{
	1711740456: "Deck",
	1969053408: "Grip",
	2540640452: "Grip cutout",
	182294718:  "Trucks",
	3359357262: "Wheels",
}

var categoryOrder = map[string]int{"Headwear": 0, "Face": 1, "Top": 2, "Gloves": 3, "Accessory": 4,
	"Bottoms": 5, "Socks": 6, "Shoes": 7, "Other": 8, "Deck": 9, "Grip": 10, "Grip cutout": 11, "Trucks": 12, "Wheels": 13}

func isBodyPart(asset string) bool {
	a := strings.ToLower(asset)
	if a == "" || strings.HasSuffix(a, "_empty") {
		return true
	}
	for _, p := range []string{"own_cust", "own_voicetype", "own_locomotion", "own_lipcolor", "own_lipstyle",
		"own_stubble", "own_moles", "own_tattoo", "own_rctn_"} {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return false
}

func parseRecipes(recipesJSON string) ([]recipe, error) {
	var r []recipe
	err := json.Unmarshal([]byte(recipesJSON), &r)
	return r, err
}

// Pieces of clothing and board parts, in a readable order.
func piecesOf(recipesJSON string) []Piece {
	groups, err := parseRecipes(recipesJSON)
	if err != nil {
		return nil
	}
	var out []Piece
	for gi, g := range groups {
		if gi > 1 {
			break // gestures
		}
		for _, it := range g.Items {
			if isBodyPart(it.Asset) {
				continue
			}
			cat := ""
			if gi == 0 {
				cat = skaterSlots[it.Slot]
			} else {
				cat = boardSlots[it.Slot]
			}
			if cat == "" {
				cat = "Other"
			}
			out = append(out, Piece{Category: cat, Key: it.Asset, Board: gi == 1})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return categoryOrder[out[i].Category] < categoryOrder[out[j].Category] })
	return out
}

// A fingerprint of the outfit and board (not gestures), stable however the JSON is written.
// Pictures are filed under it, so a picture follows its outfit into any bank.
func outfitHash(recipesJSON string) string {
	groups, err := parseRecipes(recipesJSON)
	if err != nil {
		sum := sha1.Sum([]byte(recipesJSON))
		return hex.EncodeToString(sum[:8])
	}
	var parts []string
	for gi, g := range groups {
		if gi > 1 {
			break
		}
		for _, it := range g.Items {
			parts = append(parts, fmt.Sprintf("%d:%d=%s|%s", gi, it.Slot, it.Asset, string(it.ParameterBit)))
		}
	}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}

// Readable fallback for an item key: Own_Vans_Era_All_Black -> Vans Era All Black.
func readableKey(key string) string {
	k := key
	for _, p := range []string{"Own_", "own_", "OWN_"} {
		k = strings.TrimPrefix(k, p)
	}
	k = strings.ReplaceAll(k, "_", " ")
	k = strings.Join(strings.Fields(k), " ")
	return k
}

// ---- reading and writing the profile ----------------------------------------------------------

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Reads the presets from a private copy of the profile (database + its WAL), so the live file is
// never touched while the game may be running.
func readPresets(dbPath, tmpDir string) ([]StoredPreset, int, error) {
	snap, err := os.MkdirTemp(tmpDir, "read-")
	if err != nil {
		return nil, -1, err
	}
	defer os.RemoveAll(snap)
	copyDB := filepath.Join(snap, "reskate.sqlite3")
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := copyFile(dbPath, copyDB); err != nil {
			return nil, -1, err
		}
		os.Remove(copyDB + "-wal")
		if _, err := os.Stat(dbPath + "-wal"); err == nil {
			if err := copyFile(dbPath+"-wal", copyDB+"-wal"); err != nil {
				return nil, -1, err
			}
		}
		presets, sel, err := readPresetsFrom(copyDB)
		if err == nil {
			return presets, sel, nil
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond) // the game may have been mid-write
	}
	return nil, -1, lastErr
}

func readPresetsFrom(path string) ([]StoredPreset, int, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, -1, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT preset_id, recipe_format, recipes_json, extensions_json FROM cosmetic_loadouts`)
	if err != nil {
		return nil, -1, err
	}
	byID := map[string]StoredPreset{}
	for rows.Next() {
		var p StoredPreset
		if err := rows.Scan(&p.PresetID, &p.RecipeFormat, &p.RecipesJSON, &p.ExtensionsJSON); err != nil {
			rows.Close()
			return nil, -1, err
		}
		byID[p.PresetID] = p
	}
	rows.Close()
	var out []StoredPreset
	for _, id := range presetIDs {
		if p, ok := byID[id]; ok {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, -1, errors.New("no presets found in the profile")
	}
	sel := -1
	var ext string
	if err := db.QueryRow(`SELECT value_json FROM profile_extensions WHERE section='customization'`).Scan(&ext); err == nil {
		var c struct {
			Selected *int `json:"selected_preset_index"`
		}
		if json.Unmarshal([]byte(ext), &c) == nil && c.Selected != nil {
			sel = *c.Selected
		}
	}
	return out, sel, nil
}

// Backs up the whole profile, then writes the presets into the game's slots, then reads them back. If what is
// in the file isn't what was written, the backup is put back (when the game is closed). why says what the
// write was for; it is kept with the backup so it can be told apart in Settings.
func writePresets(dbPath, backupDir string, presets []StoredPreset, live bool, why string) (string, error) {
	var ids []string
	for _, p := range presets {
		ids = append(ids, p.PresetID)
	}
	dir, err := backupProfile(dbPath, backupDir, why, ids)
	if err != nil {
		return "", fmt.Errorf("backup failed, nothing was changed: %w", err)
	}
	dir2, err := writePresetsNoCheck(dbPath, dir, presets, live)
	if err != nil {
		return dir2, err
	}
	if bad := checkWritten(dbPath, presets); bad != "" {
		if live {
			return dir, fmt.Errorf("the game's file doesn't hold what was written (%s); the backup from just before is in %s", bad, dir)
		}
		if rerr := restoreProfile(dir, dbPath); rerr != nil {
			return dir, fmt.Errorf("the game's file doesn't hold what was written (%s), and putting the backup back failed: %v; the backup is in %s", bad, rerr, dir)
		}
		return dir, fmt.Errorf("the game's file didn't hold what was written (%s), so it was put back as it was", bad)
	}
	pruneBackups(backupDir)
	return dir, nil
}

// checkWritten reads the presets back from the file: "" if they are all as written, or which isn't.
func checkWritten(dbPath string, presets []StoredPreset) string {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return "it couldn't be opened"
	}
	defer db.Close()
	for _, p := range presets {
		var got string
		if err := db.QueryRow(`SELECT recipes_json FROM cosmetic_loadouts WHERE preset_id=?`, p.PresetID).Scan(&got); err != nil {
			return p.PresetID + " couldn't be read"
		}
		if got != p.RecipesJSON {
			return p.PresetID + " differs"
		}
	}
	return ""
}

func writePresetsNoCheck(dbPath, dir string, presets []StoredPreset, live bool) (string, error) {

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return dir, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 3000`); err != nil {
		return dir, err
	}
	tx, err := db.Begin()
	if err != nil {
		return dir, err
	}
	for _, p := range presets {
		if p.RecipeFormat == 0 {
			p.RecipeFormat = 1
		}
		if p.ExtensionsJSON == "" {
			p.ExtensionsJSON = "{}"
		}
		_, err := tx.Exec(`INSERT INTO cosmetic_loadouts(preset_id, recipe_format, recipes_json, extensions_json)
			VALUES(?,?,?,?) ON CONFLICT(preset_id) DO UPDATE SET recipe_format=excluded.recipe_format,
			recipes_json=excluded.recipes_json, extensions_json=excluded.extensions_json`,
			p.PresetID, p.RecipeFormat, p.RecipesJSON, p.ExtensionsJSON)
		if err != nil {
			tx.Rollback()
			return dir, fmt.Errorf("could not write %s: %w", p.PresetID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return dir, err
	}
	if live {
		db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`) // the game has the file open: never force it
	} else {
		db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	return dir, nil
}

// The game's hash of an item or slot name (djb2 with xor); an empty name is 0.
func itemHash(asset string) uint32 {
	if asset == "" {
		return 0
	}
	h := uint32(5381)
	for i := 0; i < len(asset); i++ {
		h = (h * 33) ^ uint32(asset[i])
	}
	return h
}
