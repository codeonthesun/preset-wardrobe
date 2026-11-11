package main

import (
	"bytes"
	"container/list"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Enhance: a sharper, larger version of a picture, made on the fly when you ask for it in the large
// viewer and kept only in memory (never written to disk), so it costs no storage and is gone when the
// app closes. The originals are never touched.
//
// It follows the "Clarity Upscaler" specification (2026-11-10): classical image processing only, no
// learned data, so nothing is invented and faces, logos and text stay as captured.
//  1. colour premultiplied by opacity (the black under see-through pixels can't bleed into the outline)
//  2. Lanczos-3 to the new size, held within the bilinear-enlarged 3x3 range of the source (no ringing)
//  3. iterative back-projection: the result, averaged back down, gives the source again
//  4. a brightness-only detail boost: gated against noise, tapered on strong edges, faded out near
//     transparency, and softly limited to the local range, so it makes no halos
//  5. colour divided by opacity again

const (
	upBoardScale = 2 // the board pictures are captured larger already
	upScale      = 3 // made big enough that, zoomed in, the browser shows it close to its own size (scaling it up again softens it)
	upSharpness  = 0.5
	upIBPIters   = 4
	upIBPBeta    = 1.0
	upSigma      = 0.8 // in source pixels
	upNoise      = 3.0
	upTaper      = 40.0
)

// parallel runs fn over 0..n in chunks, one goroutine per CPU core (up to 8). The chunks never overlap, so
// fn may write to the rows or columns it was given without locks.
func parallel(n int, fn func(lo, hi int)) { parallelW(min(runtime.NumCPU(), 8), n, fn) }

// parallelW is parallel with a set number of workers (1: all on the calling thread).
func parallelW(workers, n int, fn func(lo, hi int)) {
	if workers < 2 || n < 2*workers {
		fn(0, n)
		return
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := min(lo+chunk, n)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer recoverLog("upscale")
			fn(lo, hi)
		}()
	}
	wg.Wait()
}

// upMargin is how many source pixels of transparent border are kept around the skater when only that part is
// processed: more than any of the filters below reach, so the result is the same as processing the whole picture.
const upMargin = 12

// visibleBox is the smallest rectangle holding every pixel that isn't fully transparent.
func visibleBox(src image.Image) image.Rectangle {
	b := src.Bounds()
	box := image.Rectangle{Min: b.Max, Max: b.Min}
	hit := func(x, y int) {
		if x < box.Min.X {
			box.Min.X = x
		}
		if y < box.Min.Y {
			box.Min.Y = y
		}
		if x+1 > box.Max.X {
			box.Max.X = x + 1
		}
		if y+1 > box.Max.Y {
			box.Max.Y = y + 1
		}
	}
	if n, ok := src.(*image.NRGBA); ok {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := n.Pix[(y-n.Rect.Min.Y)*n.Stride:]
			for x := b.Min.X; x < b.Max.X; x++ {
				if row[(x-n.Rect.Min.X)*4+3] != 0 {
					hit(x, y)
				}
			}
		}
	} else {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if _, _, _, a := src.At(x, y).RGBA(); a != 0 {
					hit(x, y)
				}
			}
		}
	}
	if box.Empty() {
		return image.Rectangle{}
	}
	return box
}

// premultiplied returns the colour and opacity of a pixel as 0..255 floats, colour multiplied by opacity.
func premultiplied(src image.Image, x, y int) (r, g, b, a float64) {
	if n, ok := src.(*image.NRGBA); ok {
		p := n.Pix[(y-n.Rect.Min.Y)*n.Stride+(x-n.Rect.Min.X)*4:]
		a16 := uint32(p[3]) * 0x101
		return float64(uint32(p[0])*0x101*a16/0xffff) / 257, float64(uint32(p[1])*0x101*a16/0xffff) / 257,
			float64(uint32(p[2])*0x101*a16/0xffff) / 257, float64(a16) / 257
	}
	pr, pg, pb, pa := src.At(x, y).RGBA() // premultiplied, 0..65535
	return float64(pr) / 257, float64(pg) / 257, float64(pb) / 257, float64(pa) / 257
}

// upscale returns src at scale times its size, enhanced as above. Only the part of the picture that holds the
// skater is worked on (the rest is transparent, and stays so): the result is the same, much sooner.
func upscale(src image.Image, scale int) *image.NRGBA {
	return upscaleW(src, scale, min(runtime.NumCPU(), 8))
}

// upscaleW is upscale on at most that many threads (the background HQ maker uses 1, so the game keeps the rest).
func upscaleW(src image.Image, scale, workers int) *image.NRGBA {
	b := src.Bounds()
	scale = max(scale, 1)
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx()*scale, b.Dy()*scale))
	vis := visibleBox(src)
	if vis.Empty() {
		return out
	}
	crop := image.Rect(max(b.Min.X, vis.Min.X-upMargin), max(b.Min.Y, vis.Min.Y-upMargin),
		min(b.Max.X, vis.Max.X+upMargin), min(b.Max.Y, vis.Max.Y+upMargin))
	w, h := crop.Dx(), crop.Dy()
	n := w * h
	pl := [4][]float32{make([]float32, n), make([]float32, n), make([]float32, n), make([]float32, n)}
	opaque := true
	parallelW(workers, h, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				r, g, bb, a := premultiplied(src, crop.Min.X+x, crop.Min.Y+y)
				pl[0][i], pl[1][i], pl[2][i], pl[3][i] = float32(r), float32(g), float32(bb), float32(a)
			}
		}
	})
	for _, a := range pl[3] {
		if a < 255 {
			opaque = false
			break
		}
	}
	planes := pl[:]
	if opaque {
		planes = pl[:3]
	}
	W, H := w*scale, h*scale
	up := make([][]float32, len(planes))
	parallelW(workers, len(planes), func(lo, hi int) {
		for k := lo; k < hi; k++ {
			up[k] = upPlane(planes[k], w, h, scale)
		}
	})
	if upSharpness > 0 {
		var alpha []float32
		if !opaque {
			alpha = up[3]
		}
		detailBoost(up[0], up[1], up[2], alpha, W, H, scale, workers)
	}
	ox, oy := (crop.Min.X-b.Min.X)*scale, (crop.Min.Y-b.Min.Y)*scale
	clamp8 := func(v float32) uint8 {
		if v <= 0 {
			return 0
		}
		if v >= 255 {
			return 255
		}
		return uint8(v + 0.5)
	}
	parallelW(workers, H, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < W; x++ {
				i := y*W + x
				o := (oy+y)*out.Stride + (ox+x)*4
				if opaque {
					out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = clamp8(up[0][i]), clamp8(up[1][i]), clamp8(up[2][i]), 255
					continue
				}
				a := clamp8(up[3][i])
				if a == 0 {
					continue // (0,0,0,0)
				}
				f := 255 / float32(a)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = clamp8(up[0][i]*f), clamp8(up[1][i]*f), clamp8(up[2][i]*f), a
			}
		}
	})
	return out
}

func lanczos3(x float64) float64 {
	if x == 0 {
		return 1
	}
	if x <= -3 || x >= 3 {
		return 0
	}
	px := math.Pi * x
	return 3 * math.Sin(px) * math.Sin(px/3) / (px * px)
}

// lanczosTaps: for each output index on an axis of n*s, the 6 source indices (edge-clamped) and normalised weights,
// with pixel-centre alignment.
func lanczosTaps(n, s int) (idx [][6]int, wt [][6]float32) {
	N := n * s
	idx, wt = make([][6]int, N), make([][6]float32, N)
	for i := 0; i < N; i++ {
		c := (float64(i)+0.5)/float64(s) - 0.5
		f := int(math.Floor(c))
		var sum float64
		var ws [6]float64
		for t := 0; t < 6; t++ {
			j := f - 2 + t
			ws[t] = lanczos3(c - float64(j))
			sum += ws[t]
			idx[i][t] = min(max(j, 0), n-1)
		}
		for t := 0; t < 6; t++ {
			wt[i][t] = float32(ws[t] / sum)
		}
	}
	return
}

// lanczosUp enlarges v (w x h) s times, rows first, then columns.
func lanczosUp(v []float32, w, h, s int, xi [][6]int, xw [][6]float32, yi [][6]int, yw [][6]float32) []float32 {
	W, H := w*s, h*s
	tmp := make([]float32, W*h)
	for y := 0; y < h; y++ {
		row := v[y*w:]
		for X := 0; X < W; X++ {
			var acc float32
			for t := 0; t < 6; t++ {
				acc += row[xi[X][t]] * xw[X][t]
			}
			tmp[y*W+X] = acc
		}
	}
	out := make([]float32, W*H)
	for Y := 0; Y < H; Y++ {
		for X := 0; X < W; X++ {
			var acc float32
			for t := 0; t < 6; t++ {
				acc += tmp[yi[Y][t]*W+X] * yw[Y][t]
			}
			out[Y*W+X] = acc
		}
	}
	return out
}

// minMax: the running minimum and maximum of v over a square window of radius r (separable, edges clamped).
func minMax(v []float32, w, h, r int) (lo, hi []float32) {
	tl, th := make([]float32, len(v)), make([]float32, len(v))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			mn, mx := float32(math.MaxFloat32), float32(-math.MaxFloat32)
			for d := -r; d <= r; d++ {
				p := v[y*w+min(max(x+d, 0), w-1)]
				mn, mx = min(mn, p), max(mx, p)
			}
			tl[y*w+x], th[y*w+x] = mn, mx
		}
	}
	lo, hi = make([]float32, len(v)), make([]float32, len(v))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			mn, mx := float32(math.MaxFloat32), float32(-math.MaxFloat32)
			for d := -r; d <= r; d++ {
				yy := min(max(y+d, 0), h-1)
				mn, mx = min(mn, tl[yy*w+x]), max(mx, th[yy*w+x])
			}
			lo[y*w+x], hi[y*w+x] = mn, mx
		}
	}
	return
}

// bilinearUp enlarges v s times with the same centre mapping and edge clamping.
func bilinearUp(v []float32, w, h, s int) []float32 {
	W, H := w*s, h*s
	out := make([]float32, W*H)
	coord := func(i, n int) (int, int, float32) {
		c := (float64(i)+0.5)/float64(s) - 0.5
		f := math.Floor(c)
		j0 := min(max(int(f), 0), n-1)
		j1 := min(max(int(f)+1, 0), n-1)
		return j0, j1, float32(c - f)
	}
	for Y := 0; Y < H; Y++ {
		y0, y1, fy := coord(Y, h)
		for X := 0; X < W; X++ {
			x0, x1, fx := coord(X, w)
			a := v[y0*w+x0]*(1-fx) + v[y0*w+x1]*fx
			b := v[y1*w+x0]*(1-fx) + v[y1*w+x1]*fx
			out[Y*W+X] = a*(1-fy) + b*fy
		}
	}
	return out
}

// upPlane is stage 1 for one plane: Lanczos-3, the anti-ringing envelope, then back-projection.
func upPlane(src []float32, w, h, s int) []float32 {
	if s == 1 {
		return append([]float32(nil), src...)
	}
	xi, xw := lanczosTaps(w, s)
	yi, yw := lanczosTaps(h, s)
	p := lanczosUp(src, w, h, s, xi, xw, yi, yw)
	lo, hi := minMax(src, w, h, 1)
	mnUp, mxUp := bilinearUp(lo, w, h, s), bilinearUp(hi, w, h, s)
	clampEnv := func() {
		for i := range p {
			p[i] = min(max(p[i], mnUp[i]), mxUp[i])
		}
	}
	clampEnv()
	W := w * s
	inv := 1 / float32(s*s)
	e := make([]float32, w*h)
	for it := 0; it < upIBPIters; it++ {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var sum float32
				for dy := 0; dy < s; dy++ {
					row := p[(y*s+dy)*W+x*s:]
					for dx := 0; dx < s; dx++ {
						sum += row[dx]
					}
				}
				e[y*w+x] = src[y*w+x] - sum*inv
			}
		}
		c := lanczosUp(e, w, h, s, xi, xw, yi, yw)
		for i := range p {
			p[i] += upIBPBeta * c[i]
		}
		clampEnv()
	}
	return p
}

// detailBoost is stage 2, on the premultiplied colour planes in place: an adaptive, brightness-only detail boost
// with a soft limit. alpha is nil for an opaque picture.
func detailBoost(r, g, b, alpha []float32, W, H, s, workers int) {
	N := W * H
	Y := make([]float32, N)
	for i := 0; i < N; i++ {
		Y[i] = 0.299*r[i] + 0.587*g[i] + 0.114*b[i]
	}
	// Gaussian blur, separable, radius ceil(3 sigma)
	sig := upSigma * float64(s)
	rad := int(math.Ceil(3 * sig))
	k := make([]float32, 2*rad+1)
	var ks float64
	for i := -rad; i <= rad; i++ {
		v := math.Exp(-float64(i*i) / (2 * sig * sig))
		k[i+rad] = float32(v)
		ks += v
	}
	for i := range k {
		k[i] /= float32(ks)
	}
	tmp, B := make([]float32, N), make([]float32, N)
	parallelW(workers, H, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < W; x++ {
				var acc float32
				for d := -rad; d <= rad; d++ {
					acc += Y[y*W+min(max(x+d, 0), W-1)] * k[d+rad]
				}
				tmp[y*W+x] = acc
			}
		}
	})
	parallelW(workers, H, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < W; x++ {
				var acc float32
				for d := -rad; d <= rad; d++ {
					acc += tmp[min(max(y+d, 0), H-1)*W+x] * k[d+rad]
				}
				B[y*W+x] = acc
			}
		}
	})
	ymn, ymx := minMax(Y, W, H, s)
	var amn []float32
	if alpha != nil {
		amn, _ = minMax(alpha, W, H, 2*s)
	}
	const sharp, n2, t2 = float32(upSharpness), float32(upNoise * upNoise), float32(upTaper * upTaper)
	parallelW(workers, H, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < W; x++ {
				i := y*W + x
				d := Y[i] - B[i]
				if d == 0 {
					continue
				}
				d2 := d * d
				gain := sharp * 2 * (d2 / (d2 + n2)) / (1 + d2/t2)
				if amn != nil {
					am := amn[i] / 255
					gain *= am * am
				}
				want := gain * float32(math.Abs(float64(d)))
				if want <= 0 {
					continue
				}
				var room float32
				if d > 0 {
					room = ymx[i] - Y[i]
				} else {
					room = Y[i] - ymn[i]
				}
				if room <= 0 {
					continue
				}
				move := room * want / (room + want)
				if d < 0 {
					move = -move
				}
				r[i] += move
				g[i] += move
				b[i] += move
			}
		}
	})
}

// scaleFor picks how much to enlarge a picture of this size: the small older snaps 3x, the larger
// ones the plugin takes now 2x.
func scaleFor(w, h int) int {
	if max(w, h) <= 512 {
		return upScale
	}
	return 2
}

// enhancer makes the enhanced pictures on request and keeps the newest in memory, up to a size.
type enhancer struct {
	mu       sync.Mutex
	cap      int
	size     int
	order    *list.List // of *enhItem, newest first
	items    map[string]*list.Element
	inflight map[string]chan struct{}
}

type enhItem struct {
	key  string
	data []byte
}

func newEnhancer(capBytes int) *enhancer {
	return &enhancer{cap: capBytes, order: list.New(), items: map[string]*list.Element{}, inflight: map[string]chan struct{}{}}
}

// get returns the enhanced PNG of the picture at path (key identifies it and its version).
func (e *enhancer) get(key, path string, scale int) ([]byte, error) {
	for {
		e.mu.Lock()
		if el, ok := e.items[key]; ok {
			e.order.MoveToFront(el)
			d := el.Value.(*enhItem).data
			e.mu.Unlock()
			return d, nil
		}
		if wait, busy := e.inflight[key]; busy {
			e.mu.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		e.inflight[key] = done
		e.mu.Unlock()
		data, err := enhanceFile(path, scale)
		e.mu.Lock()
		delete(e.inflight, key)
		close(done)
		if err == nil {
			e.items[key] = e.order.PushFront(&enhItem{key, data})
			e.size += len(data)
			for e.size > e.cap && e.order.Len() > 1 {
				last := e.order.Back()
				it := last.Value.(*enhItem)
				e.order.Remove(last)
				delete(e.items, it.key)
				e.size -= len(it.data)
			}
		}
		e.mu.Unlock()
		return data, err
	}
}

// has: whether that picture is still kept in memory.
func (e *enhancer) has(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.items[key]
	return ok
}

func (e *enhancer) clear() {
	e.mu.Lock()
	e.order.Init()
	e.items = map[string]*list.Element{}
	e.size = 0
	e.mu.Unlock()
}

func enhanceFile(path string, scale int) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if scale <= 0 {
		scale = scaleFor(img.Bounds().Dx(), img.Bounds().Dy())
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, upscale(img, scale)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- grid thumbnails ---------------------------------------------------------------------------

const thumbSide = 576 // the longest side of a thumbnail

// thumbnail returns the path of a smaller copy of the picture at src (made once, kept in the
// thumbs folder, and made again if the picture changes), or src itself if it's small already.
func thumbnail(src, cacheDir, name string) string {
	st, err := os.Stat(src)
	if err != nil {
		return src
	}
	out := filepath.Join(cacheDir, name+".png")
	if o, err := os.Stat(out); err == nil && !o.ModTime().Before(st.ModTime()) {
		return out
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return src
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return src
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if max(w, h) <= thumbSide*5/4 {
		return src
	}
	k := float64(thumbSide) / float64(max(w, h))
	W, H := max(1, int(float64(w)*k+0.5)), max(1, int(float64(h)*k+0.5))
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, areaScale(img, W, H)); err != nil {
		return src
	}
	os.MkdirAll(cacheDir, 0o755)
	tmp := out + ".tmp"
	if os.WriteFile(tmp, buf.Bytes(), 0o644) != nil || os.Rename(tmp, out) != nil {
		os.Remove(tmp)
		return src
	}
	return out
}

// areaScale shrinks an image by averaging the pixels each new one covers (colour weighted by opacity,
// so the outline doesn't pick up a dark edge).
func areaScale(src image.Image, W, H int) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		y0, y1 := float64(y)*float64(h)/float64(H), float64(y+1)*float64(h)/float64(H)
		for x := 0; x < W; x++ {
			x0, x1 := float64(x)*float64(w)/float64(W), float64(x+1)*float64(w)/float64(W)
			var r, g, bl, a, wsum float64
			for sy := int(y0); sy < int(math.Ceil(y1)) && sy < h; sy++ {
				wy := math.Min(y1, float64(sy+1)) - math.Max(y0, float64(sy))
				for sx := int(x0); sx < int(math.Ceil(x1)) && sx < w; sx++ {
					wt := wy * (math.Min(x1, float64(sx+1)) - math.Max(x0, float64(sx)))
					pr, pg, pb, pa := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA() // premultiplied
					r += wt * float64(pr)
					g += wt * float64(pg)
					bl += wt * float64(pb)
					a += wt * float64(pa)
					wsum += wt
				}
			}
			if wsum == 0 || a == 0 {
				continue
			}
			i := (y*W + x) * 4
			out.Pix[i] = uint8(math.Min(255, r/a*255+0.5))
			out.Pix[i+1] = uint8(math.Min(255, g/a*255+0.5))
			out.Pix[i+2] = uint8(math.Min(255, bl/a*255+0.5))
			out.Pix[i+3] = uint8(math.Min(255, a/wsum/257+0.5))
		}
	}
	return out
}
