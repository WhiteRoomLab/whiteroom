//go:build !linux

package dbserver

import "testing"

// TestRunOtherSystem checks that Run returns at once, with the sentence that says why, on a system that does not tell the
// user of a peer: it opens nothing.
func TestRunOtherSystem(t *testing.T) {
	err := Run(t.Context(), Config{Data: t.TempDir(), Sockets: t.TempDir()})
	if err == nil || err.Error() != errNotLinux.Error() {
		t.Errorf("Run = %v, want %q", err, errNotLinux)
	}
}
