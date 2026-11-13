//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// For trying the program out away from Windows: PB_GAME_RUNNING=1 pretends skate. is open.
func gameRunning() bool { return os.Getenv("PB_GAME_RUNNING") == "1" }

func revealInExplorer(target string) error { fmt.Println("reveal", target); return nil }

func fatal(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(1) }

func showWindow(url string, app *App, data string) {
	fmt.Println("Preset Wardrobe is at", url)
	select {}
}

// Away from Windows there's no game to press keys in; PB_KEYS_LOG=1 pretends, for trying it out.
func gameInFront() bool { return os.Getenv("PB_KEYS_LOG") == "1" }

func pressKey(key string) error { fmt.Println("press", key); return nil }

func muteGame(mute bool) (touched, wrong int, err error) { fmt.Println("mute", mute); return 0, 0, nil }

func lowPriorityThread() {}

func focusGame() bool { return false }

func diskFree(path string) uint64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0
	}
	return st.Bavail * uint64(st.Bsize)
}
