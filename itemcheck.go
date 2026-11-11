package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Which items the game has installed. When the game loads a saved outfit it puts the slot's default in place of
// an item it hasn't got (a removed or updated mod). Writing such an item's hash straight into the tables skips
// that: the board is left with a part it can't load and shows nothing in the world. And a table that holds the
// defaults can't be told from another preset's, so the live load couldn't find it. The plugin asks the game
// (reskate.outfit.find looks through its whole item catalog) and the app remembers the answers until the game
// restarts. With no answer, everything goes on as it did before.
var (
	itemMu      sync.Mutex
	itemBoot    float64
	itemMissing = map[string]bool{} // item name -> the game hasn't got it (false: it has)
	itemDir     func() string       // the plugin's data folder
	itemSeq     atomic.Int64
	itemPause   atomic.Int64 // unix ms before which no check is made: the plugin didn't answer last time
)

const (
	itemCheckWait  = 600 * time.Millisecond
	itemCheckChunk = 40
)

// What the game last said about each item, kept in a file so it's still known when the game is closed:
// item name -> the game hasn't got it. Only a real answer from the game goes in; an item never asked about isn't here.
var (
	itemKnown      = map[string]bool{}
	itemStatusPath string
)

func loadItemStatus(path string) {
	itemStatusPath = path
	var m map[string]bool
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &m) == nil {
		itemMu.Lock()
		itemKnown = m
		itemMu.Unlock()
	}
}

func saveItemStatus() {
	if itemStatusPath == "" {
		return
	}
	itemMu.Lock()
	b, err := json.Marshal(itemKnown)
	itemMu.Unlock()
	if err == nil {
		tmp := itemStatusPath + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			os.Rename(tmp, itemStatusPath)
		}
	}
}

// itemWasMissing: the game said it hasn't got this item (the last time it was asked).
func itemWasMissing(name string) bool {
	itemMu.Lock()
	defer itemMu.Unlock()
	return itemKnown[name]
}

func itemIsMissing(name string) bool {
	if name == "" {
		return false
	}
	itemMu.Lock()
	defer itemMu.Unlock()
	return itemMissing[name]
}

// Every item name in the presets that differ between old and next.
func recipeItemNames(old, next []StoredPreset) []string {
	byID := map[string]StoredPreset{}
	for _, p := range old {
		byID[p.PresetID] = p
	}
	seen := map[string]bool{}
	var out []string
	add := func(json string) {
		groups, err := parseRecipes(json)
		if err != nil {
			return
		}
		for _, g := range groups {
			for _, it := range g.Items {
				if it.Asset != "" && !seen[it.Asset] {
					seen[it.Asset] = true
					out = append(out, it.Asset)
				}
			}
		}
	}
	for _, q := range next {
		if p, ok := byID[q.PresetID]; ok {
			add(p.RecipesJSON)
			add(q.RecipesJSON)
		}
	}
	sort.Strings(out)
	return out
}

type itemAnswer struct {
	ID      float64 `json:"id"`
	OK      bool    `json:"ok"`
	Boot    float64 `json:"boot"`
	Missing string  `json:"missing"`
	Why     string  `json:"why"`
}

// Asks the plugin about the names not asked about yet and waits for the answer for a moment.
func checkItems(names []string) {
	if itemDir == nil {
		return
	}
	dir := itemDir()
	if dir == "" {
		return
	}
	if time.Now().UnixMilli() < itemPause.Load() {
		return
	}
	var todo []string
	itemMu.Lock()
	for _, n := range names {
		if _, known := itemMissing[n]; !known {
			todo = append(todo, n)
		}
	}
	itemMu.Unlock()
	if len(todo) == 0 {
		return
	}
	// the plugin writes menu_state.json every second or so while it runs in the game
	if st, err := os.Stat(filepath.Join(dir, "menu_state.json")); err != nil || time.Since(st.ModTime()) > 4*time.Second {
		return
	}
	start := time.Now()
	deadline := start.Add(itemCheckWait)
	asked, missing := 0, 0
	for len(todo) > 0 && time.Now().Before(deadline) {
		chunk := todo
		if len(chunk) > itemCheckChunk {
			chunk = chunk[:itemCheckChunk]
		}
		todo = todo[len(chunk):]
		id := float64(time.Now().UnixMilli())
		if last := float64(itemSeq.Load()); id <= last {
			id = last + 1
		}
		itemSeq.Store(int64(id))
		req, _ := json.Marshal(map[string]any{"id": id, "names": chunk})
		tmp := filepath.Join(dir, "item_check.json.tmp")
		if os.WriteFile(tmp, req, 0o644) != nil || os.Rename(tmp, filepath.Join(dir, "item_check.json")) != nil {
			return
		}
		var ans itemAnswer
		got := false
		for time.Now().Before(deadline) {
			b, err := os.ReadFile(filepath.Join(dir, "item_check_result.json"))
			if err == nil && json.Unmarshal(b, &ans) == nil && ans.ID == id {
				got = true
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if !got || !ans.OK {
			// no answer, or the game's catalog can't be read just now: nothing is assumed, and nothing is asked for a while
			itemPause.Store(time.Now().Add(20 * time.Second).UnixMilli())
			if !got {
				tl.logf("APP: item check: no answer from the plugin in %d ms (is plugin 0.2.9 installed?)", time.Since(start).Milliseconds())
			} else {
				tl.logf("APP: item check: the plugin couldn't check: %s", ans.Why)
			}
			return
		}
		gone := map[string]bool{}
		for _, n := range strings.Split(ans.Missing, "\n") {
			if n != "" {
				gone[n] = true
			}
		}
		itemMu.Lock()
		if ans.Boot != itemBoot { // the game was restarted: what was known is old
			itemBoot = ans.Boot
			itemMissing = map[string]bool{}
		}
		changed := false
		for _, n := range chunk {
			itemMissing[n] = gone[n]
			if old, had := itemKnown[n]; !had || old != gone[n] {
				itemKnown[n] = gone[n]
				changed = true
			}
		}
		itemMu.Unlock()
		if changed {
			saveItemStatus()
		}
		asked += len(chunk)
		missing += len(gone)
	}
	tl.logf("APP: item check: %d names asked, %d not installed, %d ms", asked, missing, time.Since(start).Milliseconds())
}

// The preset the game has selected (its id and its place in the game's list), set by a live load before it patches
// memory. The board is written through the game's own preset list, only for this preset.
var liveSel struct {
	sync.Mutex
	id  string
	idx int
}

func setLiveSelected(id string, idx int) {
	liveSel.Lock()
	liveSel.id, liveSel.idx = id, idx
	liveSel.Unlock()
}

func liveSelected() (string, int) {
	liveSel.Lock()
	defer liveSel.Unlock()
	return liveSel.id, liveSel.idx
}

// Keeps asking the running game about every item in the catalogue and the lineup, so the app can mark the ones
// the game hasn't got, and remembers the answers for when the game is closed.
func (a *App) watchItems() {
	for n := 0; ; n++ {
		time.Sleep(6 * time.Second)
		if !gameRunning() {
			continue
		}
		names := a.catalogueItemNames(n%4 == 0)
		for i := 0; i < 30; i++ {
			before := itemsUnasked(names)
			if before == 0 {
				break
			}
			checkItems(names)
			if itemsUnasked(names) >= before {
				break // no answer just now
			}
		}
	}
}

func itemsUnasked(names []string) int {
	itemMu.Lock()
	defer itemMu.Unlock()
	n := 0
	for _, x := range names {
		if _, known := itemMissing[x]; !known {
			n++
		}
	}
	return n
}

// Every item name of every fit in the catalogue, and of the game's presets too when withLineup is set.
func (a *App) catalogueItemNames(withLineup bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(recipes string) {
		for _, p := range piecesOf(recipes) {
			if p.Key != "" && !seen[p.Key] {
				seen[p.Key] = true
				out = append(out, p.Key)
			}
		}
	}
	for _, f := range a.listFits() {
		add(f.RecipesJSON)
	}
	if withLineup {
		if cur, _, err := readPresets(a.profileDB(), a.dir("tmp")); err == nil {
			for _, p := range a.withPending(cur) {
				add(p.RecipesJSON)
			}
		}
	}
	sort.Strings(out)
	return out
}
