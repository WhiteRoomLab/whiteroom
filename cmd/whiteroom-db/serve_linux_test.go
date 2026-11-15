//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

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

// dirs are the directories of a daemon: a data directory that init has made and the directory of the sockets, which are
// short enough for the paths of sockets.
type dirs struct {
	root, data, socks string
}

func newDirs(t *testing.T) dirs {
	t.Helper()
	root, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	d := dirs{root: root, data: filepath.Join(root, "data"), socks: filepath.Join(root, "s")}
	if n := len(filepath.Join(d.socks, dbwire.WriterSocket)); n > 107 {
		t.Fatalf("the socket path of the tests has %d bytes, over the 107 that a socket path may have", n)
	}
	if err := os.Mkdir(d.socks, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCmd(t, "init", "-data", d.data); code != 0 {
		t.Fatalf("whiteroom-db init = %d, with stderr %q", code, stderr)
	}
	return d
}

// args are the flags that name the directories, and lower the free space that the daemon keeps to what a test disk has.
func (d dirs) args(more ...string) []string {
	return append([]string{"-data", d.data, "-db", d.socks, "-min-free", "1"}, more...)
}

// daemon is a whiteroom-db serve in a goroutine of the test.
type daemon struct {
	t      *testing.T
	dirs   dirs
	stdout *syncBuffer
	stderr *syncBuffer
	ticks  chan time.Time
	cancel context.CancelFunc
	done   chan int
	closed bool
}

// startServe runs whiteroom-db serve with args once it has said that it serves, and stops it at the end of the test.
func (d dirs) startServe(t *testing.T, args ...string) *daemon {
	t.Helper()
	s := &daemon{t: t, dirs: d, stdout: &syncBuffer{}, stderr: &syncBuffer{}, ticks: make(chan time.Time), done: make(chan int, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() { s.done <- run(ctx, append([]string{"serve"}, args...), testEnv(s.stdout, s.stderr, s.ticks)) }()
	t.Cleanup(func() {
		if !s.closed {
			s.stop()
		}
	})
	for deadline := time.Now().Add(waitLimit); !strings.Contains(s.stdout.String(), "\n"); time.Sleep(time.Millisecond) {
		select {
		case code := <-s.done:
			s.closed = true
			cancel()
			t.Fatalf("whiteroom-db serve ended with %d before it served, with stderr:\n%s", code, s.stderr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("whiteroom-db serve has not said that it serves, with stderr:\n%s", s.stderr)
		}
	}
	return s
}

// stop ends the context of the command and returns its exit code.
func (s *daemon) stop() int {
	s.t.Helper()
	s.closed = true
	s.cancel()
	select {
	case code := <-s.done:
		return code
	case <-time.After(waitLimit):
		s.t.Fatalf("whiteroom-db serve does not end, with stderr:\n%s", s.stderr)
		return -1
	}
}

// tick gives the daemon a minute.
func (s *daemon) tick() {
	s.t.Helper()
	select {
	case s.ticks <- time.Time{}:
	case <-time.After(waitLimit):
		s.t.Fatal("the daemon does not take a tick")
	}
}

// startLine is what serve says when it serves, for the user uid.
func (d dirs) startLine(uid int) string {
	return fmt.Sprintf("serving the data directory %s on the sockets in %s (writer: user %d)\n", d.data, d.socks, uid)
}

func openClient(t testing.TB, socks string, role dbwire.Role, o dbclient.Options) *dbclient.Client {
	t.Helper()
	c, err := dbclient.Open(t.Context(), dbclient.SocketDir(socks), role, o)
	if err != nil {
		t.Fatalf("Open(%s, %v) = %v", socks, role, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type row struct {
	table      uint32
	key, value string
}

// commit commits the rows and an event through the writer client c. The first commit of a store also puts the schema row,
// which the daemon requires.
func commit(ctx context.Context, c *dbclient.Client, rows ...row) error {
	return c.Update(ctx, func(tx *dbclient.Tx) error {
		if _, found, err := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema)); err != nil {
			return err
		} else if !found {
			if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion)); err != nil {
				return err
			}
		}
		for _, r := range rows {
			if err := tx.Put(r.table, []byte(r.key), []byte(r.value)); err != nil {
				return err
			}
		}
		return tx.AppendEvent([]byte("test"))
	})
}

func mustCommit(t testing.TB, c *dbclient.Client, rows ...row) {
	t.Helper()
	if err := commit(t.Context(), c, rows...); err != nil {
		t.Fatalf("Commit() = %v", err)
	}
}

// value returns the value of the row key of the table 2 that the reader client c sees.
func value(t testing.TB, c *dbclient.Client, key string) (string, bool) {
	t.Helper()
	var v []byte
	var found bool
	if err := c.View(t.Context(), func(tx *dbclient.Tx) (err error) {
		v, found, err = tx.Get(2, []byte(key))
		return err
	}); err != nil {
		t.Fatalf("View() = %v", err)
	}
	return string(v), found
}

func headLine(n int) string { return fmt.Sprintf("audit head activity %d sha256:00", n) }

func lines(s *syncBuffer) []string {
	text := s.String()
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func TestServe(t *testing.T) {
	d := newDirs(t)
	s := d.startServe(t, d.args()...)
	if got, want := s.stdout.String(), d.startLine(os.Getuid()); got != want {
		t.Errorf("the start line is %q, want %q", got, want)
	}
	for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		info, err := os.Lstat(filepath.Join(d.socks, name))
		if err != nil || info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o660 {
			t.Errorf("%s is %v (%v), want a socket with the mode 0660", name, info, err)
		}
	}

	// The writer commits, and a reader reads it, and cannot write.
	w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
	r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
	mustCommit(t, w, row{2, "a", "1"}, row{dbwire.TableHeads, "activity", headLine(1)})
	if v, found := value(t, r, "a"); !found || v != "1" {
		t.Errorf("the reader reads %q, %v, want 1", v, found)
	}
	if err := r.Update(t.Context(), func(tx *dbclient.Tx) error { return tx.Put(2, []byte("x"), []byte("y")) }); !errors.Is(err, dbclient.ErrReadOnly) {
		t.Errorf("a write through the reader socket = %v, want %v", err, dbclient.ErrReadOnly)
	}

	// The heads go to the standard error as they are, at a tick, once, and the start line is all that stdout has.
	if got := lines(s.stderr); len(got) != 0 {
		t.Errorf("stderr before the first tick has %q", got)
	}
	s.tick()
	eventually(t, "the head at the first tick", func() bool { return slices.Equal(lines(s.stderr), []string{headLine(1)}) })
	s.tick()
	mustCommit(t, w, row{2, "b", "2"}, row{dbwire.TableHeads, "activity", headLine(2)})
	s.tick()
	eventually(t, "the head at the second tick", func() bool { return slices.Equal(lines(s.stderr), []string{headLine(1), headLine(2)}) })

	// A head that no tick has seen is written at the stop, and the stop is a success that removes the sockets.
	mustCommit(t, w, row{dbwire.TableHeads, "activity", headLine(3)})
	if code := s.stop(); code != 0 {
		t.Fatalf("whiteroom-db serve = %d after the end of its context, with stderr:\n%s", code, s.stderr)
	}
	if got, want := lines(s.stderr), []string{headLine(1), headLine(2), headLine(3)}; !slices.Equal(got, want) {
		t.Errorf("stderr after the stop is %q, want %q", got, want)
	}
	if got, want := s.stdout.String(), d.startLine(os.Getuid()); got != want {
		t.Errorf("stdout after the stop is %q, want only the start line", got)
	}
	if got, err := os.ReadDir(d.socks); err != nil || len(got) != 0 {
		t.Errorf("the socket directory after the stop has %v (%v), want it empty", got, err)
	}

	// Started again, it serves what was committed, and writes the heads at its start.
	s = d.startServe(t, d.args()...)
	eventually(t, "the head at the start", func() bool { return slices.Equal(lines(s.stderr), []string{headLine(3)}) })
	r2 := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
	if v, found := value(t, r2, "b"); !found || v != "2" {
		t.Errorf("the reader of the second daemon reads %q, %v, want 2", v, found)
	}
	if code := s.stop(); code != 0 {
		t.Errorf("the second whiteroom-db serve = %d after the end of its context", code)
	}
}

// TestServeLogLines checks that the log lines of the daemon go to the standard error with the date and the time, and have
// no row: the free space of a data directory that has moved cannot be read.
func TestServeLogLines(t *testing.T) {
	d := newDirs(t)
	s := d.startServe(t, d.args()...)
	w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
	mustCommit(t, w, row{2, "a", "secret-value-3f9c"})
	moved := d.data + ".moved"
	if err := os.Rename(d.data, moved); err != nil {
		t.Fatal(err)
	}
	err := commit(t.Context(), w, row{2, "b", "secret-value-3f9c"})
	var re *dbwire.RemoteError
	if !errors.As(err, &re) || re.Code != dbwire.CodeFailed {
		t.Errorf("a commit while the data directory cannot be found = %v, want the code %v", err, dbwire.CodeFailed)
	}
	logged := regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d cannot read the free space \(.*\)$`)
	eventually(t, "the log line", func() bool { return slices.ContainsFunc(lines(s.stderr), logged.MatchString) })
	if code := s.stop(); code != 0 {
		t.Errorf("whiteroom-db serve = %d", code)
	}
	if strings.Contains(s.stderr.String(), "secret-value") || strings.Contains(s.stdout.String(), "secret-value") {
		t.Errorf("the output has a value of a row:\n%s", s.stderr)
	}
}

func TestServeFailures(t *testing.T) {
	d := newDirs(t)
	before := files(t, d.data)

	// The directory of the sockets must exist; serve does not make it, and does not open the data directory before it knows.
	missing := filepath.Join(d.root, "nosuch")
	code, stdout, stderr := runCmd(t, "serve", "-data", d.data, "-db", missing)
	want := "whiteroom-db serve: " + missing + ": the directory of the sockets does not exist; make it first, which serve does not\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("serve with a missing directory of the sockets = %d, with stdout %q and stderr %q, want 1, nothing and %q",
			code, stdout, stderr, want)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("serve has made the directory of the sockets: %v", err)
	}
	file := writeFile(t, d.root, "file", "x")
	code, stdout, stderr = runCmd(t, "serve", "-data", d.data, "-db", file)
	want = "whiteroom-db serve: " + file + ": not a directory; the sockets are made in a directory that exists\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("serve with a file as the directory of the sockets = %d, with stdout %q and stderr %q, want 1, nothing and %q",
			code, stdout, stderr, want)
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("a failed serve changed the data directory:\n%v\nwas\n%v", got, before)
	}

	// A data directory that is not there, or not a store.
	gone := filepath.Join(d.root, "gone")
	code, stdout, stderr = runCmd(t, "serve", "-data", gone, "-db", d.socks)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom-db serve: ") || !strings.Contains(stderr, gone) {
		t.Errorf("serve of a missing data directory = %d, with stdout %q and stderr %q, want 1 and the error", code, stdout, stderr)
	}
	if _, err := os.Lstat(gone); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("serve has made the data directory: %v", err)
	}
	empty := filepath.Join(d.root, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCmd(t, "serve", "-data", empty, "-db", d.socks)
	if want := "whiteroom-db serve: " + filepath.Join(empty, "logdb") + ": missing directory\n"; code != 1 || stderr != want {
		t.Errorf("serve of an empty directory = %d, with stderr %q, want 1 and %q", code, stderr, want)
	}
	if got, err := os.ReadDir(d.socks); err != nil || len(got) != 0 {
		t.Errorf("the socket directory after the failures has %v (%v)", got, err)
	}

	// A second daemon on the data directory fails with the sentence of the engine, and the first goes on.
	s := d.startServe(t, d.args()...)
	other := filepath.Join(d.root, "s2")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCmd(t, "serve", "-data", d.data, "-db", other, "-min-free", "1")
	want = "whiteroom-db serve: " + filepath.Join(d.data, "logdb") + ": another process holds the writer lock\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("a second serve on the data directory = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, want)
	}
	if got, err := os.ReadDir(other); err != nil || len(got) != 0 {
		t.Errorf("the second serve has made %v (%v) in its directory of the sockets", got, err)
	}
	w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
	mustCommit(t, w, row{2, "a", "1"})
	if code := s.stop(); code != 0 {
		t.Errorf("the first whiteroom-db serve = %d", code)
	}
}

// socketGroup returns the group of the socket file name in dir.
func socketGroup(t *testing.T, dir, name string) int {
	t.Helper()
	info, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return int(info.Sys().(*syscall.Stat_t).Gid)
}

func TestServeUsers(t *testing.T) {
	uid := os.Getuid()
	userName, groupName := currentNames(t)
	gid := os.Getegid()
	other := -1
	if groups, err := os.Getgroups(); err == nil {
		for _, g := range groups {
			if g != gid {
				other = g
			}
		}
	}

	// The user of the process is the writer by default, and by its name and by its number.
	forms := [][]string{nil, {"-writer-user", strconv.Itoa(uid)}}
	if userName != "" {
		forms = append(forms, []string{"-writer-user", userName})
	}
	for _, form := range forms {
		d := newDirs(t)
		s := d.startServe(t, d.args(form...)...)
		if got, want := s.stdout.String(), d.startLine(uid); got != want {
			t.Errorf("with %q the start line is %q, want %q", form, got, want)
		}
		w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
		mustCommit(t, w, row{2, "a", "1"})
		s.stop()
	}

	// Another user is no writer, and the reader socket serves it.
	d := newDirs(t)
	s := d.startServe(t, d.args("-writer-user", strconv.Itoa(uid+1))...)
	if got, want := s.stdout.String(), d.startLine(uid+1); got != want {
		t.Errorf("the start line is %q, want %q", got, want)
	}
	_, err := dbclient.Open(t.Context(), dbclient.SocketDir(d.socks), dbwire.RoleWriter, dbclient.Options{DialWait: -1})
	var re *dbwire.RemoteError
	if !errors.As(err, &re) || re.Code != dbwire.CodeDenied || re.Msg != fmt.Sprintf("the user %d is not the writer of this daemon", uid) {
		t.Errorf("a writer who is not the user of -writer-user: %v, want the code %v and the user %d", err, dbwire.CodeDenied, uid)
	}
	openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
	s.stop()

	// The groups of the sockets, by number and by name; a group that the user has not is the system's to refuse.
	groupForms := [][]string{{"-writer-group", strconv.Itoa(gid), "-reader-group", strconv.Itoa(gid)}}
	if groupName != "" {
		groupForms = append(groupForms, []string{"-writer-group", groupName, "-reader-group", groupName})
	}
	for _, form := range groupForms {
		d := newDirs(t)
		s := d.startServe(t, d.args(form...)...)
		for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
			if got := socketGroup(t, d.socks, name); got != gid {
				t.Errorf("with %q the group of %s is %d, want %d", form, name, got, gid)
			}
		}
		s.stop()
	}
	if other < 0 {
		t.Log("the process has no group besides its own: the groups of the two sockets are not told apart")
		return
	}
	for _, form := range []struct {
		args         []string
		writer, read int
	}{
		{[]string{"-writer-group", strconv.Itoa(other)}, other, gid},
		{[]string{"-reader-group", strconv.Itoa(other)}, gid, other},
		{[]string{"-writer-group", strconv.Itoa(other), "-reader-group", strconv.Itoa(other)}, other, other},
	} {
		d := newDirs(t)
		s := d.startServe(t, d.args(form.args...)...)
		if got := socketGroup(t, d.socks, dbwire.WriterSocket); got != form.writer {
			t.Errorf("with %q the group of the writer socket is %d, want %d", form.args, got, form.writer)
		}
		if got := socketGroup(t, d.socks, dbwire.ReaderSocket); got != form.read {
			t.Errorf("with %q the group of the reader socket is %d, want %d", form.args, got, form.read)
		}
		s.stop()
	}
	// The same from the config file.
	d = newDirs(t)
	file := writeFile(t, d.root, "whiteroom.yaml", fmt.Sprintf("data: data\ndb: s\nminFree: 1\nreaderGroup: %d\n", other))
	s = d.startServe(t, "-config", file)
	if got := socketGroup(t, d.socks, dbwire.ReaderSocket); got != other {
		t.Errorf("with readerGroup in the file the group of the reader socket is %d, want %d", got, other)
	}
	if got := socketGroup(t, d.socks, dbwire.WriterSocket); got != gid {
		t.Errorf("with readerGroup in the file the group of the writer socket is %d, want %d", got, gid)
	}
	s.stop()
}

// TestServeUsersRefused checks that a user or a group that is wrong is refused before anything is opened: the exit code is 2
// and no file is touched.
func TestServeUsersRefused(t *testing.T) {
	d := newDirs(t)
	before := files(t, d.data)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"-writer-user", "no-such-user-x"}, "whiteroom-db serve: -writer-user: unknown user \"no-such-user-x\"\n"},
		{[]string{"-writer-group", "no-such-group-x"}, "whiteroom-db serve: -writer-group: unknown group \"no-such-group-x\"\n"},
		{[]string{"-reader-group", "no-such-group-x"}, "whiteroom-db serve: -reader-group: unknown group \"no-such-group-x\"\n"},
		{[]string{"-max-readers", "0"}, "whiteroom-db serve: -max-readers: must be an integer of 1 or more\n"},
		{[]string{"-commit-rate", "-1"}, "whiteroom-db serve: -commit-rate: must be an integer of 0 or more\n"},
	} {
		code, stdout, stderr := runCmd(t, append([]string{"serve"}, d.args(tt.args...)...)...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, tt.want+serveUsage) {
			t.Errorf("serve %q = %d, with stdout %q and stderr %q, want 2, nothing and %q and the usage", tt.args, code, stdout,
				stderr, tt.want)
		}
	}
	file := writeFile(t, d.root, "whiteroom.yaml", "data: data\ndb: s\nwriterGroup: no-such-group-x\n")
	code, stdout, stderr := runCmd(t, "serve", "-config", file)
	if want := file + ": /writerGroup: unknown group \"no-such-group-x\"\n"; code != 2 || stdout != "" || stderr != want {
		t.Errorf("serve with an unknown group in the file = %d, with stdout %q and stderr %q, want 2, nothing and %q", code, stdout,
			stderr, want)
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("a refused serve changed the data directory:\n%v\nwas\n%v", got, before)
	}
	if got, err := os.ReadDir(d.socks); err != nil || len(got) != 0 {
		t.Errorf("a refused serve made %v (%v) in the directory of the sockets", got, err)
	}
}

// limitCase is a limit, and what a test sees of it at a daemon that has it.
type limitCase struct {
	name  string
	flags []string // the flags that set the limit
	item  string   // the lines of the config file that set the limit
	// over: the flags are given with the items, and say the opposite
	over  bool
	check func(t *testing.T, d dirs)
}

// start runs the daemon with the flags, or with the items of the config file, or with both.
func (c limitCase) start(t *testing.T, form string) (dirs, *daemon) {
	t.Helper()
	d := newDirs(t)
	var args []string
	switch form {
	case "flags":
		args = append(d.args(), c.flags...)
	case "items":
		args = []string{"-config", writeFile(t, d.root, "whiteroom.yaml", "data: data\ndb: s\n"+c.item)}
	default:
		args = append([]string{"-config", writeFile(t, d.root, "whiteroom.yaml", "data: data\ndb: s\n"+c.item)}, c.flags...)
	}
	return d, d.startServe(t, args...)
}

func TestServeLimits(t *testing.T) {
	wantCode := func(t *testing.T, err error, code dbwire.Code) {
		t.Helper()
		var re *dbwire.RemoteError
		if !errors.As(err, &re) || re.Code != code {
			t.Errorf("got %v, want the code %v", err, code)
		}
	}
	seed := func(t *testing.T, d dirs) *dbclient.Client {
		t.Helper()
		w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
		mustCommit(t, w, row{2, "a", "1"})
		return w
	}
	// idle is how long a connection that sent nothing is let alone: with a snapshot that the daemon releases after
	// 100 milliseconds, the next read is refused.
	snapshotAfterSleep := func(d dirs) error {
		r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
		return r.View(t.Context(), func(tx *dbclient.Tx) error {
			if _, _, err := tx.Get(2, []byte("a")); err != nil {
				return err
			}
			time.Sleep(600 * time.Millisecond)
			_, _, err := tx.Get(2, []byte("b"))
			return err
		})
	}
	// cutAfter reads from a connection that says nothing until the daemon closes it, or for wait.
	cutAfter := func(t *testing.T, d dirs, wait time.Duration) error {
		t.Helper()
		conn, err := net.Dial("unix", filepath.Join(d.socks, dbwire.ReaderSocket))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(wait))
		_, err = io.Copy(io.Discard, conn)
		return err
	}
	commits := func(t *testing.T, w *dbclient.Client, n int) time.Duration {
		t.Helper()
		start := time.Now()
		for i := range n {
			mustCommit(t, w, row{2, "k" + strconv.Itoa(i), "v"})
		}
		return time.Since(start)
	}

	for _, c := range []limitCase{
		{name: "max-readers", flags: []string{"-max-readers", "1", "-min-free", "1"}, item: "maxReaders: 1\nminFree: 1\n",
			check: func(t *testing.T, d dirs) {
				openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
				_, err := dbclient.Open(t.Context(), dbclient.SocketDir(d.socks), dbwire.RoleReader, dbclient.Options{DialWait: -1})
				wantCode(t, err, dbwire.CodeBusy)
			}},
		{name: "min-free", flags: []string{"-min-free", "4611686018427387904"}, item: "minFree: 4611686018427387904\n",
			check: func(t *testing.T, d dirs) {
				w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
				wantCode(t, commit(t.Context(), w, row{2, "a", "1"}), dbwire.CodeNoSpace)
			}},
		{name: "min-free of 0 over the item", flags: []string{"-min-free", "0"}, item: "minFree: 4611686018427387904\n", over: true,
			check: func(t *testing.T, d dirs) {
				w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
				mustCommit(t, w, row{2, "a", "1"})
			}},
		{name: "max-staged", flags: []string{"-max-staged", "100", "-min-free", "1"}, item: "maxStaged: 100\nminFree: 1\n",
			check: func(t *testing.T, d dirs) {
				w := seed(t, d)
				object := func(n int) error {
					data := []byte(strings.Repeat("o", n))
					return w.Update(t.Context(), func(tx *dbclient.Tx) error {
						if err := tx.PutObject(sha256.Sum256(data), data); err != nil {
							return err
						}
						return tx.AppendEvent([]byte("test"))
					})
				}
				if err := object(100); err != nil {
					t.Errorf("an object of 100 bytes = %v, want it staged and committed", err)
				}
				if err := object(101); err == nil || !strings.Contains(err.Error(), "the staged objects would be over 100 bytes") {
					t.Errorf("an object of 101 bytes = %v, want it refused for the limit of 100 bytes", err)
				}
			}},
		{name: "snapshot-idle", flags: []string{"-snapshot-idle", "100ms", "-min-free", "1"}, item: "snapshotIdle: 100ms\nminFree: 1\n",
			check: func(t *testing.T, d dirs) {
				seed(t, d)
				wantCode(t, snapshotAfterSleep(d), dbwire.CodeNoSnapshot)
			}},
		{name: "snapshot-idle of 0 over the item", flags: []string{"-snapshot-idle", "0", "-min-free", "1"},
			item: "snapshotIdle: 100ms\nminFree: 1\n", over: true,
			check: func(t *testing.T, d dirs) {
				seed(t, d)
				if err := snapshotAfterSleep(d); err != nil {
					t.Errorf("a snapshot that the daemon should never release: %v", err)
				}
			}},
		{name: "stall", flags: []string{"-stall", "200ms", "-min-free", "1"}, item: "stall: 200ms\nminFree: 1\n",
			check: func(t *testing.T, d dirs) {
				if err := cutAfter(t, d, 10*time.Second); err != nil {
					t.Errorf("a connection that says nothing: %v, want it closed by the daemon", err)
				}
			}},
		{name: "stall of 0 over the item", flags: []string{"-stall", "0", "-min-free", "1"}, item: "stall: 200ms\nminFree: 1\n", over: true,
			check: func(t *testing.T, d dirs) {
				if err := cutAfter(t, d, time.Second); !errors.Is(err, os.ErrDeadlineExceeded) {
					t.Errorf("a connection that says nothing: %v, want it left open", err)
				}
			}},
		{name: "commit-rate", flags: []string{"-commit-rate", "2", "-min-free", "1"}, item: "commitRate: 2\nminFree: 1\n",
			check: func(t *testing.T, d dirs) {
				w := seed(t, d) // takes one of the two
				if took := commits(t, w, 4); took < time.Second {
					t.Errorf("5 commits at 2 a second took %v, want a second or more", took)
				}
			}},
		{name: "commit-rate of 0 over the item", flags: []string{"-commit-rate", "0", "-min-free", "1"}, item: "commitRate: 2\nminFree: 1\n",
			over: true,
			check: func(t *testing.T, d dirs) {
				w := seed(t, d)
				if took := commits(t, w, 4); took > 900*time.Millisecond {
					t.Errorf("5 commits with no limit took %v, want less than a second", took)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			forms := []string{"flags", "items"}
			if c.over {
				forms = []string{"both"}
			}
			for _, form := range forms {
				t.Run(form, func(t *testing.T) {
					t.Parallel()
					d, s := c.start(t, form)
					c.check(t, d)
					if code := s.stop(); code != 0 {
						t.Errorf("whiteroom-db serve = %d, with stderr:\n%s", code, s.stderr)
					}
				})
			}
		})
	}
}

// TestServeCommitRateOff checks that a commit rate of 0 turns the limit off, and does not mean the default of 50 a second,
// which a burst of commits beyond the 50 that the daemon lets through at once would wait for.
func TestServeCommitRateOff(t *testing.T) {
	const n = 160
	for _, form := range []struct {
		name, item string
		flags      []string
	}{
		{"flag", "", []string{"-commit-rate", "0"}},
		{"item", "commitRate: 0\n", nil},
	} {
		t.Run(form.name, func(t *testing.T) {
			d := newDirs(t)
			args := append(d.args(), form.flags...)
			if form.item != "" {
				args = []string{"-config", writeFile(t, d.root, "whiteroom.yaml", "data: data\ndb: s\nminFree: 1\n"+form.item)}
			}
			s := d.startServe(t, args...)
			w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
			start := time.Now()
			for i := range n {
				mustCommit(t, w, row{2, "k" + strconv.Itoa(i), "v"})
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("%d commits with no limit took %v; at the default of 50 a second they would take 2.2 seconds or more", n, took)
			}
			s.stop()
		})
	}
}

func TestServeConfig(t *testing.T) {
	d := newDirs(t)
	uid := os.Getuid()
	// The items of both commands in one file, the paths relative to the file, whatever the working directory is.
	file := writeFile(t, d.root, "whiteroom.yaml", fmt.Sprintf(`# the settings of whiteroom and of whiteroom-db
data: data
db: s
site: site
theme: theme
out: out
cache: cache
tmp: tmp
url: https://example.com
addr: 127.0.0.1:8081
origin: http://127.0.0.1:8081
trustedProxies: [127.0.0.1]
workers: 2
memory: 512
writerUser: %d
maxReaders: 8
commitRate: 100
minFree: 1
snapshotIdle: 10m
stall: 1m
maxStaged: 1000000
`, uid))
	t.Chdir(t.TempDir())
	s := d.startServe(t, "-config", file)
	if got, want := s.stdout.String(), d.startLine(uid); got != want {
		t.Errorf("the start line is %q, want %q", got, want)
	}
	w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
	r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
	mustCommit(t, w, row{2, "a", "1"})
	if v, found := value(t, r, "a"); !found || v != "1" {
		t.Errorf("the reader reads %q, %v, want 1", v, found)
	}
	if code := s.stop(); code != 0 {
		t.Errorf("whiteroom-db serve = %d", code)
	}
	// The items of whiteroom make nothing in the directory that the daemon works in.
	if got, err := os.ReadDir(filepath.Join(d.root)); err != nil || len(got) != 3 {
		t.Errorf("the directory of the file has %v (%v), want only data, s and the file", got, err)
	}

	// A problem of the file is reported before anything is opened, as whiteroom reports it, and the flags do not change that.
	before := files(t, d.data)
	for _, tt := range []struct {
		name, text, want string
	}{
		{"unknown item", "data: data\ndb: s\ncolour: red\n", ":3:1: /colour: unknown item\n"},
		{"wrong type", "data: data\ndb: s\nmaxReaders: many\n", ":3:1: /maxReaders: must be an integer of 1 or more\n"},
		{"wrong range", "data: data\ndb: s\nstall: -1s\n", ":3:1: /stall: must be a duration of 0 or more, such as 30s or 5m\n"},
		{"broken", "data:\t/a\n", ":1:6: tab characters are not allowed; use spaces\n"},
	} {
		bad := writeFile(t, d.root, "bad.yaml", tt.text)
		for _, args := range [][]string{{"-config", bad}, {"-config", bad, "-data", d.data, "-db", d.socks}} {
			code, stdout, stderr := runCmd(t, append([]string{"serve"}, args...)...)
			if want := bad + tt.want; code != 2 || stdout != "" || stderr != want {
				t.Errorf("%s: serve %q = %d, with stdout %q and stderr %q, want 2, nothing and %q", tt.name, args, code, stdout, stderr, want)
			}
		}
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("a refused serve changed the data directory:\n%v\nwas\n%v", got, before)
	}
	if got, err := os.ReadDir(d.socks); err != nil || len(got) != 0 {
		t.Errorf("a refused serve made %v (%v) in the directory of the sockets", got, err)
	}

	// Without -config no file is read, whatever the working directory has: the flags are all that serve has.
	work := t.TempDir()
	for _, name := range []string{"whiteroom.yaml", "config.yaml", "whiteroom-db.yaml"} {
		writeFile(t, work, name, "data:\t/nowhere\n")
	}
	t.Chdir(work)
	s = d.startServe(t, d.args()...)
	openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
	s.stop()
}
