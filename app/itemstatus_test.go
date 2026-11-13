package main

import (
	"path/filepath"
	"testing"
)

func TestItemStatusPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "item_status.json")
	loadItemStatus(path)
	itemMu.Lock()
	itemKnown = map[string]bool{"gone_item": true, "here_item": false}
	itemMu.Unlock()
	saveItemStatus()
	itemMu.Lock()
	itemKnown = map[string]bool{}
	itemMu.Unlock()
	loadItemStatus(path)
	if !itemWasMissing("gone_item") || itemWasMissing("here_item") || itemWasMissing("never_asked") {
		t.Fatal("what the game said about each item wasn't kept")
	}
}
