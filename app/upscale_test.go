package main

import (
	"image"
	"image/color"
	"testing"
)

// Only the part holding the skater is worked on: a blank picture, a skater against the picture's edge and one in
// the middle all come out at the right size, with the transparent surround kept transparent.
func TestUpscaleCropsToSkater(t *testing.T) {
	blank := image.NewNRGBA(image.Rect(0, 0, 40, 30))
	if out := upscale(blank, 2); out.Bounds().Dx() != 80 || out.Bounds().Dy() != 60 {
		t.Fatalf("blank: %v", out.Bounds())
	}
	for _, at := range []image.Point{{0, 0}, {18, 12}, {34, 24}} {
		img := image.NewNRGBA(image.Rect(0, 0, 40, 30))
		for y := at.Y; y < at.Y+6; y++ {
			for x := at.X; x < at.X+6; x++ {
				img.SetNRGBA(x, y, color.NRGBA{200, 80, 40, 255})
			}
		}
		out := upscale(img, 2)
		if out.Bounds().Dx() != 80 || out.Bounds().Dy() != 60 {
			t.Fatalf("size %v", out.Bounds())
		}
		if c := out.NRGBAAt(at.X*2+6, at.Y*2+6); c.A != 255 || c.R < 150 {
			t.Fatalf("skater at %v came out as %v", at, c)
		}
		far := image.Pt((at.X*2+40)%80, (at.Y*2+30)%60)
		if c := out.NRGBAAt(far.X, far.Y); c.A != 0 {
			t.Fatalf("surround at %v isn't transparent: %v", far, c)
		}
	}
}

// The specification's porting check: stage 1 alone, averaged back down by s, gives the source again
// (mean difference under about 0.2 levels on real pictures; this pattern changes every pixel, so it is allowed more).
func TestUpscaleRoundTrip(t *testing.T) {
	w, h, s := 64, 48, 2
	src := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32((x*37+y*91)%256) * 0.6
			if (x/8+y/8)%2 == 0 {
				v += 80
			}
			src[y*w+x] = v
		}
	}
	p := upPlane(src, w, h, s)
	var sum float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var a float32
			for dy := 0; dy < s; dy++ {
				for dx := 0; dx < s; dx++ {
					a += p[(y*s+dy)*w*s+x*s+dx]
				}
			}
			d := float64(a/float32(s*s) - src[y*w+x])
			if d < 0 {
				d = -d
			}
			sum += d
		}
	}
	if mean := sum / float64(w*h); mean > 2 {
		t.Errorf("round trip off by %.2f levels on average", mean)
	} else {
		t.Logf("round trip mean difference %.3f levels", mean)
	}
}
