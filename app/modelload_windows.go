package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// ---- a load through the game's data model -----------------------------------------------------------------------
//
// Where the skater and the board take their outfits from (read from Skate.exe, confirmed with the model probe):
// each is a cosmetic part (a component; the local player's have 3 at +0x130). When a part is flagged as changed
// (+0x10a, and +0x10d for the rebuild), the game's update of it (0x140523130) looks its handle (+0x170) up in the
// data model manager it holds (+0x1a8) and copies the outfit it finds into itself (+0x150), then builds it again.
// For the local player both handles lead to one list of outfits in the model: the skater's is its item 0, the
// board's its item 1. A load writes the fit there and flags the two parts: the game puts it on itself, the same way
// after a map change or on a server. The game's preset record is written too (patchPresetList), so whenever the
// game publishes the preset again or saves it (through ReSkate), it's this fit.
//
// An entry of an outfit table is 24 bytes: name pointer, settings pointer (an array of 32-bit words, its count in
// front), item hash, slot hash. Names are the game's empty string (ReSkate names the item by its hash, see
// writeTable); settings are written in place (a slot's settings have the same shape whatever fills it), or point at
// the game's empty array when the new item has none.

const modelLoadOn = true

// emptyArrayOffset: the game's empty array (what Skate.exe 0x143dcbb00 returns), never freed.
const emptyArrayOffset = 0x64d4880

// localParts: the local player's skater and board parts.
func localParts(h windows.Handle, pid uint32) (skater, board uint64, err error) {
	var sk, bd []uint64
	for _, c := range allCosmeticComponents(h, pid) {
		if who, _ := rd32(h, c+0x130); who != 3 {
			continue // another player's
		}
		p1, _ := rd64(h, c+8)
		res, _ := rd64(h, p1+0x90)
		if key, _ := rd32(h, res&^4+0x38); key == boardTemplateKey {
			bd = append(bd, c)
		} else {
			sk = append(sk, c)
		}
	}
	if len(sk) != 1 || len(bd) != 1 {
		return 0, 0, fmt.Errorf("found %d skater and %d board parts of the local player, expected one each", len(sk), len(bd))
	}
	return sk[0], bd[0], nil
}

// modelElement: the outfit in the data model that part c copies from, looked up as the game does (see modelprobe).
func modelElement(h windows.Handle, c uint64) (uint64, error) {
	mgr, _ := rd64(h, c+0x1a8)
	handle, _ := rd64(h, c+0x170)
	v, size, err := modelValue(h, mgr, handle)
	if err != nil {
		return 0, err
	}
	if size != 0x20 {
		return 0, fmt.Errorf("not an outfit in a list (item size %#x)", size)
	}
	return v, nil
}

// modelValue: the address of a data model value, looked up as the game does (0x1419190b0): a handle up to 32 bits
// is remapped (0x14191ce20); the node and field descriptor (0x141915650); the value is the node's data plus the
// field's offset, or, for an item of a list, the list's item (0x14191f120). size is the list item's size (0 for a
// value that isn't in a list).
func modelValue(h windows.Handle, mgr, handle uint64) (addr, size uint64, err error) {
	le := binary.LittleEndian
	if mgr < 0x10000 || handle == 0 {
		return 0, 0, fmt.Errorf("no model manager or handle (%#x, %#x)", mgr, handle)
	}
	if handle <= 0xffffffff {
		buckets, _ := rd64(h, mgr+0x588)
		n, _ := rd32(h, mgr+0x590)
		resolved := uint64(0)
		if buckets != 0 && n != 0 && n < 1<<24 {
			end, _ := rd64(h, buckets+uint64(n)*8)
			node, _ := rd64(h, buckets+(handle%uint64(n))*8)
			for i := 0; node != 0 && i < 1000; i++ {
				if k, _ := rd64(h, node); k == handle {
					if node != end {
						resolved, _ = rd64(h, node+8)
					}
					break
				}
				node, _ = rd64(h, node+0x10)
			}
		}
		if resolved <= 0xffffffff {
			return 0, 0, fmt.Errorf("handle %#x not remapped", handle)
		}
		handle = resolved
	}
	rel := handle - (1 << 32)
	idx, field, gen := uint32(rel>>16), uint32(handle&0xffff), uint16(rel>>48)
	vb, _ := rd64(h, mgr+0x98)
	ve, _ := rd64(h, mgr+0xa0)
	if ve <= vb || uint64(idx) >= (ve-vb)/40 {
		return 0, 0, fmt.Errorf("node %d outside the model's nodes", idx)
	}
	node := vb + uint64(idx)*40
	var nb [40]byte
	var rd uintptr
	if windows.ReadProcessMemory(h, uintptr(node), &nb[0], 40, &rd) != nil || rd != 40 {
		return 0, 0, errors.New("node unreadable")
	}
	if g := le.Uint16(nb[6:]); g != gen {
		return 0, 0, fmt.Errorf("node %d is generation %d, the handle %d (stale)", idx, g, gen)
	}
	data := le.Uint64(nb[8:])
	typeIdx := uint64(le.Uint16(nb[0x18:]))
	elem := le.Uint32(nb[0x1c:])
	types, _ := rd64(h, mgr+0x600)
	descs, _ := rd64(h, mgr+0x620)
	t := types + typeIdx*72
	first, _ := rd32(h, t)
	nf, _ := rd32(h, t+4)
	if field >= nf {
		return 0, 0, fmt.Errorf("field %d outside the type's %d", field, nf)
	}
	off, _ := rd32(h, descs+uint64(first+field)*8)
	if data == 0 {
		return 0, 0, fmt.Errorf("node %d has no data", idx)
	}
	if elem == 0xffffffff {
		return data + uint64(off), 0, nil
	}
	tvb, _ := rd64(h, t+8)
	tobj, _ := rd64(h, tvb)
	tinfo, _ := rd64(h, tobj)
	kind, _ := rd32(h, tinfo+4)
	var ab [1]byte
	windows.ReadProcessMemory(h, uintptr(tinfo+0x28), &ab[0], 1, &rd)
	size = uint64(kind >> 16)
	if kind&0x3e0 == 0x360 {
		size = 8
	}
	if a := uint64(ab[0]); a > 0 && size%a != 0 {
		size += a - size%a
	}
	begin, _ := rd64(h, data)
	n, _ := rd32(h, begin-4)
	n &= 0x7fffffff
	if size == 0 || elem >= n {
		return 0, 0, fmt.Errorf("item %d outside the list of %d", elem, n)
	}
	return begin + size*uint64(elem) + uint64(off), size, nil
}

// The preset the game has selected: the native getter 0x1405300d0 reads it from the data model, through the
// loadout manager's model at +0x198 and its handle at +0x1b0. The manager holds the presets' records up to the
// highest one shown, in order.

// selectionReader reads the selection often (every 50 ms) with one handle on the game, looking for the game's
// process only once a second.
type selectionReader struct {
	pid     uint32
	h       windows.Handle
	base    uint64
	checked time.Time
}

func (r *selectionReader) Read() (pid uint32, sel, records int, err error) {
	if time.Since(r.checked) > time.Second || r.pid == 0 {
		r.checked = time.Now()
		if p := findSkatePID(); p != r.pid {
			r.Close()
			if p != 0 {
				h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, p)
				if err != nil {
					return 0, -1, 0, err
				}
				r.pid, r.h, r.base = p, h, moduleBase(p)
			}
		}
	}
	if r.pid == 0 {
		return 0, -1, 0, errors.New("skate. isn't running")
	}
	sel, records, err = gameSelectionIn(r.h, r.base)
	return r.pid, sel, records, err
}

func (r *selectionReader) Close() {
	if r.h != 0 {
		windows.CloseHandle(r.h)
	}
	r.pid, r.h = 0, 0
}

func gameSelectionIn(h windows.Handle, base uint64) (sel, records int, err error) {
	mgr, _ := rd64(h, base+loadoutManagerOffset)
	if mgr < 0x10000 {
		return -1, 0, errors.New("no loadout manager")
	}
	begin, _ := rd64(h, mgr+0x18)
	end, _ := rd64(h, mgr+0x20)
	if end >= begin && (end-begin)%0x48 == 0 {
		records = int((end - begin) / 0x48)
	}
	model, _ := rd64(h, mgr+0x198)
	handle, _ := rd64(h, mgr+0x1b0)
	v, _, err := modelValue(h, model, handle)
	if err != nil {
		return -1, records, err
	}
	s, ok := rd32(h, v)
	if !ok || s >= 10 {
		return -1, records, fmt.Errorf("selected preset unreadable (%d)", s)
	}
	return int(s), records, nil
}

// writeParams puts the new fit's settings for entry e (e.addr is its item hash) in place.
var paramNotes sync.Map

func writeParams(h windows.Handle, pid uint32, t liveTask, e liveEnt) {
	want := t.params[e.idx]
	pp := e.addr - 8
	cur, ok := rd64(h, pp)
	if !ok || cur < 0x10000 {
		return
	}
	n, _ := rd32(h, cur-4)
	n &= 0x7fffffff
	base := moduleBase(pid)
	if len(want) == 0 {
		if n != 0 && base != 0 {
			putPointer(h, pp, base+emptyArrayOffset)
		}
		return
	}
	if int(n) != len(want) {
		key := fmt.Sprintf("%d/%d", t.group, t.slots[e.idx])
		if _, seen := paramNotes.LoadOrStore(key, true); !seen {
			tl.logf("LIVE: settings of slot %d (group %d, %s) not written: the game holds %d words there, the fit %d",
				t.slots[e.idx], t.group, t.newName[e.idx], n, len(want))
		}
		return
	}
	buf := make([]byte, 4*len(want))
	var rd uintptr
	if windows.ReadProcessMemory(h, uintptr(cur), &buf[0], uintptr(len(buf)), &rd) != nil || int(rd) != len(buf) {
		return
	}
	same := true
	for i, w := range want {
		if binary.LittleEndian.Uint32(buf[i*4:]) != w {
			same = false
		}
		binary.LittleEndian.PutUint32(buf[i*4:], w)
	}
	if !same {
		var wr uintptr
		windows.WriteProcessMemory(h, uintptr(cur), &buf[0], uintptr(len(buf)), &wr)
	}
}

// tableEntries: the entries of the outfit table at items that are slots of t.
func tableEntries(h windows.Handle, t liveTask, items uint64) []liveEnt {
	n, ok := rd32(h, items-4)
	if !ok {
		return nil
	}
	n &= 0x7fffffff
	if n == 0 || n > 256 {
		return nil
	}
	var out []liveEnt
	for i := uint64(0); i < uint64(n); i++ {
		ad := items + i*24 + 16
		item, slot, ok := readEnt(h, ad)
		if !ok {
			continue
		}
		if ix, in := t.slotIdx[slot]; in {
			out = append(out, liveEnt{ad, ix, item})
		}
	}
	return out
}

// shows: whether part c's own outfit holds the fit for t (every slot the load changes).
func shows(h windows.Handle, t liveTask, c uint64) (bool, int) {
	own, _ := rd64(h, c+0x158)
	ents := tableEntries(h, t, own)
	wrong := 0
	for _, e := range ents {
		if !t.keep[e.idx] && e.item != t.next[e.idx] {
			wrong++
		}
	}
	return len(ents) > 0 && wrong == 0, wrong
}

func modelLoad(h windows.Handle, pid uint32, tasks []liveTask, notes []string) (string, error) {
	st := time.Now()
	modelNotShown.Store("")
	patchPresetList(h, pid, tasks, -1)
	tList := time.Since(st)
	selID, _ := liveSelected()
	skater, board, err := localParts(h, pid)
	if err == nil {
		_, e1 := modelElement(h, skater)
		_, e2 := modelElement(h, board)
		if e1 != nil || e2 != nil {
			// parts remembered from before the game built the skater again: look for them afresh, once
			compMu.Lock()
			compCache = nil
			compMu.Unlock()
			skater, board, err = localParts(h, pid)
		}
	}
	if err != nil {
		tl.logf("LIVE: model load: %v; the fit is in the preset list only", err)
		return "", err
	}
	var flagged []uint64
	var done []string
	var skaterTask, boardTask *liveTask
	worldBefore := map[uint32]uint32{}
	if el, err := modelElement(h, skater); err == nil {
		it, _ := rd64(h, el+8)
		worldBefore = itemsBySlot(h, it)
	}
	for i := range tasks {
		t := tasks[i]
		if t.preset == selID && t.group == 0 {
			skaterTask = &tasks[i]
		}
		if t.preset == selID && t.group == 1 {
			boardTask = &tasks[i]
		}
	}
	for _, t := range tasks {
		if t.preset != selID || t.group > 1 {
			continue // only the preset worn now is on the skater
		}
		c := skater
		what := "skater"
		if t.group == 1 {
			c, what = board, "board"
		}
		el, err := modelElement(h, c)
		if err != nil {
			tl.logf("LIVE: model load: the %s's outfit in the model wasn't found: %v; nothing written there", what, err)
			return "", fmt.Errorf("the %s's outfit wasn't found in the game (%v)", what, err)
		}
		items, _ := rd64(h, el+8)
		ents := tableEntries(h, t, items)
		if len(ents)*2 < len(t.slots) {
			tl.logf("LIVE: model load: the %s's outfit at %#x has %d of the fit's %d slots; nothing written", what, el, len(ents), len(t.slots))
			return "", fmt.Errorf("the %s's outfit in the game doesn't match the fit's slots", what)
		}
		changed := 0
		for _, e := range ents {
			if !t.keep[e.idx] && e.item != t.next[e.idx] {
				changed++
			}
		}
		ok := writeTable(h, t, ents)
		tl.logf("LIVE: model load: %s outfit at %#x (items %#x): %d of %d entries changed (ok %v)", what, el, items, changed, len(ents), ok)
		flagged = append(flagged, c)
		done = append(done, fmt.Sprintf("%s %d", what, changed))
	}
	if len(flagged) == 0 {
		return "the fit is in the preset list (the preset isn't the one worn now)", nil
	}
	one := []byte{1}
	var w uintptr
	for _, c := range flagged {
		windows.WriteProcessMemory(h, uintptr(c+0x10a), &one[0], 1, &w)
		windows.WriteProcessMemory(h, uintptr(c+0x10d), &one[0], 1, &w)
	}
	// the game copies on its next update of the parts; see that it did
	took := map[uint64]bool{}
	waited := time.Now()
	for time.Since(waited) < 1500*time.Millisecond && len(took) < len(flagged) {
		for _, t := range tasks {
			if t.preset != selID || t.group > 1 {
				continue
			}
			c := skater
			if t.group == 1 {
				c = board
			}
			if ok, _ := shows(h, t, c); ok {
				took[c] = true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	var state, notOn []string
	for _, t := range tasks {
		if t.preset != selID || t.group > 1 {
			continue
		}
		c, what := skater, "skater"
		if t.group == 1 {
			c, what = board, "board"
		}
		ok, wrong := shows(h, t, c)
		state = append(state, fmt.Sprintf("%s %v (%d parts not yet)", what, ok, wrong))
		if !ok {
			notOn = append(notOn, what)
		}
	}
	if len(notOn) > 0 {
		modelNotShown.Store(strings.Join(notOn, " and "))
	} else {
		modelNotShown.Store("")
	}
	tl.logf("LIVE: model load: parts flagged; after %d ms the game shows the fit: %s; %.0f ms in all (preset list %.0f ms, model %.0f ms)",
		time.Since(waited).Milliseconds(), strings.Join(state, ", "), float64(time.Since(st).Microseconds())/1000,
		float64(tList.Microseconds())/1000, float64((waited.Sub(st)-tList).Microseconds())/1000)
	res := "Put on through the game's data model (" + strings.Join(done, ", ") + " entries changed)."
	previewDone.Store(false)
	previewShows.Store(false)
	if liveMenuOpen.Load() {
		tp := time.Now()
		r := previewLoad(h, pid, skaterTask, boardTask, worldBefore)
		tl.logf("LIVE: model load: the Skaters page's preview (%d ms): %s", time.Since(tp).Milliseconds(), r)
		previewDone.Store(strings.HasPrefix(r, "written"))
	}
	if len(notes) > 0 {
		res += "\n" + strings.Join(notes, "\n")
	}
	return res, nil
}

// presetShowing: whether the world skater's outfit (the data model's) is recipes' clothing, nearly every slot.
func presetShowing(recipes string) bool {
	groups, err := parseRecipes(recipes)
	if err != nil || len(groups) == 0 {
		return false
	}
	pid := findSkatePID()
	if pid == 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	skater, _, err := localParts(h, pid)
	if err != nil {
		return false
	}
	el, err := modelElement(h, skater)
	if err != nil {
		return false
	}
	items, _ := rd64(h, el+8)
	got := itemsBySlot(h, items)
	same, of := 0, 0
	for _, it := range groups[0].Items {
		if _, clothing := skaterSlots[it.Slot]; !clothing || (it.Asset != "" && itemWasMissing(it.Asset)) {
			continue
		}
		v, ok := got[uint32(it.Slot)]
		if !ok {
			continue
		}
		of++
		if v == itemHash(it.Asset) {
			same++
		}
	}
	return of > 0 && same == of
}
