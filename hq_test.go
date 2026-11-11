package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestPNG(t *testing.T, path string) {
	img := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := 10; y < 30; y++ {
		for x := 10; x < 30; x++ {
			img.Set(x, y, color.NRGBA{uint8(x * 6), uint8(y * 6), 90, 255})
		}
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, img)
	f.Close()
}

func TestHQPass(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir}
	h, gone := "0123456789abcdef", "fedcba9876543210"
	writeTestPNG(t, a.dir("pictures", h, "1.png"))
	writeTestPNG(t, a.dir("pictures", h, "2.png"))
	writeTestPNG(t, a.dir("pictures", h, "board", "1.png"))
	writeTestPNG(t, a.dir("pictures", h+".png"))
	os.MkdirAll(a.dir("hq", gone), 0o755) // a fit that no longer exists
	if a.hqState(h) != "working" {
		t.Fatalf("before: %q", a.hqState(h))
	}
	a.hqPass()
	if a.hqState(h) != "done" {
		t.Fatalf("after: %q", a.hqState(h))
	}
	for _, f := range []string{"1.webp", "2.webp", "board/1.webp"} {
		if !fileExists(a.dir("hq", h, filepath.FromSlash(f))) {
			t.Errorf("%s not made", f)
		}
	}
	if !fileExists(a.dir("hq", h+".webp")) {
		t.Error("card picture not made")
	}
	if fileExists(a.dir("hq", gone)) {
		t.Error("the HQ set of a fit that's gone was kept")
	}
	urls := a.frameURLs(h)
	if len(urls) != 2 || !strings.HasPrefix(urls[0], "/hq/"+h+"/1.webp?v=") {
		t.Errorf("frame URLs: %v", urls)
	}
	if p, ok := a.hqFile(strings.SplitN(strings.TrimPrefix(urls[0], "/hq/"), "?", 2)[0]); !ok || !fileExists(p) {
		t.Errorf("hqFile didn't find %s", urls[0])
	}
	if _, ok := a.hqFile("../config.json"); ok {
		t.Error("hqFile let a path out")
	}
	// a re-capture: the originals are shown again until the new HQ set is made
	time.Sleep(20 * time.Millisecond)
	writeTestPNG(t, a.dir("pictures", h, "3.png"))
	if a.hqState(h) != "working" || strings.HasPrefix(a.frameURLs(h)[0], "/hq/") {
		t.Error("a changed fit still shows its old HQ pictures")
	}
}
