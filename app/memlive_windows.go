//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Changing what the running game wears, in its memory. An outfit is a table of 24-byte entries: a name pointer, a
// settings pointer, the item hash (u32) and the slot hash (u32); both hashes are the djb2-xor hash of the name, and
// an empty slot is item 0. A load writes the fit where the game itself takes it from: the preset's record in the
// game's loadout manager (what the game saves through ReSkate and shows again after a map change or on a server),
// and the data model the skater and the board copy their outfits from (modelload_windows.go). The game's save file
// isn't touched here (see pending.go).

type liveTask struct {
	preset  string
	group   int
	slots   []uint32
	old     []uint32       // what the file says is there
	next    []uint32       // what should be there: the new fit's item for each of the old slots (0 if it has none)
	keep    []bool         // slots left as the game has them (face and body, unless asked for; items not installed)
	slotIdx map[uint32]int // slot hash -> position in the file's order
	oldName []string       // the file's item names, for the log
	newName []string
	params  [][]uint32 // the new fit's settings (colors, options) for each of the old slots: its parameter words
}

// An entry of a table in the game: where its item hash is, which of the task's slots it is, and the item it holds.
type liveEnt struct {
	addr uint64
	idx  int
	item uint32
}

type memRegion struct{ base, size, alloc uint64 }

const (
	loadoutManagerOffset    = 0x71ec8c8 // build 20260929: Skate.exe's pointer to the game's loadout manager
	boardTemplateKey        = 0x5e61aaef
	cosmeticComponentVtable = 0x6084c88 // build 20260929: the item component's vtable, from Skate.exe's base
)

func liveTasks(old, next []StoredPreset, body, force bool) ([]liveTask, []string) {
	var tasks []liveTask
	var notes []string
	byID := map[string]StoredPreset{}
	for _, p := range old {
		byID[p.PresetID] = p
	}
	for _, q := range next {
		p, ok := byID[q.PresetID]
		if !ok || (p.RecipesJSON == q.RecipesJSON && !force) {
			continue
		}
		og, err1 := parseRecipes(p.RecipesJSON)
		ng, err2 := parseRecipes(q.RecipesJSON)
		if err1 != nil || err2 != nil || len(og) != len(ng) {
			notes = append(notes, "One preset is laid out differently in the game, so it was left as it is.")
			continue
		}
		for gi := range og {
			a, b := og[gi].Items, ng[gi].Items
			if len(a) == 0 {
				continue
			}
			bySlot := map[uint64]uint32{}
			nameBySlot := map[uint64]string{}
			paramsBySlot := map[uint64][]uint32{}
			for _, it := range b {
				var words []uint32
				if len(it.ParameterBit) > 0 {
					json.Unmarshal(it.ParameterBit, &words)
				}
				paramsBySlot[it.Slot] = words
				bySlot[it.Slot] = itemHash(it.Asset)
				nameBySlot[it.Slot] = it.Asset
			}
			t := liveTask{preset: q.PresetID, group: gi}
			changed := false
			for _, it := range a {
				n := bySlot[it.Slot] // 0 when the new fit has nothing for this slot
				keep := !liveSlotIncluded(gi, it.Slot, body)
				if gi != 1 && itemIsMissing(nameBySlot[it.Slot]) && !keep {
					// not installed: the game has no such item, so its hash would make ReSkate reject the preset when
					// the game saves it (it can't name it). ReSkate shows the slot's default in its place. For the
					// slots that can be empty (headwear, eyewear, gloves, accessories) that's nothing: the slot is
					// emptied, so a piece from the fit before doesn't stay on. The others keep what's there.
					if optionalSlot(it.Slot) {
						n = 0
						notes = append(notes, nameBySlot[it.Slot]+" isn't installed, so that slot is left empty.")
					} else {
						keep = true
						notes = append(notes, nameBySlot[it.Slot]+" isn't installed, so that piece stays as it is.")
					}
				}
				if gi == 1 && itemIsMissing(nameBySlot[it.Slot]) && !keep {
					// the game would show its default and the board can't be built from the raw item: leave that part
					keep = true
					notes = append(notes, nameBySlot[it.Slot]+" isn't installed, so that part of the board stays as it is.")
				}
				t.oldName = append(t.oldName, it.Asset)
				t.newName = append(t.newName, nameBySlot[it.Slot])
				t.slots = append(t.slots, uint32(it.Slot))
				t.old = append(t.old, itemHash(it.Asset))
				t.next = append(t.next, n)
				t.params = append(t.params, paramsBySlot[it.Slot])
				t.keep = append(t.keep, keep)
				if !keep && n != itemHash(it.Asset) {
					changed = true
				}
			}
			if !changed && !force {
				continue
			}
			t.slotIdx = map[uint32]int{}
			for i, sl := range t.slots {
				t.slotIdx[sl] = i
			}
			tasks = append(tasks, t)
		}
	}
	return tasks, notes
}

// findSkatePID: the game's process. Listing the processes takes milliseconds and loads ask for it many times, so the
// answer is kept for half a second.
var pidCache struct {
	sync.Mutex
	pid uint32
	at  time.Time
}

func findSkatePID() uint32 {
	pidCache.Lock()
	defer pidCache.Unlock()
	if time.Since(pidCache.at) < 500*time.Millisecond {
		return pidCache.pid
	}
	pidCache.pid, pidCache.at = findSkatePIDNow(), time.Now()
	return pidCache.pid
}

func findSkatePIDNow() uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "Skate.exe") {
			return e.ProcessID
		}
	}
	return 0
}

func writableRegions(h windows.Handle) []memRegion {
	var out []memRegion
	var addr uintptr
	for {
		var mbi windows.MemoryBasicInformation
		if err := windows.VirtualQueryEx(h, addr, &mbi, unsafe.Sizeof(mbi)); err != nil {
			break
		}
		const commit, guard = 0x1000, 0x100
		if mbi.State == commit && mbi.Protect&guard == 0 && mbi.Type == 0x20000 && mbi.Protect&(0x04|0x08|0x40|0x80) != 0 { // private memory only: the tables live on the heap
			out = append(out, memRegion{uint64(mbi.BaseAddress), uint64(mbi.RegionSize), uint64(mbi.AllocationBase)})
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr {
			break
		}
		addr = next
	}
	return out
}

func readEnt(h windows.Handle, addr uint64) (item, slot uint32, ok bool) {
	var b [8]byte
	var n uintptr
	if windows.ReadProcessMemory(h, uintptr(addr), &b[0], 8, &n) != nil || n != 8 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint32(b[:]), binary.LittleEndian.Uint32(b[4:]), true
}

// patchLiveMemory puts next on in the running game for every preset that differs from old (what the game has
// loaded). Returns a sentence for the person.
func patchLiveMemory(old, next []StoredPreset, body, sweep bool) (string, error) {
	if sweep {
		return "", nil // (nothing to do after a load: the game puts the fit on itself)
	}
	modelNotShown.Store("")
	checkItems(recipeItemNames(old, next))
	tasks, notes := liveTasks(old, next, body, false)
	if len(tasks) == 0 {
		return "nothing differs", nil
	}
	pid := findSkatePID()
	if pid == 0 {
		return "", errors.New("skate. isn't running")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.PROCESS_VM_WRITE|windows.PROCESS_VM_OPERATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("couldn't open skate. to change it: %v", err)
	}
	defer windows.CloseHandle(h)
	return modelLoad(h, pid, tasks, notes)
}

// Writes the new item into each entry that isn't already it.
func writeTable(h windows.Handle, t liveTask, ents []liveEnt) bool {
	ok := true
	pid := findSkatePID()
	for _, e := range ents {
		if t.keep[e.idx] {
			continue
		}
		// The name pointer: the game's empty string. ReSkate's save turns an entry back into a name by its name
		// pointer, and when that name is empty it looks the hash up in its own item catalog (ReSkate.dll, the check
		// that logs invalid_native_recipe). An old item's name left in front of a new hash fails that check; an
		// empty name never does, and the empty string is the game's own, never freed (the components' tables use
		// it for every item). Entries with the right hash but an old name (from earlier loads) are put right too.
		cur, _ := rd64(h, e.addr-16)
		if p := emptyName(h, pid); p != 0 && cur != p {
			if e.item != t.next[e.idx] || (cur>>32 != 1 && nameAt(h, cur) != "" && itemHash(nameAt(h, cur)) != e.item) {
				putPointer(h, e.addr-16, p)
			}
		}
		if e.idx < len(t.params) {
			writeParams(h, pid, t, e)
		}
		if e.item == t.next[e.idx] {
			continue
		}
		var v [4]byte
		binary.LittleEndian.PutUint32(v[:], t.next[e.idx])
		var w uintptr
		if windows.WriteProcessMemory(h, uintptr(e.addr), &v[0], 4, &w) != nil || w != 4 {
			ok = false
		}
	}
	return ok
}

func rd64(h windows.Handle, a uint64) (uint64, bool) {
	var b [8]byte
	var n uintptr
	if a < 0x10000 || windows.ReadProcessMemory(h, uintptr(a), &b[0], 8, &n) != nil || n != 8 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(b[:]), true
}

func rd32(h windows.Handle, a uint64) (uint32, bool) {
	var b [4]byte
	var n uintptr
	if a < 0x10000 || windows.ReadProcessMemory(h, uintptr(a), &b[0], 4, &n) != nil || n != 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b[:]), true
}

// moduleBase: where Skate.exe sits in the game's memory. It never moves while the game runs, and finding it means
// listing the game's modules (milliseconds each time), so it's remembered per game process.
var baseCache sync.Map // pid -> uint64

func moduleBase(pid uint32) uint64 {
	if v, ok := baseCache.Load(pid); ok {
		return v.(uint64)
	}
	b := moduleBaseNow(pid)
	if b != 0 {
		baseCache.Store(pid, b)
	}
	return b
}

func moduleBaseNow(pid uint32) uint64 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, pid)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ModuleEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if windows.Module32First(snap, &e) != nil {
		return 0
	}
	// the first module is the executable
	return uint64(e.ModBaseAddr)
}

// The runs of pages in [base, base+size) that are in the game's working set.
func residentRuns(h windows.Handle, base, size uint64) [][2]uint64 {
	type wsEx struct{ addr, attrs uintptr }
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32QueryWorkingSetEx")
	const page, batch = 4096, 16384
	var runs [][2]uint64
	var runStart uint64
	inRun := false
	flush := func(end uint64) {
		if inRun {
			runs = append(runs, [2]uint64{runStart, end - runStart})
			inRun = false
		}
	}
	for off := uint64(0); off < size; off += batch * page {
		pages := (size - off + page - 1) / page
		if pages > batch {
			pages = batch
		}
		arr := make([]wsEx, pages)
		for i := range arr {
			arr[i].addr = uintptr(base + off + uint64(i)*page)
		}
		ok, _, _ := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&arr[0])), uintptr(len(arr))*unsafe.Sizeof(arr[0]))
		for i := range arr {
			at := base + off + uint64(i)*page
			valid := ok == 0 || arr[i].attrs&1 == 1 // if the question fails, treat every page as in use
			if valid && !inRun {
				runStart, inRun = at, true
			} else if !valid {
				flush(at)
			}
		}
	}
	flush(base + size)
	// the last page may run past the region
	for i := range runs {
		if end := runs[i][0] + runs[i][1]; end > base+size {
			runs[i][1] -= end - (base + size)
		}
	}
	return runs
}

func findComponents(h windows.Handle, want uint64) []uint64 {
	// the components sit in the biggest blocks: look there first, and everywhere only if they are not
	// (they have always sat in a block of 512 MB, so that is where to look first)
	for _, r := range [][2]uint64{{480 << 20, 600 << 20}, {128 << 20, 0}, {64 << 10, 0}} {
		if c := findComponentsIn(h, want, r[0], r[1]); len(c) > 0 {
			return c
		}
	}
	return nil
}

// residentRegions splits the regions of at least minSize into the runs of pages the game really holds in memory;
// reading the rest (paged out or never touched) is slow and has nothing in it to find.
func residentRegions(h windows.Handle, minSize, maxSize uint64, byAlloc bool) (out []memRegion, mb uint64) {
	all := writableRegions(h)
	total := map[uint64]uint64{} // allocation -> its committed size
	for _, r := range all {
		total[r.alloc] += r.size
	}
	for _, r := range all {
		size := r.size
		if byAlloc {
			size = total[r.alloc]
		}
		if size < minSize || (maxSize != 0 && size > maxSize) {
			continue
		}
		for _, run := range residentRuns(h, r.base, r.size) {
			out = append(out, memRegion{base: run[0], size: run[1]})
			mb += run[1] >> 20
		}
	}
	return out, mb
}

func findComponentsIn(h windows.Handle, want uint64, minSize, maxSize uint64) []uint64 {
	start := time.Now()
	regs, mbAll := residentRegions(h, minSize, maxSize, false)
	var mu sync.Mutex
	var comps []uint64
	sizes := map[uint64]uint64{} // component -> size in MB of the block it sits in
	jobs := make(chan memRegion)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer drainOnPanic("memory scan", jobs)
			const chunk = 4 << 20
			buf := make([]byte, chunk)
			le := binary.LittleEndian
			for r := range jobs {
				for off := uint64(0); off < r.size; off += chunk {
					n := uint64(chunk)
					if off+n > r.size {
						n = r.size - off
					}
					var rd uintptr
					if windows.ReadProcessMemory(h, uintptr(r.base+off), &buf[0], uintptr(n), &rd) != nil && rd == 0 {
						continue
					}
					var pat [8]byte
					le.PutUint64(pat[:], want)
					for from := 0; from+8 <= int(rd); {
						i := bytes.Index(buf[from:rd], pat[:])
						if i < 0 {
							break
						}
						p := uint64(from + i)
						if (r.base+off+p)%8 == 0 {
							mu.Lock()
							comps = append(comps, r.base+off+p)
							sizes[r.base+off+p] = r.size >> 20 // of the resident run
							mu.Unlock()
						}
						from = int(p) + 1
					}
				}
			}
		}()
	}
	mb := mbAll
	for _, r := range regs {
		jobs <- r
	}
	close(jobs)
	wg.Wait()
	sort.Slice(comps, func(i, j int) bool { return comps[i] < comps[j] })
	tl.logf("LIVE: components: %d item components found in %d MB, %.1f s (blocks of %d to %d MB)", len(comps), mb, time.Since(start).Seconds(), minSize>>20, maxSize>>20)
	for _, c := range comps {
		tl.logf("LIVE: component %#x sits in a block of %d MB", c, sizes[c])
	}
	return comps
}

// The cosmetic parts found (where they sit), for the game process and level they were found in: a map change or a
// server builds the skater again, and parts found before may be freed (and still look right).
var (
	compMu    sync.Mutex
	compPID   uint32
	compCache []uint64
	compGen   int64
)

func allCosmeticComponents(h windows.Handle, pid uint32) []uint64 {
	base := moduleBase(pid)
	if base == 0 {
		return nil
	}
	want := base + cosmeticComponentVtable
	compMu.Lock()
	if compPID != pid || compGen != profileApplied.Load() {
		// a map change or a server builds the skater again: parts found before may be freed (and still look right)
		compPID, compCache, compGen = pid, nil, profileApplied.Load()
	}
	cached := append([]uint64(nil), compCache...)
	compMu.Unlock()
	var out []uint64
	for _, c := range cached {
		if v, ok := rd64(h, c); ok && v == want {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		out = findComponents(h, want)
		compMu.Lock()
		compPID, compCache, compGen = pid, append([]uint64(nil), out...), profileApplied.Load() // once per set of parts
		compMu.Unlock()
	}
	return out
}

// Writes down where in the game's memory the tables were, to see whether they sit together.
// Diagnostic: a file named only_blocks in the app's data folder lists block sizes (in MB class: 2, 68, 200, 256, 512)
// and the skater tables in other blocks are left alone. It tells which block holds the copy the game reads.
func blockClass(regs []memRegion, addr uint64) int {
	i := sort.Search(len(regs), func(i int) bool { return regs[i].base+regs[i].size > addr })
	if i >= len(regs) || regs[i].base > addr {
		return 0
	}
	var mb uint64
	for _, r := range regs {
		if r.alloc == regs[i].alloc {
			mb += r.size >> 20
		}
	}
	switch {
	case mb <= 4:
		return 2
	case mb < 100:
		return 68
	case mb < 230:
		return 200
	case mb < 300:
		return 256
	}
	return 512
}

func patchPresetList(h windows.Handle, pid uint32, tasks []liveTask, forceIdx int) {
	base := moduleBase(pid)
	mgr, _ := rd64(h, base+loadoutManagerOffset)
	begin, ok1 := rd64(h, mgr+0x18)
	end, ok2 := rd64(h, mgr+0x20)
	if mgr == 0 || !ok1 || !ok2 || begin >= end || (end-begin)%0x48 != 0 || (end-begin)/0x48 > 10 {
		tl.logf("LIVE: preset list: not found")
		return
	}
	n := int((end - begin) / 0x48)
	// The manager holds the presets' records up to the highest one shown this session, in preset order (ReSkate reads
	// them so too: worn_items.cpp). A preset not shown yet has no record: the game makes it from ReSkate's profile when
	// it's first shown, and the app puts the fit on then (watchSelection).
	for _, t := range tasks {
		idx := -1
		for i, id := range presetIDs {
			if id == t.preset {
				idx = i
			}
		}
		if forceIdx >= 0 {
			idx = forceIdx
		}
		if idx < 0 || idx >= n {
			tl.logf("LIVE: preset list: %s group %d not written (the list holds %d presets)", t.preset, t.group, n)
			continue
		}
		rec := begin + uint64(idx)*0x48
		vb, _ := rd64(h, rec)
		ve, _ := rd64(h, rec+8)
		best, bestHits := []liveEnt(nil), 0
		for p := vb; p < ve && p-vb < 0x28*16; p += 0x28 {
			items, _ := rd64(h, p+16)
			cnt, _ := rd32(h, items-4)
			c := uint64(cnt & 0x7fffffff)
			if items == 0 || c == 0 || c > 80 {
				continue
			}
			var ents []liveEnt
			for i := uint64(0); i < c; i++ {
				ad := items + i*24 + 16
				item, slot, ok := readEnt(h, ad)
				if !ok {
					continue
				}
				if ix, in := t.slotIdx[slot]; in {
					ents = append(ents, liveEnt{ad, ix, item})
				}
			}
			if len(ents) > bestHits {
				best, bestHits = ents, len(ents)
			}
		}
		if bestHits < 3 || bestHits*2 < len(t.slots) && bestHits < 5 {
			tl.logf("LIVE: preset list: %s group %d: no table of its slots in the record (%d at best)", t.preset, t.group, bestHits)
			continue
		}
		changed := 0
		for _, e := range best {
			if !t.keep[e.idx] && e.item != t.next[e.idx] {
				changed++
			}
		}
		ok := writeTable(h, t, best)
		tl.logf("LIVE: preset list: %s (entry %d of %d) group %d: %d of %d entries changed (ok %v)", t.preset, idx, n, t.group, changed, len(best), ok)
	}
}

// writePresetRecord writes a load's fit into the game's preset record idx, for the switch flow: done after the game
// has switched away from the preset (switching away saves the preset from the skater on screen, over the record).
func writePresetRecord(old, next []StoredPreset, body bool, idx int) error {
	pid := findSkatePID()
	if pid == 0 {
		return errors.New("skate. isn't running")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.PROCESS_VM_WRITE|windows.PROCESS_VM_OPERATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	tasks, _ := liveTasks(old, next, body, true)
	if len(tasks) == 0 {
		return errors.New("nothing to write")
	}
	patchPresetList(h, pid, tasks, idx)
	return nil
}

// optionalSlot: a clothing slot that can be empty (headwear, eyewear, gloves, accessories).
func optionalSlot(slot uint64) bool {
	switch skaterSlots[slot] {
	case "Headwear", "Face", "Gloves", "Accessory":
		return true
	}
	return false
}
