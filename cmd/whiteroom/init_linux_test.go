package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInitClosesStore checks that whiteroom init leaves no file open, whether it succeeds or fails once the store is
// made, so that the store is closed before the data directory is removed.
func TestInitClosesStore(t *testing.T) {
	open := func() int {
		t.Helper()
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skipf("no /proc/self/fd: %v", err)
		}
		return len(entries)
	}
	for _, tt := range []struct {
		what string
		args []string
		code int
	}{
		{"a success", nil, 0},
		{"a failure once the store is made", []string{"-email", "nobody"}, 1},
	} {
		data := filepath.Join(t.TempDir(), "data")
		before := open()
		code, stdout, stderr := runInitCmd(t, initPassword+"\n", adminFlags(append([]string{"-data", data}, tt.args...)...)...)
		if code != tt.code {
			t.Fatalf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s", tt.what, code, stdout, stderr)
		}
		if after := open(); after != before {
			t.Errorf("%s: %d files are open, want %d, as before", tt.what, after, before)
		}
	}
}
