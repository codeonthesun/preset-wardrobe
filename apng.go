package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
)

// ReSkate frames a saved picture on the outline of what's in it. Saved one at a time, each frame of
// a turn is framed on its own outline, so the skater shifts and changes size from frame to frame.
// Saved together, as an animated PNG, every frame shares one frame: the plugin saves a turn that
// way and the app splits it back into pictures here. ReSkate writes every frame whole (no offsets,
// no blending), so each frame's data with the file's header is a picture of its own.

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

type pngChunk struct {
	kind string
	data []byte
}

func readChunks(b []byte) ([]pngChunk, error) {
	if !bytes.HasPrefix(b, pngSignature) {
		return nil, errors.New("not a PNG")
	}
	var out []pngChunk
	for at := len(pngSignature); at+12 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[at:]))
		if n < 0 || at+12+n > len(b) {
			return nil, errors.New("a PNG chunk runs past the end")
		}
		out = append(out, pngChunk{string(b[at+4 : at+8]), b[at+8 : at+8+n]})
		at += 12 + n
	}
	return out, nil
}

func writeChunk(w *bytes.Buffer, kind string, data []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	w.Write(n[:])
	w.WriteString(kind)
	w.Write(data)
	c := crc32.NewIEEE()
	c.Write([]byte(kind))
	c.Write(data)
	binary.BigEndian.PutUint32(n[:], c.Sum32())
	w.Write(n[:])
}

// splitAPNG returns each frame of an animated PNG as a PNG of its own. A plain PNG is one frame.
func splitAPNG(b []byte) ([][]byte, error) {
	chunks, err := readChunks(b)
	if err != nil {
		return nil, err
	}
	var header []pngChunk // IHDR and what goes with it, before the picture data
	var frames [][][]byte // each frame's data, in parts
	for _, c := range chunks {
		switch c.kind {
		case "acTL", "IEND":
		case "fcTL":
			frames = append(frames, nil)
		case "IDAT":
			if len(frames) == 0 {
				frames = append(frames, nil)
			}
			frames[len(frames)-1] = append(frames[len(frames)-1], c.data)
		case "fdAT":
			if len(frames) == 0 || len(c.data) < 4 {
				return nil, errors.New("a frame's data comes before its frame")
			}
			frames[len(frames)-1] = append(frames[len(frames)-1], c.data[4:])
		default:
			if len(frames) == 0 {
				header = append(header, c)
			}
		}
	}
	if len(header) == 0 || header[0].kind != "IHDR" {
		return nil, errors.New("no PNG header")
	}
	var out [][]byte
	for _, parts := range frames {
		if len(parts) == 0 {
			continue
		}
		var w bytes.Buffer
		w.Write(pngSignature)
		for _, h := range header {
			writeChunk(&w, h.kind, h.data)
		}
		for _, d := range parts {
			writeChunk(&w, "IDAT", d)
		}
		writeChunk(&w, "IEND", nil)
		out = append(out, w.Bytes())
	}
	if len(out) == 0 {
		return nil, errors.New("no frames")
	}
	return out, nil
}

// splitTo writes an animated PNG's frames into dir as <prefix><n>.png and returns their paths.
func splitTo(path, dir, prefix string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	frames, err := splitAPNG(b)
	if err != nil {
		return nil, err
	}
	var paths []string
	for i, f := range frames {
		p := filepath.Join(dir, fmt.Sprintf("%s%d.png", prefix, i+1))
		if err := os.WriteFile(p, f, 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// snapSets gives a snap's turn frames and board views as picture paths: split out of the plugin's
// animated PNGs (framed together, so framed is true) into a temporary folder tmp the caller
// removes, or else the separate pictures older plugins saved. Sets that aren't whole are left out.
func snapSets(snaps string, frames, board []string, turn, boardTurn string) (f, b []string, framed bool, tmp string) {
	if (turn == "" || !snapName.MatchString(turn)) && (boardTurn == "" || !snapName.MatchString(boardTurn)) {
		return existingFrames(snaps, frames), existingBoard(snaps, board), false, ""
	}
	tmp, err := os.MkdirTemp("", "presetwardrobe-split-")
	if err != nil {
		return nil, nil, true, ""
	}
	if turn != "" && snapName.MatchString(turn) {
		if p, err := splitTo(filepath.Join(snaps, turn), tmp, "t"); err == nil && len(p) >= 2 && len(p) <= maxFrames {
			f = p
		}
	}
	if boardTurn != "" && snapName.MatchString(boardTurn) {
		if p, err := splitTo(filepath.Join(snaps, boardTurn), tmp, "b"); err == nil && len(p) == boardViews {
			b = p
		}
	}
	return f, b, true, tmp
}

// snapFiles is every file of a snap's sets in the plugin's folder, to remove once it's filed.
func snapFiles(snaps string, frames, board []string, turn, boardTurn string) []string {
	var out []string
	for _, n := range append(append(append([]string{}, frames...), board...), turn, boardTurn) {
		if n != "" && snapName.MatchString(n) {
			out = append(out, filepath.Join(snaps, n))
		}
	}
	return out
}
