//go:build windows

// Memory edit experiment. memprobe showed the game keeps a copy of every preset as JSON text in
// its own memory. Question: if one of those copies changes, does the game show it when you switch
// presets in the menu? This swaps ONE item name for another of the SAME LENGTH in those copies
// (black helmet 00001 <-> 00002), so nothing moves or resizes. No arguments = look only, change
// nothing. "apply" = swap 00001 -> 00002. "revert" = swap back.
package main

import (
	"bytes"
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

func main() {
	if f, err := os.Create(filepath.Join(filepath.Dir(os.Args[0]), "memedit_log.txt")); err == nil {
		defer f.Close()
		logw = f
	}
	defer func() {
		if r := recover(); r != nil {
			say("CRASH: %v\n", r)
		}
	}()
	say("memedit started\n")
	mode := "look"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	const a, b = "Own_HeadwearHelmet_Gen_HelmetSkateboard_00001", "Own_HeadwearHelmet_Gen_HelmetSkateboard_00002"
	from, to := a, b
	if mode == "revert" {
		from, to = b, a
	}
	// the preset JSON, not the item catalogue: the name followed by its slot entry
	tail := `","parameter_bits":[],"slot":2889696005`
	needle, repl := []byte(`"asset":"`+from+tail), []byte(`"asset":"`+to+tail)

	pid := findPID()
	if pid == 0 {
		say("Skate.exe isn't running.\n")
		return
	}
	access := uint32(windows.PROCESS_QUERY_INFORMATION | windows.PROCESS_VM_READ)
	if mode != "look" {
		access |= windows.PROCESS_VM_WRITE | windows.PROCESS_VM_OPERATION
	}
	h, err := windows.OpenProcess(access, false, pid)
	if err != nil {
		say("Couldn't open the game: %v (try running as administrator)\n", err)
		return
	}
	defer windows.CloseHandle(h)

	found, changed := 0, 0
	const chunk = 4 << 20
	for _, r := range regions(h) {
		if r.prot&0x04 == 0 && r.prot&0x40 == 0 && r.prot&0x08 == 0 { // writable only
			continue
		}
		for off := uint64(0); off < r.size; off += chunk {
			n := uint64(chunk + len(needle))
			if off+n > r.size {
				n = r.size - off
			}
			buf := make([]byte, n)
			var got uintptr
			if windows.ReadProcessMemory(h, uintptr(r.base+off), &buf[0], uintptr(n), &got) != nil && got == 0 {
				continue
			}
			own := len(buf)
			if off+chunk < r.size {
				own = chunk
			}
			buf = buf[:got]
			for from := 0; ; {
				i := bytes.Index(buf[from:], needle)
				if i < 0 {
					break
				}
				i += from
				from = i + 1
				if i >= own {
					continue
				}
				addr := r.base + off + uint64(i)
				found++
				say("copy %d at 0x%x\n", found, addr)
				if mode == "look" {
					continue
				}
				var w uintptr
				if err := windows.WriteProcessMemory(h, uintptr(addr), &repl[0], uintptr(len(repl)), &w); err != nil || int(w) != len(repl) {
					say("   couldn't write: %v (wrote %d)\n", err, w)
					continue
				}
				changed++
			}
		}
	}
	say("\n%d copies of the helmet entry found; %d changed (%s).\n", found, changed, mode)
	if mode == "look" {
		say("Nothing was changed.\n")
	}
}
