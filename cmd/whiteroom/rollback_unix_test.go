//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRollbackLocked checks that a rollback does not wait for a build, or another rollback, that holds the lock of the
// output directory: it fails at once, with current as it was, and works when the lock is free. internal/build does not
// export its lock, so the test takes the flock that it takes.
func TestRollbackLocked(t *testing.T) {
	out, older, newer := twoReleases(t)
	f, err := os.OpenFile(filepath.Join(out, ".lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	want := "whiteroom rollback: another build is running in " + out + "\n"
	for _, args := range [][]string{{out, older}, {out}} {
		code, stdout, stderr := runCmd(t, append([]string{"rollback"}, args...)...)
		if code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom rollback %v while the output directory is locked = %d, with stdout %q and stderr %q, want 1, none, and %q",
				args, code, stdout, stderr, want)
		}
		checkReleases(t, "the lock", out, newer, older, newer)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCmd(t, "rollback", out, older); code != 0 {
		t.Fatalf("whiteroom rollback after the lock is free = %d, with stderr:\n%s", code, stderr)
	}
	checkReleases(t, "after the lock", out, older, older, newer)
}
