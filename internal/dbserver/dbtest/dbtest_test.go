package dbtest

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// noLeaks fails the test at its end if a goroutine of the harness or the server is left.
func noLeaks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		var stacks string
		leaked := func() bool {
			buf := make([]byte, 1<<20)
			stacks = string(buf[:runtime.Stack(buf, true)])
			for _, g := range strings.Split(stacks, "\n\n") {
				if strings.Contains(g, "internal/dbserver.(") || strings.Contains(g, "dbtest.dialer.") ||
					strings.Contains(g, "dbtest.(*Daemon).") || strings.Contains(g, "internal/dbclient.(") {
					return true
				}
			}
			return false
		}
		for deadline := time.Now().Add(20 * time.Second); leaked(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Errorf("goroutines are left:\n%s", stacks)
				return
			}
		}
	})
}

func options() Options {
	return Options{Log: &logdb.Options{Placement: logdb.Hybrid, WriterWait: 20 * time.Millisecond}}
}

// newDir makes an initialized data directory on the OS.
func newDir(t *testing.T) string {
	t.Helper()
	noLeaks(t)
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(dir, options()); err != nil {
		t.Fatal(err)
	}
	return dir
}

func acquire(t *testing.T, dir string) *Daemon {
	t.Helper()
	h, err := Acquire(dir, options())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// commit seeds the store, and puts a row.
func commit(t *testing.T, c *dbclient.Client, key string) {
	t.Helper()
	err := c.Update(context.Background(), func(tx *dbclient.Tx) error {
		if _, ok, err := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema)); err != nil {
			return err
		} else if !ok {
			if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion)); err != nil {
				return err
			}
		}
		if err := tx.Put(2, []byte(key), []byte("v")); err != nil {
			return err
		}
		return tx.AppendEvent([]byte(`{"e":1}`))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAcquire(t *testing.T) {
	dir := newDir(t)
	a, b := acquire(t, dir), acquire(t, dir)
	if a.Server() != b.Server() {
		t.Fatal("two handles of a directory have two servers")
	}
	srv := a.Server()
	// A relative path, and another way to write the path, are the same directory.
	t.Chdir(filepath.Dir(dir))
	c := acquire(t, "data")
	d := acquire(t, filepath.Join(dir, "..", "data"))
	if c.Server() != srv || d.Server() != srv {
		t.Error("the same directory, written in another way, has another server")
	}

	// A handle is released once, and the daemon is closed by the last release.
	for range 3 {
		if err := a.Release(); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []*Daemon{b, c} {
		if err := h.Release(); err != nil {
			t.Fatal(err)
		}
	}
	cl, err := dbclient.Open(context.Background(), d.Dialer(), dbwire.RoleWriter, dbclient.Options{})
	if err != nil {
		t.Fatalf("the daemon was closed before its last handle was released: %v", err)
	}
	commit(t, cl, "k")
	if err := cl.Close(); err != nil { // which releases d
		t.Fatal(err)
	}
	if err := d.Release(); err != nil {
		t.Fatal(err)
	}
	// Closed: its engine is free to open, with the commit that was made.
	again := acquire(t, dir)
	if again.Server() == srv {
		t.Error("a daemon that was released is still the one of the directory")
	}
	cl, err = dbclient.Open(context.Background(), again.Dialer(), dbwire.RoleReader, dbclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	var found bool
	if err := cl.View(context.Background(), func(tx *dbclient.Tx) error {
		_, found, err = tx.Get(2, []byte("k"))
		return err
	}); err != nil || !found {
		t.Errorf("the commit is %v (%v)", found, err)
	}
}

func TestAcquireErrors(t *testing.T) {
	noLeaks(t)
	if _, err := Acquire(filepath.Join(t.TempDir(), "missing"), options()); err == nil {
		t.Error("Acquire of a directory that is not a data directory")
	}
	dir := newDir(t)
	if err := Init(dir, options()); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("Init of a data directory: %v", err)
	}
}

// TestPulse: the interval of the pulses is the daemon's, which a negative one makes Open refuse.
func TestPulse(t *testing.T) {
	dir := newDir(t)
	o := options()
	o.Pulse = -time.Second
	if _, err := Acquire(dir, o); err == nil || !strings.Contains(err.Error(), "the pulse interval is negative (-1s)") {
		t.Errorf("Acquire with a negative pulse interval: %v", err)
	}
	o.Pulse = time.Second
	h, err := Acquire(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestDialer(t *testing.T) {
	dir := newDir(t)
	h := acquire(t, dir)
	dialer := h.Dialer()
	if got := dialer.(interface{ String() string }).String(); got != dir {
		t.Errorf("the dialer is called %q, want %q", got, dir)
	}
	w, err := dbclient.Open(context.Background(), dialer, dbwire.RoleWriter, dbclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, w, "k")
	// The writer's connection is the only one of the daemon, and the dialer's Close released the handle.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Dialer().Dial(context.Background(), dbwire.RoleReader); err == nil || !strings.Contains(err.Error(), "has been released") {
		t.Errorf("Dial after the release: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestListen(t *testing.T) {
	dir := newDir(t)
	h := acquire(t, dir)
	sockets := t.TempDir()
	if err := h.Listen(sockets); err != nil {
		t.Skipf("no unix sockets here: %v", err)
	}
	for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		if info, err := os.Stat(filepath.Join(sockets, name)); err != nil || info.Mode()&os.ModeSocket == 0 {
			t.Errorf("%s: %v, %v", name, info, err)
		}
	}
	if err := h.Listen(sockets); err == nil {
		t.Error("Listen on sockets that are listened on")
	}
	w, err := dbclient.Open(context.Background(), dbclient.SocketDir(sockets), dbwire.RoleWriter, dbclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, w, "k")
	r, err := dbclient.Open(context.Background(), dbclient.SocketDir(sockets), dbwire.RoleReader, dbclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	if err := r.View(context.Background(), func(tx *dbclient.Tx) error {
		_, found, err = tx.Get(2, []byte("k"))
		return err
	}); err != nil || !found {
		t.Errorf("the reader found %v (%v)", found, err)
	}
	// The release ends the connections that are open, and removes the sockets.
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	_ = r.Close()
	for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		if _, err := os.Stat(filepath.Join(sockets, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is left: %v", name, err)
		}
	}
	if err := h.Listen(sockets); err == nil {
		t.Error("Listen after the release")
	}
}

func TestAbandon(t *testing.T) {
	dir := newDir(t)
	h := acquire(t, dir)
	w, err := dbclient.Open(context.Background(), h.Dialer(), dbwire.RoleWriter, dbclient.Options{DialWait: -1})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, w, "k")
	srv := h.Server()
	h.Abandon()
	// The connections are closed, and the dialer refuses.
	err = w.Update(context.Background(), func(tx *dbclient.Tx) error { return tx.Put(2, []byte("x"), nil) })
	if !errors.Is(err, dbclient.ErrUnavailable) {
		t.Errorf("Update on an abandoned daemon: %v", err)
	}
	if _, err := h.Dialer().Dial(context.Background(), dbwire.RoleReader); err == nil {
		t.Error("Dial on an abandoned daemon")
	}
	// The engine is open still, as that of a process that hangs: another daemon cannot open the directory.
	if _, err := Acquire(dir, options()); err == nil || !strings.Contains(err.Error(), "writer lock") {
		t.Errorf("Acquire of an abandoned directory: %v", err)
	}
	_ = w.Close()
	if err := h.Release(); err != nil {
		t.Errorf("Release after Abandon: %v", err)
	}
	if err := srv.Close(); err != nil { // the test closes what it abandoned
		t.Error(err)
	}
}

// TestHungDaemon checks that a daemon that hangs while it closes stops only the Acquire of its own directory, and that
// the Acquire waits for it.
func TestHungDaemon(t *testing.T) {
	dir, other := newDir(t), newDir(t)
	hung, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	o := options()
	o.Log.Hook = func(point string) error {
		if point == "checkpoint:renamed" {
			once.Do(func() { close(hung) })
			<-release
		}
		return nil
	}
	h, err := Acquire(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dbclient.Open(context.Background(), h.Dialer(), dbwire.RoleWriter, dbclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, c, "k")
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }() // the last release closes the engine, which hangs in a checkpoint
	select {
	case <-hung:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("the engine has not written a checkpoint when it was closed")
	}

	done := make(chan error, 1)
	go func() {
		h, err := Acquire(other, options())
		if err == nil {
			err = h.Release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Acquire and Release of another directory: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the daemon that hangs stops Acquire and Release of another directory")
	}

	type acquired struct {
		h   *Daemon
		err error
	}
	again := make(chan acquired, 1)
	go func() {
		h, err := Acquire(dir, options())
		again <- acquired{h, err}
	}()
	select {
	case a := <-again:
		close(release)
		t.Fatalf("Acquire of the directory of a daemon that is closing has returned: %v", a.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-closed; err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case a := <-again:
		if a.err != nil {
			t.Fatalf("Acquire after the daemon has closed: %v", a.err)
		}
		if err := a.h.Release(); err != nil {
			t.Error(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Acquire of the directory has not returned after the daemon has closed")
	}
}
