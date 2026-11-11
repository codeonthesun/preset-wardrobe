//go:build !windows

package main

import "errors"

func replaceAndRestart(newExe string) error {
	return errors.New("updating itself works on Windows only")
}

func afterUpdate() {}

func openURL(u string) {}
