package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Fits loaded live exist only in the game's memory; the game's file gets them when it closes (see pending.go).
// If the app was closed then, or the game was started afterwards, a running game has the old fits while the app
// holds the new ones. So the app remembers which game process it last loaded into (live_state.json), and when it
// finds a different one running with fits pending, it loads them again by itself once the skater is in the world.

type liveState struct {
	PID uint32 `json:"pid"`
	// When the fits went into that game's memory. The game re-applies its saved profile now and then (joining or
	// leaving a server, for one), which puts its file's fits back in memory: after that they're loaded again.
	At int64 `json:"at,omitempty"`
}

// profileApplied: when the game last said "Saved profile applied to the game" (timeline.go reads its log).
var profileApplied atomic.Int64

func (a *App) liveStatePath() string { return a.dir("live_state.json") }

func (a *App) readLiveState() (s liveState) {
	if b, err := os.ReadFile(a.liveStatePath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (a *App) saveLiveState(pid uint32) {
	writeJSON(a.liveStatePath(), liveState{PID: pid, At: time.Now().UnixNano()})
}

// liveCurrent: whether the game process pid still has the fits last loaded into it.
func (a *App) liveCurrent(pid uint32) bool {
	s := a.readLiveState()
	return s.PID == pid && profileApplied.Load() <= s.At
}

func (a *App) watchSync() {
	var lastPID uint32
	var tries int
	var next time.Time
	var seenApplied int64
	fileSeen := map[string]string{}
	for range time.Tick(time.Second) {
		pid := findSkatePID()
		if pid == 0 {
			lastPID = 0
			continue
		}
		if pid != lastPID {
			lastPID, tries = pid, 0
			next = time.Now().Add(5 * time.Second)
		}
		a.dropSaved(fileSeen)
		if ap := profileApplied.Load(); ap != seenApplied {
			// The game put its saved profile back: what was found in its memory moved, and fits loaded
			// live are gone from it until loaded again.
			seenApplied, tries = ap, 0
			next = time.Now().Add(time.Second)
		}
		if time.Now().Before(next) {
			continue
		}
		if tries >= 4 {
			continue
		}
		if _, err := os.Stat(a.dir("no_sync")); err == nil {
			continue // diagnostic: a file named no_sync turns the automatic sync off
		}
		m := a.readPending()
		if len(m) == 0 || a.liveCurrent(pid) {
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
// liveLoadMu: a load and the automatic sync take turns. A sync that waited for a load re-checks, so a fit the
// player just loaded is never replaced by an older one the app was holding.
var liveLoadMu sync.Mutex

var dropTick int

func (a *App) syncPending(pid uint32, m map[string]pendingEntry) error {
	liveLoadMu.Lock()
	defer liveLoadMu.Unlock()
	if a.liveCurrent(pid) {
		return nil // a load came first
	}
	m = a.readPending()
	if len(m) == 0 {
		return nil
	}
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
	why := "it has just started"
	if a.readLiveState().PID == pid {
		why = "it put its saved profile back"
	}
	tl.logf("APP: sync: the game (process %d) doesn't have the fits the app holds (%s); loading %d of them", pid, why, len(writes))
	res, err := patchLiveMemory(current, writes, false, false)
	if err != nil {
		return err
	}
	mem := map[string]string{}
	for _, o := range current {
		for _, w := range writes {
			if o.PresetID == w.PresetID {
				mem[w.PresetID] = reshape(o.RecipesJSON, w.RecipesJSON, false)
			}
		}
	}
	a.addPending(writes, mem, false)
	a.saveLiveState(pid)
	tl.logf("APP: sync: %s", res)
	return nil
}

// dropSaved: once the game has saved a held preset itself (ReSkate writes it to the file when the game saves it, as
// on switching presets), the file holds the fit and nothing needs holding. When the file changed while the game
// ran and the change came after the fit was loaded, the game's own save is newer: it wins too.
func (a *App) dropSaved(fileSeen map[string]string) {
	if !gameRunning() {
		clear(fileSeen) // the app writes the file itself while the game is closed
		return
	}
	if dropTick++; dropTick%3 != 0 {
		return // every 3 s
	}
	current, _, err := readPresets(a.profileDB(), a.dir("tmp"))
	if err != nil {
		return
	}
	m := a.readPending()
	var drop []string
	for _, c := range current {
		prev, seen := fileSeen[c.PresetID]
		fileSeen[c.PresetID] = c.RecipesJSON
		q, held := m[c.PresetID]
		if !held {
			continue
		}
		if seen && prev == c.RecipesJSON {
			continue // not saved again since the last look
		}
		same, held2 := fitInFile(c.RecipesJSON, q.RecipesJSON, q.Body)
		switch {
		case same && held2 == 0:
			tl.logf("APP: the game saved %s itself: its file has the fit, nothing to hold", c.PresetID)
			drop = append(drop, c.PresetID)
		case same:
			// The game can't hold some of the fit (items not installed, or face and body when they weren't loaded):
			// its save is the fit as it can wear it. The fit stays held, so the slot still shows it (with its
			// pictures), and it goes to the file when the game closes, as ReSkate itself keeps items that aren't
			// installed.
			if !seen || prev != c.RecipesJSON {
				tl.logf("APP: the game saved %s itself: the fit, apart from %d parts it can't hold; still held", c.PresetID, held2)
			}
		case seen && prev != c.RecipesJSON:
			tl.logf("APP: the game saved %s itself (a newer version than the fit held): its own save wins", c.PresetID)
			drop = append(drop, c.PresetID)
		}
	}
	if len(drop) > 0 {
		a.dropPending(drop)
	}
}

// fitInFile: whether the game's saved outfit is the held fit, slot by slot (item and settings), apart from the slots
// the game can't hold: items it hasn't got (it keeps what was there), and face and body unless they were loaded.
// held is how many slots were left out.
func fitInFile(file, fit string, body bool) (same bool, held int) {
	fg, err1 := parseRecipes(file)
	hg, err2 := parseRecipes(fit)
	if err1 != nil || err2 != nil || len(fg) != len(hg) {
		return false, 0
	}
	norm := func(r json.RawMessage) string {
		var w []uint32
		if len(r) == 0 || json.Unmarshal(r, &w) != nil {
			return ""
		}
		return fmt.Sprint(w)
	}
	for gi := range hg {
		if gi > 1 {
			break
		}
		got := map[uint64]recipeItem{}
		for _, it := range fg[gi].Items {
			got[it.Slot] = it
		}
		for _, it := range hg[gi].Items {
			if !liveSlotIncluded(gi, it.Slot, body) || (it.Asset != "" && itemWasMissing(it.Asset)) {
				if g, ok := got[it.Slot]; !ok || g.Asset != it.Asset {
					held++
				}
				continue
			}
			g, ok := got[it.Slot]
			if !ok {
				if it.Asset == "" {
					continue
				}
				return false, 0
			}
			if g.Asset != it.Asset && g.Asset != "" && itemWasMissing(g.Asset) {
				// ReSkate's own hold: the preset had an item that isn't installed, so the game showed the slot's
				// default instead, and the fit puts that same default there. ReSkate takes the slot for still showing
				// its stand-in and saves the held item back (it keeps items for when they're installed again).
				held++
				continue
			}
			if g.Asset != it.Asset || norm(g.ParameterBit) != norm(it.ParameterBit) {
				return false, 0
			}
		}
	}
	return true, held
}

// watchSelection: when the game switches to another preset that holds a fit loaded while it wasn't the one worn,
// the fit is put on. A preset the game hasn't shown yet this session has no record in its manager, so a load could
// only hold the fit for it: the game makes the record from ReSkate's profile (the older fit) when the preset is first
// shown, and this puts the held fit on then, through the data model as a load does. A preset that has a record had
// the fit written into it at the load, and the game shows it by itself.
func (a *App) watchSelection() {
	last, lastPID, lastRecords := -1, uint32(0), 0
	var lastErr string
	var r selectionReader
	defer r.Close()
	for range time.Tick(50 * time.Millisecond) {
		if !modelLoadOn {
			continue
		}
		pid, sel, records, err := r.Read()
		if pid == 0 {
			last, lastPID, lastRecords = -1, 0, 0
			continue
		}
		if err != nil {
			if e := err.Error(); e != lastErr {
				lastErr = e
				tl.logf("APP: the game's selected preset isn't readable now: %v", err)
			}
			continue
		}
		lastErr = ""
		if pid != lastPID {
			lastPID, last, lastRecords = pid, sel, records
			tl.logf("APP: the game has preset %d selected (%d records)", sel+1, records)
			continue
		}
		if records > lastRecords {
			// the game made records for presets shown for the first time: a held fit goes in at once, so the game
			// shows it from its record (it may already have drawn the record's older fit; see below)
			a.writeHeldRecords(lastRecords, records)
		}
		lastRecords = records
		if sel == last {
			continue
		}
		tl.logf("APP: the game switched from preset %d to %d (%d records)", last+1, sel+1, records)
		last = sel
		if sel >= 0 && sel < len(presetIDs) {
			setLiveSelected(presetIDs[sel], sel)
		}
		a.putHeldOn(pid, sel)
	}
}

// writeHeldRecords writes the held fits of the presets whose records the game has just made (from to to-1).
func (a *App) writeHeldRecords(from, to int) {
	m := a.readPending()
	if len(m) == 0 {
		return
	}
	var current []StoredPreset
	for i := from; i < to && i < len(presetIDs); i++ {
		q, ok := m[presetIDs[i]]
		if !ok {
			continue
		}
		if current == nil {
			var err error
			if current, _, err = readPresets(a.profileDB(), a.dir("tmp")); err != nil {
				return
			}
		}
		err := writePresetRecord(current, []StoredPreset{q.StoredPreset}, q.Body, i)
		tl.logf("APP: preset %d's record made by the game: its held fit written in (error: %v)", i+1, err)
	}
}

// putHeldOn puts the fit held for preset sel on, if there is one: as soon as the game shows the preset (its older
// fit from ReSkate's profile, when the preset is new this session), or at once if it shows the held fit already.
func (a *App) putHeldOn(pid uint32, sel int) {
	if sel < 0 || sel >= len(presetIDs) {
		return
	}
	id := presetIDs[sel]
	q, ok := a.readPending()[id]
	if !ok {
		return
	}
	liveLoadMu.Lock()
	defer liveLoadMu.Unlock()
	current, _, err := readPresets(a.profileDB(), a.dir("tmp"))
	if err != nil {
		return
	}
	old := ""
	for _, c := range current {
		if c.PresetID == id {
			old = c.RecipesJSON
		}
	}
	if ms, ok := a.readMenuState(); ok && ms.Known {
		liveMenuOpen.Store(ms.Open)
	} else {
		liveMenuOpen.Store(true)
	}
	st := time.Now()
	waited := "timed out"
	for time.Since(st) < 1500*time.Millisecond {
		if presetShowing(q.RecipesJSON) {
			waited = "the held fit shows already"
			break
		}
		if old != "" && presetShowing(old) {
			waited = "the preset's older fit shows"
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	tl.logf("APP: preset %d shown: %s after %d ms", sel+1, waited, time.Since(st).Milliseconds())
	// the Skaters page makes its preview of the preset a moment after the switch: tried again until it shows it
	for try := 1; try <= 5; try++ {
		res, err := patchLiveMemory(current, []StoredPreset{q.StoredPreset}, q.Body, false)
		notOn, _ := modelNotShown.Load().(string)
		menu := liveMenuOpen.Load()
		tl.logf("APP: preset %d shown: its held fit put on (try %d): %s (error: %v; not on: %q; menu open %v, preview shows it %v)", sel+1, try, strings.ReplaceAll(res, "\n", "; "), err, notOn, menu, previewShows.Load())
		if err == nil && notOn == "" && (!menu || previewShows.Load()) {
			a.saveLiveState(pid)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
