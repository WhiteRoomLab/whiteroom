package dbserver

import (
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// TestCommitGate: a commit holds the gate of the server while its Update runs, which a Backup takes to keep commits out.
func TestCommitGate(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	f := newFixture(t, func(o *Options) {
		o.Hook = func(point string) error {
			if point == "commit:gate" {
				entered <- struct{}{}
				<-release
			}
			return nil
		}
	})
	w := f.connect(dbwire.RoleWriter)
	res := make(chan error, 1)
	go func() {
		_, _, err := w.tryCommit(put(2, "k", "v"))
		res <- err
	}()
	<-entered
	if f.srv.gate.TryLock() {
		t.Error("the gate is free while a commit runs")
	}
	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot() // reads do not need the gate
	close(release)
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	if !f.srv.gate.TryLock() {
		t.Fatal("the gate is held after the commit")
	}
	f.srv.gate.Unlock()
}
