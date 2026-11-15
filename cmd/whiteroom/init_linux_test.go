package main

import (
	"os"
	"testing"
	"time"
)

// TestInitClosesStore checks that whiteroom init leaves no file open, whether it succeeds or fails once the store is made,
// so that its connections to the daemon are closed, the daemon's side of them too.
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
		// The daemon runs before the files are counted: its sockets are open all the time.
		db := newDB(t)
		before := open()
		code, stdout, stderr := initDB(t, db, initPassword+"\n", tt.args...)
		if code != tt.code {
			t.Fatalf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s", tt.what, code, stdout, stderr)
		}
		// The daemon sees the end of a connection a moment after the client has closed it.
		deadline := time.Now().Add(5 * time.Second)
		after := open()
		for after != before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			after = open()
		}
		if after != before {
			t.Errorf("%s: %d files are open, want %d, as before", tt.what, after, before)
		}
	}
}
