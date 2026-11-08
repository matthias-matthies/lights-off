package main

import (
	"os/exec"
	"runtime"
)

// openURL opens url in the person's browser. The opener is started and not
// waited for, so that a browser that stays open holds nothing up. Its errors
// are the caller's to ignore: the Dashboard's URL is also printed.
func openURL(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
