//go:build !linux && !darwin

package dbserver

import "testing"

func mkfifo(t testing.TB, _ string) {
	t.Helper()
	t.Skip("no named pipes on this system")
}
