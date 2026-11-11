//go:build !windows

package main

import "errors"

func patchLiveMemory(old, next []StoredPreset, body, sweep bool) (string, error) {
	return "", errors.New("changing the running game only works on Windows")
}

var liveCachePath string

func findSkatePID() uint32 { return 0 }

func warmLiveMemory(cur []StoredPreset) (bool, error) {
	return false, errors.New("only works on Windows")
}

func warmBoard() {}

func boardEarly() {}

func noteLiveOutfit(preset string, items map[uint32]uint32, lens map[uint32]int) {}
