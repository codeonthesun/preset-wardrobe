package main

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The activity log: what the app does and what happens to the game, as lines with a time, shown in the
// sidebar. Messages the window raises itself (the results of what you click) go into the log there; this is
// for the ones only the program sees, such as the game starting or closing while the window is shut. It is
// kept in memory, the last few hundred lines, and the window asks for what it hasn't seen.

type actEntry struct {
	ID    int    `json:"id"`
	T     int64  `json:"t"`     // unix milliseconds
	Level string `json:"level"` // info, ok, warn or bad
	Text  string `json:"text"`
}

type actLog struct {
	mu   sync.Mutex
	boot int64 // when this run started: the ids restart with it
	seq  int
	list []actEntry
}

const actMax = 300

func (l *actLog) add(level, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.boot == 0 {
		l.boot = time.Now().UnixMilli()
	}
	l.seq++
	l.list = append(l.list, actEntry{ID: l.seq, T: time.Now().UnixMilli(), Level: level, Text: text})
	if len(l.list) > actMax {
		l.list = append([]actEntry(nil), l.list[len(l.list)-actMax:]...)
	}
}

// since: the lines after the one numbered after, for a window that has seen this run up to there (any other
// run: everything).
func (l *actLog) since(boot int64, after int) (int64, []actEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if boot != l.boot {
		after = 0
	}
	out := []actEntry{}
	for _, e := range l.list {
		if e.ID > after {
			out = append(out, e)
		}
	}
	return l.boot, out
}

// note adds a line to the activity log.
func (a *App) note(level, format string, args ...any) {
	a.act.add(level, fmt.Sprintf(format, args...))
}

func (a *App) logHandler(w http.ResponseWriter, r *http.Request) {
	boot, _ := strconv.ParseInt(r.URL.Query().Get("boot"), 10, 64)
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	b, entries := a.act.since(boot, after)
	reply(w, map[string]any{"boot": b, "entries": entries})
}

// watchGame notes when skate. starts and closes.
func (a *App) watchGame() {
	running := gameRunning()
	if running {
		a.note("info", "skate. is running")
	} else {
		a.note("info", "skate. is not running")
	}
	for range time.Tick(2 * time.Second) {
		now := gameRunning()
		if now == running {
			continue
		}
		running = now
		if now {
			a.note("info", "skate. started")
		} else {
			a.note("info", "skate. closed")
		}
	}
}

// plural: "1 fit", "3 fits".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// logLine: a line a reply hands the window to put in the activity log.
type logLine struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}
