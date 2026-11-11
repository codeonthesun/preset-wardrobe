//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// A count of every outfit table in the game's memory, by what it holds and whose it is. Reads only: it is
// there to find out why a table the game uses isn't being changed.
func liveCensus(h windows.Handle, regs []memRegion, tasks []liveTask, old, next []StoredPreset) {
	t0 := time.Now()
	names := map[uint32]string{}
	type belief struct {
		label  string
		isNext bool
		items  map[int]map[uint32]uint32 // group -> slot -> item
	}
	var beliefs []belief
	add := func(list []StoredPreset, tag string, isNext bool) {
		for _, p := range list {
			gs, err := parseRecipes(p.RecipesJSON)
			if err != nil {
				continue
			}
			b := belief{label: p.PresetID + " " + tag, isNext: isNext, items: map[int]map[uint32]uint32{}}
			for gi, g := range gs {
				m := map[uint32]uint32{}
				for _, it := range g.Items {
					m[uint32(it.Slot)] = itemHash(it.Asset)
					if it.Asset != "" {
						names[itemHash(it.Asset)] = it.Asset
					}
				}
				b.items[gi] = m
			}
			beliefs = append(beliefs, b)
		}
	}
	add(old, "as the app thinks the game has it", false)
	add(next, "the fit being loaded", true)

	byGroup := map[int]liveTask{}
	for _, t := range tasks {
		if _, ok := byGroup[t.group]; !ok {
			byGroup[t.group] = t
		}
	}
	type sig struct {
		group      int
		t          liveTask
		s0, s1, s2 uint32
	}
	var sigs []sig
	for g, t := range byGroup {
		if len(t.slots) < 6 {
			continue
		}
		k := len(t.slots) / 2
		sigs = append(sigs, sig{g, t, t.slots[k], t.slots[k+1], t.slots[k+2]})
	}
	if len(sigs) == 0 {
		return
	}
	nameOf := func(it uint32) string {
		if it == 0 {
			return "(empty)"
		}
		if n, ok := names[it]; ok {
			return n
		}
		return fmt.Sprint(it)
	}
	slotName := func(h uint32) string {
		if n, ok := skaterSlots[uint64(h)]; ok {
			return n
		}
		if n, ok := boardSlots[uint64(h)]; ok {
			return n
		}
		return fmt.Sprint(h)
	}

	type key struct {
		group int
		label string
	}
	type tally struct{ hot, cold int }
	counts := map[key]*tally{}
	var mu sync.Mutex
	classify := func(sg sig, ents []liveEnt, addr uint64) {
		t := sg.t
		best, bestDiff, bestSlot := "", 1<<30, ""
		var matched []string
		for _, b := range beliefs {
			m := b.items[sg.group]
			if m == nil {
				continue
			}
			compared, diff, first := 0, 0, ""
			for _, e := range ents {
				sl := t.slots[e.idx]
				if b.isNext && t.keep[e.idx] {
					continue
				}
				want, ok := m[sl]
				if !ok {
					continue
				}
				compared++
				if e.item != want {
					diff++
					if first == "" {
						first = fmt.Sprintf("%s: game %s, that has %s", slotName(sl), nameOf(e.item), nameOf(want))
					}
				}
			}
			if compared >= 20 && diff == 0 {
				matched = append(matched, b.label)
			} else if compared >= 20 && diff < bestDiff {
				best, bestDiff, bestSlot = b.label, diff, first
			}
		}
		label := ""
		if len(matched) > 0 {
			sort.Strings(matched)
			label = fmt.Sprint(matched)
		} else {
			label = fmt.Sprintf("matches nothing; closest is %q, %d entries differ, e.g. %s", best, bestDiff, bestSlot)
		}
		i := sort.Search(len(regs), func(i int) bool { return regs[i].base+regs[i].size > addr })
		hot := i < len(regs) && regs[i].base <= addr && isHotAlloc(regs[i].alloc)
		k := key{sg.group, label}
		mu.Lock()
		c := counts[k]
		if c == nil {
			c = &tally{}
			counts[k] = c
		}
		if hot {
			c.hot++
		} else {
			c.cold++
		}
		mu.Unlock()
	}

	const chunk = 4 << 20
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
			defer drainOnPanic("census scan", jobs)
			buf := make([]byte, chunk+64)
			for j := range jobs {
				n := j[1] + 64
				var rd uintptr
				if windows.ReadProcessMemory(h, uintptr(j[0]), &buf[0], uintptr(n), &rd) != nil && rd == 0 {
					continue
				}
				cur := buf[:rd]
				for i := 0; i+56 <= len(cur) && uint64(i) < j[1]; i += 8 {
					a := binary.LittleEndian.Uint32(cur[i+4:])
					for _, sg := range sigs {
						if a != sg.s0 || binary.LittleEndian.Uint32(cur[i+28:]) != sg.s1 || binary.LittleEndian.Uint32(cur[i+52:]) != sg.s2 {
							continue
						}
						addr := j[0] + uint64(i)
						if _, ents, _ := scoreTable(h, addr, sg.t); len(ents) > 0 {
							classify(sg, ents, ents[0].addr)
						}
					}
				}
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

	type row struct {
		k key
		t *tally
	}
	var rows []row
	for k, t := range counts {
		rows = append(rows, row{k, t})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.hot+rows[i].t.cold > rows[j].t.hot+rows[j].t.cold })
	tl.logf("LIVE: census %.1f s (all of the game's memory, whether or not it is in use): %d kinds of table", time.Since(t0).Seconds(), len(rows))
	for i, r := range rows {
		if i >= 24 {
			break
		}
		tl.logf("LIVE: census:   group %d: %d tables (%d in the known blocks, %d elsewhere): %s", r.k.group, r.t.hot+r.t.cold, r.t.hot, r.t.cold, r.k.label)
	}
}
