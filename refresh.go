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

// After a live load the game wears the new clothes and board, but not a new face or hair until it
// redraws the whole skater, which switching to another preset and back does. So when the preset in use
// was loaded, the app waits for the Skaters page to be open (the plugin reports that in
// menu_state.json), then presses the shoulder buttons for the person: next preset and back.

type refreshArm struct {
	at  time.Time
	ids map[string]bool
}

var (
	refreshMu    sync.Mutex
	refreshWant  *refreshArm
	liveSweeping atomic.Bool // a second look through the game's memory is running
)

func (a *App) armRefresh(ids []string) {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	refreshMu.Lock()
	refreshWant = &refreshArm{at: time.Now(), ids: m}
	refreshMu.Unlock()
}

func (a *App) disarmRefresh() {
	refreshMu.Lock()
	refreshWant = nil
	refreshMu.Unlock()
}

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

// Opening the replay (B) and closing it again (Esc, then Space to confirm) makes the game rebuild every
// skater in the world from what it holds now, face and hair too. It's done in the world with no menu open.
func (a *App) replayRefresh() (float64, error) {
	start := time.Now()
	last := start
	// every step's time goes to timeline.log ("REFRESH: +total (step) what"), to see where a refresh's time goes
	step := func(what string) {
		n := time.Now()
		tl.logf("REFRESH: +%4d ms (%4d ms) %s", n.Sub(start).Milliseconds(), n.Sub(last).Milliseconds(), what)
		last = n
	}
	dir := a.pluginDataDir()
	cover := func(body string) {
		if dir != "" {
			os.WriteFile(filepath.Join(dir, "cover.json"), []byte(body), 0o644)
		}
	}
	id := time.Now().UnixMilli()
	os.Remove(filepath.Join(dir, "cover_done.json"))
	// a black cover over the game (drawn by the plugin) that stays until the skater is back; the game's own
	// menu sounds are muted meanwhile
	n, wrong, merr := muteGame(true)
	step(fmt.Sprintf("muted the game's sound (%d sessions, %d not as asked, error: %v)", n, wrong, merr))
	defer restoreGameSound()
	cover(fmt.Sprintf(`{"id":%d,"in":80,"hold":8000,"out":250}`, id))
	step("asked the plugin for the black cover")
	if dir != "" {
		time.Sleep(120 * time.Millisecond) // the plugin notices within 50 ms, then the fade-in
		step("waited for the cover to fade in (fixed 120 ms)")
	}
	err := pressKey("b")
	step(fmt.Sprintf("pressed B to open the replay (error: %v)", err))
	if err == nil {
		time.Sleep(70 * time.Millisecond)
		step("waited after B (fixed 70 ms)")
		err = pressKey("esc")
		step(fmt.Sprintf("pressed Esc to close it (error: %v)", err))
	}
	if err == nil {
		time.Sleep(70 * time.Millisecond) // the close prompt
		step("waited for the close prompt (fixed 70 ms)")
		err = pressKey("space")
		step(fmt.Sprintf("pressed Space to confirm (error: %v)", err))
	}
	spaceAt := time.Now()
	if err == nil {
		// the board is written now, while the replay closes behind the black cover, so it is ready when the screen
		// comes back (if this is missed, the second look after the refresh writes it)
		time.Sleep(100 * time.Millisecond)
		step("waited before the board (fixed 100 ms)")
		boardEarly()
		step("board written")
	}
	// leaving the replay makes the game rebuild the skater behind its own black screen (about 0.7 s in the
	// recordings); the game logs nothing at that point, so this is a fixed wait
	if left := replayExit - time.Since(spaceAt); left > 0 {
		time.Sleep(left)
		step(fmt.Sprintf("waited for the game to rebuild the skater (fixed %d ms after Space, %d ms of it here)", replayExit.Milliseconds(), left.Milliseconds()))
	}
	expect := a.coverExpect()
	step(fmt.Sprintf("worked out what the skater should wear (waits for the skater: %v)", expect != ""))
	cover(fmt.Sprintf(`{"id":%d,"release":true%s}`, id+1, expect))
	step("asked the plugin to lift the cover")
	time.Sleep(250 * time.Millisecond)
	step("waited for the cover's fade-out (fixed 250 ms)")
	go a.logCoverDone(dir, start)
	return time.Since(start).Seconds(), err
}

// The plugin says when it really lifted the cover (cover_done.json): how long it waited for the skater, and why it
// stopped waiting.
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

const replayExit = 700 * time.Millisecond

// Test: the game rewrites many of its outfit tables for several seconds after a replay. Nothing is scanned or
// written until settleAfterReplay has passed since the last replay ended.
const settleAfterReplay = 0 * time.Second

var (
	replayEndMu sync.Mutex
	replayEnd   time.Time
)

var lastLoadAt time.Time // the last live load (guarded by replayEndMu)

func markLiveLoad() {
	replayEndMu.Lock()
	lastLoadAt = time.Now()
	replayEndMu.Unlock()
}

// settleSince is how long it has been since the game last changed things by itself: a live load or a replay.
func settleSince() time.Duration {
	replayEndMu.Lock()
	defer replayEndMu.Unlock()
	d := time.Hour
	for _, t := range []time.Time{replayEnd, lastLoadAt} {
		if !t.IsZero() && time.Since(t) < d {
			d = time.Since(t)
		}
	}
	return d
}

func markReplayEnd() {
	replayEndMu.Lock()
	replayEnd = time.Now()
	replayEndMu.Unlock()
}

// settleLeft is how much longer to wait before the game's tables are quiet again.
func settleLeft() time.Duration {
	replayEndMu.Lock()
	defer replayEndMu.Unlock()
	if replayEnd.IsZero() {
		return 0
	}
	if d := settleAfterReplay - time.Since(replayEnd); d > 0 {
		return d
	}
	return 0
}

// After a live load the skater is redrawn by opening and closing the replay. It happens by itself the next
// time the game is in front and you're in the world with no menu open, and it's the only refresh there is.
func (a *App) watchRefresh() {
	var ready time.Time // since when the game has been in the world and in front
	var why string      // diagnostic: what the refresh is waiting for, written to timeline.log only
	note := func(r string, want *refreshArm) {
		if r != why {
			why = r
			tl.logf("APP: refresh wait (+%.1f s after the load): %s", time.Since(want.at).Seconds(), r)
		}
	}
	for range time.Tick(100 * time.Millisecond) {
		refreshMu.Lock()
		want := refreshWant
		refreshMu.Unlock()
		if want == nil {
			ready = time.Time{}
			a.runSweep(1500 * time.Millisecond) // nothing to refresh: just the second look
			continue
		}
		if time.Since(want.at) > 30*time.Minute || !gameRunning() {
			a.disarmRefresh()
			continue
		}
		if _, err := os.Stat(a.dir("hold_refresh")); err == nil {
			ready = time.Time{} // diagnostic: a file named hold_refresh stops the automatic refresh
			continue
		}
		ms, ok := a.readMenuState()
		if !(ok && ms.Known && !ms.Open) || !gameInFront() {
			ready = time.Time{}
			continue
		}
		if ready.IsZero() {
			ready = time.Now()
		}
		if time.Since(ready) < 300*time.Millisecond {
			note("in the world and in front, settling 0.3 s", want)
			continue // let it settle first
		}
		ready = time.Time{}
		a.disarmRefresh()
		secs, err := a.replayRefresh()
		markReplayEnd()
		tl.logf("APP: refreshed the skater by opening and closing the replay (%.1f s, error: %v)", secs, err)
		a.runSweep(0)
	}
}

// The second quick look through the game's memory that follows a live load.
type sweepJob struct {
	old, next []StoredPreset
	body      bool
	at        time.Time
}

var (
	sweepMu   sync.Mutex
	sweepWant *sweepJob
)

func armSweep(old, next []StoredPreset, body bool) {
	sweepMu.Lock()
	sweepWant = &sweepJob{old, next, body, time.Now()}
	sweepMu.Unlock()
}

// runSweep does the pending second look, once at least wait has passed since the load.
func (a *App) runSweep(wait time.Duration) {
	if settleLeft() > 0 {
		return // the game is still rewriting its tables after the replay
	}
	sweepMu.Lock()
	j := sweepWant
	if j != nil && time.Since(j.at) < wait {
		sweepMu.Unlock()
		return
	}
	sweepWant = nil
	sweepMu.Unlock()
	if j == nil || !gameRunning() {
		return
	}
	patchLiveMemory(j.old, j.next, j.body, true)
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

// Looks for the game's outfit tables in the background as soon as they exist (at the hub menu, as the game starts),
// so the first load needn't make the long search itself. Nothing is changed in the game. A load takes over from it,
// and it is off unless a file named prescan is in the app's data folder (it can make a fit show in the world early).
func (a *App) watchWarm() {
	var pid uint32
	var tries int
	var found, worldDone bool
	var next time.Time
	for range time.Tick(3 * time.Second) {
		p := findSkatePID()
		if p == 0 {
			pid = 0
			continue
		}
		if p != pid {
			pid, tries, found, worldDone = p, 0, false, false
			next = time.Now().Add(10 * time.Second)
		}
		if time.Now().Before(next) {
			continue
		}
		if _, err := os.Stat(a.dir("prescan")); err != nil {
			continue // off unless a file named prescan is in the app's data folder
		}
		ms, ok := a.readMenuState()
		inWorld := ok && ms.Known && !ms.Open
		switch {
		case !found && tries < 6:
			tries++
		case found && inWorld && !worldDone:
			worldDone = true // once more when the skater is in the world: the hub's own copies are gone by then
		default:
			continue
		}
		cur, sel, err := readPresets(a.profileDB(), a.dir("tmp"))
		if err != nil || sel < 0 || sel >= len(presetIDs) {
			next = time.Now().Add(20 * time.Second)
			continue
		}
		var one []StoredPreset
		for _, q := range a.withPendingMem(cur) {
			if q.PresetID == presetIDs[sel] {
				one = append(one, q)
			}
		}
		t0 := time.Now()
		ok2, err := warmLiveMemory(one)
		tl.logf("APP: pre-scan %d took %.1f s: found %v (error: %v)", tries, time.Since(t0).Seconds(), ok2, err)
		if ok2 {
			found = true
		}
		next = time.Now().Add(20 * time.Second)
	}
}
