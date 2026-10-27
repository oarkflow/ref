//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// startDir is where the process was started. serve.Run changes directory into
// the application, so a restart must go back first or a relative application
// path (".") would resolve somewhere else.
var startDir, _ = os.Getwd()

// restartSelf replaces the process with a fresh copy of itself so the
// application reloads its files.
func restartSelf() {
	if startDir != "" {
		if err := os.Chdir(startDir); err != nil {
			fmt.Fprintln(os.Stderr, "ref: studio: restart the application to apply the change:", err)
			return
		}
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ref: studio: restart the application to apply the change:", err)
		return
	}
	time.Sleep(300 * time.Millisecond) // let the activation response reach the browser
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "ref: studio: restart the application to apply the change:", err)
	}
}
