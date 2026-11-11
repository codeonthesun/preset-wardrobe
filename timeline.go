package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A timeline of what happens to the game's presets, for finding out when the game reads and writes
// them (timeline.log in the app's data folder). Every change to the profile database is logged with
// the revision counter, which presets changed and how, and whether the game was running; so are the
// game's own profile lines (ReSkate.log) and the app's own writes. It only reads.

type timeline struct {
	mu   sync.Mutex
	path string
}

var tl = &timeline{}

// How many times the game has rebuilt the skater (its "Skater component factory" lines), for waiting on it.
var skaterBuilds atomic.Int64

func (t *timeline) logf(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.path == "" {
		return
	}
	if st, err := os.Stat(t.path); err == nil && st.Size() > 1<<20 { // keep it small: drop the older half
		if b, err := os.ReadFile(t.path); err == nil {
			os.WriteFile(t.path, b[len(b)/2:], 0o644)
		}
	}
	f, err := os.OpenFile(t.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

type tlSnapshot struct {
	revision string
	presets  map[string]string // preset id -> recipes_json
}

func readTimelineSnapshot(dbPath, tmpDir string) (tlSnapshot, error) {
	snap, err := os.MkdirTemp(tmpDir, "tl-")
	if err != nil {
		return tlSnapshot{}, err
	}
	defer os.RemoveAll(snap)
	cp := filepath.Join(snap, "reskate.sqlite3")
	if err := copyFile(dbPath, cp); err != nil {
		return tlSnapshot{}, err
	}
	if _, err := os.Stat(dbPath + "-wal"); err == nil {
		copyFile(dbPath+"-wal", cp+"-wal")
	}
	db, err := sql.Open("sqlite3", cp)
	if err != nil {
		return tlSnapshot{}, err
	}
	defer db.Close()
	s := tlSnapshot{presets: map[string]string{}}
	if err := db.QueryRow(`SELECT revision FROM profile_meta WHERE id=1`).Scan(&s.revision); err != nil {
		return tlSnapshot{}, err
	}
	rows, err := db.Query(`SELECT preset_id, recipes_json FROM cosmetic_loadouts`)
	if err != nil {
		return tlSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, rj string
		if rows.Scan(&id, &rj) == nil {
			s.presets[id] = rj
		}
	}
	return s, nil
}

func assetsBySlot(recipesJSON string) map[string]string {
	out := map[string]string{}
	groups, err := parseRecipes(recipesJSON)
	if err != nil {
		return out
	}
	for gi, g := range groups {
		for _, it := range g.Items {
			out[fmt.Sprintf("r%d/slot %d", gi, it.Slot)] = it.Asset
		}
	}
	return out
}

func describeChange(oldJSON, newJSON string) string {
	a, b := assetsBySlot(oldJSON), assetsBySlot(newJSON)
	var parts []string
	for k, nv := range b {
		if ov, ok := a[k]; !ok || ov != nv {
			parts = append(parts, fmt.Sprintf("%s %q -> %q", k, a[k], nv))
		}
	}
	if len(parts) == 0 && oldJSON != newJSON {
		return "(text differs, items the same)"
	}
	if len(parts) > 6 {
		return fmt.Sprintf("%d slots differ, e.g. %s", len(parts), strings.Join(parts[:3], "; "))
	}
	return strings.Join(parts, "; ")
}

// What the game logs when it touches the profile or rebuilds the skater: the lines to line up with
// what the player did.
var gameProfileLine = regexp.MustCompile(`\[Profile\]|Profile options|Local cosmetic inventory published|Skater component factory|Spectate camera|Plugin animation: listening|Plugin board: listening`)

func (a *App) watchTimeline() {
	a.mu.Lock()
	tl.path = filepath.Join(a.dataDir, "timeline.log")
	a.mu.Unlock()
	tl.logf("---- timeline started (app %s) ----", appVersion)
	var lastSig, lastShm string
	var lastShmLog time.Time
	var last tlSnapshot
	var haveLast, wasRunning bool
	var logPath string
	var logOff int64 = -1
	for range time.Tick(500 * time.Millisecond) {
		a.mu.Lock()
		db, gameDir := a.profileDB(), a.cfg.GameDir
		a.mu.Unlock()
		running := gameRunning()
		if running != wasRunning {
			tl.logf("game %s", map[bool]string{true: "STARTED", false: "CLOSED"}[running])
			wasRunning = running
		}

		// The profile database: log it whenever its files change.
		if db != "" {
			sig := ""
			for _, suffix := range []string{"", "-wal"} {
				if st, err := os.Stat(db + suffix); err == nil {
					sig += fmt.Sprintf("%d/%d;", st.ModTime().UnixNano(), st.Size())
				}
			}
			if sig != lastSig {
				lastSig = sig
				if snap, err := readTimelineSnapshot(db, a.dir("tmp")); err == nil {
					if !haveLast {
						tl.logf("database first seen: revision %s, %d presets (game running: %v)", snap.revision, len(snap.presets), running)
					} else {
						if snap.revision != last.revision {
							tl.logf("database revision %s -> %s (game running: %v)", last.revision, snap.revision, running)
						}
						for _, id := range presetIDs {
							if snap.presets[id] != last.presets[id] {
								tl.logf("  %s changed: %s", id, describeChange(last.presets[id], snap.presets[id]))
							}
						}
					}
					last, haveLast = snap, true
				}
			}
		}

		// Anything opening the database touches its -shm file: log that, at most once a second.
		if db != "" {
			if st, err := os.Stat(db + "-shm"); err == nil {
				sig := fmt.Sprintf("%d/%d", st.ModTime().UnixNano(), st.Size())
				if sig != lastShm {
					if lastShm != "" && time.Since(lastShmLog) > time.Second {
						tl.logf("database -shm touched (something opened or read it; game running: %v)", running)
						lastShmLog = time.Now()
					}
					lastShm = sig
				}
			}
		}

		// The game's own profile lines.
		if gameDir != "" {
			p := filepath.Join(gameDir, "logs", "ReSkate.log")
			if p != logPath {
				logPath, logOff = p, -1
			}
			if st, err := os.Stat(p); err == nil {
				if logOff < 0 {
					logOff = st.Size() // the first look: only what comes after
				} else if st.Size() < logOff {
					logOff = 0 // a fresh game log: from its top
				}
				if st.Size() > logOff {
					if f, err := os.Open(p); err == nil {
						buf := make([]byte, st.Size()-logOff)
						n, _ := f.ReadAt(buf, logOff)
						f.Close()
						logOff += int64(n)
						for _, line := range strings.Split(string(buf[:n]), "\n") {
							if strings.Contains(line, "Skater component factory") {
								skaterBuilds.Add(1)
							}
							if gameProfileLine.MatchString(line) || (strings.Contains(line, "[Customization]") && !strings.Contains(line, "Item browser")) {
								if len(line) > 220 {
									line = line[:220]
								}
								tl.logf("game: %s", strings.TrimSpace(line))
							}
						}
					}
				}
			}
		}
	}
}
