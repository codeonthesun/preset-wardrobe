//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

func gameRunning() bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "Skate.exe") {
			return true
		}
	}
	return false
}

func revealInExplorer(target string) error {
	cmd := exec.Command("explorer.exe")
	if st, err := os.Stat(target); err == nil && !st.IsDir() {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + target + `"`}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe "` + target + `"`}
	}
	return cmd.Start() // explorer's exit code means nothing
}

func messageBox(text string) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Preset Wardrobe")
	user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x10)
}

func fatal(msg string) {
	messageBox(msg)
	os.Exit(1)
}

func showWindow(url string, app *App, data string) {
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(data, "webview"),
		WindowOptions: webview2.WindowOptions{
			Title: "Preset Wardrobe for ReSkate", Width: 1280, Height: 860, Center: true, IconId: 1,
		},
	})
	if w == nil {
		// No WebView2 runtime: use the default browser instead, and quit when it stops asking.
		log.Println("webview2 unavailable, opening the browser")
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		waitForIdle(app)
		return
	}
	defer w.Destroy()
	appWindow = uintptr(w.Window())
	w.SetSize(1280, 860, webview2.HintNone)
	w.Navigate(url)
	w.Run()
}

func waitForIdle(app *App) {
	for {
		time.Sleep(5 * time.Second)
		app.mu.Lock()
		idle := time.Since(app.lastSeen)
		app.mu.Unlock()
		if idle > 45*time.Second {
			return
		}
	}
}

// gameInFront: the window in front belongs to Skate.exe.
func gameInFront() bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	hwnd, _, _ := user32.NewProc("GetForegroundWindow").Call()
	if hwnd == 0 {
		return false
	}
	var pid uint32
	user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return false
	}
	return strings.EqualFold(filepath.Base(windows.UTF16ToString(buf[:n])), "Skate.exe")
}

// keyInput is Windows' INPUT for a key (64-bit layout: 40 bytes).
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

// pressKey presses and lets go of Z, C, B, Escape or Space by its scan code, as a keyboard would: games often read
// the keys' positions rather than the letters.
func pressKey(key string) error {
	scans := map[string]uint16{"z": 0x2C, "c": 0x2E, "b": 0x30, "esc": 0x01, "space": 0x39}
	scan, ok := scans[key]
	if !ok {
		return os.ErrInvalid
	}
	const inputKeyboard, scancode, keyUp = 1, 0x0008, 0x0002
	send := windows.NewLazySystemDLL("user32.dll").NewProc("SendInput")
	for _, flags := range []uint32{scancode, scancode | keyUp} {
		in := keyInput{typ: inputKeyboard, scan: scan, flags: flags}
		if n, _, err := send.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in)); n != 1 {
			return err
		}
		if flags&keyUp == 0 {
			time.Sleep(60 * time.Millisecond) // held for a moment, like a real press
		}
	}
	return nil
}

var procSetThreadPriority = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority")

// lowPriorityThread keeps the calling goroutine on its own thread and puts that thread at the lowest priority,
// so background work only uses time nothing else wants.
func lowPriorityThread() {
	runtime.LockOSThread()
	lowest := int32(-2) // THREAD_PRIORITY_LOWEST
	procSetThreadPriority.Call(uintptr(windows.CurrentThread()), uintptr(lowest))
}

var appWindow uintptr // the app's own window, once it is open

// focusGame brings skate.'s window to the front (restoring it if minimized) and minimizes the app's window.
// It is called right after a click in the app, so Windows lets the app hand the focus over.
func focusGame() bool {
	pid := findSkatePID()
	if pid == 0 {
		return false
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	isVisible, getPid, getOwner := user32.NewProc("IsWindowVisible"), user32.NewProc("GetWindowThreadProcessId"), user32.NewProc("GetWindow")
	var found uintptr
	cb := windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		var p uint32
		getPid.Call(hwnd, uintptr(unsafe.Pointer(&p)))
		if p != pid {
			return 1
		}
		if v, _, _ := isVisible.Call(hwnd); v == 0 {
			return 1
		}
		if owner, _, _ := getOwner.Call(hwnd, 4 /* GW_OWNER */); owner != 0 {
			return 1 // a pop-up, not the main window
		}
		found = hwnd
		return 0
	})
	user32.NewProc("EnumWindows").Call(cb, 0)
	if found == 0 {
		return false
	}
	if iconic, _, _ := user32.NewProc("IsIconic").Call(found); iconic != 0 {
		user32.NewProc("ShowWindow").Call(found, 9 /* SW_RESTORE */)
	}
	ok, _, _ := user32.NewProc("SetForegroundWindow").Call(found)
	if ok != 0 && appWindow != 0 {
		user32.NewProc("ShowWindow").Call(appWindow, 6 /* SW_MINIMIZE */)
	}
	return ok != 0
}

// diskFree: bytes free on the drive that holds path (0 if it can't be told).
func diskFree(path string) uint64 {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free, total, totalFree uint64
	if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil {
		return 0
	}
	return free
}
