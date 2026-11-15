package dbclient_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// script returns a function for pair.both from a function that logs, and ends the Update with an event if end is true.
func script(end bool, f func(tx txn, log func(format string, args ...any)) error) func(tx txn) ([]string, error) {
	return func(tx txn) ([]string, error) {
		var lines []string
		log := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
		if err := f(tx, log); err != nil {
			return lines, err
		}
		if end {
			return lines, tx.AppendEvent([]byte(`{"e":"end"}`))
		}
		return lines, nil
	}
}

func seedBase(tx txn) error {
	for _, k := range []string{"a", "b", "c"} {
		if err := tx.Put(2, []byte(k), []byte("base-"+k)); err != nil {
			return err
		}
	}
	return tx.AppendEvent([]byte(`{"base":true}`))
}

func putS(tx txn, table uint32, key, value string) error {
	return tx.Put(table, []byte(key), []byte(value))
}
func delS(tx txn, table uint32, key string) error { return tx.Delete(table, []byte(key)) }

// TestReadYourWrites runs the calls of each sentence of the rules for Get, Delete, Put and Object on a client and on an
// engine, and compares what they give.
func TestReadYourWrites(t *testing.T) {
	t.Run("Get gives the last Put", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		log := p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			log(fmtGet(tx, 2, "k"))
			must(t, putS(tx, 2, "k", "1"))
			log(fmtGet(tx, 2, "k"))
			must(t, putS(tx, 2, "k", "2"))
			log(fmtGet(tx, 2, "k"))
			must(t, putS(tx, 2, "a", "again"))
			log(fmtGet(tx, 2, "a"))
			return nil
		}))
		if !strings.Contains(log[2], `"2"`) || !strings.Contains(log[3], `"again"`) {
			t.Errorf("the log is %q", log)
		}
	})

	t.Run("Get after Delete is missing, and after Put again is the value", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		log := p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			log(fmtGet(tx, 2, "b"))
			must(t, delS(tx, 2, "b"))
			log(fmtGet(tx, 2, "b"))
			must(t, putS(tx, 2, "b", "new"))
			log(fmtGet(tx, 2, "b"))
			must(t, delS(tx, 2, "b"))
			must(t, delS(tx, 2, "b")) // it is not seen now: nothing
			must(t, putS(tx, 2, "b", "newer"))
			log(fmtGet(tx, 2, "b"))
			must(t, putS(tx, 2, "x", "1"))
			must(t, delS(tx, 2, "x")) // a key that was put by the transaction is seen
			log(fmtGet(tx, 2, "x"))
			log(fmtScan(tx, 2, ""))
			return nil
		}))
		if !strings.Contains(log[1], "missing") || !strings.Contains(log[2], `"new"`) || !strings.Contains(log[3], `"newer"`) ||
			!strings.Contains(log[4], "missing") {
			t.Errorf("the log is %q", log)
		}
	})

	t.Run("Get keeps what the daemon answered, and the writes come first", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		p.rec.reset()
		p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			log(fmtGet(tx, 2, "a"))  // a hit
			log(fmtGet(tx, 2, "zz")) // a miss
			must(t, putS(tx, 2, "a", "own"))
			must(t, putS(tx, 2, "zz", "own"))
			log(fmtGet(tx, 2, "a"))
			log(fmtGet(tx, 2, "zz"))
			must(t, delS(tx, 2, "a"))
			log(fmtGet(tx, 2, "a"))
			return nil
		}))
		if got := p.rec.count(dbwire.KindGet); got != 2 {
			t.Errorf("%d Get requests, want one for each key", got)
		}
	})

	t.Run("Delete of a key that is not seen adds no op", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		seq := p.f.state().Seq
		p.rec.reset()
		p.both(t, script(false, func(tx txn, log func(string, ...any)) error {
			must(t, delS(tx, 2, "missing"))
			must(t, delS(tx, 7, "missing")) // a table that does not exist
			log(fmtGet(tx, 2, "missing"))
			return nil
		}))
		if got := p.f.state().Seq; got != seq {
			t.Errorf("the store is at %d, was at %d", got, seq)
		}
		wantRequests(t, p.rec, dbwire.KindSnapshot, dbwire.KindGet, dbwire.KindGet, dbwire.KindRelease)
	})

	t.Run("Delete looks with the rule of Get", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		p.rec.reset()
		p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			log(fmtGet(tx, 2, "a")) // kept
			must(t, delS(tx, 2, "a"))
			log(fmtGet(tx, 2, "a"))
			must(t, delS(tx, 2, "b")) // asked, and kept
			log(fmtGet(tx, 2, "b"))
			must(t, putS(tx, 2, "n", "1"))
			must(t, delS(tx, 2, "n")) // from the writes
			return nil
		}))
		if got := p.rec.count(dbwire.KindGet); got != 2 {
			t.Errorf("%d Get requests, want 2", got)
		}
	})

	t.Run("Put copies its key and value", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			key, value := []byte("key"), []byte("value")
			must(t, tx.Put(2, key, value))
			key[0], value[0] = 'K', 'V'
			log(fmtGet(tx, 2, "key"))
			log(fmtGet(tx, 2, "Key"))
			v, _, _ := tx.Get(2, []byte("key"))
			_ = append(v, "tail"...) // no spare capacity: this does not reach the value that the transaction has
			log(fmtGet(tx, 2, "key"))
			log(fmtScan(tx, 2, ""))
			return nil
		}))
		if got := rows(t, p.c, 2, "k"); !slices.Equal(got, []string{"key=value"}) {
			t.Errorf("the store has %q", got)
		}
	})

	t.Run("Object gives an object that was put", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		small, large := []byte("small"), bytes.Repeat([]byte("large "), 100) // the engine puts large ones in files
		p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			for _, data := range [][]byte{small, large, nil} {
				h := sha256.Sum256(data)
				got, found, err := tx.Object(h)
				log("Object before: %d %v %v", len(got), found, err)
				must(t, tx.PutObject(h, data))
				got, found, err = tx.Object(h)
				log("Object after: %d %v %v %v", len(got), found, err, bytes.Equal(got, data))
			}
			must(t, tx.PutObject(sha256.Sum256(small), small)) // twice
			return nil
		}))
		for _, data := range [][]byte{small, large, nil} {
			must(t, p.c.View(bg, func(tx *dbclient.Tx) error {
				got, found, err := tx.Object(sha256.Sum256(data))
				if err != nil || !found || !bytes.Equal(got, data) {
					t.Errorf("Object of %d bytes: %d bytes, %v, %v", len(data), len(got), found, err)
				}
				return nil
			}))
		}
	})

	t.Run("PutObject of an object that the store has adds nothing", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		data := bytes.Repeat([]byte("an object "), 20)
		h := sha256.Sum256(data)
		put := script(true, func(tx txn, log func(string, ...any)) error { return tx.PutObject(h, data) })
		p.both(t, put)
		before := p.f.state().Objects
		p.both(t, put)
		if after := p.f.state().Objects; len(before) != 1 || !slices.Equal(before, after) {
			t.Errorf("the objects were %v, and are %v", before, after)
		}
	})
}

// ownWrites is the Scan block of TestOwnWrites of internal/logdb, for any transaction: the keys that the transaction has
// written before are given by the first Scan, and what fn writes in it does not change them. It returns the keys that
// the Scan gave, as a log, and fails the test by itself if they are not those of the table when Scan was called.
func ownWrites(t *testing.T, tx txn, values func(key string) string) []string {
	t.Helper()
	var log []string
	keys := make([]string, 0, 40)
	for i := range 40 {
		key := fmt.Sprintf("k%02d", 2*i)
		keys = append(keys, key)
		must(t, putS(tx, 1, key, values(key)))
	}
	var seen []string
	err := tx.Scan(1, []byte("k"), func(k, v []byte) bool {
		if len(seen) > 1000 {
			t.Error("the Scan does not end: it sees what its function writes")
			return false
		}
		seen = append(seen, string(k))
		if !strings.HasPrefix(string(v), string(k)) {
			t.Errorf("Scan: %s=%s", k, v)
		}
		for _, w := range []error{
			putS(tx, 1, string(k)+"x", string(v)),
			putS(tx, 1, fmt.Sprintf("k%02d", len(seen)*2+1), string(v)),
			delS(tx, 1, "k78"),
		} {
			if w != nil {
				t.Error(w)
			}
		}
		return true
	})
	must(t, err)
	if !slices.Equal(seen, keys) {
		t.Errorf("Scan with writes saw %q, want %q", seen, keys)
	}
	log = append(log, "seen "+strings.Join(seen, " "))
	log = append(log, fmtGet(tx, 1, "k78"), fmtGet(tx, 1, "k00x"))
	// The Scan that starts after the writes sees them.
	state := map[string]string{}
	for i := range 40 {
		key := fmt.Sprintf("k%02d", 2*i)
		state[key], state[key+"x"], state[fmt.Sprintf("k%02d", 2*i+3)] = values(key), values(key), values(key)
	}
	delete(state, "k78")
	log = append(log, fmtScan(tx, 1, "k"))
	var after []string
	must(t, tx.Scan(1, []byte("k"), func(k, _ []byte) bool { after = append(after, string(k)); return true }))
	if want := slices.Sorted(maps.Keys(state)); !slices.Equal(after, want) {
		t.Errorf("the Scan after the writes saw %d keys, want %d", len(after), len(want))
	}
	// fn may call every method, also Scan of the same table, and the writes of the nested Scan are not seen by the outer one.
	var outer, inner []string
	must(t, tx.Scan(1, []byte("k7"), func(k, _ []byte) bool {
		outer = append(outer, string(k))
		if len(outer) == 2 {
			must(t, tx.Scan(1, []byte("k7"), func(k, _ []byte) bool {
				inner = append(inner, string(k))
				must(t, putS(tx, 1, string(k)+"n", "n"))
				return true
			}))
		}
		return true
	}))
	log = append(log, "outer "+strings.Join(outer, " "), "inner "+strings.Join(inner, " "))
	return log
}

func TestScanSeesTableAsCalled(t *testing.T) {
	t.Run("the writes of the transaction", func(t *testing.T) {
		p := newPair(t, nil, func(tx txn) error { return putS(tx, 1, "base", "0") })
		log := p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			for _, line := range ownWrites(t, tx, func(k string) string { return k }) {
				log("%s", line)
			}
			return nil
		}))
		if len(log) == 0 {
			t.Error("nothing was logged")
		}
	})

	t.Run("rows of the daemon, over pages", func(t *testing.T) {
		pad := strings.Repeat("-", 700) // a page of the daemon has 5 or 6 rows
		values := func(k string) string { return k + pad }
		p := newPair(t, nil, func(tx txn) error {
			for i := 0; i < 40; i += 2 { // the keys of half of the rows
				key := fmt.Sprintf("k%02d", 2*i)
				if err := putS(tx, 1, key, key+"old"+pad); err != nil {
					return err
				}
			}
			return tx.AppendEvent([]byte(`{"old":true}`))
		})
		p.rec.reset()
		log := p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			// The daemon's rows that the transaction deletes are not given, those that it puts are given as it has them.
			must(t, delS(tx, 1, "k04"))
			must(t, delS(tx, 1, "k60"))
			for _, line := range ownWrites(t, tx, values) {
				log("%s", line)
			}
			return nil
		}))
		if p.rec.count(dbwire.KindScan) < 10 {
			t.Errorf("%d Scan requests: the rows did not take pages", p.rec.count(dbwire.KindScan))
		}
		// k04 and k60 were put again by the Scan block.
		_ = log
		got := rows(t, p.c, 1, "k00")
		if len(got) != 2 || !strings.HasPrefix(got[0], "k00="+"k00") || !strings.HasPrefix(got[1], "k00x=k00") {
			t.Errorf("rows %q", got)
		}
	})

	t.Run("a delete of a row of the daemon that the Scan has not reached", func(t *testing.T) {
		p := newPair(t, nil, func(tx txn) error {
			for i := range 30 {
				if err := putS(tx, 2, fmt.Sprintf("k%02d", i), strings.Repeat("v", 600)); err != nil {
					return err
				}
			}
			return tx.AppendEvent([]byte(`{"old":true}`))
		})
		log := p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			var seen []string
			must(t, tx.Scan(2, []byte("k"), func(k, _ []byte) bool {
				if len(seen) > 1000 {
					t.Error("the Scan does not end: it sees what its function writes")
					return false
				}
				seen = append(seen, string(k))
				must(t, delS(tx, 2, "k29"))
				must(t, delS(tx, 2, "k25"))
				must(t, delS(tx, 2, string(k))) // the current one
				must(t, putS(tx, 2, "k29", "again"))
				return true
			}))
			log("seen %d: %s", len(seen), strings.Join(seen, " "))
			log(fmtScan(tx, 2, ""))
			return nil
		}))
		if !strings.HasPrefix(log[0], "seen 30: k00 k01") {
			t.Errorf("the Scan saw %q", log[0])
		}
		if log[1] != "Scan 2 \"\": "+"\"k29\"="+digest([]byte("again")) {
			t.Errorf("the table after the Scan is %q", log[1])
		}
	})
}

func TestGetOnce(t *testing.T) {
	f := newFixture(t, nil)
	w, rec := f.writer(dbclient.Options{})
	commit(t, w, func(tx *dbclient.Tx) {
		put(t, tx, 2, "k1", "v1")
		put(t, tx, 2, "k2", "v2")
	})
	keys := []string{"k1", "k2", "k3"}
	thousand := func(tx *dbclient.Tx) {
		for i := range 1000 {
			v, ok := get(t, tx, 2, keys[i%3])
			if want := fmt.Sprintf("v%d", i%3+1); i%3 < 2 && (!ok || v != want) || i%3 == 2 && ok {
				t.Fatalf("Get %s: %q, %v", keys[i%3], v, ok)
			}
		}
	}
	t.Run("in a View", func(t *testing.T) {
		rec.reset()
		must(t, w.View(bg, func(tx *dbclient.Tx) error { thousand(tx); return nil }))
		if got := rec.count(dbwire.KindGet); got != 3 {
			t.Errorf("%d Get requests", got)
		}
	})
	t.Run("in an Update", func(t *testing.T) {
		rec.reset()
		must(t, w.Update(bg, func(tx *dbclient.Tx) error { thousand(tx); return nil }))
		if got := rec.count(dbwire.KindGet); got != 3 {
			t.Errorf("%d Get requests", got)
		}
	})
	t.Run("the writes come first", func(t *testing.T) {
		rec.reset()
		commit(t, w, func(tx *dbclient.Tx) {
			get(t, tx, 2, "k3") // a miss, kept
			put(t, tx, 2, "k3", "own")
			if v, ok := get(t, tx, 2, "k3"); !ok || v != "own" {
				t.Errorf("a Put after a kept miss: %q, %v", v, ok)
			}
			get(t, tx, 2, "k1") // a hit, kept
			del(t, tx, 2, "k1")
			if _, ok := get(t, tx, 2, "k1"); ok {
				t.Error("a Delete after a kept hit: found")
			}
			del(t, tx, 2, "k1") // not seen now
			put(t, tx, 2, "k1", "back")
			if v, _ := get(t, tx, 2, "k1"); v != "back" {
				t.Errorf("a Put after a Delete: %q", v)
			}
		})
		if got := rec.count(dbwire.KindGet); got != 2 {
			t.Errorf("%d Get requests, want 2", got)
		}
	})
	t.Run("Delete asks once", func(t *testing.T) {
		rec.reset()
		commit(t, w, func(tx *dbclient.Tx) {
			del(t, tx, 2, "k2")
			if _, ok := get(t, tx, 2, "k2"); ok {
				t.Error("found a key that was deleted")
			}
		})
		if got := rec.count(dbwire.KindGet); got != 1 {
			t.Errorf("%d Get requests, want 1", got)
		}
	})
	t.Run("Scan keeps no answers", func(t *testing.T) {
		rec.reset()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.Scan(2, nil, func(_, _ []byte) bool { return true }))
			get(t, tx, 2, "k3")
			return nil
		}))
		if got := rec.count(dbwire.KindGet); got != 1 {
			t.Errorf("%d Get requests, want 1", got)
		}
	})
}

// pageRows makes the rows of TestScanPages: each of 100 bytes in a page, so that a page of a frame of 4096 has 41 rows.
func pageRows(n int) map[string]string {
	m := map[string]string{}
	for i := range n {
		m[fmt.Sprintf("k%03d", i)] = strings.Repeat("v", 87)
	}
	return m
}

func TestScanPages(t *testing.T) {
	for _, c := range []struct {
		name  string
		rows  int
		pages int
	}{
		{"partial last page", 200, 5},
		{"a last page that is full: one more request, with no row", 164, 5},
		{"one page", 20, 1},
		{"no row", 0, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, nil)
			w, rec := f.writer(dbclient.Options{})
			data := pageRows(c.rows)
			commit(t, w, func(tx *dbclient.Tx) {
				for k, v := range data {
					put(t, tx, 2, k, v)
				}
			})
			rec.reset()
			var got []string
			must(t, w.View(bg, func(tx *dbclient.Tx) error {
				return tx.Scan(2, []byte("k"), func(k, v []byte) bool {
					if got = append(got, string(k)); string(v) != data[string(k)] {
						t.Errorf("%s has the value %q", k, v)
					}
					// Requests between the pages, on the same connection.
					if g, ok := get(t, tx, 2, string(k)); !ok || g != data[string(k)] {
						t.Errorf("Get of %s: %q, %v", k, g, ok)
					}
					return true
				})
			}))
			if want := slices.Sorted(maps.Keys(data)); !slices.Equal(got, want) {
				t.Errorf("got %d rows, want %d", len(got), len(want))
			}
			if n := rec.count(dbwire.KindScan); n != c.pages {
				t.Errorf("%d Scan requests, want %d", n, c.pages)
			}
		})
	}

	t.Run("an early stop asks for no more", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		data := pageRows(200)
		commit(t, w, func(tx *dbclient.Tx) {
			for k, v := range data {
				put(t, tx, 2, k, v)
			}
		})
		for _, c := range []struct{ stop, requests int }{{1, 1}, {41, 1}, {42, 2}, {82, 2}, {83, 3}, {200, 5}} {
			rec.reset()
			n := 0
			must(t, w.View(bg, func(tx *dbclient.Tx) error {
				return tx.Scan(2, nil, func(_, _ []byte) bool { n++; return n < c.stop })
			}))
			if got := rec.count(dbwire.KindScan); n != c.stop || got != c.requests {
				t.Errorf("stop at %d: %d rows, %d Scan requests, want %d", c.stop, n, got, c.requests)
			}
		}
	})

	t.Run("the writes of the transaction at the borders of the pages", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		data := pageRows(200)
		commit(t, w, func(tx *dbclient.Tx) {
			for k, v := range data {
				put(t, tx, 2, k, v)
			}
		})
		rec.reset()
		model := maps.Clone(data)
		must(t, w.Update(bg, func(tx *dbclient.Tx) error {
			set := func(k, v string) { put(t, tx, 2, k, v); model[k] = v }
			unset := func(k string) { del(t, tx, 2, k); delete(model, k) }
			set("k", "first")         // before the first row
			set("k0400", "last-of-1") // between the last row of the first page (k040) and the first of the second
			unset("k040")             // the last row of a page
			unset("k041")             // the first row of the next one
			set("k082", "overridden") // the last row of the second page, put again
			unset("k123")
			set("k123", "again")
			unset("k164")
			set("k199z", "after the last") // after the last row
			set("k200", "past")
			unset("k100") // and put back as another key
			var got []string
			must(t, tx.Scan(2, []byte("k"), func(k, v []byte) bool {
				got = append(got, string(k)+"="+string(v))
				return true
			}))
			var want []string
			for _, k := range slices.Sorted(maps.Keys(model)) {
				want = append(want, k+"="+model[k])
			}
			if !slices.Equal(got, want) {
				t.Errorf("got %d rows, want %d: %q", len(got), len(want), diffRows(got, want))
			}
			return tx.AppendEvent([]byte(`{"e":1}`))
		}))
	})
}

// diffRows returns the first rows that are not the same.
func diffRows(got, want []string) []string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return []string{fmt.Sprint(i), got[i], want[i]}
		}
	}
	return []string{"the lengths", fmt.Sprint(len(got)), fmt.Sprint(len(want))}
}

func TestLargeValues(t *testing.T) {
	f := newFixture(t, nil)
	w, _ := f.writer(dbclient.Options{})
	big := strings.Repeat("0123456789abcdef", 20_000) // 320,000 bytes: 79 frames of 4,096
	object := bytes.Repeat([]byte("an object of several frames "), 40_000)
	h := sha256.Sum256(object)
	must(t, w.Update(bg, func(tx *dbclient.Tx) error {
		put(t, tx, 2, "big", big)
		for i := range 30 {
			put(t, tx, 2, fmt.Sprintf("other%02d", i), big[:60_000])
		}
		must(t, tx.PutObject(h, object))
		got, found, err := tx.Object(h)
		if err != nil || !found || !bytes.Equal(got, object) {
			t.Errorf("Object in the Update: %d bytes, %v, %v", len(got), found, err)
		}
		return tx.AppendEvent([]byte(strings.Repeat("e", 100_000)))
	}))
	must(t, w.View(bg, func(tx *dbclient.Tx) error {
		if v, ok := get(t, tx, 2, "big"); !ok || v != big {
			t.Errorf("Get: %d bytes, %v", len(v), ok)
		}
		n, size := 0, 0
		must(t, tx.Scan(2, []byte("big"), func(k, v []byte) bool { n++; size += len(v); return true }))
		if n != 1 || size != len(big) {
			t.Errorf("Scan of a row of %d bytes: %d rows of %d bytes", len(big), n, size)
		}
		got, found, err := tx.Object(h)
		if err != nil || !found || !bytes.Equal(got, object) {
			t.Errorf("Object: %d bytes, %v, %v", len(got), found, err)
		}
		must(t, tx.Events(bg, func(_ uint64, data []byte) bool {
			if len(data) == 100_000 && string(data) != strings.Repeat("e", 100_000) {
				t.Error("an event of several frames came back changed")
			}
			return true
		}))
		return nil
	}))
}

func TestEvents(t *testing.T) {
	f := newFixture(t, nil)
	w, rec := f.writer(dbclient.Options{})
	commit(t, w, func(tx *dbclient.Tx) { event(t, tx, "e1") })
	must(t, w.Update(bg, func(tx *dbclient.Tx) error { event(t, tx, "e2"); event(t, tx, "e3"); return nil }))
	type ev struct {
		n    uint64
		data string
	}
	read := func(tx *dbclient.Tx) (out []ev) {
		must(t, tx.Events(bg, func(n uint64, data []byte) bool { out = append(out, ev{n, string(data)}); return true }))
		return out
	}
	t.Run("the marks and the numbers", func(t *testing.T) {
		var got []ev
		must(t, w.View(bg, func(tx *dbclient.Tx) error { got = read(tx); return nil }))
		var want []ev
		for _, e := range f.state().Events {
			want = append(want, ev{e.Number, string(e.Data)})
		}
		if len(got) < 7 || !slices.Equal(got, want) {
			t.Errorf("got %d events, the engine has %d", len(got), len(want))
		}
		marks := 0
		for i, e := range got {
			if e.n != uint64(i+1) {
				t.Errorf("event %d has the number %d", i+1, e.n)
			}
			if _, ok := dbwire.ParseMark([]byte(e.data)); ok {
				marks++
			}
		}
		if seq := f.state().Seq; marks != int(seq) {
			t.Errorf("%d marks after %d commits, want one for each", marks, seq)
		}
	})
	t.Run("up to the snapshot", func(t *testing.T) {
		n0 := len(f.state().Events)
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			seq := tx.Seq()
			commit(t, w, func(tx *dbclient.Tx) { event(t, tx, "later") })
			for _, e := range read(tx) {
				if e.data == "later" {
					t.Error("an event of a commit after the snapshot")
				}
			}
			if got := len(read(tx)); got != n0 {
				t.Errorf("%d events at the commit %d, want %d", got, seq, n0)
			}
			return nil
		}))
		must(t, w.Update(bg, func(tx *dbclient.Tx) error {
			event(t, tx, "pending")
			if got := read(tx); got[len(got)-1].data == "pending" {
				t.Error("the events of the transaction itself are given")
			}
			return nil
		}))
	})
	t.Run("an early stop", func(t *testing.T) {
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			get(t, tx, 2, "k")
			rec.reset()
			n := 0
			must(t, tx.Events(bg, func(uint64, []byte) bool { n++; return n < 2 }))
			if n != 2 {
				t.Errorf("fn was called %d times", n)
			}
			// The transaction goes on, with its own snapshot, and Events again.
			if got := len(read(tx)); got < 7 {
				t.Errorf("%d events", got)
			}
			if _, ok := get(t, tx, 2, "k"); ok {
				t.Error("found a key that nothing put")
			}
			return nil
		}))
		if got := rec.count(dbwire.KindEvents); got != 2 {
			t.Errorf("%d Events requests, want 2", got)
		}
	})
	t.Run("fn uses the transaction", func(t *testing.T) {
		must(t, w.Update(bg, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "v")
			calls := 0
			must(t, tx.Events(bg, func(uint64, []byte) bool {
				calls++
				if v, ok := get(t, tx, 2, "k"); !ok || v != "v" {
					t.Errorf("Get in fn: %q, %v", v, ok)
				}
				return true
			}))
			if calls == 0 {
				t.Error("no event")
			}
			return tx.AppendEvent([]byte(`{"e":1}`))
		}))
	})
}

func TestLimits(t *testing.T) {
	t.Run("keys and tables", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		long := bytes.Repeat([]byte("k"), dbwire.MaxKey+1)
		rec.reset()
		must(t, w.Update(bg, func(tx *dbclient.Tx) error {
			if err := tx.Put(2, long, nil); err == nil || !strings.HasPrefix(err.Error(), "key too long: 4097 bytes (max 4096)") {
				t.Errorf("Put of a long key: %v", err)
			}
			if err := tx.Delete(2, long); err == nil {
				t.Error("Delete of a long key")
			}
			if err := tx.Put(0, []byte("k"), nil); err == nil || err.Error() != "invalid table 0" {
				t.Errorf("Put to table 0: %v", err)
			}
			if err := tx.Delete(0, []byte("k")); err == nil {
				t.Error("Delete in table 0")
			}
			if _, ok, err := tx.Get(2, long); ok || err != nil {
				t.Errorf("Get of a long key: %v, %v", ok, err)
			}
			if err := tx.Scan(2, long, func(_, _ []byte) bool { t.Error("a row"); return true }); err != nil {
				t.Errorf("Scan of a long prefix: %v", err)
			}
			if _, ok, err := tx.Get(0, []byte("k")); ok || err != nil {
				t.Errorf("Get in table 0: %v, %v", ok, err)
			}
			return nil
		}))
		// Nothing was written, and the daemon was asked only for table 0.
		wantRequests(t, rec, dbwire.KindSnapshot, dbwire.KindGet, dbwire.KindRelease)
	})

	t.Run("the size of a transaction", func(t *testing.T) {
		f := newFixture(t, func(o *dbtest.Options) { o.Limits.Commit = 3000 })
		w, rec := f.writer(dbclient.Options{})
		rec.reset()
		var errs []error
		must(t, w.Update(bg, func(tx *dbclient.Tx) error {
			for i := range 3 {
				errs = append(errs, tx.Put(2, fmt.Appendf(nil, "k%d", i), bytes.Repeat([]byte("v"), 1200)))
			}
			return tx.AppendEvent([]byte(`{"e":1}`))
		}))
		if errs[0] != nil || errs[1] != nil || errs[2] == nil || !strings.HasPrefix(errs[2].Error(), "the transaction is too large: ") ||
			!strings.HasSuffix(errs[2].Error(), "bytes (max 3000)") {
			t.Errorf("the errors of the Puts: %v", errs)
		}
		if got := rows(t, w, 2, ""); len(got) != 2 {
			t.Errorf("the store has %d rows, want the two that fit", len(got))
		}
	})

	t.Run("objects", func(t *testing.T) {
		f := newFixture(t, func(o *dbtest.Options) { o.Limits.Object, o.Limits.Staged = 1000, 1500 })
		w, rec := f.writer(dbclient.Options{})
		rec.reset()
		a, b, c := bytes.Repeat([]byte("a"), 800), bytes.Repeat([]byte("b"), 800), bytes.Repeat([]byte("c"), 1001)
		must(t, w.Update(bg, func(tx *dbclient.Tx) error {
			must(t, tx.PutObject(sha256.Sum256(a), a))
			must(t, tx.PutObject(sha256.Sum256(a), a)) // the same again: not sent
			if err := tx.PutObject(sha256.Sum256(b), b); err == nil || !strings.Contains(err.Error(), "the staged objects would be over 1500 bytes") {
				t.Errorf("a second object: %v", err)
			}
			if err := tx.PutObject(sha256.Sum256(c), c); err == nil || !strings.Contains(err.Error(), "max 1000") {
				t.Errorf("an object over the limit: %v", err)
			}
			return nil
		}))
		wantRequests(t, rec, dbwire.KindSnapshot, dbwire.KindPutObject, dbwire.KindCommit)
		if got := f.state().Objects; len(got) != 1 {
			t.Errorf("the store has %d objects", len(got))
		}
	})
}
