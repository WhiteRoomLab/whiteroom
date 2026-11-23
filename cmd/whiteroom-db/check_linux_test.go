//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// checkSecret is in the rows, the object and the originals of the store of these tests: nothing that check prints may have it.
const checkSecret = "CHECK-SECRET-91"

// checkStore is a data directory with rows, an object that is a file, and two originals, whose daemon runs until stop, and a
// copy of it that stop makes. (Backup writes through a file system of the engine, which these tests may not import: whiteroom
// backup and a check of its copy are in TestProcesses of cmd/whiteroom.)
type checkStore struct {
	d      dirs
	srv    *daemon
	w      *dbclient.Client
	copy   string
	hashes [][32]byte // of the originals
}

func newCheckStore(t *testing.T) *checkStore {
	t.Helper()
	d := newDirs(t)
	s := &checkStore{d: d, srv: d.startServe(t, d.args()...)}
	s.w = openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
	object := append([]byte(checkSecret), bytes.Repeat([]byte("o"), 100<<10)...)
	oh := sha256.Sum256(object)
	mustCommit(t, s.w, row{2, checkSecret + "-key", checkSecret + "-value"}, row{2, "b", "2"})
	if err := s.w.Update(t.Context(), func(tx *dbclient.Tx) error {
		if err := tx.PutObject(oh, object); err != nil {
			return err
		}
		if err := tx.Put(2, []byte("object"), oh[:]); err != nil {
			return err
		}
		return tx.AppendEvent([]byte(checkSecret))
	}); err != nil {
		t.Fatalf("committing an object = %v", err)
	}
	for _, data := range [][]byte{[]byte("GIF89a" + checkSecret), bytes.Repeat([]byte(checkSecret), 5000)} {
		h, _, err := s.w.PutOriginal(t.Context(), bytes.NewReader(data))
		if err != nil {
			t.Fatalf("PutOriginal() = %v", err)
		}
		s.hashes = append(s.hashes, h)
	}
	mustCommit(t, s.w, row{2, "a", "3"})
	return s
}

// stop stops the daemon, whose lock its client would keep, and copies the data directory.
func (s *checkStore) stop(t *testing.T) {
	t.Helper()
	_ = s.w.Close()
	if code := s.srv.stop(); code != 0 {
		t.Fatalf("whiteroom-db serve = %d", code)
	}
	s.copy = cloneTree(t, s.d.data)
}

func (s *checkStore) original(i int) string {
	return fmt.Sprintf("files/%x/%x", s.hashes[i][:1], s.hashes[i])
}

// cloneTree makes a copy of the directory src, with the modes that Backup gives.
func cloneTree(t testing.TB, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "clone")
	if err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Mkdir(filepath.Join(dst, rel), 0o700)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	return dst
}

func editFile(t testing.TB, name string, change func([]byte) []byte) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, change(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func flipByte(at int) func([]byte) []byte {
	return func(b []byte) []byte {
		if at < 0 {
			at += len(b)
		}
		b[at] ^= 1
		return b
	}
}

func lastSegment(t testing.TB, dir string) string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "logdb", "log", "*.log"))
	if err != nil || len(m) == 0 {
		t.Fatalf("the segments are %v (%v)", m, err)
	}
	slices.Sort(m)
	return m[len(m)-1]
}

func hexSum(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:])
}

// wantCheck runs whiteroom-db check on dir and checks the exit code 1, that each line of the output starts with the prefix
// of want, the last line, and that nothing is on the standard error and nothing has changed in dir; or the exit code 0, if want
// is empty.
func wantCheck(t *testing.T, dir string, want ...string) {
	t.Helper()
	before := files(t, dir)
	code, stdout, stderr := runCmd(t, "check", "-data", dir)
	after := files(t, dir)
	if !maps.Equal(after, before) {
		t.Errorf("whiteroom-db check changed the data directory:\n%v\nwas\n%v", after, before)
	}
	if stderr != "" {
		t.Errorf("whiteroom-db check printed on stderr: %q", stderr)
	}
	if strings.Contains(stdout+stderr, checkSecret) {
		t.Errorf("whiteroom-db check printed what is in the rows, the objects or the originals:\n%s", stdout)
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(want) == 0 {
		if code != 0 || stdout != "no problems found in "+dir+"\n" {
			t.Errorf("whiteroom-db check = %d, with stdout %q, want 0 and no problems", code, stdout)
		}
		return
	}
	summary := fmt.Sprintf("%d problems found in %s", len(want), dir)
	if len(want) == 1 {
		summary = "1 problem found in " + dir
	}
	if code != 1 || len(got) != len(want)+1 || got[len(got)-1] != summary {
		t.Errorf("whiteroom-db check = %d, with stdout:\n%s\nwant 1, with %d problems and %q", code, stdout, len(want), summary)
		return
	}
	for i, w := range want {
		if line := strings.ReplaceAll(got[i], dir+"/", ""); !strings.HasPrefix(line, w) {
			t.Errorf("line %d of the output is %q, want it to start with %q", i, line, w)
		}
	}
}

// TestCheck runs check on a copy that Backup made, on a data directory whose daemon has stopped, and on each damage of the
// copy: the lines, the summary, the exit code, and that nothing is changed.
func TestCheck(t *testing.T) {
	s := newCheckStore(t)

	// While the daemon runs, check waits for about two seconds, fails, and changes nothing; the daemon goes on.
	t.Run("a directory in use", func(t *testing.T) {
		before := files(t, s.d.data)
		start := time.Now()
		code, stdout, stderr := runCmd(t, "check", "-data", s.d.data)
		want := "whiteroom-db check: " + s.d.data + ": the data directory is in use; stop whiteroom-db serve on it first\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db check on a directory in use = %d, with stdout %q and stderr %q, want 1, nothing and %q", code,
				stdout, stderr, want)
		}
		if took := time.Since(start); took < 1500*time.Millisecond || took > 8*time.Second {
			t.Errorf("whiteroom-db check on a directory in use took %v, want about 2 seconds", took)
		}
		if got := files(t, s.d.data); !maps.Equal(got, before) {
			t.Errorf("check on a directory in use changed the files:\n%v\nwas\n%v", got, before)
		}
		mustCommit(t, s.w, row{2, "a", "3"}) // the writer still commits
	})

	s.stop(t)

	t.Run("sound", func(t *testing.T) {
		for _, dir := range []string{s.copy, s.d.data} {
			wantCheck(t, dir)
		}
		// A temporary file of a daemon that was killed, and names that are not those of originals, are left as they are.
		dir := cloneTree(t, s.copy)
		for name, data := range map[string]string{"files/.tmp-0123456789abcdef": "half", "files/README": "x", "files/ab/notes": "x"} {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		wantCheck(t, dir)
	})

	// The copy is as sound as it says: a daemon on it opens it and serves what the first one had.
	t.Run("a daemon on the copy", func(t *testing.T) {
		dir := cloneTree(t, s.copy)
		d2 := s.d
		d2.data, d2.socks = dir, filepath.Join(s.d.root, "s2")
		if err := os.Mkdir(d2.socks, 0o700); err != nil {
			t.Fatal(err)
		}
		srv := d2.startServe(t, d2.args()...)
		if v, found := value(t, openClient(t, d2.socks, dbwire.RoleReader, dbclient.Options{}), "a"); !found || v != "3" {
			t.Errorf("the daemon on the copy serves %q, %v, want 3", v, found)
		}
		srv.stop()
	})

	object := func(dir string) string {
		m, err := filepath.Glob(filepath.Join(dir, "logdb", "objects", "*", "*"))
		if err != nil || len(m) != 1 {
			t.Fatalf("the object files are %v (%v), want one", m, err)
		}
		return m[0]
	}
	for _, tc := range []struct {
		name         string
		noCheckpoint bool // as the copy of a Backup that a daemon made before its first checkpoint
		damage       func(t *testing.T, dir string)
		want         []string
	}{
		{"the last segment is removed", false, func(t *testing.T, dir string) {
			if err := os.Remove(lastSegment(t, dir)); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/log: no segments"}},
		{"the last record is cut short", false, func(t *testing.T, dir string) {
			editFile(t, lastSegment(t, dir), func(b []byte) []byte { return b[:len(b)-5] })
		}, []string{"engine: logdb/checkpoint: the log does not have sequence "}},
		{"the last record is cut short, and there is no checkpoint", true, func(t *testing.T, dir string) {
			editFile(t, lastSegment(t, dir), func(b []byte) []byte { return b[:len(b)-5] })
		}, []string{"engine: logdb/log/", "engine: logdb/head: head names sequence "}},
		{"the log is damaged", true, func(t *testing.T, dir string) {
			editFile(t, lastSegment(t, dir), flipByte(-3))
		}, []string{"engine: logdb/log/"}},
		{"the file of the object is missing", false, func(t *testing.T, dir string) {
			if err := os.Remove(object(dir)); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/objects/"}},
		{"head is missing", false, func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "logdb", "head")); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/head: missing file"}},
		{"the checkpoint is damaged", false, func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, "logdb", "checkpoint"), flipByte(-3))
		}, []string{"engine: logdb/checkpoint: damaged (bad body CRC): Open reads the log from the start of its generation"}},
		{"an original has other data", false, func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, s.original(0)), func([]byte) []byte { return []byte("other data") })
		}, []string{"original: " + s.original(0) + ": the data has the hash sha256:" + hexSum("other data")}},
		{"an original is a directory", false, func(t *testing.T, dir string) {
			p := filepath.Join(dir, s.original(1))
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{"original: " + s.original(1) + ": must be a regular file"}},
		{"an original is a symbolic link", false, func(t *testing.T, dir string) {
			p := filepath.Join(dir, s.original(0))
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir, s.original(1)), p); err != nil {
				t.Skipf("no symbolic links: %v", err)
			}
		}, []string{"original: " + s.original(0) + ": symbolic links are not allowed"}},
		{"the originals and the engine", false, func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, s.original(0)), flipByte(0))
			editFile(t, filepath.Join(dir, s.original(1)), flipByte(0))
			if err := os.Remove(object(dir)); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/objects/", "original: ", "original: "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := cloneTree(t, s.copy)
			if tc.noCheckpoint {
				if err := os.Remove(filepath.Join(dir, "logdb", "checkpoint")); err != nil {
					t.Fatal(err)
				}
			}
			// What a killed daemon leaves is not touched.
			if err := os.WriteFile(filepath.Join(dir, "files", ".tmp-0123456789abcdef"), []byte("half"), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.damage(t, dir)
			wantCheck(t, dir, tc.want...)
			if _, err := os.Lstat(filepath.Join(dir, "files", ".tmp-0123456789abcdef")); err != nil {
				t.Errorf("the temporary file: %v, want it left", err)
			}
		})
	}

	// The same through the config file, whose data is used when -data is not given.
	t.Run("config", func(t *testing.T) {
		dir := cloneTree(t, s.copy)
		editFile(t, filepath.Join(dir, s.original(0)), flipByte(0))
		conf := writeFile(t, t.TempDir(), "whiteroom.yaml", fmt.Sprintf("data: %q\n", dir))
		code, stdout, stderr := runCmd(t, "check", "-config", conf)
		if want := "original: " + s.original(0) + ": the data has the hash sha256:"; code != 1 || stderr != "" ||
			!strings.HasPrefix(stdout, want) || !strings.HasSuffix(stdout, "\n1 problem found in "+dir+"\n") {
			t.Errorf("whiteroom-db check -config = %d, with stdout %q and stderr %q", code, stdout, stderr)
		}
		// -data overrides it.
		code, stdout, stderr = runCmd(t, "check", "-config", conf, "-data", s.copy)
		if want := "no problems found in " + s.copy + "\n"; code != 0 || stdout != want || stderr != "" {
			t.Errorf("whiteroom-db check -config -data = %d, with stdout %q and stderr %q, want 0 and %q", code, stdout, stderr, want)
		}
	})

	// After 1000 problems it stops: the 1001st is not printed.
	t.Run("a thousand problems", func(t *testing.T) {
		dir := cloneTree(t, s.copy)
		for i := range 1001 {
			name := hexSum(fmt.Sprintf("name %d", i))
			if err := os.MkdirAll(filepath.Join(dir, "files", name[:2]), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "files", name[:2], name), []byte(fmt.Sprintf("data %d", i)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		before := files(t, dir)
		code, stdout, stderr := runCmd(t, "check", "-data", dir)
		got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if code != 1 || stderr != "" || len(got) != 1001 || got[1000] != "stopped after 1000 problems in "+dir {
			t.Errorf("whiteroom-db check of 1001 problems = %d, with %d lines, the last %q, and stderr %q, want 1, 1001 lines and "+
				"the stop", code, len(got), got[len(got)-1], stderr)
		}
		if n := strings.Count(stdout, "original: files/"); n != 1000 {
			t.Errorf("whiteroom-db check printed %d problems, want 1000", n)
		}
		if after := files(t, dir); !maps.Equal(after, before) {
			t.Error("whiteroom-db check changed the data directory")
		}
		// Exactly 1000 problems are a stop too, as for whiteroom doctor; one less are not.
		for i, summary := range []string{"stopped after 1000 problems in " + dir, "999 problems found in " + dir} {
			name := hexSum(fmt.Sprintf("name %d", i))
			if err := os.Remove(filepath.Join(dir, "files", name[:2], name)); err != nil {
				t.Fatal(err)
			}
			code, stdout, _ = runCmd(t, "check", "-data", dir)
			if got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n"); code != 1 || got[len(got)-1] != summary {
				t.Errorf("whiteroom-db check of %d problems = %d, with the last line %q, want 1 and %q", 1000-i, code, got[len(got)-1], summary)
			}
		}
	})
}

// TestCheckRefused checks that check refuses a data directory whose mark of a failed sync has the boot ID of this machine, and
// one whose mark cannot be read, with the words of compact and gc, and without a change; and that it checks one whose mark is
// of an earlier boot, and leaves the mark where it is.
func TestCheckRefused(t *testing.T) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Skipf("no boot ID: %v", err)
	}
	machine := strings.TrimSuffix(string(b), "\n")
	earlier := "00000000-0000-4000-8000-0000000000e1"
	if machine == earlier {
		earlier = "00000000-0000-4000-8000-0000000000e2"
	}
	const at, cause = "2026-11-23T08:49:00Z", "sync x/logdb/log/y.log: input/output error"
	valid := func(boot string) string {
		return "whiteroom-db stopped after a sync failed; restart the machine before starting it again\n" +
			"boot: " + boot + "\ntime: " + at + "\ncause: " + cause + "\n"
	}
	s := newCheckStore(t)
	s.stop(t)
	for _, tc := range []struct {
		name, mark string
		refused    bool
	}{
		{"this boot", valid(machine), true},
		{"empty", "", true},
		{"no boot line", "whiteroom-db stopped after a sync failed\ntime: " + at + "\n", true},
		{"an earlier boot", valid(earlier), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := cloneTree(t, s.copy)
			editFile(t, filepath.Join(dir, s.original(0)), flipByte(0))
			mark := filepath.Join(dir, "stopped")
			if err := os.WriteFile(mark, []byte(tc.mark), 0o600); err != nil {
				t.Fatal(err)
			}
			before := files(t, dir)
			code, stdout, stderr := runCmd(t, "check", "-data", dir)
			if after := files(t, dir); !maps.Equal(after, before) {
				t.Errorf("whiteroom-db check changed the data directory:\n%v\nwas\n%v", after, before)
			}
			if !tc.refused {
				// The check runs, and the mark stays; compact and gc remove it.
				if code != 1 || stderr != "" || !strings.HasSuffix(stdout, "\n1 problem found in "+dir+"\n") {
					t.Errorf("whiteroom-db check on a mark of an earlier boot = %d, with stdout %q and stderr %q, want the check", code,
						stdout, stderr)
				}
				return
			}
			// The words of gc, which refuses the same directories.
			gcCode, gcStdout, gcStderr := runCmd(t, "gc", "-data", dir)
			want := strings.Replace(gcStderr, "whiteroom-db gc: ", "whiteroom-db check: ", 1)
			if gcCode != 1 || gcStdout != "" || !strings.HasPrefix(want, "whiteroom-db check: "+dir) {
				t.Fatalf("whiteroom-db gc = %d, with stdout %q and stderr %q, want a refusal", gcCode, gcStdout, gcStderr)
			}
			if code != 1 || stdout != "" || stderr != want {
				t.Errorf("whiteroom-db check = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, want)
			}
		})
	}
}

// TestCheckReadOnly checks a data directory that the user cannot write in.
func TestCheckReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser passes access(2)")
	}
	s := newCheckStore(t)
	s.stop(t)
	dir := cloneTree(t, s.copy)
	editFile(t, filepath.Join(dir, s.original(0)), flipByte(0))
	var dirs []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		return os.Chmod(p, 0o400)
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range slices.Backward(dirs) {
		if err := os.Chmod(p, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, p := range dirs {
			_ = os.Chmod(p, 0o700)
		}
	})
	wantCheck(t, dir, "original: "+s.original(0)+": the data has the hash sha256:")

	// A root that it cannot write in, which compact and gc refuse: the mark of a failed sync could not be left there.
	root := cloneTree(t, s.copy)
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if code, _, stderr := runCmd(t, "gc", "-data", root); code != 1 || !strings.Contains(stderr, "whiteroom-db cannot write in the data directory") {
		t.Errorf("whiteroom-db gc on a root that it cannot write in = %d, with stderr %q, want a refusal", code, stderr)
	}
	wantCheck(t, root)
}

// TestCheckInterrupted checks that a signal while check waits for a daemon ends it with its own line, as an interruption and
// not as a directory in use.
func TestCheckInterrupted(t *testing.T) {
	s := newCheckStore(t)
	before := files(t, s.d.data)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	code, stdout, stderr := runCmdIn(ctx, t, "check", "-data", s.d.data)
	cancel()
	if want := "whiteroom-db check: interrupted\n"; code != 1 || stdout != "" || stderr != want {
		t.Errorf("whiteroom-db check interrupted = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, want)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("whiteroom-db check took %v to end after the interruption", took)
	}
	if got := files(t, s.d.data); !maps.Equal(got, before) {
		t.Errorf("the interrupted check changed the files:\n%v\nwas\n%v", got, before)
	}
	mustCommit(t, s.w, row{2, "a", "4"}) // the daemon goes on
	s.stop(t)

	// Before it starts: the check ends at once, whatever is on disk.
	ctx, cancel = context.WithCancel(t.Context())
	cancel()
	code, stdout, stderr = runCmdIn(ctx, t, "check", "-data", s.copy)
	if want := "whiteroom-db check: interrupted\n"; code != 1 || stdout != "" || stderr != want {
		t.Errorf("whiteroom-db check with the context ended = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout,
			stderr, want)
	}
}

// TestCheckRefusedDirectories checks the directories that are not data directories: an error, and nothing made.
func TestCheckRefusedDirectories(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing")
	code, stdout, stderr := runCmd(t, "check", "-data", empty)
	if want := "whiteroom-db check: " + filepath.Join(empty, "logdb") + ": missing directory\n"; code != 1 || stdout != "" || stderr != want {
		t.Errorf("whiteroom-db check of an empty directory = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout,
			stderr, want)
	}
	code, stdout, stderr = runCmd(t, "check", "-data", missing)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom-db check: ") || !strings.Contains(stderr, missing) {
		t.Errorf("whiteroom-db check of a missing directory = %d, with stdout %q and stderr %q, want 1 and the error", code, stdout, stderr)
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Errorf("check made %v (%v) in an empty directory", entries, err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Errorf("check made a missing directory: %v", err)
	}
}
