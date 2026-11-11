//go:build windows

// Memory probe (read-only): where does the running game keep the loaded presets?
// It only READS Skate.exe's memory. It never writes, injects or hooks anything.
// Run it with the game open, a preset selected, and (ideally) that preset wearing the black
// helmet "Own_HeadwearHelmet_Gen_HelmetSkateboard_00001". Writes memprobe.txt next to the exe.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type needle struct {
	name string
	data []byte
}

type hit struct {
	n    string
	addr uint64
}

type region struct {
	base, size uint64
	typ, prot  uint32
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
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
			out = append(out, region{uint64(mbi.BaseAddress), uint64(mbi.RegionSize), mbi.Type, mbi.Protect})
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr {
			break
		}
		addr = next
	}
	return out
}

// scan calls fn(regionIndex, regionBase, chunk) for every readable chunk; chunks overlap by overlap bytes.
func scan(h windows.Handle, regs []region, overlap int, fn func(r region, base uint64, buf []byte, own int)) {
	const chunk = 4 << 20
	for _, r := range regs {
		for off := uint64(0); off < r.size; off += chunk {
			n := uint64(chunk + overlap)
			if off+n > r.size {
				n = r.size - off
			}
			buf := make([]byte, n)
			var got uintptr
			if windows.ReadProcessMemory(h, uintptr(r.base+off), &buf[0], uintptr(n), &got) != nil && got == 0 {
				continue
			}
			own := chunk
			if off+chunk >= r.size {
				own = len(buf) // last chunk of the region: everything is its own
			}
			fn(r, r.base+off, buf[:got], own)
		}
	}
}

func printable(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= 32 && c < 127:
			sb.WriteByte(c)
		case c == 0 && i+1 < len(b) && b[i+1] >= 32 && b[i+1] < 127:
			// utf-16 gap: skip
		default:
			sb.WriteByte('.')
		}
	}
	return sb.String()
}

func main() {
	out, _ := os.Create(filepath.Join(filepath.Dir(os.Args[0]), "memprobe.txt"))
	w := bufio.NewWriter(out)
	say := func(f string, a ...any) { s := fmt.Sprintf(f, a...); fmt.Print(s); w.WriteString(s); w.Flush() }
	defer func() { fmt.Print("\nPress Enter to close."); fmt.Scanln() }()

	pid := findPID()
	if pid == 0 {
		say("Skate.exe isn't running. Start the game first.\n")
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		say("Couldn't open the game to read it: %v\n(Try running this as administrator.)\n", err)
		return
	}
	defer windows.CloseHandle(h)

	words := []string{
		"Own_HeadwearHelmet_Gen_HelmetSkateboard_00001",
		"cust_hats",
		"cosmetic_loadouts",
		"Loadout02",
		`"slot":2889696005`,
		`"slot": 2889696005`,
		"recipes_json",
	}
	words = append(words, os.Args[1:]...)
	var needles []needle
	for _, s := range words {
		needles = append(needles, needle{s + " [ascii]", []byte(s)}, needle{s + " [utf16]", utf16le(s)})
	}

	regs := regions(h)
	var total uint64
	for _, r := range regs {
		total += r.size
	}
	say("Skate.exe pid %d: %d readable regions, %d MB. Read-only scan, started %s\n\n", pid, len(regs), total>>20, time.Now().Format("15:04:05"))

	counts := map[string]int{}
	var hits []hit
	ctx := map[uint64][]byte{}
	scan(h, regs, 256, func(r region, base uint64, buf []byte, own int) {
		for _, nd := range needles {
			from := 0
			for {
				i := bytes.Index(buf[from:], nd.data)
				if i < 0 {
					break
				}
				i += from
				from = i + 1
				addr := base + uint64(i)
				if i >= own { // inside the overlap: the next chunk reports it
					continue
				}
				counts[nd.name]++
				if counts[nd.name] <= 12 {
					hits = append(hits, hit{nd.name, addr})
					lo, hi := i-80, i+len(nd.data)+160
					if lo < 0 {
						lo = 0
					}
					if hi > len(buf) {
						hi = len(buf)
					}
					ctx[addr] = append([]byte(fmt.Sprintf("type=0x%x prot=0x%x region=0x%x+%d | ", r.typ, r.prot, r.base, r.size)), printable(buf[lo:hi])...)
				}
			}
		}
	})
	say("== Hits per string ==\n")
	for _, nd := range needles {
		say("%6d  %s\n", counts[nd.name], nd.name)
	}
	say("\n== First hits with surroundings ==\n")
	for _, x := range hits {
		say("%s @0x%x\n   %s\n", x.n, x.addr, ctx[x.addr])
	}

	// Who points at the first few helmet-name hits? (an 8-byte pointer to the string or just before it)
	var targets []uint64
	for _, x := range hits {
		if strings.HasPrefix(x.n, "Own_HeadwearHelmet") && len(targets) < 6 {
			targets = append(targets, x.addr)
		}
	}
	if len(targets) > 0 {
		say("\n== Pointers to the helmet-name hits ==\n")
		found := 0
		scan(h, regs, 8, func(r region, base uint64, buf []byte, own int) {
			for i := 0; i+8 <= len(buf) && i < own && found < 60; i += 8 {
				v := binary.LittleEndian.Uint64(buf[i:])
				for _, t := range targets {
					if v == t {
						found++
						lo, hi := i-32, i+64
						if lo < 0 {
							lo = 0
						}
						if hi > len(buf) {
							hi = len(buf)
						}
						say("ptr to 0x%x stored at 0x%x (type=0x%x)\n   %x\n", t, base+uint64(i), r.typ, buf[lo:hi])
					}
				}
			}
		})
		say("%d pointers found.\n", found)
	}
	say("\nDone %s. Send me memprobe.txt (next to this exe).\n", time.Now().Format("15:04:05"))
}
