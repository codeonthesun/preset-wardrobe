package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The plugin writes an empty list as {}. One snap with "worn": {} must not hide the snaps after it.
func TestSnapIndexEmptyWornObject(t *testing.T) {
	idx := `{"singles":[
	 {"seq":48,"file":"fit-00048.png","worn":{}},
	 {"seq":49,"file":"fit-00049.png","worn":["Own_A","Own_B"]},
	 {"seq":50,"file":"fit-00050.png","frames":{"1":"fit-00050-1.png","2":"fit-00050-2.png"},"worn":["Own_C"]}]}`
	p := filepath.Join(t.TempDir(), "index.json")
	os.WriteFile(p, []byte(idx), 0o644)
	got, err := readSnapIndex(p)
	if err != nil || len(got.Singles) != 3 {
		t.Fatalf("read %d singles, error %v; want all 3", len(got.Singles), err)
	}
	if len(got.Singles[0].Worn) != 0 || len(got.Singles[1].Worn) != 2 || len(got.Singles[2].Frames) != 2 {
		t.Fatalf("lists read wrong: %+v %+v %+v", got.Singles[0], got.Singles[1], got.Singles[2])
	}
}
