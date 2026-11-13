package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Presets loaded into the running game (see memlive_windows.go) exist only in the game's memory: it
// doesn't save them to its file. So they're kept here until skate. closes, then written to the file
// (a write with the game closed is safe). Meanwhile the app shows them as what's in the game.

var pendingMu sync.Mutex

// A preset loaded live. The StoredPreset is the fit, written to the file when the game closes. Mem is the
// same outfit in the shape the game's memory has: its tables keep the slots of the preset they were
// loaded from, whatever slots the fit has. That's what the next live load has to search for.
type pendingEntry struct {
	StoredPreset
	Mem string `json:"mem_recipes"`
	// The game's skater has been seen wearing this since it was loaded (the plugin reports what it wears). Until
	// then a difference means the load hasn't shown yet, not that something was edited in the game.
	Verified bool `json:"verified,omitempty"`
	// Face and body were loaded with the fit (otherwise the game keeps its own there).
	Body bool `json:"body,omitempty"`
}

func (a *App) pendingPath() string { return a.dir("pending_live.json") }

func (a *App) readPending() map[string]pendingEntry {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	return a.readPendingLocked()
}

func (a *App) readPendingLocked() map[string]pendingEntry {
	out := map[string]pendingEntry{}
	if b, err := os.ReadFile(a.pendingPath()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func (a *App) savePendingLocked(m map[string]pendingEntry) {
	if len(m) == 0 {
		os.Remove(a.pendingPath())
		return
	}
	writeJSON(a.pendingPath(), m)
}

func (a *App) addPending(writes []StoredPreset, mem map[string]string, body bool) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	m := a.readPendingLocked()
	for _, p := range writes {
		m[p.PresetID] = pendingEntry{StoredPreset: p, Mem: mem[p.PresetID], Body: body}
	}
	a.savePendingLocked(m)
}

func (a *App) dropPending(ids []string) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	m := a.readPendingLocked()
	for _, id := range ids {
		delete(m, id)
	}
	a.savePendingLocked(m)
}

// What the game is wearing now: its file, with anything loaded live on top.
func (a *App) withPending(current []StoredPreset) []StoredPreset {
	m := a.readPending()
	if len(m) == 0 {
		return current
	}
	out := make([]StoredPreset, 0, len(current)+len(m))
	seen := map[string]bool{}
	for _, p := range current {
		if q, ok := m[p.PresetID]; ok {
			p = q.StoredPreset
		}
		seen[p.PresetID] = true
		out = append(out, p)
	}
	for id, q := range m {
		if !seen[id] {
			out = append(out, q.StoredPreset)
		}
	}
	return out
}

// The same, but each live-loaded preset in the shape the game's memory has (see pendingEntry).
func (a *App) withPendingMem(current []StoredPreset) []StoredPreset {
	m := a.readPending()
	out := append([]StoredPreset(nil), current...)
	for i, p := range out {
		if q, ok := m[p.PresetID]; ok && q.Mem != "" {
			out[i].RecipesJSON = q.Mem
		}
	}
	return out
}

// The fit's items poured into the slots of shape (the recipes the game has loaded): a slot the fit
// doesn't have is empty, a slot of the fit that shape lacks is left out. Without body, the face and body
// slots keep what shape has, as the live load leaves them alone.
func reshape(shape, fit string, body bool) string {
	var sg []map[string]json.RawMessage
	fg, err := parseRecipes(fit)
	if err != nil || json.Unmarshal([]byte(shape), &sg) != nil || len(sg) != len(fg) {
		return fit
	}
	for gi, g := range sg {
		var items []map[string]json.RawMessage
		if json.Unmarshal(g["items"], &items) != nil {
			continue
		}
		by := map[uint64]string{}
		for _, it := range fg[gi].Items {
			by[it.Slot] = it.Asset
		}
		for _, it := range items {
			var slot uint64
			json.Unmarshal(it["slot"], &slot)
			if !liveSlotIncluded(gi, slot, body) {
				continue // left as the game has it
			}
			it["asset"], _ = json.Marshal(by[slot])
		}
		g["items"], _ = json.Marshal(items)
	}
	out, err := json.Marshal(sg)
	if err != nil {
		return fit
	}
	return string(out)
}

// Once the game is closed, the live-loaded presets go into its file.
func (a *App) watchPending() {
	for range time.Tick(2 * time.Second) {
		if gameRunning() {
			continue
		}
		m := a.readPending()
		if len(m) == 0 {
			continue
		}
		var writes []StoredPreset
		var ids []string
		for id, p := range m {
			writes = append(writes, p.StoredPreset)
			ids = append(ids, id)
		}
		_, err := writePresets(a.profileDB(), a.dir("backups"), writes, false, "saving the fits you loaded live, when skate. closed")
		tl.logf("APP: game closed, wrote %d live-loaded presets to its file (error: %v)", len(writes), err)
		if err == nil {
			a.dropPending(ids)
			a.note("ok", "skate. closed: saved the %s you loaded live into the game's file", plural(len(writes), "fit", "fits"))
		} else {
			a.note("warn", "skate. closed, but the fits you loaded live couldn't be saved into the game's file: %v", err)
		}
	}
}

// liveSlotIncluded: whether a slot is one of the clothes or the board. The skater's other slots are
// the face and body (skin, head, hair, eyes, voice and so on) and the gestures.
func liveSlotIncluded(group int, slot uint64, body bool) bool {
	if body {
		return true
	}
	if group == 0 {
		_, clothing := skaterSlots[slot]
		return clothing
	}
	return group == 1
}
