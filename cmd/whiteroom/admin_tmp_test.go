//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestServeAdminTmpAfterWriter checks the order in which whiteroom serve admin opens what it needs: the writer connection of the
// daemon first, and the directory of the temporary files after it. A second server on the same daemon fails on the writer
// connection, and does not touch the temporary files of the first, which the second would remove if it came to them first.
func TestServeAdminTmpAfterWriter(t *testing.T) {
	s := initSample(t)
	dir := filepath.Join(t.TempDir(), "spool")
	first := startAdmin(t, t.Context(), s.args("-tmp", dir)...)
	file := filepath.Join(dir, ".tmp-0123456789abcdef") // named as the spool of a request that the first server is receiving
	if err := os.WriteFile(file, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runAdminCmd(t, onSystemPort(s.args("-tmp", dir))...)
	if want := "whiteroom serve admin: " + s.db + ": another process is connected as the writer\n"; code != 1 || stdout != "" ||
		stderr != want {
		t.Errorf("a second server on the daemon = %d, with stdout %q and stderr:\n%s\nwant 1, and %q", code, stdout, stderr, want)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "half" {
		t.Errorf("the temporary file of the first server is %q (%v) after the second server has failed, want it kept", data, err)
	}
	stopsWith(t, "the first server", first)
}
