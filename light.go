package main

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

// Board lighting: a post-effect for the board pictures. The Skaters page's stage lights the board
// from below, so the top of a board held up is in shadow. This evens it out and brings the colour
// back, as a copy beside each original (board/lit/1.png ... next to board/1.png), so the original
// is always kept and the app can show either.
//
// The steps were worked out on a board picture by jrkmods (2026-11-01); the app sets the exposure
// per picture and uses less saturation (lightApp), as the spec's settings (lightSpec) were tuned on
// a darker picture than the plugin captures:
//  1. a broad lighting map: the picture blurred by about a tenth of its height
//  2. flat-field division: divided by that map, normalised to its mean, at strength 0.40
//  3. top lift: brightened by a ramp from x1.85 at the top to x1 at the bottom
//  4. midtone gamma 0.90
//  5. exposure x2.10
//  6. clamped to 0..255
//  7. saturation x2.40 about the luma (0.299 R + 0.587 G + 0.114 B), clamped again
//
// Two things differ from working on a flat picture, because these are cut out on transparency:
// the blur is weighted by opacity (so the see-through background doesn't darken the edges, and
// the mean is the board's own), and the top-to-bottom ramp runs over the board itself rather than
// the whole picture. Transparency is kept as it was.

const (
	lightFlatten    = 0.40
	lightTopLift    = 0.85
	lightGamma      = 0.90
	lightExposure   = 2.10
	lightSaturation = 2.40
	lightBlurShare  = 96.0 / 1024 // blur radius as a share of the picture's height
	litDir          = "lit"
	litMark         = ".lit3" // the version of the clean-up the copies were made with (3: edge colours)
)

// lightParams: Exposure 0 means set per picture, so its average brightness ends up Target times
// what was captured.
type lightParams struct {
	Exposure, Target, Saturation float64
}

// The settings as given (exposure x2.10, saturation x2.40): worked out on a darker sample picture
// than the plugin's own captures, which they blow out (average brightness 94 -> 223).
var lightSpec = lightParams{Exposure: lightExposure, Saturation: lightSaturation}

// What the app uses: the same evening-out, with the exposure set per picture.
var lightApp = lightParams{Target: 1.25, Saturation: 1.5}

// evenLight returns the picture with the board lighting effect applied.
func evenLight(src image.Image) *image.NRGBA { return evenLightWith(src, lightApp) }

func evenLightWith(src image.Image, p lightParams) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	n := w * h
	r, g, bl, a := make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	top, bottom := -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cr, cg, cb, ca := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := y*w + x
			if ca == 0 {
				continue
			}
			// un-premultiplied colour, 0..255, and opacity 0..1
			al := float64(ca) / 65535
			r[i] = float64(cr) / float64(ca) * 255
			g[i] = float64(cg) / float64(ca) * 255
			bl[i] = float64(cb) / float64(ca) * 255
			a[i] = al
			if al > 0.15 {
				if top < 0 {
					top = y
				}
				bottom = y
			}
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	if top < 0 || bottom-top < 8 {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		return out
	}

	// 1. The lighting map: luminance, blurred, from the solid board only. The edge (a few pixels of
	// board mixed with the background's dark) would pull the map down there and over-brighten a band
	// just inside it, so it's left out; the map there comes from the board further in.
	solid := make([]bool, n)
	for i := range solid {
		solid[i] = a[i] > 0.98
	}
	m := solid
	for k := 0; k < 3; k++ {
		m = erode(m, w, h)
	}
	lum := make([]float64, n)
	weight := make([]float64, n)
	for i := range lum {
		if m[i] {
			lum[i] = 0.299*r[i] + 0.587*g[i] + 0.114*bl[i]
			weight[i] = 1
		}
	}
	sigma := lightBlurShare * float64(h)
	gaussBlur(lum, w, h, sigma)
	gaussBlur(weight, w, h, sigma)
	light := make([]float64, n)
	sum, cnt := 0.0, 0.0
	for i := range light {
		if weight[i] > 1e-3 {
			light[i] = lum[i]/weight[i] + 1e-5
		} else {
			light[i] = 1e-5
		}
		if a[i] > 0.15 {
			sum += light[i]
			cnt++
		}
	}
	mean := sum / cnt

	span := float64(bottom - top)
	exposure := p.Exposure
	if exposure == 0 {
		// how bright it is before exposure, against as captured: exposure brings it to Target
		before, after, wsum := 0.0, 0.0, 0.0
		for y := top; y <= bottom; y++ {
			t := math.Max(0, math.Min(1, 1-(float64(y)-float64(top))/span))
			lift := 1 + lightTopLift*t
			for x := 0; x < w; x++ {
				i := y*w + x
				if a[i] <= 0.15 {
					continue
				}
				eff := 1 + lightFlatten*(light[i]/mean-1)
				l0 := 0.299*r[i] + 0.587*g[i] + 0.114*bl[i]
				l1 := math.Pow(math.Max(0, l0/eff*lift)/255, lightGamma) * 255
				before += l0
				after += l1
				wsum++
			}
		}
		exposure = 1
		if after > 0 {
			exposure = math.Max(0.5, math.Min(3, p.Target*before/after))
		}
	}
	for y := 0; y < h; y++ {
		// 3. the ramp over the board: 1 at its top, 0 at its bottom
		t := 1 - (float64(y)-float64(top))/span
		t = math.Max(0, math.Min(1, t))
		lift := 1 + lightTopLift*t
		for x := 0; x < w; x++ {
			i := y*w + x
			if a[i] == 0 {
				continue
			}
			// 2. flat-field division at the set strength
			eff := 1 + lightFlatten*(light[i]/mean-1)
			c := [3]float64{r[i] / eff * lift, g[i] / eff * lift, bl[i] / eff * lift}
			for k := range c {
				v := math.Max(0, c[k])
				v = math.Pow(v/255, lightGamma) * 255 // 4. gamma
				v *= exposure                         // 5. exposure
				c[k] = math.Max(0, math.Min(255, v))  // 6. clamp
			}
			// 7. saturation about the luma
			y0 := 0.299*c[0] + 0.587*c[1] + 0.114*c[2]
			o := out.Pix[i*4 : i*4+4]
			for k := 0; k < 3; k++ {
				o[k] = uint8(math.Max(0, math.Min(255, y0+p.Saturation*(c[k]-y0))) + 0.5)
			}
			o[3] = uint8(a[i]*255 + 0.5)
		}
	}
	return out
}

// gaussBlur blurs v (w x h) in place: three box blurs each way, which come out as a Gaussian of
// the given sigma to within a few percent, and quickly at any size.
func gaussBlur(v []float64, w, h int, sigma float64) {
	// box sizes for three passes (Kovesi's)
	wIdeal := math.Sqrt(12*sigma*sigma/3 + 1)
	wl := int(math.Floor(wIdeal))
	if wl%2 == 0 {
		wl--
	}
	wu := wl + 2
	m := int(math.Round((12*sigma*sigma - 3*float64(wl*wl) - 12*float64(wl) - 9) / (-4*float64(wl) - 4)))
	tmp := make([]float64, len(v))
	for pass := 0; pass < 3; pass++ {
		size := wl
		if pass >= m {
			size = wu
		}
		r := (size - 1) / 2
		boxRows(v, tmp, w, h, r)
		boxCols(tmp, v, w, h, r)
	}
}

func boxRows(in, out []float64, w, h, r int) {
	parallel(h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			row := in[y*w : y*w+w]
			acc := 0.0
			for x := -r; x <= r; x++ {
				acc += row[clampInt(x, 0, w-1)]
			}
			for x := 0; x < w; x++ {
				out[y*w+x] = acc / float64(2*r+1)
				acc += row[clampInt(x+r+1, 0, w-1)] - row[clampInt(x-r, 0, w-1)]
			}
		}
	})
}

func boxCols(in, out []float64, w, h, r int) {
	parallel(w, func(lo, hi int) {
		for x := lo; x < hi; x++ {
			acc := 0.0
			for y := -r; y <= r; y++ {
				acc += in[clampInt(y, 0, h-1)*w+x]
			}
			for y := 0; y < h; y++ {
				out[y*w+x] = acc / float64(2*r+1)
				acc += in[clampInt(y+r+1, 0, h-1)*w+x] - in[clampInt(y-r, 0, h-1)*w+x]
			}
		}
	})
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// makeLit writes the lit copies of a picture's board views (board/1.png ...) into board/lit/.
// The originals aren't touched. Done once per version of the effect.
func makeLit(boardDir string) error {
	if _, err := os.Stat(filepath.Join(boardDir, litDir, litMark)); err == nil {
		return nil
	}
	out := filepath.Join(boardDir, litDir)
	os.RemoveAll(out)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for n := 1; n <= boardViews; n++ {
		b, err := os.ReadFile(filepath.Join(boardDir, strconv.Itoa(n)+".png"))
		if err != nil {
			os.RemoveAll(out)
			return err
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			os.RemoveAll(out)
			return err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, cleanEdges(evenLight(img))); err != nil {
			os.RemoveAll(out)
			return err
		}
		if err := os.WriteFile(filepath.Join(out, strconv.Itoa(n)+".png"), buf.Bytes(), 0o644); err != nil {
			os.RemoveAll(out)
			return err
		}
	}
	return os.WriteFile(filepath.Join(out, litMark), nil, 0o644)
}
