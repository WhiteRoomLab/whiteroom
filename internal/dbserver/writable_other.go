//go:build !linux

package dbserver

func writable(string) error { return nil }
