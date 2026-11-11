package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Moving between presets for the plugin. ReSkate lets plugins hold a stick but never press a
// button, and the Skaters page moves between presets with the shoulder buttons (Z and C on the
// keyboard). So in a walk-through the plugin writes snaps/keys.json asking for a key, and the app
// presses it for the game, once, and answers in snaps/keys_done.json. Only while the game is the
// window in front: a key pressed anywhere else would land in the wrong window.

type keyRequest struct {
	ID  int    `json:"id"`
	Key string `json:"key"` // "z" (left) or "c" (right)
}

type keyAnswer struct {
	ID      int    `json:"id"`
	Pressed bool   `json:"pressed"`
	Why     string `json:"why,omitempty"`
}

func (a *App) watchKeyRequests() {
	done := 0
	answered := 0
	for range time.Tick(150 * time.Millisecond) {
		a.mu.Lock()
		dir := a.pluginDataDir()
		a.mu.Unlock()
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, "snaps", "keys.json")
		st, err := os.Stat(path)
		if err != nil || time.Since(st.ModTime()) > 10*time.Second {
			continue // none, or an old one left from before: never pressed late
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var req keyRequest
		if json.Unmarshal(b, &req) != nil || req.ID <= done || (req.Key != "z" && req.Key != "c") {
			continue
		}
		answer := func(ans keyAnswer) {
			if bs, err := json.Marshal(ans); err == nil {
				os.WriteFile(filepath.Join(dir, "snaps", "keys_done.json"), bs, 0o644)
			}
		}
		if !gameInFront() {
			if answered != req.ID { // say so once; keep watching until it's in front
				answer(keyAnswer{ID: req.ID, Pressed: false, Why: "the game isn't the window in front"})
				answered = req.ID
			}
			continue
		}
		if err := pressKey(req.Key); err != nil {
			log.Println("couldn't press", req.Key, "for the plugin:", err)
			answer(keyAnswer{ID: req.ID, Pressed: false, Why: err.Error()})
			done = req.ID
			continue
		}
		done = req.ID
		answer(keyAnswer{ID: req.ID, Pressed: true})
	}
}
