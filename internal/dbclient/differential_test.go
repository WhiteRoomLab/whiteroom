package dbclient_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

func longTests() bool { return os.Getenv("WHITEROOM_LONG_TESTS") == "1" }

// reproduce returns the command that runs the subtest of a seed alone.
func reproduce(t *testing.T, seed int64) string {
	env := ""
	if longTests() || seed >= 200 {
		env = "WHITEROOM_LONG_TESTS=1 "
	}
	name := regexp.QuoteMeta(t.Name()[strings.Index(t.Name(), "/")+1:])
	return fmt.Sprintf("%sgo test -count=1 -run '^TestDifferential$/^%s$' ./internal/dbclient", env, name)
}

// interp runs the steps of a program on a transaction and logs what each call gave.
type interp struct {
	prog  *program
	log   []string
	hints hintCounts
}

// hintCounts counts the calls of the hints that a program ran, and the keys, hashes and prefixes that they named.
type hintCounts struct {
	getMany, getManyKeys, prefetchObjects, prefetchObjectHashes, prefetchScans, prefetchPrefixes, dropPrefetched int
}

func (h *hintCounts) add(o hintCounts) {
	h.getMany += o.getMany
	h.getManyKeys += o.getManyKeys
	h.prefetchObjects += o.prefetchObjects
	h.prefetchObjectHashes += o.prefetchObjectHashes
	h.prefetchScans += o.prefetchScans
	h.prefetchPrefixes += o.prefetchPrefixes
	h.dropPrefetched += o.dropPrefetched
}

func (in *interp) logf(format string, args ...any) {
	in.log = append(in.log, fmt.Sprintf(format, args...))
}

func result(v []byte, ok bool, err error) string {
	switch {
	case err != nil:
		return errText(err)
	case !ok:
		return "missing"
	}
	return digest(v)
}

// errText names an error so that the engine and the client say the same: both give the texts of the errors that a
// transaction refuses a call with.
func errText(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, dbclient.ErrReadOnly), errors.Is(err, logdb.ErrReadOnly):
		return "error: read-only"
	}
	return "error: " + err.Error()
}

func (in *interp) run(tx txn, st step) error {
	for i, c := range st.calls {
		in.call(tx, fmt.Sprintf("call %d: ", i), c)
	}
	if st.update && st.event {
		in.logf("end: AppendEvent -> %s", errText(tx.AppendEvent([]byte(`{"end":true}`))))
	}
	if st.abort {
		return errAbort
	}
	return nil
}

func (in *interp) call(tx txn, p string, c call) {
	switch c.op {
	case opGet:
		v, ok, err := tx.Get(c.table, c.key)
		in.logf("%sGet %d %q -> %s", p, c.table, c.key, result(v, ok, err))
	case opPut:
		in.logf("%sPut %d %q %s -> %s", p, c.table, c.key, digest(c.value), errText(tx.Put(c.table, c.key, c.value)))
	case opDelete:
		in.logf("%sDelete %d %q -> %s", p, c.table, c.key, errText(tx.Delete(c.table, c.key)))
	case opScan:
		n := 0
		err := tx.Scan(c.table, c.key, func(k, v []byte) bool {
			if n >= 5000 { // a Scan that sees what its own function writes does not end
				in.logf("%s  the Scan does not end", p)
				return false
			}
			in.logf("%s  row %d %q=%s", p, n, k, digest(v))
			if n < len(c.script.rows) {
				in.actions(tx, p, k, c.script.rows[n])
			}
			n++
			return c.script.stopAt == 0 || n < c.script.stopAt
		})
		in.logf("%sScan %d %q -> %d rows, %s", p, c.table, c.key, n, errText(err))
	case opPutObject:
		data := in.prog.objects[c.object]
		in.logf("%sPutObject #%d (%d bytes) -> %s", p, c.object, len(data), errText(tx.PutObject(hashOfObject(data), data)))
	case opObject:
		hash := hashOfObject([]byte("an object that nothing puts"))
		if c.object >= 0 {
			hash = hashOfObject(in.prog.objects[c.object])
		}
		data, found, err := tx.Object(hash)
		in.logf("%sObject #%d -> %s", p, c.object, result(data, found, err))
	case opEvent:
		in.logf("%sAppendEvent %s -> %s", p, c.value, errText(tx.AppendEvent(c.value)))
	case opSeq:
		in.logf("%sSeq -> %d", p, tx.Seq())
	case opEvents:
		var datas []string
		err := tx.Events(bg, func(_ uint64, data []byte) bool {
			if !dbwire.IsDaemonEvent(data) {
				datas = append(datas, string(data))
			}
			return true
		})
		in.logf("%sEvents -> %s, %s", p, sum(datas), errText(err))
	case opGetMany:
		in.hints.getMany++
		in.hints.getManyKeys += len(c.keys)
		in.logf("%sGetMany %d %d keys -> %s", p, c.table, len(c.keys), errText(tx.GetMany(c.table, c.keys)))
	case opPrefetchObjects:
		hashes := make([][32]byte, len(c.objects))
		for i, o := range c.objects {
			hashes[i] = hashOfObject([]byte("an object that nothing puts"))
			if o >= 0 {
				hashes[i] = hashOfObject(in.prog.objects[o])
			}
		}
		in.hints.prefetchObjects++
		in.hints.prefetchObjectHashes += len(hashes)
		in.logf("%sPrefetchObjects %d hashes -> %s", p, len(hashes), errText(tx.PrefetchObjects(hashes)))
	case opPrefetchScans:
		in.hints.prefetchScans++
		in.hints.prefetchPrefixes += len(c.keys)
		in.logf("%sPrefetchScans %d %d prefixes -> %s", p, c.table, len(c.keys), errText(tx.PrefetchScans(c.table, c.keys)))
	case opDropPrefetched:
		in.hints.dropPrefetched++
		tx.DropPrefetched()
		in.logf("%sDropPrefetched", p)
	}
}

// allowed says whether an action of a Scan may write key: the daemon refuses a row of an append-only table that is
// changed, a row of the heads that is deleted, and the row of the schema that is not there or is not a version.
func allowed(a action, key []byte) bool {
	switch a.table {
	case 0, 3, 4, 5:
		return false
	case 11:
		return a.op == opPut || a.op == opDelete && strings.HasPrefix(string(key), "zz-missing-")
	case 1:
		return string(key) != dbwire.MetaSchema
	}
	return true
}

func (in *interp) actions(tx txn, p string, cur []byte, acts []action) {
	for _, a := range acts {
		key := a.keyAt(cur)
		switch a.op {
		case opGet:
			v, ok, err := tx.Get(a.table, key)
			in.logf("%s    Get %d %q -> %s", p, a.table, key, result(v, ok, err))
		case opPut, opDelete:
			if !allowed(a, key) {
				in.logf("%s    skip %v %d %q", p, a.op, a.table, key)
				continue
			}
			var err error
			if a.op == opPut {
				err = tx.Put(a.table, key, a.value)
			} else {
				err = tx.Delete(a.table, key)
			}
			in.logf("%s    %v %d %q %s -> %s", p, a.op, a.table, key, digest(a.value), errText(err))
		case opScan:
			var rows []string
			err := tx.Scan(a.table, a.prefix, func(k, v []byte) bool {
				rows = append(rows, fmt.Sprintf("%q=%s", k, digest(v)))
				return len(rows) < 8
			})
			in.logf("%s    Scan %d %q -> %s, %s", p, a.table, a.prefix, strings.Join(rows, " "), errText(err))
		case opGetMany:
			keys := append([][]byte{key}, a.keys...)
			in.hints.getMany++
			in.hints.getManyKeys += len(keys)
			in.logf("%s    GetMany %d %d keys -> %s", p, a.table, len(keys), errText(tx.GetMany(a.table, keys)))
		case opPutObject, opObject, opEvent, opSeq, opEvents, opPrefetchObjects, opPrefetchScans, opDropPrefetched: // not made for an action
		}
	}
}

// diffStats counts what the programs did, so that the test can tell that they have used each rule.
type diffCounts struct {
	seeds, committed, aborted, noOp, scans, scanWrites, nested, stopped, deletes, deleteMissing, pages int
	hints                                                                                              hintCounts
	// windows are the Write calls of the client with more than one request, and requests the requests in them.
	windows, windowRequests int
}

var diffStats diffCounts

func TestDifferential(t *testing.T) {
	seeds, steps := 200, 40
	if longTests() {
		seeds = 2000
	}
	diffStats = diffCounts{}
	for seed := range seeds {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { differential(t, int64(seed), steps) })
	}
	s := diffStats
	t.Logf("%d seeds: %d commits, %d Updates aborted, %d Updates with no op, %d Scans (%d stopped, %d writes in fn, %d nested), %d pages, %d deletes (%d of missing keys)",
		s.seeds, s.committed, s.aborted, s.noOp, s.scans, s.stopped, s.scanWrites, s.nested, s.pages, s.deletes, s.deleteMissing)
	h := s.hints
	t.Logf("hints: %d GetMany (%d keys), %d PrefetchObjects (%d hashes), %d PrefetchScans (%d prefixes), %d DropPrefetched; %d windows of %d requests",
		h.getMany, h.getManyKeys, h.prefetchObjects, h.prefetchObjectHashes, h.prefetchScans, h.prefetchPrefixes, h.dropPrefetched,
		s.windows, s.windowRequests)
	if s.seeds == seeds && (s.committed < seeds || s.aborted < seeds/2 || s.noOp < seeds || s.scanWrites < seeds || s.nested < seeds ||
		s.stopped < seeds/2 || s.pages < s.scans+seeds || s.deleteMissing < seeds) {
		t.Error("the programs did not use every case often enough")
	}
	// A kind of hint that never ran would leave the differential test with nothing to say about it.
	if s.seeds == seeds && (h.getMany < 2*seeds || h.getManyKeys < 4*seeds || h.prefetchObjects < seeds || h.prefetchObjectHashes < 2*seeds ||
		h.prefetchScans < seeds || h.prefetchPrefixes < 2*seeds || h.dropPrefetched < seeds || s.windows < seeds || s.windowRequests < 3*s.windows) {
		t.Error("the programs did not run every hint often enough, or sent no windows")
	}
}

func differential(t *testing.T, seed int64, steps int) {
	prog := genProgram(seed, steps)
	placement := []logdb.Placement{logdb.Inline, logdb.Files, logdb.Hybrid}[seed%3]
	f := newFixture(t, func(o *dbtest.Options) {
		*o.Log = logdb.Options{Placement: placement, Threshold: 50, WriterWait: 20 * time.Millisecond}
		o.Limits.Frame = prog.frame
	})
	c, rec := f.open(dbwire.RoleWriter, dbclient.Options{})
	db, err := logdb.Create(simfs.New("direct"), *f.opts.Log)
	must(t, err)
	t.Cleanup(func() { _ = db.Close() })
	first := func(tx txn) error {
		if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion)); err != nil {
			return err
		}
		return tx.AppendEvent([]byte(`{"seed":true}`))
	}
	must(t, updateClient(c, first))
	must(t, updateEngine(db, first))

	for i, st := range prog.steps {
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d, step %d, %s: %s\nreproduce: %s", seed, i, st.describe(), fmt.Sprintf(format, args...), reproduce(t, seed))
		}
		var cin, ein interp
		cin.prog, ein.prog = prog, prog
		seqBefore := f.seq()
		pagesBefore := rec.count(dbwire.KindScan)
		var cerr, eerr error
		if st.update {
			cerr = updateClient(c, func(tx txn) error { return cin.run(tx, st) })
			eerr = updateEngine(db, func(tx txn) error { return ein.run(tx, st) })
		} else {
			cerr = viewClient(c, func(tx txn) error { return cin.run(tx, st) })
			eerr = viewEngine(db, func(tx txn) error { return ein.run(tx, st) })
		}
		for j := range max(len(cin.log), len(ein.log)) {
			var cl, el string
			if j < len(cin.log) {
				cl = cin.log[j]
			}
			if j < len(ein.log) {
				el = ein.log[j]
			}
			if cl != el {
				ctx := strings.Join(cin.log[max(0, j-4):min(j, len(cin.log))], "\n        ")
				fail("the results differ at the line %d of the log of the step:\n   client %s\n   engine %s\n  before it:\n        %s", j, cl, el, ctx)
			}
		}
		if outcome(cerr) != outcome(eerr) {
			fail("the step ended as %s on the client, and as %s on the engine", outcome(cerr), outcome(eerr))
		}
		if st.update {
			direct, err := readState(db)
			must(t, err)
			if err := compareStates(f.state(), direct); err != nil {
				fail("after the step the stores differ: %v", err)
			}
		}

		diffStats.hints.add(cin.hints)
		for _, line := range cin.log {
			switch {
			case strings.Contains(line, "Scan ") && !strings.Contains(line, "    Scan") && strings.Contains(line, " rows, "):
				diffStats.scans++
			case strings.Contains(line, "    Scan"):
				diffStats.nested++
				diffStats.scans++
			case strings.Contains(line, "    Put") && strings.HasSuffix(line, "-> ok"), strings.Contains(line, "    Delete") && strings.HasSuffix(line, "-> ok"):
				diffStats.scanWrites++
			case strings.Contains(line, "Delete ") && strings.Contains(line, "zz-missing"):
				diffStats.deleteMissing++
			}
			if strings.Contains(line, "Delete ") {
				diffStats.deletes++
			}
		}
		for _, c := range st.calls {
			if c.op == opScan && c.script.stopAt > 0 {
				diffStats.stopped++
			}
		}
		diffStats.pages += rec.count(dbwire.KindScan) - pagesBefore
		switch {
		case !st.update:
		case outcome(cerr) == "aborted":
			diffStats.aborted++
		case f.seq() > seqBefore:
			diffStats.committed++
		default:
			diffStats.noOp++
		}
	}
	diffStats.seeds++
	for _, w := range rec.writeCalls() {
		if len(w.kinds) >= 2 {
			diffStats.windows++
			diffStats.windowRequests += len(w.kinds)
		}
	}

	direct, err := readState(db)
	must(t, err)
	if err := checkObjects(db, direct.Objects); err != nil {
		t.Fatalf("seed %d: the engine: %v", seed, err)
	}
	if err := checkObjects(f.engine(), f.state().Objects); err != nil {
		t.Fatalf("seed %d: the daemon: %v", seed, err)
	}
	if err := c.Verify(bg); err != nil {
		t.Fatalf("seed %d: Verify: %v", seed, err)
	}
}
