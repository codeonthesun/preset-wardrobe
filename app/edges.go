package main

import (
	"image"
	"math"
)

// Edge clean-up, the second half of "Clean up board pictures" (after the lighting, light.go).
// The game's cut-out leaves a board's outline with pinholes, stray specks and stair-stepped curves.
// The colours are left alone; only the transparency is redone (worked out by jrkmods, 2026-11-01):
//  1. the transparency as a mask: solid where it's more than half opaque
//  2. closing (grow, then shrink, 3x3) seals pinholes and gaps; opening (shrink, then grow) drops
//     single specks
//  3. only the biggest connected piece is kept (the board), its pinholes filled
//  4. a small Gaussian blur (sigma 0.8, 3x3) on the transparency, on top of the game's soft edge
//  5. the colours: the solid board's own; the soft edge's part-transparent pixels (a mix of board and
//     background, which the lighting turns into a jagged rim) take the solid board's next to them

// What the game cuts out already has a soft, anti-aliased edge; what goes wrong is the colour of
// those part-transparent edge pixels once the lighting brightens them (they turn into a jagged,
// pale or rusty rim). So the game's edge is kept, specks and pinholes are dealt with, and the edge
// pixels take their colour from the solid board just inside. (Rebuilding the outline from a
// blurred mask was tried: it flattened curves into lumps and lost the deck's thin wood edge.)
const (
	edgeFeather = 0.8 // the spec's small feather (3x3), on top of the game's own soft edge
)

func cleanEdges(src *image.NRGBA) *image.NRGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	n := w * h
	mask := make([]bool, n)
	for i := 0; i < n; i++ {
		mask[i] = src.Pix[i*4+3] > 127
	}
	// 2. closing, then opening
	mask = erode(dilate(mask, w, h), w, h)
	mask = dilate(erode(mask, w, h), w, h)
	// 3. the biggest piece, its pinholes filled; with a 2-pixel margin round it so the game's soft
	// edge stays whole
	body := fillHoles(biggestPiece(mask, w, h), w, h)
	keep := dilate(dilate(body, w, h), w, h)
	alpha := make([]float64, n)
	for i := 0; i < n; i++ {
		switch {
		case !keep[i]:
			alpha[i] = 0 // a speck, or anything not part of the board
		case body[i] && src.Pix[i*4+3] < 128:
			alpha[i] = 255 // a pinhole, filled
		default:
			alpha[i] = float64(src.Pix[i*4+3])
		}
	}
	// 4. feathered a little
	alpha = blur3(alpha, w, h, edgeFeather)
	// 5. colours: solid pixels keep their own; part-transparent ones (the edge) and filled pinholes
	// take the colour of the solid board next to them, a ring at a time
	out := image.NewNRGBA(src.Rect)
	copy(out.Pix, src.Pix)
	trusted := make([]bool, n)
	for i := 0; i < n; i++ {
		trusted[i] = src.Pix[i*4+3] >= 250
	}
	for pass := 0; pass < 8; pass++ {
		var next []int
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				if trusted[i] || alpha[i] < 1 {
					continue
				}
				var r, g, b, c float64
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						xx, yy := x+dx, y+dy
						if xx < 0 || yy < 0 || xx >= w || yy >= h {
							continue
						}
						j := yy*w + xx
						if trusted[j] {
							r += float64(out.Pix[j*4])
							g += float64(out.Pix[j*4+1])
							b += float64(out.Pix[j*4+2])
							c++
						}
					}
				}
				if c > 0 {
					out.Pix[i*4], out.Pix[i*4+1], out.Pix[i*4+2] = uint8(r/c+0.5), uint8(g/c+0.5), uint8(b/c+0.5)
					next = append(next, i)
				}
			}
		}
		if len(next) == 0 {
			break
		}
		for _, i := range next {
			trusted[i] = true
		}
	}
	for i := 0; i < n; i++ {
		out.Pix[i*4+3] = uint8(math.Max(0, math.Min(255, alpha[i])) + 0.5)
	}
	return out
}

func dilate(m []bool, w, h int) []bool { return morph(m, w, h, true) }
func erode(m []bool, w, h int) []bool  { return morph(m, w, h, false) }

// morph: 3x3 max (grow) or min (shrink) filter on the mask.
func morph(m []bool, w, h int, grow bool) []bool {
	out := make([]bool, len(m))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := !grow
			for dy := -1; dy <= 1 && v != grow; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := clampInt(x+dx, 0, w-1), clampInt(y+dy, 0, h-1)
					if m[yy*w+xx] == grow {
						v = grow
						break
					}
				}
			}
			out[y*w+x] = v
		}
	}
	return out
}

// biggestPiece keeps the largest group of touching solid pixels (8 ways).
func biggestPiece(m []bool, w, h int) []bool {
	label := make([]int32, len(m))
	best, bestSize := int32(0), 0
	next := int32(0)
	stack := make([]int, 0, 1024)
	for start := range m {
		if !m[start] || label[start] != 0 {
			continue
		}
		next++
		size := 0
		stack = append(stack[:0], start)
		label[start] = next
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			size++
			x, y := i%w, i/w
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if xx < 0 || yy < 0 || xx >= w || yy >= h {
						continue
					}
					j := yy*w + xx
					if m[j] && label[j] == 0 {
						label[j] = next
						stack = append(stack, j)
					}
				}
			}
		}
		if size > bestSize {
			best, bestSize = next, size
		}
	}
	out := make([]bool, len(m))
	for i := range m {
		out[i] = best != 0 && label[i] == best
	}
	return out
}

// fillHoles: small see-through holes the board closes all the way round become solid.
func fillHoles(m []bool, w, h int) []bool {
	outside := make([]bool, len(m))
	stack := make([]int, 0, 1024)
	push := func(i int) {
		if !m[i] && !outside[i] {
			outside[i] = true
			stack = append(stack, i)
		}
	}
	for x := 0; x < w; x++ {
		push(x)
		push((h-1)*w + x)
	}
	for y := 0; y < h; y++ {
		push(y * w)
		push(y*w + w - 1)
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := i%w, i/w
		if x > 0 {
			push(i - 1)
		}
		if x < w-1 {
			push(i + 1)
		}
		if y > 0 {
			push(i - w)
		}
		if y < h-1 {
			push(i + w)
		}
	}
	// Each enclosed see-through area is filled only if it's small: a pinhole. A real gap (between the
	// deck, a truck and a wheel, seen from the side) stays see-through.
	limit := len(m) / 2000 // about 500 pixels on a 1024 picture
	out := append([]bool(nil), m...)
	seen := make([]bool, len(m))
	for start := range m {
		if m[start] || outside[start] || seen[start] {
			continue
		}
		hole := []int{start}
		seen[start] = true
		for k := 0; k < len(hole); k++ {
			i := hole[k]
			x, y := i%w, i/w
			for _, j := range [4]int{i - 1, i + 1, i - w, i + w} {
				if j < 0 || j >= len(m) || (j == i-1 && x == 0) || (j == i+1 && x == w-1) || (j == i-w && y == 0) || (j == i+w && y == h-1) {
					continue
				}
				if !m[j] && !outside[j] && !seen[j] {
					seen[j] = true
					hole = append(hole, j)
				}
			}
		}
		if len(hole) <= limit {
			for _, i := range hole {
				out[i] = true
			}
		}
	}
	return out
}

// blur3: a 3x3 Gaussian of the given sigma.
func blur3(v []float64, w, h int, sigma float64) []float64 {
	k := [3]float64{math.Exp(-1 / (2 * sigma * sigma)), 1, math.Exp(-1 / (2 * sigma * sigma))}
	sum := k[0] + k[1] + k[2]
	for i := range k {
		k[i] /= sum
	}
	tmp := make([]float64, len(v))
	out := make([]float64, len(v))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			tmp[y*w+x] = k[0]*v[y*w+clampInt(x-1, 0, w-1)] + k[1]*v[y*w+x] + k[2]*v[y*w+clampInt(x+1, 0, w-1)]
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out[y*w+x] = k[0]*tmp[clampInt(y-1, 0, h-1)*w+x] + k[1]*tmp[y*w+x] + k[2]*tmp[clampInt(y+1, 0, h-1)*w+x]
		}
	}
	return out
}
