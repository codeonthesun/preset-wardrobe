//go:build windows

// probe: which of the skater's outfit tables does the game really read?
// It finds every skater table, then changes the Top item in half of them (a different, ordinary shirt),
// asks whether your skater changed, puts the old item back, and tries a smaller half, until it has found the table(s)
// the game reads. It only writes four bytes per table, and puts each back (also if you close it with Ctrl+C).
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
	"os"
	"os/signal"
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

type keyInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	_     uint32
	extra uintptr
	_     [8]byte
}

func pressKey(scan uint16) {
	const inputKeyboard, scancode, keyUp = 1, 0x0008, 0x0002
	send := windows.NewLazySystemDLL("user32.dll").NewProc("SendInput")
	for _, flags := range []uint32{scancode, scancode | keyUp} {
		in := keyInput{typ: inputKeyboard, scan: scan, flags: flags}
		send.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
		if flags&keyUp == 0 {
			time.Sleep(70 * time.Millisecond)
		}
	}
}

func readBytes(a uint64, n int) []byte {
	buf := make([]byte, n)
	var got uintptr
	if windows.ReadProcessMemory(h, uintptr(a), &buf[0], uintptr(n), &got) != nil || int(got) != n {
		return nil
	}
	return buf
}

type write struct {
	addr       uint64
	orig, mine uint32
}

var (
	pending []write
	pmu     sync.Mutex
	in      *bufio.Reader
)

func put(addr uint64, v uint32) bool {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	var n uintptr
	return windows.WriteProcessMemory(h, uintptr(addr), &b[0], 4, &n) == nil && n == 4
}

// puts every changed item back, unless the game has changed it since
func restore() {
	pmu.Lock()
	defer pmu.Unlock()
	for _, w := range pending {
		var b [4]byte
		var n uintptr
		if windows.ReadProcessMemory(h, uintptr(w.addr), &b[0], 4, &n) != nil || n != 4 {
			continue
		}
		if binary.LittleEndian.Uint32(b[:]) == w.mine {
			put(w.addr, w.orig)
		} else {
			logf("   (the game had already changed %#x itself: left alone)", w.addr)
		}
	}
	pending = nil
}

func ask(q string) string {
	for {
		fmt.Print(q)
		s, _ := in.ReadString('\n')
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "y" || s == "n" {
			return s
		}
		fmt.Println("   please type y or n and press Enter")
	}
}

func main() {
	for i, s := range slots {
		slotIdx[s] = i
	}
	in = bufio.NewReader(os.Stdin)
	fmt.Print("A label for this run (for example normal): ")
	label, _ := in.ReadString('\n')
	label = strings.TrimSpace(label)
	if label == "" {
		label = time.Now().Format("150405")
	}
	exe, _ := os.Executable()
	f, err := os.Create(filepath.Join(filepath.Dir(exe), "probe_"+label+".txt"))
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
		fmt.Scanln()
		return
	}
	h, err = windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.PROCESS_VM_WRITE|windows.PROCESS_VM_OPERATION, false, pid)
	if err != nil {
		logf("couldn't open skate.: %v", err)
		fmt.Scanln()
		return
	}
	defer windows.CloseHandle(h)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		restore()
		os.Exit(1)
	}()
	logf("probe %q at %s, game pid %d", label, time.Now().Format("15:04:05"), pid)
	fmt.Println("Finding the skater tables (about 15 seconds)...")
	priv := regions(false)
	var tmu sync.Mutex
	byStart := map[uint64]*table{}
	scanAll(priv, func(base uint64, buf []byte, own int) {
		for i := 0; i+56 <= len(buf) && i < own; i += 8 {
			if binary.LittleEndian.Uint32(buf[i+4:]) != slots[3] { // any entry for the Top slot: the game drops some entries, so don't ask for its neighbours
				continue
			}
			ents := walk(base + uint64(i))
			if len(ents) < 8 {
				continue
			}
			tmu.Lock()
			byStart[ents[0].addr] = &table{start: ents[0].addr, ents: ents}
			tmu.Unlock()
		}
	})
	type cand struct {
		start, top uint64
		orig, mine uint32
		hdr        []byte
		items      []uint32
	}
	var cands []*cand
	var starts []uint64
	for a := range byStart {
		starts = append(starts, a)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	probes := []uint32{1946648020, 1946648018, 425238997, 2722333476}
	for _, a := range starts {
		t := byStart[a]
		hdr := readBytes(a-32, 32)
		if len(hdr) != 32 {
			hdr = make([]byte, 32)
		}
		var top *ent
		var items []uint32
		for k := range t.ents {
			items = append(items, t.ents[k].item)
			if t.ents[k].idx == 3 {
				top = &t.ents[k]
			}
		}
		if top == nil {
			continue
		}
		c := &cand{start: a, top: top.addr, orig: top.item, hdr: hdr, items: items}
		for _, p := range probes {
			if p != c.orig {
				c.mine = p
				break
			}
		}
		cands = append(cands, c)
	}
	logf("found %d skater tables, %d to test", len(byStart), len(cands))
	if len(cands) < 2 {
		logf("not enough tables to test")
		fmt.Scanln()
		return
	}
	fmt.Println()
	fmt.Println("Look at your skater in the world. Each round the tool changes your skater's top in some tables")
	fmt.Println("to a plain shirt and asks you if the top changed.")
	fmt.Println("  1 = does the skater change on its own right away?")
	fmt.Println("  2 = does it change after the replay refresh (the tool presses B, Esc, Space itself)?")
	mode := ""
	for mode != "1" && mode != "2" {
		fmt.Print("Which test, 1 or 2? ")
		mode, _ = in.ReadString('\n')
		mode = strings.TrimSpace(mode)
	}
	logf("test %s", mode)
	round := 0
	test := func(set []*cand) bool {
		round++
		pmu.Lock()
		pending = nil
		for _, c := range set {
			if put(c.top, c.mine) {
				pending = append(pending, write{c.top, c.orig, c.mine})
			}
		}
		pmu.Unlock()
		logf("round %d: changed the top in %d tables", round, len(set))
		fmt.Println("   look at the skater for a few seconds, without touching anything...")
		time.Sleep(4 * time.Second)
		ans := ask("   did the skater's top change? (y/n) ")
		if ans == "n" && mode == "2" {
			fmt.Println("   now click on the game window. The tool presses B, Esc, Space in 6 seconds...")
			time.Sleep(6 * time.Second)
			pressKey(0x30)
			time.Sleep(120 * time.Millisecond)
			pressKey(0x01)
			time.Sleep(120 * time.Millisecond)
			pressKey(0x39)
			time.Sleep(5 * time.Second)
			ans = ask("   did the skater's top change after the refresh? (y/n) ")
		}
		restore()
		logf("   answer: %s", ans)
		if mode == "2" || ans == "y" {
			fmt.Println("   put back. If your skater changed, wait for it to come back before the next round.")
			time.Sleep(3 * time.Second)
		}
		return ans == "y"
	}
	var found []*cand
	rest := cands
	for len(rest) > 0 {
		if !test(rest) {
			logf("none of the other %d tables changes the skater", len(rest))
			break
		}
		set := rest
		for len(set) > 1 {
			half := set[:len(set)/2]
			if test(half) {
				set = half
			} else {
				set = set[len(set)/2:]
			}
		}
		c := set[0]
		found = append(found, c)
		logf("FOUND: the game reads the table at %#x", c.start)
		var next []*cand
		for _, x := range rest {
			if x != c {
				next = append(next, x)
			}
		}
		rest = next
	}
	logf("")
	logf("=== the tables the game reads (%d) ===", len(found))
	for _, c := range found {
		logf("table %#x, %d entries, 32 bytes before it: %x, Top %d", c.start, len(c.items), c.hdr, c.orig)
	}
	logf("")
	logf("=== all the %d tables tested ===", len(cands))
	isFound := map[*cand]bool{}
	for _, c := range found {
		isFound[c] = true
	}
	for _, c := range cands {
		mark := " "
		if isFound[c] {
			mark = "*"
		}
		logf("%s %#x %d entries, Top %d  header %x", mark, c.start, len(c.items), c.orig, c.hdr)
	}
	fmt.Println("Done. The report is probe_" + label + ".txt next to this program. Press Enter to close.")
	fmt.Scanln()
}
