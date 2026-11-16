//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
)

// stopAtProblem is a standard output that runs stop when the first line that starts with prefix is written, and keeps what is
// written.
type stopAtProblem struct {
	bytes.Buffer
	prefix string
	stop   func()
}

func (w *stopAtProblem) Write(p []byte) (int, error) {
	if w.stop != nil && strings.HasPrefix(string(p), w.prefix) {
		stop := w.stop
		w.stop = nil
		stop()
	}
	return w.Buffer.Write(p)
}

// TestDoctorDaemonGoesAway stops the daemon in the middle of whiteroom doctor, after the checks of the originals, which are
// the last of the checks of the store, and before that of the audit logs: it is a check that could not be made, and not a
// problem of the store. The command says so on the standard error, with the exit code 1 and no line of the problems of the
// standard output about the audit logs, nor the line that counts the problems.
func TestDoctorDaemonGoesAway(t *testing.T) {
	s := initSample(t)
	// The original that the check reads last, which is the one with the greatest hash, has other bytes: its problem is the last
	// thing that the checks of the store write, and the last thing that they read.
	files := originalFiles(t, s.data)
	hash := hashOf(t, files[len(files)-1])
	flipBit(t, files[len(files)-1], 0)

	out := &stopAtProblem{prefix: "original: " + hash, stop: func() { stopDB(t, s.database()) }}
	var stderr bytes.Buffer
	code := run(t.Context(), []string{"doctor", "-db", s.db, s.site}, testEnv(strings.NewReader(""), out, &stderr))
	if out.stop != nil {
		t.Fatalf("the problem of the original %s did not come, so that the daemon was not stopped; stdout:\n%s", hash, out)
	}
	startDB(t, s.database())

	if lines := splitLines(out.String()); len(lines) != 1 || !strings.HasPrefix(lines[0], "original: "+hash) {
		t.Errorf("whiteroom doctor wrote on stdout:\n%s\nwant the one problem of the original that has other bytes", out)
	}
	want := "whiteroom doctor: " + s.db + ": " + dbclient.ErrUnavailable.Error() + ": dial unix "
	if text := stderr.String(); code != 1 || !strings.HasPrefix(text, want) || strings.Count(text, "\n") != 1 {
		t.Errorf("whiteroom doctor = %d, with stderr %q, want 1, and the one line that starts with %q", code, text, want)
	}
}
