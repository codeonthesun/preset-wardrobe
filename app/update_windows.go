//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// replaceAndRestart: the running .exe can't be overwritten but can be renamed, so it's moved aside
// (PresetWardrobe.exe.old), the new one put in its place and started, and this one exits. Any step failing puts the
// old one back. The new one waits for this one to close, then removes the .old file.
func replaceAndRestart(newExe string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("couldn't replace the app in its folder (%v). Download the new version from GitHub instead", err)
	}
	if err := os.Rename(newExe, exe); err != nil {
		if err2 := copyFile(newExe, exe); err2 != nil {
			os.Remove(exe)
			os.Rename(old, exe)
			return fmt.Errorf("couldn't put the new version in place: %v", err2)
		}
	}
	cmd := exec.Command(exe, "--updated-from", strconv.Itoa(os.Getpid()))
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		os.Remove(exe)
		os.Rename(old, exe)
		return fmt.Errorf("couldn't start the new version: %v", err)
	}
	go func() {
		time.Sleep(400 * time.Millisecond) // the reply gets out first
		os.Exit(0)
	}()
	return nil
}

// afterUpdate: started by an update: wait for the old app to close (it shares the window's data folder), then
// remove the old .exe.
func afterUpdate() {
	for i, a := range os.Args {
		if a == "--updated-from" && i+1 < len(os.Args) {
			if pid, err := strconv.Atoi(os.Args[i+1]); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					done := make(chan struct{})
					go func() { p.Wait(); close(done) }()
					select {
					case <-done:
					case <-time.After(15 * time.Second):
					}
				}
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	go func() {
		defer recoverLog("old exe cleanup")
		for i := 0; i < 10; i++ {
			if err := os.Remove(exe + ".old"); err == nil || os.IsNotExist(err) {
				return
			}
			time.Sleep(time.Second)
		}
	}()
}

func openURL(u string) { exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start() }
