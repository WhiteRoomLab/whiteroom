package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// backupOriginal is the original that the writer of TestBackupWhileWriting puts with the pair n, a multiple of 5.
func backupOriginal(n int) []byte {
	return bytes.Repeat(fmt.Appendf(nil, "the original of the pair %d ", n), 40)
}

// backupMedia is the ID of the media document that names backupOriginal(n).
func backupMedia(n int) string {
	return fmt.Sprintf("med_01JE%022d", n)
}

// TestBackupWhileWriting takes three copies of a store of the log backend with backupLog while a writer puts the pair
// n = 1, 2, ..., with every fifth an original, put before the Update, and a media document that names it in the
// Update, in segments of 4096 bytes with checkpoints every 8192 bytes. Each copy opens with open: it has the pair of
// one n that the writer had committed while backupLog ran, n versions of each term, the media documents of the
// multiples of 5 up to n, and their originals, whole; it passes checkIntegrity and checkConsistent, and goes on with
// the next pair.
func TestBackupWhileWriting(t *testing.T) {
	t.Parallel() // with the matrices, after the tests that do not run in parallel
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		t.Parallel()
		o := *tb.log
		o.SegmentBytes, o.CheckpointBytes = 4096, 8192
		s, dir := newLogStore(t, o)
		var committed atomic.Int64 // the last n whose Update has returned nil
		stop := make(chan struct{})
		ended := make(chan error, 1)
		go func() {
			for n := 1; ; n++ {
				select {
				case <-stop:
					ended <- nil
					return
				default:
				}
				var original []byte
				if n%5 == 0 {
					original = backupOriginal(n)
					if _, err := s.PutOriginal(t.Context(), bytes.NewReader(original)); err != nil {
						ended <- fmt.Errorf("PutOriginal() of the pair %d = %w", n, err)
						return
					}
				}
				if err := s.Update(t.Context(), withRecord(func(tx *Tx) error {
					if original != nil {
						id := backupMedia(n)
						if _, err := tx.Put(content.RefMedia, id, mediaJSON(id, original), userID); err != nil {
							return err
						}
					}
					return putPair(tx, n)
				})); err != nil {
					ended <- fmt.Errorf("Update() of the pair %d = %w", n, err)
					return
				}
				committed.Store(int64(n))
			}
		}()
		defer func() {
			close(stop)
			if err := <-ended; err != nil {
				t.Error(err)
			}
		}()
		for i := range 3 {
			// Some Updates between the copies.
			for deadline := time.Now().Add(10 * time.Second); committed.Load() < int64(10*(i+1)); {
				if time.Now().After(deadline) {
					t.Fatalf("the writer has committed %d pairs in 10 seconds", committed.Load())
				}
				time.Sleep(time.Millisecond)
			}
			before := committed.Load()
			copied := filepath.Join(t.TempDir(), "copy")
			if err := backupLog(t.Context(), dir, copied); err != nil {
				t.Fatalf("backupLog() %d = %v", i+1, err)
			}
			after := committed.Load()
			c := reopenLog(t, copied, o)
			checkIntegrity(t, c)
			checkConsistent(t, c)
			var n int
			view(t, c, func(tx *Tx) error {
				var err error
				// The commit of the copy may be one whose Update had not returned yet.
				if n, err = checkPairView(tx); err != nil || int64(n) < before || int64(n) > after+1 {
					t.Errorf("the copy %d has the pair of %d, %v; want one from %d to %d", i+1, n, err, before, after+1)
				}
				docs, err := tx.Documents(content.RefMedia)
				if err != nil || len(docs) != n/5 {
					t.Errorf("the copy %d of the pair %d has %d media documents, %v; want %d", i+1, n, len(docs), err,
						n/5)
				}
				for m := 5; m <= n; m += 5 {
					data, err := readOriginalData(c, backupOriginal(m))
					if err != nil || !bytes.Equal(data, backupOriginal(m)) {
						t.Errorf("the copy %d of the pair %d: the original of %d reads %d bytes, %v", i+1, n, m,
							len(data), err)
					}
				}
				return nil
			})
			update(t, c, func(tx *Tx) error { return putPair(tx, n+1) })
			t.Logf("the copy %d has the pair of %d (the writer had committed %d to %d)", i+1, n, before, after)
		}
	})
}

// checkPairView returns the number of the pair in tx, or 0 if tx has neither of its terms, after it has checked that
// both terms have the number, that Documents has both of them with it and no other term, and that each has as many
// versions as the number. The readers of TestReadersWhileWriting check every View with it too.
func checkPairView(tx *Tx) (int, error) {
	numbers, err := readPair(tx)
	docs, derr := tx.Documents(content.RefTerm)
	switch {
	case derr != nil:
		return 0, derr
	case errors.Is(err, fs.ErrNotExist) && len(docs) == 0:
		return 0, nil
	case err != nil:
		return 0, err
	case numbers[0] != numbers[1]:
		return 0, fmt.Errorf("the pair has the numbers %v", numbers)
	case len(docs) != len(pair):
		return 0, fmt.Errorf("Documents() has %d terms, want the %d of the pair", len(docs), len(pair))
	}
	for i, d := range docs {
		n, err := pairNumber(d.Data)
		switch {
		case err != nil:
			return 0, err
		case d.ID != pair[i] || n != numbers[0]:
			return 0, fmt.Errorf("Documents() has %s with the number %d, want %s with %d", d.ID, n, pair[i], numbers[0])
		}
	}
	for _, id := range pair {
		versions, err := tx.History(content.RefTerm, id)
		switch {
		case err != nil:
			return 0, err
		case len(versions) != numbers[0]:
			return 0, fmt.Errorf("History(%q) has %d versions, want %d", id, len(versions), numbers[0])
		}
	}
	return numbers[0], nil
}

// readOriginalData reads the original data from s to its end, which checks its hash.
func readOriginalData(s *Store, data []byte) ([]byte, error) {
	f, err := s.OpenOriginal(sum(data))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	_, err = b.ReadFrom(f)
	return b.Bytes(), errors.Join(err, f.Close())
}
