package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// What the skater wears in the game right now, as the plugin reports it (skater_now.json), compared with what
// the app thinks the selected preset holds. Changes made in the game's editor that the game doesn't save (it
// refuses to save a preset that was loaded live) show up here: the Lineup shows the slot as it really is.

type skaterNow struct {
	N      int       `json:"n"`
	Slots  luaItems  `json:"slots"`
	Params luaParams `json:"params"`
}

// The plugin's JSON writes an empty table as [] or {} alike, so these read either.
type luaItems map[string]string

func (m *luaItems) UnmarshalJSON(b []byte) error {
	*m = luaItems{}
	if t := strings.TrimSpace(string(b)); strings.HasPrefix(t, "[") || t == "null" {
		return nil
	}
	return json.Unmarshal(b, (*map[string]string)(m))
}

type luaParams map[string][]float64

func (m *luaParams) UnmarshalJSON(b []byte) error {
	*m = luaParams{}
	if t := strings.TrimSpace(string(b)); strings.HasPrefix(t, "[") || t == "null" {
		return nil
	}
	return json.Unmarshal(b, (*map[string][]float64)(m))
}

type liveChange struct {
	Slot     uint64    `json:"slot"`
	SlotName string    `json:"slot_name"`
	Was      string    `json:"was"`
	Now      string    `json:"now"`
	Params   []float64 `json:"params,omitempty"` // the new parameters, when they are what changed
}

func (a *App) readSkaterNow() (skaterNow, bool) {
	var sn skaterNow
	dir := a.pluginDataDir()
	if dir == "" {
		return sn, false
	}
	path := filepath.Join(dir, "skater_now.json")
	st, err := os.Stat(path)
	if err != nil || time.Since(st.ModTime()) > 4*time.Second {
		return sn, false
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &sn) != nil || sn.N < 20 || len(sn.Slots) < 20 {
		return sn, false
	}
	return sn, true
}

func paramFloats(raw json.RawMessage) []float64 {
	var bits []uint32
	if len(raw) == 0 || json.Unmarshal(raw, &bits) != nil {
		return nil
	}
	out := make([]float64, len(bits))
	for i, b := range bits {
		out[i] = float64(math.Float32frombits(b))
	}
	return out
}

func sameParams(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 0.002 {
			return false
		}
	}
	return true
}

// liveDiff: where the skater in the game differs from the preset's skater group. Slots whose item the game
// hasn't got (it shows a default there) are left out, as are slots the preset doesn't have.
func liveDiff(recipesJSON string, now skaterNow, missing func(string) bool) []liveChange {
	groups, err := parseRecipes(recipesJSON)
	if err != nil || len(groups) == 0 {
		return nil
	}
	exp := map[uint64]recipeItem{}
	for _, it := range groups[0].Items {
		exp[it.Slot] = it
	}
	var out []liveChange
	for name, item := range now.Slots {
		slot := uint64(itemHash(name))
		e, ok := exp[slot]
		if !ok || (e.Asset != "" && missing != nil && missing(e.Asset)) {
			continue
		}
		ch := liveChange{Slot: slot, SlotName: name, Was: e.Asset, Now: item}
		switch {
		case !strings.EqualFold(e.Asset, item):
			if lp := now.Params[name]; len(lp) > 0 {
				ch.Params = lp
			}
		case item != "":
			ep, lp := paramFloats(e.ParameterBit), now.Params[name]
			if len(ep) == 0 || len(lp) == 0 || sameParams(ep, lp) {
				continue
			}
			ch.Params = lp
		default:
			continue
		}
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SlotName < out[j].SlotName })
	return out
}

// applyLive puts the live items into the skater group of a recipes JSON, changing nothing else.
func applyLive(recipesJSON string, changes []liveChange) string {
	var groups []map[string]json.RawMessage
	if json.Unmarshal([]byte(recipesJSON), &groups) != nil || len(groups) == 0 {
		return recipesJSON
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(groups[0]["items"], &items) != nil {
		return recipesJSON
	}
	by := map[uint64]liveChange{}
	for _, c := range changes {
		by[c.Slot] = c
	}
	for _, it := range items {
		var slot uint64
		json.Unmarshal(it["slot"], &slot)
		c, ok := by[slot]
		if !ok {
			continue
		}
		it["asset"], _ = json.Marshal(c.Now)
		if len(c.Params) > 0 {
			bits := make([]uint32, len(c.Params))
			for i, f := range c.Params {
				bits[i] = math.Float32bits(float32(f))
			}
			it["parameter_bits"], _ = json.Marshal(bits)
		} else if c.Now == "" {
			it["parameter_bits"] = json.RawMessage("[]")
		}
	}
	groups[0]["items"], _ = json.Marshal(items)
	out, err := json.Marshal(groups)
	if err != nil {
		return recipesJSON
	}
	return string(out)
}

// ---- watching ----------------------------------------------------------------------------------------

type liveSeen struct {
	PresetID string
	Changes  []liveChange
}

var (
	liveSeenMu sync.Mutex
	liveShown  *liveSeen // changes that have held for a few seconds: what the Lineup shows
)

func shownChanges() *liveSeen {
	liveSeenMu.Lock()
	defer liveSeenMu.Unlock()
	return liveShown
}

// withLive: the presets as the game really has them, the selected one with what the editor changed on top.
func (a *App) withLive(presets []StoredPreset) []StoredPreset {
	ls := shownChanges()
	if ls == nil {
		return presets
	}
	out := append([]StoredPreset(nil), presets...)
	for i, p := range out {
		if p.PresetID == ls.PresetID {
			out[i].RecipesJSON = applyLive(p.RecipesJSON, ls.Changes)
		}
	}
	return out
}

func friendlySlot(name string) string {
	known := map[string]string{"cust_skintypes": "Skin tone", "cust_heads": "Head", "cust_bodies": "Body", "cust_eyeColor": "Eye color",
		"cust_scalpHair": "Hair", "cust_hairColor": "Hair color", "cust_facialHair": "Facial hair", "cust_eyebrowColor": "Eyebrow color",
		"cust_voicetypes": "Voice", "cust_lipstyle": "Lip style", "cust_makeup": "Makeup", "locomotion": "Walk style"}
	if n, ok := known[name]; ok {
		return n
	}
	n := strings.TrimPrefix(name, "cust_")
	n = strings.NewReplacer("_", " ").Replace(n)
	if n == "" {
		return name
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

func prettyItem(item string) string {
	if item == "" {
		return "nothing"
	}
	n := strings.TrimPrefix(item, "Own_")
	n = strings.TrimPrefix(n, "own_")
	n = strings.TrimPrefix(n, "Cust")
	return strings.ReplaceAll(n, "_", " ")
}

func describeChanges(cs []liveChange) []string {
	var out []string
	for _, c := range cs {
		if len(c.Params) > 0 && strings.EqualFold(c.Was, c.Now) {
			out = append(out, friendlySlot(c.SlotName)+": adjusted")
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s → %s", friendlySlot(c.SlotName), prettyItem(c.Was), prettyItem(c.Now)))
	}
	return out
}

func changeSig(cs []liveChange) string {
	b, _ := json.Marshal(cs)
	return string(b)
}

// editsOf: what the game's skater has that preset id doesn't, when that can be told apart from the game simply
// showing another skater (browsing the presets): too many differences, or a match with another preset, is that.
func editsOf(presets []StoredPreset, id string, now skaterNow) (diffs []liveChange, ok bool) {
	var chosen *StoredPreset
	for i := range presets {
		if presets[i].PresetID == id {
			chosen = &presets[i]
		}
	}
	if chosen == nil {
		return nil, false
	}
	diffs = liveDiff(chosen.RecipesJSON, now, itemWasMissing)
	if len(diffs) > 20 {
		return nil, false
	}
	if len(diffs) > 0 {
		for _, p := range presets {
			if p.PresetID != id && len(liveDiff(p.RecipesJSON, now, itemWasMissing)) == 0 {
				return nil, false
			}
		}
	}
	return diffs, true
}

func slotNumber(id string) int {
	for i, p := range presetIDs {
		if p == id {
			return i + 1
		}
	}
	return 0
}

func (a *App) setVerified(id string) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	m := a.readPendingLocked()
	if p, ok := m[id]; ok && !p.Verified {
		p.Verified = true
		m[id] = p
		a.savePendingLocked(m)
	}
}

// Looks at the game every couple of seconds. A difference counts once it has held for 3 s, so the moments while
// the game switches presets or redraws the skater don't show up.
func (a *App) watchSkaterNow() {
	var sig string
	var since time.Time
	var waitSig string // a loaded fit that isn't showing: what differs, and since when
	var waitSince time.Time
	warned := map[string]bool{}
	for range time.Tick(2 * time.Second) {
		clear := func() {
			liveSeenMu.Lock()
			if liveShown != nil {
				tl.logf("APP: the skater in the game matches the preset again")
			}
			liveShown = nil
			liveSeenMu.Unlock()
			sig = ""
		}
		if !gameRunning() {
			clear()
			waitSig = ""
			continue
		}
		now, ok := a.readSkaterNow()
		if !ok {
			clear()
			continue
		}
		if settleSince() < 8*time.Second {
			continue // a load or a refresh is going on: the game is changing things itself
		}
		cur, sel, err := readPresets(a.profileDB(), a.dir("tmp"))
		if err != nil || sel < 0 || sel >= len(presetIDs) {
			continue
		}
		id := presetIDs[sel]
		diffs, ok := editsOf(a.withPending(cur), id, now)
		if !ok {
			clear() // another skater is on show, or the preset isn't there
			continue
		}
		if p, loaded := a.readPending()[id]; loaded && !p.Verified {
			// loaded live and not seen in the game yet: a difference is the load not showing, not an edit
			clear()
			if len(diffs) == 0 {
				a.setVerified(id)
				waitSig = ""
				tl.logf("APP: checked: the game's skater wears what was loaded into %s", id)
				a.note("ok", "Checked in the game: slot %d shows the fit you loaded.", slotNumber(id))
				continue
			}
			refreshMu.Lock()
			armed := refreshWant != nil
			refreshMu.Unlock()
			ms, mok := a.readMenuState()
			if armed || !(mok && ms.Known && !ms.Open) {
				waitSig = "" // the refresh hasn't happened yet, or a menu is open
				continue
			}
			if s := id + changeSig(diffs); s != waitSig {
				waitSig, waitSince = s, time.Now()
			} else if time.Since(waitSince) > 20*time.Second && !warned[s] {
				warned[s] = true
				var what []string
				for i, c := range diffs {
					if i == 3 {
						what = append(what, fmt.Sprintf("and %d more", len(diffs)-3))
						break
					}
					what = append(what, fmt.Sprintf("%s (the game has %s)", friendlySlot(c.SlotName), prettyItem(c.Now)))
				}
				tl.logf("APP: checked: the game's skater doesn't wear what was loaded into %s: %s", id, strings.Join(describeChanges(diffs), "; "))
				a.note("warn", "Slot %d in the game doesn't show the fit you loaded. Different: %s.", slotNumber(id), strings.Join(what, ", "))
			}
			continue
		}
		waitSig = ""
		if len(diffs) == 0 {
			clear()
			continue
		}
		s := id + changeSig(diffs)
		if s != sig {
			sig, since = s, time.Now()
			liveSeenMu.Lock()
			liveShown = nil
			liveSeenMu.Unlock()
			continue
		}
		if time.Since(since) < 3*time.Second {
			continue
		}
		liveSeenMu.Lock()
		first := liveShown == nil
		liveShown = &liveSeen{PresetID: id, Changes: diffs}
		liveSeenMu.Unlock()
		if first {
			tl.logf("APP: the game's skater differs from %s: %s", id, strings.Join(describeChanges(diffs), "; "))
			a.keepLiveEdits(id, diffs)
		}
	}
}

// A preset that was loaded live is written into the game's file when the game closes. The game won't save the
// edits made to it afterwards, so they go into what is waiting to be written, or closing the game would undo them.
func (a *App) keepLiveEdits(id string, diffs []liveChange) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	m := a.readPendingLocked()
	p, ok := m[id]
	if !ok {
		return
	}
	p.RecipesJSON = applyLive(p.RecipesJSON, diffs)
	if p.Mem != "" {
		p.Mem = applyLive(p.Mem, diffs)
	}
	m[id] = p
	a.savePendingLocked(m)
	tl.logf("APP: the edits made in the game are kept for when it closes (%s)", id)
}

// snapOutfit: which preset a snap is of, going by what the skater wore in it, and that preset's recipes with the
// snap's skater on top. The preset with the fewest differences wins; among equals, the one the board pointed at
// (guess), then the selected one.
func snapOutfit(current []StoredPreset, selected int, guess []int, worn skaterNow) (slot int, recipes string, ok bool) {
	if len(worn.Slots) < 20 {
		return 0, "", false
	}
	bestN := -1
	var bestDiffs []liveChange
	rank := func(sl int) int { // lower is preferred
		for i, g := range guess {
			if g == sl {
				return i
			}
		}
		if sl == selected+1 {
			return len(guess)
		}
		return len(guess) + 1 + sl
	}
	for _, p := range current {
		sl := slotNumber(p.PresetID)
		if sl == 0 {
			continue
		}
		d := liveDiff(p.RecipesJSON, worn, itemWasMissing)
		if len(d) > 20 {
			continue
		}
		if bestN < 0 || len(d) < bestN || (len(d) == bestN && rank(sl) < rank(slot)) {
			bestN, bestDiffs, slot, recipes = len(d), d, sl, p.RecipesJSON
		}
	}
	if bestN < 0 {
		return 0, "", false
	}
	return slot, applyLive(recipes, bestDiffs), true
}

// coverExpect: what the selected preset's skater should wear once a refresh is done, as an addition to the
// plugin's "release the cover" message. The plugin then lifts the cover when the skater wears it (or after "late"
// ms), not on a fixed delay. Empty when it can't be told.
func (a *App) coverExpect() string {
	none := func(why string) string {
		tl.logf("REFRESH: nothing for the plugin to wait for: %s", why)
		return ""
	}
	sk, ok := a.readSkaterNow()
	if !ok {
		return none("the plugin hasn't reported the skater in the last 4 s")
	}
	cur, sel, err := readPresets(a.profileDB(), a.dir("tmp"))
	if err != nil || sel < 0 || sel >= len(presetIDs) {
		return none(fmt.Sprintf("couldn't read the selected preset (%v)", err))
	}
	for _, p := range a.withPending(cur) {
		if p.PresetID != presetIDs[sel] {
			continue
		}
		groups, err := parseRecipes(p.RecipesJSON)
		if err != nil || len(groups) == 0 {
			return none("the preset's outfit couldn't be read")
		}
		by := map[uint64]string{}
		for _, it := range groups[0].Items {
			by[it.Slot] = it.Asset
		}
		expect := map[string]string{}
		for name := range sk.Slots {
			if asset, has := by[uint64(itemHash(name))]; has && !(asset != "" && itemWasMissing(asset)) {
				expect[name] = strings.ToLower(asset)
			}
		}
		if len(expect) < 10 {
			return none(fmt.Sprintf("only %d of the skater's slots are in the preset", len(expect)))
		}
		b, err := json.Marshal(expect)
		if err != nil {
			return none(err.Error())
		}
		return `,"expect":` + string(b) + `,"late":1500`
	}
	return none("the selected preset isn't in the game's file")
}
