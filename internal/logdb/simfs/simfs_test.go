package simfs_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// recorder logs what operations return, in the same words for every file system.
type recorder struct {
	lines []string
}

func (r *recorder) add(format string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func describe(err error) string {
	if err == nil {
		return "ok"
	}
	s := fmt.Sprintf("%T %q", err, err.Error())
	for _, kind := range []error{fs.ErrNotExist, fs.ErrExist, fs.ErrClosed, vfs.ErrLocked, io.EOF} {
		if errors.Is(err, kind) {
			s += fmt.Sprintf(" is(%v)", kind)
		}
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		s += fmt.Sprintf(" path(%s %s %T)", pe.Op, pe.Path, pe.Err)
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		s += fmt.Sprintf(" link(%s %s %s %T)", le.Op, le.Old, le.New, le.Err)
	}
	return s
}

func (r *recorder) err(what string, err error) {
	r.add("%s: %s", what, describe(err))
}

func (r *recorder) open(fsys vfs.FS, name string, flag int, perm fs.FileMode) vfs.File {
	f, err := fsys.OpenFile(name, flag, perm)
	r.err(fmt.Sprintf("open %q %#x %v", name, flag, perm), err)
	return f
}

func (r *recorder) info(what string) func(fs.FileInfo, error) {
	return func(info fs.FileInfo, err error) {
		if err != nil {
			r.err(what, err)
			return
		}
		size := "-"
		if !info.IsDir() {
			size = fmt.Sprint(info.Size())
		}
		r.add("%s: %q %v %v %s", what, info.Name(), info.Mode(), info.IsDir(), size)
	}
}

func (r *recorder) entries(what string) func([]fs.DirEntry, error) {
	return func(entries []fs.DirEntry, err error) {
		var names []string
		for _, e := range entries {
			names = append(names, fmt.Sprintf("%s %v %v", e.Name(), e.IsDir(), e.Type()))
		}
		slices.Sort(names)
		r.add("%s: %q nil=%v %s", what, names, entries == nil, describe(err))
	}
}

func (r *recorder) read(what string, f vfs.File, n int) {
	buf := make([]byte, n)
	k, err := f.Read(buf)
	r.add("%s: %d %q %s", what, k, buf[:k], describe(err))
}

func (r *recorder) readAt(what string, f vfs.File, n int, off int64) {
	buf := make([]byte, n)
	k, err := f.ReadAt(buf, off)
	r.add("%s: %d %q %s", what, k, buf[:k], describe(err))
}

func (r *recorder) write(what string, f vfs.File, data string) {
	n, err := f.Write([]byte(data))
	r.add("%s: %d %s", what, n, describe(err))
}

func (r *recorder) writeAt(what string, f vfs.File, data string, off int64) {
	n, err := f.WriteAt([]byte(data), off)
	r.add("%s: %d %s", what, n, describe(err))
}

func (r *recorder) seek(what string, f vfs.File, off int64, whence int) {
	n, err := f.Seek(off, whence)
	r.add("%s: %d %s", what, n, describe(err))
}

// tree logs every directory and file of fsys, with the data of the files.
func (r *recorder) tree(fsys vfs.FS) {
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			r.err("tree "+dir, err)
			return
		}
		for _, e := range entries {
			name := path.Join(dir, e.Name())
			info, err := e.Info()
			if err != nil {
				r.err("tree "+name, err)
				continue
			}
			if e.IsDir() {
				r.add("tree %s/ %v", name, info.Mode())
				walk(name)
				continue
			}
			f, err := fsys.Open(name)
			if err != nil {
				r.err("tree "+name, err)
				continue
			}
			data, err := io.ReadAll(f)
			r.add("tree %s %v %q %s %s", name, info.Mode(), data, describe(err), describe(f.Close()))
		}
	}
	walk(".")
}

// setup makes d/, d/e/ and f, which has "hello".
func setup(r *recorder, fsys vfs.FS) {
	r.err("mkdir d", fsys.Mkdir("d", 0o700))
	r.err("mkdir d/e", fsys.Mkdir("d/e", 0o700))
	f := r.open(fsys, "f", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if f != nil {
		r.write("write f", f, "hello")
		r.err("close f", f.Close())
	}
}

var likeOS = []struct {
	name string
	run  func(r *recorder, fsys vfs.FS)
}{
	{"files", func(r *recorder, fsys vfs.FS) {
		f := r.open(fsys, "g", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		r.write("write", f, "hello")
		r.seek("seek start", f, 0, io.SeekStart)
		r.read("read", f, 10)
		r.read("read at the end", f, 10)
		r.read("read nothing", f, 0)
		r.writeAt("writeat past the end", f, "ab", 10)
		r.readAt("readat", f, 20, 0)
		r.readAt("readat at the end", f, 4, 12)
		r.readAt("readat past the end", f, 4, 100)
		r.readAt("readat nothing", f, 0, 100)
		r.info("stat")(f.Stat())
		r.write("write at the offset", f, "xy")
		r.seek("seek current", f, 0, io.SeekCurrent)
		r.seek("seek end", f, -2, io.SeekEnd)
		r.seek("seek past the end", f, 100, io.SeekStart)
		r.read("read past the end", f, 4)
		r.write("write past the end", f, "z")
		r.info("stat")(f.Stat())
		r.err("truncate", f.Truncate(3))
		r.info("stat")(f.Stat())
		r.err("truncate up", f.Truncate(6))
		r.readAt("readat", f, 10, 0)
		r.write("write nothing", f, "")
		r.writeAt("writeat nothing past the end", f, "", 50)
		r.info("stat")(f.Stat())
		r.err("sync", f.Sync())
		r.err("close", f.Close())
		r.err("close again", f.Close())
		r.read("read closed", f, 1)
		r.read("read nothing closed", f, 0)
		r.readAt("readat closed", f, 1, 0)
		r.readAt("readat nothing closed", f, 0, 0)
		r.write("write closed", f, "x")
		r.writeAt("writeat closed", f, "x", 0)
		r.writeAt("writeat nothing closed", f, "", 0)
		r.err("sync closed", f.Sync())
		r.err("truncate closed", f.Truncate(0))
		r.seek("seek closed", f, 0, io.SeekStart)
		r.info("stat closed")(f.Stat())
		r.entries("readdir closed")(f.ReadDir(-1))
		f = r.open(fsys, "g", os.O_RDWR|os.O_TRUNC, 0)
		r.info("stat after O_TRUNC")(f.Stat())
		r.err("close", f.Close())
		f = r.open(fsys, "g", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		r.write("write", f, "again")
		r.err("close", f.Close())
		f = r.open(fsys, "g", os.O_RDWR|os.O_CREATE, 0o600)
		r.read("read without O_TRUNC", f, 10)
		r.err("close", f.Close())
		f = r.open(fsys, "g", os.O_RDONLY|os.O_TRUNC, 0)
		r.info("stat after O_RDONLY|O_TRUNC")(f.Stat())
		r.err("close", f.Close())
		big := strings.Repeat("0123456789", 1000)
		f = r.open(fsys, "big", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		r.writeAt("writeat big", f, big, 100)
		r.readAt("readat big", f, 10, 9000)
		r.info("stat big")(f.Stat())
		r.err("close big", f.Close())
	}},
	{"open", func(r *recorder, fsys vfs.FS) {
		for _, name := range []string{"missing", "missing/x", "f/x", "f/", "d/", "", "/etc", "..", "../x", "d/../f",
			"missing/../f", "f/../f", "d/../../x", "./f", "d//e", ".", "./", "d/./e/", "d/e/..", "d/e/../..", "d/e/../../.."} {
			f, err := fsys.Open(name)
			r.err(fmt.Sprintf("open %q", name), err)
			if err == nil {
				r.info("stat")(f.Stat())
				r.err("close", f.Close())
			}
		}
	}},
	{"create", func(r *recorder, fsys vfs.FS) {
		for _, c := range []struct {
			name string
			flag int
			perm fs.FileMode
		}{
			{"f", os.O_RDWR | os.O_CREATE | os.O_EXCL, 0o600},
			{"d", os.O_RDWR | os.O_CREATE | os.O_EXCL, 0o600},
			{"d", os.O_RDONLY | os.O_CREATE | os.O_EXCL, 0o600},
			{".", os.O_RDONLY | os.O_CREATE | os.O_EXCL, 0o600},
			{"d", os.O_RDWR, 0},
			{"d", os.O_WRONLY, 0},
			{"d", os.O_RDONLY | os.O_CREATE, 0o600},
			{"d", os.O_RDONLY | os.O_TRUNC, 0},
			{".", os.O_RDWR | os.O_CREATE, 0o600},
			{"./", os.O_RDWR | os.O_CREATE, 0o600},
			{"x", os.O_RDWR | os.O_CREATE, 0o1600},
			{"missing/x", os.O_RDWR | os.O_CREATE, 0o600},
			{"f/x", os.O_RDWR | os.O_CREATE, 0o600},
			{"new/", os.O_RDWR | os.O_CREATE, 0o600},
			{"../x", os.O_RDWR | os.O_CREATE, 0o600},
			{"missing", os.O_RDWR, 0},
			{"d/new", os.O_RDWR | os.O_CREATE | os.O_EXCL, 0o600},
			{"d/new", os.O_RDWR | os.O_CREATE, 0o600},
			{"d/e/../new2", os.O_WRONLY | os.O_CREATE | os.O_EXCL, 0o600},
		} {
			if f := r.open(fsys, c.name, c.flag, c.perm); f != nil {
				r.info("stat")(f.Stat())
				r.err("close", f.Close())
			}
		}
	}},
	{"mkdir", func(r *recorder, fsys vfs.FS) {
		for _, name := range []string{"d", "f", "missing/x", "f/x", ".", "t/", "d/e/", "../zz", "d/new", "d/e/../new2"} {
			r.err(fmt.Sprintf("mkdir %q", name), fsys.Mkdir(name, 0o700))
		}
		r.err("mkdir with a bad mode", fsys.Mkdir("x", 0o1700))
	}},
	{"remove", func(r *recorder, fsys vfs.FS) {
		r.err("mkdir empty", fsys.Mkdir("empty", 0o700))
		r.err("mkdir empty2", fsys.Mkdir("empty2", 0o700))
		for _, name := range []string{"missing", "missing/x", "d", ".", "d/e/..", "f/", "f/x", "empty/", "empty2", "d/e", "f", "f", "../x"} {
			r.err(fmt.Sprintf("remove %q", name), fsys.Remove(name))
		}
	}},
	{"lstat", func(r *recorder, fsys vfs.FS) {
		for _, name := range []string{"missing", "f", "d/e", ".", "d/", "f/", "d/e/..", "d/.", "d/../f", "../f", ""} {
			r.info(fmt.Sprintf("lstat %q", name))(fsys.Lstat(name))
		}
	}},
	{"rename errors", func(r *recorder, fsys vfs.FS) {
		r.err("mkdir empty", fsys.Mkdir("empty", 0o700))
		for _, c := range [][2]string{{"missing", "x"}, {"f", "missing/x"}, {"f", "f/x"}, {"f", "d"}, {"d", "empty"},
			{"d", "f"}, {"d", "d/e/x"}, {"d", "d/x"}, {".", "x"}, {"f", "."}, {"f", "f"}, {"d", "d"}, {"f", "new/"},
			{"f/", "g"}, {"missing/", "g"}, {"f", "../x"}, {"../f", "x"}, {"missing", "d"}, {"d/e", "d/e"}} {
			r.err(fmt.Sprintf("rename %q %q", c[0], c[1]), fsys.Rename(c[0], c[1]))
		}
	}},
	{"rename", func(r *recorder, fsys vfs.FS) {
		a := r.open(fsys, "a", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		r.write("write a", a, "new")
		r.err("rename a b", fsys.Rename("a", "b"))
		r.write("write to the renamed file", a, "!")
		r.err("close a", a.Close())
		r.err("rename b f", fsys.Rename("b", "f"))
		r.err("rename f d/g", fsys.Rename("f", "d/g"))
		r.err("rename d/e e2", fsys.Rename("d/e", "e2"))
		r.err("rename d/ dd/", fsys.Rename("d/", "dd/"))
		r.err("rename e2 dd/e3/", fsys.Rename("e2", "dd/e3/"))
		r.err("rename dd/e3 dd/e3", fsys.Rename("dd/e3", "dd/e3"))
		r.err("rename dd/g dd/./h", fsys.Rename("dd/g", "dd/./h"))
	}},
	{"handles", func(r *recorder, fsys vfs.FS) {
		f := r.open(fsys, "f", os.O_RDWR, 0)
		r.readAt("readat negative", f, 4, -1)
		r.writeAt("writeat negative", f, "x", -1)
		r.err("truncate negative", f.Truncate(-1))
		r.seek("seek negative", f, -1, io.SeekStart)
		r.seek("seek bad whence", f, 0, 7)
		r.entries("readdir a file")(f.ReadDir(-1))
		r.entries("readdir 1 of a file")(f.ReadDir(1))
		r.err("close", f.Close())
		ro := r.open(fsys, "f", os.O_RDONLY, 0)
		r.write("write read-only", ro, "x")
		r.write("write nothing read-only", ro, "")
		r.writeAt("writeat read-only", ro, "x", 0)
		r.err("truncate read-only", ro.Truncate(0))
		r.err("sync read-only", ro.Sync())
		r.read("read read-only", ro, 2)
		r.err("close", ro.Close())
		wo := r.open(fsys, "f", os.O_WRONLY, 0)
		r.read("read write-only", wo, 1)
		r.readAt("readat write-only", wo, 1, 0)
		r.write("write write-only", wo, "H")
		r.err("close", wo.Close())
		d := r.open(fsys, "d", os.O_RDONLY, 0)
		r.read("read a directory", d, 1)
		r.readAt("readat a directory", d, 1, 0)
		r.write("write a directory", d, "x")
		r.err("truncate a directory", d.Truncate(0))
		r.err("sync a directory", d.Sync())
		r.info("stat a directory")(d.Stat())
		r.entries("readdir 1")(d.ReadDir(1))
		r.entries("readdir 1 at the end")(d.ReadDir(1))
		r.entries("readdir -1 at the end")(d.ReadDir(-1))
		r.seek("seek 0", d, 0, io.SeekStart)
		r.entries("readdir 0 after seek")(d.ReadDir(0))
		r.err("close", d.Close())
		root := r.open(fsys, ".", os.O_RDONLY, 0)
		r.info("stat .")(root.Stat())
		r.entries("readdir . 2")(root.ReadDir(2))
		r.entries("readdir . 2")(root.ReadDir(2))
		r.entries("readdir . 2")(root.ReadDir(2))
		r.err("close", root.Close())
	}},
	{"sub", func(r *recorder, fsys vfs.FS) {
		sub, err := fsys.Sub("d")
		r.err("sub d", err)
		r.add("name %s", strings.TrimPrefix(sub.Name(), fsys.Name()))
		_, err = sub.Open("..")
		r.err("sub open ..", err)
		_, err = sub.Open("../f")
		r.err("sub open ../f", err)
		sub2, err := sub.Sub("e")
		r.err("sub e", err)
		r.add("name %s", strings.TrimPrefix(sub2.Name(), fsys.Name()))
		if f := r.open(sub2, "x", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); f != nil {
			r.write("write", f, "in sub")
			r.err("close", f.Close())
		}
		r.info("lstat d/e/x")(fsys.Lstat("d/e/x"))
		r.err("close sub2", sub2.Close())
		r.err("close sub", sub.Close())
		_, err = fsys.Sub("f")
		r.err("sub f", err)
		_, err = fsys.Sub("missing")
		r.err("sub missing", err)
		_, err = fsys.Sub("..")
		r.err("sub ..", err)
		sub3, err := fsys.Sub("d/./e/")
		r.err("sub d/./e/", err)
		r.add("name %s", strings.TrimPrefix(sub3.Name(), fsys.Name()))
		r.err("close sub3", sub3.Close())
		dot, err := fsys.Sub(".")
		r.err("sub .", err)
		r.add("name %s", strings.TrimPrefix(dot.Name(), fsys.Name()))
		r.entries("readdir in sub .")(dot.ReadDir("."))
		r.err("close dot", dot.Close())
	}},
	{"closed", func(r *recorder, fsys vfs.FS) {
		f := r.open(fsys, "f", os.O_RDWR, 0)
		sub, err := fsys.Sub("d")
		r.err("sub", err)
		r.err("close the root", fsys.Close())
		r.err("close the root again", fsys.Close())
		_, err = fsys.Open("f")
		r.err("open", err)
		_, err = fsys.OpenFile("n", os.O_RDWR|os.O_CREATE, 0o600)
		r.err("create", err)
		r.err("mkdir", fsys.Mkdir("x", 0o700))
		r.err("rename", fsys.Rename("f", "g"))
		r.err("remove", fsys.Remove("f"))
		r.info("lstat")(fsys.Lstat("f"))
		r.entries("readdir")(fsys.ReadDir("."))
		r.err("syncdir", fsys.SyncDir("."))
		_, err = fsys.Sub("d")
		r.err("sub", err)
		_, err = fsys.Lock("f", vfs.Shared)
		r.err("lock", err)
		r.write("write to a file of a closed root", f, "J")
		r.err("close the file", f.Close())
		r.entries("readdir in the sub of a closed root")(sub.ReadDir("."))
		r.err("close the sub", sub.Close())
	}},
	{"unlinked", func(r *recorder, fsys vfs.FS) {
		f := r.open(fsys, "f", os.O_RDWR, 0)
		g := r.open(fsys, "f", os.O_RDONLY, 0)
		r.err("remove", fsys.Remove("f"))
		r.write("write", f, "abc")
		r.err("sync", f.Sync())
		r.info("stat")(f.Stat())
		r.read("read through another file", g, 10)
		r.err("close", f.Close())
		r.err("close", g.Close())
		r.info("lstat")(fsys.Lstat("f"))
	}},
	{"removed directory", func(r *recorder, fsys vfs.FS) {
		r.err("mkdir", fsys.Mkdir("gone", 0o700))
		sub, err := fsys.Sub("gone")
		r.err("sub", err)
		r.err("remove", fsys.Remove("gone"))
		_, err = sub.OpenFile("x", os.O_RDWR|os.O_CREATE, 0o600)
		r.err("create in it", err)
		r.err("mkdir in it", sub.Mkdir("x", 0o700))
		r.err("rename into it", fsys.Rename("f", "gone/f"))
		d, err := sub.Open(".")
		r.err("open .", err)
		if d != nil {
			r.entries("readdir")(d.ReadDir(-1))
			r.err("close", d.Close())
		}
		r.entries("readdir .")(sub.ReadDir("."))
		r.info("lstat .")(sub.Lstat("."))
		r.err("syncdir .", sub.SyncDir("."))
		r.err("close", sub.Close())
	}},
	{"renamed directory", func(r *recorder, fsys vfs.FS) {
		sub, err := fsys.Sub("d")
		r.err("sub", err)
		r.err("rename", fsys.Rename("d", "d2"))
		r.add("name %s", strings.TrimPrefix(sub.Name(), fsys.Name()))
		if f := r.open(sub, "x", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); f != nil {
			r.err("close", f.Close())
		}
		r.info("lstat d2/x")(fsys.Lstat("d2/x"))
		r.err("close", sub.Close())
	}},
	{"readdir and syncdir", func(r *recorder, fsys vfs.FS) {
		for _, name := range []string{"x2", "x1", "d/x3"} {
			if f := r.open(fsys, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); f != nil {
				r.err("close", f.Close())
			}
		}
		for _, name := range []string{".", "d", "d/", "d/e", "f", "missing", "f/", "../x"} {
			r.entries(fmt.Sprintf("readdir %q", name))(fsys.ReadDir(name))
			r.err(fmt.Sprintf("syncdir %q", name), fsys.SyncDir(name))
		}
	}},
	{"locks", func(r *recorder, fsys vfs.FS) {
		if f := r.open(fsys, "lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); f != nil {
			r.err("close", f.Close())
		}
		lock := func(what, name string, mode vfs.LockMode) func() error {
			unlock, err := fsys.Lock(name, mode)
			r.err(what, err)
			return unlock
		}
		lock("lock missing", "missing", vfs.Shared)
		lock("lock with a bad mode", "lock", vfs.LockMode(5))
		ex := lock("exclusive", "lock", vfs.Exclusive)
		lock("shared while exclusive", "lock", vfs.Shared)
		lock("exclusive while exclusive", "lock", vfs.Exclusive)
		r.err("unlock", ex())
		r.err("unlock again", ex())
		s1 := lock("shared", "lock", vfs.Shared)
		s2 := lock("shared again", "lock", vfs.Shared)
		lock("exclusive while shared", "lock", vfs.Exclusive)
		r.err("unlock", s1())
		lock("exclusive while shared", "lock", vfs.Exclusive)
		r.err("unlock", s2())
		ex = lock("exclusive", "lock", vfs.Exclusive)
		r.err("unlock", ex())
		d := lock("lock a directory", "d", vfs.Exclusive)
		lock("lock a directory again", "d", vfs.Shared)
		r.err("unlock", d())
		r.err("rename the lock file", fsys.Rename("lock", "lock2"))
	}},
}

// TestLikeOS runs the same operations on the OS and on simfs, which must give the same results, errors and files.
func TestLikeOS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("simfs behaves as Linux does")
	}
	for _, tt := range likeOS {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			osfs, err := vfs.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = osfs.Close() }()
			run := func(fsys vfs.FS) []string {
				var r recorder
				setup(&r, fsys)
				view, err := fsys.Sub(".")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = view.Close() }()
				tt.run(&r, fsys)
				r.tree(view)
				return r.lines
			}
			want := run(osfs)
			sim := simfs.New(dir)
			got := run(sim)
			for i := range max(len(got), len(want)) {
				var g, w string
				if i < len(got) {
					g = got[i]
				}
				if i < len(want) {
					w = want[i]
				}
				if g != w {
					t.Errorf("line %d:\nsimfs: %s\nos:    %s", i, g, w)
				}
			}
		})
	}
}

func mustCreate(t *testing.T, fsys vfs.FS, name, data string) {
	t.Helper()
	f, err := fsys.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustWriteAt(t *testing.T, fsys vfs.FS, name string, off int64, data string) {
	t.Helper()
	f, err := fsys.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(data), off); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustTruncate(t *testing.T, fsys vfs.FS, name string, size int64) {
	t.Helper()
	f, err := fsys.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Truncate(size), f.Close()); err != nil {
		t.Fatal(err)
	}
}

func mustSync(t *testing.T, fsys vfs.FS, name string) {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		t.Fatal(err)
	}
}

func mustSyncDir(t *testing.T, fsys vfs.FS, name string) {
	t.Helper()
	if err := fsys.SyncDir(name); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, fsys vfs.FS, name string) {
	t.Helper()
	if err := fsys.Mkdir(name, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustRename(t *testing.T, fsys vfs.FS, oldname, newname string) {
	t.Helper()
	if err := fsys.Rename(oldname, newname); err != nil {
		t.Fatal(err)
	}
}

// files returns every directory ("d/") and file ("f=data") of fsys, in the order of their paths.
func files(t *testing.T, fsys vfs.FS) string {
	t.Helper()
	var all []string
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := path.Join(dir, e.Name())
			if e.IsDir() {
				all = append(all, name+"/")
				walk(name)
				continue
			}
			f, err := fsys.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(f)
			if err := errors.Join(err, f.Close()); err != nil {
				t.Fatal(err)
			}
			all = append(all, name+"="+string(data))
		}
	}
	walk(".")
	return strings.Join(all, " ")
}

var loseAll = simfs.Model{Name: "lose-all"}

func keepAll(fsys *simfs.FS) simfs.Model {
	m := simfs.Model{Name: "keep-all", Keep: make([]bool, len(fsys.Pending()))}
	for i := range m.Keep {
		m.Keep[i] = true
	}
	return m
}

// keep returns the model that keeps the pending changes with a 1 in bits.
func keep(t *testing.T, fsys *simfs.FS, bits string) simfs.Model {
	t.Helper()
	if n := len(fsys.Pending()); len(bits) != n {
		t.Fatalf("%d bits for %d pending changes: %s", len(bits), n, pending(fsys))
	}
	m := simfs.Model{Name: bits, Keep: make([]bool, len(bits))}
	for i, b := range bits {
		m.Keep[i] = b == '1'
	}
	return m
}

func pending(fsys *simfs.FS) string {
	var p []string
	for _, u := range fsys.Pending() {
		p = append(p, u.String())
	}
	return strings.Join(p, "; ")
}

func checkPending(t *testing.T, fsys *simfs.FS, want string) {
	t.Helper()
	if got := pending(fsys); got != want {
		t.Errorf("Pending: %s\nwant:    %s", got, want)
	}
}

func checkCrash(t *testing.T, fsys *simfs.FS, m simfs.Model, want string) {
	t.Helper()
	if got := files(t, fsys.Crash(m)); got != want {
		t.Errorf("Crash(%s): %q, want %q", m.Name, got, want)
	}
}

func TestNameNeedsSyncDir(t *testing.T) {
	fsys := simfs.New("/data")
	mustCreate(t, fsys, "f", "data")
	mustSync(t, fsys, "f")
	checkPending(t, fsys, "entry f")
	checkCrash(t, fsys, loseAll, "")
	checkCrash(t, fsys, keepAll(fsys), "f=data")
	mustSyncDir(t, fsys, ".")
	checkPending(t, fsys, "")
	checkCrash(t, fsys, loseAll, "f=data")

	mustMkdir(t, fsys, "d")
	mustCreate(t, fsys, "d/g", "in d")
	mustSync(t, fsys, "d/g")
	mustSyncDir(t, fsys, "d")
	checkPending(t, fsys, "entry d")
	checkCrash(t, fsys, loseAll, "f=data")
	checkCrash(t, fsys, keepAll(fsys), "d/ d/g=in d f=data")
	mustSyncDir(t, fsys, ".")
	checkCrash(t, fsys, loseAll, "d/ d/g=in d f=data")

	if err := fsys.Remove("f"); err != nil {
		t.Fatal(err)
	}
	mustSync(t, fsys, "d/g")
	checkPending(t, fsys, "entry f")
	checkCrash(t, fsys, loseAll, "d/ d/g=in d f=data")
	checkCrash(t, fsys, keepAll(fsys), "d/ d/g=in d")
	mustSyncDir(t, fsys, ".")
	checkCrash(t, fsys, loseAll, "d/ d/g=in d")
}

func TestDataNeedsSync(t *testing.T) {
	fsys := simfs.New("/data")
	mustCreate(t, fsys, "f", "")
	mustCreate(t, fsys, "g", "")
	mustSyncDir(t, fsys, ".")
	checkPending(t, fsys, "")
	mustWriteAt(t, fsys, "f", 0, "hello")
	mustWriteAt(t, fsys, "g", 0, "other")
	checkPending(t, fsys, "write f [0,5); write g [0,5)")
	checkCrash(t, fsys, loseAll, "f= g=")
	checkCrash(t, fsys, keepAll(fsys), "f=hello g=other")
	mustSyncDir(t, fsys, ".")
	mustSync(t, fsys, "g")
	checkPending(t, fsys, "write f [0,5)")
	checkCrash(t, fsys, loseAll, "f= g=other")
	mustSync(t, fsys, "f")
	checkPending(t, fsys, "")
	checkCrash(t, fsys, loseAll, "f=hello g=other")
}

func TestRenameDurability(t *testing.T) {
	t.Run("same directory", func(t *testing.T) {
		fsys := simfs.New("/data")
		mustCreate(t, fsys, "old", "old data")
		mustCreate(t, fsys, "tmp", "new data")
		mustSync(t, fsys, "old")
		mustSync(t, fsys, "tmp")
		mustSyncDir(t, fsys, ".")
		mustRename(t, fsys, "tmp", "old")
		checkPending(t, fsys, "entry old")
		checkCrash(t, fsys, loseAll, "old=old data tmp=new data")
		checkCrash(t, fsys, keepAll(fsys), "old=new data")
		mustSyncDir(t, fsys, ".")
		checkCrash(t, fsys, loseAll, "old=new data")
	})
	t.Run("across directories", func(t *testing.T) {
		for _, first := range []string{"dst", "src"} {
			fsys := simfs.New("/data")
			mustMkdir(t, fsys, "src")
			mustMkdir(t, fsys, "dst")
			mustCreate(t, fsys, "src/f", "data")
			mustSync(t, fsys, "src/f")
			mustSyncDir(t, fsys, "src")
			mustSyncDir(t, fsys, ".")
			mustRename(t, fsys, "src/f", "dst/g")
			checkPending(t, fsys, "entry dst/g; entry src/f")
			neverLost := func() {
				t.Helper()
				for _, m := range simfs.Models(fsys.Pending(), 1<<10, nil) {
					if got := files(t, fsys.Crash(m)); !strings.Contains(got, "=data") {
						t.Errorf("Crash(%s) lost the file: %q", m.Name, got)
					}
				}
			}
			neverLost()
			defer neverLost()
			for bits, want := range map[string]string{
				"00": "dst/ src/ src/f=data",
				"10": "dst/ dst/g=data src/ src/f=data",
				"01": "dst/ src/ src/f=data",
				"11": "dst/ dst/g=data src/",
			} {
				checkCrash(t, fsys, keep(t, fsys, bits), want)
			}
			if first == "src" {
				mustSyncDir(t, fsys, "src")
				checkPending(t, fsys, "entry dst/g; entry src/f")
				checkCrash(t, fsys, loseAll, "dst/ src/ src/f=data")
				neverLost()
			}
			mustSyncDir(t, fsys, "dst")
			checkPending(t, fsys, "entry src/f")
			neverLost()
			checkCrash(t, fsys, loseAll, "dst/ dst/g=data src/ src/f=data")
			checkCrash(t, fsys, keepAll(fsys), "dst/ dst/g=data src/")
			mustSyncDir(t, fsys, "src")
			checkPending(t, fsys, "")
			checkCrash(t, fsys, loseAll, "dst/ dst/g=data src/")
		}
	})
	t.Run("two names", func(t *testing.T) {
		fsys := simfs.New("/data")
		mustMkdir(t, fsys, "src")
		mustMkdir(t, fsys, "dst")
		mustCreate(t, fsys, "src/f", "data")
		mustSync(t, fsys, "src/f")
		mustSyncDir(t, fsys, "src")
		mustSyncDir(t, fsys, ".")
		mustRename(t, fsys, "src/f", "dst/g")
		crashed := fsys.Crash(keep(t, fsys, "10"))
		mustWriteAt(t, crashed, "dst/g", 0, "DATA")
		if got := files(t, crashed); got != "dst/ dst/g=DATA src/ src/f=DATA" {
			t.Errorf("after a write to one of two names: %q", got)
		}
	})
}

func TestTruncateDurability(t *testing.T) {
	fsys := simfs.New("/data")
	mustCreate(t, fsys, "f", "0123456789")
	mustSync(t, fsys, "f")
	mustSyncDir(t, fsys, ".")
	mustTruncate(t, fsys, "f", 4)
	checkPending(t, fsys, "truncate f to 4")
	checkCrash(t, fsys, loseAll, "f=0123456789")
	checkCrash(t, fsys, keepAll(fsys), "f=0123")
	mustTruncate(t, fsys, "f", 6)
	mustWriteAt(t, fsys, "f", 1, "x")
	checkPending(t, fsys, "truncate f to 4; truncate f to 6; write f [1,2)")
	for bits, want := range map[string]string{
		"100": "f=0123",
		"010": "f=012345",
		"001": "f=0x23456789",
		"101": "f=0x23",
		"110": "f=0123\x00\x00",
		"111": "f=0x23\x00\x00",
	} {
		checkCrash(t, fsys, keep(t, fsys, bits), want)
	}
	mustSync(t, fsys, "f")
	checkCrash(t, fsys, loseAll, "f=0x23\x00\x00")

	f, err := fsys.OpenFile("f", os.O_RDWR|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	checkPending(t, fsys, "truncate f to 0")
	checkCrash(t, fsys, keepAll(fsys), "f=")
	if f, err = fsys.OpenFile("new", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	checkPending(t, fsys, "truncate f to 0; entry new")
}

func TestLocks(t *testing.T) {
	fsys := simfs.New("/data")
	mustCreate(t, fsys, "lock", "")
	mustMkdir(t, fsys, "d")
	mustCreate(t, fsys, "d/lock", "")
	sub, err := fsys.Sub("d")
	if err != nil {
		t.Fatal(err)
	}
	lock := func(fsys vfs.FS, name string, mode vfs.LockMode) func() error {
		t.Helper()
		unlock, err := fsys.Lock(name, mode)
		if err != nil {
			t.Fatal(err)
		}
		return unlock
	}
	locked := func(fsys vfs.FS, name string, mode vfs.LockMode) {
		t.Helper()
		_, err := fsys.Lock(name, mode)
		if !errors.Is(err, vfs.ErrLocked) || err.Error() != "flock "+name+": locked by another process" {
			t.Errorf("Lock(%q, %d): %v, want ErrLocked", name, mode, err)
		}
	}
	ex := lock(fsys, "lock", vfs.Exclusive)
	locked(fsys, "lock", vfs.Shared)
	locked(fsys, "lock", vfs.Exclusive)
	sh := lock(sub, "lock", vfs.Shared)
	locked(fsys, "d/lock", vfs.Exclusive)
	sh2 := lock(fsys, "d/lock", vfs.Shared)
	crashed := fsys.Crash(keepAll(fsys))
	lock(crashed, "lock", vfs.Exclusive)
	if err := errors.Join(ex(), sh(), sh2()); err != nil {
		t.Fatal(err)
	}
	if err := ex(); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("a second unlock: %v", err)
	}
	lock(sub, "lock", vfs.Exclusive)
	lock(fsys, "lock", vfs.Exclusive)
	if _, err := fsys.Lock("missing", vfs.Shared); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lock of a missing file: %v", err)
	}
	if len(fsys.Pending()) != 3 {
		t.Errorf("locks changed what is pending: %s", pending(fsys))
	}
}

func TestSub(t *testing.T) {
	fsys := simfs.New("/srv/data")
	mustMkdir(t, fsys, "logdb")
	sub, err := fsys.Sub("logdb")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Name() != "/srv/data/logdb" {
		t.Errorf("Name: %q", sub.Name())
	}
	var ops []string
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		ops = append(ops, op.Kind.String()+" "+op.Path)
		return false
	}, Err: syscall.EIO})
	mustMkdir(t, sub, "log")
	mustCreate(t, sub, "log/x", "data")
	sub2, err := sub.Sub("log")
	if err != nil {
		t.Fatal(err)
	}
	if sub2.Name() != "/srv/data/logdb/log" {
		t.Errorf("Name: %q", sub2.Name())
	}
	mustSync(t, sub2, "x")
	if _, err := sub2.Open("../../f"); err == nil || err.Error() != "openat ../../f: path escapes from parent" {
		t.Errorf("Open out of a Sub: %v", err)
	}
	wantOps := []string{"mkdir logdb/log", "create logdb/log/x", "write logdb/log/x", "open logdb/log", "open logdb/log/x",
		"sync logdb/log/x", "open f"}
	if !slices.Equal(ops, wantOps) {
		t.Errorf("operations: %q, want %q", ops, wantOps)
	}
	checkPending(t, fsys, "entry logdb; entry logdb/log; entry logdb/log/x")
	if got := files(t, fsys); got != "logdb/ logdb/log/ logdb/log/x=data" {
		t.Errorf("files: %q", got)
	}
	crashed := sub2.(*simfs.FS).Crash(keepAll(fsys))
	if crashed.Name() != "/srv/data" {
		t.Errorf("Name of the crashed file system: %q", crashed.Name())
	}
	if got := files(t, crashed); got != "logdb/ logdb/log/ logdb/log/x=data" {
		t.Errorf("files after a crash: %q", got)
	}
	mustCreate(t, fsys, "f", "")
	if _, err := fsys.Sub("f"); err == nil || err.Error() != "open /srv/data/f: not a directory" {
		t.Errorf("Sub of a file: %v", err)
	}
}

func TestLimits(t *testing.T) {
	fsys := simfs.New("/data")
	for _, flag := range []int{os.O_RDWR | os.O_CREATE | os.O_APPEND, os.O_RDWR | os.O_CREATE | os.O_SYNC, os.O_WRONLY | os.O_RDWR | os.O_CREATE} {
		if _, err := fsys.OpenFile("f", flag, 0o600); err == nil || err.Error() != "openat f: simfs: unsupported flag" {
			t.Errorf("OpenFile with %#x: %v", flag, err)
		}
	}
	mustCreate(t, fsys, "f", "")
	f, err := fsys.OpenFile("f", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt([]byte("x"), 1<<30); !errors.Is(err, syscall.EFBIG) {
		t.Errorf("WriteAt past 1 GiB: %v", err)
	}
	if _, err := f.Seek(1<<30, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("x")); !errors.Is(err, syscall.EFBIG) {
		t.Errorf("Write past 1 GiB: %v", err)
	}
	if err := f.Truncate(1<<30 + 1); !errors.Is(err, syscall.EFBIG) {
		t.Errorf("Truncate past 1 GiB: %v", err)
	}
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpWrite }, Err: syscall.ENOSPC, Short: 1, Times: 1})
	if n, err := f.WriteAt([]byte("xy"), 1<<30); n != 0 || !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("short write past 1 GiB: %d, %v", n, err)
	}
	if info, err := f.Stat(); err != nil || info.Size() != 0 {
		t.Errorf("after the writes past 1 GiB: %v, %v", info, err)
	}
	checkPending(t, fsys, "entry f")
}

// TestConcurrentUse is for the race detector: files written and read at once.
func TestConcurrentUse(t *testing.T) {
	fsys := simfs.New("/data")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			name := fmt.Sprintf("f%d", i)
			f, err := fsys.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Error(err)
				return
			}
			chunk := bytes.Repeat([]byte{byte('a' + i)}, 1000)
			for j := range 50 {
				if _, err := f.Write(chunk); err != nil {
					t.Error(err)
				}
				if j%10 == 0 {
					if err := f.Sync(); err != nil {
						t.Error(err)
					}
				}
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				t.Error(err)
			}
			data, err := io.ReadAll(f)
			if err != nil || !bytes.Equal(data, bytes.Repeat(chunk, 50)) {
				t.Errorf("%s: %d bytes, %v", name, len(data), err)
			}
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Go(func() {
		for range 100 {
			if _, err := fsys.ReadDir("."); err != nil {
				t.Error(err)
			}
			_ = fsys.Pending()
			fsys.Walk(func(string, []byte) {})
		}
	})
	wg.Wait()
	n := 0
	fsys.Walk(func(_ string, data []byte) {
		if len(data) != 50000 {
			t.Errorf("%d bytes", len(data))
		}
		n++
	})
	if n != 8 {
		t.Errorf("%d files", n)
	}
}
