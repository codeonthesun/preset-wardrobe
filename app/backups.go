package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Backups of the game's save file (reskate.sqlite3 and its -wal/-shm), one folder per write, in backups/<time>/
// with about.json saying what the write was for. The newest 15 are kept, plus the newest of each of the last
// 7 days. They are copies the app made; your own files are never deleted.

var backupNameRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}(_\d+)?$`)

type backupInfo struct {
	Name  string    `json:"name"`
	At    time.Time `json:"at"`
	Why   string    `json:"why"`
	Slots []string  `json:"slots,omitempty"`
}

func backupProfile(dbPath, backupDir, why string, ids []string) (string, error) {
	stamp := time.Now().Format("2006-01-02_15-04-05")
	dir := filepath.Join(backupDir, stamp)
	for i := 2; fileExists(dir); i++ { // two writes in the same second
		dir = filepath.Join(backupDir, stamp+"_"+itoa(i))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			if err := copyFile(dbPath+suffix, filepath.Join(dir, "reskate.sqlite3"+suffix)); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		}
	}
	info := backupInfo{Name: filepath.Base(dir), At: time.Now(), Why: why, Slots: ids}
	if b, err := json.MarshalIndent(info, "", "  "); err == nil {
		os.WriteFile(filepath.Join(dir, "about.json"), b, 0o644)
	}
	return dir, nil
}

// restoreProfile puts a backup back in place of the game's file. A -wal or -shm the backup doesn't have is
// removed, or the game would apply it on top. The game must be closed.
func restoreProfile(dir, dbPath string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := filepath.Join(dir, "reskate.sqlite3"+suffix)
		if fileExists(src) {
			tmp := dbPath + suffix + ".restoring"
			if err := copyFile(src, tmp); err != nil {
				return err
			}
			if err := os.Rename(tmp, dbPath+suffix); err != nil {
				return err
			}
		} else if suffix != "" {
			os.Remove(dbPath + suffix)
		}
	}
	return nil
}

func listBackups(backupDir string) []backupInfo {
	ents, _ := os.ReadDir(backupDir)
	var out []backupInfo
	for _, e := range ents {
		if !e.IsDir() || !backupNameRe.MatchString(e.Name()) || !fileExists(filepath.Join(backupDir, e.Name(), "reskate.sqlite3")) {
			continue
		}
		info := backupInfo{Name: e.Name(), Why: "an older write (before backups said why)"}
		if b, err := os.ReadFile(filepath.Join(backupDir, e.Name(), "about.json")); err == nil {
			json.Unmarshal(b, &info)
			info.Name = e.Name()
		}
		if info.At.IsZero() {
			if t, err := time.ParseInLocation("2006-01-02_15-04-05", e.Name()[:19], time.Local); err == nil {
				info.At = t
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// pruneBackups keeps the newest 15, and the newest of each of the last 7 days.
func pruneBackups(backupDir string) {
	all := listBackups(backupDir)
	keep := map[string]bool{}
	for i, b := range all {
		if i < 15 {
			keep[b.Name] = true
		}
	}
	days := map[string]bool{}
	for _, b := range all { // newest first
		day := b.Name[:10]
		if !days[day] && time.Since(b.At) < 7*24*time.Hour {
			days[day] = true
			keep[b.Name] = true
		}
	}
	for _, b := range all {
		if !keep[b.Name] {
			os.RemoveAll(filepath.Join(backupDir, b.Name))
		}
	}
}
