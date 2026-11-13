//go:build !windows

package main

import "errors"

// Changing the running game works only on Windows (the game runs there); elsewhere these do nothing.

const modelLoadOn = false

func patchLiveMemory(old, next []StoredPreset, body, sweep bool) (string, error) {
	return "", errors.New("changing the running game only works on Windows")
}

func findSkatePID() uint32 { return 0 }

func writePresetRecord(old, next []StoredPreset, body bool, idx int) error {
	return errors.New("Windows only")
}

func warmPreviewParts() {}

func previewReady() bool { return false }

func presetShowing(recipes string) bool { return false }

type selectionReader struct{}

func (r *selectionReader) Read() (uint32, int, int, error) {
	return 0, -1, 0, errors.New("Windows only")
}

func (r *selectionReader) Close() {}
