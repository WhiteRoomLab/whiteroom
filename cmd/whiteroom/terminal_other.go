//go:build !linux

package main

import (
	"context"
	"os"
)

// canReadHidden is false where readHidden cannot read from a terminal without echo.
const canReadHidden = false

// isTerminal reports whether f is a character device, which a terminal is.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func readHidden(context.Context, *os.File, int) ([]byte, error) {
	return nil, errNoHidden
}
