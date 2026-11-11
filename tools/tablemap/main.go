//go:build windows

// tablemap (read-only): which of the outfit tables in the running game are real, and who points at them?
// It only READS Skate.exe's memory. It never writes, injects or hooks anything.
//
// Run it with the game open and your skater standing in the world with no menu open. It finds every
// skater outfit table (a run of 24-byte entries: item hash, slot hash, 16 bytes), then looks through memory for
// pointers to those tables, and then for pointers to whatever holds those pointers. A table that something
// points at is probably live; a table nothing points at is probably freed memory with old bytes left in it.
// Writes tablemap_<label>.txt next to the exe. It takes a minute or two and the game will stutter meanwhile.
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const entry = 24

// the skater table's slots, in the order the file keeps them (from the app's own log)
var slots = []uint32{2601889598, 1952629119, 2889696005, 2890690067, 3228215319, 902165545, 4223317014, 870817456,
	878015887, 2890175425, 3161036499, 3351033597, 1787581620, 1311571609, 3991046852, 4052126180, 2744863983,
	1876118485, 3972308227, 2323857215, 3684982372, 3049576345, 1849983682, 4170730925, 841072201, 3028857462,
	902472012, 2700041961, 3335133427, 985782260, 881743303, 1345629699, 4183344496, 396566312, 14387044, 876665555,
	2890016738, 3051755856, 3162643500, 1327235081, 3035368275, 3035345869, 2802720648, 2817308003, 2817318397,
	1099591108, 4185861819, 978921364, 292570831, 2983562079, 2983562076, 1276597169, 4058999366, 4058999365, 4058999364}

var slotIdx = map[uint32]int{}

type region struct {
	base, size uint64
	typ        uint32 // 0x20000 private, 0x1000000 image
}

type ent struct {
	addr uint64
	idx  int
	item uint32
}

type table struct {
	start uint64
	ents  []ent
	hash  uint64
}

type ref struct {
	holder, value uint64
	target        int
}

var h windows.Handle
var out *bufio.Writer

func logf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	fmt.Println(s)
	fmt.Fprintln(out, s)
}

func findPID() uint32 {
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

type module struct {
	name       string
	base, size uint64
}

func modules(pid uint32) []module {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, pid)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ModuleEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var ms []module
	for err = windows.Module32First(snap, &e); err == nil; err = windows.Module32Next(snap, &e) {
		ms = append(ms, module{windows.UTF16ToString(e.Module[:]), uint64(e.ModBaseAddr), uint64(e.ModBaseSize)})
	}
	return ms
}

func regions(images bool) []region {
	var out []region
	var addr uintptr
	for {
		var mbi windows.MemoryBasicInformation
		if err := windows.VirtualQueryEx(h, addr, &mbi, unsafe.Sizeof(mbi)); err != nil {
			break
		}
		const commit, guard = 0x1000, 0x100
		readable := mbi.Protect&(0x02|0x04|0x08|0x20|0x40|0x80) != 0
		if mbi.State == commit && mbi.Protect&guard == 0 && readable {
			if mbi.Type == 0x20000 && mbi.Protect&(0x04|0x08|0x40|0x80) != 0 {
				out = append(out, region{uint64(mbi.BaseAddress), uint64(mbi.RegionSize), 0x20000})
			} else if images && mbi.Type == 0x1000000 {
				out = append(out, region{uint64(mbi.BaseAddress), uint64(mbi.RegionSize), 0x1000000})
			}
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr {
			break
		}
		addr = next
	}
	return out
}

func readEnt(a uint64) (item, slot uint32, ok bool) {
	var b [8]byte
	var n uintptr
	if windows.ReadProcessMemory(h, uintptr(a), &b[0], 8, &n) != nil || n != 8 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint32(b[:]), binary.LittleEndian.Uint32(b[4:]), true
}

// the run of entries around one whose slot is in the list, in the file's order
func walk(addr uint64) []ent {
	item, slot, ok := readEnt(addr)
	if !ok {
		return nil
	}
	ai, ok := slotIdx[slot]
	if !ok {
		return nil
	}
	var back []ent
	last := ai
	for k := 1; k <= len(slots); k++ {
		a := addr - uint64(k*entry)
		it, sl, ok := readEnt(a)
		if !ok {
			break
		}
		i, in := slotIdx[sl]
		if !in || i >= last {
			break
		}
		back = append(back, ent{a, i, it})
		last = i
	}
	var ents []ent
	for i := len(back) - 1; i >= 0; i-- {
		ents = append(ents, back[i])
	}
	ents = append(ents, ent{addr, ai, item})
	last = ai
	for k := 1; k <= len(slots); k++ {
		a := addr + uint64(k*entry)
		it, sl, ok := readEnt(a)
		if !ok {
			break
		}
		i, in := slotIdx[sl]
		if !in || i <= last {
			break
		}
		ents = append(ents, ent{a, i, it})
		last = i
	}
	return ents
}

const chunk = 4 << 20

// runs fn over every chunk of the regions, in parallel
func scanAll(regs []region, fn func(base uint64, buf []byte, own int)) {
	jobs := make(chan [2]uint64, 64)
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, chunk+64)
			for j := range jobs {
				n := j[1] + 64
				var rd uintptr
				if windows.ReadProcessMemory(h, uintptr(j[0]), &buf[0], uintptr(n), &rd) != nil && rd == 0 {
					continue
				}
				own := int(j[1])
				if own > int(rd) {
					own = int(rd)
				}
				fn(j[0], buf[:rd], own)
			}
		}()
	}
	for _, r := range regs {
		for off := uint64(0); off < r.size; off += chunk {
			n := r.size - off
			if n > chunk {
				n = chunk
			}
			jobs <- [2]uint64{r.base + off, n}
		}
	}
	close(jobs)
	wg.Wait()
}

// pointers (8-aligned 64-bit values) that land in any of the ranges
func findRefs(regs []region, ranges [][2]uint64) []ref {
	order := make([]int, len(ranges))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return ranges[order[a]][0] < ranges[order[b]][0] })
	const shift = 10
	var maxLen uint64
	for _, r := range ranges {
		if r[1]-r[0] > maxLen {
			maxLen = r[1] - r[0]
		}
	}
	bloom := map[uint64]bool{}
	for _, r := range ranges {
		for b := r[0] >> shift; b <= r[1]>>shift; b++ {
			bloom[b] = true
		}
	}
	var mu sync.Mutex
	var refs []ref
	scanAll(regs, func(base uint64, buf []byte, own int) {
		var local []ref
		for i := 0; i+8 <= len(buf) && i < own; i += 8 {
			v := binary.LittleEndian.Uint64(buf[i:])
			if v < 0x10000 || v > 0x7fffffffffff || v&7 != 0 || !bloom[v>>shift] {
				continue
			}
			// the last range starting at or below v, then back while a range could still reach it
			k := sort.Search(len(order), func(x int) bool { return ranges[order[x]][0] > v }) - 1
			for ; k >= 0; k-- {
				r := ranges[order[k]]
				if r[0]+maxLen < v {
					break
				}
				if v >= r[0] && v <= r[1] {
					local = append(local, ref{base + uint64(i), v, order[k]})
				}
			}
		}
		if len(local) > 0 {
			mu.Lock()
			refs = append(refs, local...)
			mu.Unlock()
		}
	})
	return refs
}

func main() {
	for i, s := range slots {
		slotIdx[s] = i
	}
	fmt.Print("A label for this run (for example idle, after-switch, after-replay): ")
	in := bufio.NewReader(os.Stdin)
	label, _ := in.ReadString('\n')
	label = strings.TrimSpace(label)
	if label == "" {
		label = time.Now().Format("150405")
	}
	exe, _ := os.Executable()
	f, err := os.Create(filepath.Join(filepath.Dir(exe), "tablemap_"+label+".txt"))
	if err != nil {
		fmt.Println("can't write the report:", err)
		fmt.Scanln()
		return
	}
	out = bufio.NewWriter(f)
	defer func() { out.Flush(); f.Close() }()

	pid := findPID()
	if pid == 0 {
		logf("skate. isn't running")
		return
	}
	h, err = windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		logf("couldn't open skate.: %v", err)
		return
	}
	defer windows.CloseHandle(h)
	t0 := time.Now()
	logf("tablemap %q at %s, game pid %d", label, time.Now().Format("15:04:05"), pid)
	mods := modules(pid)
	for _, m := range mods {
		if strings.EqualFold(m.name, "Skate.exe") {
			logf("module %s base %#x size %d MB", m.name, m.base, m.size>>20)
		}
	}
	modOf := func(a uint64) string {
		for _, m := range mods {
			if a >= m.base && a < m.base+m.size {
				return fmt.Sprintf("%s+%#x", m.name, a-m.base)
			}
		}
		return ""
	}
	priv := regions(false)
	all := regions(true)
	regionOf := func(a uint64) string {
		i := sort.Search(len(all), func(i int) bool { return all[i].base+all[i].size > a })
		if i < len(all) && all[i].base <= a {
			t := "private"
			if all[i].typ == 0x1000000 {
				t = "image"
			}
			return fmt.Sprintf("%s region %#x (%d KB)", t, all[i].base, all[i].size>>10)
		}
		return "unknown region"
	}

	// 1. every skater table: Headwear, Top and Bottoms entries in a row
	logf("step 1: finding tables (%d regions)...", len(priv))
	var tmu sync.Mutex
	byStart := map[uint64]*table{}
	scanAll(priv, func(base uint64, buf []byte, own int) {
		for i := 0; i+56 <= len(buf) && i < own; i += 8 {
			if binary.LittleEndian.Uint32(buf[i+4:]) != slots[2] || binary.LittleEndian.Uint32(buf[i+28:]) != slots[3] || binary.LittleEndian.Uint32(buf[i+52:]) != slots[4] {
				continue
			}
			ents := walk(base + uint64(i))
			if len(ents) < 10 {
				continue
			}
			hh := fnv.New64a()
			for _, e := range ents {
				var b [8]byte
				binary.LittleEndian.PutUint32(b[:], uint32(e.idx))
				binary.LittleEndian.PutUint32(b[4:], e.item)
				hh.Write(b[:])
			}
			tmu.Lock()
			byStart[ents[0].addr] = &table{ents[0].addr, ents, hh.Sum64()}
			tmu.Unlock()
		}
	})
	var tables []*table
	for _, t := range byStart {
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].start < tables[j].start })
	logf("found %d tables in %.0f s", len(tables), time.Since(t0).Seconds())

	// 2. who points at each table
	logf("step 2: pointers to the tables (%d regions including the program's own data)...", len(all))
	// The game's own recipe points at the array of items, whose first field is the 16 bytes before the first hash
	// (an item is {name pointer, parameters pointer, hash, slot}), so look for pointers to both places.
	nt := len(tables)
	ranges := make([][2]uint64, 2*nt)
	for i, t := range tables {
		ranges[i] = [2]uint64{t.start, t.start}
		ranges[nt+i] = [2]uint64{t.start - 16, t.start - 16}
	}
	refs := findRefs(all, ranges)
	refsOf := map[int][]ref{}
	var arrayRefs []ref // pointers to the array's base: the recipe objects
	for _, r := range refs {
		if r.target >= nt {
			r.target -= nt
			arrayRefs = append(arrayRefs, r)
		}
		refsOf[r.target] = append(refsOf[r.target], r)
	}
	logf("%d pointers found in %.0f s", len(refs), time.Since(t0).Seconds())

	// 3. who points at whatever holds those pointers (the object they are a field of): anything pointing a little before
	var holders []uint64
	seenH := map[uint64]bool{}
	for _, r := range refs {
		if !seenH[r.holder] && len(holders) < 300 {
			seenH[r.holder] = true
			holders = append(holders, r.holder)
		}
	}
	logf("step 3: pointers to the %d things that hold them...", len(holders))
	hr := make([][2]uint64, len(holders))
	for i, a := range holders {
		hr[i] = [2]uint64{a - 0x400, a}
	}
	refs2 := findRefs(all, hr)
	refs2Of := map[int][]ref{}
	for _, r := range refs2 {
		refs2Of[r.target] = append(refs2Of[r.target], r)
	}
	logf("%d pointers found in %.0f s", len(refs2), time.Since(t0).Seconds())

	// report
	logf("")
	logf("=== tables by what they hold (same hash = same outfit) ===")
	type group struct {
		hash     uint64
		n, refd  int
		example  *table
		nRefsSum int
	}
	groups := map[uint64]*group{}
	for i, t := range tables {
		g := groups[t.hash]
		if g == nil {
			g = &group{hash: t.hash, example: t}
			groups[t.hash] = g
		}
		g.n++
		if len(refsOf[i]) > 0 {
			g.refd++
			g.nRefsSum += len(refsOf[i])
		}
	}
	var gs []*group
	for _, g := range groups {
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].n > gs[j].n })
	item := func(t *table, idx int) uint32 {
		for _, e := range t.ents {
			if e.idx == idx {
				return e.item
			}
		}
		return 0
	}
	for _, g := range gs {
		t := g.example
		logf("outfit %016x: %d tables, %d of them pointed at; entries %d; headwear %d, top %d, bottoms %d, shoes %d", g.hash, g.n, g.refd, len(t.ents), item(t, 2), item(t, 3), item(t, 4), item(t, 5))
	}
	nref := 0
	for i := range tables {
		if len(refsOf[i]) > 0 {
			nref++
		}
	}
	logf("")
	logf("=== %d of %d tables have something pointing at them ===", nref, len(tables))
	for i, t := range tables {
		if len(refsOf[i]) == 0 {
			continue
		}
		logf("table %#x (%s), outfit %016x, headwear %d top %d bottoms %d, %d pointers:", t.start, regionOf(t.start), t.hash, item(t, 2), item(t, 3), item(t, 4), len(refsOf[i]))
		for k, r := range refsOf[i] {
			if k >= 6 {
				logf("    ... and %d more", len(refsOf[i])-k)
				break
			}
			extra := ""
			if m := modOf(r.holder); m != "" {
				extra = " = " + m + " (in the program's own data)"
			}
			logf("    held at %#x in %s%s", r.holder, regionOf(r.holder), extra)
			// what points at the holder's object
			hi := -1
			for x, a := range holders {
				if a == r.holder {
					hi = x
				}
			}
			if hi >= 0 {
				for kk, r2 := range refs2Of[hi] {
					if kk >= 4 {
						break
					}
					extra2 := ""
					if m := modOf(r2.holder); m != "" {
						extra2 = " = " + m
					}
					logf("        that is inside an object %d bytes below... pointed to by %#x in %s%s (points at %#x)", r.holder-r2.value, r2.holder, regionOf(r2.holder), extra2, r2.value)
				}
			}
		}
	}
	logf("")
	logf("=== recipes: objects holding a pointer to a table's array (table start minus 16) ===")
	u32 := func(a uint64) (uint32, bool) {
		b := readBytes(a, 4)
		if b == nil {
			return 0, false
		}
		return binary.LittleEndian.Uint32(b), true
	}
	u8 := func(a uint64) (byte, bool) {
		b := readBytes(a, 1)
		if b == nil {
			return 0, false
		}
		return b[0], true
	}
	for _, r := range arrayRefs {
		if r.target < 0 || r.target >= len(tables) {
			continue
		}
		t := tables[r.target]
		comp := r.holder - 0x158 // if this is the recipe at component+0x150, its items pointer is at +8
		var w [4]int64
		for k, off := range []uint64{0x138, 0x13c, 0x144, 0x148} {
			if v, ok := u32(comp + off); ok {
				w[k] = int64(v)
			} else {
				w[k] = -1
			}
		}
		f1, _ := u8(comp + 0x10a)
		f2, _ := u8(comp + 0x10d)
		logf("table %#x (outfit %016x, top %d): pointer held at %#x in %s; if that is a recipe, the item component is %#x: feed words %v, items-changed %d, feed-changed %d", t.start, t.hash, item(t, 3), r.holder, regionOf(r.holder), comp, w, f1, f2)
	}
	logf("=== raw records around each holder (0x120 bytes before, 0x90 after; dwords) ===")
	for _, r := range arrayRefs {
		if r.target < 0 || r.target >= len(tables) {
			continue
		}
		t := tables[r.target]
		base := r.holder - 0x120
		b := readBytes(base, 0x120+0x90)
		if b == nil {
			continue
		}
		logf("holder %#x -> table %#x (top %d)", r.holder, t.start, item(t, 3))
		for o := 0; o+16 <= len(b); o += 16 {
			mark := ""
			if base+uint64(o) <= r.holder && r.holder < base+uint64(o)+16 {
				mark = "  <-- holder"
			}
			logf("  %+05x: %08x %08x %08x %08x%s", int(base)+o-int(r.holder), binary.LittleEndian.Uint32(b[o:]), binary.LittleEndian.Uint32(b[o+4:]), binary.LittleEndian.Uint32(b[o+8:]), binary.LittleEndian.Uint32(b[o+12:]), mark)
		}
	}
	if len(arrayRefs) == 0 {
		logf("no pointers to any table's array were found")
	}
	logf("")
	logf("=== every table, by address (P = something points at it) ===")
	order := make([]int, len(tables))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return tables[order[a]].start < tables[order[b]].start })
	for _, i := range order {
		t := tables[i]
		mark := " "
		if len(refsOf[i]) > 0 {
			mark = "P"
		}
		pre := ""
		if b := readBytes(t.start-32, 32); b != nil {
			pre = fmt.Sprintf("%x", b)
		}
		logf("%s %#x outfit %016x top %d bottoms %d shoes %d  32 bytes before: %s", mark, t.start, t.hash, item(t, 3), item(t, 4), item(t, 5), pre)
	}
	logf("")
	logf("=== full contents of the tables that are pointed at ===")
	for i, t := range tables {
		if len(refsOf[i]) == 0 {
			continue
		}
		logf("table %#x:", t.start)
		for _, e := range t.ents {
			logf("    [%d] item %d", e.idx, e.item)
		}
	}
	logf("")
	logf("done in %.0f s", time.Since(t0).Seconds())
	fmt.Println("Done. The report is tablemap_" + label + ".txt next to this program. Press Enter to close.")
	fmt.Scanln()
}

func readBytes(a uint64, n int) []byte {
	buf := make([]byte, n)
	var got uintptr
	if windows.ReadProcessMemory(h, uintptr(a), &buf[0], uintptr(n), &got) != nil || int(got) != n {
		return nil
	}
	return buf
}
