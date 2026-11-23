package dbserver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

const (
	markSentence = "whiteroom-db stopped after a sync failed; restart the machine before starting it again"
	refusedTail  = ", and the machine has not restarted since; restart the machine before starting whiteroom-db on this directory again"
	unreadable   = "data/stopped: the mark of a failed sync has no boot ID that this program can read; restart the machine if it " +
		"has not restarted since the sync failed, and then remove the mark"
	leftText = "cannot leave the mark data/stopped (input/output error); do not start the daemon on data again before the machine restarts"
)

// stopKind is a failure of a sync (or of an undo) that stops the daemon: the request that fails, and the faults that it needs.
type stopKind struct {
	name     string
	faults   []simfs.Fault
	object   string // staged before the faults, long enough to be a file
	original bool   // the failing request is a PutOriginal
	open     bool   // the failing request is the Open of the engine
}

func stopKinds() []stopKind {
	onLog := func(kind simfs.OpKind) func(simfs.Op) bool {
		return func(op simfs.Op) bool { return op.Kind == kind && strings.HasPrefix(op.Path, "logdb/log/") }
	}
	drop := func(match func(simfs.Op) bool) []simfs.Fault {
		return []simfs.Fault{{Match: match, Err: syscall.EIO, Drop: true, Times: 1}}
	}
	return []stopKind{
		{name: "the sync of a record", faults: drop(onLog(simfs.OpSync))},
		{name: "the sync of an object file", object: strings.Repeat("an object in a file ", 50),
			faults: drop(func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/objects/") })},
		{name: "the undo of a record", faults: []simfs.Fault{
			{Match: onLog(simfs.OpWrite), Err: syscall.EIO, Times: 1},
			{Match: onLog(simfs.OpTruncate), Err: syscall.EIO, Times: 1},
		}},
		{name: "the sync of the open", open: true, faults: drop(onLog(simfs.OpSync))},
		{name: "the sync of the temporary file of an original", original: true,
			faults: drop(func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "files/.tmp-") })},
		{name: "the sync of files/", original: true,
			faults: drop(func(op simfs.Op) bool { return op.Kind == simfs.OpSyncDir && op.Path == "files" })},
		{name: "the sync of the directory of an original", original: true,
			faults: drop(func(op simfs.Op) bool { return op.Kind == simfs.OpSyncDir && strings.HasPrefix(op.Path, "files/") })},
	}
}

// openClosing is Open, and OpenDir if fsys is nil, for a test that expects it to fail: a server that opens is closed at the
// end of the test, so that the refusal that does not happen fails the test and leaves nothing running.
func openClosing(t testing.TB, f *fixture, o Options) (*Server, error) {
	t.Helper()
	var srv *Server
	var err error
	if f.fs == nil {
		srv, err = OpenDir(f.dir, o)
	} else {
		srv, err = Open(f.fs, "data", o)
	}
	if err == nil {
		t.Cleanup(func() { _ = srv.Close() })
	}
	return srv, err
}

// worldFile returns the content of the file name of the world fsys, and whether it is there.
func worldFile(fsys *simfs.FS, name string) (string, bool) {
	var text string
	var found bool
	fsys.Walk(func(p string, data []byte) {
		if p == name {
			text, found = string(data), true
		}
	})
	return text, found
}

// putMark writes text as the mark in the world fsys.
func putMark(t testing.TB, fsys *simfs.FS, text string) {
	t.Helper()
	f, err := fsys.OpenFile("stopped", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// markWith is a valid mark of the boot, with the lines after it.
func markWith(boot string, lines ...string) string {
	return markSentence + "\nboot: " + boot + "\n" + strings.Join(lines, "")
}

// markLine returns the value of the line of text that starts with key and ": ".
func markLine(text, key string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if v, ok := strings.CutPrefix(line, key+": "); ok {
			return v
		}
	}
	return ""
}

// restart is the machine restarting after a crash of f.fs of the model m, with the boot ID boot: a fixture, whose server is
// not open, on the world that the crash leaves.
func (f *fixture) restart(m simfs.Model, boot string) *fixture {
	f.t.Helper()
	g := &fixture{t: f.t, fs: f.fs.Crash(m), opts: f.opts, logs: &logLines{},
		clock: &clock{t: testBase.Add(time.Hour), step: time.Second}}
	g.opts.Boot = machineWith(boot)
	g.opts.Now = g.clock.now
	g.opts.Logf = g.logs.logf
	return g
}

func removalLine(at, cause string) string {
	return fmt.Sprintf("data: a sync failed at %s (%s) before the machine restarted; the mark data/stopped is removed: "+
		"check the store with whiteroom doctor", at, cause)
}

// TestRestartAfterSyncFailure checks the rule for each failure that stops the daemon (concept section 3.5): the daemon
// leaves the mark before it answers; a daemon cannot open the directory again in the same boot, which changes nothing; and
// after a restart of the machine, whatever the crash has kept, it opens, removes the mark, and goes on, and what it
// writes stays after a further crash.
func TestRestartAfterSyncFailure(t *testing.T) {
	data := []byte("GIF89a")
	for _, tc := range stopKinds() {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			w := f.connect(dbwire.RoleWriter)
			w.mustCommit(schemaOp(), put(2, "a", "1"))
			seq := w.mustSnapshot()
			if tc.object != "" {
				if _, err := w.putObject(tc.object); err != nil {
					t.Fatal(err)
				}
			}
			if tc.open {
				if err := f.srv.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for _, flt := range tc.faults {
				f.fs.Inject(flt)
			}
			switch {
			case tc.open:
				srv, err := Open(f.fs, "data", f.opts)
				if err == nil {
					_ = srv.Close()
					t.Fatal("Open with a sync that fails succeeded")
				}
				if !errors.Is(err, logdb.ErrFailed) {
					t.Fatalf("Open = %v, want an error that wraps logdb.ErrFailed", err)
				}
			case tc.original:
				_, err := w.putOriginal(data)
				wantCode(t, err, dbwire.CodeStopped)
			default:
				_, _, err := w.commit(seq, put(2, "b", "2"+secret), event("e"+secret))
				wantCode(t, err, dbwire.CodeStopped)
			}

			// The mark is there when the answer is: a regular file with the boot, the time and the cause.
			info, err := f.fs.Lstat("stopped")
			if err != nil {
				t.Fatalf("no mark after the failure: %v", err)
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Errorf("the mark has the mode %v, want a regular file of 0600", info.Mode())
			}
			mark, _ := worldFile(f.fs, "stopped")
			at, cause := markLine(mark, "time"), markLine(mark, "cause")
			if when, err := time.Parse(time.RFC3339, at); err != nil || when.Before(testBase.Truncate(time.Second)) ||
				when.Location() != time.UTC {
				t.Errorf("the time of the mark is %q (%v), want RFC 3339 in UTC, not before %v", at, err, testBase)
			}
			if !strings.HasPrefix(mark, markSentence+"\n") || markLine(mark, "boot") != testBoot ||
				!strings.Contains(cause, "input/output error") || strings.Contains(mark, secret) || !strings.HasSuffix(mark, "\n") ||
				len(strings.Split(mark, "\n")) != 5 {
				t.Errorf("the mark is %q, want the sentence, the boot %s, the time and a cause with the error, and no secret", mark,
					testBoot)
			}

			if !tc.open {
				if err := f.srv.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			}
			counted := f.fs.Counted()
			if _, err := openClosing(t, f, f.opts); !errors.Is(err, ErrNotRestarted) ||
				err.Error() != "data: a sync failed at "+at+" ("+cause+")"+refusedTail {
				t.Fatalf("Open in the same boot = %v, want the refusal for the mark %q", err, mark)
			}
			if n := f.fs.Counted(); n != counted {
				t.Errorf("the refused Open made %d operations, want none", n-counted)
			}
			if again, _ := worldFile(f.fs, "stopped"); again != mark {
				t.Errorf("the refused Open changed the mark to %q", again)
			}

			for _, m := range []simfs.Model{keepAll(f.fs), loseAll} {
				t.Run(m.Name, func(t *testing.T) {
					g := f.restart(m, anotherBoot)
					g.open()
					if lines := g.logs.all(); len(lines) != 1 || lines[0] != removalLine(at, cause) {
						t.Errorf("log lines %q, want the removal of the mark", lines)
					}
					if _, ok := worldFile(g.fs, "stopped"); ok {
						t.Error("the mark is still there after the restart")
					}
					w2 := g.connect(dbwire.RoleWriter)
					w2.mustCommit(put(2, "c", "3"), event("e2"))
					var orig [32]byte
					if tc.original {
						orig = mustPut(t, w2, data)
					}
					if err := g.srv.Close(); err != nil {
						t.Fatalf("Close: %v", err)
					}

					crashed := g.fs.Crash(loseAll)
					h := &fixture{t: t, fs: crashed, opts: g.opts, logs: &logLines{}, clock: g.clock}
					h.opts.Boot = machineWith(thirdBoot)
					h.opts.Logf = h.logs.logf
					h.open()
					if err := EngineOf(h.srv).Verify(context.Background()); err != nil {
						t.Errorf("Verify after the second crash: %v", err)
					}
					r := h.connect(dbwire.RoleReader)
					r.mustSnapshot()
					for _, key := range []string{"a", "c"} {
						if found, _ := r.mustGet(2, key); !found {
							t.Errorf("the row %q is gone after the second crash", key)
						}
					}
					if tc.original {
						if got := filesOf(crashed, "files")[originalPath(orig)]; got != string(data) {
							t.Errorf("the original is not there after the second crash: %q", got)
						}
					}
				})
			}
		})
	}
}

// TestMarkRead checks which marks make Open refuse, with which text, and which it removes.
func TestMarkRead(t *testing.T) {
	const at, cause = "2026-11-15T09:29:00Z", "sync data/logdb/log/x.log: input/output error"
	const full = "data: a sync failed at " + at + " (" + cause + ")"
	valid := func(boot string) string { return markWith(boot, "time: "+at+"\n", "cause: "+cause+"\n") }
	type result int
	const (
		refuses result = iota
		opens
	)
	for _, tc := range []struct {
		name  string
		mark  string
		boot  string // of the daemon
		want  result
		error string // of a refusal
	}{
		{"this boot", valid(testBoot), testBoot, refuses, full + refusedTail},
		{"another boot", valid(testBoot), anotherBoot, opens, ""},
		{"unknown lines", markWith(testBoot, "future: 1\n", "time: "+at+"\n", "cause: "+cause+"\n", "other: x\n"), testBoot, refuses,
			full + refusedTail},
		{"unknown lines, another boot", markWith(testBoot, "future: 1\n", "time: "+at+"\n", "other: x\n"), anotherBoot, opens, ""},
		{"no time and no cause", markWith(testBoot), testBoot, refuses, "data: a sync failed" + refusedTail},
		{"a time that does not parse", markWith(testBoot, "time: yesterday\n", "cause: "+cause+"\n"), testBoot, refuses,
			"data: a sync failed (" + cause + ")" + refusedTail},
		{"a cause that is not printable", markWith(testBoot, "time: "+at+"\n", "cause: a\x1b[31mb\n"), testBoot, refuses,
			"data: a sync failed at " + at + refusedTail},
		{"without a final newline", "boot: " + testBoot, testBoot, refuses, "data: a sync failed" + refusedTail},
		{"as large as is read", valid(testBoot) + strings.Repeat("x", maxMark-len(valid(testBoot))), anotherBoot, opens, ""},
		{"empty", "", testBoot, refuses, unreadable},
		{"no boot line", markSentence + "\ntime: " + at + "\n", testBoot, refuses, unreadable},
		{"an upper case ID", markWith("0000000A-0000-4000-8000-00000000000B"), testBoot, refuses, unreadable},
		{"a short ID", markWith(testBoot[:35]), testBoot, refuses, unreadable},
		{"an ID with a letter that is not hex", markWith(testBoot[:35] + "g"), testBoot, refuses, unreadable},
		{"an ID with a missing hyphen", markWith(strings.Replace(testBoot, "-", "0", 1)), testBoot, refuses, unreadable},
		{"too large", valid(testBoot) + strings.Repeat("x", maxMark+1-len(valid(testBoot))), anotherBoot, refuses, unreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			if err := f.srv.Close(); err != nil {
				t.Fatal(err)
			}
			putMark(t, f.fs, tc.mark)
			counted := f.fs.Counted()
			o := f.opts
			o.Boot = machineWith(tc.boot)
			srv, err := Open(f.fs, "data", o)
			if tc.want == opens {
				if err != nil {
					t.Fatalf("Open = %v, want it opened", err)
				}
				t.Cleanup(func() { _ = srv.Close() })
				if _, ok := worldFile(f.fs, "stopped"); ok {
					t.Error("the mark of an earlier boot is still there")
				}
				if lines := f.logs.all(); len(lines) != 1 || !strings.Contains(lines[0], "before the machine restarted") {
					t.Errorf("log lines %q, want the one of the removal", lines)
				}
				return
			}
			if err == nil {
				_ = srv.Close()
				t.Fatal("Open succeeded, want a refusal")
			}
			if err.Error() != tc.error {
				t.Errorf("Open = %q, want %q", err, tc.error)
			}
			if errors.Is(err, ErrNotRestarted) != strings.HasSuffix(tc.error, refusedTail) {
				t.Errorf("Open = %v, wraps ErrNotRestarted: %v", err, errors.Is(err, ErrNotRestarted))
			}
			if n := f.fs.Counted(); n != counted {
				t.Errorf("the refusal made %d operations, want none", n-counted)
			}
			if got, _ := worldFile(f.fs, "stopped"); got != tc.mark {
				t.Errorf("the refusal changed the mark to %q", got)
			}
		})
	}

	t.Run("a directory", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.fs.Mkdir("stopped", 0o700); err != nil {
			t.Fatal(err)
		}
		counted := f.fs.Counted()
		if _, err := openClosing(t, f, f.opts); err == nil || err.Error() != unreadable {
			t.Errorf("Open = %v, want %q", err, unreadable)
		}
		if n := f.fs.Counted(); n != counted {
			t.Errorf("the refusal made %d operations, want none", n-counted)
		}
	})

	// The mark of an earlier boot is removed once the engine has opened: it stays if the engine does not.
	t.Run("an earlier boot and an engine that does not open", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		earlier := valid(testBoot)
		putMark(t, f.fs, earlier)
		f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpOpen && op.Path == "logdb/head" },
			Err: syscall.EIO, Times: 1})
		o := f.opts
		o.Boot = machineWith(anotherBoot)
		if _, err := openClosing(t, f, o); err == nil {
			t.Fatal("Open succeeded although the engine could not open")
		}
		if got, _ := worldFile(f.fs, "stopped"); got != earlier {
			t.Errorf("the mark of an earlier boot is %q after an Open that failed, want it kept", got)
		}
		if lines := f.logs.all(); len(lines) != 0 {
			t.Errorf("log lines %q, want none", lines)
		}
		if _, err := openClosing(t, f, o); err != nil {
			t.Fatalf("Open once the engine opens = %v", err)
		}
		if _, ok := worldFile(f.fs, "stopped"); ok {
			t.Error("the mark of an earlier boot is still there after the Open that succeeded")
		}
	})

	t.Run("a symbolic link", func(t *testing.T) {
		f := newDirFixture(t, nil)
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(f.dir, "elsewhere")
		if err := os.WriteFile(target, []byte(valid(anotherBoot)), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(f.dir, "stopped")
		if err := os.Symlink("elsewhere", link); err != nil {
			t.Skipf("no symbolic links: %v", err)
		}
		_, err := openClosing(t, f, f.opts)
		if want := f.dir + "/stopped: the mark of a failed sync has no boot ID that this program can read; restart the " +
			"machine if it has not restarted since the sync failed, and then remove the mark"; err == nil || err.Error() != want {
			t.Errorf("OpenDir = %v, want %q", err, want)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Errorf("the refusal removed the link: %v", err)
		}
	})

	t.Run("on the OS", func(t *testing.T) {
		f := newDirFixture(t, nil)
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		mark := filepath.Join(f.dir, "stopped")
		if err := os.WriteFile(mark, []byte(valid(testBoot)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openClosing(t, f, f.opts); !errors.Is(err, ErrNotRestarted) {
			t.Fatalf("OpenDir in the same boot = %v, want ErrNotRestarted", err)
		}
		o := f.opts
		o.Boot = machineWith(anotherBoot)
		srv, err := OpenDir(f.dir, o)
		if err != nil {
			t.Fatalf("OpenDir in another boot = %v", err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		if _, err := os.Lstat(mark); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the mark is still there: %v", err)
		}
	})
}

// TestMachineBoot checks where the daemon has the boot ID of the machine from.
func TestMachineBoot(t *testing.T) {
	t.Run("the kernel", func(t *testing.T) {
		b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			t.Skipf("no boot ID: %v", err)
		}
		want := strings.TrimSuffix(string(b), "\n")
		got, err := machineBoot()
		if err != nil || got != want || !validBoot(got) {
			t.Errorf("machineBoot() = %q, %v; want %q, a valid ID", got, err, want)
		}

		// Without Options.Boot, the mark has it.
		f := newFixture(t, func(o *Options) { o.Boot = nil })
		f.srv.stop(logdb.ErrFailed)
		mark, _ := worldFile(f.fs, "stopped")
		if markLine(mark, "boot") != want {
			t.Errorf("the mark is %q, want the boot %s", mark, want)
		}
	})

	failures := []struct {
		name string
		boot func() (string, error)
		want string
	}{
		{"an error", func() (string, error) { return "", errors.New("no boot ID here") },
			"cannot read the boot ID of the machine: no boot ID here; whiteroom-db needs it to know whether the machine has " +
				"restarted since a sync failed"},
		{"an ID that is not valid", machineWith("not-a-uuid"), `the boot ID of the machine is not valid: "not-a-uuid"`},
		{"an empty ID", machineWith(""), `the boot ID of the machine is not valid: ""`},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			if err := f.srv.Close(); err != nil {
				t.Fatal(err)
			}
			counted := f.fs.Counted()
			o := f.opts
			o.Boot = tc.boot
			if _, err := openClosing(t, f, o); err == nil || err.Error() != tc.want {
				t.Errorf("Open = %v, want %q", err, tc.want)
			}
			if n := f.fs.Counted(); n != counted {
				t.Errorf("the refusal made %d operations, want none", n-counted)
			}
		})
	}
}

// TestMarkWriteFails checks that a mark that cannot be written does not keep the daemon from stopping: it refuses the
// writes, serves the reads, and logs that the mark is not there; a mark that the page cache has still keeps the next
// daemon of the same boot out.
func TestMarkWriteFails(t *testing.T) {
	onMark := func(kind simfs.OpKind) func(simfs.Op) bool {
		return func(op simfs.Op) bool { return op.Kind == kind && op.Path == "stopped" }
	}
	for _, tc := range []struct {
		name    string
		match   func(simfs.Op) bool
		drop    bool
		refused string // what an Open of the same world in the same boot says; "": it opens
		// dirSyncs is how many times the stop syncs the data directory: it does so after a failed write or sync of the file,
		// because the name may still reach the disk.
		dirSyncs int32
	}{
		{"its create", onMark(simfs.OpCreate), false, "", 0},
		{"its write", onMark(simfs.OpWrite), false, unreadable, 1},
		{"its sync", onMark(simfs.OpSync), true, "mark", 1},
		{"the sync of the directory", func(op simfs.Op) bool { return op.Kind == simfs.OpSyncDir && op.Path == "." }, true, "mark", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			w := f.connect(dbwire.RoleWriter)
			w.mustCommit(schemaOp(), put(2, "a", "1"))
			seq := w.mustSnapshot()
			f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/") },
				Err: syscall.EIO, Drop: true, Times: 1})
			var dirSyncs atomic.Int32
			f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
				if op.Kind == simfs.OpSyncDir && op.Path == "." {
					dirSyncs.Add(1)
				}
				return false
			}, Err: syscall.EIO})
			f.fs.Inject(simfs.Fault{Match: tc.match, Err: syscall.EIO, Drop: tc.drop, Times: 1})
			_, _, err := w.commit(seq, put(2, "b", "2"+secret), event("e"+secret))
			wantCode(t, err, dbwire.CodeStopped)
			if n := dirSyncs.Load(); n != tc.dirSyncs {
				t.Errorf("the stop synced the data directory %d times, want %d", n, tc.dirSyncs)
			}

			seq = w.mustSnapshot()
			_, _, err = w.commit(seq, put(2, "c", "3"), event("e"))
			wantCode(t, err, dbwire.CodeStopped)
			_, err = w.putObject("x")
			wantCode(t, err, dbwire.CodeStopped)
			_, err = w.putOriginal([]byte("GIF89a"))
			wantCode(t, err, dbwire.CodeStopped)
			r := f.connect(dbwire.RoleReader)
			if !r.hello.Stopped {
				t.Error("Hello does not say that the store has stopped")
			}
			r.mustSnapshot()
			if found, v := r.mustGet(2, "a"); !found || v != "1" {
				t.Errorf("a read after the stop: %v %q", found, v)
			}
			lines := f.logs.all()
			if len(lines) != 2 || !strings.HasPrefix(lines[0], "the store has stopped after a sync failed (") || lines[1] != leftText {
				t.Errorf("log lines %q, want the stop and then %q", lines, leftText)
			}

			if err := f.srv.Close(); err != nil {
				t.Fatal(err)
			}
			srv, err := Open(f.fs, "data", f.opts)
			switch tc.refused {
			case "":
				if err != nil {
					t.Errorf("Open after the failed create = %v", err)
				} else {
					_ = srv.Close()
				}
			case "mark":
				if !errors.Is(err, ErrNotRestarted) {
					t.Errorf("Open in the same boot = %v, want ErrNotRestarted: the page cache has the mark", err)
				}
			default:
				if err == nil || err.Error() != tc.refused {
					t.Errorf("Open in the same boot = %v, want %q", err, tc.refused)
				}
			}
		})
	}

	t.Run("the open", func(t *testing.T) {
		f := newFixture(t, nil)
		w := f.connect(dbwire.RoleWriter)
		w.mustCommit(schemaOp(), put(2, "a", "1"))
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/") },
			Err: syscall.EIO, Drop: true, Times: 1})
		f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpCreate && op.Path == "stopped" },
			Err: syscall.EIO, Times: 1})
		_, err := openClosing(t, f, f.opts)
		if !errors.Is(err, logdb.ErrFailed) || err == nil || !strings.Contains(err.Error(), "input/output error") ||
			!strings.HasSuffix(err.Error(), "; "+leftText) {
			t.Errorf("Open = %v, want an error of the engine that wraps logdb.ErrFailed, and then %q", err, leftText)
		}
	})
}

// TestStopMarkOnce checks that calls of stop at the same time write the mark once and log the stop once, and that each of
// them returns when the mark is there.
func TestStopMarkOnce(t *testing.T) {
	t.Run("at the same time", func(t *testing.T) {
		f := newFixture(t, nil)
		var creates int
		var mu sync.Mutex
		f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
			if op.Kind == simfs.OpCreate && op.Path == "stopped" {
				mu.Lock()
				creates++
				mu.Unlock()
			}
			return false
		}, Err: syscall.EIO})
		cause := &fs.PathError{Op: "sync", Path: "data/logdb/log/x.log", Err: syscall.EIO}
		start := make(chan struct{})
		var wg sync.WaitGroup
		marks := make([]string, 8)
		for i := range marks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				f.srv.stop(fmt.Errorf("%w: %w", logdb.ErrFailed, cause))
				marks[i], _ = worldFile(f.fs, "stopped")
			}()
		}
		close(start)
		wg.Wait()
		mu.Lock()
		n := creates
		mu.Unlock()
		if n != 1 {
			t.Errorf("the mark was created %d times, want once", n)
		}
		for i, m := range marks {
			if markLine(m, "boot") != testBoot || markLine(m, "time") == "" || markLine(m, "cause") != cause.Error() ||
				!strings.HasSuffix(m, "\n") {
				t.Errorf("stop %d returned when the mark was %q", i, m)
			}
		}
		if lines := f.logs.all(); len(lines) != 1 || !strings.Contains(lines[0], "has stopped") {
			t.Errorf("log lines %q, want the stop once", lines)
		}
	})

	t.Run("a cause that is not known", func(t *testing.T) {
		f := newFixture(t, nil)
		f.srv.stop(fmt.Errorf("%w: %w", logdb.ErrFailed, errors.New(secret)))
		if mark, _ := worldFile(f.fs, "stopped"); markLine(mark, "cause") != "the engine failed" || strings.Contains(mark, secret) {
			t.Errorf("the mark is %q, want the cause %q", mark, "the engine failed")
		}
	})

	// A cause as long as a path of the longest kind: the cut keeps the mark small enough to be read.
	t.Run("a long cause", func(t *testing.T) {
		f := newFixture(t, nil)
		long := "x/" + strings.Repeat("d", 4000) + "/logdb/log/y.log"
		f.srv.stop(fmt.Errorf("%w: %w", logdb.ErrFailed, &fs.PathError{Op: "sync", Path: long, Err: syscall.EIO}))
		mark, _ := worldFile(f.fs, "stopped")
		if cause := markLine(mark, "cause"); len(mark) > maxMark || len(cause) == 0 || len(cause) > maxCause ||
			!strings.HasSuffix(mark, "\n") {
			t.Errorf("the mark has %d bytes and a cause of %d, want at most %d and at most %d", len(mark), len(cause), maxMark,
				maxCause)
		}
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := openClosing(t, f, f.opts); !errors.Is(err, ErrNotRestarted) {
			t.Fatalf("Open in the same boot = %v, want ErrNotRestarted", err)
		}
		o := f.opts
		o.Boot = machineWith(anotherBoot)
		srv, err := Open(f.fs, "data", o)
		if err != nil {
			t.Fatalf("Open in another boot = %v", err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		if _, ok := worldFile(f.fs, "stopped"); ok {
			t.Error("the mark is still there after the restart")
		}
	})
}

// TestStopAfterEarlierMark checks that a stop leaves its mark although the mark of an earlier boot, which the start could not
// remove, is still there: the mark is made again, with the boot of this machine, and keeps the next daemon out.
func TestStopAfterEarlierMark(t *testing.T) {
	f := newFixture(t, nil)
	f.connect(dbwire.RoleWriter).mustCommit(schemaOp(), put(2, "a", "1"))
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	earlier := markWith(anotherBoot, "time: 2026-11-15T09:29:00Z\n", "cause: old\n")
	putMark(t, f.fs, earlier)
	f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpRemove && op.Path == "stopped" },
		Err: syscall.EACCES, Times: 1})
	f.open()
	if lines := f.logs.all(); len(lines) != 1 || lines[0] != "cannot remove the mark data/stopped: permission denied" {
		t.Errorf("log lines %q, want the one of the removal that failed", lines)
	}
	if got, _ := worldFile(f.fs, "stopped"); got != earlier {
		t.Fatalf("the mark of the earlier boot is %q after the start, want it kept", got)
	}
	f.srv.stop(fmt.Errorf("%w: %w", logdb.ErrFailed, &fs.PathError{Op: "sync", Path: "data/logdb/log/x.log", Err: syscall.EIO}))
	if mark, _ := worldFile(f.fs, "stopped"); markLine(mark, "boot") != testBoot || strings.Contains(mark, "old") {
		t.Errorf("the mark is %q after the stop, want one of the boot %s that replaces the earlier one", mark, testBoot)
	}
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openClosing(t, f, f.opts); !errors.Is(err, ErrNotRestarted) {
		t.Fatalf("Open in the same boot after the stop = %v, want ErrNotRestarted", err)
	}
}

// TestMarkAfterCheck checks the second look at the mark, after the engine has opened and has the lock: a daemon that
// stopped while this one waited for the lock has left its mark since the first look. Open refuses, and the engine writes
// nothing when it closes, not the checkpoint of the state that it has read from a log that the page cache may have had. The
// mark may be one that the stopping daemon has made and not yet written: it is empty, and refuses as well.
func TestMarkAfterCheck(t *testing.T) {
	for _, tc := range []struct {
		name, mark   string
		notRestarted bool
	}{
		{"a mark of this boot", markWith(testBoot, "time: 2026-11-15T09:29:00Z\n",
			"cause: sync data/logdb/log/x.log: input/output error\n"), true},
		{"a mark that is empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) { testMarkAfterCheck(t, tc.mark, tc.notRestarted) })
	}
}

func testMarkAfterCheck(t *testing.T, mark string, notRestarted bool) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	w.mustCommit(schemaOp(), put(2, "a", "1"))
	// A copy before the server closes: it has the commit in the log, and no checkpoint of it.
	world := f.fs.Crash(keepAll(f.fs))
	snapshot := func() map[string]string {
		files := filesOf(world, "logdb")
		delete(files, "logdb/head")
		return files
	}
	before := snapshot()
	// removeTemps would remove it.
	temp, err := world.OpenFile("files/"+tempPrefix+"0123456789abcdef", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	var written string
	o := f.opts
	o.Hook = func(point string) error {
		if point == "open:checked" {
			written = mark
			putMark(t, world, written)
		}
		return nil
	}
	srv, err := Open(world, "data", o)
	if err == nil {
		_ = srv.Close()
	}
	if err == nil || errors.Is(err, ErrNotRestarted) != notRestarted || (!notRestarted && err.Error() != unreadable) {
		t.Fatalf("Open = %v, want a refusal that wraps ErrNotRestarted: %v", err, notRestarted)
	}
	if after := snapshot(); !maps.Equal(before, after) {
		t.Errorf("the refused Open changed logdb/: before %v, after %v", slicesOf(before), slicesOf(after))
	}
	for p := range filesOf(world, "logdb") {
		if strings.HasPrefix(path.Base(p), ".tmp-") {
			t.Errorf("the refused Open left %s", p)
		}
	}
	if got, _ := worldFile(world, "stopped"); got != written {
		t.Errorf("the mark is %q, want it as it was written", got)
	}
	if got := tempFiles(world); len(got) != 1 {
		t.Errorf("the temporary files of files/ are %q after the refusal, want the one that was there", got)
	}
}

func slicesOf(m map[string]string) []string {
	var names []string
	for p, c := range m {
		names = append(names, fmt.Sprintf("%s(%d)", p, len(c)))
	}
	return names
}
