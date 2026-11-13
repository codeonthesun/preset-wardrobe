package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gen2brain/webp"
)

// HQ pictures: a 2x enhanced copy of every picture of every fit (spin frames, board views, the card picture),
// kept on disk as WebP (quality 90, about a tenth of a PNG's size) in hq/<hash>/. They are made in the
// background, one picture at a time, on one thread at the lowest priority, slower still while the game runs,
// so playing isn't affected. Until a fit's set is complete the app shows its original PNGs, which are never
// touched; then it shows the HQ ones. hq/ holds nothing that can't be made again from the originals.

const (
	hqScale   = 2
	hqQuality = 90
	hqMark    = ".done" // holds the signature of the originals it was made from
)

// hqSig: what the fit's pictures are now (a re-capture changes the folders' times); an HQ set made from other
// pictures is out of date.
func (a *App) hqSig(hash string) string {
	var parts []string
	for _, p := range []string{a.dir("pictures", hash), a.dir("pictures", hash, "board"), a.dir("pictures", hash, "board", litDir), a.dir("pictures", hash+".png")} {
		if st, err := os.Stat(p); err == nil {
			parts = append(parts, fmt.Sprint(st.ModTime().UnixNano()))
		} else {
			parts = append(parts, "-")
		}
	}
	return strings.Join(parts, ",")
}

func (a *App) hqDone(hash string) bool {
	if a.cfg.PlainBoards { // "Clean up and improve clarity of pictures" is off: the originals only
		return false
	}
	b, err := os.ReadFile(a.dir("hq", hash, hqMark))
	return err == nil && string(b) == a.hqSig(hash)
}

// hqState for the cards: "done", "working" (pictures there, HQ not made yet) or "" (no pictures).
func (a *App) hqState(hash string) string {
	if a.cfg.PlainBoards {
		return ""
	}
	if a.hqDone(hash) {
		return "done"
	}
	if fileExists(a.dir("pictures", hash)) || fileExists(a.dir("pictures", hash+".png")) {
		return "working"
	}
	return ""
}

// toHQ turns a picture's URL into its HQ one, once the fit's HQ set is complete.
func (a *App) toHQ(hash string, urls []string) []string {
	if !a.hqDone(hash) {
		return urls
	}
	out := make([]string, len(urls))
	for i, u := range urls {
		out[i] = strings.Replace(strings.Replace(u, "/pic/", "/hq/", 1), ".png?", ".webp?", 1)
	}
	return out
}

var hqPathRe = regexp.MustCompile(`^[0-9a-f]{16}(\.webp|/\d{1,3}\.webp|/board/\d{1,2}\.webp|/board/` + regexp.QuoteMeta(litDir) + `/\d{1,2}\.webp)$`)

// hqFile: the file behind an /hq/ address, if it is one.
func (a *App) hqFile(rel string) (string, bool) {
	if !hqPathRe.MatchString(rel) {
		return "", false
	}
	return a.dir("hq", filepath.FromSlash(rel)), true
}

// hqJobs: every picture of the fit, as (original, HQ copy).
func (a *App) hqJobs(hash string) [][2]string {
	var jobs [][2]string
	add := func(srcDir, dstDir string) {
		ents, _ := os.ReadDir(srcDir)
		for _, e := range ents {
			var n int
			if _, err := fmt.Sscanf(e.Name(), "%d.png", &n); err == nil && !e.IsDir() {
				jobs = append(jobs, [2]string{filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, fmt.Sprintf("%d.webp", n))})
			}
		}
	}
	add(a.dir("pictures", hash), a.dir("hq", hash))
	add(a.dir("pictures", hash, "board"), a.dir("hq", hash, "board"))
	add(a.dir("pictures", hash, "board", litDir), a.dir("hq", hash, "board", litDir))
	if fileExists(a.dir("pictures", hash+".png")) {
		jobs = append(jobs, [2]string{a.dir("pictures", hash+".png"), a.dir("hq", hash+".webp")})
	}
	return jobs
}

func hqMake(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	up := upscaleW(img, hqScale, 1)
	var b bytes.Buffer
	if err := webp.Encode(&b, up, webp.Options{Quality: hqQuality, Method: 4}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// hqPause: the rest between two pictures. Longer while the game runs; long when it is in front.
func hqPause() time.Duration {
	switch {
	case !gameRunning():
		return 150 * time.Millisecond
	case gameInFront():
		return 2 * time.Second
	default:
		return 700 * time.Millisecond
	}
}

func (a *App) watchHQ() {
	lowPriorityThread()          // this goroutine stays on one thread, at the lowest priority
	time.Sleep(15 * time.Second) // let the app start up first
	for {
		a.hqPass()
		time.Sleep(20 * time.Second)
	}
}

// hqPass makes what's missing, one fit at a time (the newest first), and lets go of HQ sets whose fit is gone.
func (a *App) hqPass() {
	if a.cfg.PlainBoards {
		return // turned off in Settings
	}
	ents, err := os.ReadDir(a.dir("pictures"))
	if err != nil {
		return
	}
	have := map[string]bool{}
	type fit struct {
		hash string
		at   time.Time
	}
	var todo []fit
	for _, e := range ents {
		h := strings.TrimSuffix(e.Name(), ".png")
		if !hashRe.MatchString(h) || have[h] {
			continue
		}
		have[h] = true
		if !a.hqDone(h) {
			info, _ := e.Info()
			var at time.Time
			if info != nil {
				at = info.ModTime()
			}
			todo = append(todo, fit{h, at})
		}
	}
	// the most recently captured first: what was just added gets its HQ soonest
	for i := 1; i < len(todo); i++ {
		for j := i; j > 0 && todo[j].at.After(todo[j-1].at); j-- {
			todo[j], todo[j-1] = todo[j-1], todo[j]
		}
	}
	for _, f := range todo {
		sig := a.hqSig(f.hash)
		t0, n, failed := time.Now(), 0, 0
		os.Remove(a.dir("hq", f.hash, hqMark))
		for _, j := range a.hqJobs(f.hash) {
			if st, err := os.Stat(j[1]); err == nil {
				if src, err := os.Stat(j[0]); err == nil && st.ModTime().After(src.ModTime()) {
					continue // made earlier from these pictures (a pass that was interrupted)
				}
			}
			if err := hqMake(j[0], j[1]); err != nil {
				failed++
				tl.logf("APP: HQ: %s: %v", filepath.Base(j[0]), err)
			}
			n++
			time.Sleep(hqPause())
		}
		complete := true
		for _, j := range a.hqJobs(f.hash) {
			if !fileExists(j[1]) {
				complete = false // cleared meanwhile (Storage), or a picture couldn't be made
			}
		}
		if failed == 0 && complete && a.hqSig(f.hash) == sig { // nothing changed under it meanwhile
			os.MkdirAll(a.dir("hq", f.hash), 0o755)
			os.WriteFile(a.dir("hq", f.hash, hqMark), []byte(sig), 0o644)
			tl.logf("APP: HQ pictures of %s made (%d pictures, %.0f s)", f.hash, n, time.Since(t0).Seconds())
		}
	}
	// HQ sets of fits that are gone (deleted, or renamed by an edit): made from pictures that no longer exist
	if hq, err := os.ReadDir(a.dir("hq")); err == nil {
		for _, e := range hq {
			h := strings.TrimSuffix(e.Name(), ".webp")
			if hashRe.MatchString(h) && !have[h] {
				os.RemoveAll(a.dir("hq", e.Name()))
			}
		}
	}
}
