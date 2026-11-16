package dbclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// manyKeys puts the rows k0000=v0000, k0001=v0001... of table 2 in one commit, and returns their keys.
func manyKeys(t testing.TB, w *dbclient.Client, n int) [][]byte {
	t.Helper()
	keys := make([][]byte, n)
	commit(t, w, func(tx *dbclient.Tx) {
		for i := range keys {
			keys[i] = fmt.Appendf(nil, "k%04d", i)
			put(t, tx, 2, string(keys[i]), fmt.Sprintf("v%04d", i))
		}
	})
	return keys
}

// manyObjects puts n objects in Updates, and returns their hashes and data: the sizes differ, and some have more than
// one frame.
func manyObjects(t testing.TB, w *dbclient.Client, n int) ([][32]byte, [][]byte) {
	t.Helper()
	hashes, datas := make([][32]byte, n), make([][]byte, n)
	for from := 0; from < n; from += 100 {
		commit(t, w, func(tx *dbclient.Tx) {
			for i := from; i < min(from+100, n); i++ {
				datas[i] = bytes.Repeat(fmt.Appendf(nil, "%06d", i), 17+i%7*150)
				hashes[i] = sha256.Sum256(datas[i])
				must(t, tx.PutObject(hashes[i], datas[i]))
			}
		})
	}
	return hashes, datas
}

func wantCount(t testing.TB, rec *recorder, kind dbwire.Kind, want int, what string) {
	t.Helper()
	if got := rec.count(kind); got != want {
		t.Errorf("%s: %d %v requests, want %d", what, got, kind, want)
	}
}

// within runs f, and fails the test if it returns an error or does not return within waitLimit: a key or a prefix that is
// over the limit, sent to the daemon, is not read by it, and the call would wait.
func within(t testing.TB, what string, f func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(waitLimit):
		t.Fatalf("%s does not end", what)
	}
}

// scanRows reads the rows of table 2 with the prefix, and says how many Scan requests it took.
func scanRows(t testing.TB, tx *dbclient.Tx, rec *recorder, prefix string) (rows []string, requests int) {
	t.Helper()
	before := rec.count(dbwire.KindScan)
	must(t, tx.Scan(2, []byte(prefix), func(k, v []byte) bool {
		rows = append(rows, string(k)+"="+string(v))
		return len(rows) < 10_000 // a Scan that does not end is a failure, not a hang
	}))
	return rows, rec.count(dbwire.KindScan) - before
}

func TestGetMany(t *testing.T) {
	const n = 600
	t.Run("the answers are those of Get, and each key is asked once", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		keys := manyKeys(t, w, n)
		ask := append(slices.Clone(keys), keys[:50]...)
		var missing [][]byte
		for i := range 40 {
			missing = append(missing, fmt.Appendf(nil, "none%03d", i))
		}
		long := bytes.Repeat([]byte("L"), dbwire.MaxKey+1)
		ask = append(append(ask, missing...), long)
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			rec.reset()
			within(t, "GetMany", func() error { return tx.GetMany(2, ask) })
			wantCount(t, rec, dbwire.KindGet, n+len(missing), "GetMany of keys that are twice in the list, missing, and too long")
			rec.reset()
			for i, k := range keys {
				if v, ok := get(t, tx, 2, string(k)); !ok || v != fmt.Sprintf("v%04d", i) {
					t.Fatalf("Get %s after GetMany: %q, %v", k, v, ok)
				}
			}
			for _, k := range missing {
				if v, ok := get(t, tx, 2, string(k)); ok {
					t.Errorf("Get %s after GetMany: %q, found", k, v)
				}
			}
			if v, ok := get(t, tx, 2, string(long)); ok {
				t.Errorf("Get of a key over the limit: %q, found", v)
			}
			wantCount(t, rec, dbwire.KindGet, 0, "the Gets after GetMany")
			return nil
		}))
	})

	t.Run("a second call asks only for what it has no answer for", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		keys := manyKeys(t, w, n)
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			rec.reset()
			must(t, tx.GetMany(2, keys[:300]))
			must(t, tx.GetMany(2, keys[150:450]))
			get(t, tx, 2, string(keys[10])) // taken from the first call
			wantCount(t, rec, dbwire.KindGet, 450, "two overlapping calls")
			if _, ok := get(t, tx, 2, "k0001-none"); ok {
				t.Error("a key that was not asked is found")
			}
			wantCount(t, rec, dbwire.KindGet, 451, "a Get of a key that GetMany did not ask")
			must(t, tx.GetMany(6, keys[:10])) // another table
			wantCount(t, rec, dbwire.KindGet, 461, "GetMany of another table")
			return nil
		}))
	})

	t.Run("a View takes its snapshot", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.GetMany(2, nil))
			return nil
		}))
		wantRequests(t, rec, dbwire.KindHello, dbwire.KindSnapshot, dbwire.KindRelease)
	})

	t.Run("in an Update the writes come first", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		keys := manyKeys(t, w, n)
		var later string
		commit(t, w, func(tx *dbclient.Tx) {
			put(t, tx, 2, string(keys[0]), "own")
			del(t, tx, 2, string(keys[1]))
			put(t, tx, 2, "new-key", "x")
			rec.reset()
			must(t, tx.GetMany(2, [][]byte{keys[0], keys[1], []byte("new-key"), keys[2], keys[3], keys[4]}))
			wantCount(t, rec, dbwire.KindGet, 3, "GetMany of keys of which three were written")
			if v, ok := get(t, tx, 2, string(keys[0])); !ok || v != "own" {
				t.Errorf("a key that was put: %q, %v", v, ok)
			}
			if v, ok := get(t, tx, 2, string(keys[1])); ok {
				t.Errorf("a key that was deleted: %q, found", v)
			}
			if v, ok := get(t, tx, 2, "new-key"); !ok || v != "x" {
				t.Errorf("a key that was put and was not there: %q, %v", v, ok)
			}
			// What is written after the answers were kept is read first too.
			put(t, tx, 2, string(keys[2]), "later")
			del(t, tx, 2, string(keys[3]))
			later, _ = get(t, tx, 2, string(keys[2]))
			if _, ok := get(t, tx, 2, string(keys[3])); ok {
				t.Error("a key that was deleted after GetMany is found")
			}
			if v, ok := get(t, tx, 2, string(keys[4])); !ok || v != "v0004" {
				t.Errorf("a key that was not written: %q, %v", v, ok)
			}
			wantCount(t, rec, dbwire.KindGet, 3, "the Gets that follow")
		})
		if later != "later" {
			t.Errorf("a key that was put after GetMany: %q", later)
		}
		wantRows(t, rows(t, w, 2, "k000")[:5], "k0000=own", "k0002=later", "k0004=v0004", "k0005=v0005", "k0006=v0006")
	})
}

func TestPrefetchObjects(t *testing.T) {
	f := newFixture(t, nil)
	w, rec := f.writer(dbclient.Options{})
	hashes, datas := manyObjects(t, w, 300)
	unknown := make([][32]byte, 5)
	for i := range unknown {
		unknown[i] = sha256.Sum256(fmt.Appendf(nil, "no object %d", i))
	}
	ask := append(append(slices.Clone(hashes), hashes[:20]...), unknown...)
	must(t, w.View(bg, func(tx *dbclient.Tx) error {
		rec.reset()
		must(t, tx.PrefetchObjects(ask))
		wantCount(t, rec, dbwire.KindObject, len(hashes)+len(unknown), "PrefetchObjects with objects twice in the list and unknown ones")
		must(t, tx.PrefetchObjects(hashes[:50]))
		wantCount(t, rec, dbwire.KindObject, len(hashes)+len(unknown), "PrefetchObjects of objects that are kept")
		rec.reset()
		for i, h := range hashes {
			data, found, err := tx.Object(h)
			if err != nil || !found || !bytes.Equal(data, datas[i]) {
				t.Fatalf("Object %d after PrefetchObjects: %d bytes, %v, %v", i, len(data), found, err)
			}
		}
		wantCount(t, rec, dbwire.KindObject, 0, "the Objects after PrefetchObjects")
		// An object is returned once: the caller may change what it gets, and the next call asks again.
		data, found, err := tx.Object(hashes[1])
		if err != nil || !found || !bytes.Equal(data, datas[1]) {
			t.Fatalf("a second Object: %d bytes, %v, %v", len(data), found, err)
		}
		wantCount(t, rec, dbwire.KindObject, 1, "an Object after it was taken")
		data[0] ^= 0xff
		if again, _, _ := tx.Object(hashes[1]); !bytes.Equal(again, datas[1]) {
			t.Error("an Object that the caller changed is changed for the next")
		}
		// A missing object is not kept.
		for range 2 {
			if data, found, err := tx.Object(unknown[0]); err != nil || found || len(data) != 0 {
				t.Errorf("an unknown object: %d bytes, %v, %v", len(data), found, err)
			}
		}
		wantCount(t, rec, dbwire.KindObject, 4, "two Objects of an unknown hash")
		return nil
	}))
}

func TestPrefetchScans(t *testing.T) {
	f := newFixture(t, nil)
	w, rec := f.writer(dbclient.Options{})
	commit(t, w, func(tx *dbclient.Tx) {
		for i := range 5 {
			put(t, tx, 2, fmt.Sprintf("a%d", i), fmt.Sprintf("va%d", i))
		}
		for i := range 3 {
			put(t, tx, 2, fmt.Sprintf("b%d", i), fmt.Sprintf("vb%d", i))
		}
		for i := range 200 {
			put(t, tx, 2, fmt.Sprintf("m%03d", i), fmt.Sprintf("%060d", i))
		}
	})
	prefixes := [][]byte{[]byte("a"), []byte("b"), []byte("m"), []byte("z"), []byte(""), []byte("a"), bytes.Repeat([]byte("L"), dbwire.MaxKey+1)}
	plain := map[string][]string{}
	pages := map[string]int{}
	must(t, w.View(bg, func(tx *dbclient.Tx) error {
		for _, p := range prefixes[:5] {
			plain[string(p)], pages[string(p)] = scanRows(t, tx, rec, string(p))
		}
		return nil
	}))
	if pages["m"] < 3 || pages["a"] != 1 || pages["z"] != 1 {
		t.Fatalf("the pages of a Scan are %v: the test needs a prefix of several pages", pages)
	}

	t.Run("a Scan starts with the page that was kept", func(t *testing.T) {
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			rec.reset()
			within(t, "PrefetchScans", func() error { return tx.PrefetchScans(2, prefixes) })
			wantCount(t, rec, dbwire.KindScan, 5, "PrefetchScans of five prefixes, one twice and one that is too long")
			must(t, tx.PrefetchScans(2, prefixes[:3]))
			wantCount(t, rec, dbwire.KindScan, 5, "PrefetchScans of prefixes that are kept")
			for _, p := range []string{"a", "b", "z"} {
				if got, requests := scanRows(t, tx, rec, p); !slices.Equal(got, plain[p]) || requests != 0 {
					t.Errorf("Scan %q after PrefetchScans: %d rows (want %d) and %d requests", p, len(got), len(plain[p]), requests)
				}
			}
			// The pages after the first are asked for, as often as the Scan is made: the first page stays.
			for range 2 {
				if got, requests := scanRows(t, tx, rec, "m"); !slices.Equal(got, plain["m"]) || requests != pages["m"]-1 {
					t.Errorf("Scan m after PrefetchScans: %d rows (want %d) and %d requests (want %d)", len(got), len(plain["m"]), requests,
						pages["m"]-1)
				}
			}
			if got, _ := scanRows(t, tx, rec, ""); !slices.Equal(got, plain[""]) {
				t.Errorf("Scan of everything after PrefetchScans: %d rows, want %d", len(got), len(plain[""]))
			}
			// A prefix that was not prefetched is asked for.
			if got, requests := scanRows(t, tx, rec, "m00"); len(got) != 10 || requests == 0 {
				t.Errorf("Scan m00: %d rows and %d requests", len(got), requests)
			}
			// A Scan that stops early leaves the page for the next.
			n := 0
			must(t, tx.Scan(2, []byte("a"), func(_, _ []byte) bool { n++; return n < 2 }))
			if got, requests := scanRows(t, tx, rec, "a"); !slices.Equal(got, plain["a"]) || requests != 0 {
				t.Errorf("Scan a after a Scan that stopped: %d rows and %d requests", len(got), requests)
			}
			// The key that fn gets is its own, as it is without a prefetch: changing it does not change the page.
			must(t, tx.Scan(2, []byte("a"), func(k, _ []byte) bool {
				for i := range k {
					k[i] = 'X'
				}
				return true
			}))
			if got, requests := scanRows(t, tx, rec, "a"); !slices.Equal(got, plain["a"]) || requests != 0 {
				t.Errorf("Scan a after a Scan whose function changed its keys: %q and %d requests", got, requests)
			}
			tx.DropPrefetched()
			if got, requests := scanRows(t, tx, rec, "a"); !slices.Equal(got, plain["a"]) || requests != 1 {
				t.Errorf("Scan a after DropPrefetched: %d rows and %d requests, want 1", len(got), requests)
			}
			return nil
		}))
	})

	t.Run("the writes of an Update that come after are merged", func(t *testing.T) {
		commit(t, w, func(tx *dbclient.Tx) {
			must(t, tx.PrefetchScans(2, [][]byte{[]byte("a"), []byte("m")}))
			put(t, tx, 2, "a2x", "own")
			del(t, tx, 2, "a1")
			put(t, tx, 2, "a0", "changed")
			put(t, tx, 2, "m0995", "own")
			del(t, tx, 2, "m002")
			rec.reset()
			got, _ := scanRows(t, tx, rec, "a")
			wantRows(t, got, "a0=changed", "a2=va2", "a2x=own", "a3=va3", "a4=va4")
			got, requests := scanRows(t, tx, rec, "m")
			if requests != pages["m"]-1 || len(got) != 200 {
				t.Errorf("Scan m: %d rows and %d requests", len(got), requests)
			}
			if !slices.Contains(got, "m0995=own") || slices.Contains(got, "m002="+fmt.Sprintf("%060d", 2)) {
				t.Error("Scan m does not have the writes of the Update")
			}
			// A write that comes between two Scans is in the second.
			put(t, tx, 2, "a1", "back")
			got, _ = scanRows(t, tx, rec, "a")
			wantRows(t, got, "a0=changed", "a1=back", "a2=va2", "a2x=own", "a3=va3", "a4=va4")
		})
	})
}

// TestWindowSize: a window is one Write call of at most dbwire.WindowRequests requests and dbwire.WindowBytes bytes, also
// where the frame limit of the daemon is smaller than a window, and the client writes nothing while it has not read the
// reply to every request that it has sent.
func TestWindowSize(t *testing.T) {
	run := func(t *testing.T, name string, keyLen int, want []int) {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, func(o *dbtest.Options) { o.Limits.Stall = 3 * time.Second })
			w, rec := f.writer(dbclient.Options{})
			keys := make([][]byte, 1000)
			commit(t, w, func(tx *dbclient.Tx) {
				for i := range keys {
					keys[i] = fmt.Appendf(nil, "%0*d", keyLen, i)
					put(t, tx, 2, string(keys[i]), fmt.Sprintf("v%d", i))
				}
			})
			rec.reset()
			done := make(chan error, 1)
			go func() {
				done <- w.View(bg, func(tx *dbclient.Tx) error {
					if err := tx.GetMany(2, keys); err != nil {
						return err
					}
					for i, k := range keys {
						if v, ok := get(t, tx, 2, string(k)); !ok || v != fmt.Sprintf("v%d", i) {
							t.Errorf("Get %d after GetMany: %q, %v", i, v, ok)
						}
					}
					return nil
				})
			}()
			select {
			case err := <-done:
				must(t, err)
			case <-time.After(waitLimit):
				t.Fatal("GetMany does not end: a window may have been written before the replies to the one before were read")
			}
			var got []int
			for _, wr := range rec.windows(dbwire.KindGet) {
				got = append(got, len(wr.kinds))
				if wr.bytes > dbwire.WindowBytes || len(wr.kinds) > dbwire.WindowRequests {
					t.Errorf("a window of %d requests and %d bytes: at most %d and %d", len(wr.kinds), wr.bytes, dbwire.WindowRequests,
						dbwire.WindowBytes)
				}
				if want := len(wr.kinds) * (dbwire.HeaderSize + 8 + keyLen); wr.bytes != want {
					t.Errorf("a window of %d requests has %d bytes, want %d: it is not one Write call", len(wr.kinds), wr.bytes, want)
				}
			}
			if !slices.Equal(got, want) {
				t.Errorf("the windows have %v requests, want %v", got, want)
			}
			for i, wr := range rec.writeCalls() {
				if wr.outstanding != 0 {
					t.Errorf("the Write call %d (%d bytes, %v) was made while %d requests were without their replies", i, wr.bytes, wr.kinds,
						wr.outstanding)
				}
			}
		})
	}
	// 216 bytes a request: 151 in the bytes of a window.
	run(t, "bounded by the bytes", 200, []int{151, 151, 151, 151, 151, 151, 94})
	run(t, "bounded by the requests", 8, []int{256, 256, 256, 232})
	// 256 bytes a request: 128 make exactly the bytes of a window.
	run(t, "exactly the bytes of a window", 240, []int{128, 128, 128, 128, 128, 128, 128, 104})

	t.Run("a request that fills a frame is a window of its own", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		big := bytes.Repeat([]byte("B"), dbwire.MaxKey) // a body of 4104 bytes, more than the frame limit of 4096
		keys := [][]byte{[]byte("s1"), []byte("s2"), []byte("s3"), big, []byte("s4"), []byte("s5"), []byte("s6")}
		commit(t, w, func(tx *dbclient.Tx) {
			for _, k := range keys {
				put(t, tx, 2, string(k), "v-"+string(k[:2]))
			}
		})
		rec.reset()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.GetMany(2, keys))
			for _, k := range keys {
				if v, ok := get(t, tx, 2, string(k)); !ok || v != "v-"+string(k[:2]) {
					t.Errorf("Get of %d bytes after GetMany: %q, %v", len(k), v, ok)
				}
			}
			return nil
		}))
		var got []int
		for _, wr := range rec.windows(dbwire.KindGet) {
			got = append(got, len(wr.kinds))
		}
		if want := []int{3, 1, 3}; !slices.Equal(got, want) {
			t.Errorf("the windows have %v requests, want %v", got, want)
		}
	})
}

// TestWindowSequence: the client writes a window, then reads the replies to all of it, and only then writes the next.
func TestWindowSequence(t *testing.T) {
	const keys = dbwire.WindowRequests + 44
	script := func(p *peer) {
		p.hello()
		p.snapshot(1)
		for i := range dbwire.WindowRequests {
			if r := p.expect(dbwire.KindGet); string(r.key) != fmt.Sprintf("k%03d", i) {
				p.t.Errorf("the request %d is %v", i, r)
			}
		}
		p.silent(50 * time.Millisecond)
		for i := range dbwire.WindowRequests - 1 {
			p.getReply(true, fmt.Sprintf("v%03d", i))
		}
		p.silent(50 * time.Millisecond)
		p.getReply(true, fmt.Sprintf("v%03d", dbwire.WindowRequests-1))
		for i := dbwire.WindowRequests; i < keys; i++ {
			if r := p.expect(dbwire.KindGet); string(r.key) != fmt.Sprintf("k%03d", i) {
				p.t.Errorf("the request %d is %v", i, r)
			}
		}
		for i := dbwire.WindowRequests; i < keys; i++ {
			p.getReply(true, fmt.Sprintf("v%03d", i))
		}
		p.release()
	}
	c, rec, _ := openPeer(t, dbwire.RoleReader, script)
	ask := make([][]byte, keys)
	for i := range ask {
		ask[i] = fmt.Appendf(nil, "k%03d", i)
	}
	must(t, c.View(bg, func(tx *dbclient.Tx) error {
		must(t, tx.GetMany(2, ask))
		for i := range ask {
			if v, ok := get(t, tx, 2, string(ask[i])); !ok || v != fmt.Sprintf("v%03d", i) {
				t.Errorf("Get %d: %q, %v", i, v, ok)
			}
		}
		return nil
	}))
	for i, wr := range rec.writeCalls() {
		if wr.outstanding != 0 {
			t.Errorf("the Write call %d was made while %d requests were without their replies", i, wr.outstanding)
		}
	}
}

// TestWindowRefusals: the daemon's refusal of one request of a window is that request's alone.
func TestWindowRefusals(t *testing.T) {
	// halfReply starts an OK reply, so that its first frames are on the connection, and then refuses in its place.
	halfReply := func(p *peer, body func(e *dbwire.Enc)) {
		p.wr.SetMaxFrame(2)
		p.wr.Begin(dbwire.KindOK)
		e := &dbwire.Enc{W: p.wr}
		body(e)
		if e.Err != nil || p.wr.Fail(dbwire.CodeFailed, "the engine failed") != nil {
			p.stop()
		}
		p.wr.SetMaxFrame(1 << 20)
	}

	t.Run("Get", func(t *testing.T) {
		script := func(p *peer) {
			p.hello()
			p.snapshot(1)
			for range 6 {
				p.expect(dbwire.KindGet)
			}
			p.getReply(true, "v1")
			p.refuse(dbwire.CodeFailed, "the engine failed")
			halfReply(p, func(e *dbwire.Enc) { e.Bool(true); e.U32(100) })
			p.getReply(false, "")
			p.getReply(true, "v5")
			p.refuse(dbwire.CodeNoSnapshot, "no snapshot is held")
			// The refused keys are asked again, one at a time, and what the daemon says then is what the reads give.
			if r := p.expect(dbwire.KindGet); string(r.key) != "k2" {
				p.t.Errorf("the request after the window is %v, want k2", r)
			}
			p.getReply(true, "again")
			if r := p.expect(dbwire.KindGet); string(r.key) != "k3" {
				p.t.Errorf("the request is %v, want k3", r)
			}
			p.refuse(dbwire.CodeFailed, "still failing")
			if r := p.expect(dbwire.KindGet); string(r.key) != "k6" {
				p.t.Errorf("the request is %v, want k6", r)
			}
			p.refuse(dbwire.CodeNoSnapshot, "no snapshot is held")
			p.release()
		}
		c, rec, pd := openPeer(t, dbwire.RoleReader, script)
		keys := [][]byte{[]byte("k1"), []byte("k2"), []byte("k3"), []byte("k4"), []byte("k5"), []byte("k6")}
		must(t, c.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.GetMany(2, keys))
			if v, ok := get(t, tx, 2, "k1"); !ok || v != "v1" {
				t.Errorf("k1: %q, %v", v, ok)
			}
			if v, ok := get(t, tx, 2, "k4"); ok {
				t.Errorf("k4: %q, found", v)
			}
			if v, ok := get(t, tx, 2, "k5"); !ok || v != "v5" {
				t.Errorf("k5: %q, %v", v, ok)
			}
			if v, ok := get(t, tx, 2, "k2"); !ok || v != "again" {
				t.Errorf("k2, which was refused: %q, %v", v, ok)
			}
			_, _, err := tx.Get(2, []byte("k3"))
			if re := remote(t, err); re.Code != dbwire.CodeFailed || re.Msg != "still failing" {
				t.Errorf("k3: %v", err)
			}
			_, _, err = tx.Get(2, []byte("k6"))
			if re := remote(t, err); re.Code != dbwire.CodeNoSnapshot {
				t.Errorf("k6: %v", err)
			}
			return nil
		}))
		if got := pd.dialed(); got != 1 {
			t.Errorf("%d connections: a refusal does not end the connection", got)
		}
		wantCount(t, rec, dbwire.KindGet, 9, "the Gets")
	})

	t.Run("Object", func(t *testing.T) {
		hashes := [][32]byte{sha256.Sum256([]byte("1")), sha256.Sum256([]byte("2")), sha256.Sum256([]byte("3"))}
		script := func(p *peer) {
			p.hello()
			p.snapshot(1)
			for range 3 {
				p.expect(dbwire.KindObject)
			}
			p.objectReply(true, "d1")
			halfReply(p, func(e *dbwire.Enc) { e.Bool(true); e.U64(100) })
			p.objectReply(true, "d3")
			for _, want := range []struct {
				hash [32]byte
				data string
			}{{hashes[1], "d2"}, {hashes[0], "d1 again"}} {
				if r := p.expect(dbwire.KindObject); r.hash != want.hash {
					p.t.Errorf("the request is %v, want %x", r, want.hash[:4])
				}
				p.objectReply(true, want.data)
			}
			p.release()
		}
		c, _, pd := openPeer(t, dbwire.RoleReader, script)
		must(t, c.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.PrefetchObjects(hashes))
			for _, want := range []struct {
				hash int
				data string
			}{{0, "d1"}, {2, "d3"}, {1, "d2"}, {0, "d1 again"}} {
				if data, found, err := tx.Object(hashes[want.hash]); err != nil || !found || string(data) != want.data {
					t.Errorf("Object %d: %q, %v, %v, want %q", want.hash+1, data, found, err, want.data)
				}
			}
			return nil
		}))
		if got := pd.dialed(); got != 1 {
			t.Errorf("%d connections: a refusal does not end the connection", got)
		}
	})

	t.Run("Scan", func(t *testing.T) {
		script := func(p *peer) {
			p.hello()
			p.snapshot(1)
			for range 3 {
				p.expect(dbwire.KindScan)
			}
			p.pageReply(true, "a1", "1", "a2", "2")
			halfReply(p, func(e *dbwire.Enc) { e.ScanRow([]byte("b1"), []byte("1")) })
			p.pageReply(false, "c1", "1")
			// The page of a that was kept is followed by the next, which asks after its last key.
			if r := p.expect(dbwire.KindScan); string(r.key) != "a" || !r.more || string(r.after) != "a2" {
				p.t.Errorf("the request is %v, want the page of a after a2", r)
			}
			p.pageReply(false, "a3", "3")
			if r := p.expect(dbwire.KindScan); string(r.key) != "b" || r.more {
				p.t.Errorf("the request is %v, want the first page of b", r)
			}
			p.pageReply(false, "b1", "one", "b2", "two")
			p.release()
		}
		c, _, pd := openPeer(t, dbwire.RoleReader, script)
		must(t, c.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.PrefetchScans(2, [][]byte{[]byte("a"), []byte("b"), []byte("c")}))
			for _, want := range []struct {
				prefix string
				rows   []string
			}{{"a", []string{"a1=1", "a2=2", "a3=3"}}, {"c", []string{"c1=1"}}, {"b", []string{"b1=one", "b2=two"}}} {
				var got []string
				must(t, tx.Scan(2, []byte(want.prefix), func(k, v []byte) bool {
					got = append(got, string(k)+"="+string(v))
					return len(got) < 100 // a Scan that does not end fails the test, and does not fill the memory
				}))
				if !slices.Equal(got, want.rows) {
					t.Errorf("Scan %q: %q, want %q", want.prefix, got, want.rows)
				}
			}
			return nil
		}))
		if got := pd.dialed(); got != 1 {
			t.Errorf("%d connections: a refusal does not end the connection", got)
		}
	})
}

// TestWindowConnectionEnds: a connection that ends after a part of the replies is an error of the call, and is not used
// again.
func TestWindowConnectionEnds(t *testing.T) {
	script := func(p *peer) {
		p.hello()
		p.snapshot(uint64(p.n + 1))
		if p.n == 0 {
			for range 10 {
				p.expect(dbwire.KindGet)
			}
			for range 4 {
				p.getReply(true, "v")
			}
			_ = p.nc.Close()
			return
		}
		p.expect(dbwire.KindGet)
		p.getReply(true, "second")
		p.release()
	}
	c, rec, pd := openPeer(t, dbwire.RoleReader, script)
	keys := make([][]byte, 10)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "k%d", i)
	}
	err := c.View(bg, func(tx *dbclient.Tx) error {
		err := tx.GetMany(2, keys)
		if !errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("GetMany: %v, want an error that says that the daemon cannot be reached", err)
		}
		// As after a Get that failed: what the transaction has is still there, and the connection is not.
		if _, _, err := tx.Get(2, []byte("k9")); !errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("Get after the end of the connection: %v", err)
		}
		return err
	})
	if !errors.Is(err, dbclient.ErrUnavailable) {
		t.Errorf("View: %v", err)
	}
	if got := rec.liveConns(); got != 0 {
		t.Errorf("%d connections are open", got)
	}
	// The connection is not in the pool: the next View dials, and works.
	must(t, c.View(bg, func(tx *dbclient.Tx) error {
		if v, ok := get(t, tx, 2, "x"); !ok || v != "second" {
			t.Errorf("Get on the next View: %q, %v", v, ok)
		}
		return nil
	}))
	if got := pd.dialed(); got != 2 {
		t.Errorf("%d connections, want 2", got)
	}
}

// TestWindowMalformed: a reply that is not well formed is an error of the protocol, and ends the connection.
func TestWindowMalformed(t *testing.T) {
	for _, c := range []struct {
		name string
		bad  func(p *peer)
	}{
		{"a boolean of 2", func(p *peer) { p.reply(func(e *dbwire.Enc) { e.U8(2) }) }},
		{"a frame of an unknown kind", func(p *peer) { p.raw([]byte{0x7f, 1, 0, 0, 0, 0, 0, 0}) }},
		{"more bytes than the reply says", func(p *peer) { p.reply(func(e *dbwire.Enc) { e.GetReply(true, []byte("x")); e.U8(9) }) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			script := func(p *peer) {
				p.hello()
				p.snapshot(1)
				if p.n > 0 {
					p.expect(dbwire.KindGet)
					p.getReply(true, "next")
					p.release()
					return
				}
				for range 3 {
					p.expect(dbwire.KindGet)
				}
				p.getReply(true, "v")
				c.bad(p)
				select { // the client ends the connection
				case r, ok := <-p.reqs:
					if ok {
						p.t.Errorf("the client sent %v after a reply that is not well formed", r)
					}
				case <-time.After(waitLimit):
					p.t.Error("the client did not end the connection")
				}
			}
			cl, rec, pd := openPeer(t, dbwire.RoleReader, script)
			err := cl.View(bg, func(tx *dbclient.Tx) error {
				return tx.GetMany(2, [][]byte{[]byte("a"), []byte("b"), []byte("c")})
			})
			if !errors.Is(err, dbwire.ErrProtocol) || errors.Is(err, dbclient.ErrUnavailable) {
				t.Errorf("GetMany: %v, want an error of the protocol", err)
			}
			if got := rec.liveConns(); got != 0 {
				t.Errorf("%d connections are open", got)
			}
			must(t, cl.View(bg, func(tx *dbclient.Tx) error {
				if v, ok := get(t, tx, 2, "x"); !ok || v != "next" {
					t.Errorf("Get on the next View: %q, %v", v, ok)
				}
				return nil
			}))
			if got := pd.dialed(); got != 2 {
				t.Errorf("%d connections, want 2", got)
			}
		})
	}
}

// TestWindowContext: a context that ends in the middle of a window ends the call with its error, promptly.
func TestWindowContext(t *testing.T) {
	t.Run("in a View, while the daemon is silent", func(t *testing.T) {
		started := make(chan struct{})
		script := func(p *peer) {
			p.hello()
			p.snapshot(1)
			for range 5 {
				p.expect(dbwire.KindGet)
			}
			close(started)
			select { // no reply: the client ends the connection
			case r, ok := <-p.reqs:
				if ok {
					p.t.Errorf("the client sent %v", r)
				}
			case <-time.After(waitLimit):
				p.t.Error("the client did not end the connection")
			}
		}
		c, rec, _ := openPeer(t, dbwire.RoleReader, script)
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		keys := [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d"), []byte("e")}
		done := make(chan error, 1)
		go func() {
			done <- c.View(ctx, func(tx *dbclient.Tx) error {
				err := tx.GetMany(2, keys)
				if !errors.Is(err, context.Canceled) {
					t.Errorf("GetMany: %v, want the error of the context", err)
				}
				return err
			})
		}()
		select {
		case <-started:
		case <-time.After(waitLimit):
			t.Fatal("the window did not arrive")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("View: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the call did not return after its context ended")
		}
		if got := rec.liveConns(); got != 0 {
			t.Errorf("%d connections are open", got)
		}
	})

	t.Run("in an Update, between two windows", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		keys := manyKeys(t, w, 700)
		before := f.state()
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		fired := false
		rec.after = func(_ *recConn, kind dbwire.Kind, _ int, _ bool) {
			if kind == dbwire.KindGet && !fired && rec.count(dbwire.KindGet) > dbwire.WindowRequests {
				fired = true
				cancel()
				time.Sleep(50 * time.Millisecond) // the connection has its deadline by the time it reads
			}
		}
		rec.reset()
		var inner error
		err := w.Update(ctx, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k0000", "own")
			event(t, tx, `{"a":1}`)
			inner = tx.GetMany(2, keys)
			return inner
		})
		rec.after = nil
		if !fired {
			t.Fatal("the context did not end")
		}
		if !errors.Is(inner, context.Canceled) || !errors.Is(err, context.Canceled) {
			t.Errorf("GetMany: %v, Update: %v, want the error of the context", inner, err)
		}
		if after := f.state(); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Error("the Update that was cut committed something")
		}
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k0000", "next") })
		wantRows(t, rows(t, w, 2, "k0000"), "k0000=next")
	})
}

// TestWindowIdleSnapshot: a snapshot that the daemon has released refuses every request of a window, and nothing is kept.
func TestWindowIdleSnapshot(t *testing.T) {
	f := newFixture(t, func(o *dbtest.Options) { o.Limits.SnapshotIdle = 100 * time.Millisecond })
	w, rec := f.writer(dbclient.Options{})
	keys := manyKeys(t, w, 20)
	must(t, w.View(bg, func(tx *dbclient.Tx) error {
		get(t, tx, 2, "first") // takes the snapshot
		time.Sleep(600 * time.Millisecond)
		rec.reset()
		if err := tx.GetMany(2, keys); err != nil {
			t.Errorf("GetMany: %v", err)
		}
		wantCount(t, rec, dbwire.KindGet, len(keys), "GetMany after the snapshot was released")
		for _, k := range keys[:3] {
			_, _, err := tx.Get(2, k)
			if re := remote(t, err); re.Code != dbwire.CodeNoSnapshot {
				t.Errorf("Get of %s: %v, want the refusal of the daemon", k, err)
			}
		}
		wantCount(t, rec, dbwire.KindGet, len(keys)+3, "the Gets after it")
		return nil
	}))
}

// TestWindowInUpdate: the calls of a window in an Update read what the Update has written, and the Commit is that of the
// same Update without them.
func TestWindowInUpdate(t *testing.T) {
	small, large := []byte("a small object"), bytes.Repeat([]byte("a large one "), 100)
	hashes := [][32]byte{sha256.Sum256(small), sha256.Sum256(large)}

	t.Run("an object that the Update has put is found", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		p.rec.reset()
		log := p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			must(t, tx.PrefetchObjects(hashes)) // not there yet
			must(t, tx.PutObject(hashes[0], small))
			must(t, tx.PutObject(hashes[1], large))
			must(t, tx.PrefetchObjects(hashes))
			for _, h := range hashes {
				data, found, err := tx.Object(h)
				log("Object %x: %d bytes, %v, %v", h[:3], len(data), found, err)
			}
			data, found, err := tx.Object(hashes[0])
			log("Object again: %d bytes, %v, %v", len(data), found, err)
			return nil
		}))
		if len(log) != 3 || log[2] != "Object again: 14 bytes, true, <nil>" {
			t.Errorf("the log is %q", log)
		}
		// Two asks that found nothing and then something, the two Objects that were kept, and the one that asked.
		wantCount(t, p.rec, dbwire.KindObject, 2+2+1, "the Objects")
		wantCount(t, p.rec, dbwire.KindCommit, 1, "the Commits")
	})

	t.Run("GetMany after a Put and a Delete", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		p.rec.reset()
		p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			must(t, putS(tx, 2, "x", "own"))
			must(t, delS(tx, 2, "a"))
			must(t, tx.GetMany(2, [][]byte{[]byte("x"), []byte("a"), []byte("b"), []byte("zz")}))
			for _, k := range []string{"x", "a", "b", "zz"} {
				log(fmtGet(tx, 2, k))
			}
			must(t, putS(tx, 2, "b", "later"))
			must(t, delS(tx, 2, "c"))
			log(fmtGet(tx, 2, "b"))
			log(fmtGet(tx, 2, "c"))
			return nil
		}))
		// The Delete of a asked for it, then GetMany asked for b and zz and the Delete of c for c.
		wantCount(t, p.rec, dbwire.KindGet, 4, "the Gets")
	})

	t.Run("PrefetchScans and writes after it", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		p.rec.reset()
		log := p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			must(t, tx.PrefetchScans(2, [][]byte{nil, []byte("b")}))
			must(t, putS(tx, 2, "bb", "own"))
			must(t, delS(tx, 2, "b"))
			must(t, putS(tx, 2, "a", "changed"))
			log(fmtScan(tx, 2, ""))
			log(fmtScan(tx, 2, "b"))
			tx.DropPrefetched()
			log(fmtScan(tx, 2, ""))
			return nil
		}))
		if want := `Scan 2 "b": "bb"=`; len(log) != 3 || !bytes.Contains([]byte(log[1]), []byte(want)) || bytes.Contains([]byte(log[1]), []byte(`"b"=`)) {
			t.Errorf("the log is %q", log)
		}
		if log[0] != log[2] {
			t.Errorf("a Scan after DropPrefetched gives other rows:\n %s\n %s", log[0], log[2])
		}
		// The two pages that were kept, and the Scan after DropPrefetched.
		wantCount(t, p.rec, dbwire.KindScan, 3, "the Scan requests")
	})

	t.Run("the Commit", func(t *testing.T) {
		p := newPair(t, nil, seedBase)
		seq := p.f.seq()
		p.rec.reset()
		p.both(t, script(true, func(tx txn, log func(string, ...any)) error {
			must(t, tx.GetMany(2, [][]byte{[]byte("a"), []byte("b")}))
			must(t, tx.PrefetchScans(2, [][]byte{nil}))
			must(t, putS(tx, 2, "n", "1"))
			must(t, tx.PrefetchObjects(hashes))
			must(t, tx.PutObject(hashes[0], small))
			return nil
		}))
		if got := p.f.seq(); got != seq+1 {
			t.Errorf("the store is at %d, want %d", got, seq+1)
		}
		wantCount(t, p.rec, dbwire.KindCommit, 1, "the Commits")
	})
}

// TestWindowOneSnapshot: the windows of a View are answered from its one snapshot, whatever is committed between them.
func TestWindowOneSnapshot(t *testing.T) {
	f := newFixture(t, nil)
	w, _ := f.writer(dbclient.Options{})
	r, rrec := f.open(dbwire.RoleReader, dbclient.Options{})
	keys := manyKeys(t, w, 700)
	committed := false
	rrec.after = func(_ *recConn, kind dbwire.Kind, _ int, _ bool) {
		if kind == dbwire.KindGet && !committed {
			committed = true // after the first window was written
			commit(t, w, func(tx *dbclient.Tx) {
				for i, k := range keys {
					put(t, tx, 2, string(k), fmt.Sprintf("new%04d", i))
				}
			})
		}
	}
	must(t, r.View(bg, func(tx *dbclient.Tx) error {
		before := tx.Seq()
		must(t, tx.GetMany(2, keys))
		rrec.after = nil
		if !committed {
			t.Fatal("nothing was committed between the windows")
		}
		if f.seq() == before {
			t.Error("the store is still at the snapshot of the View")
		}
		for i, k := range keys {
			if v, ok := get(t, tx, 2, string(k)); !ok || v != fmt.Sprintf("v%04d", i) {
				t.Fatalf("Get %s: %q, %v, want the value of the snapshot", k, v, ok)
			}
		}
		return nil
	}))
	if got := rrec.windows(dbwire.KindGet); len(got) != 3 {
		t.Errorf("%d windows, want 3", len(got))
	}
	wantRows(t, rows(t, r, 2, "k0000"), "k0000=new0000")
}

// TestPrefetchBudget: a transaction keeps at most what prefetchBudget says of objects and pages.
func TestPrefetchBudget(t *testing.T) {
	const budget = 64 << 10
	t.Run("objects", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		hashes, datas := make([][32]byte, 300), make([][]byte, 300)
		commit(t, w, func(tx *dbclient.Tx) {
			for i := range hashes {
				datas[i] = bytes.Repeat(fmt.Appendf(nil, "%04d", i), 256) // 1 KiB
				hashes[i] = sha256.Sum256(datas[i])
				must(t, tx.PutObject(hashes[i], datas[i]))
			}
		})
		defer dbclient.SetPrefetchBudget(budget)()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			rec.reset()
			must(t, tx.PrefetchObjects(hashes))
			// The first window brings 256 KiB: 64 objects are kept, and no window follows it.
			wantCount(t, rec, dbwire.KindObject, dbwire.WindowRequests, "PrefetchObjects of 300 objects of 1 KiB")
			if got := len(rec.windows(dbwire.KindObject)); got != 1 {
				t.Errorf("%d windows, want 1", got)
			}
			rec.reset()
			for i, h := range hashes {
				if data, found, err := tx.Object(h); err != nil || !found || !bytes.Equal(data, datas[i]) {
					t.Fatalf("Object %d: %d bytes, %v, %v", i, len(data), found, err)
				}
			}
			wantCount(t, rec, dbwire.KindObject, len(hashes)-64, "the Objects: those that were kept need no request")
			// What was taken is given back: the next prefetch keeps again.
			rec.reset()
			must(t, tx.PrefetchObjects(hashes[64:128]))
			wantCount(t, rec, dbwire.KindObject, 64, "a second PrefetchObjects")
			rec.reset()
			for i := 64; i < 128; i++ {
				if data, _, _ := tx.Object(hashes[i]); !bytes.Equal(data, datas[i]) {
					t.Fatalf("Object %d after the second PrefetchObjects: %d bytes", i, len(data))
				}
			}
			wantCount(t, rec, dbwire.KindObject, 0, "the Objects after the second PrefetchObjects")
			// A full budget sends nothing, and DropPrefetched gives it back.
			must(t, tx.PrefetchObjects(hashes[:64]))
			rec.reset()
			must(t, tx.PrefetchObjects(hashes[64:128]))
			wantCount(t, rec, dbwire.KindObject, 0, "PrefetchObjects with the budget full")
			tx.DropPrefetched()
			must(t, tx.PrefetchObjects(hashes[64:128]))
			wantCount(t, rec, dbwire.KindObject, 64, "PrefetchObjects after DropPrefetched")
			rec.reset()
			for i := 64; i < 128; i++ {
				if data, _, _ := tx.Object(hashes[i]); !bytes.Equal(data, datas[i]) {
					t.Fatalf("Object %d: %d bytes", i, len(data))
				}
			}
			wantCount(t, rec, dbwire.KindObject, 0, "the Objects after DropPrefetched and a prefetch")
			return nil
		}))
	})

	t.Run("objects of 4 KiB in one window", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		hashes, datas := make([][32]byte, 100), make([][]byte, 100)
		commit(t, w, func(tx *dbclient.Tx) {
			for i := range hashes {
				datas[i] = bytes.Repeat(fmt.Appendf(nil, "%04d", i), 1024)
				hashes[i] = sha256.Sum256(datas[i])
				must(t, tx.PutObject(hashes[i], datas[i]))
			}
		})
		defer dbclient.SetPrefetchBudget(budget)()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.PrefetchObjects(hashes))
			rec.reset()
			for i, h := range hashes {
				if data, found, err := tx.Object(h); err != nil || !found || !bytes.Equal(data, datas[i]) {
					t.Fatalf("Object %d: %d bytes, %v, %v", i, len(data), found, err)
				}
			}
			wantCount(t, rec, dbwire.KindObject, 100-16, "the Objects: 16 of 4 KiB are kept")
			return nil
		}))
	})

	t.Run("an object larger than the budget", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		small, big := bytes.Repeat([]byte("s"), 1000), bytes.Repeat([]byte("B"), budget+1)
		hs, hb := sha256.Sum256(small), sha256.Sum256(big)
		commit(t, w, func(tx *dbclient.Tx) {
			must(t, tx.PutObject(hs, small))
			must(t, tx.PutObject(hb, big))
		})
		defer dbclient.SetPrefetchBudget(budget)()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.PrefetchObjects([][32]byte{hb, hs}))
			rec.reset()
			for _, c := range []struct {
				hash [32]byte
				data []byte
			}{{hs, small}, {hb, big}, {hb, big}} {
				data, found, err := tx.Object(c.hash)
				if err != nil || !found || !bytes.Equal(data, c.data) {
					t.Fatalf("Object of %d bytes: %d bytes, %v, %v", len(c.data), len(data), found, err)
				}
			}
			// The small one was kept, and the large one is asked for each time.
			wantCount(t, rec, dbwire.KindObject, 2, "the Objects")
			return nil
		}))
	})

	t.Run("pages", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		prefixes := make([][]byte, 300)
		commit(t, w, func(tx *dbclient.Tx) {
			for i := range prefixes {
				prefixes[i] = fmt.Appendf(nil, "s%03d", i)
				put(t, tx, 2, string(prefixes[i])+"/1", fmt.Sprintf("%0100d", i)) // a page of 6 + 100 bytes
			}
		})
		defer dbclient.SetPrefetchBudget(5000)()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			rec.reset()
			must(t, tx.PrefetchScans(2, prefixes))
			// The first window brings 256 pages of 106 bytes: 47 are kept, and no window follows it.
			wantCount(t, rec, dbwire.KindScan, dbwire.WindowRequests, "PrefetchScans of 300 prefixes")
			rec.reset()
			for i, p := range prefixes {
				if got, _ := scanRows(t, tx, rec, string(p)); !slices.Equal(got, []string{string(p) + "/1=" + fmt.Sprintf("%0100d", i)}) {
					t.Fatalf("Scan %s: %q", p, got)
				}
			}
			wantCount(t, rec, dbwire.KindScan, len(prefixes)-47, "the Scans: those that were kept need no request")
			// A page that is taken from the kept ones stays kept: its bytes are given back by DropPrefetched.
			tx.DropPrefetched()
			rec.reset()
			must(t, tx.PrefetchScans(2, prefixes[:40]))
			for _, p := range prefixes[:40] {
				scanRows(t, tx, rec, string(p))
			}
			wantCount(t, rec, dbwire.KindScan, 40, "40 pages after DropPrefetched: all kept")
			return nil
		}))
	})

	t.Run("a page larger than the budget", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		commit(t, w, func(tx *dbclient.Tx) {
			put(t, tx, 2, "big1", strings.Repeat("x", 200))
			put(t, tx, 2, "big2", strings.Repeat("y", 200))
		})
		defer dbclient.SetPrefetchBudget(300)()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			must(t, tx.PrefetchScans(2, [][]byte{[]byte("big")}))
			rec.reset()
			got, requests := scanRows(t, tx, rec, "big")
			if len(got) != 2 || requests != 1 {
				t.Errorf("Scan of a page that is over the budget: %d rows and %d requests, want 2 and 1", len(got), requests)
			}
			return nil
		}))
	})
}
