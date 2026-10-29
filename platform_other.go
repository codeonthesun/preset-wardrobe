//go:build !windows

package main

import (
	"fmt"
	"os"
)

// For trying the program out away from Windows: PB_GAME_RUNNING=1 pretends skate. is open.
func gameRunning() bool { return os.Getenv("PB_GAME_RUNNING") == "1" }

func revealInExplorer(target string) error { fmt.Println("reveal", target); return nil }

func fatal(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(1) }

func showWindow(url string, app *App, data string) {
	fmt.Println("Preset Wardrobe is at", url)
	select {}
}
