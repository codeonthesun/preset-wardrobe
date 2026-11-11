package main

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sort"
)

// ReSkate trims each saved picture to the outline of what's in it and centres that. As the skater
// turns, the board swings out to one side and back, so the outline (and with it the body) shifts
// sideways from frame to frame. The head sits on the axis the skater turns about, so the frames
// are lined up on the head instead.

// Folders lined up already. (Test builds tried lining frames up on the feet as well, as versions 2
// and 3; that's undone by snapping the fit again, and folders with any marker are left alone.)
const alignedMark = ".aligned3"

// headX is the centre of the opaque pixels in the top tenth of the figure, or -1.
func headX(img image.Image) float64 {
	b := img.Bounds()
	top, bottom := -1, -1
	for y := b.Min.Y; y < b.Max.Y && top < 0; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 40*257 {
				top = y
				break
			}
		}
	}
	for y := b.Max.Y - 1; y >= b.Min.Y && bottom < 0; y-- {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 40*257 {
				bottom = y
				break
			}
		}
	}
	if top < 0 || bottom-top < 20 {
		return -1
	}
	band := (bottom - top) / 10
	sum, n := 0.0, 0
	for y := top; y <= top+band; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 40*257 {
				sum += float64(x)
				n++
			}
		}
	}
	if n == 0 {
		return -1
	}
	return sum / float64(n)
}

// deckX is the centre of the opaque pixels in the middle fifth of a board view: the deck between the
// trucks, so the board's own middle and not the wheels sticking out to one side. -1 if none.
func deckX(img image.Image) float64 {
	b := img.Bounds()
	top, bottom := -1, -1
	for y := b.Min.Y; y < b.Max.Y && top < 0; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 40*257 {
				top = y
				break
			}
		}
	}
	for y := b.Max.Y - 1; y >= b.Min.Y && bottom < 0; y-- {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 40*257 {
				bottom = y
				break
			}
		}
	}
	if top < 0 || bottom-top < 20 {
		return -1
	}
	h := bottom - top
	sum, n := 0.0, 0
	for y := top + h*2/5; y <= top+h*3/5; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 40*257 {
				sum += float64(x)
				n++
			}
		}
	}
	if n == 0 {
		return -1
	}
	return sum / float64(n)
}

// alignFrames returns the frames, in order, as PNGs moved sideways so the head is in the same place
// in each. Pictures it can't read or measure are left as they were.
func alignFrames(paths []string) ([][]byte, error) { return alignBy(paths, headX) }

// keepAsIs reads the pictures without moving them.
func keepAsIs(paths []string) ([][]byte, error) {
	out := make([][]byte, len(paths))
	for i, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// alignBoard does the same for the board views, on the deck's middle.
func alignBoard(paths []string) ([][]byte, error) { return alignBy(paths, deckX) }

// alignBy moves each picture sideways so measure gives the same x in all (the middle one's). Only
// for pictures saved one at a time (older plugins); a turn saved in one go is framed alike already.
func alignBy(paths []string, measure func(image.Image) float64) ([][]byte, error) {
	raw := make([][]byte, len(paths))
	imgs := make([]image.Image, len(paths))
	heads := make([]float64, len(paths))
	for i, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		raw[i] = b
		heads[i] = -1
		if img, err := png.Decode(bytes.NewReader(b)); err == nil {
			imgs[i] = img
			heads[i] = measure(img)
		}
	}
	var good []float64
	for _, h := range heads {
		if h >= 0 {
			good = append(good, h)
		}
	}
	if len(good) < 2 {
		return raw, nil
	}
	sort.Float64s(good)
	target := good[len(good)/2] // the middle one: least to move overall
	out := make([][]byte, len(paths))
	for i := range paths {
		if imgs[i] == nil || heads[i] < 0 {
			out[i] = raw[i]
			continue
		}
		dx := int(target - heads[i] + 0.5)
		if target-heads[i] < 0 {
			dx = int(target - heads[i] - 0.5)
		}
		if dx == 0 {
			out[i] = raw[i]
			continue
		}
		b := imgs[i].Bounds()
		moved := image.NewNRGBA(b)
		draw.Draw(moved, b.Add(image.Pt(dx, 0)), imgs[i], b.Min, draw.Src)
		var buf bytes.Buffer
		if err := png.Encode(&buf, moved); err != nil {
			out[i] = raw[i]
			continue
		}
		out[i] = buf.Bytes()
	}
	return out, nil
}

// alignDir lines up the frames already filed in a picture's folder (1.png, 2.png, ...), once.
func alignDir(dir string) {
	for _, m := range []string{".aligned", ".aligned2", alignedMark} {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return
		}
	}
	var paths []string
	for n := 1; n <= maxFrames; n++ {
		p := filepath.Join(dir, itoa(n)+".png")
		if _, err := os.Stat(p); err != nil {
			break
		}
		paths = append(paths, p)
	}
	if len(paths) >= 2 {
		frames, err := alignFrames(paths)
		if err != nil {
			return
		}
		for i, b := range frames {
			os.WriteFile(paths[i], b, 0o644)
		}
	}
	os.WriteFile(filepath.Join(dir, alignedMark), nil, 0o644)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
