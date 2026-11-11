//go:build windows

package main

import (
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// muteGame mutes or unmutes the game's own audio (its entry in the Windows volume mixer), so the menu
// sounds during a refresh aren't heard. Nothing else on the computer is touched.
func muteGame(mute bool) (touched, wrong int, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pid := skatePID()
	if pid == 0 {
		return 0, 0, nil
	}
	ole := windows.NewLazySystemDLL("ole32.dll")
	ole.NewProc("CoInitializeEx").Call(0, 2)
	defer ole.NewProc("CoUninitialize").Call()

	guid := func(s string) *windows.GUID { g, _ := windows.GUIDFromString(s); return &g }
	call := func(obj uintptr, idx int, args ...uintptr) uintptr {
		vt := deref(obj)
		fn := deref(vt + uintptr(idx)*unsafe.Sizeof(uintptr(0)))
		r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
		return r
	}
	release := func(obj uintptr) { call(obj, 2) }

	var enum uintptr
	r, _, _ := ole.NewProc("CoCreateInstance").Call(
		uintptr(unsafe.Pointer(guid("{BCDE0395-E52F-467C-8E3D-C4579291692E}"))), 0, 23,
		uintptr(unsafe.Pointer(guid("{A95664D2-9614-4F35-A746-DE8DB63617E6}"))), uintptr(unsafe.Pointer(&enum)))
	if r != 0 || enum == 0 {
		return 0, 0, syscall.Errno(r)
	}
	defer release(enum)
	var dev uintptr
	if r := call(enum, 4, 0, 1, uintptr(unsafe.Pointer(&dev))); r != 0 || dev == 0 { // eRender, eMultimedia
		return 0, 0, syscall.Errno(r)
	}
	defer release(dev)
	var mgr uintptr
	if r := call(dev, 3, uintptr(unsafe.Pointer(guid("{77AA99A0-1BD6-484F-8BC7-2C654C9A9B6F}"))), 23, 0, uintptr(unsafe.Pointer(&mgr))); r != 0 || mgr == 0 {
		return 0, 0, syscall.Errno(r)
	}
	defer release(mgr)
	var list uintptr
	if r := call(mgr, 5, uintptr(unsafe.Pointer(&list))); r != 0 || list == 0 {
		return 0, 0, syscall.Errno(r)
	}
	defer release(list)
	var n int32
	call(list, 3, uintptr(unsafe.Pointer(&n)))
	flag := uintptr(0)
	if mute {
		flag = 1
	}
	for i := int32(0); i < n; i++ {
		var ctl uintptr
		if call(list, 4, uintptr(i), uintptr(unsafe.Pointer(&ctl))) != 0 || ctl == 0 {
			continue
		}
		var ctl2, vol uintptr
		if call(ctl, 0, uintptr(unsafe.Pointer(guid("{BFB7FF88-7239-4FC9-8FA2-07C950BE9C6D}"))), uintptr(unsafe.Pointer(&ctl2))) == 0 && ctl2 != 0 {
			var p uint32
			call(ctl2, 14, uintptr(unsafe.Pointer(&p)))
			if p == pid && call(ctl, 0, uintptr(unsafe.Pointer(guid("{87CE5498-68D6-44E5-9215-6DA47EF883D8}"))), uintptr(unsafe.Pointer(&vol))) == 0 && vol != 0 {
				call(vol, 5, flag, 0)
				var now int32 // read it back: is it what was asked for?
				call(vol, 6, uintptr(unsafe.Pointer(&now)))
				touched++
				if (now != 0) != mute {
					wrong++
				}
				release(vol)
			}
			release(ctl2)
		}
		release(ctl)
	}
	return touched, wrong, nil
}

// deref reads the pointer-sized value at a COM object's address.
func deref(addr uintptr) uintptr { return **(**uintptr)(unsafe.Pointer(&addr)) }

func skatePID() uint32 {
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
