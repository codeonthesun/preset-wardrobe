package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// What the app needs to know about the game around a load: which menu is open (the plugin reports it in
// menu_state.json), when the last load was, the optional black cover, and the note shown in the game afterwards.

type menuState struct {
	Known   bool `json:"known"`
	Open    bool `json:"open"`
	Skaters bool `json:"skaters"`
	Grid    bool `json:"grid"`
}

func (a *App) readMenuState() (menuState, bool) {
	var ms menuState
	dir := a.pluginDataDir()
	if dir == "" {
		return ms, false
	}
	path := filepath.Join(dir, "menu_state.json")
	st, err := os.Stat(path)
	if err != nil || time.Since(st.ModTime()) > 4*time.Second {
		return ms, false // the plugin hasn't said lately
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &ms) != nil {
		return ms, false
	}
	return ms, true
}

var (
	lastLoadMu sync.Mutex
	lastLoadAt time.Time
)

func markLiveLoad() {
	lastLoadMu.Lock()
	lastLoadAt = time.Now()
	lastLoadMu.Unlock()
}

// settleSince is how long it has been since the last live load (the game changes the skater for a moment after it).
func settleSince() time.Duration {
	lastLoadMu.Lock()
	defer lastLoadMu.Unlock()
	if lastLoadAt.IsZero() {
		return time.Hour
	}
	return time.Since(lastLoadAt)
}

// Brings the game's sound back, and checks it by reading it back, trying a few times: a game left silent
// is worse than a refresh without the muting.
func restoreGameSound() {
	for attempt := 1; attempt <= 6; attempt++ {
		n, wrong, err := muteGame(false)
		if err == nil && wrong == 0 {
			tl.logf("APP: game sound is back (%d sessions, attempt %d)", n, attempt)
			return
		}
		tl.logf("APP: game sound not back yet (%d sessions, %d still muted, attempt %d, error: %v)", n, wrong, attempt, err)
		time.Sleep(400 * time.Millisecond)
	}
}

func (a *App) logCoverDone(dir string, start time.Time) {
	defer recoverLog("cover log")
	if dir == "" {
		return
	}
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		b, err := os.ReadFile(filepath.Join(dir, "cover_done.json"))
		if err != nil {
			continue
		}
		var c struct {
			Waited  float64    `json:"waited"`
			Matched bool       `json:"matched"`
			Shown   float64    `json:"shown"`
			Diff    luaStrings `json:"diff"`
		}
		if json.Unmarshal(b, &c) == nil {
			tl.logf("REFRESH: plugin: cover lifted after waiting %.0f ms for the skater (%s); black for %.0f ms in all; seen here at +%d ms",
				c.Waited, map[bool]string{true: "the skater matched", false: "gave up waiting"}[c.Matched], c.Shown, time.Since(start).Milliseconds())
			for _, d := range c.Diff {
				tl.logf("REFRESH: plugin: still different when it gave up: %s", d)
			}
			return
		}
	}
	tl.logf("REFRESH: plugin: no word on the cover being lifted (older plugin?)")
}

// ---- the black cover around a load ------------------------------------------------------------------------------
// Through the data model a load puts the fit on the skater at once, so there is no replay: the plugin's black cover
// fades in before the fit is written, and lifts once the plugin sees the skater wearing it (coverExpect), or after
// its hold if it never does.

type loadCover struct {
	id    int64
	dir   string
	start time.Time
}

// coverLoad: the cover, when the preset worn now is among the ones loaded and the game is running.
func (a *App) coverLoad(writes []StoredPreset, sel int) *loadCover {
	if !modelLoadOn || sel < 0 || sel >= len(presetIDs) || !gameRunning() {
		return nil
	}
	if !a.cfg.LoadFade { // (the load's handler holds a.mu)
		return nil // the change is seen as it happens (Settings > Loading into the game turns the fade on)
	}
	worn := false
	for _, w := range writes {
		if w.PresetID == presetIDs[sel] {
			worn = true
		}
	}
	dir := a.pluginDataDir()
	if !worn || dir == "" {
		return nil
	}
	if ms, ok := a.readMenuState(); !ok || !ms.Known || (ms.Open && !(ms.Skaters && !ms.Grid && previewReady())) {
		// in a menu the skater shown there isn't the one changed (that's the one in the world), unless it's the
		// Skaters page with its preview parts known: no cover, a note
		tl.logf("COVER: none (a menu is open without the preview known, or the plugin hasn't said)")
		return nil
	}
	c := &loadCover{id: time.Now().UnixMilli(), dir: dir, start: time.Now()}
	os.Remove(filepath.Join(dir, "cover_done.json"))
	os.WriteFile(filepath.Join(dir, "cover.json"), []byte(fmt.Sprintf(`{"id":%d,"in":80,"hold":4000,"out":250}`, c.id)), 0o644)
	tl.logf("COVER: asked the plugin for the black cover")
	time.Sleep(130 * time.Millisecond) // the plugin notices within 50 ms, then the fade-in
	return c
}

// release lifts the cover: once the skater wears the fit (wait true), or at once.
func (c *loadCover) release(a *App, wait bool) {
	if c == nil {
		return
	}
	expect := ""
	if wait {
		expect = a.coverExpect()
	}
	os.WriteFile(filepath.Join(c.dir, "cover.json"), []byte(fmt.Sprintf(`{"id":%d,"release":true%s}`, c.id+1, expect)), 0o644)
	tl.logf("COVER: asked the plugin to lift the cover (+%d ms; waits for the skater: %v)", time.Since(c.start).Milliseconds(), expect != "")
	go a.logCoverDone(c.dir, c.start)
}

// liveMenuOpen: a menu is open at this load (the handler says, from the plugin's menu state); previewDone: the load
// updated the Skaters page's preview as well.
var liveMenuOpen, previewDone, previewShows atomic.Bool

// watchMenuParts finds the Skaters page's preview parts in the background while the page is open (they take seconds
// to find, and a load there needs them at once).
func (a *App) watchMenuParts() {
	for range time.Tick(time.Second) {
		if !modelLoadOn || !gameRunning() {
			continue
		}
		if ms, ok := a.readMenuState(); ok && ms.Known && ms.Open && ms.Skaters {
			warmPreviewParts()
		}
	}
}

// ---- the note in the game after a load --------------------------------------------------------------------------
// "Applied fit: …", shown in the game by the plugin, once the game is the window in front (a note shown while the
// app is in front would come and go unseen). A newer load replaces a waiting note; an old one is dropped.

var gameToast struct {
	sync.Mutex
	text string
	at   time.Time
}

func queueGameToast(text string) {
	gameToast.Lock()
	gameToast.text, gameToast.at = text, time.Now()
	gameToast.Unlock()
}

func (a *App) watchGameToast() {
	for range time.Tick(300 * time.Millisecond) {
		gameToast.Lock()
		text, at := gameToast.text, gameToast.at
		gameToast.Unlock()
		if text == "" {
			continue
		}
		if time.Since(at) > 10*time.Minute || !gameRunning() {
			queueGameToast("")
			continue
		}
		if !gameInFront() {
			continue
		}
		time.Sleep(400 * time.Millisecond) // the game has just come to the front: let it settle
		if !gameInFront() {
			continue
		}
		gameToast.Lock()
		if gameToast.at != at {
			gameToast.Unlock()
			continue // a newer load came meanwhile
		}
		gameToast.text = ""
		gameToast.Unlock()
		a.mu.Lock()
		a.ack(text, false)
		a.mu.Unlock()
		tl.logf("APP: in-game note: %s", text)
	}
}

// modelNotShown: after the last load, the parts of the skater the game didn't put the fit on ("" when it did).
var modelNotShown atomic.Value

func init() { modelNotShown.Store("") }

const toastApplied = "Fit applied successfully."
const toastFailed = "Fit couldn't be applied. Check Preset Wardrobe for details."
