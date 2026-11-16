package dbclient_test

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// compareStates returns how the state of a daemon differs from that of an engine that has had the same commits: the
// daemon has one mark more at the end of the events of each commit, which are those that the daemon's clock gave.
func compareStates(daemon, direct state) error {
	if daemon.Seq != direct.Seq {
		return fmt.Errorf("the daemon is at the commit %d, the engine at %d", daemon.Seq, direct.Seq)
	}
	for table := uint32(1); table <= 12; table++ {
		if !slices.Equal(daemon.Tables[table], direct.Tables[table]) {
			return fmt.Errorf("table %d differs:\n daemon %q\n engine %q", table, daemon.Tables[table], direct.Tables[table])
		}
	}
	if !slices.Equal(daemon.Objects, direct.Objects) {
		return fmt.Errorf("the objects differ:\n daemon %v\n engine %v", daemon.Objects, direct.Objects)
	}
	bySeq := func(events []loggedEvent) map[uint64][]loggedEvent {
		m := map[uint64][]loggedEvent{}
		for _, ev := range events {
			m[ev.Seq] = append(m[ev.Seq], ev)
		}
		return m
	}
	dm, em := bySeq(daemon.Events), bySeq(direct.Events)
	for seq := uint64(1); seq <= daemon.Seq; seq++ {
		got, want := dm[seq], em[seq]
		if len(got) != len(want)+1 {
			return fmt.Errorf("the commit %d has %d events in the daemon and %d in the engine, which should be one fewer", seq, len(got), len(want))
		}
		mark, ok := dbwire.ParseMark(got[len(got)-1].Data)
		if !ok {
			return fmt.Errorf("the last event of the commit %d is not a mark", seq)
		}
		if wantTime := clockBase.Add(time.Duration(seq-1) * time.Second); !mark.Equal(wantTime) {
			return fmt.Errorf("the mark of the commit %d is %v, want %v", seq, mark, wantTime)
		}
		for i, ev := range want {
			if string(got[i].Data) != string(ev.Data) {
				return fmt.Errorf("event %d of the commit %d: the daemon has %q, the engine %q", i, seq, got[i].Data, ev.Data)
			}
		}
	}
	// The numbers of the daemon count its marks.
	for i, ev := range daemon.Events {
		if ev.Number != uint64(i+1) {
			return fmt.Errorf("the daemon's event %d has the number %d", i+1, ev.Number)
		}
	}
	return nil
}

// pair is a daemon with a client, and an engine of its own, which a test gives the same transactions.
type pair struct {
	f      *fixture
	c      *dbclient.Client
	rec    *recorder
	db     *logdb.DB
	engine *simfs.FS
}

// newPair makes a pair whose stores have had the first commit, which puts the row of the schema and an event, and then
// the commit that does seed, if it is not nil.
func newPair(t *testing.T, mod func(*dbtest.Options), seed func(tx txn) error) *pair {
	t.Helper()
	f := newFixture(t, mod)
	c, rec := f.open(dbwire.RoleWriter, dbclient.Options{})
	fsys := simfs.New("direct")
	db, err := logdb.Create(fsys, *f.opts.Log)
	must(t, err)
	t.Cleanup(func() { _ = db.Close() })
	p := &pair{f: f, c: c, rec: rec, db: db, engine: fsys}
	first := func(tx txn) error {
		if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion)); err != nil {
			return err
		}
		if err := tx.AppendEvent([]byte(`{"seed":true}`)); err != nil {
			return err
		}
		if seed != nil {
			return seed(tx)
		}
		return nil
	}
	must(t, updateClient(c, first))
	must(t, updateEngine(db, first))
	rec.reset()
	return p
}

// update runs script on both, as an Update, and returns what it logged on each. A script that returns an error rolls back.
func (p *pair) update(t *testing.T, script func(tx txn) ([]string, error)) (client, engine []string, cerr, eerr error) {
	t.Helper()
	cerr = updateClient(p.c, func(tx txn) (err error) { client, err = script(tx); return err })
	eerr = updateEngine(p.db, func(tx txn) (err error) { engine, err = script(tx); return err })
	return client, engine, cerr, eerr
}

// both runs script on both sides, and fails the test if their logs, their outcomes or their states differ. It returns the
// log of the client.
func (p *pair) both(t *testing.T, script func(tx txn) ([]string, error)) []string {
	t.Helper()
	client, engine, cerr, eerr := p.update(t, script)
	if outcome(cerr) != outcome(eerr) {
		t.Errorf("the Update ended as %s on the client, and as %s on the engine", outcome(cerr), outcome(eerr))
	}
	if !reflect.DeepEqual(client, engine) {
		t.Errorf("the client and the engine gave different results:\n client %s\n engine %s", strings.Join(client, "\n        "),
			strings.Join(engine, "\n        "))
	}
	p.compare(t)
	return client
}

func (p *pair) compare(t *testing.T) {
	t.Helper()
	direct, err := readState(p.db)
	must(t, err)
	if err := compareStates(p.f.state(), direct); err != nil {
		t.Error(err)
	}
}

// fmtGet and fmtScan are the results of a Get and a Scan, as one line.
func fmtGet(tx txn, table uint32, key string) string {
	v, ok, err := tx.Get(table, []byte(key))
	switch {
	case err != nil:
		return "error"
	case !ok:
		return fmt.Sprintf("Get %d %q: missing", table, key)
	}
	return fmt.Sprintf("Get %d %q: %s cap %d", table, key, digest(v), cap(v))
}

func fmtScan(tx txn, table uint32, prefix string) string {
	var parts []string
	err := tx.Scan(table, []byte(prefix), func(k, v []byte) bool {
		parts = append(parts, fmt.Sprintf("%q=%s", k, digest(v)))
		return len(parts) < 10_000 // a Scan that does not end fails the test, and does not fill the memory
	})
	if err != nil {
		return "error"
	}
	return fmt.Sprintf("Scan %d %q: %s", table, prefix, strings.Join(parts, " "))
}
