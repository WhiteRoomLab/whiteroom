package dbserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// secret is in the keys, values, objects and events that the tests send: no reply and no log line may hold it.
const secret = "SECRET-MARKER-5f3a"

const waitLimit = 20 * time.Second

// eventually calls cond until it is true, and fails the test after waitLimit.
func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(waitLimit); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// noLeaks fails the test at its end if a goroutine of the server is left. Register it before the server is made, so
// that it runs after the server is closed.
func noLeaks(t testing.TB) {
	t.Helper()
	t.Cleanup(func() {
		var stacks string
		leaked := func() bool {
			buf := make([]byte, 1<<20)
			stacks = string(buf[:runtime.Stack(buf, true)])
			for _, g := range strings.Split(stacks, "\n\n") {
				for _, marker := range []string{"dbserver.(*session)", "dbserver.(*Server).openSnapshot", "dbserver.(*Server).ServeConn",
					"dbserver.(*Server).Serve(", "dbserver.(*Server).serveConn", "dbserver.Run("} {
					if strings.Contains(g, marker) {
						return true
					}
				}
			}
			return false
		}
		for deadline := time.Now().Add(waitLimit); leaked(); {
			if time.Now().After(deadline) {
				t.Errorf("goroutines of the server are left:\n%s", stacks)
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logLines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

// clock is the clock of the marks: each call returns the time and moves it on by step.
type clock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.t
	c.t = c.t.Add(c.step)
	return t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// fixture is a server on a simulated file system, with the clients of a test.
type fixture struct {
	t     testing.TB
	fs    *simfs.FS
	dir   string // a data directory on the OS, if fs is nil
	opts  Options
	srv   *Server
	logs  *logLines
	clock *clock

	mu   sync.Mutex
	errs []string // the texts of every error reply that the clients have read
}

var testBase = time.Date(2026, 11, 15, 9, 30, 0, 123456789, time.UTC)

// testOptions are the options of the fixtures: small frames, and no limit by time or rate unless a test sets one.
func testOptions() Options {
	return Options{
		Log: logdb.Options{Placement: logdb.Hybrid, Threshold: 256, WriterWait: 20 * time.Millisecond},
		Limits: dbwire.Limits{Frame: 4096, SnapshotIdle: -1, Stall: waitLimit, CommitsPerSecond: -1, MinFree: -1,
			Readers: 4},
	}
}

// newFixture creates and opens the data directory "data" on a simulated file system. mod changes the options first.
func newFixture(t testing.TB, mod func(*Options)) *fixture {
	t.Helper()
	noLeaks(t)
	f := &fixture{t: t, fs: simfs.New("data"), opts: testOptions(), logs: &logLines{},
		clock: &clock{t: testBase, step: time.Second}}
	if mod != nil {
		mod(&f.opts)
	}
	f.opts.Now = f.clock.now
	f.opts.Logf = f.logs.logf
	if err := Create(f.fs, "data", f.opts); err != nil {
		t.Fatal(err)
	}
	f.open()
	return f
}

// newDirFixture is newFixture for a data directory on the OS, for the tests that need a real file system.
func newDirFixture(t testing.TB, mod func(*Options)) *fixture {
	t.Helper()
	noLeaks(t)
	f := &fixture{t: t, dir: filepath.Join(t.TempDir(), "data"), opts: testOptions(), logs: &logLines{},
		clock: &clock{t: testBase, step: time.Second}}
	if mod != nil {
		mod(&f.opts)
	}
	f.opts.Now = f.clock.now
	f.opts.Logf = f.logs.logf
	if err := CreateDir(f.dir, f.opts); err != nil {
		t.Fatal(err)
	}
	f.open()
	return f
}

// open opens the server, and closes it at the end of the test.
func (f *fixture) open() {
	f.t.Helper()
	var srv *Server
	var err error
	if f.fs == nil {
		srv, err = OpenDir(f.dir, f.opts)
	} else {
		srv, err = Open(f.fs, "data", f.opts)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	f.srv = srv
	f.t.Cleanup(func() { _ = srv.Close() })
}

// reopen closes the server and opens it again.
func (f *fixture) reopen() {
	f.t.Helper()
	if err := f.srv.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.open()
}

func (f *fixture) db() *logdb.DB { return EngineOf(f.srv) }

// update runs fn in an Update of the engine, which puts the schema row too if the store has none, as the first commit of a
// store has to.
func (f *fixture) update(fn func(tx *logdb.Tx)) {
	f.t.Helper()
	if err := f.db().Update(context.Background(), func(tx *logdb.Tx) error {
		if _, ok := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema)); !ok {
			op := schemaOp()
			if err := tx.Put(op.Table, op.Key, op.Value); err != nil {
				return err
			}
		}
		fn(tx)
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) record(err error) {
	var re *dbwire.RemoteError
	if errors.As(err, &re) {
		f.mu.Lock()
		f.errs = append(f.errs, re.Msg)
		f.mu.Unlock()
	}
}

func (f *fixture) errorTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.errs)
}

// dump is what a store holds, for the tests that say that a refusal changed nothing.
type dump struct {
	Seq     uint64
	Tables  map[uint32][]string
	Objects []string
	Events  []string
}

func (f *fixture) dump() dump {
	f.t.Helper()
	d := dump{Tables: map[uint32][]string{}}
	if err := f.db().View(context.Background(), func(tx *logdb.Tx) error {
		d.Seq = tx.Seq()
		for table := uint32(1); table <= 16; table++ {
			tx.Scan(table, nil, func(k, v []byte) bool {
				d.Tables[table] = append(d.Tables[table], fmt.Sprintf("%q=%q", k, v))
				return true
			})
		}
		tx.Objects(func(hash [32]byte, size int64) bool {
			d.Objects = append(d.Objects, fmt.Sprintf("%x:%d", hash, size))
			return true
		})
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.db().Events(context.Background(), func(ev logdb.Event) bool {
		d.Events = append(d.Events, fmt.Sprintf("%d@%d:%q", ev.Number, ev.Seq, ev.Data))
		return true
	}); err != nil {
		f.t.Fatal(err)
	}
	return d
}

// unchanged fails the test if the store is not what before was.
func (f *fixture) unchanged(before dump) {
	f.t.Helper()
	if after := f.dump(); !reflect.DeepEqual(before, after) {
		f.t.Errorf("a refusal changed the store:\n before %+v\n after  %+v", before, after)
	}
}

// hashOf is the SHA-256 of data.
func hashOf(data string) [32]byte { return sha256.Sum256([]byte(data)) }

// A client is the raw client of a test, over net.Pipe: it sends a request from another goroutine, because a
// refusal in the middle of a request is a reply that the client must read while it is still writing.
type client struct {
	t     testing.TB
	f     *fixture
	conn  net.Conn
	rd    *dbwire.Reader
	wr    *dbwire.Writer
	hello dbwire.HelloReply
}

// dial connects as role, without a Hello.
func (f *fixture) dial(role dbwire.Role) *client {
	f.t.Helper()
	cc, sc := net.Pipe()
	go f.srv.ServeConn(sc, role)
	c := &client{t: f.t, f: f, conn: cc, rd: dbwire.NewReader(cc, 1<<20), wr: dbwire.NewWriter(cc, 1<<20)}
	f.t.Cleanup(func() { _ = cc.Close() })
	return c
}

// connect connects as role and says Hello.
func (f *fixture) connect(role dbwire.Role) *client {
	f.t.Helper()
	c := f.dial(role)
	if err := c.sayHello(); err != nil {
		f.t.Fatalf("Hello as the %v: %v", role, err)
	}
	return c
}

func (c *client) sayHello() error {
	var h dbwire.HelloReply
	err := c.do(dbwire.KindHello, func(e *dbwire.Enc) { e.HelloRequest() }, func(d *dbwire.Dec) (err error) {
		h, err = dbwire.ReadHelloReply(d)
		return err
	})
	if err == nil {
		c.hello = h
		c.rd.SetMaxFrame(h.Frame)
		c.wr.SetMaxFrame(h.Frame)
	}
	return err
}

func (c *client) send(kind dbwire.Kind, body func(e *dbwire.Enc)) <-chan error {
	done := make(chan error, 1)
	go func() {
		c.wr.Begin(kind)
		e := &dbwire.Enc{W: c.wr}
		if body != nil {
			body(e)
		}
		err := c.wr.End()
		if e.Err != nil {
			err = e.Err
		}
		done <- err
	}()
	return done
}

// do sends a request, reads its reply with read, and returns the error of the reply, which is a *dbwire.RemoteError for
// a refusal.
func (c *client) do(kind dbwire.Kind, body func(e *dbwire.Enc), read func(d *dbwire.Dec) error) error {
	c.t.Helper()
	sent := c.send(kind, body)
	err := c.rd.Reply()
	if err == nil && read != nil {
		err = read(&dbwire.Dec{R: c.rd})
	}
	serr := <-sent
	if err == nil && serr != nil {
		err = serr
	}
	c.f.record(err)
	return err
}

func (c *client) snapshot() (seq uint64, err error) {
	err = c.do(dbwire.KindSnapshot, nil, func(d *dbwire.Dec) (err error) {
		seq, err = dbwire.ReadSnapshotReply(d)
		return err
	})
	return seq, err
}

func (c *client) mustSnapshot() uint64 {
	c.t.Helper()
	seq, err := c.snapshot()
	if err != nil {
		c.t.Fatalf("Snapshot: %v", err)
	}
	return seq
}

func (c *client) release() error {
	return c.do(dbwire.KindRelease, nil, dbwire.ReadEmpty)
}

func (c *client) get(table uint32, key string) (found bool, value []byte, err error) {
	err = c.do(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(table, []byte(key)) }, func(d *dbwire.Dec) (err error) {
		found, value, err = dbwire.ReadGetReply(d, 1<<30)
		return err
	})
	return found, value, err
}

func (c *client) mustGet(table uint32, key string) (bool, string) {
	c.t.Helper()
	found, value, err := c.get(table, key)
	if err != nil {
		c.t.Fatalf("Get: %v", err)
	}
	return found, string(value)
}

type kv struct{ Key, Value string }

func (c *client) scan(r dbwire.ScanRequest) (rows []kv, more bool, err error) {
	err = c.do(dbwire.KindScan, func(e *dbwire.Enc) { e.ScanRequest(r) }, func(d *dbwire.Dec) error {
		for {
			k, v, ok := dbwire.ReadScanRow(d, dbwire.MaxKey, 1<<30)
			if !ok {
				break
			}
			rows = append(rows, kv{string(k), string(v)})
		}
		var err error
		more, err = dbwire.ReadScanEnd(d)
		return err
	})
	return rows, more, err
}

// scanAll reads the table in pages, each after the last key of the one before, and returns the rows and the pages.
func (c *client) scanAll(table uint32, prefix string, limit uint32) (rows []kv, pages int) {
	c.t.Helper()
	req := dbwire.ScanRequest{Table: table, Prefix: []byte(prefix), Limit: limit}
	for {
		page, more, err := c.scan(req)
		if err != nil {
			c.t.Fatalf("Scan: %v", err)
		}
		pages++
		rows = append(rows, page...)
		if !more {
			return rows, pages
		}
		if len(page) == 0 {
			c.t.Fatal("a page with no row says that more follow")
		}
		req.HasAfter, req.After = true, []byte(page[len(page)-1].Key)
	}
}

func (c *client) object(hash [32]byte) (found bool, data []byte, err error) {
	err = c.do(dbwire.KindObject, func(e *dbwire.Enc) { e.ObjectRequest(hash) }, func(d *dbwire.Dec) (err error) {
		found, data, err = dbwire.ReadObjectReply(d, 1<<30)
		return err
	})
	return found, data, err
}

func (c *client) events() (events []dbwire.Event, err error) {
	err = c.do(dbwire.KindEvents, nil, func(d *dbwire.Dec) error {
		for {
			ev, ok := dbwire.ReadEvent(d, 1<<30)
			if !ok {
				return d.Err
			}
			events = append(events, ev)
		}
	})
	return events, err
}

func (c *client) verify() error {
	return c.do(dbwire.KindVerify, nil, dbwire.ReadEmpty)
}

func (c *client) putObject(data string) ([32]byte, error) {
	h := hashOf(data)
	return h, c.do(dbwire.KindPutObject, func(e *dbwire.Enc) { e.PutObjectRequest(h, []byte(data)) }, dbwire.ReadEmpty)
}

// commit sends a Commit. The first commit of a store also puts the schema row, which every commit must leave, unless ops
// have it, and an event for it: commitExactly sends the ops as they are.
func (c *client) commit(expected uint64, ops ...dbwire.Op) (seq uint64, mark time.Time, err error) {
	if expected == 0 && !slices.ContainsFunc(ops, func(o dbwire.Op) bool {
		return o.Table == dbwire.TableMeta && string(o.Key) == dbwire.MetaSchema
	}) {
		ops = append([]dbwire.Op{schemaOp()}, ops...)
		if !slices.ContainsFunc(ops, func(o dbwire.Op) bool { return o.Kind == dbwire.OpEvent }) {
			ops = append(ops, event("schema"))
		}
	}
	return c.commitExactly(expected, ops...)
}

func (c *client) commitExactly(expected uint64, ops ...dbwire.Op) (seq uint64, mark time.Time, err error) {
	err = c.do(dbwire.KindCommit, func(e *dbwire.Enc) {
		e.CommitRequest(expected)
		for _, o := range ops {
			e.CommitOp(o)
		}
		e.CommitEnd()
	}, func(d *dbwire.Dec) (err error) {
		seq, mark, err = dbwire.ReadCommitReply(d)
		return err
	})
	return seq, mark, err
}

// tryCommit takes a snapshot and commits ops with an audit event after them, which a commit that changes a table needs.
func (c *client) tryCommit(ops ...dbwire.Op) (uint64, time.Time, error) {
	seq, err := c.snapshot()
	if err != nil {
		return 0, time.Time{}, err
	}
	got, mark, err := c.commit(seq, append(ops, event("test"))...)
	if err == nil && got != seq+1 {
		err = fmt.Errorf("Commit answered %d after %d", got, seq)
	}
	return got, mark, err
}

func (c *client) mustCommit(ops ...dbwire.Op) (uint64, time.Time) {
	c.t.Helper()
	seq, mark, err := c.tryCommit(ops...)
	if err != nil {
		c.t.Fatalf("Commit: %v", err)
	}
	return seq, mark
}

func put(table uint32, key, value string) dbwire.Op {
	return dbwire.Op{Kind: dbwire.OpPut, Table: table, Key: []byte(key), Value: []byte(value)}
}

func del(table uint32, key string) dbwire.Op {
	return dbwire.Op{Kind: dbwire.OpDelete, Table: table, Key: []byte(key)}
}

func event(data string) dbwire.Op { return dbwire.Op{Kind: dbwire.OpEvent, Value: []byte(data)} }

// reply is the code and the text of an error reply.
type reply struct {
	Code dbwire.Code
	Msg  string
}

// wantCode fails the test unless err is the error reply with code.
func wantCode(t testing.TB, err error, code dbwire.Code) reply {
	t.Helper()
	var re *dbwire.RemoteError
	if !errors.As(err, &re) {
		t.Fatalf("got %v, want an error reply with the code %v", err, code)
	}
	if re.Code != code {
		t.Fatalf("got the code %v (%q), want %v", re.Code, re.Msg, code)
	}
	return reply{re.Code, re.Msg}
}

// wantClosed fails the test unless the server has closed the connection.
func (c *client) wantClosed() {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(waitLimit))
	if _, err := c.rd.Next(); err == nil {
		c.t.Fatal("the connection is still serving")
	} else if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.ErrUnexpectedEOF) {
		return
	} else {
		c.t.Fatalf("the connection was not closed: %v", err)
	}
}

// rawFrame is a frame as it is on the wire, whatever it holds.
func rawFrame(kind byte, flags byte, payload []byte) []byte {
	return slices.Concat([]byte{kind, flags, 0, 0}, binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload)
}

// readReply reads one reply of the server that is an error, after raw bytes were written.
func (c *client) readReply() error {
	_ = c.conn.SetReadDeadline(time.Now().Add(waitLimit))
	err := c.rd.Reply()
	c.f.record(err)
	return err
}

// writeRaw writes bytes to the connection from another goroutine. The channel is closed when it is done, or when the
// server has closed the connection.
func (c *client) writeRaw(b []byte) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.conn.Write(b)
	}()
	return done
}

func hello(version uint32, magic []byte) []byte {
	return rawFrame(byte(dbwire.KindHello), 1, binary.BigEndian.AppendUint32(slices.Clone(magic), version))
}

var helloBytes = hello(dbwire.Version, dbwire.Magic[:])

// waitSessions waits until the server has n sessions.
func (f *fixture) waitSessions(n int) {
	f.t.Helper()
	eventually(f.t, "the sessions to end", func() bool {
		f.srv.mu.Lock()
		defer f.srv.mu.Unlock()
		return len(f.srv.sessions) == n
	})
}

// schemaOp is the row that a store of this version has in its meta table.
func schemaOp() dbwire.Op {
	return dbwire.Op{Kind: dbwire.OpPut, Table: dbwire.TableMeta, Key: []byte(dbwire.MetaSchema),
		Value: binary.AppendUvarint(nil, dbwire.TableVersion)}
}

// objectPath is the path in the data directory of the file of the object with the data.
func objectPath(data string) string {
	h := hashOf(data)
	return fmt.Sprintf("logdb/objects/%x/%x", h[:1], h)
}

func hashHex(data string) string {
	h := hashOf(data)
	return fmt.Sprintf("%x", h[:])
}

// putOriginal sends data as the chunks of a PutOriginal, with the trailer that has its hash and size.
func (c *client) putOriginal(data []byte) ([32]byte, error) {
	h := sha256.Sum256(data)
	return h, c.putOriginalAs(data, h, int64(len(data)))
}

// putOriginalAs is putOriginal with the hash and the size of the trailer, which may be wrong.
func (c *client) putOriginalAs(data []byte, hash [32]byte, size int64) error {
	return c.do(dbwire.KindPutOriginal, func(e *dbwire.Enc) {
		ch := e.Chunks()
		_, _ = ch.Write(data)
		_ = ch.Close()
		e.PutOriginalTrailer(hash, size)
	}, dbwire.ReadEmpty)
}

// original reads the original whose hash is hash: its size, and its bytes.
func (c *client) original(hash [32]byte) (data []byte, err error) {
	err = c.do(dbwire.KindOriginal, func(e *dbwire.Enc) { e.OriginalRequest(hash) }, func(d *dbwire.Dec) error {
		size, err := dbwire.ReadOriginalReply(d, 1<<30)
		if err != nil {
			return err
		}
		if data, err = io.ReadAll(d.Stream(size)); err != nil {
			return err
		}
		return d.End()
	})
	return data, err
}

// A backupReply is what a Backup gave: the entries in order, as text, the position, the lines of the heads, and the files.
type backupReply struct {
	order []string
	pos   dbwire.BackupPosition
	heads []string
	dirs  []string
	files map[string][]byte
}

// readBackup reads the reply of a Backup that has been sent, to its end.
func (c *client) readBackup(into vfs.FS) (*backupReply, error) {
	r := &backupReply{files: map[string][]byte{}}
	if err := c.rd.Reply(); err != nil {
		c.f.record(err)
		return nil, err
	}
	err := r.read(&dbwire.Dec{R: c.rd}, into, nil)
	c.f.record(err)
	return r, err
}

// read reads the entries of d. If into is not nil, it makes them there, as a restore does. after is called after each entry.
func (r *backupReply) read(d *dbwire.Dec, into vfs.FS, after func(en dbwire.BackupEntry)) error {
	for {
		en, ok := dbwire.ReadBackupEntry(d)
		if !ok {
			if d.Err == nil {
				r.order = append(r.order, "end")
			}
			return d.Err
		}
		switch en.Kind {
		case dbwire.EntryEnd: // not given
		case dbwire.EntryPosition:
			r.pos = en.Position
			r.order = append(r.order, "position")
		case dbwire.EntryHead:
			r.heads = append(r.heads, en.Line)
			r.order = append(r.order, "head")
		case dbwire.EntryDir:
			r.dirs = append(r.dirs, en.Path)
			r.order = append(r.order, "dir "+en.Path)
			if into != nil {
				if err := into.Mkdir(en.Path, 0o700); err != nil {
					return err
				}
			}
		case dbwire.EntryFile:
			r.order = append(r.order, "file "+en.Path)
			var f vfs.File
			if into != nil {
				var err error
				if f, err = into.OpenFile(en.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
					return err
				}
			}
			var buf bytes.Buffer
			w := io.Writer(&buf)
			if f != nil {
				w = io.MultiWriter(&buf, f)
			}
			_, err := io.Copy(w, dbwire.NewChunkReader(d, 1<<40))
			if f != nil {
				err = errors.Join(err, f.Sync(), f.Close())
			}
			if err != nil {
				return err
			}
			r.files[en.Path] = buf.Bytes()
		}
		if after != nil {
			after(en)
		}
	}
}

// backup takes a Backup, and makes what it gives in into, if it is not nil.
func (c *client) backup(into vfs.FS) (*backupReply, error) {
	c.t.Helper()
	sent := c.send(dbwire.KindBackup, nil)
	r, err := c.readBackup(into)
	if serr := <-sent; err == nil && serr != nil {
		err = serr
	}
	return r, err
}

// keepAll is the crash model that keeps every change of fsys that is not durable: the machine restarts after its page
// cache has reached the disk.
func keepAll(fsys *simfs.FS) simfs.Model {
	keep := make([]bool, len(fsys.Pending()))
	for i := range keep {
		keep[i] = true
	}
	return simfs.Model{Name: "keep-all", Keep: keep}
}

var loseAll = simfs.Model{Name: "lose-all"}

// filesOf returns the paths of the files of fsys that are in the directory dir, with what they hold.
func filesOf(fsys *simfs.FS, dir string) map[string]string {
	m := map[string]string{}
	fsys.Walk(func(p string, data []byte) {
		if strings.HasPrefix(p, dir+"/") {
			m[p] = string(data)
		}
	})
	return m
}

// tempFiles returns the temporary files of PutOriginal in the files/ of fsys.
func tempFiles(fsys *simfs.FS) []string {
	var names []string
	for p := range filesOf(fsys, "files") {
		if strings.HasPrefix(path.Base(p), tempPrefix) {
			names = append(names, p)
		}
	}
	slices.Sort(names)
	return names
}

// originalPath is the path of the file of the original whose hash is h in the data directory.
func originalPath(h [32]byte) string { return fmt.Sprintf("files/%x/%x", h[:1], h) }
