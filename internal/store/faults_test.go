package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// failAt makes the hook of s fail with err at point. called returns the points where the hook has been called.
func failAt(s *Store, point string, err error) (called func() []string) {
	var points []string
	s.hook = func(p string) error {
		points = append(points, p)
		if p == point {
			return err
		}
		return nil
	}
	return func() []string { return points }
}

// TestCommitFault checks that an Update that fails at its commit leaves nothing of what f has written.
func TestCommitFault(t *testing.T) {
	s, _ := newStore(t)
	errHook := errors.New("hook")
	called := failAt(s, "commit", errHook)
	err := s.Update(t.Context(), func(tx *Tx) error {
		for _, id := range []string{entryID, pageID} {
			if _, err := tx.Put(content.RefEntry, id, docJSON(content.RefEntry, id), userID); err != nil {
				return err
			}
		}
		if _, err := tx.Put(content.RefTerm, termID, docJSON(content.RefTerm, termID), userID); err != nil {
			return err
		}
		_, err := tx.AddRevision(entryID, revisionID, revJSON("About"), userID)
		return err
	})
	if !errors.Is(err, errHook) || err.Error() != errHook.Error() {
		t.Errorf("Update() = %v, want the error of the hook", err)
	}
	if got, want := called(), []string{"commit"}; !slices.Equal(got, want) {
		t.Errorf("the hook has been called at %q, want %q", got, want)
	}
	checkEmpty(t, s)
	checkConsistent(t, s)
	checkMissing(t, s, content.RefEntry, entryID, "entries/"+entryID+".json", nil)
	checkMissing(t, s, content.RefEntry, pageID, "entries/"+pageID+".json", nil)
	checkMissing(t, s, content.RefTerm, termID, "terms/"+termID+".json", nil)
	view(t, s, func(tx *Tx) error {
		if r, err := tx.Revision(revisionID); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Revision() = %+v, %v; want an error that wraps fs.ErrNotExist", r, err)
		}
		if revs, err := tx.Revisions(entryID); err != nil || revs != nil {
			t.Errorf("Revisions() = %+v, %v; want none", revs, err)
		}
		return nil
	})

	// The next Update commits.
	s.hook = nil
	putWithRevision(t, s)
	for _, table := range []string{"versions", "revisions"} {
		if n := count(t, s, table); n != 1 {
			t.Errorf("%s after the next Update() = %d, want 1", table, n)
		}
	}
}

// TestOriginalFaults checks what PutOriginal leaves in files/ when it fails at the points of its hook.
func TestOriginalFaults(t *testing.T) {
	data := []byte("GIF89a")
	hash := sum(data)
	digits := strings.TrimPrefix(hash, "sha256:")
	for _, tt := range []struct {
		point  string
		points []string // where the hook is called
		kept   bool     // the original
	}{
		{"original:synced", []string{"original:synced"}, false},
		{"original:renamed", []string{"original:synced", "original:renamed"}, true},
	} {
		t.Run(tt.point, func(t *testing.T) {
			s, dir := newStore(t)
			errHook := errors.New("hook")
			called := failAt(s, tt.point, errHook)
			o, err := s.PutOriginal(t.Context(), bytes.NewReader(data))
			if o != (Original{}) || !errors.Is(err, errHook) || err.Error() != errHook.Error() {
				t.Errorf("PutOriginal() = %+v, %v; want the error of the hook", o, err)
			}
			if got := called(); !slices.Equal(got, tt.points) {
				t.Errorf("the hook has been called at %q, want %q", got, tt.points)
			}
			s.hook = nil
			files := filepath.Join(dir, "files")
			if !tt.kept {
				if got := names(t, files); got != nil {
					t.Errorf("files/ has %q, want nothing", got)
				}
				f, err := s.OpenOriginal(hash)
				if f != nil {
					_ = f.Close()
				}
				if !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("OpenOriginal() = %v, %v; want an error that wraps fs.ErrNotExist", f, err)
				}
				return
			}
			if got, want := names(t, files), []string{digits[:2]}; !slices.Equal(got, want) {
				t.Errorf("files/ has %q, want %q", got, want)
			}
			if got, want := names(t, filepath.Join(files, digits[:2])), []string{digits}; !slices.Equal(got, want) {
				t.Errorf("files/%s has %q, want %q", digits[:2], got, want)
			}
			if got, err := readOriginal(t, s, hash); err != nil || !bytes.Equal(got, data) {
				t.Errorf("OpenOriginal() reads %q, %v; want %q", got, err, data)
			}
		})
	}
}

// TestUpdateCanceled checks that an Update whose context ends in f writes nothing, although f returns nil.
func TestUpdateCanceled(t *testing.T) {
	s, _ := newStore(t)
	for _, tt := range []struct {
		name     string
		rollback bool // f waits until database/sql has rolled the transaction back, and Commit fails with sql.ErrTxDone
	}{
		{"canceled", false},
		{"rolled back", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := s.Update(ctx, func(tx *Tx) error {
				if _, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID); err != nil {
					return err
				}
				cancel()
				if !tt.rollback {
					return nil
				}
				for {
					_, err := tx.b.(*sqliteTx).tx.ExecContext(context.Background(), "SELECT 1")
					switch {
					case errors.Is(err, sql.ErrTxDone):
						return nil
					case err != nil:
						return err
					}
				}
			})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Update() = %v, want context.Canceled", err)
			}
			checkEmpty(t, s)
		})
	}

	update(t, s, func(tx *Tx) error {
		_, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID)
		return err
	})
	if n := count(t, s, "versions"); n != 1 {
		t.Errorf("versions after the next Update() = %d, want 1", n)
	}
}

// hold calls run, the Update or the View of a store, in a new goroutine, with a function that calls f and then waits
// until release is called, and returns once f has returned nil. release returns the error of run; the end of the test
// calls it too.
func hold(t *testing.T, run func(context.Context, func(*Tx) error) error, f func(*Tx) error) (release func() error) {
	t.Helper()
	started, done := make(chan struct{}), make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- run(context.Background(), func(tx *Tx) error {
			if err := f(tx); err != nil {
				return err
			}
			close(started)
			<-done
			return nil
		})
	}()
	select {
	case <-started:
	case err := <-errc:
		t.Fatalf("the transaction that waits = %v", err)
	}
	release = sync.OnceValue(func() error {
		close(done)
		return <-errc
	})
	t.Cleanup(func() { _ = release() })
	return release
}

// TestUpdateWaiting checks that an Update that waits for another one stops waiting when its context ends, and that a
// View does not wait for it, nor sees what it has written.
func TestUpdateWaiting(t *testing.T) {
	s, _ := newStore(t)
	release := hold(t, s.Update, func(tx *Tx) error {
		_, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID)
		return err
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	called := false
	err := s.Update(ctx, func(*Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Errorf("Update() while another one runs = %v (f called: %v), want context.DeadlineExceeded", err, called)
	}

	// A View that waited for the Update would fail when its context ends.
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.View(ctx, func(tx *Tx) error {
		_, err := tx.Document(content.RefEntry, entryID)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Document() in View() while an Update puts it = %v, want an error that wraps fs.ErrNotExist", err)
		}
		return nil
	}); err != nil {
		t.Errorf("View() while an Update runs = %v", err)
	}

	if err := release(); err != nil {
		t.Errorf("the other Update() = %v", err)
	}
	view(t, s, func(tx *Tx) error {
		if _, err := tx.Document(content.RefEntry, entryID); err != nil {
			t.Errorf("Document() after the Update = %v", err)
		}
		return nil
	})
}

// TestViewsAtOnce checks that a View does not wait for another one.
func TestViewsAtOnce(t *testing.T) {
	s, _ := newStore(t)
	putWithRevision(t, s)
	release := hold(t, s.View, func(tx *Tx) error {
		_, err := tx.Document(content.RefEntry, entryID)
		return err
	})
	// It would fail when its context ends if it waited.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.View(ctx, func(tx *Tx) error {
		_, err := tx.Revision(revisionID)
		return err
	}); err != nil {
		t.Errorf("View() while another one runs = %v", err)
	}
	if err := release(); err != nil {
		t.Errorf("the other View() = %v", err)
	}
}
