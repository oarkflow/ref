//go:build windows

package main

import (
	"fmt"
	"os"
)

func restartSelf() {
	fmt.Fprintln(os.Stderr, "ref: studio: restart the application to apply the change")
}
