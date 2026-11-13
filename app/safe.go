package main

import (
	"runtime/debug"
	"time"
)

// A panic in a background goroutine would close the whole app, perhaps in the middle of a write. These catch it,
// write it to timeline.log, and let the rest carry on.

func recoverLog(name string) {
	if r := recover(); r != nil {
		tl.logf("APP: PANIC in %s: %v\n%s", name, r, debug.Stack())
	}
}

// drainOnPanic is for a worker that takes jobs from a channel: if it panics, it keeps emptying the channel so
// whoever is sending jobs never waits on a worker that's gone.
func drainOnPanic[T any](name string, jobs <-chan T) {
	if r := recover(); r != nil {
		tl.logf("APP: PANIC in %s: %v\n%s", name, r, debug.Stack())
		for range jobs {
		}
	}
}

// supervise runs a loop that should go on for as long as the app does, starting it again if it panics.
func supervise(name string, f func()) {
	go func() {
		for {
			if !func() (ok bool) {
				defer func() {
					if r := recover(); r != nil {
						tl.logf("APP: PANIC in %s (starting it again): %v\n%s", name, r, debug.Stack())
						ok = false
					}
				}()
				f()
				return true
			}() {
				time.Sleep(2 * time.Second)
				continue
			}
			return
		}
	}()
}
