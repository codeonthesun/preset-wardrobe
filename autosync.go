package main

import (
	"encoding/json"
	"os"
	"time"
)

// Fits loaded live exist only in the game's memory; the game's file gets them when it closes (see pending.go).
// If the app was closed then, or the game was started afterwards, a running game has the old fits while the app
// holds the new ones. So the app remembers which game process it last loaded into (live_state.json), and when it
// finds a different one running with fits pending, it loads them again by itself once the skater is in the world.

type liveState struct {
	PID uint32 `json:"pid"`
}

func (a *App) liveStatePath() string { return a.dir("live_state.json") }

func (a *App) readLiveState() (s liveState) {
	if b, err := os.ReadFile(a.liveStatePath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (a *App) saveLiveState(pid uint32) { writeJSON(a.liveStatePath(), liveState{PID: pid}) }

func (a *App) watchSync() {
	var lastPID, warmedPID uint32
	var tries int
	var next time.Time
	for range time.Tick(3 * time.Second) {
		pid := findSkatePID()
		if pid == 0 {
			lastPID = 0
			continue
		}
		if pid != lastPID {
			lastPID, tries = pid, 0
			next = time.Now().Add(5 * time.Second)
		}
		if time.Now().Before(next) {
			continue
		}
		if warmedPID != pid {
			if ms, ok := a.readMenuState(); ok && ms.Known && !ms.Open {
				warmedPID = pid
				go warmBoard() // the board's search happens now, not at the first fit
			}
		}
		if tries >= 4 {
			continue
		}
		if _, err := os.Stat(a.dir("no_sync")); err == nil {
			continue // diagnostic: a file named no_sync turns the automatic sync off
		}
		m := a.readPending()
		if len(m) == 0 || a.readLiveState().PID == pid {
			continue
		}
		ms, ok := a.readMenuState()
		if !(ok && ms.Known && !ms.Open) {
			continue // the skater isn't in the world yet
		}
		tries++
		if err := a.syncPending(pid, m); err != nil {
			tl.logf("APP: sync: couldn't load the pending fits into the game (try %d): %v", tries, err)
			next = time.Now().Add(20 * time.Second)
		}
	}
}

// syncPending loads the pending fits into the game that has just started, whose memory holds what its file did.
func (a *App) syncPending(pid uint32, m map[string]pendingEntry) error {
	current, sel, err := readPresets(a.profileDB(), a.dir("tmp"))
	if err != nil {
		return err
	}
	var writes []StoredPreset
	var loaded []string
	for _, id := range presetIDs {
		if q, ok := m[id]; ok {
			writes = append(writes, q.StoredPreset)
			loaded = append(loaded, id)
		}
	}
	if len(writes) == 0 {
		return nil
	}
	selID := ""
	if sel >= 0 && sel < len(presetIDs) {
		selID = presetIDs[sel]
	}
	setLiveSelected(selID, sel)
	tl.logf("APP: sync: the game (process %d) doesn't have the fits the app holds; loading %d of them", pid, len(writes))
	res, err := patchLiveMemory(current, writes, false, false)
	if err != nil {
		return err
	}
	armSweep(current, writes, false)
	mem := map[string]string{}
	for _, o := range current {
		for _, w := range writes {
			if o.PresetID == w.PresetID {
				mem[w.PresetID] = reshape(o.RecipesJSON, w.RecipesJSON, false)
			}
		}
	}
	a.addPending(writes, mem)
	a.saveLiveState(pid)
	tl.logf("APP: sync: %s", res)
	for _, id := range loaded {
		if id == selID {
			a.armRefresh(loaded)
			break
		}
	}
	return nil
}
