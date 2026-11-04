// Package simfs is a simulated vfs.FS for the tests of internal/logdb and internal/store; nothing else imports it. It
// promises after a crash only what POSIX does, not what ext4 adds: the data of a file is durable after a Sync of it,
// and a name after a SyncDir of its directory. Crash builds what a crash can leave from the durable state and a model
// of the changes that are not durable yet; Inject makes operations fail, and CrashAt stops every operation after a
// point. It is deterministic: the same calls give the same results, numbers and crashes.
package simfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// ErrCrashed is the error, in an *fs.PathError or an *os.LinkError, of every operation after the point of CrashAt.
var ErrCrashed = errors.New("simfs: crashed")

// The errors that os gives in the same cases, with the same texts.
var (
	errEmptyPath       = errors.New("empty path")
	errPathEscapes     = errors.New("path escapes from parent")
	errUnsupportedMode = errors.New("unsupported file mode")
	errNegativeOffset  = errors.New("negative offset")
	errNotDirectory    = errors.New("not a directory")
	errClosedFile      = errors.New("use of closed file")
)

var errUnsupportedFlag = errors.New("simfs: unsupported flag")

// maxFileSize bounds files, which simfs keeps in memory.
const maxFileSize = 1 << 30

// OpKind is the kind of an operation. The kinds that change the file system are counted: each such operation has a
// number, which CrashAt uses.
type OpKind int

const (
	OpCreate   OpKind = iota // OpenFile with O_CREATE
	OpWrite                  // Write, WriteAt
	OpTruncate               // Truncate; OpenFile with O_TRUNC and without O_CREATE
	OpSync                   // File.Sync
	OpSyncDir
	OpMkdir
	OpRemove
	OpRename
	OpOpen    // Open; OpenFile with neither O_CREATE nor O_TRUNC; Sub
	OpRead    // Read, ReadAt
	OpLstat   // Lstat, File.Stat
	OpReadDir // ReadDir, File.ReadDir
	OpLock
)

var opKinds = [...]string{
	OpCreate:   "create",
	OpWrite:    "write",
	OpTruncate: "truncate",
	OpSync:     "sync",
	OpSyncDir:  "syncdir",
	OpMkdir:    "mkdir",
	OpRemove:   "remove",
	OpRename:   "rename",
	OpOpen:     "open",
	OpRead:     "read",
	OpLstat:    "lstat",
	OpReadDir:  "readdir",
	OpLock:     "lock",
}

func (k OpKind) String() string {
	if k >= 0 && int(k) < len(opKinds) {
		return opKinds[k]
	}
	return "OpKind(" + strconv.Itoa(int(k)) + ")"
}

func (k OpKind) counted() bool {
	return k >= OpCreate && k <= OpRename
}

// Op is an operation, as Fault.Match sees it.
type Op struct {
	N, All int // N: the number among the counted operations, from 1, or 0; All: among all operations, from 1
	Kind   OpKind
	Path   string // from the root of New, cleaned; for a rename, the old name
	Off    int64  // OpWrite, OpRead: the offset; OpTruncate: the size
	Len    int    // OpWrite, OpRead: the number of bytes
}

func (o Op) String() string {
	s := "op " + strconv.Itoa(o.All)
	if o.N > 0 {
		s += " (counted " + strconv.Itoa(o.N) + ")"
	}
	s += " " + o.Kind.String() + " " + o.Path
	if o.Kind == OpWrite || o.Kind == OpRead {
		s += fmt.Sprintf(" [%d,%d)", o.Off, o.Off+int64(o.Len))
	}
	if o.Kind == OpTruncate {
		s += " to " + strconv.FormatInt(o.Off, 10)
	}
	return s
}

// Fault makes the operations that Match reports fail with Err, before they do anything else. Match is called with the
// file system locked, and must not use it.
type Fault struct {
	Match func(Op) bool
	Err   error // in an *fs.PathError, or an *os.LinkError for a rename
	Short int   // OpWrite: the number of bytes that are written before Err
	Drop  bool  // OpSync, OpSyncDir: the changes that the sync would make durable never become durable
	Times int   // the number of operations that fail; 0: every one that matches
}

type fault struct {
	Fault
	left int // with Times > 0
}

// FS is a simulated file system, or the tree of one of its directories that Sub returns; they share one state. Its
// methods, and those of its files, may be called concurrently.
type FS struct {
	w      *world
	dir    *inode
	name   string // what Name returns
	prefix string // the path of dir from the root of the world
	closed bool   // guarded by w.mu
}

var _ vfs.FS = (*FS)(nil)

// world is the state that the FSes of a file system share.
type world struct {
	mu       sync.Mutex
	name     string // of the root
	root     *inode
	units    []*unit // see unit
	faults   []*fault
	all      int // operations
	counted  int // counted operations
	crashSet bool
	crashAt  int
}

// inode is a file or a directory. What reads see is in data or entries; the durable state is base or baseEntries
// with the durable units that are still in world.units.
type inode struct {
	dir  bool
	perm fs.FileMode

	data []byte // files
	base []byte

	entries     map[string]*inode // directories
	baseEntries map[string]*inode
	parent      *inode // in entries
	unlinked    bool   // removed, so that nothing can be made in it

	shared    int // locks held
	exclusive bool
}

// New returns a file system with an empty root directory, whose Name is name.
func New(name string) *FS {
	root := newDir(0o700, nil)
	return &FS{w: &world{name: name, root: root}, dir: root, name: name, prefix: "."}
}

func newDir(perm fs.FileMode, parent *inode) *inode {
	return &inode{dir: true, perm: perm, entries: map[string]*inode{}, baseEntries: map[string]*inode{}, parent: parent}
}

// Inject adds f, which is checked after the faults added before it; one fault at most fires for an operation. It
// panics if f has no Match or no Err.
func (s *FS) Inject(f Fault) {
	if f.Match == nil || f.Err == nil || f.Short < 0 || f.Times < 0 {
		panic("simfs: a Fault needs Match and Err, and no negative Short or Times")
	}
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	s.w.faults = append(s.w.faults, &fault{Fault: f, left: f.Times})
}

// CrashAt makes every operation, reads too, fail with ErrCrashed after the counted operation n, or at once if n
// operations have been counted. Crash then gives what the crash can leave.
func (s *FS) CrashAt(n int) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	s.w.crashSet, s.w.crashAt = true, n
}

// Counted returns the number of counted operations so far.
func (s *FS) Counted() int {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	return s.w.counted
}

func (w *world) crashed() bool {
	return w.crashSet && w.counted >= w.crashAt
}

// begin starts op. It returns ErrCrashed after the crash point; else it numbers op and returns the fault that
// matches it, if any, with its error.
func (w *world) begin(op Op) (*fault, error) {
	if w.crashed() {
		return nil, ErrCrashed
	}
	w.all++
	op.All = w.all
	if op.Kind.counted() {
		w.counted++
		op.N = w.counted
	}
	for _, f := range w.faults {
		if f.Times > 0 && f.left == 0 {
			continue
		}
		if f.Match(op) {
			f.left--
			return f, f.Err
		}
	}
	return nil, nil
}

func (s *FS) Name() string {
	return s.name
}

// path returns the path of name from the root of the world.
func (s *FS) path(name string) string {
	return path.Join(s.prefix, name)
}

// joinPath joins as os does for the names of files and roots.
func joinPath(dir, name string) string {
	if len(dir) > 0 && dir[len(dir)-1] == '/' {
		return dir + name
	}
	return dir + "/" + name
}

type resolveFlags int

const (
	noTerminalSlash resolveFlags = 1 << iota // a name that ends in a slash is not checked to be a directory
	creatingDir                              // a name that ends in a slash may be missing
)

// resolve finds the directory that holds the last component of name, as os.Root does on Linux without symbolic
// links. It returns the directory, the component, which may be ".", and whether name ends in a slash.
func (s *FS) resolve(name string, flags resolveFlags) (*inode, string, bool, error) {
	parts, endsInSlash, err := splitPath(name)
	if err != nil {
		return nil, "", false, err
	}
	dir := s.dir
	for i := 0; ; {
		if parts[i] == ".." {
			end := i + 1
			for end < len(parts) && parts[end] == ".." {
				end++
			}
			count := end - i
			if count > i {
				return nil, "", false, errPathEscapes
			}
			parts = slices.Delete(parts, i-count, end)
			if len(parts) == 0 {
				parts = []string{"."}
			}
			i, dir = 0, s.dir
			continue
		}
		n := lookup(dir, parts[i])
		if i == len(parts)-1 {
			if endsInSlash && flags&noTerminalSlash == 0 {
				switch {
				case n == nil && flags&creatingDir != 0:
				case n == nil:
					return nil, "", false, syscall.ENOENT
				case !n.dir:
					return nil, "", false, syscall.ENOTDIR
				}
			}
			return dir, parts[i], endsInSlash, nil
		}
		switch {
		case n == nil:
			return nil, "", false, syscall.ENOENT
		case !n.dir:
			return nil, "", false, syscall.ENOTDIR
		}
		dir = n
		i++
	}
}

// splitPath splits name into its components as os.Root does: "." stays only as the last one.
func splitPath(name string) (parts []string, endsInSlash bool, err error) {
	if name == "" {
		return nil, false, errEmptyPath
	}
	if name[0] == '/' {
		return nil, false, errPathEscapes
	}
	i, j := 0, 1
	for {
		if j < len(name) && name[j] != '/' {
			j++
			continue
		}
		parts = append(parts, name[i:j])
		partEnd := j
		for j < len(name) && name[j] == '/' {
			j++
		}
		if j == len(name) {
			endsInSlash = partEnd < len(name)
			return parts, endsInSlash, nil
		}
		if parts[len(parts)-1] == "." {
			parts = parts[:len(parts)-1]
		}
		i = j
	}
}

func lookup(dir *inode, name string) *inode {
	if name == "." {
		return dir
	}
	return dir.entries[name]
}

// find returns what name is, for the operations that os.Root does with an Open.
func (s *FS) find(name string) (*inode, error) {
	if s.closed {
		return nil, fs.ErrClosed
	}
	dir, last, _, err := s.resolve(name, 0)
	if err != nil {
		return nil, err
	}
	n := lookup(dir, last)
	if n == nil {
		return nil, syscall.ENOENT
	}
	return n, nil
}

func (s *FS) Open(name string) (vfs.File, error) {
	return s.OpenFile(name, os.O_RDONLY, 0)
}

// OpenFile opens name with the flags O_RDONLY, O_WRONLY, O_RDWR, O_CREATE, O_EXCL and O_TRUNC; others fail.
func (s *FS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	f, err := s.openFile(name, flag, perm)
	if err != nil {
		return nil, &fs.PathError{Op: "openat", Path: name, Err: err}
	}
	return f, nil
}

func (s *FS) openFile(name string, flag int, perm fs.FileMode) (*file, error) {
	kind := OpOpen
	switch {
	case flag&os.O_CREATE != 0:
		kind = OpCreate
	case flag&os.O_TRUNC != 0:
		kind = OpTruncate
	}
	p := s.path(name)
	if _, err := s.w.begin(Op{Kind: kind, Path: p}); err != nil {
		return nil, err
	}
	if perm&0o777 != perm {
		return nil, errUnsupportedMode
	}
	if s.closed {
		return nil, fs.ErrClosed
	}
	if flag&^(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_TRUNC) != 0 {
		return nil, errUnsupportedFlag
	}
	f := &file{w: s.w, name: joinPath(s.name, name), path: p}
	switch flag & (os.O_WRONLY | os.O_RDWR) {
	case os.O_RDONLY:
		f.read = true
	case os.O_WRONLY:
		f.write = true
	case os.O_RDWR:
		f.read, f.write = true, true
	default:
		return nil, errUnsupportedFlag
	}
	dir, last, _, err := s.resolve(name, 0)
	if err != nil {
		return nil, err
	}
	n := lookup(dir, last)
	switch {
	case n != nil && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		return nil, syscall.EEXIST
	case n != nil && n.dir && (f.write || flag&(os.O_CREATE|os.O_TRUNC) != 0):
		return nil, syscall.EISDIR
	case n != nil:
		if flag&os.O_TRUNC != 0 {
			s.w.truncate(n, p, 0)
		}
	case flag&os.O_CREATE == 0, dir.unlinked:
		return nil, syscall.ENOENT
	default:
		n = &inode{perm: perm}
		dir.entries[last] = n
		s.w.addEntry(p, dir, last, n)
	}
	f.n = n
	return f, nil
}

func (s *FS) Mkdir(name string, perm fs.FileMode) error {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if err := s.mkdir(name, perm); err != nil {
		return &fs.PathError{Op: "mkdirat", Path: name, Err: err}
	}
	return nil
}

func (s *FS) mkdir(name string, perm fs.FileMode) error {
	p := s.path(name)
	if _, err := s.w.begin(Op{Kind: OpMkdir, Path: p}); err != nil {
		return err
	}
	if perm&0o777 != perm {
		return errUnsupportedMode
	}
	if s.closed {
		return fs.ErrClosed
	}
	dir, last, _, err := s.resolve(name, noTerminalSlash)
	if err != nil {
		return err
	}
	switch {
	case lookup(dir, last) != nil:
		return syscall.EEXIST
	case dir.unlinked:
		return syscall.ENOENT
	}
	d := newDir(perm, dir)
	dir.entries[last] = d
	s.w.addEntry(p, dir, last, d)
	return nil
}

func (s *FS) Remove(name string) error {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if err := s.remove(name); err != nil {
		return &fs.PathError{Op: "removeat", Path: name, Err: err}
	}
	return nil
}

func (s *FS) remove(name string) error {
	p := s.path(name)
	if _, err := s.w.begin(Op{Kind: OpRemove, Path: p}); err != nil {
		return err
	}
	if s.closed {
		return fs.ErrClosed
	}
	dir, last, _, err := s.resolve(name, 0)
	if err != nil {
		return err
	}
	if last == "." {
		return syscall.EINVAL
	}
	n := dir.entries[last]
	switch {
	case n == nil:
		return syscall.ENOENT
	case n.dir && len(n.entries) > 0:
		return syscall.ENOTEMPTY
	}
	delete(dir.entries, last)
	if n.dir {
		n.unlinked, n.parent = true, nil
	}
	s.w.addEntry(p, dir, last, nil)
	return nil
}

// Rename renames as os.Root does: it replaces a file, but not a directory.
func (s *FS) Rename(oldname, newname string) error {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if err := s.rename(oldname, newname); err != nil {
		return &os.LinkError{Op: "renameat", Old: oldname, New: newname, Err: err}
	}
	return nil
}

func (s *FS) rename(oldname, newname string) error {
	if _, err := s.w.begin(Op{Kind: OpRename, Path: s.path(oldname)}); err != nil {
		return err
	}
	if s.closed {
		return fs.ErrClosed
	}
	od, olast, _, err := s.resolve(oldname, 0)
	if err != nil {
		return err
	}
	nd, nlast, newEndsInSlash, err := s.resolve(newname, creatingDir)
	if err != nil {
		return err
	}
	o := lookup(od, olast)
	if newEndsInSlash {
		switch {
		case o == nil:
			return syscall.ENOENT
		case !o.dir:
			return syscall.ENOTDIR
		}
	}
	if n := lookup(nd, nlast); n != nil && n.dir {
		switch {
		case o == nil:
			return syscall.ENOENT
		case nlast == olast || n != o:
			return syscall.EEXIST
		}
	}
	if olast == "." || nlast == "." {
		return syscall.EBUSY
	}
	n := nd.entries[nlast]
	switch {
	case o == nil:
		return syscall.ENOENT
	case n == o:
		return nil
	case o.dir && n != nil:
		return syscall.ENOTDIR
	case nd.unlinked:
		return syscall.ENOENT
	}
	if o.dir {
		for d := nd; d != nil; d = d.parent {
			if d == o {
				return syscall.EINVAL
			}
		}
	}
	delete(od.entries, olast)
	nd.entries[nlast] = o
	if o.dir {
		o.parent = nd
	}
	newPath, oldPath := s.path(newname), s.path(oldname)
	if od == nd {
		s.w.units = append(s.w.units, &unit{kind: PendingEntry, path: newPath, n: nd,
			sets: []entrySet{{dir: nd, name: nlast, node: o}, {dir: od, name: olast}}})
		return nil
	}
	ins := &unit{kind: PendingEntry, path: newPath, n: nd, sets: []entrySet{{dir: nd, name: nlast, node: o}}}
	s.w.units = append(s.w.units, ins,
		&unit{kind: PendingEntry, path: oldPath, n: od, sets: []entrySet{{dir: od, name: olast}}, ins: ins})
	return nil
}

func (s *FS) Lstat(name string) (fs.FileInfo, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	info, err := s.lstat(name)
	if err != nil {
		return nil, &fs.PathError{Op: "statat", Path: name, Err: err}
	}
	return info, nil
}

func (s *FS) lstat(name string) (fs.FileInfo, error) {
	if _, err := s.w.begin(Op{Kind: OpLstat, Path: s.path(name)}); err != nil {
		return nil, err
	}
	n, err := s.find(name)
	if err != nil {
		return nil, err
	}
	return n.info(path.Base(name)), nil
}

func (s *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if _, err := s.w.begin(Op{Kind: OpReadDir, Path: s.path(name)}); err != nil {
		return nil, &fs.PathError{Op: "readdirent", Path: joinPath(s.name, name), Err: err}
	}
	n, err := s.find(name)
	if err != nil {
		return nil, &fs.PathError{Op: "openat", Path: name, Err: err}
	}
	switch {
	case !n.dir:
		return []fs.DirEntry{}, &fs.PathError{Op: "readdirent", Path: joinPath(s.name, name), Err: syscall.ENOTDIR}
	case n.unlinked:
		return []fs.DirEntry{}, &fs.PathError{Op: "readdirent", Path: joinPath(s.name, name), Err: syscall.ENOENT}
	}
	return n.list(), nil
}

// SyncDir syncs name: for a directory, the changes of its entries, and for a file, those of its data.
func (s *FS) SyncDir(name string) error {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if f, err := s.w.begin(Op{Kind: OpSyncDir, Path: s.path(name)}); err != nil {
		if f != nil && f.Drop {
			if n, err := s.find(name); err == nil {
				s.w.drop(n)
			}
		}
		return errors.Join(&fs.PathError{Op: "sync", Path: joinPath(s.name, name), Err: err})
	}
	n, err := s.find(name)
	if err != nil {
		return &fs.PathError{Op: "openat", Path: name, Err: err}
	}
	s.w.sync(n)
	return nil
}

func (s *FS) Sub(name string) (vfs.FS, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	p := s.path(name)
	if _, err := s.w.begin(Op{Kind: OpOpen, Path: p}); err != nil {
		return nil, &fs.PathError{Op: "openat", Path: name, Err: err}
	}
	n, err := s.find(name)
	if err != nil {
		return nil, &fs.PathError{Op: "openat", Path: name, Err: err}
	}
	joined := joinPath(s.name, name)
	if !n.dir {
		return nil, &fs.PathError{Op: "open", Path: joined, Err: errNotDirectory}
	}
	return &FS{w: s.w, dir: n, name: joined, prefix: p}, nil
}

// Lock locks the file or directory name as flock(2) does: every lock is its own holder, which conflicts with the other
// holders of the same file, and unlock releases it.
func (s *FS) Lock(name string, mode vfs.LockMode) (unlock func() error, err error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if _, err := s.w.begin(Op{Kind: OpLock, Path: s.path(name)}); err != nil {
		return nil, &fs.PathError{Op: "flock", Path: name, Err: err}
	}
	if mode != vfs.Shared && mode != vfs.Exclusive {
		return nil, &fs.PathError{Op: "flock", Path: name, Err: syscall.EINVAL}
	}
	n, err := s.find(name)
	if err != nil {
		return nil, &fs.PathError{Op: "openat", Path: name, Err: err}
	}
	if n.exclusive || mode == vfs.Exclusive && n.shared > 0 {
		return nil, &fs.PathError{Op: "flock", Path: name, Err: vfs.ErrLocked}
	}
	if mode == vfs.Exclusive {
		n.exclusive = true
	} else {
		n.shared++
	}
	l := &lock{w: s.w, n: n, name: joinPath(s.name, name), exclusive: mode == vfs.Exclusive}
	return l.unlock, nil
}

type lock struct {
	w         *world
	n         *inode
	name      string // in errors
	exclusive bool
	released  bool
}

func (l *lock) unlock() error {
	l.w.mu.Lock()
	defer l.w.mu.Unlock()
	if l.released {
		return &fs.PathError{Op: "close", Path: l.name, Err: fs.ErrClosed}
	}
	l.released = true
	if l.exclusive {
		l.n.exclusive = false
	} else {
		l.n.shared--
	}
	if l.w.crashed() {
		return &fs.PathError{Op: "close", Path: l.name, Err: ErrCrashed}
	}
	return nil
}

// Close closes s, as Root.Close does: its files and the FSes of its Sub stay open.
func (s *FS) Close() error {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	s.closed = true
	if s.w.crashed() {
		return &fs.PathError{Op: "close", Path: s.name, Err: ErrCrashed}
	}
	return nil
}

// file is a file or directory that an FS has opened, which behaves as an *os.File on Linux.
type file struct {
	w           *world
	n           *inode
	name        string // in errors, as *os.File has it
	path        string // from the root of the world
	read, write bool
	off         int64
	closed      bool
	listed      bool          // directories: rest has been taken
	rest        []fs.DirEntry // what ReadDir has not returned yet
}

func (f *file) fail(op string, err error) error {
	return &fs.PathError{Op: op, Path: f.name, Err: err}
}

func (f *file) readable() error {
	switch {
	case f.closed:
		return fs.ErrClosed
	case !f.read:
		return syscall.EBADF
	case f.n.dir:
		return syscall.EISDIR
	}
	return nil
}

func (f *file) writable() error {
	switch {
	case f.closed:
		return fs.ErrClosed
	case !f.write:
		return syscall.EBADF
	}
	return nil
}

func (f *file) Read(p []byte) (int, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if _, err := f.w.begin(Op{Kind: OpRead, Path: f.path, Off: f.off, Len: len(p)}); err != nil {
		return 0, f.fail("read", err)
	}
	if f.closed {
		return 0, f.fail("read", fs.ErrClosed)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := f.readable(); err != nil {
		return 0, f.fail("read", err)
	}
	if f.off >= int64(len(f.n.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.n.data[f.off:])
	f.off += int64(n)
	return n, nil
}

func (f *file) ReadAt(p []byte, off int64) (int, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if _, err := f.w.begin(Op{Kind: OpRead, Path: f.path, Off: off, Len: len(p)}); err != nil {
		return 0, f.fail("read", err)
	}
	if off < 0 {
		return 0, f.fail("readat", errNegativeOffset)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := f.readable(); err != nil {
		return 0, f.fail("read", err)
	}
	if off >= int64(len(f.n.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.n.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *file) Write(p []byte) (int, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if flt, err := f.w.begin(Op{Kind: OpWrite, Path: f.path, Off: f.off, Len: len(p)}); err != nil {
		n := f.short(flt, p, f.off)
		f.off += int64(n)
		return n, f.fail("write", err)
	}
	if err := f.writable(); err != nil {
		return 0, f.fail("write", err)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if f.off+int64(len(p)) > maxFileSize {
		return 0, f.fail("write", syscall.EFBIG)
	}
	f.w.write(f.n, f.path, f.off, p)
	f.off += int64(len(p))
	return len(p), nil
}

func (f *file) WriteAt(p []byte, off int64) (int, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if flt, err := f.w.begin(Op{Kind: OpWrite, Path: f.path, Off: off, Len: len(p)}); err != nil {
		return f.short(flt, p, off), f.fail("write", err)
	}
	if off < 0 {
		return 0, f.fail("writeat", errNegativeOffset)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := f.writable(); err != nil {
		return 0, f.fail("write", err)
	}
	if off+int64(len(p)) > maxFileSize {
		return 0, f.fail("write", syscall.EFBIG)
	}
	f.w.write(f.n, f.path, off, p)
	return len(p), nil
}

// short writes the first bytes of p at off that the fault flt of a write lets through, and returns how many.
func (f *file) short(flt *fault, p []byte, off int64) int {
	if flt == nil || flt.Short == 0 || f.writable() != nil || off < 0 {
		return 0
	}
	n := min(flt.Short, len(p))
	if off+int64(n) > maxFileSize {
		return 0
	}
	f.w.write(f.n, f.path, off, p[:n])
	return n
}

func (f *file) Seek(offset int64, whence int) (int64, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if f.w.crashed() {
		return 0, f.fail("seek", ErrCrashed)
	}
	if f.closed {
		return 0, f.fail("seek", fs.ErrClosed)
	}
	var off int64
	switch whence {
	case io.SeekStart:
		off = offset
	case io.SeekCurrent:
		off = f.off + offset
	case io.SeekEnd:
		off = int64(len(f.n.data)) + offset
	default:
		return 0, f.fail("seek", syscall.EINVAL)
	}
	if off < 0 {
		return 0, f.fail("seek", syscall.EINVAL)
	}
	f.off = off
	if f.n.dir && off == 0 {
		f.listed, f.rest = false, nil
	}
	return off, nil
}

// Sync makes the changes of the data of the file durable, or for a directory those of its entries.
func (f *file) Sync() error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if flt, err := f.w.begin(Op{Kind: OpSync, Path: f.path}); err != nil {
		if flt != nil && flt.Drop && !f.closed {
			f.w.drop(f.n)
		}
		return f.fail("sync", err)
	}
	if f.closed {
		return f.fail("sync", fs.ErrClosed)
	}
	f.w.sync(f.n)
	return nil
}

func (f *file) Truncate(size int64) error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if _, err := f.w.begin(Op{Kind: OpTruncate, Path: f.path, Off: size}); err != nil {
		return f.fail("truncate", err)
	}
	switch {
	case f.closed:
		return f.fail("truncate", fs.ErrClosed)
	case size < 0 || !f.write:
		return f.fail("truncate", syscall.EINVAL)
	case size > maxFileSize:
		return f.fail("truncate", syscall.EFBIG)
	}
	f.w.truncate(f.n, f.path, size)
	return nil
}

func (f *file) Stat() (fs.FileInfo, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if _, err := f.w.begin(Op{Kind: OpLstat, Path: f.path}); err != nil {
		return nil, f.fail("stat", err)
	}
	if f.closed {
		return nil, f.fail("stat", fs.ErrClosed)
	}
	return f.n.info(path.Base(f.name)), nil
}

// ReadDir returns the entries in the order of their names.
func (f *file) ReadDir(n int) ([]fs.DirEntry, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if _, err := f.w.begin(Op{Kind: OpReadDir, Path: f.path}); err != nil {
		return []fs.DirEntry{}, f.fail("readdirent", err)
	}
	switch {
	case f.closed:
		return []fs.DirEntry{}, f.fail("readdirent", errClosedFile)
	case !f.n.dir:
		return []fs.DirEntry{}, f.fail("readdirent", syscall.ENOTDIR)
	case f.n.unlinked:
		return []fs.DirEntry{}, f.fail("readdirent", syscall.ENOENT)
	}
	if !f.listed {
		f.rest, f.listed = f.n.list(), true
	}
	if n <= 0 {
		entries := f.rest
		f.rest = nil
		if entries == nil {
			entries = []fs.DirEntry{}
		}
		return entries, nil
	}
	if len(f.rest) == 0 {
		return []fs.DirEntry{}, io.EOF
	}
	n = min(n, len(f.rest))
	entries := f.rest[:n:n]
	f.rest = f.rest[n:]
	return entries, nil
}

func (f *file) Close() error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if f.w.crashed() {
		f.closed = true
		return f.fail("close", ErrCrashed)
	}
	if f.closed {
		return f.fail("close", fs.ErrClosed)
	}
	f.closed = true
	return nil
}

func (n *inode) info(name string) fs.FileInfo {
	info := fileInfo{name: name, mode: n.perm}
	if n.dir {
		info.mode |= fs.ModeDir
	} else {
		info.size = int64(len(n.data))
	}
	return info
}

// list returns the entries of the directory n, sorted by name.
func (n *inode) list() []fs.DirEntry {
	names := slices.Sorted(maps.Keys(n.entries))
	entries := make([]fs.DirEntry, len(names))
	for i, name := range names {
		entries[i] = fs.FileInfoToDirEntry(n.entries[name].info(name))
	}
	return entries
}

type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (fi fileInfo) Name() string       { return fi.name }
func (fi fileInfo) Size() int64        { return fi.size }
func (fi fileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi fileInfo) ModTime() time.Time { return time.Time{} }
func (fi fileInfo) IsDir() bool        { return fi.mode.IsDir() }
func (fi fileInfo) Sys() any           { return nil }
