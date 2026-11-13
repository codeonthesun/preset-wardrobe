package main

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// ---- the Skaters page's preview ---------------------------------------------------------------------------------
//
// With the Skaters page open the game shows a preview of the preset, apart from the skater in the world (read with
// the model probe): the menu works on its own copy of the preset, a second list of outfits in the data model (item 0
// the skater's, item 1 the board's). The preview's board part follows that list's item 1 by its handle, as the world
// board does; the preview skater's part has no handle (its outfit is copied into it by the menu: +0x138 is 1, so the
// game's update doesn't look anything up and builds what the part holds). The local player's world parts have 3 at
// +0x130; the preview's don't.
//
// A load with the Skaters page open, when the menu shows the preset being loaded, writes the fit into the menu's list
// and into the preview skater's own outfit, and flags both preview parts. The preview parts aren't where the world's
// are in memory, and finding them means reading all of it (seconds): it's done in the background while the Skaters
// page is open (watchMenuParts), and a load uses what was found, checked again before it's used.

var preview struct {
	sync.Mutex
	pid    uint32
	gen    int64
	parts  []uint64        // every cosmetic part that isn't the local player's world skater or board
	allocs map[uint64]bool // the memory blocks they were found in: the page makes new parts there (on every
	// switch between presets), and those few blocks are read again in milliseconds
	at       time.Time
	scanning bool
}

// warmPreviewParts finds the preview parts, unless they're known for this game and level already.
func warmPreviewParts() {
	pid := findSkatePID()
	if pid == 0 {
		return
	}
	preview.Lock()
	if preview.scanning || (preview.pid == pid && preview.gen == profileApplied.Load() && len(preview.parts) > 0 && previewPartsValid(pid)) {
		preview.Unlock()
		return
	}
	preview.scanning = true
	preview.Unlock()
	defer func() {
		preview.Lock()
		preview.scanning = false
		preview.Unlock()
	}()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	base := moduleBase(pid)
	if base == 0 {
		return
	}
	st := time.Now()
	gen := profileApplied.Load()
	var parts []uint64
	regs := writableRegions(h)
	// they've sat in blocks of about 2 MB: those first (about a second), everything only if none is there
	found := findComponentsIn(h, base+cosmeticComponentVtable, 64<<10, 16<<20)
	if len(found) == 0 {
		found = findComponentsIn(h, base+cosmeticComponentVtable, 64<<10, 0)
	}
	for _, c := range found {
		if who, _ := rd32(h, c+0x130); who == 3 {
			continue
		}
		parts = append(parts, c)
	}
	var where []string
	allocs := map[uint64]bool{}
	for _, c := range parts {
		where = append(where, fmt.Sprintf("%#x (block %d MB)", c, blockClass(regs, c)))
		for _, r := range regs {
			if r.base <= c && c < r.base+r.size {
				allocs[r.alloc] = true
			}
		}
	}
	tl.logf("LIVE: preview: %d parts besides the world's, found in %.1f s: %v", len(parts), time.Since(st).Seconds(), where)
	preview.Lock()
	preview.pid, preview.gen, preview.parts, preview.allocs, preview.at = pid, gen, parts, allocs, time.Now()
	preview.Unlock()
}

// previewPartsValid: the remembered parts still look like parts (called with preview locked).
func previewPartsValid(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	want := moduleBase(pid) + cosmeticComponentVtable
	for _, c := range preview.parts {
		if v, ok := rd64(h, c); !ok || v != want {
			return false
		}
	}
	return true
}

// previewReady: the preview parts are known (a load in the menu can update the preview).
func previewReady() bool {
	pid := findSkatePID()
	preview.Lock()
	defer preview.Unlock()
	return pid != 0 && preview.pid == pid && preview.gen == profileApplied.Load() && len(preview.allocs) > 0
}

// freshPreviewParts: the preview parts now, read from the blocks they're made in (milliseconds), so a load right
// after a switch between presets finds the parts the page has just made.
func freshPreviewParts(h windows.Handle, pid uint32) []uint64 {
	preview.Lock()
	allocs := preview.allocs
	preview.Unlock()
	want := moduleBase(pid) + cosmeticComponentVtable
	var out []uint64
	le := binary.LittleEndian
	for _, r := range writableRegions(h) {
		if !allocs[r.alloc] || r.size > 64<<20 {
			continue
		}
		buf := make([]byte, r.size)
		var rd uintptr
		if windows.ReadProcessMemory(h, uintptr(r.base), &buf[0], uintptr(r.size), &rd) != nil && rd == 0 {
			continue
		}
		for p := uint64(0); p+8 <= uint64(rd); p += 8 {
			if le.Uint64(buf[p:]) != want {
				continue
			}
			c := r.base + p
			if who, _ := rd32(h, c+0x130); who == 3 {
				continue
			}
			out = append(out, c)
		}
	}
	return out
}

// itemsBySlot: slot hash -> item hash of an outfit table.
func itemsBySlot(h windows.Handle, items uint64) map[uint32]uint32 {
	out := map[uint32]uint32{}
	n, ok := rd32(h, items-4)
	if !ok {
		return out
	}
	n &= 0x7fffffff
	if n > 256 {
		return out
	}
	for i := uint64(0); i < uint64(n); i++ {
		if item, slot, ok := readEnt(h, items+i*24+16); ok {
			out[slot] = item
		}
	}
	return out
}

// sameOutfit: how many of t's slots hold the same item in both outfits, and how many were compared.
func sameOutfit(t liveTask, a, b map[uint32]uint32) (same, of int) {
	for _, sl := range t.slots {
		x, ok1 := a[sl]
		y, ok2 := b[sl]
		if !ok1 || !ok2 {
			continue
		}
		of++
		if x == y {
			same++
		}
	}
	return
}

// previewLoad writes the fit into the Skaters page's preview. worldBefore is the world skater's outfit before the
// load (to tell that the menu shows this preset). It reports what it did; nothing is written when anything is off.
func previewLoad(h windows.Handle, pid uint32, skaterTask, boardTask *liveTask, worldBefore map[uint32]uint32) string {
	if !previewReady() {
		return "not known yet"
	}
	parts := freshPreviewParts(h, pid)
	if len(parts) == 0 {
		preview.Lock()
		parts = append([]uint64(nil), preview.parts...)
		preview.Unlock()
	}
	var bp uint64
	var skaters []uint64
	for _, c := range parts {
		p1, _ := rd64(h, c+8)
		res, _ := rd64(h, p1+0x90)
		key, _ := rd32(h, res&^4+0x38)
		handle, _ := rd64(h, c+0x170)
		if key == boardTemplateKey {
			if handle > 0xffffffff {
				if bp != 0 {
					return "more than one preview board"
				}
				bp = c
			}
		} else if handle == 0 {
			skaters = append(skaters, c)
		}
	}
	if bp == 0 {
		return "no preview board"
	}
	elB, err := modelElement(h, bp)
	if err != nil {
		return "the preview's list wasn't found: " + err.Error()
	}
	elS := elB - 0x20 // item 0 of the same list
	sItems, _ := rd64(h, elS+8)
	bItems, _ := rd64(h, elB+8)
	menuSkater := itemsBySlot(h, sItems)
	if skaterTask != nil {
		// The page shows the selected preset (moving between presets there selects them); this only makes sure the
		// list is a copy of this skater's outfit at all (it can differ from the world's in a few slots: 47 of 55
		// were the same in a test, the page having copied the preset when it opened)
		if same, of := sameOutfit(*skaterTask, menuSkater, worldBefore); of == 0 || same*2 < of {
			return fmt.Sprintf("the menu shows another preset (%d of %d slots the same)", same, of)
		}
	}
	// the preview skater: of the parts without a handle, the one whose outfit is most like the menu list's (it can
	// differ from it in a few slots: items the game shows a default for); at least half the slots the same
	var sp uint64
	var spNote []string
	if skaterTask != nil {
		best := 0
		for _, c := range skaters {
			own, _ := rd64(h, c+0x158)
			same, of := sameOutfit(*skaterTask, itemsBySlot(h, own), menuSkater)
			spNote = append(spNote, fmt.Sprintf("%#x %d/%d", c, same, of))
			if of > 0 && same*2 >= of && same > best {
				sp, best = c, same
			}
		}
		if sp == 0 {
			return fmt.Sprintf("no preview skater wears the menu's outfit (%v)", spNote)
		}
	}
	var did []string
	var flag []uint64
	if skaterTask != nil {
		writeTable(h, *skaterTask, tableEntries(h, *skaterTask, sItems))
		own, _ := rd64(h, sp+0x158)
		writeTable(h, *skaterTask, tableEntries(h, *skaterTask, own))
		flag = append(flag, sp)
		did = append(did, "skater")
	}
	if boardTask != nil {
		writeTable(h, *boardTask, tableEntries(h, *boardTask, bItems))
		flag = append(flag, bp)
		did = append(did, "board")
	}
	one := []byte{1}
	var w uintptr
	for _, c := range flag {
		windows.WriteProcessMemory(h, uintptr(c+0x10a), &one[0], 1, &w)
		windows.WriteProcessMemory(h, uintptr(c+0x10d), &one[0], 1, &w)
	}
	var shown []string
	all := true
	for wait := time.Now(); time.Since(wait) < 300*time.Millisecond; time.Sleep(15 * time.Millisecond) {
		shown, all = nil, true
		if skaterTask != nil {
			ok, wrong := shows(h, *skaterTask, sp)
			shown = append(shown, fmt.Sprintf("skater %v (%d not yet)", ok, wrong))
			all = all && ok
		}
		if boardTask != nil {
			ok, wrong := shows(h, *boardTask, bp)
			shown = append(shown, fmt.Sprintf("board %v (%d not yet)", ok, wrong))
			all = all && ok
		}
		if all {
			break
		}
	}
	previewShows.Store(all)
	return fmt.Sprintf("written (%v; preview skater %#x of %v, board %#x, list items %#x); shows it: %v", did, sp, spNote, bp, elS, shown)
}
