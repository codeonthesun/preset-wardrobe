package main

import (
	"image"
	"image/png"
	"os"
	"testing"
	"time"
)

func TestEnhanceRealFrame(t *testing.T) {
	path := os.Getenv("PW_FRAME")
	if path == "" {
		t.Skip()
	}
	f, _ := os.Open(path)
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	out := upscale(img, 4)
	t.Logf("4x of %v in %v", img.Bounds(), time.Since(t0))
	o, _ := os.Create(os.Getenv("PW_OUT"))
	png.Encode(o, out)
	o.Close()
}

func TestEnhanceRealRoundTrip(t *testing.T) {
	path := os.Getenv("PW_FRAME")
	if path == "" {
		t.Skip()
	}
	f, _ := os.Open(path)
	img, _, _ := image.Decode(f)
	f.Close()
	b := img.Bounds()
	w, h, s := b.Dx(), b.Dy(), 2
	src := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := premultiplied(img, x, y)
			src[y*w+x] = float32(r)
		}
	}
	p := upPlane(src, w, h, s)
	var sum float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := (p[(2*y)*2*w+2*x] + p[(2*y)*2*w+2*x+1] + p[(2*y+1)*2*w+2*x] + p[(2*y+1)*2*w+2*x+1]) / 4
			d := float64(a - src[y*w+x])
			if d < 0 {
				d = -d
			}
			sum += d
		}
	}
	t.Logf("real frame round trip mean difference %.3f levels", sum/float64(w*h))
}
