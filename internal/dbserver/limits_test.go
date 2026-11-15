package dbserver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

func TestSafeText(t *testing.T) {
	pe := &fs.PathError{Op: "sync", Path: "data/logdb/log/0000000000000001-0000000000000001.log", Err: syscall.EIO}
	le := &os.LinkError{Op: "rename", Old: "data/logdb/objects/ab/.tmp-1", New: "data/logdb/objects/ab/abcd", Err: syscall.EXDEV}
	ce := &logdb.CorruptError{Path: "data/logdb/log/x.log", Offset: 12, Reason: "bad CRC"}
	ve := &logdb.VersionError{Path: "data/logdb/head", Version: 9, Known: 1}
	objectText := "object " + strings.Repeat("ab", 32) + ": the data has the hash " + strings.Repeat("cd", 32)
	tooLarge := "the transaction is too large: 300000000 bytes (max 268435446)"
	differs := "data/logdb: the state in memory differs from the log"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nothing", nil, ""},
		{"a path error", pe, pe.Error()},
		{"a path error that something wraps", fmt.Errorf("key %q: %w", secret, pe), pe.Error()},
		{"a link error", le, le.Error()},
		{"a damaged file", ce, ce.Error()},
		{"a damaged file that something wraps", fmt.Errorf("%s: %w", secret, ce), ce.Error()},
		{"a file of a newer format", ve, ve.Error()},
		{"a sync that failed", fmt.Errorf("%w: %w", logdb.ErrFailed, pe), pe.Error()},
		{"a sync that failed, with a cause that is not known", fmt.Errorf("%w: %w", logdb.ErrFailed, errors.New(secret)), "the engine failed"},
		{"the sentence of an object that has not its hash", errors.New(objectText), objectText},
		{"the same sentence with something before it", errors.New(secret + " " + objectText), "the engine failed"},
		{"the same sentence with something after it", errors.New(objectText + " " + secret), "the engine failed"},
		{"the size of a transaction", errors.New(tooLarge), tooLarge},
		{"a state that differs from the log", errors.New(differs + `: table 2, key "` + secret + `": "` + secret + `" in the log`),
			differs + " (the detail is not sent: it may hold values of rows)"},
		{"a state that differs, a difference of the objects", errors.New(differs + ": object ab: at 4 of segment 1"),
			differs + " (the detail is not sent: it may hold values of rows)"},
		{"an error that nothing knows", errors.New("table 2, key " + secret), "the engine failed"},
		{"a joined error", errors.Join(errors.New(secret), pe), pe.Error()},
		{"the error of a failed sync alone", logdb.ErrFailed, "the engine failed"},
		{"the error of a context", fmt.Errorf("%s: %w", secret, errors.ErrUnsupported), "the engine failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := safeText(tc.err)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if strings.Contains(got, secret) {
				t.Errorf("the text holds the secret: %q", got)
			}
		})
	}
}

// TestNoValuesLeak: the keys, the values, the objects and the events that the peer sends hold a marker, and no reply
// of a refusal and no line of the log may hold it.
func TestNoValuesLeak(t *testing.T) {
	var freeFail, hookFail atomic.Bool
	f := newFixture(t, func(o *Options) {
		o.Limits.Commit, o.Limits.Object, o.Limits.Staged, o.Limits.MinFree = 3000, 3000, 4000, 1
		o.FreeSpace = func() (uint64, error) {
			if freeFail.Load() {
				return 0, errors.New("no free space of " + secret)
			}
			return 1 << 40, nil
		}
		o.Hook = func(point string) error {
			if point == "commit:gate" && hookFail.Load() {
				return errors.New("a hook with " + secret)
			}
			return nil
		}
	})
	big := secret + strings.Repeat("-", 600) // in a file, as an object
	f.update(func(tx *logdb.Tx) {
		_ = tx.Put(2, []byte("k"+secret), []byte("v"+secret))
		_ = tx.Put(dbwire.TableHistory, []byte("h"+secret), []byte("old"+secret))
		_ = tx.AppendEvent([]byte("event " + secret))
	})
	w := f.connect(dbwire.RoleWriter)
	r := f.connect(dbwire.RoleReader)
	seq := w.mustSnapshot()
	if _, err := w.putObject(big); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.commit(seq, put(2, "k"+secret, "v"+secret), event("e"+secret)); err != nil {
		t.Fatal(err)
	}
	refused := 0
	refuse := func(err error) {
		t.Helper()
		var re *dbwire.RemoteError
		if !errors.As(err, &re) {
			t.Fatalf("got %v, want an error reply", err)
		}
		refused++
	}

	// Rules, in a writer that goes on.
	for _, ops := range [][]dbwire.Op{
		{put(2, "x"+secret, "y"+secret)},
		{del(2, "k"+secret)},
		{event("\x00" + secret), put(2, "x"+secret, secret)},
		{put(3, "h"+secret, "new"+secret), event("e" + secret)},
		{put(3, "n"+secret, "a"), put(3, "n"+secret, "b"), event("e" + secret)},
		{del(3, "h"+secret), event("e" + secret)},
		{put(11, "bad key "+secret, "line "+secret), event("e" + secret)},
		{put(11, "activity", "line \x01"+secret), event("e" + secret)},
		{del(11, secret), event("e" + secret)},
		{put(0, secret, secret), event("e" + secret)},
		{},
	} {
		seq := w.mustSnapshot()
		if len(ops) > 0 {
			if _, err := w.putObject("staged " + secret); err != nil {
				t.Fatal(err)
			}
		}
		_, _, err := w.commit(seq, ops...)
		refuse(err)
	}
	// A sequence number that is not the last one.
	seq = w.mustSnapshot()
	_, _, err := w.commit(seq+3, put(2, "x"+secret, secret), event("e"+secret))
	refuse(err)
	// A hash that is not the one of the data.
	w.mustSnapshot()
	refuse(w.do(dbwire.KindPutObject, func(e *dbwire.Enc) { e.PutObjectRequest(hashOf("other"), []byte(secret)) }, nil))
	// The free space, and a hook, that fail.
	freeFail.Store(true)
	_, err = w.putObject(secret)
	refuse(err)
	freeFail.Store(false)
	hookFail.Store(true)
	_, _, err = w.tryCommit(put(2, "x"+secret, secret))
	refuse(err)
	hookFail.Store(false)
	// Reads without a snapshot, with the marker in what they ask for.
	_, _, err = r.get(2, "k"+secret)
	refuse(err)
	_, _, err = r.scan(dbwire.ScanRequest{Table: 2, Prefix: []byte(secret)})
	refuse(err)
	_, _, err = r.object(hashOf(secret))
	refuse(err)
	// A reader that sends what only the writer sends.
	r.mustSnapshot()
	_, err = r.putObject(secret)
	refuse(err)
	_, _, err = r.commit(1, put(2, "x"+secret, secret), event("e"+secret))
	refuse(err)
	refuse(r.do(dbwire.KindPutOriginal, func(e *dbwire.Enc) {
		c := e.Chunks()
		_, _ = c.Write([]byte(secret))
		_ = c.Close()
		e.PutOriginalTrailer(hashOf(secret), int64(len(secret)))
	}, nil))
	refuse(r.do(dbwire.KindBackup, nil, nil))
	refuse(r.do(dbwire.KindOriginal, func(e *dbwire.Enc) { e.OriginalRequest(hashOf(secret)) }, nil))

	// What the peer sends that is not a message, each in a connection that ends after it.
	raw := func(role dbwire.Role, hello bool, b []byte) {
		t.Helper()
		var c *client
		if hello {
			c = f.connect(role)
		} else {
			c = f.dial(role)
		}
		done := c.writeRaw(b)
		refuse(c.readReply())
		<-done
		c.wantClosed()
	}
	long := []byte(strings.Repeat(secret, 300))
	raw(dbwire.RoleReader, false, hello(7, []byte("NOPE")))
	raw(dbwire.RoleReader, false, hello(7, dbwire.Magic[:]))
	raw(dbwire.RoleReader, false, rawFrame(byte(dbwire.KindHello), 1, append(binary.BigEndian.AppendUint32(append([]byte{}, dbwire.Magic[:]...), 1), secret...)))
	raw(dbwire.RoleReader, false, rawFrame(byte(dbwire.KindGet), 1, []byte(secret)))
	raw(dbwire.RoleReader, true, rawFrame(0x42, 1, []byte(secret)))
	raw(dbwire.RoleReader, true, rawFrame(byte(dbwire.KindGet), 0x80, []byte(secret)))
	raw(dbwire.RoleReader, true, rawFrame(byte(dbwire.KindGet), 1, append([]byte{0, 0, 0, 2, 0, 0, 0, 1, 'k', 0}, secret...)))
	raw(dbwire.RoleReader, true, rawFrame(byte(dbwire.KindOK), 1, []byte(secret)))
	raw(dbwire.RoleReader, true, rawFrame(byte(dbwire.KindScan), 1, append([]byte{0, 0, 0, 2, 0, 0, 0, 0, 9, 0, 0, 0, 0, 0, 0, 0, 0}, secret...)))
	raw(dbwire.RoleReader, true, append([]byte{byte(dbwire.KindGet), 1, 0, 0, 0xff, 0xff, 0xff, 0xff}, long...))
	raw(dbwire.RoleReader, true, rawFrame(byte(dbwire.KindGet), 1, binary.BigEndian.AppendUint32([]byte{0, 0, 0, 2}, dbwire.MaxKey+1)))
	// A request over its limit, and a size or a claim over it, in a writer.
	big4k := strings.Repeat(secret, 400)
	for _, drop := range []func(){
		func() { // ops over the limit of a Commit
			w := f.connectAfter(w)
			seq := w.mustSnapshot()
			_, _, err := w.commit(seq, event(big4k), event(big4k))
			refuse(err)
			w.wantClosed()
		},
		func() { // an object over the limit
			w := f.connectAfter(w)
			w.mustSnapshot()
			_, err := w.putObject(big4k)
			refuse(err)
			w.wantClosed()
		},
		func() { // a key over the limit
			w := f.connectAfter(w)
			seq := w.mustSnapshot()
			_, _, err := w.commit(seq, put(2, strings.Repeat(secret, 300), "v"), event("e"))
			refuse(err)
			w.wantClosed()
		},
	} {
		drop()
	}
	// Reads that fail: a file of an object that is damaged.
	w2 := f.connectAfter(w)
	w2.mustSnapshot()
	file, err := f.fs.OpenFile(objectPath(big), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(20); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	_, _, err = w2.object(hashOf(big))
	refuse(err)
	refuse(w2.verify())

	// The sync of the log fails, with values in the commit.
	seq = w2.mustSnapshot()
	f.fs.Inject(simfs.Fault{
		Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/") },
		Err:   syscall.EIO, Times: 1,
	})
	_, _, err = w2.commit(seq, put(2, "x"+secret, secret), event("e"+secret))
	refuse(err)
	_, err = w2.putObject(secret)
	refuse(err)

	if refused < 40 {
		t.Fatalf("only %d refusals ran", refused)
	}
	texts := f.errorTexts()
	if len(texts) < refused {
		t.Errorf("%d error texts for %d refusals", len(texts), refused)
	}
	for _, text := range texts {
		if strings.Contains(text, secret) || strings.Contains(text, secret[:12]) {
			t.Errorf("a reply holds the marker: %q", text)
		}
	}
	lines := f.logs.all()
	if len(lines) == 0 {
		t.Error("the log has no line: the stop and the failure of the free space are logged")
	}
	for _, line := range lines {
		if strings.Contains(line, secret) {
			t.Errorf("a log line holds the marker: %q", line)
		}
	}
}
