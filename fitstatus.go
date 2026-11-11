package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lineup_status.json, for the plugin's card on the game's Skaters page: for each of the 10 slots, whether its fit is
// in the Fit Catalogue, whether that fit has pictures, its name and a few of its pieces, and what its skater wears
// (slot hash -> item hash). The plugin compares that with the skater on screen to tell which slot is showing,
// straight away while you browse the presets.

type fitStatusSlot struct {
	Slot    int               `json:"slot"`
	Saved   bool              `json:"saved"`
	Pics    bool              `json:"pics"`
	Name    string            `json:"name"`
	Parts   []fitPart         `json:"parts"`
	Changed []string          `json:"changed,omitempty"` // edited in the game, not saved
	Skater  map[string]uint32 `json:"skater"`            // slot hash -> item hash (items the game hasn't got left out)
}

type fitPart struct {
	C string `json:"c"`           // category: Top, Shoes, Deck...
	T string `json:"t"`           // the item's name
	B bool   `json:"b,omitempty"` // part of the board
}

type fitStatus struct {
	Selected int             `json:"selected"` // the slot the game's file says is selected (1-10)
	Slots    []fitStatusSlot `json:"slots"`
}

func (a *App) buildFitStatus() (fitStatus, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, sel, err := readPresets(a.profileDB(), a.dir("tmp"))
	if err != nil {
		return fitStatus{}, false
	}
	cur = a.withLive(a.withPending(cur))
	byHash := map[string]Fit{}
	for _, f := range a.listFits() {
		byHash[f.Hash] = f
	}
	out := fitStatus{Selected: sel + 1}
	ls := shownChanges()
	for _, p := range cur {
		slot := slotNumber(p.PresetID)
		if slot == 0 {
			continue
		}
		h := outfitHash(p.RecipesJSON)
		f, ok := byHash[h]
		saved := ok && !f.Hidden
		name := a.autoName(p.RecipesJSON)
		if saved {
			name = a.fitName(f)
		}
		s := fitStatusSlot{Slot: slot, Saved: saved, Pics: saved && a.pictureURL(h) != "", Name: name, Skater: map[string]uint32{}}
		var board []fitPart
		for _, pc := range a.piecesWithTitles(p.RecipesJSON) {
			if pc.Title == "" {
				continue
			}
			if pc.Board {
				board = append(board, fitPart{pc.Category, pc.Title, true})
			} else {
				s.Parts = append(s.Parts, fitPart{pc.Category, pc.Title, false})
			}
		}
		s.Parts = append(s.Parts, board...) // the skater's pieces first, then the board's
		if ls != nil && ls.PresetID == p.PresetID {
			s.Changed = describeChanges(ls.Changes)
		}
		if groups, err := parseRecipes(p.RecipesJSON); err == nil && len(groups) > 0 {
			for _, it := range groups[0].Items {
				if it.Asset != "" && itemWasMissing(it.Asset) {
					continue // the game shows a default there: it can't be compared
				}
				s.Skater[fmt.Sprint(it.Slot)] = itemHash(it.Asset)
			}
		}
		out.Slots = append(out.Slots, s)
	}
	return out, true
}

func (a *App) watchFitStatus() {
	var last string
	var wrote time.Time
	for range time.Tick(1500 * time.Millisecond) {
		dir := a.pluginDataDir()
		if dir == "" || !gameRunning() {
			last = ""
			continue
		}
		st, ok := a.buildFitStatus()
		if !ok {
			continue
		}
		b, err := json.Marshal(st)
		if err != nil {
			continue
		}
		if string(b) == last && time.Since(wrote) < 10*time.Second {
			continue
		}
		path := filepath.Join(dir, "lineup_status.json")
		if os.WriteFile(path+".tmp", b, 0o644) == nil && os.Rename(path+".tmp", path) == nil {
			last, wrote = string(b), time.Now()
		}
	}
}
