//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Changing what the running game wears. The game keeps the outfit of every preset as a table of
// 24-byte entries in its memory: item hash (u32), slot hash (u32), then 16 bytes of its own. Both
// hashes are the djb2-xor hash of the name, and an empty slot is item 0. Switching presets in
// the game reads these tables, so this finds the tables that hold a preset's current outfit and
// writes the new item hashes into them. The game's save file isn't touched here (see pending.go).
//
// A table is recognised by its slot hashes (all must match, in order) and most of its item hashes:
// the game swaps items it can't find for defaults, so a table isn't always exactly the file.

const liveEntry = 24

type liveTask struct {
	preset  string
	group   int
	slots   []uint32
	old     []uint32       // what the file says is there
	next    []uint32       // what should be there: the new fit's item for each of the old slots (0 if it has none)
	keep    []bool         // slots left as the game has them (face and body, unless asked for)
	anchors []int          // entries rare enough to find a table by
	gens    [][]uint32     // every outfit this preset has had in this game session, by the file's slot order
	keys    []uint64       // item|slot<<32 of entries rare enough to find a table by, from every one of them
	slotIdx map[uint32]int // slot hash -> position in the file's order
	oldName []string       // the file's item names, for the log
	newName []string
	direct  bool       // the board: written through the game's preset list, never found by scanning
	skip    []bool     // slots whose old or new item the game hasn't got: it holds a default there, so they don't count in matching
	others  [][]uint32 // what each other preset wears now in this group, by the file's slot order: a table holding one of those is theirs
}

// isOthersOutfit: the table's entries are what another preset wears now, and not what this one does. Presets pass
// outfits around, so an outfit this preset wore long ago can be exactly what another one (often the selected one)
// wears today; that table must not be taken for this preset's.
func isOthersOutfit(chk []liveEnt, t liveTask) bool {
	if len(chk) == 0 || len(t.others) == 0 {
		return false
	}
	count := func(row []uint32) int {
		n := 0
		for _, e := range chk {
			if e.idx < len(row) && e.item == row[e.idx] {
				n++
			}
		}
		return n
	}
	need := len(chk) - max(len(chk)/10, 1)
	if count(t.old) >= need {
		return false // it's this preset's own (an old copy of what the file says) as well
	}
	for _, o := range t.others {
		if count(o) >= need {
			return true
		}
	}
	return false
}

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
			for _, it := range b {
				bySlot[it.Slot] = itemHash(it.Asset)
				nameBySlot[it.Slot] = it.Asset
			}
			t := liveTask{preset: q.PresetID, group: gi}
			changed := false
			for _, it := range a {
				n := bySlot[it.Slot] // 0 when the new fit has nothing for this slot
				keep := !liveSlotIncluded(gi, it.Slot, body)
				miss := itemIsMissing(it.Asset) || itemIsMissing(nameBySlot[it.Slot])
				if gi == 1 && itemIsMissing(nameBySlot[it.Slot]) && !keep {
					// the game would show its default and the board can't be built from the raw item: leave that part
					keep = true
					notes = append(notes, nameBySlot[it.Slot]+" isn't installed, so that part of the board stays as it is.")
				}
				t.skip = append(t.skip, miss)
				t.oldName = append(t.oldName, it.Asset)
				t.newName = append(t.newName, nameBySlot[it.Slot])
				t.slots = append(t.slots, uint32(it.Slot))
				t.old = append(t.old, itemHash(it.Asset))
				t.next = append(t.next, n)
				t.keep = append(t.keep, keep)
				if !keep && n != itemHash(it.Asset) {
					changed = true
				}
			}
			for _, o := range old {
				if o.PresetID == q.PresetID {
					continue
				}
				og2, err := parseRecipes(o.RecipesJSON)
				if err != nil || gi >= len(og2) {
					continue
				}
				bySlot2 := map[uint64]uint32{}
				for _, it := range og2[gi].Items {
					bySlot2[it.Slot] = itemHash(it.Asset)
				}
				row := make([]uint32, len(t.slots))
				for j, sl := range t.slots {
					row[j] = bySlot2[uint64(sl)]
				}
				t.others = append(t.others, row)
			}
			if !changed && !force {
				continue
			}
			// the entries with the longest names: the least likely to be somewhere else by chance
			idx := make([]int, 0, len(a))
			for i := range a {
				if a[i].Asset != "" {
					idx = append(idx, i)
				}
			}
			sort.Slice(idx, func(x, y int) bool { return len(a[idx[x]].Asset) > len(a[idx[y]].Asset) })
			if len(idx) > 4 {
				idx = idx[:4]
			}
			if len(idx) == 0 {
				continue
			}
			t.anchors = idx
			t.slotIdx = map[uint32]int{}
			for i, sl := range t.slots {
				t.slotIdx[sl] = i
			}
			tasks = append(tasks, t)
		}
	}
	return tasks, notes
}

func findSkatePID() uint32 {
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

type memRegion struct{ base, size, alloc uint64 }

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

// Where tables were found last time: the memory blocks (allocations) of the game that held them. The game
// puts its copies in the same blocks, so those are looked at first. Only valid for the game process that
// made them.
// Diagnostic: after each load, count every table in the game's memory by what it holds (takes about half a minute).
const liveCensusOn = false

var liveMu sync.Mutex
var liveWorkers int
var liveCancel atomic.Bool // a new load is waiting: the background look through the rest of memory gives way

type liveHit struct {
	addr  uint64
	group int
}

var (
	hotMu    sync.Mutex
	hotPID   uint32
	hotAlloc = map[uint64]bool{}
)

func isHotAlloc(a uint64) bool {
	hotMu.Lock()
	defer hotMu.Unlock()
	return hotAlloc[a]
}

func markHot(regs []memRegion, addr uint64) {
	i := sort.Search(len(regs), func(i int) bool { return regs[i].base+regs[i].size > addr })
	if i >= len(regs) || regs[i].base > addr {
		return
	}
	hotMu.Lock()
	hotAlloc[regs[i].alloc] = true
	hotMu.Unlock()
}

type liveCand struct{ task, entry int }

// The list of blocks is kept in a small file as well, so a restart of this app while the game is still running
// doesn't throw it away. It is only a hint of where to look first: every table found is checked against what it
// should hold before anything is written, so a wrong or old list costs time, never a wrong write. It belongs to
// one run of the game (blocks are in different places each run), so it names the process and when it started,
// and is ignored for any other. It is replaced, never added to, so it stays a few hundred bytes.
var liveCachePath string

const liveWarmWorkers = 3

type hotFile struct {
	PID     uint32   `json:"pid"`
	Started int64    `json:"started"` // when that process started (Windows file time)
	Blocks  []uint64 `json:"blocks"`
}

func processStart(h windows.Handle) int64 {
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0
	}
	return c.Nanoseconds()
}

func hotCount() int {
	hotMu.Lock()
	defer hotMu.Unlock()
	return len(hotAlloc)
}

func loadHotCache(h windows.Handle, pid uint32) map[uint64]bool {
	out := map[uint64]bool{}
	if liveCachePath == "" {
		return out
	}
	b, err := os.ReadFile(liveCachePath)
	if err != nil {
		return out
	}
	var f hotFile
	started := processStart(h)
	if json.Unmarshal(b, &f) != nil || f.PID != pid || started == 0 || f.Started != started || len(f.Blocks) > 4096 {
		return out
	}
	for _, a := range f.Blocks {
		out[a] = true
	}
	tl.logf("LIVE: using the %d blocks remembered from earlier in this game run", len(out))
	return out
}

func saveHotCache(pid uint32, h windows.Handle) {
	if liveCachePath == "" {
		return
	}
	hotMu.Lock()
	f := hotFile{PID: pid, Started: processStart(h)}
	for a := range hotAlloc {
		f.Blocks = append(f.Blocks, a)
	}
	hotMu.Unlock()
	if f.Started == 0 || len(f.Blocks) == 0 || len(f.Blocks) > 4096 {
		return
	}
	sort.Slice(f.Blocks, func(i, j int) bool { return f.Blocks[i] < f.Blocks[j] })
	b, _ := json.Marshal(f)
	tmp := liveCachePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, liveCachePath)
	}
}

// How well the table at base holds task t's outfit: -1 if the slots aren't the same, else the
// number of entries whose item is the expected one. got has the table's items.
// One entry of a table found in the game's memory.
type liveEnt struct {
	addr uint64
	idx  int // position of its slot in the file's order
	item uint32
}

func readEnt(h windows.Handle, addr uint64) (item, slot uint32, ok bool) {
	var b [8]byte
	var n uintptr
	if windows.ReadProcessMemory(h, uintptr(addr), &b[0], 8, &n) != nil || n != 8 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint32(b[:]), binary.LittleEndian.Uint32(b[4:]), true
}

// Works out the table around an entry that was found by its item and slot. The game doesn't always keep
// the file's layout: it can drop or replace entries for items that aren't installed. So a table is the run
// of entries around the one found whose slots belong to this preset and come in the file's order, and each
// entry is dealt with by its slot, not by where it sits. score is how many items are as the file says.
func scoreTable(h windows.Handle, addr uint64, t liveTask) (score int, ents []liveEnt, why string) {
	item, slot, ok := readEnt(h, addr)
	if !ok {
		return -1, nil, "couldn't read the entry"
	}
	ai, ok := t.slotIdx[slot]
	if !ok {
		return -1, nil, fmt.Sprintf("slot %d isn't one of this preset's", slot)
	}
	back := []liveEnt{}
	last := ai
	for k := 1; k <= len(t.slots); k++ {
		a := addr - uint64(k*liveEntry)
		it, sl, ok := readEnt(h, a)
		if !ok {
			break
		}
		i, in := t.slotIdx[sl]
		if !in || i >= last {
			break
		}
		back = append(back, liveEnt{a, i, it})
		last = i
	}
	for i := len(back) - 1; i >= 0; i-- {
		ents = append(ents, back[i])
	}
	ents = append(ents, liveEnt{addr, ai, item})
	last = ai
	for k := 1; k <= len(t.slots); k++ {
		a := addr + uint64(k*liveEntry)
		it, sl, ok := readEnt(h, a)
		if !ok {
			break
		}
		i, in := t.slotIdx[sl]
		if !in || i <= last {
			break
		}
		ents = append(ents, liveEnt{a, i, it})
		last = i
	}
	// entries whose item the game hasn't got hold a default and can't be compared
	chk := make([]liveEnt, 0, len(ents))
	for _, e := range ents {
		if e.idx < len(t.skip) && t.skip[e.idx] {
			continue
		}
		chk = append(chk, e)
	}
	if len(chk) < 2 && !(t.group == 1 && len(chk) == 1 && chk[0].idx == 2 && len(ents) == len(t.slots)) { // a board whose deck is all there is to tell by
		return -1, ents, "too few entries the game has the items for to tell"
	}
	var best []uint32
	for _, g := range t.gens { // the best fit with any outfit this preset has had
		n := 0
		for _, e := range chk {
			if e.item == g[e.idx] {
				n++
			}
		}
		if n > score {
			score, best = n, g
		}
	}
	// A table the game has just emptied (it empties some when it rebuilds the skater, and reads them again by
	// itself later) has every entry at 0, which matches an outfit with many empty slots. Writing into one makes
	// the skater change at once, with no refresh. Leave them alone.
	if best != nil {
		want, got := 0, 0
		for _, e := range chk {
			if best[e.idx] != 0 {
				want++
				if e.item == best[e.idx] {
					got++
				}
			}
		}
		if want > 0 && got*2 < want && !boardStale(t, ents) {
			return -1, ents, fmt.Sprintf("emptied by the game (only %d of the %d items the outfit has)", got, want)
		}
	}
	// a copy of one of this preset's outfits matches it all but for an entry or two (the game swaps in defaults for
	// items it hasn't got); another preset's table, which can share a few items, must not be taken for it
	if tol := len(chk) / 10; score < len(chk)-max(tol, 1) && !boardStale(t, ents) {
		return -1, ents, fmt.Sprintf("only %d of %d entries are an outfit this preset has had", score, len(chk))
	}
	if isOthersOutfit(chk, t) {
		return -1, ents, "this is what another preset wears now, not this preset's"
	}
	if len(ents)*10 < len(t.slots)*4 { // a run of 40% or less isn't one of ours
		return -1, nil, fmt.Sprintf("only %d entries in a row belong to this preset (of %d)", len(ents), len(t.slots))
	}
	return score, ents, ""
}

// sampleTable describes a table the check refused, entry by entry: what the game holds against what the file said
// (old) and what the new fit wants (next). Only for the log.
func sampleTable(t liveTask, addr uint64, ents []liveEnt, why string) string {
	name := func(v uint32) string {
		for i := range t.slots {
			if t.old[i] == v && i < len(t.oldName) && t.oldName[i] != "" {
				return t.oldName[i]
			}
			if t.next[i] == v && i < len(t.newName) && t.newName[i] != "" {
				return t.newName[i]
			}
		}
		return "?"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entries at %#x (%s):", len(ents), addr, why)
	for _, e := range ents {
		mark := ""
		switch {
		case e.item == t.next[e.idx]:
			mark = "=next"
		case e.item == t.old[e.idx]:
			mark = "=file"
		}
		fmt.Fprintf(&b, " [%d] game %d (%s) %s, file %d, next %d;", e.idx, e.item, name(e.item), mark, t.old[e.idx], t.next[e.idx])
	}
	return b.String()
}

// Changes the outfit tables of every preset that differs between old (what the game has loaded) and
// next. Returns a sentence for the person.
func patchLiveMemory(old, next []StoredPreset, body, sweep bool) (string, error) {
	checkItems(recipeItemNames(old, next))
	tasks, notes := liveTasks(old, next, body, false)
	if len(tasks) == 0 {
		return "nothing differs", nil
	}
	return runLive(tasks, notes, old, next, sweep, false)
}

// Looks for the game's outfit tables without changing anything (every task's new outfit is its current one),
// so where they are is known before the first load. Gives way to a load. Returns whether every outfit group was found.
func warmLiveMemory(cur []StoredPreset) (bool, error) {
	tasks, notes := liveTasks(cur, cur, false, true)
	if len(tasks) == 0 {
		return false, errors.New("nothing to look for")
	}
	_, err := runLive(tasks, notes, cur, cur, false, true)
	return liveWarmFound.Load(), err
}

var liveWarmFound atomic.Bool

func runLive(tasks []liveTask, notes []string, old, next []StoredPreset, sweep, warm bool) (string, error) {
	pid := findSkatePID()
	if pid == 0 {
		return "", errors.New("skate. isn't running")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.PROCESS_VM_WRITE|windows.PROCESS_VM_OPERATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("couldn't open skate. to change it: %v", err)
	}
	closeH := true // the background look through the rest of memory keeps the handle and closes it itself
	defer func() {
		if closeH {
			windows.CloseHandle(h)
		}
	}()
	if !sweep && !warm {
		liveCancel.Store(true) // a load asks the scan going on in the background to give way
	}
	// The board doesn't wait for the scan that may still be running in the background: it has its own way in.
	if !warm {
		selID, _ := liveSelected()
		for ti := range tasks {
			if tasks[ti].group != 1 {
				continue
			}
			tasks[ti].direct = true
			if tasks[ti].preset != selID {
				tl.logf("LIVE: board of %s not written: it isn't the preset the game has selected (%q)", tasks[ti].preset, selID)
				continue
			}
			boardApply(h, pid, tasks[ti], sweep)
		}
	}
	liveMu.Lock()
	if !sweep && !warm {
		liveCancel.Store(false)
	}
	handoff := false // the rest of the scan carries on in the background and unlocks when it's done
	defer func() {
		if !handoff {
			liveMu.Unlock()
		}
	}()
	hotMu.Lock()
	if hotPID != pid {
		hotPID, hotAlloc = pid, loadHotCache(h, pid)
	}
	haveHot := len(hotAlloc) > 0
	applyHistory(pid, tasks)
	hotMu.Unlock()

	patched := make([]int, len(tasks))
	for ti := range tasks {
		if tasks[ti].group == 1 && !warm {
			patched[ti]++ // never scanned for, so not "missing"
		}
	}
	if diagChains && !warm && !sweep {
		componentChains(h, pid, tasks)
	}
	seen := make([]int, len(tasks)) // times an anchor was found
	best := make([]int, len(tasks)) // best match of any table found by an anchor
	skipped, skippedBlock, companions := 0, 0, 0
	worst := make([]string, len(tasks))     // what didn't match in that table
	samples := make([][]string, len(tasks)) // a few refused tables, entry by entry, for the log
	var mu sync.Mutex

	anchors := map[uint64][]liveCand{}
	var bloom [1 << 16]bool
	for ti, t := range tasks {
		for _, key := range t.keys {
			anchors[key] = append(anchors[key], liveCand{ti, 0})
			bloom[uint32(key)&0xffff] = true
		}
	}
	const chunk = 4 << 20
	tPrep := time.Now()
	regs := writableRegions(h)
	var total uint64
	for _, r := range regs {
		total += r.size
	}
	type job struct{ start, size uint64 }
	// Only the memory blocks that can hold what is written are read: the 2 MB blocks (skater tables) and the board's
	// (68 and 150-190 MB). The game's other big blocks held no table that mattered in any recording. If no skater
	// table turns up there, everything is read as before.
	allocMB := map[uint64]uint64{}
	for _, r := range regs {
		allocMB[r.alloc] += r.size >> 20
	}
	restrict := os.Getenv("PW_FULL_SCAN") == ""
	if !warm { // the sizes of the game's big blocks, to see where the boards sit
		var big []string
		seen := map[uint64]bool{}
		for _, r := range regs {
			if mb := allocMB[r.alloc]; mb >= 20 && mb <= 400 && !seen[r.alloc] {
				seen[r.alloc] = true
				big = append(big, fmt.Sprintf("%d", mb))
			}
		}
		sort.Strings(big)
		tl.logf("LIVE: blocks of 20-400 MB (MB each): %s", strings.Join(big, " "))
	}
	wanted := func(alloc uint64) bool {
		if !restrict {
			return true
		}
		mb := allocMB[alloc]
		return (mb >= 1 && mb <= 4) || (mb >= 60 && mb <= 75) || (mb >= 145 && mb <= 195)
	}
	// One pass over the game's memory. fast: only pages in the game's working set (pages it has
	// really used), looking at 8-byte positions, which is where the entries sit. Otherwise
	// everything, at every 4 bytes.
	var hits []liveHit
	dumped := make([]bool, len(tasks))
	var tQuery, tRead, tSearch, tWrite int64                               // nanoseconds; read/search/write are summed over all workers
	pass := func(fast bool, mode int, cancellable bool) (scanned uint64) { // mode: 0 everywhere, 1 only near earlier finds, 2 only elsewhere
		jobs := make(chan job, 64)
		var wg sync.WaitGroup
		var scannedMu sync.Mutex
		workers := runtime.NumCPU()
		if workers > 8 {
			workers = 8
		}
		if workers < 2 {
			workers = 2
		}
		if warm && workers > liveWarmWorkers {
			workers = liveWarmWorkers // in the background: keep out of the game's way
		}
		liveWorkers = workers
		step, extra := 4, 8
		if fast {
			step, extra = 8, 0
		}
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer drainOnPanic("memory scan", jobs)
				buf := make([]byte, chunk+8)
				var mine uint64
				for j := range jobs {
					n := j.size + uint64(extra)
					if n > uint64(len(buf)) {
						n = uint64(len(buf))
					}
					var rd uintptr
					tr := time.Now()
					rerr := windows.ReadProcessMemory(h, uintptr(j.start), &buf[0], uintptr(n), &rd)
					atomic.AddInt64(&tRead, int64(time.Since(tr)))
					if rerr != nil && rd == 0 {
						continue
					}
					ts := time.Now()
					mine += uint64(rd)
					cur := buf[:rd]
					own := int(j.size)
					if own > len(cur) {
						own = len(cur)
					}
					for i := 0; i+8 <= len(cur) && i < own; i += step {
						lo := binary.LittleEndian.Uint32(cur[i:])
						if !bloom[lo&0xffff] {
							continue
						}
						key := uint64(lo) | uint64(binary.LittleEndian.Uint32(cur[i+4:]))<<32
						cands, ok := anchors[key]
						if !ok {
							continue
						}
						addr := j.start + uint64(i)
						// score every task this entry could belong to; the best one owns the table
						bestTask, bestScore, tie := -1, -1, false
						var bestEnts []liveEnt
						for _, c := range cands {
							t := tasks[c.task]
							if t.direct {
								continue
							}
							sc, ents, why := scoreTable(h, addr, t)
							mu.Lock()
							seen[c.task]++
							if sc < 0 && worst[c.task] == "" {
								worst[c.task] = why
							}
							if sc < 0 && len(ents) >= 4 && len(samples[c.task]) < 3 && !sweep && !warm {
								samples[c.task] = append(samples[c.task], sampleTable(t, addr, ents, why))
							}
							if sc >= 0 && len(ents) > best[c.task] {
								best[c.task] = len(ents)
							}
							mu.Unlock()
							if sc < 0 {
								continue
							}
							if sc > bestScore {
								bestTask, bestScore, tie, bestEnts = c.task, sc, false, ents
							} else if sc == bestScore && c.task != bestTask {
								x, y := tasks[bestTask].next, t.next
								for k := range x {
									if k >= len(y) || x[k] != y[k] {
										tie = true
									}
								}
							}
						}
						if bestTask < 0 {
							continue
						}
						t := tasks[bestTask]
						mu.Lock()
						if !tie && !sweep && !warm && !dumped[bestTask] {
							dumped[bestTask] = true
							dumpTable(t, bestEnts)
						}
						if !tie && !blockAllowed(regs, bestEnts[0].addr, t.group) {
							skippedBlock++
						} else if tie {
							skipped++
						} else if tw := time.Now(); warm || writeTable(h, t, bestEnts) { // the pre-scan only looks: it never writes
							atomic.AddInt64(&tWrite, int64(time.Since(tw)))
							patched[bestTask]++
							hits = append(hits, liveHit{bestEnts[0].addr, t.group})
							markHot(regs, bestEnts[0].addr)
						}
						mu.Unlock()
					}
					atomic.AddInt64(&tSearch, int64(time.Since(ts)))
				}
				scannedMu.Lock()
				scanned += mine
				scannedMu.Unlock()
			}()
		}
		for _, r := range regs {
			if mode != 0 && isHotAlloc(r.alloc) != (mode == 1) {
				continue
			}
			if !wanted(r.alloc) {
				continue
			}
			if cancellable && liveCancel.Load() {
				break
			}
			if fast {
				tq := time.Now()
				runs := residentRuns(h, r.base, r.size)
				atomic.AddInt64(&tQuery, int64(time.Since(tq)))
				for _, run := range runs {
					for off := uint64(0); off < run[1]; off += chunk {
						n := run[1] - off
						if n > chunk {
							n = chunk
						}
						jobs <- job{run[0] + off, n}
					}
				}
				continue
			}
			for off := uint64(0); off < r.size; off += chunk {
				n := r.size - off
				if n > chunk {
					n = chunk
				}
				jobs <- job{r.base + off, n}
			}
		}
		close(jobs)
		wg.Wait()
		return scanned
	}
	allFound := func() bool {
		for _, c := range patched {
			if c == 0 {
				return false
			}
		}
		return true
	}
	t0 := time.Now()
	var scanned uint64
	early := false // the game's copies were all found near the earlier ones; the rest is looked at afterwards
	if haveHot {
		scanned = pass(true, 1, false)
		tl.logf("LIVE: near earlier finds %.1f s: read %d MB, %d of %d groups found", time.Since(t0).Seconds(), scanned>>20, countFound(patched), len(tasks))
		if allFound() && !sweep && !warm {
			early = true
		}
	}
	if warm && haveHot && allFound() {
		early = false // everything was where the saved list said
	} else if !early {
		mode := 0
		if haveHot {
			mode = 2
		}
		t1 := time.Now()
		scanned = pass(true, mode, sweep || warm)
		tl.logf("LIVE: fast pass %.1f s: read %d of %d MB of private memory, %d of %d groups found", time.Since(t1).Seconds(), scanned>>20, total>>20, countFound(patched), len(tasks))
	}
	skaterMissing := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range patched {
			if c == 0 { // a skater or a board group: its memory block may be outside the usual sizes
				return true
			}
		}
		return false
	}
	if restrict && !early && skaterMissing() {
		restrict = false
		t2 := time.Now()
		scanned += pass(true, 0, sweep || warm)
		tl.logf("LIVE: a group missing from the usual blocks; read everything: %.1f s, %d of %d groups found", time.Since(t2).Seconds(), countFound(patched), len(tasks))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(liveCachePath), "census")); err == nil && !warm {
		for _, t := range tasks {
			if t.group == 1 {
				boardCensus(h, regs, t)
			}
		}
	}
	if !warm {
		dumpLoadout(h, pid, hits, regs)
	}
	if companions > 0 {
		tl.logf("LIVE: board: %d companion tables written (the ones beside the ones found, holding the game's old board)", companions)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(liveCachePath), "full_scan")); err == nil && !early {
		// Test: a file named full_scan makes a complete read (every page, every 4 bytes) after the fast one, to see
		// whether the board has tables the fast pass doesn't reach.
		restrict = false
		before := countFound(patched)
		t4 := time.Now()
		nb := 0
		mu.Lock()
		for _, h := range hits {
			if h.group == 1 {
				nb++
			}
		}
		mu.Unlock()
		scanned += pass(false, 0, false)
		na := 0
		mu.Lock()
		for _, h := range hits {
			if h.group == 1 {
				na++
			}
		}
		mu.Unlock()
		tl.logf("LIVE: full scan %.1f s: board tables %d before, %d after (groups found %d before, %d after)", time.Since(t4).Seconds(), nb, na, before, countFound(patched))
	}
	tl.logf("LIVE: total %.1f s", time.Since(t0).Seconds())
	ms := func(ns int64) float64 { return float64(ns) / 1e6 }
	tl.logf("LIVE: timing: list regions %.0f ms; working-set query %.0f ms (in the feeder, overlaps the reads); read %.0f ms, search %.0f ms, write %.0f ms (read/search/write added up over %d workers); %d MB read; %d regions", ms(int64(t0.Sub(tPrep))), ms(tQuery), ms(tRead), ms(tSearch), ms(tWrite), liveWorkers, scanned>>20, len(regs))
	mu.Lock()
	if !warm {
		reportHits(hits, regs, scanned)
		dumpBoardPairs(h, hits, regs)
	}
	mu.Unlock()
	if warm {
		liveWarmFound.Store(allFound())
		tl.logf("LIVE: pre-scan: %d of %d outfit groups found, %d blocks remembered", countFound(patched), len(tasks), hotCount())
	}
	saveHotCache(pid, h)
	if early && !sweep {
		// the rest of the game's memory, in the background, keeping others out until it's done
		handoff, closeH = true, false
		before := 0
		for _, c := range patched {
			before += c
		}
		go func() {
			defer recoverLog("live pass")
			defer liveMu.Unlock()
			defer windows.CloseHandle(h)
			t3 := time.Now()
			restRead := pass(true, 2, true)
			after := 0
			mu.Lock()
			for _, c := range patched {
				after += c
			}
			mu.Unlock()
			tl.logf("LIVE: the rest %.1f s: read %d MB, %d more tables", time.Since(t3).Seconds(), restRead>>20, after-before)
			saveHotCache(pid, h)
			if liveCensusOn {
				liveCensus(h, regs, tasks, old, next)
			}
		}()
	}

	tables, found := 0, 0
	var misses []string
	mu.Lock()
	for ti, c := range patched {
		tables += c
		if c > 0 {
			found++
		} else {
			t := tasks[ti]
			misses = append(misses, fmt.Sprintf("%s group %d: anchor seen %d times, longest run of its entries found: %d of %d (%s)", t.preset, t.group, seen[ti], best[ti], len(t.slots), worst[ti]))
		}
	}
	mu.Unlock()
	// The summary is read by people (it goes into the activity log): one sentence a line.
	msg := "Nothing needed changing in the game's memory."
	if len(tasks) > 0 {
		msg = fmt.Sprintf("Changed %s in the game's memory, across %d of %d outfit groups.", plural(tables, "entry", "entries"), found, len(tasks))
	}
	if skippedBlock > 0 {
		tl.logf("LIVE: block filter: %d tables left alone (not in the listed blocks)", skippedBlock)
	}
	if skipped > 0 {
		msg += fmt.Sprintf("\n%s skipped because two presets wear the same outfit.", plural(skipped, "entry", "entries"))
	}
	if found < len(tasks) && !warm && !sweep {
		msg += fmt.Sprintf("\n%d of %d outfit groups couldn't be found in the game's memory, so they only change after a restart.", len(tasks)-found, len(tasks))
	}
	for _, n := range notes {
		msg += "\n" + n
	}
	if warm {
		return msg, nil
	}
	if sweep {
		tl.logf("LIVE: second look found %d more tables", tables)
		return msg, nil
	}
	if len(misses) > 0 {
		mu.Lock()
		for ti, c := range patched {
			if c == 0 {
				for _, sm := range samples[ti] {
					tl.logf("LIVE: refused table: %s", sm)
				}
			}
		}
		mu.Unlock()
		tl.logf("LIVE: not found: %s", strings.Join(misses, " | "))
	}
	return msg, nil
}

// Writes the new item into each entry that isn't already it.
func writeTable(h windows.Handle, t liveTask, ents []liveEnt) bool {
	ok := true
	for _, e := range ents {
		if e.item == t.next[e.idx] || t.keep[e.idx] {
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

func countFound(patched []int) (n int) {
	for _, c := range patched {
		if c > 0 {
			n++
		}
	}
	return n
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

type blockFilter struct {
	sync.Mutex
	at   time.Time
	set  map[int]bool
	text string
}

var onlySkaterBlocks, onlyBoardBlocks, boardParity blockFilter

// onlyBlocks reads a diagnostic file in the app's data folder: block sizes, like 68 or 2 512, and the tables in other
// blocks are left alone. No file, or an empty one, means no limit from it.
func (c *blockFilter) read(file string) map[int]bool {
	c.Lock()
	defer c.Unlock()
	if time.Since(c.at) < 300*time.Millisecond {
		return c.set
	}
	c.at = time.Now()
	c.set = nil
	b, err := os.ReadFile(filepath.Join(filepath.Dir(liveCachePath), file))
	if err == nil {
		set := map[int]bool{}
		for _, f := range strings.Fields(string(b)) {
			if n, e := strconv.Atoi(f); e == nil {
				set[n] = true
			}
		}
		if len(set) > 0 {
			c.set = set
		}
	}
	if t := fmt.Sprint(c.set); t != c.text {
		c.text = t
		tl.logf("LIVE: %s is now %v", file, c.set)
	}
	return c.set
}

func blockAllowed(regs []memRegion, addr uint64, group int) bool {
	if group != 0 {
		// Test: the board's tables may have a copy the game reads at once, as the skater's 512 MB block does. A file
		// named only_board_blocks (68 and/or 200; 200 is the 150-190 MB block) limits where they are written.
		if set := onlyBoardBlocks.read("only_board_blocks"); set != nil && !set[blockClass(regs, addr)] {
			return false
		}
		// Test: a file named board_parity with 0 or 1 writes only the even or only the odd tables of the big board
		// block (they sit 0xD0 apart), to see which of them the game reads.
		if p := boardParity.read("board_parity"); p != nil && blockClass(regs, addr) == 200 {
			return p[int(addr/0xD0%2)]
		}
		return true
	}
	// The skater tables in the 512 MB block are the ones the game reads at once: writing them makes the skater change
	// by itself before the replay, on some loads. The copies in the 2 MB blocks are the ones the replay rebuilds from.
	// A file named only_blocks (block sizes, like 2 512) overrides this for testing.
	if set := onlySkaterBlocks.read("only_blocks"); set != nil {
		return set[blockClass(regs, addr)]
	}
	return blockClass(regs, addr) == 2
}

func reportHits(hits []liveHit, regs []memRegion, scanned uint64) {
	if len(hits) == 0 {
		return
	}
	type agg struct {
		hits, regions int
		mb            uint64
		skater, board int
	}
	byAlloc := map[uint64]*agg{}
	regionHit := map[int]bool{}
	sizeCls := func(sz uint64) string {
		switch {
		case sz < 64<<10:
			return "under 64 KB"
		case sz < 1<<20:
			return "64 KB to 1 MB"
		case sz < 16<<20:
			return "1 to 16 MB"
		}
		return "16 MB and over"
	}
	clsAll := map[string][2]uint64{} // regions, MB
	for _, r := range regs {
		c := clsAll[sizeCls(r.size)]
		clsAll[sizeCls(r.size)] = [2]uint64{c[0] + 1, c[1] + r.size>>20}
	}
	clsHit := map[string]int{}
	for _, hit := range hits {
		i := sort.Search(len(regs), func(i int) bool { return regs[i].base+regs[i].size > hit.addr })
		if i >= len(regs) || regs[i].base > hit.addr {
			continue
		}
		regionHit[i] = true
		a := byAlloc[regs[i].alloc]
		if a == nil {
			a = &agg{}
			byAlloc[regs[i].alloc] = a
		}
		a.hits++
		if hit.group == 0 {
			a.skater++
		} else {
			a.board++
		}
		clsHit[sizeCls(regs[i].size)]++
	}
	// size of each allocation that holds a hit, counting every region of it that was read
	for _, r := range regs {
		if a := byAlloc[r.alloc]; a != nil {
			a.regions++
			a.mb += r.size >> 20
		}
	}
	var totalMB uint64
	type kv struct {
		k uint64
		a *agg
	}
	var list []kv
	for k, a := range byAlloc {
		totalMB += a.mb
		list = append(list, kv{k, a})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].a.hits > list[j].a.hits })
	tl.logf("LIVE: where: %d tables sit in %d regions of %d memory blocks (allocations); those blocks add up to %d MB of the %d MB read", len(hits), len(regionHit), len(byAlloc), totalMB, scanned>>20)
	for i, e := range list {
		if i >= 12 {
			break
		}
		tl.logf("LIVE: where:   block %#x: %d tables (%d skater, %d board), %d regions, %d MB", e.k, e.a.hits, e.a.skater, e.a.board, e.a.regions, e.a.mb)
	}
	for _, name := range []string{"under 64 KB", "64 KB to 1 MB", "1 to 16 MB", "16 MB and over"} {
		tl.logf("LIVE: where:   regions %s: %d tables; %d such regions, %d MB read in all", name, clsHit[name], clsAll[name][0], clsAll[name][1])
	}
}

// One table as the game has it, next to what the file said and what is written, for the log.
func dumpTable(t liveTask, ents []liveEnt) {
	slotName := func(h uint32) string {
		if n, ok := skaterSlots[uint64(h)]; ok {
			return n
		}
		if n, ok := boardSlots[uint64(h)]; ok {
			return n
		}
		return fmt.Sprint(h)
	}
	tl.logf("LIVE: table dump: %s group %d: the game has %d entries in a row at %#x; the file has %d slots", t.preset, t.group, len(ents), ents[0].addr, len(t.slots))
	have := map[int]bool{}
	for _, e := range ents {
		have[e.idx] = true
		act := "same"
		switch {
		case t.keep[e.idx]:
			act = "kept (not asked for)"
		case e.item != t.next[e.idx]:
			act = "WRITE"
		}
		tl.logf("LIVE:   [%d] %s: game %d, file %q (%d), new %q (%d): %s", e.idx, slotName(t.slots[e.idx]), e.item, t.oldName[e.idx], t.old[e.idx], t.newName[e.idx], t.next[e.idx], act)
	}
	var missing []string
	for i := range t.slots {
		if !have[i] && (t.old[i] != 0 || t.next[i] != 0) {
			missing = append(missing, fmt.Sprintf("[%d] %s (file %q, new %q)", i, slotName(t.slots[i]), t.oldName[i], t.newName[i]))
		}
	}
	if len(missing) > 0 {
		tl.logf("LIVE:   not in the game's table, so not written: %s", strings.Join(missing, "; "))
	}
}

// Every outfit each preset has had while the game has been running, so that copies the game made earlier,
// holding an older outfit, are still found and brought up to date.
type liveGen struct {
	items map[uint32]uint32 // slot -> item
	lens  map[uint32]int    // slot -> length of the item's name
}

var (
	liveHistMu  sync.Mutex
	liveHistPID uint32
	liveHist    = map[string][]liveGen{}
)

// What the plugin says the selected preset's skater wears right now (slot -> item, and the items' name lengths).
// The game doesn't save edits made to a preset that was loaded live, so its tables can hold an outfit the file and
// the app's own history have never seen; this is that outfit, taken as one more the preset has had.
var liveNowPreset string
var liveNowGen liveGen

func noteLiveOutfit(preset string, items map[uint32]uint32, lens map[uint32]int) {
	liveHistMu.Lock()
	liveNowPreset, liveNowGen = preset, liveGen{items, lens}
	liveHistMu.Unlock()
}

func applyHistory(pid uint32, tasks []liveTask) {
	liveHistMu.Lock()
	defer liveHistMu.Unlock()
	if liveHistPID != pid {
		liveHistPID, liveHist = pid, map[string][]liveGen{}
	}
	genOf := func(t liveTask, items []uint32, names []string) liveGen {
		g := liveGen{map[uint32]uint32{}, map[uint32]int{}}
		for i, sl := range t.slots {
			g.items[sl] = items[i]
			g.lens[sl] = len(names[i])
		}
		return g
	}
	same := func(a, b liveGen) bool {
		if len(a.items) != len(b.items) {
			return false
		}
		for k, v := range a.items {
			if b.items[k] != v {
				return false
			}
		}
		return true
	}
	for i := range tasks {
		t := &tasks[i]
		key := fmt.Sprintf("%s/%d", t.preset, t.group)
		hist := liveHist[key]
		gens := []liveGen{genOf(*t, t.old, t.oldName), genOf(*t, t.next, t.newName)}
		if t.group == 0 && t.preset == liveNowPreset && len(liveNowGen.items) > 0 {
			// the file's outfit with what the game's skater really wears on top
			g := genOf(*t, t.old, t.oldName)
			for sl, item := range liveNowGen.items {
				if _, has := g.items[sl]; has {
					g.items[sl], g.lens[sl] = item, liveNowGen.lens[sl]
				}
			}
			gens = append(gens, g)
		}
		for _, g := range gens {
			dup := false
			for _, h := range hist {
				if same(h, g) {
					dup = true
					break
				}
			}
			if !dup {
				hist = append(hist, g)
			}
		}
		if len(hist) > 16 {
			hist = hist[len(hist)-16:]
		}
		liveHist[key] = hist
		seen := map[uint64]bool{}
		for _, g := range hist {
			row := make([]uint32, len(t.slots))
			for j, sl := range t.slots {
				row[j] = g.items[sl]
			}
			t.gens = append(t.gens, row)
			// the entries with the longest names are the least likely to be somewhere else by chance
			idx := make([]int, 0, len(t.slots))
			for j, sl := range t.slots {
				if g.items[sl] != 0 {
					idx = append(idx, j)
				}
			}
			sort.Slice(idx, func(x, y int) bool { return g.lens[t.slots[idx[x]]] > g.lens[t.slots[idx[y]]] })
			if len(idx) > 3 {
				idx = idx[:3]
			}
			for _, j := range idx {
				k := uint64(g.items[t.slots[j]]) | uint64(t.slots[j])<<32
				if !seen[k] {
					seen[k] = true
					t.keys = append(t.keys, k)
				}
			}
		}
	}
}

// boardStale: the board's table is always 8 entries in a row, in the slot order. When the game's board isn't what the
// file says (the game was restarted, the board was changed in the game's menus, the app lost its history) the values
// can't be compared, but a full run of entries in the right slots, with one of them an anchor, is the table.
func boardStale(t liveTask, ents []liveEnt) bool {
	if t.group != 1 || len(ents) != len(t.slots) {
		return false
	}
	for i, e := range ents {
		if e.idx != i {
			return false
		}
	}
	// The deck must be one this preset has had. Other presets' boards sit beside this one's in the same block, and a
	// table with the right slots but another deck is theirs. With the deck not installed there is nothing to tell by.
	const deck = 2
	if deck >= len(t.skip) || t.skip[deck] {
		return false
	}
	for _, g := range t.gens {
		if deck < len(g) && ents[deck].item == g[deck] {
			return true
		}
	}
	return false
}

// dumpBoardPairs (diagnostic): the board's tables in the big block sit 0xD0 apart. Writes the raw bytes of a run of
// them (the 16 bytes before each, and its first three entries) to the log, with where each sits in its allocation,
// so the ones the replay reads can be told from the others.
func dumpBoardPairs(h windows.Handle, hits []liveHit, regs []memRegion) {
	var addrs []uint64
	for _, x := range hits {
		if x.group == 1 && blockClass(regs, x.addr) == 200 {
			addrs = append(addrs, x.addr)
		}
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i] < addrs[j] })
	allocOf := func(a uint64) uint64 {
		for _, r := range regs {
			if r.base <= a && a < r.base+r.size {
				return r.alloc
			}
		}
		return 0
	}
	hexs := func(a uint64, n int) string {
		b := make([]byte, n)
		var rd uintptr
		if windows.ReadProcessMemory(h, uintptr(a), &b[0], uintptr(n), &rd) != nil || int(rd) != n {
			return "unreadable"
		}
		return fmt.Sprintf("%x", b)
	}
	seen := 0
	for _, a := range addrs {
		if seen >= 8 {
			break
		}
		seen++
		base := allocOf(a)
		if seen <= 4 {
			for _, d := range []int64{-0xD0, 0xD0, 0x1A0} {
				na := uint64(int64(a) + d)
				tl.logf("LIVE: board raw neighbour %+#x of %#x: %#x before=%s ents=%s", d, a, na, hexs(na-16, 16), hexs(na, 72))
			}
		}
		tl.logf("LIVE: board raw: %#x alloc+%#x (/0xD0 = %d, parity %d, abs parity %d) before=%s ents=%s", a, a-base, (a-base)/0xD0, (a-base)/0xD0%2, a/0xD0%2, hexs(a-16, 16), hexs(a, 72))
	}
}

// writeCompanions: the board's tables come in pairs 0xD0 apart. The ones beside the one found hold the game's old board
// and aren't found by the file's values, but the game may rebuild from them. A neighbour whose eight entries have the
// same slots in the same order gets the same new values.
func writeCompanions(h windows.Handle, t liveTask, ents []liveEnt) int {
	if len(ents) != len(t.slots) {
		return 0
	}
	for i, e := range ents {
		if e.idx != i {
			return 0
		}
	}
	n := 0
	for _, d := range []int64{-0xD0, 0xD0} {
		nb := make([]liveEnt, 0, len(ents))
		same := true
		for _, e := range ents {
			a := uint64(int64(e.addr) + d)
			it, sl, ok := readEnt(h, a)
			if !ok || sl != t.slots[e.idx] {
				same = false
				break
			}
			nb = append(nb, liveEnt{a, e.idx, it})
		}
		if same && writeTable(h, t, nb) {
			n++
		}
	}
	return n
}

// boardCensus (diagnostic, switched on by a file named census): every run of board entries in the game's memory, whatever
// it holds (the Grip, Wheels, Deck, Grip cutout and Trucks slots in a row), counted by memory block and by whether
// it holds the new deck, the old one or something else, to see where the old board still lives after a load.
func boardCensus(h windows.Handle, regs []memRegion, t liveTask) {
	if len(t.slots) < 5 {
		return
	}
	type key struct {
		alloc uint64
		kind  string
	}
	var mu sync.Mutex
	counts := map[key]int{}
	first := map[key][]uint64{}
	allocMB := map[uint64]uint64{}
	for _, r := range regs {
		allocMB[r.alloc] += r.size >> 20
	}
	kindOf := func(v uint32) string {
		switch v {
		case t.next[2]:
			return "NEW"
		case t.old[2]:
			return "OLD(file)"
		}
		return "other"
	}
	const chunk = 4 << 20
	jobs := make(chan memRegion)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer drainOnPanic("memory scan", jobs)
			buf := make([]byte, chunk+96)
			le := binary.LittleEndian
			for r := range jobs {
				for off := uint64(0); off < r.size; off += chunk {
					n := uint64(chunk + 96)
					if off+n > r.size {
						n = r.size - off
					}
					var rd uintptr
					if windows.ReadProcessMemory(h, uintptr(r.base+off), &buf[0], uintptr(n), &rd) != nil || uint64(rd) < 120 {
						continue
					}
					lim := uint64(rd) - 120
					for p := uint64(48); p < lim && p < chunk+48; p += 8 {
						if le.Uint32(buf[p+4:]) != t.slots[2] || le.Uint32(buf[p-24+4:]) != t.slots[1] || le.Uint32(buf[p-48+4:]) != t.slots[0] ||
							le.Uint32(buf[p+24+4:]) != t.slots[3] || le.Uint32(buf[p+48+4:]) != t.slots[4] {
							continue
						}
						k := key{r.alloc, kindOf(le.Uint32(buf[p:]))}
						mu.Lock()
						counts[k]++
						if len(first[k]) < 3 {
							first[k] = append(first[k], r.base+off+p-48)
						}
						mu.Unlock()
					}
				}
			}
		}()
	}
	for _, r := range regs {
		jobs <- r
	}
	close(jobs)
	wg.Wait()
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].alloc != keys[j].alloc {
			return keys[i].alloc < keys[j].alloc
		}
		return keys[i].kind < keys[j].kind
	})
	for _, k := range keys {
		var at []string
		for _, a := range first[k] {
			at = append(at, fmt.Sprintf("%#x", a))
		}
		tl.logf("LIVE: census: allocation %#x (%d MB), deck %s: %d runs, e.g. %s", k.alloc, allocMB[k.alloc], k.kind, counts[k], strings.Join(at, " "))
	}
}

const (
	loadoutManagerOffset = 0x71ec8c8 // build 20260929: Skate.exe's pointer to the game's loadout manager
	boardTemplateKey     = 0x5e61aaef
)

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

func moduleBase(pid uint32) uint64 {
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

// dumpLoadout (diagnostic): the game keeps its saved presets in a loadout manager; each preset is a vector of recipes, and a
// recipe's items pointer is the array of {asset*, parameters*, hash, slot} the tables we look for are copies of or are. Logs
// where each preset's board and first skater recipe items sit, and whether the scan found a table at that address.
func dumpLoadout(h windows.Handle, pid uint32, hits []liveHit, regs []memRegion) {
	base := moduleBase(pid)
	if base == 0 {
		tl.logf("LIVE: loadout: no module base")
		return
	}
	mgr, ok := rd64(h, base+loadoutManagerOffset)
	if !ok || mgr == 0 {
		tl.logf("LIVE: loadout: no manager at base %#x + %#x", base, loadoutManagerOffset)
		return
	}
	begin, ok1 := rd64(h, mgr+0x18)
	end, ok2 := rd64(h, mgr+0x20)
	if !ok1 || !ok2 || begin >= end || (end-begin)%0x48 != 0 || (end-begin)/0x48 > 10 {
		tl.logf("LIVE: loadout: manager %#x has no preset list (%#x..%#x)", mgr, begin, end)
		return
	}
	found := map[uint64]bool{}
	for _, x := range hits {
		found[x.addr] = true
	}
	n := (end - begin) / 0x48
	tl.logf("LIVE: loadout: module %#x, manager %#x, %d presets", base, mgr, n)
	for i := uint64(0); i < n; i++ {
		rec := begin + i*0x48
		vb, _ := rd64(h, rec)
		ve, _ := rd64(h, rec+8)
		for p := vb; p < ve && p-vb < 0x28*16; p += 0x28 {
			res, _ := rd64(h, p)
			items, _ := rd64(h, p+16)
			key, _ := rd32(h, res+0x38)
			cnt, _ := rd32(h, items-4)
			kind := "other"
			if key == boardTemplateKey {
				kind = "BOARD"
			}
			// the first entry's hash sits 16 bytes into the array
			hash0, _ := rd32(h, items+16)
			deck := uint32(0)
			if kind == "BOARD" && cnt > 2 {
				deck, _ = rd32(h, items+2*24+16)
			}
			first := items + 16
			mb := uint64(0)
			for _, r := range regs {
				if r.base <= items && items < r.base+r.size {
					for _, q := range regs {
						if q.alloc == r.alloc {
							mb += q.size >> 20
						}
					}
					break
				}
			}
			if kind == "BOARD" || p == vb {
				tl.logf("LIVE: loadout: preset %d %s template %#x items %#x (%d entries, in a %d MB block), first hash %d, deck %d, found by the scan: %v", i, kind, key, items, cnt&0x7fffffff, mb, hash0, deck, found[first])
			}
		}
	}
}

// writeBoardRecord writes the selected preset's board straight into the game's preset list: the manager's record for the
// preset holds a recipe per template, and the board's recipe points at its array of items (24 bytes each: asset
// pointer, parameter pointer, hash, slot). No table is looked for by its contents, so no other preset's board is touched.
func writeBoardRecord(h windows.Handle, pid uint32, t liveTask, idx int) {
	base := moduleBase(pid)
	if base == 0 || idx < 0 {
		tl.logf("LIVE: board not written: can't tell which preset the game has selected")
		return
	}
	mgr, ok := rd64(h, base+loadoutManagerOffset)
	begin, ok1 := rd64(h, mgr+0x18)
	end, ok2 := rd64(h, mgr+0x20)
	if !ok || !ok1 || !ok2 || mgr == 0 || begin >= end || (end-begin)%0x48 != 0 || (end-begin)/0x48 > 10 {
		tl.logf("LIVE: board not written: the game's preset list isn't where this build expects")
		return
	}
	if uint64(idx) >= (end-begin)/0x48 {
		tl.logf("LIVE: board not written: preset %d isn't loaded in the game's list (%d are)", idx, (end-begin)/0x48)
		return
	}
	rec := begin + uint64(idx)*0x48
	// The record's own rows, and the baseline the game copies them to after a save (at +0x20).
	writeBoardVector(h, pid, t, idx, rec, "record")
	writeBoardVector(h, pid, t, idx, rec+0x20, "baseline")
}

func writeBoardVector(h windows.Handle, pid uint32, t liveTask, idx int, vec uint64, what string) {
	vb, _ := rd64(h, vec)
	ve, _ := rd64(h, vec+8)
	var items uint64
	for p := vb; p < ve && p-vb < 0x28*16; p += 0x28 {
		res, _ := rd64(h, p)
		if key, _ := rd32(h, res+0x38); key == boardTemplateKey {
			items, _ = rd64(h, p+16)
			break
		}
	}
	if items == 0 {
		tl.logf("LIVE: board not written: preset %d's %s has no board recipe", idx, what)
		return
	}
	cnt, _ := rd32(h, items-4)
	cnt &= 0x7fffffff
	if cnt == 0 || cnt > 32 {
		tl.logf("LIVE: board not written: the %s's board recipe has %d entries", what, cnt)
		return
	}
	var ents []liveEnt
	for i := uint64(0); i < uint64(cnt); i++ {
		a := items + i*24 + 16
		item, slot, ok := readEnt(h, a)
		if !ok {
			continue
		}
		if ix, in := t.slotIdx[slot]; in {
			ents = append(ents, liveEnt{a, ix, item})
		}
	}
	if len(ents) < 3 {
		tl.logf("LIVE: board not written: only %d of the %s's entries are board slots", len(ents), what)
		return
	}
	// The game checks that an item's name hashes to its hash when it saves a preset and refuses it otherwise
	// (invalid_native_recipe), so each changed entry gets its new name, kept in a small block of the game's memory.
	named := 0
	for _, e := range ents {
		if t.keep[e.idx] || e.item == t.next[e.idx] || e.idx >= len(t.newName) {
			continue
		}
		if p := remoteString(h, pid, t.newName[e.idx]); p != 0 {
			var v [8]byte
			binary.LittleEndian.PutUint64(v[:], p)
			var w uintptr
			if windows.WriteProcessMemory(h, uintptr(e.addr-16), &v[0], 8, &w) == nil && w == 8 {
				named++
			}
		}
	}
	if writeTable(h, t, ents) {
		tl.logf("LIVE: board written into preset %d's %s at %#x (%d entries, %d names set)", idx, what, items, len(ents), named)
	} else {
		tl.logf("LIVE: board: a write into preset %d's %s at %#x failed", idx, what, items)
	}
}

var (
	remoteStrMu  sync.Mutex
	remoteStrPID uint32
	remoteStr    = map[string]uint64{}
)

// remoteString puts a NUL-ended copy of s into the game's memory (once per name) and returns its address.
func remoteString(h windows.Handle, pid uint32, s string) uint64 {
	remoteStrMu.Lock()
	defer remoteStrMu.Unlock()
	if remoteStrPID != pid {
		remoteStrPID, remoteStr = pid, map[string]uint64{}
	}
	if p, ok := remoteStr[s]; ok {
		return p
	}
	return 0 // item names are no longer put into the game: that needed memory allocated in its process
}

const cosmeticComponentVtable = 0x6084c88 // build 20260929: the item component's vtable, from Skate.exe's base

// pokeBoardComponents finds the game's item components (they all start with one vtable pointer), picks the board
// ones whose outfit is this preset's, writes the new items into their own tables and sets the component's
// "items changed" flag (+0x10a), the same flag the game's own recipe copy sets, so the game rebuilds the board.
func pokeBoardComponents(h windows.Handle, pid uint32, t liveTask) {
	base := moduleBase(pid)
	if base == 0 || len(t.slots) < 5 {
		return
	}
	want := base + cosmeticComponentVtable
	var comps []uint64
	compMu.Lock()
	if compPID != pid {
		compPID, compCache = pid, nil
	}
	for _, c := range compCache {
		if v, ok := rd64(h, c); ok && v == want {
			comps = append(comps, c)
		}
	}
	compMu.Unlock()
	if len(comps) == 0 {
		comps = findComponents(h, want)
		compMu.Lock()
		compCache = comps
		compMu.Unlock()
	} else {
		tl.logf("LIVE: components: %d item components, known from before", len(comps))
	}
	pokeComponents(h, t, comps)
}

var (
	compMu    sync.Mutex
	compPID   uint32
	compCache []uint64
)

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

func pokeComponents(h windows.Handle, t liveTask, comps []uint64) {
	poked := 0
	boards := 0
	for _, c := range comps {
		if p1, ok := rd64(h, c+8); ok {
			if res, ok := rd64(h, p1+0x90); ok {
				if key, _ := rd32(h, res&^4+0x38); key == boardTemplateKey {
					boards++
				}
			}
		}
	}
	for _, c := range comps {
		p1, ok1 := rd64(h, c+8)
		res, ok2 := rd64(h, p1+0x90)
		if !ok1 || !ok2 {
			continue
		}
		res &^= 4
		key, _ := rd32(h, res+0x38)
		items, _ := rd64(h, c+0x158)
		flag := []byte{0}
		var rd uintptr
		windows.ReadProcessMemory(h, uintptr(c+0x10a), &flag[0], 1, &rd)
		tl.logf("LIVE: component %#x: template %#x, items %#x, flag %d", c, key, items, flag[0])
		if key != boardTemplateKey {
			continue
		}
		cnt, _ := rd32(h, items-4)
		cnt &= 0x7fffffff
		if items < 0x10000 || cnt == 0 || cnt > 32 {
			tl.logf("LIVE: board component %#x: items %#x, count %d: skipped", c, items, cnt)
			continue
		}
		var ents []liveEnt
		for i := uint64(0); i < uint64(cnt); i++ {
			a := items + i*24 + 16
			item, slot, ok := readEnt(h, a)
			if ok {
				if ix, in := t.slotIdx[slot]; in {
					ents = append(ents, liveEnt{a, ix, item})
				}
			}
		}
		deck := uint32(0)
		match := false
		for _, e := range ents {
			if e.idx != 2 {
				continue
			}
			deck = e.item
			if e.item == t.next[2] {
				match = true
			}
			for _, g := range t.gens {
				if 2 < len(g) && e.item == g[2] {
					match = true
				}
			}
		}
		tl.logf("LIVE: board component %#x: items %#x, %d entries, deck %d, flag %d, ours: %v", c, items, len(ents), deck, flag[0], match)
		// The game keeps one board for the local skater. When it is the only one, it is ours whatever the app thinks the
		// game holds (they often differ), so it counts without matching the deck.
		if !match && boards == 1 {
			match = true
		}
		if !match || len(ents) < 3 {
			continue
		}
		if !writeTable(h, t, ents) {
			tl.logf("LIVE: board component %#x: a write failed", c)
			continue
		}
		// The game feeds the local board from the player's outfit and puts the outfit's items back whenever it
		// updates. A recipe of its own (the multiplayer code's way, through the game's recipe copy) is marked in
		// four words of the component and in +0x134; the copy then leaves the outfit's items out of it.
		var before [4]uint32
		for i, off := range []uint64{0x138, 0x13c, 0x144, 0x148} {
			before[i], _ = rd32(h, c+off)
		}
		mode, _ := rd32(h, c+0x134)
		tl.logf("LIVE: board component %#x: feed words %v, mode %d", c, before, mode)
		var w uintptr
		put32 := func(off uint64, v uint32) {
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], v)
			windows.WriteProcessMemory(h, uintptr(c+off), &b[0], 4, &w)
		}
		put32(0x134, 0)
		for i, off := range []uint64{0x138, 0x13c, 0x144, 0x148} {
			put32(off, [4]uint32{1, 2, 1, 2}[i])
		}
		one := []byte{1}
		setA := windows.WriteProcessMemory(h, uintptr(c+0x10a), &one[0], 1, &w) == nil && w == 1
		setB := windows.WriteProcessMemory(h, uintptr(c+0x10d), &one[0], 1, &w) == nil && w == 1
		if setA && setB {
			poked++
		}
		go func(c uint64) {
			defer recoverLog("live poke")
			time.Sleep(1500 * time.Millisecond)
			var after [4]uint32
			for i, off := range []uint64{0x138, 0x13c, 0x144, 0x148} {
				after[i], _ = rd32(h, c+off)
			}
			f1 := []byte{9}
			var rd uintptr
			windows.ReadProcessMemory(h, uintptr(c+0x10a), &f1[0], 1, &rd)
			f2 := []byte{9}
			windows.ReadProcessMemory(h, uintptr(c+0x10d), &f2[0], 1, &rd)
			items, _ := rd64(h, c+0x158)
			deck, _ := rd32(h, items+2*24+16)
			tl.logf("LIVE: board component %#x, 1.5 s later: feed words %v, flags %d/%d, items %#x, entry 2 item %d", c, after, f1[0], f2[0], items, deck)
		}(c)
	}
	tl.logf("LIVE: board components written and flagged for a rebuild: %d", poked)
}

// Diagnostic build: follow the pointers out of the skater's and the board's item components and report every
// item table they lead to, so the table the game rebuilds the board from can be told by the way the skater's leads
// to its own. Nothing in the game is changed.
const diagChains = false

func componentChains(h windows.Handle, pid uint32, tasks []liveTask) {
	base := moduleBase(pid)
	if base == 0 {
		return
	}
	want := base + cosmeticComponentVtable
	var comps []uint64
	compMu.Lock()
	if compPID != pid {
		compPID, compCache = pid, nil
	}
	for _, c := range compCache {
		if v, ok := rd64(h, c); ok && v == want {
			comps = append(comps, c)
		}
	}
	compMu.Unlock()
	if len(comps) == 0 {
		comps = findComponents(h, want)
		compMu.Lock()
		compCache = comps
		compMu.Unlock()
	}
	regs := writableRegions(h)
	le := binary.LittleEndian
	plausible := func(v uint64) bool { return v > 0x10000 && v < 0x7fffffffffff && v&3 == 0 }
	for _, c := range comps {
		p1, ok1 := rd64(h, c+8)
		res, ok2 := rd64(h, p1+0x90)
		if !ok1 || !ok2 {
			continue
		}
		key, _ := rd32(h, res&^4+0x38)
		group := -1
		switch key {
		case 0xa47adc0c:
			group = 0
		case boardTemplateKey:
			group = 1
		}
		var t *liveTask
		for i := range tasks {
			if tasks[i].group == group {
				t = &tasks[i]
			}
		}
		if t == nil {
			continue
		}
		tl.logf("LIVE: chains from component %#x (template %#x, group %d)", c, key, group)
		// is A the first entry of an item table of this group?
		tableAt := func(a uint64) (hits, count int, first string) {
			var buf [4 + 24*8]byte
			var rd uintptr
			if windows.ReadProcessMemory(h, uintptr(a-4), &buf[0], uintptr(len(buf)), &rd) != nil || rd < 4+24 {
				return 0, 0, ""
			}
			count = int(le.Uint32(buf[:]) & 0x7fffffff)
			if count < 1 || count > 200 {
				return 0, count, ""
			}
			n := (int(rd) - 4) / 24
			if n > 8 {
				n = 8
			}
			for i := 0; i < n; i++ {
				item := le.Uint32(buf[4+i*24+16:])
				slot := le.Uint32(buf[4+i*24+20:])
				if _, in := t.slotIdx[slot]; in {
					hits++
				}
				first += fmt.Sprintf(" %d/%d", item, slot)
			}
			return hits, count, first
		}
		type node struct {
			addr  uint64
			path  string
			depth int
		}
		seen := map[uint64]bool{c: true}
		queue := []node{{c, "", 0}}
		reads, found := 0, 0
		for len(queue) > 0 && reads < 4000 {
			n := queue[0]
			queue = queue[1:]
			size := uint64(0x200)
			if n.depth > 0 {
				size = 0x100
			}
			buf := make([]byte, size)
			var rd uintptr
			reads++
			if windows.ReadProcessMemory(h, uintptr(n.addr), &buf[0], uintptr(size), &rd) != nil && rd == 0 {
				continue
			}
			for off := uint64(0); off+8 <= uint64(rd); off += 8 {
				v := le.Uint64(buf[off:])
				if !plausible(v) || seen[v] {
					continue
				}
				seen[v] = true
				path := fmt.Sprintf("%s+%#x", n.path, off)
				need := 3
				if group == 0 {
					need = 6
				}
				if hits, count, first := tableAt(v); hits >= need {
					found++
					tl.logf("LIVE:   table via c%s -> %#x: %d entries, %d/8 of the first in this group, block %d MB, first:%s", path, v, count, hits, blockClass(regs, v), first)
				}
				if n.depth < 2 {
					queue = append(queue, node{v, path, n.depth + 1})
				}
			}
		}
		tl.logf("LIVE:   chains from %#x: %d tables, %d nodes read", c, found, reads)
	}
}

// boardComponents returns the game's board item components (all item components start with one vtable pointer; the
// board's has the board's template). Where they sit is remembered for the next load.
func boardComponents(h windows.Handle, pid uint32) []uint64 {
	base := moduleBase(pid)
	if base == 0 {
		return nil
	}
	want := base + cosmeticComponentVtable
	var comps []uint64
	compMu.Lock()
	if compPID != pid {
		compPID, compCache = pid, nil
	}
	for _, c := range compCache {
		if v, ok := rd64(h, c); ok && v == want {
			comps = append(comps, c)
		}
	}
	compMu.Unlock()
	if len(comps) == 0 {
		comps = findComponents(h, want)
		compMu.Lock()
		compCache = comps
		compMu.Unlock()
	}
	var out []uint64
	for _, c := range comps {
		p1, ok1 := rd64(h, c+8)
		res, ok2 := rd64(h, p1+0x90)
		if !ok1 || !ok2 {
			continue
		}
		if key, _ := rd32(h, res&^4+0x38); key == boardTemplateKey {
			out = append(out, c)
		}
	}
	return out
}

// boardTablesOf lists the item tables of the board's group that the component leads to within two pointers.
func boardTablesOf(h windows.Handle, c uint64, t liveTask) []uint64 {
	le := binary.LittleEndian
	isTable := func(a uint64) bool {
		var buf [4 + 24*8]byte
		var rd uintptr
		if windows.ReadProcessMemory(h, uintptr(a-4), &buf[0], uintptr(len(buf)), &rd) != nil || int(rd) < len(buf) {
			return false
		}
		if n := le.Uint32(buf[:]) & 0x7fffffff; n != 8 {
			return false
		}
		hits := 0
		for i := 0; i < 8; i++ {
			if _, in := t.slotIdx[le.Uint32(buf[4+i*24+20:])]; in {
				hits++
			}
		}
		return hits >= 5
	}
	type node struct {
		addr  uint64
		depth int
	}
	seen := map[uint64]bool{c: true}
	queue := []node{{c, 0}}
	var out []uint64
	for len(queue) > 0 && len(seen) < 1500 {
		n := queue[0]
		queue = queue[1:]
		size := uintptr(0x200)
		if n.depth > 0 {
			size = 0x100
		}
		buf := make([]byte, size)
		var rd uintptr
		if windows.ReadProcessMemory(h, uintptr(n.addr), &buf[0], size, &rd) != nil && rd == 0 {
			continue
		}
		for off := uintptr(0); off+8 <= rd; off += 8 {
			v := le.Uint64(buf[off:])
			if v <= 0x10000 || v >= 0x7fffffffffff || v&3 != 0 || seen[v] {
				continue
			}
			seen[v] = true
			if isTable(v) {
				out = append(out, v)
			}
			if n.depth < 2 {
				queue = append(queue, node{v, n.depth + 1})
			}
		}
	}
	return out
}

var oldCensusDone bool
var traceDone bool

// oldDeckCensus looks for every board table in the game's memory that still holds the deck the board went back to
// after the rebuild. We have written the new board everywhere we know of, so a table that still has the old one is a
// candidate for the source the game rebuilds the board from. Once per run of the app.
func oldDeckCensus(h windows.Handle, t liveTask, old uint32) {
	if _, err := os.Stat(filepath.Join(filepath.Dir(liveCachePath), "census")); err != nil || oldCensusDone || old == 0 {
		return
	}
	oldCensusDone = true
	start := time.Now()
	regs := writableRegions(h)
	var mu sync.Mutex
	var lines []string
	var bases []uint64
	hits := 0
	jobs := make(chan memRegion)
	var wg sync.WaitGroup
	le := binary.LittleEndian
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer drainOnPanic("memory scan", jobs)
			const chunk = 8 << 20
			const over = 512
			buf := make([]byte, chunk+over)
			for r := range jobs {
				for off := uint64(0); off < r.size; off += chunk {
					n := uint64(chunk + over)
					if off+n > r.size {
						n = r.size - off
					}
					var rd uintptr
					if windows.ReadProcessMemory(h, uintptr(r.base+off), &buf[0], uintptr(n), &rd) != nil && rd == 0 {
						continue
					}
					for p := uint64(48); p+24*6 <= uint64(rd) && p < chunk; p += 8 {
						if le.Uint32(buf[p+16:]) != old {
							continue
						}
						// entry 2 of a board table: its neighbours are entries of the board too
						ok := 0
						for _, d := range []int{-48, -24, 0, 24, 48} {
							if _, in := t.slotIdx[le.Uint32(buf[uint64(int(p)+d)+20:])]; in {
								ok++
							}
						}
						if ok < 5 {
							continue
						}
						base := r.base + off + p - 48 - 16
						mu.Lock()
						bases = append(bases, base)
						mu.Unlock()
						line := fmt.Sprintf("LIVE:   old deck %d at table %#x, block %d MB, items:", old, base, r.size>>20)
						for i := 0; i < 8 && p-48+uint64(i)*24+24 <= uint64(rd); i++ {
							line += fmt.Sprintf(" %d", le.Uint32(buf[p-48+uint64(i)*24:][16:]))
						}
						mu.Lock()
						hits++
						if len(lines) < 40 {
							lines = append(lines, line)
						}
						mu.Unlock()
					}
				}
			}
		}()
	}
	for _, r := range regs {
		if r.size >= 4<<10 {
			jobs <- r
		}
	}
	close(jobs)
	wg.Wait()
	sort.Strings(lines)
	for _, l := range lines {
		tl.logf("%s", l)
	}
	tl.logf("LIVE: old-deck census: %d tables still hold the old deck (%.1f s)", hits, time.Since(start).Seconds())
	// Who holds each of them? A table is held by a recipe row (its items pointer sits 0x10 into the row), and a row by
	// the vector of rows of some record.
	targets := map[uint64]bool{}
	for _, b := range bases {
		targets[b+0x10] = true // the printed address is where the first entry's name pointer sits; a row holds the array 0x10 on
	}
	refs := pointersTo(h, regs, targets)
	rows := map[uint64]bool{}
	sort.Slice(bases, func(i, j int) bool { return bases[i] < bases[j] })
	for _, b := range bases {
		for _, r := range refs[b+0x10] {
			rows[r-0x10] = true
			res, _ := rd64(h, r-0x10)
			key, _ := rd32(h, res+0x38)
			tl.logf("LIVE:   table %#x is held at %#x (block %d MB), as the row %#x of template %#x", b+0x10, r, blockClass(regs, r), r-0x10, key)
		}
		if len(refs[b+0x10]) == 0 {
			tl.logf("LIVE:   table %#x is held by nothing that was found", b)
		}
	}
	rrefs := pointersTo(h, regs, rows)
	var rl []uint64
	for r := range rows {
		rl = append(rl, r)
	}
	sort.Slice(rl, func(i, j int) bool { return rl[i] < rl[j] })
	for _, r := range rl {
		for _, x := range rrefs[r] {
			tl.logf("LIVE:   row %#x is held at %#x (block %d MB)", r, x, blockClass(regs, x))
		}
		if len(rrefs[r]) == 0 {
			tl.logf("LIVE:   row %#x is held by nothing that was found (it may sit inside a vector)", r)
		}
	}
	tl.logf("LIVE: holders found (%.1f s in all)", time.Since(start).Seconds())
}

// pointersTo finds every place in the given regions that holds one of the target addresses (8-aligned).
func pointersTo(h windows.Handle, regs []memRegion, targets map[uint64]bool) map[uint64][]uint64 {
	out := map[uint64][]uint64{}
	var mu sync.Mutex
	jobs := make(chan memRegion)
	var wg sync.WaitGroup
	le := binary.LittleEndian
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer drainOnPanic("memory scan", jobs)
			const chunk = 8 << 20
			buf := make([]byte, chunk)
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
					for p := uint64(0); p+8 <= uint64(rd); p += 8 {
						v := le.Uint64(buf[p:])
						if v > 0x10000 && targets[v] {
							mu.Lock()
							if len(out[v]) < 6 {
								out[v] = append(out[v], r.base+off+p)
							}
							mu.Unlock()
						}
					}
				}
			}
		}()
	}
	for _, r := range regs {
		if r.size >= 4<<10 {
			jobs <- r
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

// tracePathTo lists every recipe row of the kind the game copied the board from (found by what it looks like: the
// board's table at +0x8 and the game's marker value at +0x10, +0x20, +0x30, +0x40 and +0x50), marking the one the game used.
func tracePathTo(h windows.Handle, c, target, owner uint64) {
	base := moduleBase(pid0)
	rows := findRecipeRows(h, base, boardSlotsOf(c), 4<<10, 0)
	regs := writableRegions(h)
	have := map[uint64]bool{}
	for _, r := range rows {
		have[r.row] = true
	}
	for _, r := range rows {
		run := 0
		for k := uint64(0); have[r.row+k*0x90]; k++ {
			run++
		}
		mark := ""
		if r.row == owner {
			mark = "   <- the row the game copied from"
		}
		tl.logf("ROWS: %#x (block %d MB) table %#x deck %d, %d more rows follow it%s", r.row, blockClass(regs, r.row), r.items, r.deck, run-1, mark)
	}
	tl.logf("ROWS: %d rows found; the game's was %#x", len(rows), owner)
}

type recipeRow struct {
	row, items uint64
	deck       uint32
}

var pid0 uint32

func boardSlotsOf(c uint64) map[uint32]int { return boardSlotIdx }

var boardSlotIdx map[uint32]int

// findRecipeRows scans the game's memory for rows that look like the one the game copied the board from.
func findRecipeRows(h windows.Handle, base uint64, slots map[uint32]int, minSize, maxSize uint64) []recipeRow {
	g := base + 0x64d4880
	regs, _ := residentRegions(h, minSize, maxSize, true)
	le := binary.LittleEndian
	var mu sync.Mutex
	var out []recipeRow
	jobs := make(chan memRegion)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer drainOnPanic("memory scan", jobs)
			const chunk = 8 << 20
			const over = 0x100
			buf := make([]byte, chunk+over)
			for r := range jobs {
				for off := uint64(0); off < r.size; off += chunk {
					n := uint64(chunk + over)
					if off+n > r.size {
						n = r.size - off
					}
					var rd uintptr
					if windows.ReadProcessMemory(h, uintptr(r.base+off), &buf[0], uintptr(n), &rd) != nil && rd == 0 {
						continue
					}
					var gp [8]byte
					le.PutUint64(gp[:], g)
					for from := 0x10; from+8 <= int(rd); {
						i := bytes.Index(buf[from:rd], gp[:])
						if i < 0 {
							break
						}
						pos := from + i
						from = pos + 1
						if p := uint64(pos - 0x10); (r.base+off+p)%8 != 0 || p+0x70 > uint64(rd) || p >= chunk {
							continue
						}
						p := uint64(pos - 0x10)
						if le.Uint64(buf[p+0x10:]) != g || le.Uint64(buf[p+0x20:]) != g || le.Uint64(buf[p+0x30:]) != g ||
							le.Uint64(buf[p+0x40:]) != g || le.Uint64(buf[p+0x50:]) != g || le.Uint64(buf[p+0x68:]) != 0x400000004 {
							continue
						}
						items := le.Uint64(buf[p+8:])
						if items < 0x10000 || items > 0x7fffffffffff {
							continue
						}
						var tb [4 + 24*8]byte
						var r2 uintptr
						if windows.ReadProcessMemory(h, uintptr(items-4), &tb[0], uintptr(len(tb)), &r2) != nil || int(r2) < len(tb) {
							continue
						}
						if le.Uint32(tb[:])&0x7fffffff != 8 {
							continue
						}
						hits := 0
						for i := 0; i < 8; i++ {
							if _, in := slots[le.Uint32(tb[4+i*24+20:])]; in {
								hits++
							}
						}
						if hits < 5 {
							continue
						}
						mu.Lock()
						out = append(out, recipeRow{r.base + off + p, items, le.Uint32(tb[4+2*24+16:])})
						mu.Unlock()
					}
				}
			}
		}()
	}
	for _, r := range regs {
		jobs <- r
	}
	close(jobs)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].row < out[j].row })
	return out
}

var codeDumped, mgrDumped bool

// dumpGameCode (only with a "dump_code" file in the data folder) logs the game's code around the place that copies the
// board into the component, and around its callers, as hex, so it can be read offline. Nothing is changed.
func dumpGameCode(h windows.Handle, pid uint32) {
	if codeDumped {
		return
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(liveCachePath), "dump_code")); err != nil {
		return
	}
	codeDumped = true
	base := moduleBase(pid)
	for _, r := range [][2]uint64{{0x191f120, 0x191f900}, {0x1915650, 0x1915d00}, {0x191ce20, 0x191d200}, {0x1917ed0, 0x1918400}} {
		for a := r[0]; a < r[1]; a += 32 {
			var b [32]byte
			var rd uintptr
			windows.ReadProcessMemory(h, uintptr(base+a), &b[0], 32, &rd)
			tl.logf("CODE %#x: %x", a, b[:rd])
		}
	}
}

// dumpManagerTables (with the "dump_code" file): the component asks an object it holds at +0x1a8 for the board's source
// recipe, by the key at +0x170. This lists the board tables reachable from that object and the pointer path to each.
func dumpManagerTables(h windows.Handle, c uint64, t liveTask) {
	if mgrDumped {
		return
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(liveCachePath), "dump_code")); err != nil {
		return
	}
	mgrDumped = true
	le := binary.LittleEndian
	mgr, _ := rd64(h, c+0x1a8)
	var key [16]byte
	var rd uintptr
	windows.ReadProcessMemory(h, uintptr(c+0x170), &key[0], 16, &rd)
	tl.logf("MGR: component %#x, object at +0x1a8: %#x, key bytes at +0x170: %x, [+0x134]: %#x", c, mgr, key[:], func() uint64 { v, _ := rd64(h, c+0x134); return v }())
	isTable := func(a uint64) (bool, uint32) {
		var buf [4 + 24*8]byte
		var r uintptr
		if windows.ReadProcessMemory(h, uintptr(a-4), &buf[0], uintptr(len(buf)), &r) != nil || int(r) < len(buf) {
			return false, 0
		}
		if n := le.Uint32(buf[:]) & 0x7fffffff; n != 8 {
			return false, 0
		}
		hits := 0
		for i := 0; i < 8; i++ {
			if _, in := t.slotIdx[le.Uint32(buf[4+i*24+20:])]; in {
				hits++
			}
		}
		return hits >= 5, le.Uint32(buf[4+2*24+16:])
	}
	type node struct {
		addr uint64
		path string
	}
	seen := map[uint64]bool{mgr: true}
	queue := []node{{mgr, "M"}}
	found := 0
	for depth := 0; depth < 5 && len(queue) > 0; depth++ {
		var next []node
		for _, n := range queue {
			if len(seen) > 150000 {
				break
			}
			buf := make([]byte, 0x300)
			var r uintptr
			if windows.ReadProcessMemory(h, uintptr(n.addr), &buf[0], uintptr(len(buf)), &r) != nil && r == 0 {
				continue
			}
			for off := uintptr(0); off+8 <= r; off += 8 {
				v := le.Uint64(buf[off:])
				if v <= 0x10000 || v >= 0x7fffffffffff || v&3 != 0 || seen[v] {
					continue
				}
				seen[v] = true
				if ok, deck := isTable(v); ok {
					found++
					if found <= 60 {
						tl.logf("MGR:   table %#x via %s+%#x (deck %d)", v, n.path, off, deck)
					}
				}
				next = append(next, node{v, fmt.Sprintf("%s+%#x", n.path, off)})
			}
		}
		queue = next
	}
	tl.logf("MGR: %d board tables reachable from the object, %d objects looked at", found, len(seen))
}

var (
	rowMu    sync.Mutex
	rowPID   uint32
	rowCache []recipeRow
)

// boardSourceTable returns the table of the recipe row the game copies the local board from: the first row of the run
// of board rows (they follow each other 0x90 apart). Where the rows are is remembered for the rest of the game run.
func boardSourceTable(h windows.Handle, pid uint32, slots map[uint32]int) uint64 {
	base := moduleBase(pid)
	if base == 0 {
		return 0
	}
	g := base + 0x64d4880
	valid := func(r recipeRow) bool {
		for _, off := range []uint64{0x10, 0x20, 0x30, 0x40, 0x50} {
			if v, ok := rd64(h, r.row+off); !ok || v != g {
				return false
			}
		}
		items, ok := rd64(h, r.row+8)
		return ok && items == r.items
	}
	rowMu.Lock()
	defer rowMu.Unlock()
	if rowPID != pid {
		rowPID, rowCache = pid, nil
	}
	var rows []recipeRow
	for _, r := range rowCache {
		if valid(r) {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 || len(rows) != len(rowCache) {
		for _, r := range [][2]uint64{{129 << 20, 300 << 20}, {40 << 20, 300 << 20}, {4 << 10, 0}} {
			st := time.Now()
			rows = findRecipeRows(h, base, slots, r[0], r[1])
			tl.logf("LIVE: board rows: %d found in blocks of %d to %d MB, %.1f s", len(rows), r[0]>>20, r[1]>>20, time.Since(st).Seconds())
			if len(rows) > 0 {
				break
			}
		}
		rowCache = rows
		tl.logf("LIVE: board rows: %d found", len(rows))
	}
	have := map[uint64]bool{}
	for _, r := range rows {
		have[r.row] = true
	}
	var best recipeRow
	bestRun := 0
	for _, r := range rows {
		if have[r.row-0x90] {
			continue // not the first of its run
		}
		run := 0
		for k := uint64(0); have[r.row+k*0x90]; k++ {
			run++
		}
		if run > bestRun {
			best, bestRun = r, run
		}
	}
	if bestRun == 0 {
		return 0
	}
	{
		regs := writableRegions(h)
		var mb uint64
		for _, r := range regs {
			if r.base <= best.row && best.row < r.base+r.size {
				for _, q := range regs {
					if q.alloc == r.alloc {
						mb += q.size
					}
				}
			}
		}
		tl.logf("LIVE: board rows: their memory block holds %d MB", mb>>20)
	}
	tl.logf("LIVE: board rows: using the first of %d, at %#x (table %#x)", bestRun, best.row, best.items)
	lastRows = rows
	return best.items
}

// ---- the board ----

// lastRows are the board rows boardSourceTable found last.
var lastRows []recipeRow

var boardMu sync.Mutex

// writeBoardAt puts the new board into the 8-entry table at ta.
func writeBoardAt(h windows.Handle, t liveTask, ta uint64) bool {
	var es []liveEnt
	for i := uint64(0); i < 8; i++ {
		ad := ta + i*24 + 16
		item, slot, ok := readEnt(h, ad)
		if !ok {
			continue
		}
		if ix, in := t.slotIdx[slot]; in {
			es = append(es, liveEnt{ad, ix, item})
		}
	}
	return len(es) >= 5 && writeTable(h, t, es)
}

func deckOf(h windows.Handle, t liveTask, ta uint64) int64 {
	for i := uint64(0); i < 8; i++ {
		item, slot, ok := readEnt(h, ta+i*24+16)
		if ok && t.slotIdx[slot] == 2 {
			return int64(item)
		}
	}
	return -1
}

// boardApply: at a load it only finds where the board lives (so the second look is quick). At the second look,
// after the replay, it writes the new board into the table the game copies the board from (the preset menu reads
// the same table) and flags the board component's items as changed, so the game copies it into the world.
func boardApply(h windows.Handle, pid uint32, t liveTask, sweep bool) {
	boardMu.Lock()
	defer boardMu.Unlock()
	tc := time.Now()
	boards := boardComponents(h, pid)
	if len(boards) != 1 {
		tl.logf("LIVE: board not written: %d board components found, expected 1", len(boards))
		return
	}
	if !sweep {
		tl.logf("LIVE: board component found in %.1f s", time.Since(tc).Seconds())
	}
	c := boards[0]
	t0 := time.Now()
	src := boardSourceTable(h, pid, t.slotIdx)
	if !sweep {
		tl.logf("LIVE: board located in %.1f s", time.Since(t0).Seconds())
		tt := t
		boardJob = &tt // written at the replay refresh
		boardJobDone = false
		return // found for later
	}
	if boardJobDone {
		boardJobDone = false
		tl.logf("LIVE: board was already written during the replay")
		return
	}
	boardWriteLocked(h, pid, t, c, src)
}

// boardApplyNow does the write at once (the replay is closing).
func boardApplyNow(h windows.Handle, pid uint32, t liveTask) {
	boardMu.Lock()
	defer boardMu.Unlock()
	boards := boardComponents(h, pid)
	if len(boards) != 1 {
		tl.logf("LIVE: board not written: %d board components found, expected 1", len(boards))
		return
	}
	src := boardSourceTable(h, pid, t.slotIdx)
	if boardWriteLocked(h, pid, t, boards[0], src) {
		boardJobDone = true
	}
}

func boardWriteLocked(h windows.Handle, pid uint32, t liveTask, c, src uint64) bool {
	if src == 0 {
		tl.logf("LIVE: board not written: the game's source table wasn't found")
		return false
	}
	if !writeBoardAt(h, t, src) {
		tl.logf("LIVE: board: nothing written into the source table %#x (the board's items may not be installed)", src)
		return false
	}
	one := []byte{1}
	var w uintptr
	if _, err := os.Stat(filepath.Join(filepath.Dir(liveCachePath), "no_flag")); err == nil {
		tl.logf("LIVE: board: no_flag is set, the component isn't told")
	} else {
		windows.WriteProcessMemory(h, uintptr(c+0x10a), &one[0], 1, &w)
	}
	// wait until the game has copied it into the component (about 50 ms)
	var own uint64
	waited := time.Now()
	for time.Since(waited) < time.Second {
		own, _ = rd64(h, c+0x158)
		if deckOf(h, t, own) == deckOf(h, t, src) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tl.logf("LIVE: board written into the source table %#x and the component told; deck now %d in the source, %d in the component (%d ms)", src, deckOf(h, t, src), deckOf(h, t, own), time.Since(waited).Milliseconds())
	return true
}

// The board to write at the next replay refresh (the last load's), and whether boardEarly already did.
var (
	boardJob     *liveTask
	boardJobDone bool
)

// boardEarly writes the waiting board while the replay is closing, behind the black cover, instead of after it.
func boardEarly() {
	boardMu.Lock()
	job := boardJob
	boardMu.Unlock()
	pid := findSkatePID()
	if job == nil || pid == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.PROCESS_VM_WRITE|windows.PROCESS_VM_OPERATION, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	tl.logf("LIVE: board early: writing during the replay")
	boardApplyNow(h, pid, *job)
}

// warmBoard finds the board component and the game's board rows ahead of the first fit, so the first fit doesn't
// wait for the search. Nothing is changed in the game.
func warmBoard() {
	pid := findSkatePID()
	if pid == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	boardMu.Lock()
	defer boardMu.Unlock()
	t0 := time.Now()
	boards := boardComponents(h, pid)
	if len(boards) != 1 {
		tl.logf("LIVE: board ahead of time: %d board components found, expected 1", len(boards))
		return
	}
	own, _ := rd64(h, boards[0]+0x158)
	slots := map[uint32]int{}
	for i := uint64(0); i < 8; i++ {
		if _, slot, ok := readEnt(h, own+i*24+16); ok {
			slots[slot] = int(i)
		}
	}
	if boardSourceTable(h, pid, slots) != 0 {
		tl.logf("LIVE: board found ahead of time in %.1f s", time.Since(t0).Seconds())
	}
}
