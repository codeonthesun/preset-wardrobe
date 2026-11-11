package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Picture requests: "Update picture" on a fit in the app asks the plugin for a new snap of it. The
// app keeps the requests (config) and writes them for the plugin as requests.json; the plugin lists
// them under Messages, and its snap comes back marked with the request, so the new pictures go
// to that fit. A request the plugin dismisses comes back in requests_dismissed.json.

// Switched off for now, kept to finish later (the plugin has matching switches):
//
//	pictureRequests: "Update picture" on a fit, and the requests going to the plugin
//	unsavedCheck:    not filing a snap whose board isn't any saved preset's (saved_outfits.json)
const (
	pictureRequests = false
	unsavedCheck    = false
)

type PicRequest struct {
	ID   int       `json:"id"`
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
}

func (a *App) requestFor(hash string) *PicRequest {
	for i := range a.cfg.Requests {
		if a.cfg.Requests[i].Hash == hash {
			return &a.cfg.Requests[i]
		}
	}
	return nil
}

func (a *App) addRequest(hash string) {
	if a.requestFor(hash) != nil {
		return
	}
	a.cfg.RequestSeq++
	a.cfg.Requests = append(a.cfg.Requests, PicRequest{ID: a.cfg.RequestSeq, Hash: hash, At: time.Now()})
	a.saveConfig()
}

func (a *App) dropRequest(id int) {
	out := a.cfg.Requests[:0]
	for _, r := range a.cfg.Requests {
		if r.ID != id {
			out = append(out, r)
		}
	}
	a.cfg.Requests = out
	a.saveConfig()
}

// syncRequests writes requests.json for the plugin (each with the fit's name and the slot it's in
// now, 0 if none) and takes back the ones it dismissed.
func (a *App) syncRequests(current []StoredPreset, nameOf func(hash string) string) {
	dir := a.pluginDataDir()
	if dir == "" {
		return
	}
	var dismissed struct {
		IDs []int `json:"ids"`
	}
	if readJSON(filepath.Join(dir, "requests_dismissed.json"), &dismissed) == nil {
		for _, id := range dismissed.IDs {
			for _, r := range a.cfg.Requests {
				if r.ID == id {
					a.dropRequest(id)
				}
			}
		}
	}
	slotOf := map[string]int{}
	for _, p := range current {
		for i, id := range presetIDs {
			if id == p.PresetID {
				slotOf[outfitHash(p.RecipesJSON)] = i + 1
			}
		}
	}
	type out struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Slot int    `json:"slot"`
	}
	list := []out{}
	for _, r := range a.cfg.Requests {
		list = append(list, out{ID: r.ID, Name: nameOf(r.Hash), Slot: slotOf[r.Hash]})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	b, _ := json.Marshal(map[string]any{"version": 1, "requests": list})
	if string(b) == a.requestsWritten {
		return
	}
	if os.WriteFile(filepath.Join(dir, "requests.json"), b, 0o644) == nil {
		a.requestsWritten = string(b)
	}
}

// requestSnap files a snap the plugin took for a request: onto the requested fit, if that's the
// outfit in the snap. ok false: it isn't (the outfit was changed, or a different preset was
// selected), so the snap is filed the usual way; the request is done either way.
func (a *App) requestSnap(req int, current []StoredPreset, best []int, presetFor func(int) *StoredPreset,
	worn []string, store func(hash string)) (msg string, ok bool) {
	var r *PicRequest
	for i := range a.cfg.Requests {
		if a.cfg.Requests[i].ID == req {
			r = &a.cfg.Requests[i]
		}
	}
	if r == nil {
		return "", false
	}
	hash, id := r.Hash, r.ID
	defer a.dropRequest(id)
	for _, slot := range best {
		p := presetFor(slot)
		if p != nil && outfitHash(p.RecipesJSON) == hash && wornIsFit(worn, p.RecipesJSON, knownItems(current)) {
			f, _ := a.addFit(*p, false)
			store(f.Hash)
			return fmt.Sprintf("Updated the pictures of %s.", a.fitName(f)), true
		}
	}
	return "", false
}

func (a *App) fitByHash(hash string) (Fit, bool) {
	var f Fit
	if !hashRe.MatchString(hash) || readJSON(a.dir("fits", hash+".json"), &f) != nil || f.Hash == "" {
		return Fit{}, false
	}
	return f, true
}

// savedOutfits writes saved_outfits.json for the plugin: each slot's items as the game last saved
// them (lower case). Before a snap, the plugin checks that what's worn is one of these: if not, the
// preset has changes the game hasn't saved yet, which the board pictures' trip to the stage would
// throw away (and the app couldn't file: there's nothing saved to make the fit from).
func (a *App) savedOutfits(current []StoredPreset) {
	dir := a.pluginDataDir()
	if dir == "" {
		return
	}
	type slotItems struct {
		Slot  int      `json:"slot"`
		Items []string `json:"items"`
	}
	list := []slotItems{}
	for _, p := range current {
		slot := 0
		for i, id := range presetIDs {
			if id == p.PresetID {
				slot = i + 1
			}
		}
		if slot == 0 {
			continue
		}
		list = append(list, slotItems{Slot: slot, Items: recipeAssets(p.RecipesJSON)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Slot < list[j].Slot })
	b, _ := json.Marshal(map[string]any{"version": 1, "at": time.Now().Unix(), "slots": list})
	// rewritten when the outfits change (the time alone doesn't count)
	sig, _ := json.Marshal(list)
	if string(sig) == a.outfitsWritten {
		return
	}
	if os.WriteFile(filepath.Join(dir, "saved_outfits.json"), b, 0o644) == nil {
		a.outfitsWritten = string(sig)
	}
}

// recipeAssets: every item asset in the outfit and board groups (not gestures), lower case.
func recipeAssets(recipes string) []string {
	groups, err := parseRecipes(recipes)
	if err != nil {
		return []string{}
	}
	out := []string{}
	for gi, g := range groups {
		if gi > 1 {
			break
		}
		for _, it := range g.Items {
			if it.Asset != "" {
				out = append(out, strings.ToLower(it.Asset))
			}
		}
	}
	sort.Strings(out)
	return out
}

// wornIsFit: every item worn in the snap is part of the fit, leaving out items no saved preset
// has at all (known): the game's defaults, like its stock wheel colour, aren't saved in presets.
// What's worn is the board's parts, so this tells boards apart, not clothes.
func wornIsFit(worn []string, recipes string, known map[string]bool) bool {
	have := map[string]bool{}
	for _, k := range recipeAssets(recipes) {
		have[k] = true
	}
	for _, w := range worn {
		w = strings.ToLower(w)
		if known != nil && !known[w] {
			continue
		}
		if !have[w] {
			return false
		}
	}
	return true
}

// knownItems: every item any of these presets has.
func knownItems(current []StoredPreset) map[string]bool {
	known := map[string]bool{}
	for _, p := range current {
		for _, k := range recipeAssets(p.RecipesJSON) {
			known[k] = true
		}
	}
	return known
}
