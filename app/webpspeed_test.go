package main

import (
	"bytes"
	"image"
	_ "image/png"
	"os"
	"testing"
	"time"

	"github.com/gen2brain/webp"
)

func TestWebpSpeed(t *testing.T) {
	path := os.Getenv("PW_FRAME")
	if path == "" {
		t.Skip()
	}
	f, _ := os.Open(path)
	img, _, _ := image.Decode(f)
	f.Close()
	t0 := time.Now()
	up := upscale(img, 2)
	t1 := time.Now()
	var b bytes.Buffer
	if err := webp.Encode(&b, up, webp.Options{Quality: 90, Method: 4}); err != nil {
		t.Fatal(err)
	}
	t.Logf("upscale %v, webp q90 %v -> %d bytes", t1.Sub(t0), time.Since(t1), b.Len())
	back, err := webp.Decode(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	r, g, bb, a := back.At(10, 10).RGBA()
	t.Logf("decoded %v, corner %d %d %d %d", back.Bounds(), r, g, bb, a)
}
