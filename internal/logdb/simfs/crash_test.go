package simfs_test

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// durable returns a file system with the file f, whose data and name are durable.
func durable(t *testing.T, data string) *simfs.FS {
	t.Helper()
	fsys := simfs.New("/data")
	mustCreate(t, fsys, "f", data)
	mustSync(t, fsys, "f")
	mustSyncDir(t, fsys, ".")
	checkPending(t, fsys, "")
	return fsys
}

func read(t *testing.T, fsys vfs.FS, name string) string {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, info.Size())
	if _, err := f.ReadAt(data, 0); err != nil && len(data) > 0 {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func model(t *testing.T, models []simfs.Model, name string) simfs.Model {
	t.Helper()
	for _, m := range models {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no model %q", name)
	return simfs.Model{}
}

func TestLoseAllAndKeepAll(t *testing.T) {
	fsys := durable(t, "durable")
	mustWriteAt(t, fsys, "f", 0, "DUR")
	mustCreate(t, fsys, "g", "new")
	mustMkdir(t, fsys, "d")
	mustCreate(t, fsys, "d/h", "in d")
	checkCrash(t, fsys, loseAll, "f=durable")
	checkCrash(t, fsys, keepAll(fsys), "d/ d/h=in d f=DURable g=new")
}

func TestPrefixes(t *testing.T) {
	fsys := durable(t, strings.Repeat("o", 3*4096))
	mustWriteAt(t, fsys, "f", 0, "A")
	mustWriteAt(t, fsys, "f", 4096, "B")
	mustWriteAt(t, fsys, "f", 8192, "C")
	checkPending(t, fsys, "write f [0,1); write f [4096,4097); write f [8192,8193)")
	models := simfs.Models(fsys.Pending(), 0, nil)
	for name, changed := range map[string]string{"lose-all": "", "prefix-1": "A", "prefix-2": "AB", "keep-all": "ABC"} {
		want := []byte(strings.Repeat("o", 3*4096))
		for i, c := range changed {
			want[i*4096] = byte(c)
		}
		if got := read(t, fsys.Crash(model(t, models, name)), "f"); got != string(want) {
			t.Errorf("%s: %q...", name, got[:8])
		}
	}
}

func TestTornLast(t *testing.T) {
	fsys := durable(t, "")
	mustWriteAt(t, fsys, "f", 0, "0123456789")
	models := simfs.Models(fsys.Pending(), 0, nil)
	for name, want := range map[string]string{
		"prefix-1-torn-1": "0",
		"prefix-1-torn-5": "01234",
		"prefix-1-torn-9": "012345678",
		"prefix-1-zero":   strings.Repeat("\x00", 10),
	} {
		if got := read(t, fsys.Crash(model(t, models, name)), "f"); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}

	fsys = durable(t, "oooooooooo")
	mustWriteAt(t, fsys, "f", 0, "abcdefghij")
	for _, m := range []struct {
		m    simfs.Model
		want string
	}{
		{simfs.Model{Name: "torn 3", Keep: []bool{true}, TornLast: 3}, "abcooooooo"},
		{simfs.Model{Name: "torn 10", Keep: []bool{true}, TornLast: 10}, "abcdefghij"},
		{simfs.Model{Name: "torn 20", Keep: []bool{true}, TornLast: 20}, "abcdefghij"},
		{simfs.Model{Name: "zero", Keep: []bool{true}, ZeroLast: true}, "oooooooooo"},
		{simfs.Model{Name: "torn but not kept", TornLast: 3}, "oooooooooo"},
	} {
		if got := read(t, fsys.Crash(m.m), "f"); got != m.want {
			t.Errorf("%s: %q, want %q", m.m.Name, got, m.want)
		}
	}
}

func TestZeroLast(t *testing.T) {
	fsys := durable(t, "old")
	mustWriteAt(t, fsys, "f", 1, "XXXXXX")
	mustWriteAt(t, fsys, "f", 9, "Y")
	checkPending(t, fsys, "write f [1,7); write f [9,10)")
	for _, m := range []struct {
		m    simfs.Model
		want string
	}{
		{keep(t, fsys, "10"), "oXXXXXX"},
		{simfs.Model{Name: "first zero", Keep: []bool{true, false}, ZeroLast: true}, "old\x00\x00\x00\x00"},
		{simfs.Model{Name: "both, the last zero", Keep: []bool{true, true}, ZeroLast: true}, "oXXXXXX\x00\x00\x00"},
		{simfs.Model{Name: "the last torn and zero", Keep: []bool{false, true}, TornLast: 1, ZeroLast: true}, "old\x00\x00\x00\x00\x00\x00\x00"},
	} {
		if got := read(t, fsys.Crash(m.m), "f"); got != m.want {
			t.Errorf("%s: %q, want %q", m.m.Name, got, m.want)
		}
	}
}

func TestOnlyLaterWrites(t *testing.T) {
	a, b := strings.Repeat("a", 4096), strings.Repeat("b", 4096)
	fsys := durable(t, "")
	mustWriteAt(t, fsys, "f", 0, a+b)
	checkPending(t, fsys, "write f [0,4096); write f [4096,8192)")
	if got, want := read(t, fsys.Crash(keep(t, fsys, "01")), "f"), strings.Repeat("\x00", 4096)+b; got != want {
		t.Errorf("only the later page, past the durable size: %q...", got[4090:4100])
	}
	old := strings.Repeat("o", 8192)
	fsys = durable(t, old)
	mustWriteAt(t, fsys, "f", 0, a+b)
	if got, want := read(t, fsys.Crash(keep(t, fsys, "01")), "f"), old[:4096]+b; got != want {
		t.Errorf("only the later page, inside the durable size: %q...", got[4090:4100])
	}

	fsys = durable(t, "")
	data := strings.Repeat("0123456789", 1000)
	mustWriteAt(t, fsys, "f", 100, data)
	checkPending(t, fsys, "write f [100,4096); write f [4096,8192); write f [8192,10100)")
	want := []byte(strings.Repeat("\x00", 100) + data)
	clear(want[4096:8192])
	if got := read(t, fsys.Crash(keep(t, fsys, "101")), "f"); got != string(want) {
		t.Error("a write split into pages: the middle page was not lost")
	}
}

// TestNameReuse shows that the changes of one name apply in the order they were made, even when a later one is durable
// and an earlier one is not.
func TestNameReuse(t *testing.T) {
	fsys := simfs.New("/data")
	mustMkdir(t, fsys, "src")
	mustMkdir(t, fsys, "dst")
	mustCreate(t, fsys, "src/a", "1")
	mustSync(t, fsys, "src/a")
	mustSyncDir(t, fsys, "src")
	mustSyncDir(t, fsys, ".")
	mustRename(t, fsys, "src/a", "dst/b")
	mustCreate(t, fsys, "src/a", "2")
	mustSync(t, fsys, "src/a")
	mustSyncDir(t, fsys, "src")
	checkPending(t, fsys, "entry dst/b; entry src/a")
	// dst was never synced, so the first file can be lost although src/a was not removed by itself.
	for bits, want := range map[string]string{
		"00": "dst/ src/ src/a=2",
		"10": "dst/ dst/b=1 src/ src/a=2",
		"01": "dst/ src/ src/a=2",
		"11": "dst/ dst/b=1 src/ src/a=2",
	} {
		checkCrash(t, fsys, keep(t, fsys, bits), want)
	}
	mustSyncDir(t, fsys, "dst")
	checkPending(t, fsys, "entry src/a")
	checkCrash(t, fsys, loseAll, "dst/ dst/b=1 src/ src/a=2")
	mustSyncDir(t, fsys, "src")
	checkPending(t, fsys, "")
	checkCrash(t, fsys, loseAll, "dst/ dst/b=1 src/ src/a=2")
}

func TestModels(t *testing.T) {
	names := func(models []simfs.Model) []string {
		var names []string
		for _, m := range models {
			names = append(names, m.Name)
		}
		return names
	}
	keys := func(t *testing.T, n int, models []simfs.Model) map[string]bool {
		t.Helper()
		keys := map[string]bool{}
		for _, m := range models {
			if m.Keep != nil && len(m.Keep) != n {
				t.Errorf("%s keeps from %d changes, not %d", m.Name, len(m.Keep), n)
			}
			var b strings.Builder
			for i := range n {
				if m.Keep != nil && m.Keep[i] {
					b.WriteByte('1')
				} else {
					b.WriteByte('0')
				}
			}
			keys[b.String()] = true
		}
		return keys
	}
	unique := func(t *testing.T, models []simfs.Model) {
		t.Helper()
		seen := map[string]bool{}
		for _, m := range models {
			if seen[m.Name] {
				t.Errorf("the name %q is twice", m.Name)
			}
			seen[m.Name] = true
		}
	}

	p := []simfs.Pending{{Kind: simfs.PendingEntry, Path: "f"}, {Kind: simfs.PendingWrite, Path: "f", Len: 10},
		{Kind: simfs.PendingWrite, Path: "f", Off: 10, Len: 1}}
	models := simfs.Models(p, 8, nil)
	want := []string{"lose-all", "keep-all", "prefix-1", "prefix-2", "prefix-2-torn-1", "prefix-2-torn-5", "prefix-2-torn-9",
		"prefix-2-zero", "prefix-3-zero", "subset-010", "subset-001", "subset-101", "subset-011"}
	if got := names(models); !slices.Equal(got, want) {
		t.Errorf("models: %q\nwant:   %q", got, want)
	}
	if got := keys(t, 3, models); len(got) != 8 {
		t.Errorf("%d different subsets, want 8", len(got))
	}
	if got := names(simfs.Models(p, 0, nil)); !slices.Equal(got, want[:9]) {
		t.Errorf("models without subsets: %q", got)
	}
	if got := names(simfs.Models(nil, 8, nil)); !slices.Equal(got, []string{"lose-all", "keep-all"}) {
		t.Errorf("models of nothing: %q", got)
	}

	ten := make([]simfs.Pending, 10)
	for i := range ten {
		ten[i] = simfs.Pending{Kind: simfs.PendingWrite, Path: "f", Off: int64(i), Len: 1}
	}
	random := simfs.Models(ten, 16, rand.New(rand.NewPCG(7, 7)))
	unique(t, random)
	subsets := slices.DeleteFunc(names(random), func(name string) bool { return !strings.HasPrefix(name, "subset-") })
	if len(subsets) != 16 || subsets[0] != "subset-0000000001" || subsets[1] != "subset-0111111111" {
		t.Errorf("random subsets: %q", subsets)
	}
	if got := len(keys(t, 10, random)); got != 2+9+16 {
		t.Errorf("%d different subsets, want %d", got, 2+9+16)
	}
	if again := simfs.Models(ten, 16, rand.New(rand.NewPCG(7, 7))); !slices.Equal(names(again), names(random)) {
		t.Error("the same seed gave other models")
	}
	if a, b := simfs.Models(ten, 16, nil), simfs.Models(ten, 16, nil); !slices.Equal(names(a), names(b)) {
		t.Error("the fixed seed gave other models")
	}
	if other := simfs.Models(ten, 16, rand.New(rand.NewPCG(8, 8))); slices.Equal(names(other), names(random)) {
		t.Error("another seed gave the same models")
	}

	four := ten[:4]
	if got := len(keys(t, 4, simfs.Models(four, 11, nil))); got != 16 {
		t.Errorf("all 11 subsets that are not prefixes: %d different subsets, want 16", got)
	}
	if got := len(keys(t, 4, simfs.Models(four, 10, nil))); got != 5+10 {
		t.Errorf("10 of the 11 subsets: %d different subsets, want 15", got)
	}
	big := make([]simfs.Pending, 100)
	random = simfs.Models(big, 50, nil)
	unique(t, random)
	if got := len(keys(t, 100, random)); got != 101+50 {
		t.Errorf("100 changes: %d different subsets, want 151", got)
	}

	fsys := durable(t, "old data")
	mustWriteAt(t, fsys, "f", 0, "new")
	mustCreate(t, fsys, "g", strings.Repeat("g", 5000))
	mustRename(t, fsys, "g", "h")
	all := simfs.Models(fsys.Pending(), 1<<10, nil)
	unique(t, all)
	if got, want := len(keys(t, len(fsys.Pending()), all)), 1<<len(fsys.Pending()); got != want {
		t.Errorf("%d different subsets, want %d", got, want)
	}
	for _, m := range all {
		fsys.Crash(m)
	}
}

func TestCrashPanics(t *testing.T) {
	fsys := durable(t, "")
	mustWriteAt(t, fsys, "f", 0, "x")
	for _, m := range []simfs.Model{
		{Name: "short Keep", Keep: []bool{}},
		{Name: "long Keep", Keep: []bool{true, true}},
		{Name: "negative TornLast", Keep: []bool{true}, TornLast: -1},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Crash(%s) did not panic", m.Name)
				}
			}()
			fsys.Crash(m)
		}()
	}
	for _, f := range []simfs.Fault{{Err: syscall.EIO}, {Match: func(simfs.Op) bool { return true }}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Inject(%+v) did not panic", f)
				}
			}()
			fsys.Inject(f)
		}()
	}
}

func TestCrashLeavesTheFileSystem(t *testing.T) {
	fsys := durable(t, "old")
	mustWriteAt(t, fsys, "f", 0, "new")
	keepIt := keepAll(fsys)
	a, b := fsys.Crash(loseAll), fsys.Crash(keepIt)
	checkPending(t, fsys, "write f [0,3)")
	if got := read(t, fsys, "f"); got != "new" {
		t.Errorf("after Crash: %q", got)
	}
	mustWriteAt(t, a, "f", 0, "A")
	if got := read(t, b, "f"); got != "new" {
		t.Errorf("a crashed file system changed another: %q", got)
	}
	if a.Counted() != 1 || len(a.Pending()) != 1 || len(b.Pending()) != 0 {
		t.Errorf("crashed: %d counted, %q and %q pending", a.Counted(), pending(a), pending(b))
	}
	checkCrash(t, a, loseAll, "f=old")
	mustWriteAt(t, fsys, "f", 3, "er")
	checkCrash(t, fsys, keepAll(fsys), "f=newer")
	checkCrash(t, fsys, loseAll, "f=old")
}

func TestFaultWrite(t *testing.T) {
	fsys := durable(t, "")
	n := fsys.Counted()
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpWrite && op.N == n+2 }, Err: syscall.EIO})
	f, err := fsys.OpenFile("f", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []string{"one", "two", "three"} {
		k, err := f.Write([]byte(s))
		var pe *fs.PathError
		switch {
		case i != 1 && (k != len(s) || err != nil):
			t.Errorf("write %d: %d, %v", i, k, err)
		case i == 1 && (k != 0 || !errors.Is(err, syscall.EIO) || !errors.As(err, &pe) || pe.Op != "write" || pe.Path != "/data/f"):
			t.Errorf("write %d: %d, %v", i, k, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, fsys, "f"); got != "onethree" {
		t.Errorf("data: %q", got)
	}
	checkPending(t, fsys, "write f [0,3); write f [3,8)")
}

func TestFaultShort(t *testing.T) {
	fsys := durable(t, "")
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpWrite }, Err: syscall.ENOSPC, Short: 3, Times: 1})
	f, err := fsys.OpenFile("f", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := f.Write([]byte("0123456789")); n != 3 || !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("short write: %d, %v", n, err)
	}
	if n, err := f.Write([]byte("abc")); n != 3 || err != nil {
		t.Errorf("write after a short write: %d, %v", n, err)
	}
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpWrite }, Err: syscall.ENOSPC, Short: 2, Times: 1})
	if n, err := f.WriteAt([]byte("XYZ"), 10); n != 2 || !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("short WriteAt: %d, %v", n, err)
	}
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpWrite }, Err: syscall.ENOSPC, Short: 100, Times: 1})
	if n, err := f.Write([]byte("q")); n != 1 || !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("Short larger than the write: %d, %v", n, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := fsys.Open("f")
	if err != nil {
		t.Fatal(err)
	}
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpWrite }, Err: syscall.ENOSPC, Short: 2, Times: 1})
	if n, err := ro.Write([]byte("no")); n != 0 || !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("short write to a read-only file: %d, %v", n, err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, fsys, "f"); got != "012abcq\x00\x00\x00XY" {
		t.Errorf("data: %q", got)
	}
	checkPending(t, fsys, "write f [0,3); write f [3,6); write f [10,12); write f [6,7)")
}

func TestFaultSync(t *testing.T) {
	syncFault := func(drop bool) simfs.Fault {
		return simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSync }, Err: syscall.EIO, Drop: drop, Times: 1}
	}
	sync := func(fsys vfs.FS) error {
		f, err := fsys.Open("f")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		return f.Sync()
	}
	t.Run("keep", func(t *testing.T) {
		fsys := durable(t, "old")
		mustWriteAt(t, fsys, "f", 0, "new")
		fsys.Inject(syncFault(false))
		if err := sync(fsys); !errors.Is(err, syscall.EIO) {
			t.Errorf("Sync: %v", err)
		}
		checkPending(t, fsys, "write f [0,3)")
		checkCrash(t, fsys, loseAll, "f=old")
		checkCrash(t, fsys, keepAll(fsys), "f=new")
		if err := sync(fsys); err != nil {
			t.Fatal(err)
		}
		checkCrash(t, fsys, loseAll, "f=new")
	})
	t.Run("drop", func(t *testing.T) {
		fsys := durable(t, "old")
		mustWriteAt(t, fsys, "f", 0, "new")
		fsys.Inject(syncFault(true))
		if err := sync(fsys); !errors.Is(err, syscall.EIO) {
			t.Errorf("Sync: %v", err)
		}
		checkPending(t, fsys, "")
		if got := read(t, fsys, "f"); got != "new" {
			t.Errorf("reads after the dropped sync: %q", got)
		}
		if err := sync(fsys); err != nil {
			t.Errorf("a later Sync: %v", err)
		}
		checkCrash(t, fsys, keepAll(fsys), "f=old")
		mustWriteAt(t, fsys, "f", 5, "x")
		if err := sync(fsys); err != nil {
			t.Fatal(err)
		}
		checkCrash(t, fsys, loseAll, "f=old\x00\x00x")
		if got := read(t, fsys, "f"); got != "new\x00\x00x" {
			t.Errorf("reads: %q", got)
		}
	})
	t.Run("drop a directory", func(t *testing.T) {
		fsys := simfs.New("/data")
		mustMkdir(t, fsys, "src")
		mustMkdir(t, fsys, "dst")
		mustCreate(t, fsys, "src/f", "data")
		mustSync(t, fsys, "src/f")
		mustSyncDir(t, fsys, "src")
		mustSyncDir(t, fsys, ".")
		mustCreate(t, fsys, "dst/new", "")
		mustRename(t, fsys, "src/f", "dst/g")
		checkPending(t, fsys, "entry dst/new; entry dst/g; entry src/f")
		fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSyncDir }, Err: syscall.EIO, Drop: true, Times: 1})
		var pe *fs.PathError
		if err := fsys.SyncDir("dst"); !errors.Is(err, syscall.EIO) || !errors.As(err, &pe) || pe.Op != "sync" || pe.Path != "/data/dst" {
			t.Errorf("SyncDir: %v", err)
		}
		checkPending(t, fsys, "")
		mustSyncDir(t, fsys, "dst")
		mustSyncDir(t, fsys, "src")
		checkCrash(t, fsys, loseAll, "dst/ src/ src/f=data")
		if got := files(t, fsys); got != "dst/ dst/g=data dst/new= src/" {
			t.Errorf("reads after the dropped sync: %q", got)
		}
	})
}

func TestFaultTimes(t *testing.T) {
	fsys := simfs.New("/data")
	mkdirs := func(op simfs.Op) bool { return op.Kind == simfs.OpMkdir }
	fsys.Inject(simfs.Fault{Match: mkdirs, Err: syscall.EIO, Times: 2})
	for i, name := range []string{"a", "b", "c", "d"} {
		if err := fsys.Mkdir(name, 0o700); (i < 2) != errors.Is(err, syscall.EIO) || i >= 2 && err != nil {
			t.Errorf("Mkdir %d: %v", i, err)
		}
	}
	removes := func(op simfs.Op) bool { return op.Kind == simfs.OpRemove }
	fsys.Inject(simfs.Fault{Match: removes, Err: syscall.EIO, Times: 1})
	fsys.Inject(simfs.Fault{Match: removes, Err: syscall.ENOSPC})
	for i, want := range []error{syscall.EIO, syscall.ENOSPC, syscall.ENOSPC} {
		if err := fsys.Remove("c"); !errors.Is(err, want) {
			t.Errorf("Remove %d: %v, want %v", i, err, want)
		}
	}
	if got := files(t, fsys); got != "c/ d/" {
		t.Errorf("files: %q", got)
	}
}

// TestFaultErrors checks the kind of each operation, and that its fault returns the error that os would.
func TestFaultErrors(t *testing.T) {
	fsys := simfs.New("/data")
	mustMkdir(t, fsys, "d")
	mustCreate(t, fsys, "d/f", "data")
	mustCreate(t, fsys, "lock", "")
	f, err := fsys.OpenFile("d/f", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	d, err := fsys.Open("d")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	ignore := func(_ any, err error) error { return err }
	ignore2 := func(_ int64, err error) error { return err }
	for _, c := range []struct {
		kind simfs.OpKind
		call func() error
		want string
	}{
		{simfs.OpCreate, func() error { return ignore(fsys.OpenFile("x", os.O_RDWR|os.O_CREATE, 0o600)) }, "openat x: input/output error"},
		{simfs.OpTruncate, func() error { return ignore(fsys.OpenFile("d/f", os.O_RDWR|os.O_TRUNC, 0)) }, "openat d/f: input/output error"},
		{simfs.OpOpen, func() error { return ignore(fsys.Open("d/f")) }, "openat d/f: input/output error"},
		{simfs.OpOpen, func() error { return ignore(fsys.Sub("d")) }, "openat d: input/output error"},
		{simfs.OpWrite, func() error { return ignore(f.Write([]byte("x"))) }, "write /data/d/f: input/output error"},
		{simfs.OpWrite, func() error { return ignore(f.WriteAt([]byte("x"), 1)) }, "write /data/d/f: input/output error"},
		{simfs.OpRead, func() error { return ignore(f.Read(make([]byte, 1))) }, "read /data/d/f: input/output error"},
		{simfs.OpRead, func() error { return ignore(f.ReadAt(make([]byte, 1), 0)) }, "read /data/d/f: input/output error"},
		{simfs.OpTruncate, func() error { return f.Truncate(1) }, "truncate /data/d/f: input/output error"},
		{simfs.OpSync, func() error { return f.Sync() }, "sync /data/d/f: input/output error"},
		{simfs.OpSync, func() error { return d.Sync() }, "sync /data/d: input/output error"},
		{simfs.OpLstat, func() error { return ignore(f.Stat()) }, "stat /data/d/f: input/output error"},
		{simfs.OpLstat, func() error { return ignore(fsys.Lstat("d/f")) }, "statat d/f: input/output error"},
		{simfs.OpReadDir, func() error { return ignore(d.ReadDir(-1)) }, "readdirent /data/d: input/output error"},
		{simfs.OpReadDir, func() error { return ignore(fsys.ReadDir("d")) }, "readdirent /data/d: input/output error"},
		{simfs.OpSyncDir, func() error { return fsys.SyncDir("d") }, "sync /data/d: input/output error"},
		{simfs.OpMkdir, func() error { return fsys.Mkdir("e", 0o700) }, "mkdirat e: input/output error"},
		{simfs.OpRemove, func() error { return fsys.Remove("d/f") }, "removeat d/f: input/output error"},
		{simfs.OpRename, func() error { return fsys.Rename("d/f", "g") }, "renameat d/f g: input/output error"},
		{simfs.OpLock, func() error { return ignore(fsys.Lock("lock", vfs.Shared)) }, "flock lock: input/output error"},
	} {
		var got simfs.Op
		fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { got = op; return true }, Err: syscall.EIO, Times: 1})
		err := c.call()
		if got.Kind != c.kind || err == nil || err.Error() != c.want || !errors.Is(err, syscall.EIO) {
			t.Errorf("%v: %v, want %v: %s", got, err, c.kind, c.want)
		}
		if c.kind == simfs.OpRename {
			var le *os.LinkError
			if !errors.As(err, &le) {
				t.Errorf("%v: %T, want *os.LinkError", got, err)
			}
		} else {
			var pe *fs.PathError
			if !errors.As(err, &pe) {
				t.Errorf("%v: %T, want *fs.PathError", got, err)
			}
		}
	}
	if err := ignore2(f.Seek(0, 0)); err != nil {
		t.Errorf("Seek: %v", err)
	}
	if got := files(t, fsys); got != "d/ d/f=data lock=" {
		t.Errorf("files after the faults: %q", got)
	}
}

func TestOps(t *testing.T) {
	fsys := simfs.New("/data")
	var ops []string
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { ops = append(ops, op.String()); return false }, Err: syscall.EIO})
	mustMkdir(t, fsys, "d")
	f, err := fsys.OpenFile("d/f", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAt(make([]byte, 2), 1); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Truncate(4), f.Sync(), f.Close(), fsys.SyncDir("d")); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Lstat("d/f"); err != nil {
		t.Fatal(err)
	}
	mustRename(t, fsys, "d/f", "g")
	if _, err := fsys.ReadDir("."); err != nil {
		t.Fatal(err)
	}
	unlock, err := fsys.Lock("g", vfs.Exclusive)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(unlock(), fsys.Remove("missing"), fsys.Remove("g")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	want := []string{
		"op 1 (counted 1) mkdir d",
		"op 2 (counted 2) create d/f",
		"op 3 (counted 3) write d/f [0,5)",
		"op 4 read d/f [1,3)",
		"op 5 (counted 4) truncate d/f to 4",
		"op 6 (counted 5) sync d/f",
		"op 7 (counted 6) syncdir d",
		"op 8 lstat d/f",
		"op 9 (counted 7) rename d/f",
		"op 10 readdir .",
		"op 11 lock g",
		"op 12 (counted 8) remove missing",
		"op 13 (counted 9) remove g",
	}
	if !slices.Equal(ops, want) {
		t.Errorf("operations:\n%s\nwant:\n%s", strings.Join(ops, "\n"), strings.Join(want, "\n"))
	}
	if fsys.Counted() != 9 {
		t.Errorf("Counted: %d", fsys.Counted())
	}
}

func TestCrashAt(t *testing.T) {
	fsys := durable(t, "data")
	n := fsys.Counted()
	fsys.CrashAt(n + 2)
	f, err := fsys.OpenFile("f", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []string{"D", "A"} {
		if _, err := f.WriteAt([]byte(s), int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	crashed := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, simfs.ErrCrashed) {
			t.Errorf("%s after the crash: %v", what, err)
		}
	}
	ignore := func(_ any, err error) error { return err }
	ignore2 := func(_ int64, err error) error { return err }
	crashed("ReadAt", ignore(f.ReadAt(make([]byte, 1), 0)))
	crashed("Read", ignore(f.Read(make([]byte, 1))))
	crashed("WriteAt", ignore(f.WriteAt([]byte("x"), 0)))
	crashed("Sync", f.Sync())
	crashed("Truncate", f.Truncate(0))
	crashed("Stat", ignore(f.Stat()))
	crashed("Seek", ignore2(f.Seek(0, 0)))
	crashed("ReadDir of a file", ignore(f.ReadDir(-1)))
	crashed("Close", f.Close())
	crashed("Open", ignore(fsys.Open("f")))
	crashed("OpenFile", ignore(fsys.OpenFile("g", os.O_RDWR|os.O_CREATE, 0o600)))
	crashed("Lstat", ignore(fsys.Lstat("f")))
	crashed("ReadDir", ignore(fsys.ReadDir(".")))
	crashed("Mkdir", fsys.Mkdir("d", 0o700))
	crashed("Remove", fsys.Remove("f"))
	crashed("Rename", fsys.Rename("f", "g"))
	crashed("SyncDir", fsys.SyncDir("."))
	crashed("Sub", ignore(fsys.Sub(".")))
	crashed("Lock", ignore(fsys.Lock("f", vfs.Shared)))
	crashed("Close of the file system", fsys.Close())
	if fsys.Counted() != n+2 {
		t.Errorf("Counted: %d, want %d", fsys.Counted(), n+2)
	}
	checkPending(t, fsys, "write f [0,1); write f [1,2)")
	checkCrash(t, fsys, keepAll(fsys), "f=DAta")
	checkCrash(t, fsys, loseAll, "f=data")
	after := fsys.Crash(keepAll(fsys))
	mustCreate(t, after, "g", "after")
	checkCrash(t, after, loseAll, "f=DAta")

	now := simfs.New("/data")
	mustMkdir(t, now, "d")
	now.CrashAt(now.Counted())
	crashed("Mkdir at once", now.Mkdir("e", 0o700))
	crashed("Lstat at once", ignore(now.Lstat("d")))

	var walked []string
	fsys.Walk(func(path string, data []byte) { walked = append(walked, path+"="+string(data)) })
	if !slices.Equal(walked, []string{"f=DAta"}) {
		t.Errorf("Walk after the crash: %q", walked)
	}
}

func TestLocksAfterCrash(t *testing.T) {
	fsys := durable(t, "")
	unlock, err := fsys.Lock("f", vfs.Exclusive)
	if err != nil {
		t.Fatal(err)
	}
	fsys.CrashAt(fsys.Counted())
	if err := unlock(); !errors.Is(err, simfs.ErrCrashed) {
		t.Errorf("unlock after the crash: %v", err)
	}
	if err := unlock(); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("a second unlock: %v", err)
	}
	if unlock, err := fsys.Crash(loseAll).Lock("f", vfs.Exclusive); err != nil {
		t.Errorf("Lock after the crash: %v", err)
	} else if err := unlock(); err != nil {
		t.Error(err)
	}
}

// workload changes a file system in many ways, the same each time.
func workload(t *testing.T) *simfs.FS {
	fsys := simfs.New("/data")
	mustMkdir(t, fsys, "log")
	mustMkdir(t, fsys, "objects")
	mustSyncDir(t, fsys, ".")
	mustCreate(t, fsys, "log/1.log", strings.Repeat("h", 64))
	mustSync(t, fsys, "log/1.log")
	mustSyncDir(t, fsys, "log")
	mustWriteAt(t, fsys, "log/1.log", 64, strings.Repeat("r", 5000))
	mustCreate(t, fsys, "objects/.tmp-1", "object")
	mustSync(t, fsys, "objects/.tmp-1")
	mustRename(t, fsys, "objects/.tmp-1", "log/obj")
	mustCreate(t, fsys, "head", "head")
	mustTruncate(t, fsys, "log/1.log", 100)
	mustCreate(t, fsys, ".tmp-checkpoint", "checkpoint")
	mustRename(t, fsys, ".tmp-checkpoint", "checkpoint")
	if err := fsys.Remove("head"); err != nil {
		t.Fatal(err)
	}
	return fsys
}

func TestDeterministic(t *testing.T) {
	a, b := workload(t), workload(t)
	if pending(a) != pending(b) {
		t.Fatalf("pending:\n%s\n%s", pending(a), pending(b))
	}
	if a.Counted() != b.Counted() {
		t.Errorf("counted: %d, %d", a.Counted(), b.Counted())
	}
	models := simfs.Models(a.Pending(), 64, rand.New(rand.NewPCG(1, 1)))
	again := simfs.Models(b.Pending(), 64, rand.New(rand.NewPCG(1, 1)))
	if len(models) != len(again) {
		t.Fatalf("%d and %d models", len(models), len(again))
	}
	for i, m := range models {
		if m.Name != again[i].Name {
			t.Errorf("model %d: %s and %s", i, m.Name, again[i].Name)
		}
		if x, y := files(t, a.Crash(m)), files(t, b.Crash(m)); x != y {
			t.Errorf("%s: %q and %q", m.Name, x, y)
		}
	}
	t.Logf("%d pending changes, %d models", len(a.Pending()), len(models))
}

func TestWalk(t *testing.T) {
	fsys := simfs.New("/data")
	mustMkdir(t, fsys, "a")
	mustCreate(t, fsys, "a/x", "in a")
	mustCreate(t, fsys, "a.txt", "dot")
	mustCreate(t, fsys, "b", "b")
	mustMkdir(t, fsys, "empty")
	var got []string
	fsys.Walk(func(path string, data []byte) {
		if _, err := fsys.Lstat(path); err != nil {
			t.Error(err)
		}
		got = append(got, fmt.Sprintf("%s=%s", path, data))
		clear(data)
	})
	if want := []string{"a.txt=dot", "a/x=in a", "b=b"}; !slices.Equal(got, want) {
		t.Errorf("Walk: %q, want %q", got, want)
	}
	if got := read(t, fsys, "a/x"); got != "in a" {
		t.Errorf("after Walk changed its copy: %q", got)
	}
}
