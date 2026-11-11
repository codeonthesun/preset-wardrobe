package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupRestoreAndPrune(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "reskate.sqlite3")
	os.WriteFile(db, []byte("one"), 0o644)
	os.WriteFile(db+"-wal", []byte("wal-one"), 0o644)
	bdir := filepath.Join(dir, "backups")
	b1, err := backupProfile(db, bdir, "first", []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(db, []byte("two"), 0o644)
	os.Remove(db + "-wal")
	os.WriteFile(db+"-shm", []byte("shm-two"), 0o644)
	if err := restoreProfile(b1, db); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(db); string(b) != "one" {
		t.Errorf("main file: %q", b)
	}
	if b, _ := os.ReadFile(db + "-wal"); string(b) != "wal-one" {
		t.Errorf("wal: %q", b)
	}
	if fileExists(db + "-shm") {
		t.Error("an -shm the backup doesn't have was left behind")
	}
	list := listBackups(bdir)
	if len(list) != 1 || list[0].Why != "first" || len(list[0].Slots) != 1 {
		t.Fatalf("list: %+v", list)
	}
	// 20 more in a row: the newest 15 are kept (the first one, today's oldest, goes)
	for i := 0; i < 20; i++ {
		if _, err := backupProfile(db, bdir, "more", nil); err != nil {
			t.Fatal(err)
		}
	}
	// an old one from 3 days ago: the only one of its day, kept
	old := filepath.Join(bdir, time.Now().Add(-72*time.Hour).Format("2006-01-02_15-04-05"))
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "reskate.sqlite3"), []byte("x"), 0o644)
	// a folder that isn't a backup: never touched
	os.MkdirAll(filepath.Join(bdir, "removed-items"), 0o755)
	pruneBackups(bdir)
	left := listBackups(bdir)
	if len(left) != 16 {
		t.Errorf("expected 16 kept (15 newest + 1 older day), got %d", len(left))
	}
	if !fileExists(old) || !fileExists(filepath.Join(bdir, "removed-items")) {
		t.Error("pruned something it shouldn't")
	}
	if fileExists(b1) {
		t.Error("the oldest of today was kept beyond the 15")
	}
}
