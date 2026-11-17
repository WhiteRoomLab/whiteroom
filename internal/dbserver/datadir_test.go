package dbserver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
)

// firstSegment is the first segment of the log of an engine, in its directory.
const firstSegment = "log/0000000000000001-0000000000000001.log"

// logLayout is the modes of the files of a new data directory, by their paths in it. After commits and a close it has the
// checkpoint too.
func logLayout() map[string]fs.FileMode {
	const dirMode, fileMode = fs.ModeDir | 0o700, fs.FileMode(0o600)
	return map[string]fs.FileMode{
		".": dirMode, "files": dirMode, "logdb": dirMode, "logdb/head": fileMode, "logdb/lock": dirMode,
		"logdb/lock/entry": fileMode, "logdb/lock/readers": fileMode, "logdb/lock/writer": fileMode,
		"logdb/log": dirMode, "logdb/" + firstSegment: fileMode, "logdb/objects": dirMode,
	}
}

// modes returns the mode of each file and directory in dir, by its path in dir.
func modes(t testing.TB, dir string) map[string]fs.FileMode {
	t.Helper()
	m := map[string]fs.FileMode{}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		m[filepath.ToSlash(rel)] = info.Mode()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func remove(t testing.TB, name string) {
	t.Helper()
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
}

func rename(t testing.TB, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDir(t *testing.T) {
	l := logLayout()
	parent := filepath.Join(t.TempDir(), "parent")
	dir := filepath.Join(parent, "data")
	o := testOptions()
	if err := CreateDir(dir, o); err != nil {
		t.Fatalf("CreateDir() = %v", err)
	}
	if got, want := modeOf(t, parent), fs.ModeDir|0o700; got != want {
		t.Errorf("mode of %s = %v, want %v", parent, got, want)
	}
	if got := modes(t, dir); !maps.Equal(got, l) {
		t.Errorf("the data directory after CreateDir() has %v, want %v", got, l)
	}
	if got := names(t, filepath.Join(dir, "files")); got != nil {
		t.Errorf("files/ has %q, want nothing", got)
	}
	// The store has no commit and no schema row: the first commit of the writer makes the row.
	srv, err := OpenDir(dir, o)
	if err != nil {
		t.Fatalf("OpenDir() = %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got := modes(t, dir); !maps.Equal(got, l) {
		t.Errorf("the data directory after OpenDir() and Close() has %v, want %v", got, l)
	}

	// An empty directory keeps its mode.
	dir = filepath.Join(t.TempDir(), "data")
	mkdir(t, dir)
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := CreateDir(dir, o); err != nil {
		t.Fatalf("CreateDir() in an empty directory = %v", err)
	}
	if got := modeOf(t, dir); got != fs.ModeDir|0o750 {
		t.Errorf("mode of the empty directory after CreateDir() = %v, want %v", got, fs.ModeDir|0o750)
	}
	if srv, err = OpenDir(dir, o); err != nil {
		t.Fatalf("OpenDir() = %v", err)
	}
	g := &fixture{t: t, srv: srv}
	g.connect(dbwire.RoleWriter).mustCommit(put(2, "a", "1"))
	_ = srv.Close()
}

// TestCreateDirMakesDirs checks that CreateDir makes the directory of the data and its parents with fspath.MakeDirs, which
// syncs the names that it makes (the tests of fspath look at the syncs).
func TestCreateDirMakesDirs(t *testing.T) {
	var made []string
	makeDirs = func(dir string) error {
		made = append(made, dir)
		return fspath.MakeDirs(dir)
	}
	t.Cleanup(func() { makeDirs = fspath.MakeDirs })
	dir := filepath.Join(t.TempDir(), "a", "b", "data")
	if err := CreateDir(dir, testOptions()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(made, []string{dir}) {
		t.Errorf("CreateDir has made %q with MakeDirs, want %q", made, dir)
	}
	for _, d := range []string{filepath.Join(filepath.Dir(dir), ".."), filepath.Dir(dir), dir} {
		if got := modeOf(t, d); got != fs.ModeDir|0o700 {
			t.Errorf("mode of %s = %v, want %v", d, got, fs.ModeDir|0o700)
		}
	}
}

func TestCreateDirErrors(t *testing.T) {
	o := testOptions()
	notEmpty := t.TempDir()
	if err := os.WriteFile(filepath.Join(notEmpty, "README.md"), []byte("# Data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "data")
	if err := CreateDir(data, o); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{notEmpty, data} {
		before := names(t, dir)
		err := CreateDir(dir, o)
		if want := dir + ": not empty; a new data directory needs a missing or empty directory"; err == nil || err.Error() != want {
			t.Errorf("CreateDir(%q) = %v, want %q", dir, err, want)
		}
		if got := names(t, dir); !slices.Equal(got, before) {
			t.Errorf("CreateDir(%q) has changed the directory: %q, want %q", dir, got, before)
		}
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err, want := CreateDir(file, o), "mkdir "+file+": not a directory"; err == nil || err.Error() != want {
		t.Errorf("CreateDir() of a file = %v, want %q", err, want)
	}
}

// TestRelativeDataDir checks that CreateDir and OpenDir take relative paths in a working directory whose absolute path
// cannot be walked, as a directory in one that the user cannot search: they make the directories, and sync their names, from
// the working directory.
func TestRelativeDataDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can search every directory")
	}
	private := filepath.Join(t.TempDir(), "private")
	work := filepath.Join(private, "site")
	if err := os.MkdirAll(filepath.Join(work, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	if err := os.Chmod(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(private, 0o700) })
	if _, err := os.Stat(work); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("the working directory can be reached by its absolute path (%v)", err)
	}

	o := testOptions()
	for _, dir := range []string{"data", "empty", filepath.Join("a", "b", "data")} {
		if err := CreateDir(dir, o); err != nil {
			t.Fatalf("CreateDir(%q) = %v", dir, err)
		}
		srv, err := OpenDir(dir, o)
		if err != nil {
			t.Fatalf("OpenDir(%q) = %v", dir, err)
		}
		g := &fixture{t: t, srv: srv}
		g.connect(dbwire.RoleWriter).mustCommit(put(2, "a", "1"))
		if err := srv.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenDirErrors(t *testing.T) {
	removeLog := func(t *testing.T, path func(string) string) {
		t.Helper()
		if err := os.RemoveAll(path("logdb")); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, path func(string) string)
		file  string // the file that the error names, in the data directory
		want  string
	}{
		{"missing logdb", removeLog, "logdb", "missing directory"},
		{
			name: "logdb as a symbolic link",
			setup: func(t *testing.T, path func(string) string) {
				rename(t, path("logdb"), path("other"))
				symlink(t, "other", path("logdb"))
			},
			file: "logdb",
			want: "symbolic links are not allowed",
		},
		{
			name: "logdb as a file",
			setup: func(t *testing.T, path func(string) string) {
				removeLog(t, path)
				if err := os.WriteFile(path("logdb"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			file: "logdb",
			want: "must be a directory",
		},
		{
			name: "segment of another program",
			setup: func(t *testing.T, path func(string) string) {
				// Without the checkpoint, which bounds what Open reads, it reads the log from its first segment.
				_ = os.Remove(path("logdb/checkpoint"))
				f, err := os.OpenFile(path("logdb/"+firstSegment), os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteAt([]byte("SQLite format 3\x00"), 0)
				if err := errors.Join(err, f.Close()); err != nil {
					t.Fatal(err)
				}
			},
			file: "logdb/" + firstSegment,
			want: "corrupt: not a segment of whiteroom's log",
		},
		{
			name: "missing logdb and files",
			setup: func(t *testing.T, path func(string) string) {
				remove(t, path("files"))
				removeLog(t, path)
			},
			file: "logdb",
			want: "missing directory",
		},
		{
			name: "empty logdb",
			setup: func(t *testing.T, path func(string) string) {
				removeLog(t, path)
				mkdir(t, path("logdb"))
			},
			file: "logdb/lock",
			want: "missing directory",
		},
		{
			name:  "missing files",
			setup: func(t *testing.T, path func(string) string) { remove(t, path("files")) },
			file:  "files",
			want:  "missing directory",
		},
		{
			name: "files as a symbolic link",
			setup: func(t *testing.T, path func(string) string) {
				rename(t, path("files"), path("other"))
				symlink(t, "other", path("files"))
			},
			file: "files",
			want: "symbolic links are not allowed",
		},
		{
			name: "files as a file",
			setup: func(t *testing.T, path func(string) string) {
				remove(t, path("files"))
				if err := os.WriteFile(path("files"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			file: "files",
			want: "must be a directory",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if err := CreateDir(dir, testOptions()); err != nil {
				t.Fatal(err)
			}
			path := func(name string) string { return filepath.Join(dir, filepath.FromSlash(name)) }
			tt.setup(t, path)
			srv, err := OpenDir(dir, testOptions())
			if srv != nil {
				_ = srv.Close()
			}
			if want := path(tt.file) + ": " + tt.want; err == nil || err.Error() != want {
				t.Errorf("OpenDir() = %v, want %q", err, want)
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if srv, err := OpenDir(missing, testOptions()); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			_ = srv.Close()
		}
		t.Errorf("OpenDir() of a missing directory = %v, want an error that wraps fs.ErrNotExist", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat() of the missing directory after OpenDir() = %v, want fs.ErrNotExist", err)
	}
}

// TestOpenDirThroughLink opens the data directory data as link/../data, where link is a symbolic link to a directory beside
// data: the errors name the files of data, those of the daemon with the path as written, and those of the engine with the
// resolved path.
func TestOpenDirThroughLink(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(base, "data")
	if err := CreateDir(data, testOptions()); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(base, "beside"))
	work := t.TempDir()
	symlink(t, filepath.Join(base, "beside"), filepath.Join(work, "link"))
	dir := filepath.Join(work, "link") + filepath.FromSlash("/../data")

	// It opens, and files/ is that of data.
	srv, err := OpenDir(dir, testOptions())
	if err != nil {
		t.Fatalf("OpenDir(%s) = %v", dir, err)
	}
	h := mustPut(t, (&fixture{t: t, srv: srv}).connect(dbwire.RoleWriter), []byte("GIF89a"))
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(diskPath(data, h)); err != nil {
		t.Errorf("the original is not in data/files: %v", err)
	}

	_ = os.Remove(filepath.Join(data, "logdb", "checkpoint"))
	segment := filepath.Join(data, "logdb", filepath.FromSlash(firstSegment))
	if err := os.WriteFile(segment, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDir(dir, testOptions()); err == nil || !strings.HasPrefix(err.Error(), segment+": ") {
		t.Errorf("OpenDir(%s) of a broken segment = %v, want an error about %s", dir, err, segment)
	}
	if err := os.RemoveAll(filepath.Join(data, "logdb")); err != nil {
		t.Fatal(err)
	}
	want := dir + filepath.FromSlash("/logdb") + ": missing directory"
	if _, err := OpenDir(dir, testOptions()); err == nil || err.Error() != want {
		t.Errorf("OpenDir(%s) without logdb = %v, want %s", dir, err, want)
	}
}

// TestLogLayout checks the files of the objects and the originals that the daemon adds to a data directory: an object of
// the size from which the engine keeps it in a file, and an original.
func TestLogLayout(t *testing.T) {
	const dirMode, fileMode = fs.ModeDir | 0o700, fs.FileMode(0o600)
	f := newDirFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	want := maps.Clone(logLayout())
	object := strings.Repeat("o", 1000)
	seq := w.mustSnapshot()
	if _, err := w.putObject(object); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.commit(seq, put(2, "a", "1"), event("e")); err != nil {
		t.Fatal(err)
	}
	original := []byte("GIF89a")
	oh := mustPut(t, w, original)
	oid := sha256.Sum256([]byte(object))
	for parent, h := range map[string][32]byte{"logdb/objects": oid, "files": oh} {
		digits := fmt.Sprintf("%x", h)
		want[parent+"/"+digits[:2]], want[parent+"/"+digits[:2]+"/"+digits] = dirMode, fileMode
	}
	if got := modes(t, f.dir); !maps.Equal(got, want) {
		t.Errorf("the data directory after the writes has %v, want %v", got, want)
	}
	if err := f.srv.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	want["logdb/checkpoint"] = fileMode
	if got := modes(t, f.dir); !maps.Equal(got, want) {
		t.Errorf("the data directory after Close() has %v, want %v", got, want)
	}
}

// TestCloseReleasesFiles checks that Close, and an open that fails, close the files of the daemon.
func TestCloseReleasesFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("counts the files in /proc/self/fd")
	}
	files := func() int {
		t.Helper()
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	dir := filepath.Join(t.TempDir(), "data")
	o := testOptions()
	o.Log.WriterWait = 10 * time.Millisecond
	if err := CreateDir(dir, o); err != nil {
		t.Fatal(err)
	}
	cycle := func() {
		t.Helper()
		srv, err := OpenDir(dir, o)
		if err != nil {
			t.Fatalf("OpenDir() = %v", err)
		}
		g := &fixture{t: t, srv: srv}
		g.connect(dbwire.RoleWriter).mustCommit(put(2, "a", "1"))
		// A second writer fails.
		if second, err := OpenDir(dir, o); err == nil {
			_ = second.Close()
			t.Error("a second OpenDir() works")
		}
		if err := srv.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
	}
	cycle() // the first may make the runtime open files of its own
	before := files()
	cycle()
	cycle()
	if after := files(); after != before {
		t.Errorf("the process has %d open files after the servers have closed, want %d", after, before)
	}

	// An open that cannot remove a temporary file.
	if os.Geteuid() != 0 {
		temp := filepath.Join(dir, "files", ".tmp-0123456789abcdef")
		if err := os.WriteFile(temp, []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(temp), 0o500); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if srv, err := OpenDir(dir, o); err == nil {
				_ = srv.Close()
				t.Fatal("OpenDir() with a temporary file that cannot be removed works")
			}
		}
		if err := os.Chmod(filepath.Dir(temp), 0o700); err != nil {
			t.Fatal(err)
		}
		if after := files(); after != before {
			t.Errorf("the process has %d open files after the opens that failed, want %d", after, before)
		}
	}
}

// TestOpenDirFreeSpace checks that OpenDir takes the free space of the file system of the data directory, unless the
// options have a function for it, and that it checks nothing where the system does not tell it.
func TestOpenDirFreeSpace(t *testing.T) {
	t.Run("the free space of the file system", func(t *testing.T) {
		f := newDirFixture(t, func(o *Options) { o.Limits.MinFree = 1 << 60 })
		w := f.connect(dbwire.RoleWriter)
		w.mustSnapshot()
		_, err := w.putObject("object")
		if !spaceChecked {
			if err != nil {
				t.Errorf("PutObject = %v, want it accepted where the free space is not known", err)
			}
			return
		}
		re := wantCode(t, err, dbwire.CodeNoSpace)
		if !strings.Contains(re.Msg, "must leave 1152921504606846976") {
			t.Errorf("the text is %q", re.Msg)
		}
	})
	t.Run("the free inodes of the file system", func(t *testing.T) {
		f := newDirFixture(t, func(o *Options) { o.Limits.MinFreeInodes = 1 << 60 })
		w := f.connect(dbwire.RoleWriter)
		w.mustSnapshot()
		_, err := w.putObject("object")
		if !spaceChecked || !inodesCounted(t, f.dir) {
			if err != nil {
				t.Errorf("PutObject = %v, want it accepted where the free inodes are not known", err)
			}
			return
		}
		re := wantCode(t, err, dbwire.CodeNoSpace)
		if !strings.HasPrefix(re.Msg, "the free inodes are ") || !strings.HasSuffix(re.Msg, " must leave 1152921504606846976") {
			t.Errorf("the text is %q", re.Msg)
		}
	})
	t.Run("a function in the options", func(t *testing.T) {
		f := newDirFixture(t, func(o *Options) {
			o.Limits.MinFree, o.Limits.MinFreeInodes = 1<<60, 1<<60
			o.FreeSpace = func() (Space, error) { return Space{Bytes: 1 << 62, Inodes: 1 << 62, HasInodes: true}, nil }
		})
		w := f.connect(dbwire.RoleWriter)
		w.mustSnapshot()
		if _, err := w.putObject("object"); err != nil {
			t.Errorf("PutObject = %v", err)
		}
	})
	t.Run("a limit that is off", func(t *testing.T) {
		f := newDirFixture(t, func(o *Options) { o.Limits.MinFree = -1 })
		w := f.connect(dbwire.RoleWriter)
		w.mustSnapshot()
		if _, err := w.putObject("object"); err != nil {
			t.Errorf("PutObject = %v", err)
		}
	})
	t.Run("the function", func(t *testing.T) {
		if !spaceChecked {
			t.Skip("the free space is not known on this system")
		}
		dir := t.TempDir()
		free, err := freeSpace(dir)
		if err != nil || free.Bytes == 0 || free.HasInodes != inodesCounted(t, dir) || free.HasInodes && free.Inodes == 0 {
			t.Errorf("freeSpace() = %+v, %v", free, err)
		}
		missing := filepath.Join(t.TempDir(), "missing")
		_, err = freeSpace(missing)
		if want := "statfs " + missing + ": no such file or directory"; err == nil || err.Error() != want {
			t.Errorf("freeSpace() of a missing directory = %v, want %q", err, want)
		}
		if !errors.Is(err, syscall.ENOENT) {
			t.Errorf("the error %v does not wrap ENOENT", err)
		}
	})
}
