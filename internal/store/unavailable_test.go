package store

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
)

// TestUnavailable shows what the operations of a store return while its daemon is gone, which it reaches on the sockets of
// a directory: an error that is ErrUnavailable and is not fs.ErrNotExist, which a caller would take for a document that is
// missing; and that they work again, with the same store, once a daemon serves the sockets.
func TestUnavailable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := dbtest.Init(dir, dbtest.Options{}); err != nil {
		t.Fatal(err)
	}
	sockets := socketDir(t)
	serveSockets := func() *dbtest.Daemon {
		t.Helper()
		h, err := dbtest.Acquire(dir, dbtest.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Listen(sockets); err != nil {
			_ = h.Release()
			t.Fatal(err)
		}
		return h
	}
	h := serveSockets()
	s, err := createOn(dbclient.SocketDir(sockets), time.Now, dbclient.Options{DialWait: 50 * time.Millisecond})
	if err != nil {
		_ = h.Release()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = h.Release()
	})

	original := []byte("the original of the unavailable store")
	hash := sum(original)
	ops := []struct {
		name string
		do   func() error
	}{
		{"View", func() error {
			return s.View(t.Context(), func(tx *Tx) error {
				_, err := tx.Document(SettingsKind, "")
				return err
			})
		}},
		{"Update", func() error {
			return s.Update(t.Context(), withRecord(func(tx *Tx) error {
				_, err := tx.Put(SettingsKind, "", docJSON(SettingsKind, ""), userID)
				return err
			}))
		}},
		{"PutOriginal", func() error {
			_, err := s.PutOriginal(t.Context(), bytes.NewReader(original))
			return err
		}},
		{"OpenOriginal", func() error {
			f, err := s.OpenOriginal(hash)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			got, err := io.ReadAll(f)
			if err == nil && !bytes.Equal(got, original) {
				t.Errorf("OpenOriginal() has %d bytes, want %d", len(got), len(original))
			}
			return err
		}},
	}
	for _, op := range ops[1:] {
		if err := op.do(); err != nil {
			t.Fatalf("%s() of a store whose daemon is there = %v", op.name, err)
		}
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		err := op.do()
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s() of a store whose daemon is gone = %v, want an error that is ErrUnavailable and is not fs.ErrNotExist",
				op.name, err)
		}
	}

	h = serveSockets()
	for _, op := range ops {
		if err := op.do(); err != nil {
			t.Errorf("%s() after a daemon serves the sockets again = %v, want it to work with no new Open", op.name, err)
		}
	}
}
