package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The game keeps an item it can't find ("for when it is installed"), so a fit made from that preset keeps it
// too. An item that isn't installed never showed in the fit's pictures (the game draws its default instead), so
// the pictures stay right without it. clearItem empties that item's slot in the skater and board groups and
// leaves everything else (the gesture groups, any other fields) as it was.
func clearItem(recipesJSON, key string) (string, int, error) {
	dec := json.NewDecoder(strings.NewReader(recipesJSON))
	dec.UseNumber()
	var groups []map[string]any
	if err := dec.Decode(&groups); err != nil {
		return "", 0, errors.New("this fit's recipe can't be read")
	}
	n := 0
	for gi, g := range groups {
		if gi > 1 {
			break
		}
		items, _ := g["items"].([]any)
		for _, x := range items {
			it, _ := x.(map[string]any)
			if it == nil {
				continue
			}
			if s, _ := it["asset"].(string); s == key {
				it["asset"] = ""
				it["parameter_bits"] = []any{}
				n++
			}
		}
	}
	if n == 0 {
		return recipesJSON, 0, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(groups); err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(buf.String()), n, nil
}

// removeMissingItem takes an uninstalled item out of a fit. The fit's identity follows its contents, so the
// fit gets a new one: its pictures, rotations, preview settings and picture request move to it. If a fit with
// the new contents already exists, they merge into it. Nothing is deleted: the old fit's file and any pictures
// that aren't used again are kept under backups.
func (a *App) removeMissingItem(hash, key string) (newHash string, merged bool, err error) {
	f, err := a.getFit(hash)
	if err != nil {
		return "", false, err
	}
	if !itemWasMissing(key) {
		return "", false, errors.New("the game hasn't said that item is missing, so it was left in")
	}
	recipes, n, err := clearItem(f.RecipesJSON, key)
	if err != nil {
		return "", false, err
	}
	if n == 0 {
		return "", false, errors.New("that item isn't part of this fit")
	}
	newHash = outfitHash(recipes)
	if newHash == hash {
		return hash, false, nil
	}
	stamp := time.Now().Format("20060102-150405")
	kept := a.dir("backups", "removed-items")
	os.MkdirAll(kept, 0o755)
	if old, err := os.ReadFile(a.dir("fits", hash+".json")); err == nil {
		os.WriteFile(filepath.Join(kept, hash+"-"+stamp+".json"), old, 0o644)
	}

	target, errT := a.getFit(newHash)
	merged = errT == nil
	if !merged {
		g := f
		g.Hash, g.RecipesJSON = newHash, recipes
		if err := a.putFit(g); err != nil {
			return "", false, err
		}
		target = g
	} else if f.Favorite && !target.Favorite || target.Hidden && !f.Hidden {
		target.Favorite = target.Favorite || f.Favorite
		target.Hidden = target.Hidden && f.Hidden
		a.putFit(target)
	}

	// the pictures: moved to the new fit, unless it has its own already (then the old ones are only set aside)
	pics := a.dir("pictures")
	if _, err := os.Stat(filepath.Join(pics, newHash+".png")); err != nil {
		os.RemoveAll(filepath.Join(pics, newHash))
		os.Rename(filepath.Join(pics, hash+".png"), filepath.Join(pics, newHash+".png"))
		os.Rename(filepath.Join(pics, hash), filepath.Join(pics, newHash))
	} else {
		os.Rename(filepath.Join(pics, hash+".png"), filepath.Join(kept, hash+"-"+stamp+".png"))
		os.Rename(filepath.Join(pics, hash), filepath.Join(kept, hash+"-"+stamp))
	}
	if matches, _ := filepath.Glob(a.dir("thumbs", hash+"-*")); len(matches) > 0 {
		for _, m := range matches {
			os.Remove(m) // rebuilt from the pictures when needed
		}
	}

	for _, rot := range a.listRotations() {
		changed := false
		for i, h := range rot.Slots {
			if h == hash {
				rot.Slots[i] = newHash
				changed = true
			}
		}
		if changed {
			rot.Updated = time.Now()
			a.putRotation(rot)
		}
	}
	if v, ok := a.cfg.Views[hash]; ok {
		if _, has := a.cfg.Views[newHash]; !has {
			a.cfg.Views[newHash] = v
		}
		delete(a.cfg.Views, hash)
	}
	for i := range a.cfg.Requests {
		if a.cfg.Requests[i].Hash == hash {
			a.cfg.Requests[i].Hash = newHash
		}
	}
	a.saveConfig()
	os.Remove(a.dir("fits", hash+".json"))
	return newHash, merged, nil
}
