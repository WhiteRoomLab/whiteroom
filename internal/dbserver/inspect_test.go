package dbserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// inspectSecret is in the rows, the events, the object and the originals of the store of these tests: no line of Inspect may
// have it.
const inspectSecret = "INSPECT-SECRET-77"

// inspectStore is a store with several segments of its log, a checkpoint in the middle of them, an object that is a file,
// and two originals, and a copy that Backup has made of it, which the tests damage in clones.
type inspectStore struct {
	f      *fixture // its server is open
	w      *client  // its writer
	copy   string
	hashes [][32]byte // of the originals
}

func newInspectStore(t *testing.T) *inspectStore {
	t.Helper()
	f := newDirFixture(t, func(o *Options) { o.Log.SegmentBytes = 400; o.Log.CheckpointBytes = -1 })
	w := f.connect(dbwire.RoleWriter)
	seq := w.mustSnapshot()
	if _, err := w.putObject(inspectSecret + strings.Repeat("o", 1000)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.commit(seq, put(2, inspectSecret+"-key", inspectSecret+"-value"), event(inspectSecret)); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		w.mustCommit(put(2, fmt.Sprintf("k%d", i), strings.Repeat("v", 100)), event("e"))
	}
	s := &inspectStore{f: f, w: w}
	for _, data := range [][]byte{[]byte("GIF89a" + inspectSecret), bytes.Repeat([]byte(inspectSecret), 5000)} {
		s.hashes = append(s.hashes, mustPut(t, w, data))
	}
	if err := f.db().Checkpoint(); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		w.mustCommit(put(2, fmt.Sprintf("m%d", i), strings.Repeat("w", 100)), event("e"))
	}
	s.copy = filepath.Join(t.TempDir(), "copy")
	mkdir(t, s.copy)
	fsys, err := vfs.OpenRoot(s.copy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	if _, err := f.connect(dbwire.RoleReader).backup(fsys); err != nil {
		t.Fatal(err)
	}
	return s
}

// cloneDir makes a copy of the data directory src, and puts in it what is in a data directory and is not an original: a
// temporary file of a PutOriginal, which a daemon removes, and names that are not those of originals, with data that does
// not have their hashes.
func cloneDir(t testing.TB, src string) string {
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

// notOriginals puts files in files/ whose names are not those of originals, with data that do not have a hash that their
// names say.
func notOriginals(t testing.TB, dir string) {
	t.Helper()
	other := strings.Repeat("ab", 32)
	for _, e := range []struct {
		name  string
		isDir bool
	}{
		{"files/README", false},
		{"files/.tmp-0123456789abcdef", false},
		{"files/.tmp-x", false},
		{"files/zz", true},
		{"files/zz/" + other, false},
		{"files/6", true},
		{"files/6/" + other, false},
		{"files/ab", true},
		{"files/ab/notes", false},
		{"files/ab/ab" + strings.ToUpper(other[2:]), false}, // not lower case
		{"files/AB", true},
		{"files/AB/AB" + other[2:], false}, // not lower case
		{"files/ab/" + other[:63], false},  // not 64 digits
		{"files/cd", true},
		{"files/cd/" + other, false}, // the directory is not the first two digits
	} {
		p := filepath.Join(dir, filepath.FromSlash(e.name))
		if e.isDir {
			mkdir(t, p)
		} else if err := os.WriteFile(p, []byte("not an original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// problemLines runs Inspect on dir and returns the lines of the problems, with dir cut from them.
func problemLines(t testing.TB, dir string, o Options) []string {
	t.Helper()
	ps, err := inspectAll(t.Context(), dir, o)
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	return ps
}

func inspectAll(ctx context.Context, dir string, o Options) ([]string, error) {
	var ps []string
	err := Inspect(ctx, dir, o, func(p Problem) bool {
		ps = append(ps, strings.ReplaceAll(p.String(), dir+"/", ""))
		return true
	})
	return ps, err
}

// wantLines fails the test unless each of got starts with the prefix of want at its place, and has none of the secret.
func wantLines(t testing.TB, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%d problems, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
		return
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("problem %d is %q, want it to start with %q", i, got[i], w)
		}
	}
	for _, line := range got {
		if strings.Contains(line, inspectSecret) {
			t.Errorf("the problem %q has what is in the rows, the objects or the originals", line)
		}
	}
}

func segmentOf(t testing.TB, dir string, i int) string {
	t.Helper()
	all := names(t, filepath.Join(dir, "logdb", "log"))
	if i < 0 {
		i += len(all)
	}
	return filepath.Join(dir, "logdb", "log", all[i])
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

func hexSum(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:])
}

func TestInspectSound(t *testing.T) {
	s := newInspectStore(t)

	t.Run("a copy that Backup made", func(t *testing.T) {
		dir := cloneDir(t, s.copy)
		notOriginals(t, dir)
		before := contents(t, dir)
		wantLines(t, problemLines(t, dir, s.f.opts))
		if after := contents(t, dir); !maps.Equal(after, before) {
			t.Errorf("Inspect changed the data directory:\n%v\nwas\n%v", after, before)
		}
		// It opens as it is: the daemon on the copy has nothing to repair.
		srv, err := OpenDir(dir, s.f.opts)
		if err != nil {
			t.Fatalf("OpenDir() of the copy = %v", err)
		}
		_ = srv.Close()
	})

	t.Run("a data directory whose daemon has stopped", func(t *testing.T) {
		if err := s.f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		notOriginals(t, s.f.dir)
		before := contents(t, s.f.dir)
		wantLines(t, problemLines(t, s.f.dir, s.f.opts))
		if after := contents(t, s.f.dir); !maps.Equal(after, before) {
			t.Errorf("Inspect changed the data directory:\n%v\nwas\n%v", after, before)
		}
	})
}

// TestInspectDamage checks each damage of a copy that Backup made: the problems that it finds, in the order of the areas, and
// that the data directory is the same after it, as Open would have repaired some of them.
func TestInspectDamage(t *testing.T) {
	s := newInspectStore(t)
	original := func(i int) string { return diskPath("", s.hashes[i]) }
	object := func(dir string) string {
		m, err := filepath.Glob(filepath.Join(dir, "logdb", "objects", "*", "*"))
		if err != nil || len(m) != 1 {
			t.Fatalf("the object files are %v (%v), want one", m, err)
		}
		return m[0]
	}
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, dir string)
		want   []string
	}{
		{"the last segment is removed", func(t *testing.T, dir string) {
			if err := os.Remove(segmentOf(t, dir, -1)); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/head: head names sequence "}},
		{"the last record is cut short", func(t *testing.T, dir string) {
			editFile(t, segmentOf(t, dir, -1), func(b []byte) []byte { return b[:len(b)-5] })
		}, []string{"engine: logdb/log/", "engine: logdb/head: head names sequence "}},
		{"a segment before the checkpoint is damaged", func(t *testing.T, dir string) {
			editFile(t, segmentOf(t, dir, 1), flipByte(-3))
		}, []string{"engine: logdb/log/"}},
		{"the first segment is damaged", func(t *testing.T, dir string) {
			editFile(t, segmentOf(t, dir, 0), flipByte(-3))
		}, []string{"engine: logdb/log/"}},
		{"a segment in the middle is missing", func(t *testing.T, dir string) {
			if err := os.Remove(segmentOf(t, dir, 2)); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/log: missing segment "}},
		{"the file of the object is missing", func(t *testing.T, dir string) {
			if err := os.Remove(object(dir)); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/objects/"}},
		{"the file of the object is damaged", func(t *testing.T, dir string) {
			editFile(t, object(dir), flipByte(5))
		}, []string{"engine: logdb/objects/"}},
		{"head is missing", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "logdb", "head")); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/head: missing file"}},
		{"a lock file is missing", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "logdb", "lock", "writer")); err != nil {
				t.Fatal(err)
			}
		}, []string{"engine: logdb/lock/writer: missing file"}},
		{"the checkpoint is damaged", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, "logdb", "checkpoint"), flipByte(-3))
		}, []string{"engine: logdb/checkpoint: damaged (bad body CRC): Open reads the log from the start of its generation"}},
		{"an original has other data", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, original(0)), func([]byte) []byte { return []byte("other data") })
		}, []string{"original: " + original(0) + ": the data has the hash sha256:" + hexSum("other data")}},
		{"an original has a byte more", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, original(1)), func(b []byte) []byte { return append(b, '!') })
		}, []string{"original: " + original(1) + ": the data has the hash sha256:"}},
		{"an original has a byte changed in its end", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, original(1)), flipByte(-1))
		}, []string{"original: " + original(1) + ": the data has the hash sha256:"}},
		{"an original is empty", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, original(0)), func([]byte) []byte { return nil })
		}, []string{"original: " + original(0) + ": the data has the hash sha256:" + hexSum("")}},
		{"an original is a directory", func(t *testing.T, dir string) {
			p := filepath.Join(dir, original(0))
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			mkdir(t, p)
		}, []string{"original: " + original(0) + ": must be a regular file"}},
		{"an original is a symbolic link", func(t *testing.T, dir string) {
			p := filepath.Join(dir, original(0))
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir, original(1)), p); err != nil {
				t.Skipf("no symbolic links: %v", err)
			}
		}, []string{"original: " + original(0) + ": symbolic links are not allowed"}},
		{"the originals of both kinds, and the engine", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, original(1)), flipByte(0))
			editFile(t, filepath.Join(dir, original(0)), flipByte(0))
			editFile(t, filepath.Join(dir, "logdb", "checkpoint"), flipByte(-3))
		}, []string{"engine: logdb/checkpoint: damaged ", "original: ", "original: "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := cloneDir(t, s.copy)
			notOriginals(t, dir)
			tc.damage(t, dir)
			before := contents(t, dir)
			got := problemLines(t, dir, s.f.opts)
			wantLines(t, got, tc.want...)
			if after := contents(t, dir); !maps.Equal(after, before) {
				t.Errorf("Inspect changed the data directory:\n%v\nwas\n%v", after, before)
			}
			// The originals come in the order of their names, after the engine.
			areas := make([]string, len(got))
			for i, line := range got {
				areas[i], _, _ = strings.Cut(line, ": ")
			}
			if !slices.IsSorted(areas) {
				t.Errorf("the areas are %q, want the engine first", areas)
			}
			if i := slices.Index(areas, "original"); i >= 0 {
				if names := got[i:]; !slices.IsSorted(names) {
					t.Errorf("the originals are not in the order of their names: %q", names)
				}
			}
		})
	}
}

// TestInspectRecovery checks that what Open would change is a problem that Inspect finds first and leaves as it is: after
// the cut of the last record, Open truncates the file and writes head again.
func TestInspectRecovery(t *testing.T) {
	s := newInspectStore(t)
	dir := cloneDir(t, s.copy)
	last := segmentOf(t, dir, -1)
	editFile(t, last, func(b []byte) []byte { return b[:len(b)-5] })
	before := contents(t, dir)
	for range 2 {
		wantLines(t, problemLines(t, dir, s.f.opts), "engine: logdb/log/", "engine: logdb/head: head names sequence ")
	}
	if after := contents(t, dir); !maps.Equal(after, before) {
		t.Errorf("Inspect changed the data directory:\n%v\nwas\n%v", after, before)
	}
	// A daemon repairs it, and what it leaves is sound.
	srv, err := OpenDir(dir, s.f.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if after := contents(t, dir); maps.Equal(after, before) {
		t.Error("the daemon left the data directory as it was, which has a torn tail")
	}
	wantLines(t, problemLines(t, dir, s.f.opts))
}

// TestInspectReadOnly checks a data directory that the user cannot write in, whole or in part, and that nothing in it is
// opened for writing.
func TestInspectReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser writes where it likes")
	}
	s := newInspectStore(t)
	for _, damaged := range []bool{false, true} {
		t.Run(fmt.Sprintf("damaged %v", damaged), func(t *testing.T) {
			dir := cloneDir(t, s.copy)
			if damaged {
				editFile(t, filepath.Join(dir, diskPath("", s.hashes[0])), flipByte(0))
				editFile(t, segmentOf(t, dir, -1), func(b []byte) []byte { return b[:len(b)-5] })
			}
			// The files first, and then the directories, deepest first: one that cannot be entered cannot be walked.
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
			before := contents(t, dir)
			got := problemLines(t, dir, s.f.opts)
			if damaged {
				wantLines(t, got, "engine: logdb/log/", "engine: logdb/head: ", "original: ")
			} else {
				wantLines(t, got)
			}
			if after := contents(t, dir); !maps.Equal(after, before) {
				t.Errorf("Inspect changed the data directory:\n%v\nwas\n%v", after, before)
			}
		})
	}
}

// TestInspectUnreadable checks that a file and a directory that cannot be read are problems, with the error of the system
// and nothing of what they hold.
func TestInspectUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser reads where it likes")
	}
	s := newInspectStore(t)
	dir := cloneDir(t, s.copy)
	file := filepath.Join(dir, diskPath("", s.hashes[0]))
	other := filepath.Join(dir, diskPath("", s.hashes[1]))
	dirs := []string{filepath.Dir(file), filepath.Dir(other)}
	t.Cleanup(func() {
		for _, p := range append(dirs, file) {
			_ = os.Chmod(p, 0o700)
		}
	})
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	wantLines(t, problemLines(t, dir, s.f.opts), "original: "+diskPath("", s.hashes[0])+": cannot read: permission denied")
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if dirs[0] == dirs[1] {
		t.Skip("the two originals are in one directory")
	}
	for _, d := range dirs {
		if err := os.Chmod(d, 0); err != nil {
			t.Fatal(err)
		}
	}
	rel := func(p string) string { return strings.TrimPrefix(p, dir+"/") }
	want := []string{"original: " + rel(dirs[0]) + ": cannot read: permission denied", "original: " + rel(dirs[1]) +
		": cannot read: permission denied"}
	slices.Sort(want)
	wantLines(t, problemLines(t, dir, s.f.opts), want...)
}

// TestInspectNames checks that only the names of originals are looked at: the others, whatever they are, are not reported,
// and none is changed or removed.
func TestInspectNames(t *testing.T) {
	s := newInspectStore(t)
	dir := cloneDir(t, s.copy)
	notOriginals(t, dir)
	// A name of an original, in a directory that has another name, is not one.
	wantLines(t, problemLines(t, dir, s.f.opts))
	for _, name := range []string{"files/.tmp-0123456789abcdef", "files/README", "files/cd/" + strings.Repeat("ab", 32)} {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s: %v, want it left", name, err)
		}
	}
	// A symbolic link with the name of a directory of originals is not looked into.
	real := filepath.Join(dir, "files", "data-6e")
	mkdir(t, real)
	if err := os.WriteFile(filepath.Join(real, "6e"+strings.Repeat("ab", 31)), []byte("not an original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data-6e", filepath.Join(dir, "files", "6e")); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	wantLines(t, problemLines(t, dir, s.f.opts))
}

func TestInspectStops(t *testing.T) {
	s := newInspectStore(t)
	dir := cloneDir(t, s.copy)
	editFile(t, filepath.Join(dir, "logdb", "checkpoint"), flipByte(-3))
	editFile(t, filepath.Join(dir, diskPath("", s.hashes[0])), flipByte(0))
	editFile(t, filepath.Join(dir, diskPath("", s.hashes[1])), flipByte(0))
	all := problemLines(t, dir, s.f.opts)
	if len(all) != 3 {
		t.Fatalf("the problems are %q, want 3", all)
	}
	for stop := 1; stop <= 3; stop++ {
		t.Run(fmt.Sprintf("after %d", stop), func(t *testing.T) {
			var got []string
			err := Inspect(t.Context(), dir, s.f.opts, func(p Problem) bool {
				got = append(got, strings.ReplaceAll(p.String(), dir+"/", ""))
				return len(got) < stop
			})
			if err != nil {
				t.Fatalf("Inspect() = %v", err)
			}
			if !slices.Equal(got, all[:stop]) {
				t.Errorf("the problems are %q, want %q", got, all[:stop])
			}
		})
	}
}

// TestInspectContext checks that the end of the context ends Inspect with its error, in the middle of the check of the
// originals too, and that found is not called after it.
func TestInspectContext(t *testing.T) {
	s := newInspectStore(t)
	dir := cloneDir(t, s.copy)
	for _, h := range s.hashes {
		editFile(t, filepath.Join(dir, diskPath("", h)), flipByte(0))
	}
	t.Run("before it starts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		calls := 0
		err := Inspect(ctx, dir, s.f.opts, func(Problem) bool { calls++; return true })
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Errorf("Inspect() = %v after %d calls, want context.Canceled and none", err, calls)
		}
	})
	t.Run("in found", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		err := Inspect(ctx, dir, s.f.opts, func(Problem) bool { calls++; cancel(); return true })
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrInUse) || calls != 1 {
			t.Errorf("Inspect() = %v after %d calls, want context.Canceled and 1", err, calls)
		}
	})
	t.Run("a deadline in found", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		calls := 0
		err := Inspect(ctx, dir, s.f.opts, func(Problem) bool {
			calls++
			time.Sleep(300 * time.Millisecond)
			return true
		})
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
			t.Errorf("Inspect() = %v after %d calls, want context.DeadlineExceeded and 1", err, calls)
		}
	})
}

// TestInspectInUse checks that a data directory that a daemon has open is waited for for about two seconds, and that Inspect
// then fails and has changed nothing; and that a caller that ends sooner is not told that the directory is in use.
func TestInspectInUse(t *testing.T) {
	s := newInspectStore(t)
	before := contents(t, s.f.dir)
	state := s.f.dump()

	calls := 0
	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := Inspect(ctx, s.f.dir, s.f.opts, func(Problem) bool { calls++; return true })
	if elapsed := time.Since(start); elapsed < lockWait-100*time.Millisecond || elapsed > lockWait+3*time.Second {
		t.Errorf("Inspect() of a directory in use took %v, want about %v", elapsed, lockWait)
	}
	if want := s.f.dir + ": the data directory is in use"; !errors.Is(err, ErrInUse) || err.Error() != want {
		t.Errorf("Inspect() of a directory in use = %v, want %q, wrapping ErrInUse", err, want)
	}
	if calls != 0 {
		t.Errorf("Inspect() called found %d times for a directory in use", calls)
	}
	if after := contents(t, s.f.dir); !maps.Equal(after, before) {
		t.Errorf("Inspect() of a directory in use changed the files:\n%v\nwas\n%v", after, before)
	}
	s.f.unchanged(state)
	s.w.mustCommit(put(2, "after", "1")) // the daemon goes on

	canceled, cancelAfter := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancelAfter)
	timed, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer stop()
	for _, tt := range []struct {
		name string
		ctx  context.Context
		want error
	}{{"canceled", canceled, context.Canceled}, {"timed out", timed, context.DeadlineExceeded}} {
		start = time.Now()
		err = Inspect(tt.ctx, s.f.dir, s.f.opts, func(Problem) bool { return true })
		if !errors.Is(err, tt.want) || errors.Is(err, ErrInUse) {
			t.Errorf("Inspect() with a context that is %s = %v, want %v and not ErrInUse", tt.name, err, tt.want)
		}
		if elapsed := time.Since(start); elapsed > lockWait/2 {
			t.Errorf("Inspect() with a context that is %s after 100 ms took %v", tt.name, elapsed)
		}
	}
}

// waitsFor returns when a process waits for the processes that have the engine of the data directory dir open: it has
// lock/entry, which keeps the others that open it away.
func waitsFor(t testing.TB, dir string) {
	t.Helper()
	fsys, err := vfs.OpenRoot(filepath.Join(dir, "logdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	eventually(t, "the check to wait for the daemon", func() bool {
		unlock, err := fsys.Lock("lock/entry", vfs.Shared)
		if err == nil {
			_ = unlock()
			return false
		}
		if !errors.Is(err, vfs.ErrLocked) {
			t.Fatalf("Lock() = %v", err)
		}
		return true
	})
}

type inspected struct {
	lines []string
	err   error
}

// startInspect runs Inspect on dir in a goroutine.
func startInspect(t testing.TB, dir string, o Options) <-chan inspected {
	t.Helper()
	done := make(chan inspected, 1)
	go func() {
		ps, err := inspectAll(t.Context(), dir, o)
		done <- inspected{ps, err}
	}()
	return done
}

// TestInspectWaits checks that Inspect waits for the daemon, goes on when it has closed, and finds what the daemon has left.
func TestInspectWaits(t *testing.T) {
	s := newInspectStore(t)
	done := startInspect(t, s.f.dir, s.f.opts)
	waitsFor(t, s.f.dir)
	select {
	case r := <-done:
		t.Fatalf("Inspect() ended with %v, %v while the daemon had the directory open", r.lines, r.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.err != nil || len(r.lines) != 0 {
		t.Errorf("Inspect() after the daemon closed = %v, %v, want no problem", r.lines, r.err)
	}
}

// TestInspectHoldsLocks checks that the engine is closed to the others while found runs, and open again after Inspect.
func TestInspectHoldsLocks(t *testing.T) {
	s := newInspectStore(t)
	dir := cloneDir(t, s.copy)
	editFile(t, filepath.Join(dir, diskPath("", s.hashes[0])), flipByte(0))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- Inspect(t.Context(), dir, s.f.opts, func(Problem) bool {
			once.Do(func() { close(entered) })
			<-release
			return true
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("Inspect() = %v without a call of found", err)
	case <-time.After(waitLimit):
		t.Fatal("Inspect() did not call found")
	}
	if _, err := OpenDir(dir, s.f.opts); err == nil || err.Error() != dir+"/logdb: maintenance is running" {
		t.Errorf("OpenDir() while Inspect runs = %v, want maintenance is running", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	srv, err := OpenDir(dir, s.f.opts)
	if err != nil {
		t.Fatalf("OpenDir() after Inspect = %v", err)
	}
	_ = srv.Close()
}

// TestInspectMark checks the rule for the mark of a failed sync: the marks that Maintain refuses are refused with the same
// words, and the mark of an earlier boot is left where it is.
func TestInspectMark(t *testing.T) {
	const at, cause = "2026-11-15T09:29:00Z", "sync data/logdb/log/x.log: input/output error"
	valid := func(boot string) string { return markWith(boot, "time: "+at+"\n", "cause: "+cause+"\n") }
	s := newInspectStore(t)
	for _, tc := range []struct {
		name  string
		mark  string
		boot  string // of the machine
		error string // "": Inspect runs
	}{
		{"this boot", valid(testBoot), testBoot, "%s: a sync failed at " + at + " (" + cause + ")" + refusedTail},
		{"no time and no cause", markWith(testBoot), testBoot, "%s: a sync failed" + refusedTail},
		{"another boot", valid(testBoot), anotherBoot, ""},
		{"another boot, as large as is read", valid(testBoot) + strings.Repeat("x", maxMark-len(valid(testBoot))), anotherBoot, ""},
		{"empty", "", testBoot, "%s/stopped: the mark of a failed sync has no boot ID that this program can read; restart the " +
			"machine if it has not restarted since the sync failed, and then remove the mark"},
		{"too large", valid(testBoot) + strings.Repeat("x", maxMark+1-len(valid(testBoot))), anotherBoot,
			"%s/stopped: the mark of a failed sync has no boot ID that this program can read; restart the machine if it has " +
				"not restarted since the sync failed, and then remove the mark"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := cloneDir(t, s.copy)
			editFile(t, filepath.Join(dir, diskPath("", s.hashes[0])), flipByte(0))
			if err := os.WriteFile(filepath.Join(dir, "stopped"), []byte(tc.mark), 0o600); err != nil {
				t.Fatal(err)
			}
			before := contents(t, dir)
			o := s.f.opts
			o.Boot = machineWith(tc.boot)
			calls := 0
			err := Inspect(t.Context(), dir, o, func(Problem) bool { calls++; return true })
			if tc.error == "" {
				if err != nil || calls != 1 {
					t.Errorf("Inspect() = %v after %d problems, want the check of the data directory", err, calls)
				}
			} else {
				if want := fmt.Sprintf(tc.error, dir); err == nil || err.Error() != want {
					t.Errorf("Inspect() = %v, want %q", err, want)
				}
				if errors.Is(err, ErrNotRestarted) != strings.HasSuffix(tc.error, refusedTail) {
					t.Errorf("Inspect() = %v, wraps ErrNotRestarted: %v", err, errors.Is(err, ErrNotRestarted))
				}
				if calls != 0 {
					t.Errorf("Inspect() called found %d times for a directory that it refuses", calls)
				}
			}
			if after := contents(t, dir); !maps.Equal(after, before) {
				t.Errorf("Inspect() changed the data directory:\n%v\nwas\n%v", after, before)
			}
		})
	}

	t.Run("a mark that is a directory", func(t *testing.T) {
		dir := cloneDir(t, s.copy)
		mkdir(t, filepath.Join(dir, "stopped"))
		err := Inspect(t.Context(), dir, s.f.opts, func(Problem) bool { return true })
		if err == nil || !strings.HasSuffix(err.Error(), "the mark of a failed sync has no boot ID that this program can read; "+
			"restart the machine if it has not restarted since the sync failed, and then remove the mark") {
			t.Errorf("Inspect() = %v, want the refusal of a mark that cannot be read", err)
		}
	})

	t.Run("the boot ID cannot be read", func(t *testing.T) {
		dir := cloneDir(t, s.copy)
		before := contents(t, dir)
		o := s.f.opts
		o.Boot = func() (string, error) { return "", errors.New("no boot ID") }
		err := Inspect(t.Context(), dir, o, func(Problem) bool { return true })
		if want := "cannot read the boot ID of the machine: no boot ID; whiteroom-db needs it to know whether the machine " +
			"has restarted since a sync failed"; err == nil || err.Error() != want {
			t.Errorf("Inspect() = %v, want %q", err, want)
		}
		o.Boot = machineWith("not an ID")
		if err := Inspect(t.Context(), dir, o, func(Problem) bool { return true }); err == nil ||
			err.Error() != `the boot ID of the machine is not valid: "not an ID"` {
			t.Errorf("Inspect() with an ID that is not valid = %v", err)
		}
		if after := contents(t, dir); !maps.Equal(after, before) {
			t.Errorf("Inspect() changed the data directory:\n%v\nwas\n%v", after, before)
		}
	})
}

// TestInspectMarkInUse checks that the first look at the mark comes before the wait: a daemon that has the directory open is
// not waited for to refuse a mark of this boot.
func TestInspectMarkInUse(t *testing.T) {
	s := newInspectStore(t)
	text := markWith(testBoot, "time: 2026-11-15T09:29:00Z\n")
	if err := os.WriteFile(filepath.Join(s.f.dir, "stopped"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := Inspect(t.Context(), s.f.dir, s.f.opts, func(Problem) bool { return true })
	if !errors.Is(err, ErrNotRestarted) || errors.Is(err, ErrInUse) {
		t.Errorf("Inspect() = %v, want the refusal of the mark and not ErrInUse", err)
	}
	if elapsed := time.Since(start); elapsed > lockWait/2 {
		t.Errorf("Inspect() took %v to refuse the mark, want it at once", elapsed)
	}
}

// TestInspectMarkWhileWaiting checks that a daemon that stops after a failed sync while Inspect waits for it is not
// checked: what it wrote may be in the page cache only.
func TestInspectMarkWhileWaiting(t *testing.T) {
	s := newInspectStore(t)
	done := startInspect(t, s.f.dir, s.f.opts)
	waitsFor(t, s.f.dir)
	text := markWith(testBoot, "time: 2026-11-15T09:29:00Z\n", "cause: sync x: input/output error\n")
	if err := os.WriteFile(filepath.Join(s.f.dir, "stopped"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if want := s.f.dir + ": a sync failed at 2026-11-15T09:29:00Z (sync x: input/output error)" + refusedTail; r.err == nil ||
		r.err.Error() != want || !errors.Is(r.err, ErrNotRestarted) || len(r.lines) != 0 {
		t.Errorf("Inspect() = %v, %v, want the refusal %q and no problem", r.lines, r.err, want)
	}
	if got, err := os.ReadFile(filepath.Join(s.f.dir, "stopped")); err != nil || string(got) != text {
		t.Errorf("the mark is %q (%v), want it as it was", got, err)
	}
}

// TestInspectRefused checks the directories that are not data directories: an error that says what is wrong, and nothing
// made.
func TestInspectRefused(t *testing.T) {
	s := newInspectStore(t)
	for _, tc := range []struct {
		name string
		set  func(t *testing.T, dir string)
		want string
	}{
		{"no logdb", func(t *testing.T, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, "logdb")); err != nil {
				t.Fatal(err)
			}
		}, "%s/logdb: missing directory"},
		{"no files", func(t *testing.T, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, "files")); err != nil {
				t.Fatal(err)
			}
		}, "%s/files: missing directory"},
		{"logdb is a file", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "logdb")
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "%s/logdb: must be a directory"},
		{"files is a symbolic link", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "files")
			if err := os.Rename(p, p+"-moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+"-moved", p); err != nil {
				t.Skipf("no symbolic links: %v", err)
			}
		}, "%s/files: symbolic links are not allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := cloneDir(t, s.copy)
			tc.set(t, dir)
			before := contents(t, dir)
			calls := 0
			err := Inspect(t.Context(), dir, s.f.opts, func(Problem) bool { calls++; return true })
			if want := fmt.Sprintf(tc.want, dir); err == nil || err.Error() != want || calls != 0 {
				t.Errorf("Inspect() = %v after %d problems, want the error %q", err, calls, want)
			}
			if after := contents(t, dir); !maps.Equal(after, before) {
				t.Errorf("Inspect() changed the data directory:\n%v\nwas\n%v", after, before)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := Inspect(t.Context(), missing, s.f.opts, func(Problem) bool { return true }); err == nil {
		t.Error("Inspect() of a directory that is not there succeeded")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Errorf("Inspect() made %s: %v", missing, err)
	}
}

func TestInspectCtxReader(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	r := ctxReader{ctx, strings.NewReader("data")}
	buf := make([]byte, 2)
	if n, err := r.Read(buf); n != 2 || err != nil {
		t.Fatalf("Read() = %d, %v, want 2 bytes", n, err)
	}
	cancel()
	if n, err := r.Read(buf); n != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("Read() after the end of the context = %d, %v, want context.Canceled", n, err)
	}
}

func TestInspectProblemString(t *testing.T) {
	for _, tc := range []struct {
		p    Problem
		want string
	}{
		{Problem{AreaOriginal, "files/ab/cd", "must be a regular file"}, "original: files/ab/cd: must be a regular file"},
		{Problem{AreaEngine, "", "x/logdb/head: missing file"}, "engine: x/logdb/head: missing file"},
		{Problem{AreaEngine, "a\nb", "c\r\nd\re"}, "engine: a b: c d e"},
	} {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestInspectEngineProblem(t *testing.T) {
	for _, tc := range []struct {
		path   string
		offset int64
		want   Problem
	}{
		{"x/logdb/log/a.log", 64, Problem{AreaEngine, "x/logdb/log/a.log at offset 64", "r"}},
		{"x/logdb/log/a.log", 0, Problem{AreaEngine, "x/logdb/log/a.log at offset 0", "r"}},
		{"x/logdb/head", -1, Problem{AreaEngine, "x/logdb/head", "r"}},
		{"", 8, Problem{AreaEngine, "at offset 8", "r"}},
		{"", -1, Problem{AreaEngine, "", "r"}},
	} {
		if got := engineProblem(logdb.Problem{Path: tc.path, Offset: tc.offset, Reason: "r"}); got != tc.want {
			t.Errorf("engineProblem(%q, %d) = %+v, want %+v", tc.path, tc.offset, got, tc.want)
		}
	}
}
