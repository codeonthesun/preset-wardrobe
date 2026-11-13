//go:build windows

package main

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/windows"
)

// Item names in the game's outfit tables. A load points the entries it writes at the game's own empty string: ReSkate's
// save turns an entry back into a name by its name pointer, and when that name is empty it looks the item hash up in
// its own catalog (the check that logs invalid_native_recipe otherwise). The empty string is the game's, never freed;
// the components' own tables use it for every item. (Pointing at copies of names crashed the game: test276, test278.)

const emptyStringOffset = 0x72fe704 // build 20260929: Skate.exe's empty string

// emptyName: the address of the game's empty string, or 0 if it isn't one in this build.
func emptyName(h windows.Handle, pid uint32) uint64 {
	base := moduleBase(pid)
	if base == 0 {
		return 0
	}
	var z [1]byte
	var got uintptr
	if windows.ReadProcessMemory(h, uintptr(base+emptyStringOffset), &z[0], 1, &got) != nil || z[0] != 0 {
		return 0
	}
	return base + emptyStringOffset
}

// nameAt: the name at p in the game ("" if there's none).
func nameAt(h windows.Handle, p uint64) string {
	var b [168]byte
	var got uintptr
	if p == 0 || windows.ReadProcessMemory(h, uintptr(p), &b[0], uintptr(len(b)), &got) != nil && got == 0 {
		return ""
	}
	if i := bytes.IndexByte(b[:got], 0); i >= 0 {
		return string(b[:i])
	}
	return ""
}

func putPointer(h windows.Handle, at, p uint64) bool {
	var v [8]byte
	binary.LittleEndian.PutUint64(v[:], p)
	var w uintptr
	return windows.WriteProcessMemory(h, uintptr(at), &v[0], 8, &w) == nil && w == 8
}
