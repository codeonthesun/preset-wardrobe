//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
