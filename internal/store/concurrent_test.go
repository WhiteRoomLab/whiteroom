package store

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// pair are the IDs of two terms that the tests put together, in one Update, with the same number as their names.
var pair = [2]string{termID, "trm_01JE0000000000000000000000"}

// putPair puts the terms of pair with the name n.
func putPair(tx *Tx, n int) error {
	for _, id := range pair {
		data := []byte(`{"schemaVersion": 1, "id": "` + id + `", "name": "` + strconv.Itoa(n) + `"}`)
		if _, err := tx.Put(content.RefTerm, id, data, userID); err != nil {
			return err
		}
	}
	return nil
}

// pairNumber returns the number in the name of a term of pair, whose JSON is data.
func pairNumber(data []byte) (int, error) {
	var term content.Term
	if err := term.UnmarshalJSON(data); err != nil {
		return 0, err
	}
	return strconv.Atoi(term.Name)
}

// readPair returns the numbers in the names of the terms of pair.
func readPair(tx *Tx) ([2]int, error) {
	var numbers [2]int
	for i, id := range pair {
		d, err := tx.Document(content.RefTerm, id)
		if err != nil {
			return numbers, err
		}
		if numbers[i], err = pairNumber(d.Data); err != nil {
			return numbers, err
		}
	}
	return numbers, nil
}

// TestConcurrent writes and reads documents, revisions and originals in many goroutines at once.
func TestConcurrent(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		const writes = 20 // of each goroutine that writes
		s, dir := newStore(t, tb)
		same, other := bytes.Repeat([]byte("same "), 20_000), bytes.Repeat([]byte("other "), 20_000)
		update(t, s, func(tx *Tx) error {
			if _, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID); err != nil {
				return err
			}
			return putPair(tx, 0)
		})
		putOriginal(t, s, same)
		putOriginal(t, s, other)
		var revisions []string
		for i := range writes {
			revisions = append(revisions, fmt.Sprintf("rev_01JE%022d", i))
		}

		var writers, readers sync.WaitGroup
		done := make(chan struct{}) // closed when the writers have ended
		for w := range 2 {
			writers.Go(func() {
				for i := range writes {
					if err := s.Update(t.Context(), withRecord(func(tx *Tx) error {
						return putPair(tx, 1+w*writes+i)
					})); err != nil {
						t.Errorf("Update() of the pair = %v", err)
						return
					}
				}
			})
		}
		writers.Go(func() {
			for _, id := range revisions {
				if err := s.Update(t.Context(), withRecord(func(tx *Tx) error {
					_, err := tx.AddRevision(entryID, id, revJSON(id), userID)
					return err
				})); err != nil {
					t.Errorf("Update() of a revision = %v", err)
					return
				}
			}
		})
		for _, data := range [][]byte{same, same, other} {
			writers.Go(func() {
				want := Original{Hash: sum(data), Size: int64(len(data))}
				for range writes {
					if o, err := s.PutOriginal(t.Context(), bytes.NewReader(data)); err != nil || o != want {
						t.Errorf("PutOriginal() = %+v, %v; want %+v", o, err, want)
						return
					}
				}
			})
		}
		// read calls f until the writers have ended, or until f fails.
		read := func(f func() error) {
			for {
				if err := f(); err != nil {
					t.Error(err)
					return
				}
				select {
				case <-done:
					return
				default:
					// Gives the writers their turns when GOMAXPROCS is 1.
					runtime.Gosched()
				}
			}
		}
		viewPair := func(tx *Tx) error {
			numbers, err := readPair(tx)
			if err != nil {
				return err
			}
			docs, err := tx.Documents(content.RefTerm)
			if err != nil {
				return err
			}
			var listed []int
			for _, d := range docs {
				n, err := pairNumber(d.Data)
				if err != nil {
					return err
				}
				listed = append(listed, n)
			}
			if numbers[0] != numbers[1] || !slices.Equal(listed, numbers[:]) {
				return fmt.Errorf("View() reads the names %v of the pair, and %v from Documents()", numbers, listed)
			}
			return nil
		}
		readOriginals := func() error {
			for _, want := range [][]byte{same, other} {
				f, err := s.OpenOriginal(sum(want))
				if err != nil {
					return err
				}
				data, err := io.ReadAll(f)
				_ = f.Close()
				if err != nil {
					return err
				}
				if !bytes.Equal(data, want) {
					return fmt.Errorf("OpenOriginal() reads %d bytes, want the %d bytes of the original", len(data),
						len(want))
				}
			}
			return nil
		}
		for range 4 {
			readers.Go(func() { read(func() error { return s.View(t.Context(), viewPair) }) })
		}
		for range 2 {
			readers.Go(func() { read(readOriginals) })
		}
		writers.Wait()
		close(done)
		readers.Wait()

		checkConsistent(t, s)
		checkIntegrity(t, s)
		view(t, s, func(tx *Tx) error {
			for _, id := range pair {
				if h, err := tx.History(content.RefTerm, id); err != nil || len(h) != 1+2*writes {
					t.Errorf("History(%q) = %d versions, %v; want %d", id, len(h), err, 1+2*writes)
				}
			}
			revs, err := tx.Revisions(entryID)
			var ids []string
			for _, r := range revs {
				ids = append(ids, r.ID)
			}
			if err != nil || !slices.Equal(ids, revisions) {
				t.Errorf("Revisions() = %q, %v; want %q", ids, err, revisions)
			}
			return nil
		})
		files := filepath.Join(dir, "files")
		for _, name := range names(t, files) {
			if strings.HasPrefix(name, ".tmp-") {
				t.Errorf("files/ has the temporary file %s", name)
			}
		}
		for _, want := range [][]byte{same, other} {
			if data, err := readOriginal(t, s, sum(want)); err != nil || !bytes.Equal(data, want) {
				t.Errorf("OpenOriginal() reads %d bytes, %v; want the %d bytes of the original", len(data), err,
					len(want))
			}
		}
	})
}
