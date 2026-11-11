//go:build windows

// memhash: finds the game's parsed outfit data.
// Memory edit experiment (old header). memprobe showed the game keeps a copy of every preset as JSON text in
// its own memory. Question: if one of those copies changes, does the game show it when you switch
// presets in the menu? This swaps ONE item name for another of the SAME LENGTH in those copies
// (black helmet 00001 <-> 00002), so nothing moves or resizes. No arguments = look only, change
// nothing. "apply" = swap 00001 -> 00002. "revert" = swap back.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var logw io.Writer

func say(f string, a ...any) {
	s := fmt.Sprintf(f, a...)
	fmt.Print(s)
	if logw != nil {
		io.WriteString(logw, s)
	}
}

type region struct {
	base, size uint64
	prot       uint32
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

func regions(h windows.Handle) []region {
	var out []region
	var addr uintptr
	for {
		var mbi windows.MemoryBasicInformation
		if err := windows.VirtualQueryEx(h, addr, &mbi, unsafe.Sizeof(mbi)); err != nil {
			break
		}
		const commit, noaccess, guard = 0x1000, 0x01, 0x100
		if mbi.State == commit && mbi.Protect&noaccess == 0 && mbi.Protect&guard == 0 {
			out = append(out, region{uint64(mbi.BaseAddress), uint64(mbi.RegionSize), mbi.Protect})
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr {
			break
		}
		addr = next
	}
	return out
}

func djb2x(s string) uint32 {
	h := uint32(5381)
	for i := 0; i < len(s); i++ {
		h = (h * 33) ^ uint32(s[i])
	}
	return h
}
func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return h
}
func fnv64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 1099511628211
	}
	return h
}

func main() {
	if f, err := os.Create(filepath.Join(filepath.Dir(os.Args[0]), "memhash_log.txt")); err == nil {
		defer f.Close()
		logw = f
	}
	defer func() {
		if r := recover(); r != nil {
			say("CRASH: %v\n", r)
		}
	}()
	pid := findPID()
	if pid == 0 {
		say("Skate.exe isn't running.\n")
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		say("Couldn't open the game: %v\n", err)
		return
	}
	defer windows.CloseHandle(h)

	const slot = 2889696005
	name := "Own_HeadwearHelmet_Gen_HelmetSkateboard_00001"
	type cand struct {
		label string
		v     uint64
		wide  bool
	}
	var cands []cand
	for _, n := range []string{name, strings.ToLower(name)} {
		tag := "name"
		if n != name {
			tag = "lower"
		}
		cands = append(cands, cand{"djb2xor(" + tag + ")", uint64(djb2x(n)), false}, cand{"fnv32(" + tag + ")", uint64(fnv32(n)), false}, cand{"fnv64(" + tag + ")", fnv64(n), true})
	}
	for _, c := range cands {
		say("candidate %s = %d\n", c.label, c.v)
	}
	slotHits, pairHits := 0, 0
	const chunk = 4 << 20
	for _, r := range regions(h) {
		for off := uint64(0); off < r.size; off += chunk {
			n := uint64(chunk + 256)
			if off+n > r.size {
				n = r.size - off
			}
			buf := make([]byte, n)
			var got uintptr
			if windows.ReadProcessMemory(h, uintptr(r.base+off), &buf[0], uintptr(n), &got) != nil && got == 0 {
				continue
			}
			buf = buf[:got]
			own := len(buf)
			if off+chunk < r.size {
				own = chunk
			}
			for i := 0; i+4 <= own; i += 4 {
				if binary.LittleEndian.Uint32(buf[i:]) != slot {
					continue
				}
				slotHits++
				lo, hi := i-96, i+96
				if lo < 0 {
					lo = 0
				}
				if hi > len(buf) {
					hi = len(buf)
				}
				for j := lo; j+4 <= hi; j += 4 {
					for _, c := range cands {
						ok := false
						if c.wide {
							ok = j+8 <= hi && binary.LittleEndian.Uint64(buf[j:]) == c.v
						} else {
							ok = uint64(binary.LittleEndian.Uint32(buf[j:])) == c.v
						}
						if ok && pairHits < 40 {
							pairHits++
							say("\nPAIR: slot id at 0x%x, %s at %+d bytes (region type 0x%x)\n   %x\n", r.base+off+uint64(i), c.label, j-i, uint32(0), buf[lo:hi])
						}
					}
				}
			}
		}
	}
	say("\nslot id 2889696005 appears %d times as a number; %d of them have a helmet-name hash within 96 bytes.\n", slotHits, pairHits)
}
