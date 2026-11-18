package dbserver

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

func TestSnapshot(t *testing.T) {
	t.Run("isolation", func(t *testing.T) {
		f := newFixture(t, nil)
		w := f.connect(dbwire.RoleWriter)
		w.mustCommit(put(2, "a", "1"), put(2, "gone", "x"))
		r := f.connect(dbwire.RoleReader)
		if seq := r.mustSnapshot(); seq != 1 {
			t.Fatalf("Snapshot is at %d, want 1", seq)
		}
		w.mustCommit(put(2, "a", "2"), put(2, "b", "3"), del(2, "gone"))

		if found, v := r.mustGet(2, "a"); !found || v != "1" {
			t.Errorf("a is %v %q in the snapshot, want 1", found, v)
		}
		if found, _ := r.mustGet(2, "b"); found {
			t.Error("b, put after the snapshot, is in it")
		}
		if found, _ := r.mustGet(2, "gone"); !found {
			t.Error("gone, deleted after the snapshot, is not in it")
		}
		rows, _ := r.scanAll(2, "", 0)
		if want := []kv{{"a", "1"}, {"gone", "x"}}; !reflect.DeepEqual(rows, want) {
			t.Errorf("the snapshot scans %v, want %v", rows, want)
		}
		if events, err := r.events(); err != nil || len(events) != 2 {
			t.Errorf("the events of the snapshot: %d, %v", len(events), err)
		}

		// A new Snapshot replaces it.
		if seq := r.mustSnapshot(); seq != 2 {
			t.Fatalf("the second Snapshot is at %d, want 2", seq)
		}
		if found, v := r.mustGet(2, "a"); !found || v != "2" {
			t.Errorf("a is %v %q in the new snapshot, want 2", found, v)
		}
		if found, _ := r.mustGet(2, "gone"); found {
			t.Error("gone is in the new snapshot")
		}
	})

	t.Run("reads need one", func(t *testing.T) {
		f := newFixture(t, nil)
		f.update(func(tx *logdb.Tx) { _ = tx.Put(2, []byte("k"), []byte("v")) })
		for _, role := range []dbwire.Role{dbwire.RoleReader, dbwire.RoleWriter} {
			c := f.connect(role)
			check := func() {
				t.Helper()
				_, _, err := c.get(2, "k")
				wantCode(t, err, dbwire.CodeNoSnapshot)
				_, _, err = c.scan(dbwire.ScanRequest{Table: 2})
				wantCode(t, err, dbwire.CodeNoSnapshot)
				_, _, err = c.object([32]byte{})
				wantCode(t, err, dbwire.CodeNoSnapshot)
				_, err = c.events()
				wantCode(t, err, dbwire.CodeNoSnapshot)
			}
			check()
			c.mustSnapshot()
			if found, v := c.mustGet(2, "k"); !found || v != "v" {
				t.Errorf("%v: Get after the refusals: %v %q", role, found, v)
			}
			if err := c.release(); err != nil {
				t.Fatal(err)
			}
			check()
			if err := c.release(); err != nil { // nothing is held, and Release does not mind
				t.Errorf("Release of nothing: %v", err)
			}
		}
	})

	t.Run("a held snapshot does not block commits", func(t *testing.T) {
		f := newFixture(t, nil)
		f.update(func(tx *logdb.Tx) {
			for i := range 100 {
				_ = tx.Put(3, []byte{byte(i)}, make([]byte, 200))
			}
		})
		w := f.connect(dbwire.RoleWriter)
		var readers []*client
		for range 3 {
			r := f.connect(dbwire.RoleReader)
			r.mustSnapshot()
			readers = append(readers, r)
		}
		// One reader is in the middle of a Scan whose reply it does not take.
		slow := readers[0]
		if err := <-slow.send(dbwire.KindScan, func(e *dbwire.Enc) { e.ScanRequest(dbwire.ScanRequest{Table: 3}) }); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			for range 5 {
				if _, _, err := w.tryCommit(put(2, "k", "v")); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(waitLimit):
			t.Fatal("the commits wait for the readers")
		}
		// The snapshots are still at the first commit.
		if found, _ := readers[1].mustGet(2, "k"); found {
			t.Error("a held snapshot sees a later commit")
		}
	})

	t.Run("the goroutine of the session holds it", func(t *testing.T) {
		f := newFixture(t, nil)
		f.update(func(tx *logdb.Tx) { _ = tx.Put(2, []byte("k"), []byte("v")) })
		held := func(want int) {
			t.Helper()
			eventually(t, fmt.Sprintf("%d goroutines to hold a snapshot", want), func() bool { return len(stacksWith(snapshotMarker)) == want })
		}
		held(0)
		a := f.connect(dbwire.RoleReader)
		a.mustSnapshot()
		stacks := stacksWith(snapshotMarker)
		if len(stacks) != 1 || !strings.Contains(stacks[0], "dbserver.(*session).serve") {
			t.Fatalf("a snapshot is held by %d goroutines, which are not the session's: %v", len(stacks), stacks)
		}
		b := f.connect(dbwire.RoleReader)
		b.mustSnapshot()
		held(2)

		// Release.
		if err := a.release(); err != nil {
			t.Fatal(err)
		}
		held(1)
		// The end of the session.
		_ = b.conn.Close()
		held(0)

		// A Commit that was read to its end lets go of the snapshot, whether it commits or not.
		w := f.connect(dbwire.RoleWriter)
		seq := w.mustSnapshot()
		held(1)
		if _, _, err := w.commit(seq, put(2, "k", "new"), event("e")); err != nil {
			t.Fatal(err)
		}
		held(0)
		seq = w.mustSnapshot()
		_, _, err := w.commit(seq+7, put(2, "k", "again"), event("e"))
		wantCode(t, err, dbwire.CodeSeq)
		held(0)

		// A request that is refused before it is read leaves the snapshot where it is.
		a.mustSnapshot()
		_, _, err = a.commit(seq, put(2, "k", "denied"), event("e"))
		wantCode(t, err, dbwire.CodeDenied)
		held(1)
		if found, _ := a.mustGet(2, "k"); !found {
			t.Error("the snapshot of a reader that was refused is gone")
		}
	})

	t.Run("the idle limit returns the View", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Limits.SnapshotIdle = 40 * time.Millisecond })
		idle := make(chan struct{}, 4)
		f.srv.onIdle = func() { idle <- struct{}{} }
		w := f.connect(dbwire.RoleWriter) // the idle limit ends the connection of a reader that holds no snapshot
		w.mustSnapshot()
		<-idle
		// No request has come since: the View has returned, and the session is still there.
		eventually(t, "the View to return", func() bool { return len(stacksWith(snapshotMarker)) == 0 })
		f.waitSessions(1)
		_, _, err := w.get(2, "k")
		wantCode(t, err, dbwire.CodeNoSnapshot)
	})

	t.Run("Views do not nest", func(t *testing.T) {
		f := newFixture(t, nil)
		w := f.connect(dbwire.RoleWriter)
		r := f.connect(dbwire.RoleReader)
		want := uint64(0)
		for i := range 1000 {
			if i%97 == 0 {
				want, _ = w.mustCommit(put(2, fmt.Sprintf("k%d", i), "v"))
			}
			if got := r.mustSnapshot(); got != want {
				t.Fatalf("Snapshot %d is at %d, want %d", i, got, want)
			}
		}
		// The session of the writer leaves its View just after the reply to its Commit, so it may still be on its way out.
		eventually(t, "one View in the stacks of the sessions after 1,000 Snapshot requests in a row", func() bool {
			n := 0
			for _, g := range stacksWith("dbserver.(*session).serve") {
				n += strings.Count(g, "dbserver.(*Server).openSnapshot(")
			}
			return n == 1
		})
	})

	t.Run("a commit does not block readers", func(t *testing.T) {
		var armed atomic.Bool
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		f := newFixture(t, func(o *Options) {
			o.Log.Hook = func(point string) error {
				if point == "update:objects" && armed.Load() {
					entered <- struct{}{}
					<-release
				}
				return nil
			}
		})
		f.update(func(tx *logdb.Tx) { _ = tx.Put(2, []byte("k"), []byte("v")) })
		w := f.connect(dbwire.RoleWriter)
		armed.Store(true)
		res := make(chan error, 1)
		go func() {
			_, _, err := w.tryCommit(put(2, "k", "new"))
			res <- err
		}()
		<-entered // the Update is inside the engine, holding its lock

		r := f.connect(dbwire.RoleReader)
		r.mustSnapshot()
		if found, v := r.mustGet(2, "k"); !found || v != "v" {
			t.Errorf("Get during a commit: %v %q", found, v)
		}
		if rows, _ := r.scanAll(2, "", 0); len(rows) != 1 {
			t.Errorf("Scan during a commit: %v", rows)
		}
		if _, err := r.events(); err != nil {
			t.Errorf("Events during a commit: %v", err)
		}
		close(release)
		if err := <-res; err != nil {
			t.Fatal(err)
		}
	})
}

func TestGet(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Frame = 64 })
	big := strings.Repeat("0123456789", 100)
	f.update(func(tx *logdb.Tx) {
		_ = tx.Put(2, []byte("key"), []byte("value"))
		_ = tx.Put(2, []byte("empty value"), nil)
		_ = tx.Put(2, nil, []byte("empty key"))
		_ = tx.Put(2, []byte("big"), []byte(big))
		_ = tx.Put(2, []byte(strings.Repeat("k", dbwire.MaxKey)), []byte("longest key"))
	})
	c := f.connect(dbwire.RoleReader)
	c.mustSnapshot()
	for _, tc := range []struct {
		table uint32
		key   string
		found bool
		value string
	}{
		{2, "key", true, "value"},
		{2, "empty value", true, ""},
		{2, "", true, "empty key"},
		{2, "big", true, big},
		{2, strings.Repeat("k", dbwire.MaxKey), true, "longest key"},
		{2, "missing", false, ""},
		{2, "ke", false, ""},
		{2, "key ", false, ""},
		{3, "key", false, ""},
		{0, "key", false, ""},
		{99, "key", false, ""},
	} {
		found, v := c.mustGet(tc.table, tc.key)
		if found != tc.found || v != tc.value {
			t.Errorf("Get(%d, %.20q) = %v %.20q, want %v %.20q", tc.table, tc.key, found, v, tc.found, tc.value)
		}
	}

	// A key over the limit is a limit that was passed in reading: the connection ends.
	_, _, err := c.get(2, strings.Repeat("k", dbwire.MaxKey+1))
	wantCode(t, err, dbwire.CodeLimit)
	c.wantClosed()
}

func TestScan(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Frame = 100 })
	f.update(func(tx *logdb.Tx) {
		for i := range 10 {
			_ = tx.Put(2, fmt.Appendf(nil, "a%02d", i), fmt.Appendf(nil, "value of a%02d....", i)) // 3+17 bytes: a row of 32
		}
		for i := range 5 {
			_ = tx.Put(2, fmt.Appendf(nil, "b%02d", i), []byte("b"))
		}
		_ = tx.Put(2, nil, []byte("empty key"))
		_ = tx.Put(3, []byte("other table"), []byte("x"))
	})
	c := f.connect(dbwire.RoleReader)
	c.mustSnapshot()

	t.Run("pages end at the frame size", func(t *testing.T) {
		// A row is 9+3+17 = 29 bytes: 3 rows are 87 and the fourth makes 116, over the frame of 100.
		var sizes []int
		var more []bool
		req := dbwire.ScanRequest{Table: 2, Prefix: []byte("a")}
		var all []kv
		for {
			rows, m, err := c.scan(req)
			if err != nil {
				t.Fatal(err)
			}
			sizes, more, all = append(sizes, len(rows)), append(more, m), append(all, rows...)
			if !m {
				break
			}
			req.HasAfter, req.After = true, []byte(rows[len(rows)-1].Key)
		}
		if want := []int{4, 4, 2}; !slices.Equal(sizes, want) {
			t.Errorf("pages of %v rows, want %v", sizes, want)
		}
		if want := []bool{true, true, false}; !slices.Equal(more, want) {
			t.Errorf("more is %v, want %v", more, want)
		}
		if len(all) != 10 || all[0].Key != "a00" || all[9].Key != "a09" || all[3].Value != "value of a03...." {
			t.Errorf("rows: %v", all)
		}
	})

	t.Run("prefix and order", func(t *testing.T) {
		rows, _ := c.scanAll(2, "b", 0)
		if want := []string{"b00", "b01", "b02", "b03", "b04"}; !slices.Equal(keysOf(rows), want) {
			t.Errorf("prefix b: %v", keysOf(rows))
		}
		rows, _ = c.scanAll(2, "", 0)
		if len(rows) != 16 || rows[0].Key != "" || rows[1].Key != "a00" || rows[15].Key != "b04" {
			t.Errorf("all rows, the empty key first: %v", keysOf(rows))
		}
		if rows, _ := c.scanAll(2, "c", 0); len(rows) != 0 {
			t.Errorf("a prefix that nothing has: %v", rows)
		}
		if rows, _ := c.scanAll(9, "", 0); len(rows) != 0 {
			t.Errorf("a table that nothing has: %v", rows)
		}
		if rows, _ := c.scanAll(3, "", 0); len(rows) != 1 {
			t.Errorf("another table: %v", rows)
		}
	})

	t.Run("the limit of rows", func(t *testing.T) {
		rows, more, err := c.scan(dbwire.ScanRequest{Table: 2, Prefix: []byte("b"), Limit: 2})
		if err != nil || len(rows) != 2 || !more {
			t.Fatalf("limit 2 of 5: %d rows, more %v, %v", len(rows), more, err)
		}
		// A page that the limit stops may be the last: the server does not look ahead.
		rows, more, err = c.scan(dbwire.ScanRequest{Table: 2, Prefix: []byte("b"), Limit: 5})
		if err != nil || len(rows) != 5 || !more {
			t.Fatalf("limit 5 of 5: %d rows, more %v, %v", len(rows), more, err)
		}
		rows, more, err = c.scan(dbwire.ScanRequest{Table: 2, Prefix: []byte("b"), HasAfter: true, After: []byte("b04"), Limit: 5})
		if err != nil || len(rows) != 0 || more {
			t.Fatalf("after the last: %d rows, more %v, %v", len(rows), more, err)
		}
		rows, more, err = c.scan(dbwire.ScanRequest{Table: 2, Prefix: []byte("b"), Limit: 6})
		if err != nil || len(rows) != 5 || more {
			t.Fatalf("limit 6 of 5: %d rows, more %v, %v", len(rows), more, err)
		}
		for _, limit := range []uint32{1, 2, 3, 7} {
			rows, _ := c.scanAll(2, "", limit)
			if len(rows) != 16 {
				t.Errorf("pages of %d rows give %d rows", limit, len(rows))
			}
		}
	})

	t.Run("after", func(t *testing.T) {
		engine := func(prefix, after string) []string {
			var keys []string
			_ = f.db().View(context.Background(), func(tx *logdb.Tx) error {
				tx.ScanAfter(2, []byte(prefix), []byte(after), func(k, _ []byte) bool {
					keys = append(keys, string(k))
					return true
				})
				return nil
			})
			return keys
		}
		for _, tc := range []struct{ prefix, after string }{
			{"", ""}, {"a", ""}, {"a", "a03"}, {"a", "a09"}, {"a", "a091"}, {"a", "a"}, {"b", "a05"}, {"b", "c"},
			{"a", "b02"}, {"", "b04"}, {"", "zzz"}, {"a0", "a05"},
		} {
			var got []string
			req := dbwire.ScanRequest{Table: 2, Prefix: []byte(tc.prefix), HasAfter: true, After: []byte(tc.after)}
			for {
				rows, more, err := c.scan(req)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, keysOf(rows)...)
				if !more {
					break
				}
				req.After = []byte(rows[len(rows)-1].Key)
			}
			if want := engine(tc.prefix, tc.after); !slices.Equal(got, want) {
				t.Errorf("prefix %q after %q gives %v, want %v", tc.prefix, tc.after, got, want)
			}
		}
	})

	t.Run("a value of several frames", func(t *testing.T) {
		big := strings.Repeat("x", 5000)
		f.update(func(tx *logdb.Tx) { _ = tx.Put(4, []byte("big"), []byte(big)) })
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		rows, _ := c.scanAll(4, "", 0)
		if len(rows) != 1 || rows[0].Value != big {
			t.Fatalf("%d rows", len(rows))
		}
	})

	t.Run("a prefix over the limit", func(t *testing.T) {
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		_, _, err := c.scan(dbwire.ScanRequest{Table: 2, Prefix: make([]byte, dbwire.MaxKey+1)})
		wantCode(t, err, dbwire.CodeLimit)
		c.wantClosed()
	})
}

// TestScanFrameBoundary: a page ends with the row that brings it to the size of the frame, or over it.
func TestScanFrameBoundary(t *testing.T) {
	for _, tc := range []struct {
		frame int
		pages []int
	}{
		{100, []int{4, 4, 2}}, // a row is 9+4+12 = 25 bytes: four of them are the frame
		{101, []int{5, 5, 0}},
		{99, []int{4, 4, 2}},
		{25, []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0}},
	} {
		t.Run(fmt.Sprint(tc.frame), func(t *testing.T) {
			f := newFixture(t, func(o *Options) { o.Limits.Frame = tc.frame })
			f.update(func(tx *logdb.Tx) {
				for i := range 10 {
					_ = tx.Put(2, fmt.Appendf(nil, "k%03d", i), fmt.Appendf(nil, "value%07d", i))
				}
			})
			c := f.connect(dbwire.RoleReader)
			c.mustSnapshot()
			var pages []int
			req := dbwire.ScanRequest{Table: 2}
			for {
				rows, more, err := c.scan(req)
				if err != nil {
					t.Fatal(err)
				}
				pages = append(pages, len(rows))
				if !more {
					break
				}
				req.HasAfter, req.After = true, []byte(rows[len(rows)-1].Key)
			}
			if !slices.Equal(pages, tc.pages) {
				t.Errorf("pages of %v rows, want %v", pages, tc.pages)
			}
		})
	}
}

func keysOf(rows []kv) []string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Key
	}
	return keys
}

// TestScanPagesOnePass: the server keeps no place between the pages of a scan, and the engine gives it each row once.
func TestScanPagesOnePass(t *testing.T) {
	const rows = 200_000
	f := newFixture(t, func(o *Options) { o.Limits.Frame = 64 << 10 })
	f.update(func(tx *logdb.Tx) {
		for i := range rows {
			_ = tx.Put(7, fmt.Appendf(nil, "%08d", i), fmt.Appendf(nil, "v%011d", i))
		}
		_ = tx.Put(8, []byte("g"), []byte("get"))
	})
	c := f.connect(dbwire.RoleReader)
	c.mustSnapshot()

	var got int
	pages := 0
	req := dbwire.ScanRequest{Table: 7}
	for {
		page, more, err := c.scan(req)
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range page {
			if want := fmt.Sprintf("%08d", got+i); r.Key != want {
				t.Fatalf("row %d has the key %s, want %s", got+i, r.Key, want)
			}
		}
		got += len(page)
		pages++
		if !more {
			break
		}
		req.HasAfter, req.After = true, []byte(page[len(page)-1].Key)
		// Another request between two pages does not make the next one start over.
		if found, v := c.mustGet(8, "g"); !found || v != "get" {
			t.Fatalf("Get between pages: %v %q", found, v)
		}
	}
	if got != rows {
		t.Fatalf("read %d rows, want %d", got, rows)
	}
	if pages < 50 {
		t.Errorf("%d pages: the pages are not of the size of the frame", pages)
	}
	if n := f.srv.scanned.Load(); n != rows {
		t.Errorf("the engine gave the server %d rows for %d, in %d pages", n, rows, pages)
	}
}

func TestScanPagesMixed(t *testing.T) {
	const perPrefix = 3000
	f := newFixture(t, func(o *Options) { o.Limits.Frame = 4096 })
	f.update(func(tx *logdb.Tx) {
		for i := range perPrefix {
			_ = tx.Put(7, fmt.Appendf(nil, "a/%06d", i), fmt.Appendf(nil, "A%d", i))
			_ = tx.Put(7, fmt.Appendf(nil, "b/%06d", i), fmt.Appendf(nil, "B%d", i))
		}
	})
	reader := f.connect(dbwire.RoleReader)
	reader.mustSnapshot()
	w := f.connect(dbwire.RoleWriter)

	// Two scans of different prefixes, read in turns, page by page, with a limit of rows that changes, while another
	// connection commits to the same table: the pages are those of the snapshot.
	type scanState struct {
		req  dbwire.ScanRequest
		keys []string
		done bool
	}
	scans := []*scanState{
		{req: dbwire.ScanRequest{Table: 7, Prefix: []byte("a/")}},
		{req: dbwire.ScanRequest{Table: 7, Prefix: []byte("b/")}},
	}
	for round := 0; scans[0] != nil && (!scans[0].done || !scans[1].done); round++ {
		if round == 2 {
			w.mustCommit(put(7, "a/000010", "changed"), del(7, "b/000020"), put(7, "a/new", "new"))
		}
		for _, s := range scans {
			if s.done {
				continue
			}
			s.req.Limit = uint32(round%5) * 7 // 0 is no limit
			rows, more, err := reader.scan(s.req)
			if err != nil {
				t.Fatal(err)
			}
			s.keys = append(s.keys, keysOf(rows)...)
			if !more {
				s.done = true
				continue
			}
			s.req.HasAfter, s.req.After = true, []byte(rows[len(rows)-1].Key)
		}
	}
	for i, s := range scans {
		prefix := "ab"[i : i+1]
		if len(s.keys) != perPrefix {
			t.Fatalf("scan %s: %d rows, want %d", prefix, len(s.keys), perPrefix)
		}
		for j, k := range s.keys {
			if want := fmt.Sprintf("%s/%06d", prefix, j); k != want {
				t.Fatalf("scan %s: row %d is %s, want %s", prefix, j, k, want)
			}
		}
	}
	if found, v := reader.mustGet(7, "a/000010"); !found || v != "A10" {
		t.Errorf("the snapshot has %v %q for a key that a later commit changed", found, v)
	}
	if found, _ := reader.mustGet(7, "b/000020"); !found {
		t.Error("the snapshot lost a key that a later commit deleted")
	}

	// The last key of the prefix: an empty page, and no more.
	rows, more, err := reader.scan(dbwire.ScanRequest{Table: 7, Prefix: []byte("a/"), HasAfter: true, After: []byte(fmt.Sprintf("a/%06d", perPrefix-1))})
	if err != nil || len(rows) != 0 || more {
		t.Errorf("after the last key: %d rows, more %v, %v", len(rows), more, err)
	}
	// A key that is not in the table, one before the prefix and one after it: the rows of the keys that are greater.
	var inSnapshot []string
	for i := range perPrefix {
		inSnapshot = append(inSnapshot, fmt.Sprintf("a/%06d", i))
	}
	for _, after := range []string{"a/000010x", "0", "z", "a/", "a/000009\xff", "", "a/002998", "a/002999\x00"} {
		var want []string
		for _, k := range inSnapshot {
			if k > after && len(want) < 3 {
				want = append(want, k)
			}
		}
		rows, _, err := reader.scan(dbwire.ScanRequest{Table: 7, Prefix: []byte("a/"), HasAfter: true, After: []byte(after), Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(keysOf(rows), want) {
			t.Errorf("after %q: %v, want %v", after, keysOf(rows), want)
		}
	}
}

func TestObject(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Frame = 512 })
	small := "small object"
	large := strings.Repeat("a large object ", 200) // in a file: over the threshold of 256 bytes
	w := f.connect(dbwire.RoleWriter)
	w.mustSnapshot()
	for _, data := range []string{small, large} {
		if _, err := w.putObject(data); err != nil {
			t.Fatal(err)
		}
		// The writer finds what it has staged, before the commit.
		if found, got, err := w.object(hashOf(data)); err != nil || !found || string(got) != data {
			t.Fatalf("a staged object: %v %d bytes %v", found, len(got), err)
		}
	}
	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot()
	if found, _, err := r.object(hashOf(small)); err != nil || found {
		t.Errorf("a reader finds a staged object: %v %v", found, err)
	}
	if found, _, err := w.object(hashOf("not there")); err != nil || found {
		t.Errorf("an object that nothing has: %v %v", found, err)
	}

	seq := w.mustSnapshot() // a Snapshot drops what is staged
	if found, _, _ := w.object(hashOf(small)); found {
		t.Error("an object stays staged after a Snapshot")
	}
	for _, data := range []string{small, large} {
		if _, err := w.putObject(data); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := w.commit(seq); err != nil { // only objects, and the mark
		t.Fatal(err)
	}
	// The reader's snapshot is older than the commit.
	if found, _, _ := r.object(hashOf(small)); found {
		t.Error("an old snapshot finds an object of a later commit")
	}
	r.mustSnapshot()
	for _, data := range []string{small, large} {
		if found, got, err := r.object(hashOf(data)); err != nil || !found || string(got) != data {
			t.Errorf("a committed object: %v %d bytes %v", found, len(got), err)
		}
	}
	if _, err := f.fs.Lstat(objectPath(large)); err != nil {
		t.Errorf("the large object is not in a file: %v", err)
	}

	t.Run("damage", func(t *testing.T) {
		file, err := f.fs.OpenFile(objectPath(large), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		if err := file.Truncate(20); err != nil {
			t.Fatal(err)
		}
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		_, _, err = c.object(hashOf(large))
		re := wantCode(t, err, dbwire.CodeFailed)
		if !strings.Contains(re.Msg, "corrupt") || !strings.Contains(re.Msg, hashHex(large)) {
			t.Errorf("got %q", re.Msg)
		}
		if found, got, err := c.object(hashOf(small)); err != nil || !found || string(got) != small {
			t.Errorf("another object after the failure: %v %v", found, err)
		}
	})
}

func TestEvents(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Frame = 256 })
	w := f.connect(dbwire.RoleWriter)
	w.mustCommit(put(2, "k", "1"), event("alpha")) // the commit has a second event, the one that mustCommit adds
	huge := strings.Repeat("h", 3000)              // several frames
	seq := w.mustSnapshot()
	if _, _, err := w.commit(seq, event("beta"), event(huge)); err != nil {
		t.Fatal(err)
	}
	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot()
	events, err := r.events()
	if err != nil {
		t.Fatal(err)
	}
	type ev struct {
		Number, Seq uint64
		Data        string
	}
	var got []ev
	for _, e := range events {
		data := string(e.Data)
		if mark, ok := dbwire.ParseMark(e.Data); ok {
			data = "mark " + mark.Format(time.RFC3339Nano)
		}
		got = append(got, ev{e.Number, e.Seq, data})
	}
	want := []ev{
		{1, 1, "alpha"}, {2, 1, "test"}, {3, 1, "mark " + testBase.Format(time.RFC3339Nano)},
		{4, 2, "beta"}, {5, 2, huge}, {6, 2, "mark " + testBase.Add(time.Second).Format(time.RFC3339Nano)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events\n got %v\nwant %v", got, want)
	}
	for _, e := range events {
		if _, ok := dbwire.ParseMark(e.Data); ok != dbwire.IsDaemonEvent(e.Data) {
			t.Errorf("event %d: a mark is not the only event of the daemon", e.Number)
		}
	}

	// The events of the snapshot only.
	old := f.connect(dbwire.RoleReader)
	old.mustSnapshot()
	w.mustCommit(event("gamma"))
	events, err = old.events()
	if err != nil || len(events) != 6 {
		t.Errorf("an old snapshot has %d events (%v), want 6", len(events), err)
	}
	r.mustSnapshot()
	if events, err = r.events(); err != nil || len(events) != 9 || string(events[6].Data) != "gamma" {
		t.Errorf("a new snapshot has %d events (%v), want 9", len(events), err)
	}
}

func TestEventsEmpty(t *testing.T) {
	f := newFixture(t, nil)
	c := f.connect(dbwire.RoleReader)
	c.mustSnapshot()
	if events, err := c.events(); err != nil || len(events) != 0 {
		t.Errorf("a store without commits has events: %v %v", events, err)
	}
}

func TestEventsEngineFailure(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Limits.Frame = 4096
		o.Log.SegmentBytes = 1 << 10
	})
	w := f.connect(dbwire.RoleWriter)
	chunk := strings.Repeat("e", 1500)
	for range 4 {
		w.mustCommit(event(chunk))
	}
	f.fs.Inject(simfs.Fault{
		Match: func(op simfs.Op) bool {
			return op.Kind == simfs.OpRead && strings.HasSuffix(op.Path, "0000000000000004.log")
		},
		Err: syscall.EIO,
	})
	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot()
	events, err := r.events()
	re := wantCode(t, err, dbwire.CodeFailed)
	if len(events) == 0 {
		t.Error("the events before the failure were not sent")
	}
	if !strings.Contains(re.Msg, "input/output error") || strings.Contains(re.Msg, chunk) {
		t.Errorf("got %q", re.Msg)
	}
	// The reply ended properly: the connection goes on.
	if found, _ := r.mustGet(2, "k"); found {
		t.Error("found a key that nothing put")
	}
}

// TestEventsPeerGone: the last byte of an Events reply fills a frame exactly, and the peer is gone before that frame is
// written: the write fails with the reply open, which ends the session as any failed write does, and the daemon goes on.
func TestEventsPeerGone(t *testing.T) {
	for _, frames := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d frames", frames), func(t *testing.T) {
			f := newFixture(t, nil)
			frame := f.opts.Limits.Frame
			w := f.connect(dbwire.RoleWriter)
			seq := w.mustSnapshot()
			// The body is an event of the writer, the mark of its commit, and the byte that ends the events.
			n := frames*frame - 21 - (21 + dbwire.MarkSize) - 1 // an event has 21 bytes besides its data
			if _, _, err := w.commitExactly(seq, schemaOp(), event(strings.Repeat("e", n))); err != nil {
				t.Fatal(err)
			}
			r := f.connect(dbwire.RoleReader)
			r.mustSnapshot()
			events, err := r.events()
			if err != nil {
				t.Fatal(err)
			}
			size := 1
			for _, ev := range events {
				size += 21 + len(ev.Data)
			}
			if size != frames*frame {
				t.Fatalf("the body of the reply has %d bytes, want %d", size, frames*frame)
			}

			if err := <-r.send(dbwire.KindEvents, nil); err != nil {
				t.Fatal(err)
			}
			if frames > 1 {
				// The first frame is written when it is full; the peer takes it and then goes.
				if err := r.rd.Reply(); err != nil {
					t.Fatal(err)
				}
			}
			eventually(t, "the daemon to write the last frame", func() bool {
				return blockedIn("dbserver.(*session).eventsRequest", "net.(*pipe).Write")
			})
			_ = r.conn.Close()
			f.waitSessions(1) // the writer's

			other := f.connect(dbwire.RoleReader)
			other.mustSnapshot()
			if got, err := other.events(); err != nil || len(got) != len(events) {
				t.Errorf("the events after the session that failed: %d (%v), want %d", len(got), err, len(events))
			}
		})
	}
}

func TestVerify(t *testing.T) {
	large := strings.Repeat("an object in a file ", 100)
	setup := func(t *testing.T) (*fixture, *client) {
		f := newFixture(t, nil)
		w := f.connect(dbwire.RoleWriter)
		seq := w.mustSnapshot()
		if _, err := w.putObject(large); err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.commit(seq, put(2, "k", "v"), event("e")); err != nil {
			t.Fatal(err)
		}
		return f, f.connect(dbwire.RoleReader)
	}

	t.Run("a good store", func(t *testing.T) {
		_, r := setup(t)
		if err := r.verify(); err != nil {
			t.Fatal(err)
		}
		if err := r.verify(); err != nil { // and again, on the same connection, with or without a snapshot
			t.Fatal(err)
		}
	})

	t.Run("data that do not have their hash", func(t *testing.T) {
		f, r := setup(t)
		file, err := f.fs.OpenFile(objectPath(large), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := file.Stat()
		if _, err := file.WriteAt([]byte{'X'}, info.Size()-1); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		re := wantCode(t, r.verify(), dbwire.CodeFailed)
		want := "object " + hashHex(large) + ": the data has the hash "
		if !strings.HasPrefix(re.Msg, want) || len(re.Msg) != len(want)+64 {
			t.Errorf("got %q", re.Msg)
		}
		if err := r.verify(); err == nil {
			t.Error("a second Verify passed")
		}
	})

	t.Run("a file that is cut short", func(t *testing.T) {
		f, r := setup(t)
		file, err := f.fs.OpenFile(objectPath(large), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(30); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		re := wantCode(t, r.verify(), dbwire.CodeFailed)
		if !strings.Contains(re.Msg, ": corrupt: has 30 bytes, want ") {
			t.Errorf("got %q", re.Msg)
		}
	})

	t.Run("a log that is damaged", func(t *testing.T) {
		f, r := setup(t)
		seg := "logdb/log/0000000000000001-0000000000000001.log"
		file, err := f.fs.OpenFile(seg, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := file.Stat()
		if _, err := file.WriteAt([]byte{0xff, 0xff}, info.Size()-3); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		re := wantCode(t, r.verify(), dbwire.CodeFailed)
		if !strings.Contains(re.Msg, "corrupt") {
			t.Errorf("got %q", re.Msg)
		}
	})
}
