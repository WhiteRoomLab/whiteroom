package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/dmlog"
)

// The check of the store with dm-log-writes of Linux (3.5; internal/logdb/dmlog): record.sh runs TestDMLogWorkload
// (dmlog_workload_test.go) in a file system through the target, as root, and TestDMLogCheck replays the log of the
// recording, and looks at the store in each state of the device: without root, in an image of ext4, which e2fsck and
// debugfs read; or as root, in the file system, of any type, which it mounts (vm.nix runs record.sh and this in a
// virtual machine). TestExt4Extraction checks the way of TestDMLogCheck from an image of ext4 to a store, with images
// that mke2fs makes of stores of the workload.

const (
	envDMLog       = "WHITEROOM_DMLOG"        // the directory of a recording
	envDMLogPoints = "WHITEROOM_DMLOG_POINTS" // how many states TestDMLogCheck checks
	envDMLogStates = "WHITEROOM_DMLOG_STATES" // all: TestDMLogCheck checks the state after every entry
	envDMLogMount  = "WHITEROOM_DMLOG_MOUNT"  // the type of the file system, which TestDMLogCheck mounts, as root
	envDMLogWant   = "WHITEROOM_DMLOG_WANT"   // problems: TestDMLogCheck passes only if it finds some
)

// dmlogCheck is how checkRecording checks a recording.
type dmlogCheck struct {
	limit int    // the number of states that it checks, or 0 for all of them
	every bool   // the states are those of dmlogStates with every
	mount string // the type of the file system, which it mounts, as root; or "" for ext4, without root
}

// TestDMLogCheck checks the recording of record.sh in the directory envDMLog with checkRecording: at every point after
// the mark mkfs, or with envDMLogStates=all, after every entry; or in the first envDMLogPoints of these states. It
// needs e2fsck and debugfs, and no root; or with envDMLogMount, root, mount and umount. With envDMLogWant=problems, it
// passes only if it finds a problem of the store, as it must in a recording of a workload that drops syncs
// (envDMLogLoose).
func TestDMLogCheck(t *testing.T) {
	dir := os.Getenv(envDMLog)
	if dir == "" {
		t.Skip("checks a recording of internal/logdb/dmlog/record.sh in the directory " + envDMLog)
	}
	c := dmlogCheck{mount: os.Getenv(envDMLogMount)}
	if v := os.Getenv(envDMLogPoints); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("%s=%s: want a number of states from 1", envDMLogPoints, v)
		}
		c.limit = n
	}
	switch v := os.Getenv(envDMLogStates); v {
	case "":
	case "all":
		c.every = true
	default:
		t.Fatalf("%s=%s: want all", envDMLogStates, v)
	}
	wantProblems := false
	switch v := os.Getenv(envDMLogWant); v {
	case "":
	case "problems":
		wantProblems = true
	default:
		t.Fatalf("%s=%s: want problems", envDMLogWant, v)
	}
	needs := []string{"e2fsck", "debugfs"}
	if c.mount != "" {
		if os.Geteuid() != 0 {
			t.Fatalf("%s=%s: needs root, to mount the file system", envDMLogMount, c.mount)
		}
		needs = []string{"mount", "umount"}
	}
	if missing := missingCommands(needs...); missing != nil {
		t.Fatalf("needs %s", strings.Join(missing, " and "))
	}
	r, err := checkRecording(t, dir, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(r)
	for _, out := range r.fixed {
		t.Logf("e2fsck -fy has fixed the file system at %s", out)
	}
	if wantProblems {
		for _, p := range r.problems[:min(3, len(r.problems))] {
			t.Logf("a problem, as there must be: %s", p)
		}
	}
	for _, f := range r.failures(wantProblems) {
		t.Error(f)
	}
}

// missingCommands returns those of names that are not in PATH.
func missingCommands(names ...string) []string {
	var missing []string
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	return missing
}

// e2fsprogs returns the command name of e2fsprogs with args, in the C locale, and with the defaults of e2fsck in place
// of a configuration file.
func e2fsprogs(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "E2FSCK_CONFIG=/dev/null")
	return cmd
}

// e2fsck runs e2fsck -fy on the image name, and returns its exit code and its output.
func e2fsck(name string) (int, string, error) {
	out, err := e2fsprogs("e2fsck", "-fy", name).CombinedOutput()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() > 0 {
		return ee.ExitCode(), string(out), nil
	}
	return 0, string(out), err
}

var errNotInImage = errors.New("not in the image")

// rdump copies the directory path of the image name into dir with debugfs, which needs a dir without spaces. debugfs
// cannot give the files to their owner if it is another user, such as root after record.sh: they are then those of
// the user of the process.
func rdump(name, path, dir string) error {
	if strings.ContainsAny(dir, " \t\"\\") {
		return fmt.Errorf("debugfs cannot copy into %q", dir)
	}
	out, err := e2fsprogs("debugfs", "-R", "rdump "+path+" "+dir, name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("debugfs: %w\n%s", err, out)
	}
	// debugfs exits with 0 after errors, which it prints after the line of its version.
	var other []string
	for i, line := range strings.Split(string(out), "\n") {
		switch {
		case line == "", i == 0 && strings.HasPrefix(line, "debugfs "):
		case strings.Contains(line, ": Operation not permitted while changing ownership of "):
		case strings.HasPrefix(line, path+": File not found by ext2_lookup"):
			return fmt.Errorf("%s: %w", path, errNotInImage)
		default:
			other = append(other, line)
		}
	}
	if other != nil {
		return fmt.Errorf("debugfs: %s", strings.Join(other, "\n"))
	}
	_, err = os.Lstat(filepath.Join(dir, filepath.Base(path)))
	return err
}

// extents are the ranges [start, end) of bytes of a device that a replay has written, in order, with bytes between
// them.
type extents [][2]int64

func (x extents) add(off, n int64) extents {
	if n == 0 {
		return x
	}
	start, end := off, off+n
	i := sort.Search(len(x), func(i int) bool { return x[i][1] >= start })
	j := i
	for ; j < len(x) && x[j][0] <= end; j++ {
		start, end = min(start, x[j][0]), max(end, x[j][1])
	}
	return slices.Replace(x, i, j, [2]int64{start, end})
}

// copyExtents copies the bytes x of dev, a device of size bytes, into a new file name of the same size, which is sparse
// elsewhere.
func copyExtents(name string, dev io.ReaderAt, size int64, x extents) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	err = f.Truncate(size)
	buf := make([]byte, 1<<20)
	for _, r := range x {
		if err != nil {
			break
		}
		_, err = io.CopyBuffer(io.NewOffsetWriter(f, r[0]), io.NewSectionReader(dev, r[0], r[1]-r[0]), buf)
	}
	return errors.Join(err, f.Close())
}

// dmlogPoint is a state of the device of a recording of the workload, after an entry of its log, with what the marks
// of the workload before it tell (runPhase).
type dmlogPoint struct {
	index       int // of the entry
	entry       dmlog.Entry
	acked       int    // n of the last mark ack-<n> or seen-<n>, or -1
	closed      bool   // after the mark closed, until the mark open or maintain
	backup      int    // n of the mark backup-<n>, or -1
	generation  uint64 // g of the last mark generation-<g>, or 1
	maintaining bool   // after the mark maintain, until the mark generation-<g>
}

func newDMLogPoint() *dmlogPoint {
	return &dmlogPoint{acked: -1, backup: -1, generation: 1}
}

// see takes e, an entry of the log up to the state, if it is a mark of the workload.
func (p *dmlogPoint) see(e dmlog.Entry) error {
	if e.Flags&dmlog.FlagMark == 0 {
		return nil
	}
	bad := fmt.Errorf("the mark %q %s", e.Mark, p.after())
	kind, value, _ := strings.Cut(e.Mark, "-")
	n, err := strconv.Atoi(value)
	switch kind {
	case "ack", "seen":
		// A process that opens the store may see the pair of the last ack again.
		if err != nil || n < p.acked || n == p.acked && kind == "ack" || p.closed || p.maintaining {
			return bad
		}
		p.acked = n
	case "backup":
		if err != nil || n != p.acked || p.backup >= 0 || p.closed || p.maintaining {
			return bad
		}
		p.backup = n
	case "generation":
		g, err := strconv.ParseUint(value, 10, 64)
		if err != nil || g < p.generation || !p.maintaining {
			return bad
		}
		p.generation, p.maintaining = g, false
	}
	switch e.Mark {
	case "closed":
		if p.maintaining {
			return bad
		}
		p.closed = true
	case "open":
		if p.maintaining {
			return bad
		}
		p.closed = false
	case "maintain":
		p.closed, p.maintaining = false, true
	}
	return nil
}

// after tells what the marks before the state tell.
func (p *dmlogPoint) after() string {
	after := "before an ack"
	if p.acked >= 0 {
		after = fmt.Sprintf("after ack-%d", p.acked)
	}
	if p.closed {
		after += " and closed"
	}
	if p.maintaining {
		after += ", in the maintenance"
	}
	return after
}

func (p *dmlogPoint) String() string {
	return fmt.Sprintf("entry %d (%s), %s", p.index, p.entry.String(), p.after())
}

// dmlogStates returns the indexes of the entries of l after which checkRecording looks at the device: the points of
// l; or with every, each entry that changes the device or is a mark, as a device may have written any of the writes
// before a flush when the power is lost. A flush without data changes nothing after the entry before it.
func dmlogStates(l *dmlog.Log, every bool) []int {
	if !every {
		return l.Points()
	}
	var states []int
	for i, e := range l.Entries {
		if _, n := l.Range(i); n > 0 || e.Flags&dmlog.FlagMark != 0 {
			states = append(states, i)
		}
	}
	return states
}

// dmlogReport is what checkRecording has found.
type dmlogReport struct {
	entries, points, checked int
	every                    bool
	fsck                     map[int]int    // the states by the exit code of e2fsck -fy
	fixed                    []string       // the states of the first outputs of e2fsck -fy with the code 1, with them
	mounted                  int            // the states that were mounted
	missing, unopened, early int            // the states before an ack: without the store, where it did not open, others
	acked, running           int            // the states after one with the pair of the last ack, and of the next Update
	readers                  int            // the states after an ack where a reader saw the store, before the writer
	backups                  int            // the states after the mark backup-<n> where the copy of Backup has the pair n
	generations              map[uint64]int // the states where the store opened, by its generation
	recovered                tally
	end                      string   // how the device after the log compares with data.img
	problems                 []string // of the store, in the states
	unread                   []string // the states whose file system could not be read, and a device that is not that of data.img
}

// failures returns what makes the check of the recording of r fail: what could not be read, and the problems of the
// store; or with wantProblems, that the store has no problem.
func (r *dmlogReport) failures(wantProblems bool) []string {
	f := slices.Clone(r.unread)
	switch {
	case !wantProblems:
		f = append(f, r.problems...)
	case len(r.problems) == 0:
		f = append(f, "no problem of the store in a recording that must have one")
	}
	return f
}

func (r *dmlogReport) String() string {
	states, how := "the flushes, the FUA writes and the marks", fmt.Sprintf("mounted at %d", r.mounted)
	if r.every {
		states = "the writes, the discards and the marks"
	}
	if len(r.fsck) > 0 {
		var codes []string
		for _, code := range slices.Sorted(maps.Keys(r.fsck)) {
			codes = append(codes, fmt.Sprintf("%d at %d", code, r.fsck[code]))
		}
		how = "e2fsck -fy exited with " + strings.Join(codes, ", ")
	}
	var generations []string
	for _, g := range slices.Sorted(maps.Keys(r.generations)) {
		generations = append(generations, fmt.Sprintf("%d at %d", g, r.generations[g]))
	}
	return fmt.Sprintf("%d entries, %d states after the mark mkfs (%s), %d checked: %s; before an ack, the store was "+
		"missing at %d, did not open at %d and opened at %d; after one, it had the pair of the last ack at %d and that "+
		"of the Update after it at %d; a reader saw it first at %d; the copy of Backup had its pair at %d; the "+
		"generation was %s; recovered: %v; the device after the log: %s; %d problems, %d not read", r.entries,
		r.points, states, r.checked, how, r.missing, r.unopened, r.early, r.acked, r.running, r.readers, r.backups,
		strings.Join(generations, ", "), r.recovered, r.end, len(r.problems), len(r.unread))
}

// checkRecording replays the recording in dir to each state of dmlogStates after the mark mkfs, or to the first
// c.limit of them, and checks the device in each with checkPoint. The recording is the files that record.sh makes:
// log.img, the log; data-size, the size of the device in bytes; and data.img, the device at the end, which the device
// after the whole log must equal, if it is there. checkRecording returns an error if it cannot read the recording, or
// it is not one of the workload, and the problems of the states in the report.
func checkRecording(t *testing.T, dir string, c dmlogCheck) (*dmlogReport, error) {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "log.img"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	l, err := dmlog.Parse(f, info.Size())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name(), err)
	}
	size, err := readDataSize(filepath.Join(dir, "data-size"))
	if err != nil {
		return nil, err
	}
	mkfs := -1
	for i, e := range l.Entries {
		if off, n := l.Range(i); off+n > size {
			return nil, fmt.Errorf("%s: the entry %d (%s) ends past the device of %d bytes", f.Name(), i, e.String(),
				size)
		}
		if mkfs < 0 && e.Flags&dmlog.FlagMark != 0 && e.Mark == "mkfs" {
			mkfs = i
		}
	}
	if n := len(l.Entries); n == 0 || l.Entries[n-1].Flags&dmlog.FlagMark == 0 ||
		l.Entries[n-1].Mark != "dm-log-writes-end" {
		return nil, fmt.Errorf("%s: the log does not end with the mark dm-log-writes-end", f.Name())
	}
	if mkfs < 0 {
		return nil, fmt.Errorf("%s: no mark mkfs", f.Name())
	}
	if a, b, ok := l.Unordered(); ok {
		return nil, fmt.Errorf("%s: the log does not tell the order of the entries %d (%s) and %d (%s)", f.Name(), a,
			l.Entries[a].String(), b, l.Entries[b].String())
	}

	work := t.TempDir()
	dev, err := os.Create(filepath.Join(work, "device.img"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = dev.Close() }()
	if err := dev.Truncate(size); err != nil {
		return nil, err
	}
	r := &dmlogReport{entries: len(l.Entries), every: c.every, fsck: map[int]int{}, generations: map[uint64]int{},
		recovered: tally{}}
	var written extents
	p := newDMLogPoint()
	from := 0
	for _, i := range dmlogStates(l, c.every) {
		if err := l.Replay(dev, f, from, i+1); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name(), err)
		}
		for ; from <= i; from++ {
			written = written.add(l.Range(from))
			if err := p.see(l.Entries[from]); err != nil {
				return nil, fmt.Errorf("%s: the entry %d: %w", f.Name(), from, err)
			}
		}
		if i < mkfs {
			continue
		}
		r.points++
		if c.limit > 0 && r.checked == c.limit {
			continue
		}
		r.checked++
		p.index, p.entry = i, l.Entries[i]
		if err := r.checkPoint(t, c, work, dev, size, written, p); err != nil {
			r.problems = append(r.problems, fmt.Sprintf("%s: %v", p.String(), err))
		}
	}
	switch end, err := compareDevice(dev, filepath.Join(dir, "data.img"), size); {
	case err != nil:
		r.unread = append(r.unread, fmt.Sprintf("the device after the log: %v", err))
	default:
		r.end = end
	}
	return r, nil
}

// readDataSize reads the size of the device of a recording in bytes from the file name.
func readDataSize(name string) (int64, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return 0, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || size <= 0 {
		return 0, fmt.Errorf("%s: not a size of a device", name)
	}
	return size, nil
}

// checkPoint checks the device in the state p, dev of size bytes, of which a replay has written the bytes written,
// with a copy of it that extract gives as a directory. After an ack, a reader, which repairs nothing, opens the store
// of the workload and passes checkWorkload with the pair n of the last ack or n+1, or after the mark closed n; then
// checkWriter passes, with a pair that is not before the one that the reader has seen; and after the mark
// backup-<n>, checkBackup passes. Before an ack, the store may be missing, and no reader looks. An error is a problem
// of the store; a file system that cannot be read is in r.unread.
func (r *dmlogReport) checkPoint(t *testing.T, c dmlogCheck, work string, dev io.ReaderAt, size int64,
	written extents, p *dmlogPoint) error {
	t.Helper()
	unread := func(err error) error {
		r.unread = append(r.unread, fmt.Sprintf("%s: %v", p.String(), err))
		return nil
	}
	img := filepath.Join(work, "state.img")
	// A new file: the loop device of the last mount may still have the last one.
	if err := os.Remove(img); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return unread(err)
	}
	if err := copyExtents(img, dev, size, written); err != nil {
		return unread(err)
	}
	root, done, err := r.extract(c, work, img, p)
	if err != nil {
		return unread(err)
	}
	defer func() {
		if err := done(); err != nil {
			_ = unread(err)
		}
	}()
	dir := filepath.Join(root, filepath.FromSlash(workloadData))
	switch _, err := os.Lstat(dir); {
	case errors.Is(err, fs.ErrNotExist) && p.acked < 0:
		r.missing++
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("/%s: %w", workloadData, errNotInImage)
	case err != nil:
		return unread(err)
	}
	lo, hi := p.acked, p.acked+1
	switch {
	case p.acked < 0:
		lo, hi = 0, 0
	case p.closed:
		hi = lo
	}
	seen, rerr := 0, error(nil)
	if p.acked >= 0 {
		if seen, rerr = readerSees(t, dir, lo, hi, p.closed); rerr == nil {
			r.readers++
		}
	}
	n, werr := r.checkWriter(t, dir, p, lo, hi)
	if werr == nil && rerr == nil && n < seen {
		werr = fmt.Errorf("the writer has the pair of %d after a reader has seen that of %d", n, seen)
	}
	// The error of the writer first: it is the one that the store is not usable with.
	if err := errors.Join(werr, rerr); err != nil || p.backup < 0 {
		return err
	}
	return r.checkBackup(t, root, p)
}

// readerSees opens the store of dir as a reader, and returns its pair after checkWorkload.
func readerSees(t *testing.T, dir string, lo, hi int, closed bool) (int, error) {
	t.Helper()
	s, err := openLogReader(dir, workloadOptions())
	if err != nil {
		return 0, fmt.Errorf("a reader: openLogReader() = %w", err)
	}
	n, err := checkWorkload(t, s, lo, hi, closed)
	if err := errors.Join(err, s.Close()); err != nil {
		return n, fmt.Errorf("a reader: %w", err)
	}
	return n, nil
}

// checkWriter checks the store of dir, the data directory of the workload in the state p, as its writer: it opens,
// and passes checkWorkload with a pair from lo to hi; it is of the generation of the last mark generation-<g>, or in
// the maintenance, of that one or a later one; and after the next Update of the workload and Close, it opens with
// that Update. Before an ack, the store may not open. checkWriter returns the pair.
func (r *dmlogReport) checkWriter(t *testing.T, dir string, p *dmlogPoint, lo, hi int) (int, error) {
	t.Helper()
	s, err := open(dir, workloadOptions())
	if err != nil {
		if p.acked < 0 {
			r.unopened++
			return 0, nil
		}
		return 0, fmt.Errorf("open() = %w", err)
	}
	info := logDB(t, s).Recovery()
	r.recovered.add(info)
	r.generations[info.Generation]++
	n, err := checkWorkload(t, s, lo, hi, p.closed)
	switch {
	case err != nil:
	case p.maintaining && info.Generation < p.generation:
		err = fmt.Errorf("the generation %d, want %d or a later one", info.Generation, p.generation)
	case !p.maintaining && info.Generation != p.generation:
		err = fmt.Errorf("the generation %d, want %d", info.Generation, p.generation)
	default:
		err = workloadUpdate(t.Context(), s, n+1)
	}
	if err := errors.Join(err, s.Close()); err != nil {
		return n, err
	}
	switch {
	case p.acked < 0:
		r.early++
	case n == p.acked:
		r.acked++
	default:
		r.running++
	}
	again, err := open(dir, workloadOptions())
	if err != nil {
		return n, fmt.Errorf("open() after the Update %d = %w", n+1, err)
	}
	if _, err = checkWorkload(t, again, n+1, n+1, true); err != nil {
		err = fmt.Errorf("after the Update %d: %w", n+1, err)
	}
	return n, errors.Join(err, again.Close())
}

// checkBackup checks the copy of Backup in root, the recorded file system in a state p after the mark backup-<n>: a
// reader, and then the writer, open it, and it passes checkWorkload with the pair n.
func (r *dmlogReport) checkBackup(t *testing.T, root string, p *dmlogPoint) error {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(workloadBackup))
	_, rerr := readerSees(t, dir, p.backup, p.backup, false)
	werr := func() error {
		s, err := open(dir, workloadOptions())
		if err != nil {
			return fmt.Errorf("open() = %w", err)
		}
		_, err = checkWorkload(t, s, p.backup, p.backup, false)
		return errors.Join(err, s.Close())
	}()
	if err := errors.Join(werr, rerr); err != nil {
		return fmt.Errorf("the copy of Backup: %w", err)
	}
	r.backups++
	return nil
}

// extract gives the recorded file system of the image img, a copy of the device in the state p, as the directory
// root, until done: with c.mount, the file system, mounted; else what debugfs copies out of it of the directory of
// the workload, which may be missing, after e2fsck -fy has fixed the image and exited with 0 or 1.
func (r *dmlogReport) extract(c dmlogCheck, work, img string, p *dmlogPoint) (root string, done func() error,
	err error) {
	if c.mount != "" {
		mnt := filepath.Join(work, "mnt")
		if err := os.MkdirAll(mnt, 0o700); err != nil {
			return "", nil, err
		}
		if out, err := mountCommand("mount", "-t", c.mount, "-o", "loop", img, mnt).CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("mount: %w\n%s", err, out)
		}
		r.mounted++
		return mnt, func() error {
			if out, err := mountCommand("umount", mnt).CombinedOutput(); err != nil {
				return fmt.Errorf("umount: %w\n%s", err, out)
			}
			return nil
		}, nil
	}
	code, out, err := e2fsck(img)
	if err != nil {
		return "", nil, fmt.Errorf("e2fsck: %w\n%s", err, out)
	}
	r.fsck[code]++
	switch {
	case code == 1 && len(r.fixed) < 3:
		r.fixed = append(r.fixed, p.String()+":\n"+out)
	case code > 1:
		return "", nil, fmt.Errorf("e2fsck -fy has exited with %d:\n%s", code, out)
	}
	dump := filepath.Join(work, "dump")
	if err := os.RemoveAll(dump); err != nil {
		return "", nil, err
	}
	if err := os.Mkdir(dump, 0o700); err != nil {
		return "", nil, err
	}
	if err := rdump(img, "/"+workloadTop, dump); err != nil && !errors.Is(err, errNotInImage) {
		return "", nil, err
	}
	return dump, func() error { return nil }, nil
}

// mountCommand returns the command name of util-linux with args, in the C locale.
func mountCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd
}

// checkWorkload returns the n of the pair of s, a store of the workload, after it has checked that n is from lo to hi,
// that s has the records store.create and store.write of the pairs 1 to n, the media documents of the Updates up to n
// and their originals, whole, and passes checkStore; and if closed, that open has used the checkpoint of Close and
// replayed no record.
func checkWorkload(t *testing.T, s *Store, lo, hi int, closed bool) (int, error) {
	t.Helper()
	n := 0
	if err := s.View(t.Context(), func(tx *Tx) error {
		var err error
		if n, err = checkPairView(tx); err != nil {
			return err
		}
		docs, err := tx.Documents(content.RefMedia)
		if err != nil {
			return err
		}
		for i, d := range docs {
			k := 10 * (i + 1)
			if id := workloadMedia(k); d.ID != id || !bytes.Equal(d.Data, mediaJSON(id, workloadOriginal(k))) {
				return fmt.Errorf("the media document %d is %s %s, want %s", i+1, d.ID, d.Data, id)
			}
		}
		if len(docs) != n/10 {
			return fmt.Errorf("%d media documents with the pair of %d", len(docs), n)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if n < lo || n > hi {
		return n, fmt.Errorf("the pair of %d, want one from %d to %d", n, lo, hi)
	}
	for k := 10; k <= n; k += 10 {
		switch ok, err := hasOriginal(s, workloadOriginal(k)); {
		case err != nil:
			return n, fmt.Errorf("the original of the Update %d: %w", k, err)
		case !ok:
			return n, fmt.Errorf("the original of the Update %d is missing", k)
		}
	}
	var records []string
	if err := s.ReadAudit(t.Context(), audit.Filter{}, func(r audit.Record) bool {
		records = append(records, fmt.Sprintf("%d %s %s %s", r.N, r.Log, r.Action, r.Detail["pair"]))
		return true
	}); err != nil {
		return n, err
	}
	want := []string{"1 activity store.create "}
	for k := 1; k <= n; k++ {
		want = append(want, fmt.Sprintf("%d activity store.write %d", k+1, k))
	}
	if !slices.Equal(records, want) {
		return n, fmt.Errorf("the audit records %q, want %q", records, want)
	}
	if err := checkStore(t, s); err != nil {
		return n, err
	}
	if info := logDB(t, s).Recovery(); closed && (info.Checkpoint != "used" || info.Replayed != 0) {
		return n, fmt.Errorf("after Close, open has found %+v", info)
	}
	return n, nil
}

// compareDevice compares dev, a device of size bytes, with the file name, and says how; if name is missing, it says
// so.
func compareDevice(dev io.ReaderAt, name string, size int64) (string, error) {
	f, err := os.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return "not compared: no " + filepath.Base(name), nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() != size {
		return "", fmt.Errorf("%s has %d bytes, the device %d", name, info.Size(), size)
	}
	a, b := make([]byte, 1<<20), make([]byte, 1<<20)
	for off := int64(0); off < size; off += int64(len(a)) {
		n := min(int64(len(a)), size-off)
		if _, err := io.ReadFull(io.NewSectionReader(dev, off, n), a[:n]); err != nil {
			return "", err
		}
		if _, err := io.ReadFull(io.NewSectionReader(f, off, n), b[:n]); err != nil {
			return "", err
		}
		if !bytes.Equal(a[:n], b[:n]) {
			i := 0
			for a[i] == b[i] {
				i++
			}
			return "", fmt.Errorf("it differs from %s at the byte %d", name, off+int64(i))
		}
	}
	return "the same as " + filepath.Base(name), nil
}

// syntheticLog makes a log of dm-log-writes with sectors of 512 bytes, as internal/logdb/dmlog describes it.
type syntheticLog struct {
	b       []byte
	entries uint64
}

const syntheticSector = 512

func newSyntheticLog() *syntheticLog {
	return &syntheticLog{b: make([]byte, syntheticSector)}
}

func (l *syntheticLog) entry(sector, sectors, flags uint64, mark string, data []byte) {
	e := make([]byte, syntheticSector)
	binary.LittleEndian.PutUint64(e[0:], sector)
	binary.LittleEndian.PutUint64(e[8:], sectors)
	binary.LittleEndian.PutUint64(e[16:], flags)
	binary.LittleEndian.PutUint64(e[24:], uint64(len(mark)))
	copy(e[32:], mark)
	l.b = append(append(l.b, e...), data...)
	l.entries++
}

func (l *syntheticLog) mark(text string) {
	l.entry(0, 0, dmlog.FlagMark, text, nil)
}

func (l *syntheticLog) flush() {
	l.entry(0, 0, dmlog.FlagFlush, "", nil)
}

// change appends what makes the device prev into next, in blocks of 4096 bytes: a write of each block that differs,
// or a discard if next has zeros in it. The last write has the flags last.
func (l *syntheticLog) change(prev, next []byte, last uint64) {
	const block = 4096
	zeros := make([]byte, block)
	write := -1 // the offset of the last write in l.b
	for off := 0; off < len(next); off += block {
		b := next[off : off+block]
		switch {
		case bytes.Equal(prev[off:off+block], b):
		case bytes.Equal(b, zeros):
			l.entry(uint64(off/syntheticSector), block/syntheticSector, dmlog.FlagDiscard, "", nil)
		default:
			write = len(l.b)
			l.entry(uint64(off/syntheticSector), block/syntheticSector, 0, "", b)
		}
	}
	if write >= 0 {
		binary.LittleEndian.PutUint64(l.b[write+16:], last)
	}
}

// write writes the recording of the log with the device last at its end into dir.
func (l *syntheticLog) write(t *testing.T, dir string, last []byte) {
	t.Helper()
	binary.LittleEndian.PutUint64(l.b[0:], dmlog.Magic)
	binary.LittleEndian.PutUint64(l.b[8:], dmlog.Version)
	binary.LittleEndian.PutUint64(l.b[16:], l.entries)
	binary.LittleEndian.PutUint32(l.b[24:], syntheticSector)
	for name, data := range map[string][]byte{"log.img": l.b, "data.img": last,
		"data-size": fmt.Appendf(nil, "%d\n", len(last))} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// mke2fs makes an image of ext4 of 8 MiB at name, with the files of dir in it if dir is not "", with the defaults of
// mke2fs in place of a configuration file, and returns its bytes.
func mke2fs(t *testing.T, name, dir string) []byte {
	t.Helper()
	args := []string{"-q", "-F", "-t", "ext4"}
	if dir != "" {
		args = append(args, "-d", dir)
	}
	cmd := e2fsprogs("mke2fs", append(args, name, "8M")...)
	cmd.Env = append(cmd.Env, "MKE2FS_CONFIG="+name+".missing.conf")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestExt4Extraction checks the way of TestDMLogCheck from an image of ext4 to a store, without root, with images that
// mke2fs makes of an empty file system and of the directory of the workload: with the store after 9 Updates and after
// 10, closed; with these and the copy of Backup of the store of 9; and with that copy and the store of 10 after a
// compaction. e2fsck -fy finds them clean; debugfs copies out of them the files as they were, and the stores open
// with them (or the directory is missing from the empty one, and debugfs fails on an image of zeros). checkRecording
// finds no problem in the recordings of logs made from the images, with writes of the blocks that change (the last
// of a state with FUA), discards of those that become zeros, flushes and the marks of the workload, also with a
// store that does not open before the first ack, and finds the
// problem in recordings of other logs: the mark ack-10 or seen-10 before the Update 10; the store with two Updates
// after the last ack, with one before an ack, with one after the mark closed, lost after an ack, or with another
// audit record; a store that no process opens, that its writer cannot open, without an original or a media document
// of its Updates, with an index that is not that of its documents, with a commit after its checkpoint at the mark
// closed, or that takes no Update; no copy of Backup after the mark backup-9, or one of another Update than the
// mark tells; the generation 1 after the mark generation-2, also in a later maintenance; a damaged file system;
// another data.img; acks out of order; and a write after one with FUA of the same block, without a flush.
func TestExt4Extraction(t *testing.T) {
	if missing := missingCommands("mke2fs", "e2fsck", "debugfs"); missing != nil {
		t.Skipf("needs %s of e2fsprogs, which the dev shell has on Linux", strings.Join(missing, ", "))
	}
	t.Parallel() // with the matrices, after the tests that do not run in parallel
	work := t.TempDir()
	none := func(string) error { return nil }
	// tree returns the root of a new file system of the workload, and its data directory.
	tree := func(name string) (root, data string) {
		root = filepath.Join(work, name)
		return root, filepath.Join(root, filepath.FromSlash(workloadData))
	}
	image := func(name, root string) []byte {
		return mke2fs(t, filepath.Join(work, name+".img"), root)
	}
	empty := image("empty", "")
	root, dir := tree("plain")
	images := map[int][]byte{}
	trees := map[int]map[string]string{}
	from := 0
	for _, n := range []int{9, 10} {
		if err := runWorkload(t.Context(), dir, from, n, none); err != nil {
			t.Fatalf("the workload to %d: %v", n, err)
		}
		images[n] = image(fmt.Sprintf("store-%d", n), root)
		trees[n] = snapshot(t, filepath.Join(root, workloadTop))
		from = n
	}
	// The store of 9 with its copy of Backup, then the store of 10, and then the store of 10 after a compaction.
	rootCopy, dirCopy := tree("copy")
	if err := runWorkload(t.Context(), dirCopy, 0, 9, none); err != nil {
		t.Fatal(err)
	}
	if _, err := backup(t.Context(), dirCopy, nil, filepath.Join(rootCopy, filepath.FromSlash(workloadBackup)), nil); err != nil {
		t.Fatalf("Backup() = %v", err)
	}
	copy9 := image("copy-9", rootCopy)
	if err := runWorkload(t.Context(), dirCopy, 9, 10, none); err != nil {
		t.Fatal(err)
	}
	copy10 := image("copy-10", rootCopy)
	rewriteEngine(t, dirCopy, logdb.Rewrite{})
	compacted := image("compacted", rootCopy)
	// A store of 10 whose last Update has another record.
	rootOther, other := tree("other")
	if err := runWorkload(t.Context(), other, 0, 9, none); err != nil {
		t.Fatal(err)
	}
	s := reopenLog(t, other, *workloadOptions().log)
	if _, err := s.PutOriginal(t.Context(), bytes.NewReader(workloadOriginal(10))); err != nil {
		t.Fatal(err)
	}
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Put(content.RefMedia, workloadMedia(10), mediaJSON(workloadMedia(10), workloadOriginal(10)),
			userID); err != nil {
			return err
		}
		if err := putPair(tx, 10); err != nil {
			return err
		}
		return tx.Record(workloadRecord(100))
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	otherRecord := image("other", rootOther)
	// broken returns the image of a store of the workload after the Updates to n, closed, and then broken.
	broken := func(name string, n int, breakIt func(dir string) error) []byte {
		root, dir := tree(name)
		err := runWorkload(t.Context(), dir, 0, n, none)
		if err == nil {
			err = breakIt(dir)
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return image(name, root)
	}
	segment := filepath.Join("logdb", "log", "0000000000000001-0000000000000001.log")
	noHead := broken("no-head", 9, func(dir string) error { return os.Remove(filepath.Join(dir, "logdb", "head")) })
	noWriter := broken("no-writer", 9, func(dir string) error { return os.Chmod(filepath.Join(dir, segment), 0o400) })
	noOriginal := broken("no-original", 10, func(dir string) error {
		digits := strings.TrimPrefix(sum(workloadOriginal(10)), "sha256:")
		return os.Remove(filepath.Join(dir, "files", digits[:2], digits))
	})
	// with returns a function that makes f in the store of dir, opened as its writer, and closes it.
	with := func(f func(s *Store) error) func(dir string) error {
		return func(dir string) error {
			s, err := open(dir, workloadOptions())
			if err != nil {
				return err
			}
			return errors.Join(f(s), s.Close())
		}
	}
	noMedia := broken("no-media", 9, with(func(s *Store) error {
		return s.Update(t.Context(), func(tx *Tx) error {
			if err := putPair(tx, 10); err != nil {
				return err
			}
			return tx.Record(workloadRecord(10))
		})
	}))
	noIndex := broken("no-index", 10, with(func(s *Store) error {
		// Past the store, whose Update adds a record that the workload does not have.
		return logDB(t, s).Update(t.Context(), func(tx *logdb.Tx) error {
			return tx.Delete(tableSummaries, docKey(content.RefMedia, workloadMedia(10)))
		})
	}))
	noUpdate := broken("no-update", 9, func(dir string) error { return os.Chmod(filepath.Join(dir, "files"), 0o500) })
	// A new store.
	rootNew, dirNew := tree("created")
	if err := runWorkload(t.Context(), dirNew, 0, 0, none); err != nil {
		t.Fatal(err)
	}
	created := image("created", rootNew)
	// The store of 10 as its writer has it before Close: the checkpoint is that of the Update 9.
	rootOpen, dirOpen := tree("unclosed")
	if err := runWorkload(t.Context(), dirOpen, 0, 9, none); err != nil {
		t.Fatal(err)
	}
	unclosed := reopenLog(t, dirOpen, *workloadOptions().log)
	if err := workloadUpdate(t.Context(), unclosed, 10); err != nil {
		t.Fatal(err)
	}
	notClosed := image("unclosed", rootOpen)
	if err := unclosed.Close(); err != nil {
		t.Fatal(err)
	}
	otherShown := []string{`"1 activity store.create "`}
	for k := 1; k <= 9; k++ {
		otherShown = append(otherShown, fmt.Sprintf(`"%d activity store.write %d"`, k+1, k))
	}
	otherShown = append(otherShown, `"11 activity store.write 100"`)

	t.Run("rdump", func(t *testing.T) {
		t.Parallel()
		for _, n := range []int{9, 10} {
			img := filepath.Join(work, fmt.Sprintf("store-%d.img", n))
			if code, out, err := e2fsck(img); code != 0 || err != nil {
				t.Errorf("e2fsck -fy of the store of %d = %d, %v\n%s", n, code, err, out)
			}
			dump := t.TempDir()
			if err := rdump(img, "/"+workloadTop, dump); err != nil {
				t.Fatalf("rdump of the store of %d: %v", n, err)
			}
			if got := snapshot(t, filepath.Join(dump, workloadTop)); !maps.Equal(got, trees[n]) {
				t.Errorf("rdump of the store of %d has %d files and directories, mke2fs %d:\n%q\nwant\n%q", n,
					len(got), len(trees[n]), got, trees[n])
			}
			s, err := open(filepath.Join(dump, filepath.FromSlash(workloadData)), workloadOptions())
			if err != nil {
				t.Fatalf("open() of the store of %d from the image = %v", n, err)
			}
			if _, err := checkWorkload(t, s, n, n, true); err != nil {
				t.Errorf("the store of %d from the image: %v", n, err)
			}
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}
		if err := rdump(filepath.Join(work, "empty.img"), "/"+workloadTop, t.TempDir()); !errors.Is(err, errNotInImage) {
			t.Errorf("rdump of the directory of the workload of the empty file system = %v, want %v", err, errNotInImage)
		}
		zeros := filepath.Join(work, "zeros.img")
		if err := os.WriteFile(zeros, make([]byte, 1<<20), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := rdump(zeros, "/"+workloadTop, t.TempDir()); err == nil || errors.Is(err, errNotInImage) ||
			!strings.Contains(err.Error(), "Bad magic number in super-block") {
			t.Errorf("rdump of an image of zeros = %v, want the error of debugfs", err)
		}
	})

	// fua is a state of the device whose last write has FUA, which makes it a point.
	type fua []byte
	// recordingOf returns a recording of a log of steps: a state of the device (its image), "flush", or the text of a
	// mark; the end is the mark dm-log-writes-end.
	recordingOf := func(t *testing.T, steps ...any) string {
		l := newSyntheticLog()
		prev := make([]byte, len(empty))
		for _, step := range steps {
			switch step := step.(type) {
			case []byte:
				l.change(prev, step, 0)
				prev = step
			case fua:
				l.change(prev, step, dmlog.FlagFUA)
				prev = step
			case string:
				if step == "flush" {
					l.flush()
				} else {
					l.mark(step)
				}
			}
		}
		l.mark("dm-log-writes-end")
		rec := t.TempDir()
		l.write(t, rec, prev)
		return rec
	}
	// recording returns the recording of steps after the empty file system, a flush, the mark mkfs and the store of 9.
	recording := func(t *testing.T, steps ...any) string {
		return recordingOf(t, append([]any{empty, "flush", "mkfs", images[9]}, steps...)...)
	}

	t.Run("replay", func(t *testing.T) {
		t.Parallel()
		r, err := checkRecording(t, recording(t, "ack-9", fua(images[10]), "flush", "ack-10", "closed"), dmlogCheck{})
		if err != nil {
			t.Fatal(err)
		}
		// The points: mkfs (the store missing), ack-9 (9), the write with FUA and the flush (10), ack-10, closed and
		// the end (10).
		if r.points != 7 || r.checked != 7 || !maps.Equal(r.fsck, map[int]int{0: 7}) || r.mounted != 0 ||
			r.missing != 1 || r.unopened != 0 || r.early != 0 || r.acked != 4 || r.running != 2 || r.readers != 6 ||
			r.backups != 0 || !maps.Equal(r.generations, map[uint64]int{1: 6}) || r.end != "the same as data.img" ||
			r.failures(false) != nil {
			t.Errorf("checkRecording() = %v\n%q", r, r.failures(false))
		}
		t.Log(r)
	})
	t.Run("replay before an ack", func(t *testing.T) {
		t.Parallel()
		r, err := checkRecording(t, recordingOf(t, empty, "flush", "mkfs", noHead, "flush", created, "flush", "ack-0"),
			dmlogCheck{})
		if err != nil {
			t.Fatal(err)
		}
		// The states: mkfs (the store missing); a flush (a store that does not open), and no reader looks; a flush
		// (the new store); ack-0 and the end (the new store, which a reader sees first).
		if r.points != 5 || r.checked != 5 || r.missing != 1 || r.unopened != 1 || r.early != 1 || r.acked != 2 ||
			r.running != 0 || r.readers != 2 || r.failures(false) != nil {
			t.Errorf("checkRecording() = %v\n%q", r, r.failures(false))
		}
		t.Log(r)
	})
	t.Run("replay of the phases", func(t *testing.T) {
		t.Parallel()
		r, err := checkRecording(t, recording(t, "ack-9", fua(copy9), "flush", "backup-9", fua(copy10), "flush",
			"ack-10", "closed", "open", "seen-10", "closed", "maintain", fua(compacted), "flush", "generation-2",
			"closed"), dmlogCheck{})
		if err != nil {
			t.Fatal(err)
		}
		// The points: mkfs (the store missing); ack-9, the write with FUA and the flush, and backup-9 (9); the write
		// with FUA and the flush (10, after ack-9); ack-10, closed, open, seen-10, closed and maintain (10, of the
		// generation 1); the write with FUA, the flush, generation-2, closed and the end (10, of the generation 2).
		// The copy of Backup is there from the mark backup-9 on.
		if r.points != 18 || r.checked != 18 || !maps.Equal(r.fsck, map[int]int{0: 18}) || r.missing != 1 ||
			r.unopened != 0 || r.early != 0 || r.acked != 15 || r.running != 2 || r.readers != 17 || r.backups != 14 ||
			!maps.Equal(r.generations, map[uint64]int{1: 12, 2: 5}) || r.end != "the same as data.img" ||
			r.failures(false) != nil {
			t.Errorf("checkRecording() = %v\n%q", r, r.failures(false))
		}
		t.Log(r)
	})

	damaged := slices.Clone(images[9])
	clear(damaged[1024+56 : 1024+58]) // the magic of the super block
	const reader = "a reader: "
	// also is what the failure of a case has too; or after "!", what it has not.
	also := map[string]string{
		"closed early":                        reader + `the pair of 10, want one from 9 to 9`,
		"no copy":                             reader + `openLogReader() = `,
		"a store that no process opens":       reader + `openLogReader() = `,
		"a store that its writer cannot open": "!" + reader,
		"a missing original":                  reader + `the original of the Update 10 is missing`,
		"a store that takes no Update":        "!" + reader,

		"a commit after the checkpoint of Close": reader + `after Close, open has found `,
	}
	// The cases that tell a reader from a writer by what each opens: a reader is a client of the daemon, which opens the
	// engine as a writer, and has no engine of its own.
	pending := map[string]string{
		"a store that its writer cannot open":    "the daemon opens the engine as a writer, so a reader that opens where the writer cannot is no case",
		"a commit after the checkpoint of Close": "the reader is a client of the daemon, which has no open of its own to check after Close",
	}
	// As root, a process opens and writes what the modes do not allow.
	needsUser := map[string]bool{"a store that its writer cannot open": true, "a store that takes no Update": true}
	// The cases whose failure is not a problem of the store: the file system, or the device, is not read.
	unread := map[string]bool{"damaged file system": true, "other device": true}
	for _, c := range []struct {
		name   string
		steps  []any
		points int    // checked
		data   []byte // in data.img, if not the last state
		want   string // in the failure
	}{
		{"early ack", []any{"ack-9", "ack-10", images[10], "flush", "closed"}, 3, nil,
			`(mark "ack-10"), after ack-10: the pair of 9, want one from 10 to 11`},
		{"early seen", []any{"ack-9", "seen-10", images[10], "flush", "closed"}, 3, nil,
			`(mark "seen-10"), after ack-10: the pair of 9, want one from 10 to 11`},
		{"late ack", []any{"ack-8", images[10], "flush", "ack-10", "closed"}, 3, nil,
			`(flush), after ack-8: the pair of 10, want one from 8 to 9`},
		{"no ack", []any{"flush", "ack-9", images[10], "flush", "ack-10", "closed"}, 2, nil,
			`(flush), before an ack: the pair of 9, want one from 0 to 0`},
		{"lost store", []any{"ack-9", empty, "flush", images[10], "flush", "ack-10", "closed"}, 3, nil,
			`(flush), after ack-9: /` + workloadData + `: not in the image`},
		{"closed early", []any{"ack-9", "closed", images[10], "flush"}, 4, nil,
			`(flush), after ack-9 and closed: the pair of 10, want one from 9 to 9`},
		{"a store that no process opens", []any{"ack-9", noHead, "flush"}, 3, nil,
			`(flush), after ack-9: open() = `},
		{"a store that its writer cannot open", []any{"ack-9", noWriter, "flush"}, 3, nil,
			`(flush), after ack-9: open() = `},
		{"a missing original", []any{"ack-9", noOriginal, "flush"}, 3, nil,
			`(flush), after ack-9: the original of the Update 10 is missing`},
		{"a missing media document", []any{"ack-9", noMedia, "flush"}, 3, nil,
			`(flush), after ack-9: 0 media documents with the pair of 10`},
		{"an index of other documents", []any{"ack-9", noIndex, "flush"}, 3, nil,
			`(flush), after ack-9: the index is not the one that the documents give: `},
		{"a commit after the checkpoint of Close", []any{"ack-9", notClosed, "flush", "ack-10", "closed"}, 5, nil,
			`(mark "closed"), after ack-10 and closed: after Close, open has found `},
		{"a store that takes no Update", []any{"ack-9", noUpdate, "flush"}, 3, nil,
			`(flush), after ack-9: PutOriginal() before the Update 10: `},
		{"no copy", []any{"ack-9", "backup-9", images[10], "flush", "ack-10", "closed"}, 3, nil,
			`(mark "backup-9"), after ack-9: the copy of Backup: open() = `},
		{"the copy of another Update", []any{"ack-9", fua(copy10), "flush", "ack-10", "backup-10"}, 6, nil,
			`(mark "backup-10"), after ack-10: the copy of Backup: the pair of 9, want one from 10 to 10`},
		{"an old generation", []any{"ack-9", "maintain", "generation-2", images[10], "flush"}, 4, nil,
			`(mark "generation-2"), after ack-9: the generation 1, want 2`},
		{"a generation that comes back", []any{"ack-9", fua(copy10), "flush", "ack-10", "closed", "maintain",
			fua(compacted), "flush", "generation-2", "closed", "maintain", fua(copy10), "flush"}, 13, nil,
			`, fua), after ack-10, in the maintenance: the generation 1, want 2 or a later one`},
		{"damaged file system", []any{"ack-9", damaged, "flush", images[10], "flush", "ack-10", "closed"}, 3, nil,
			"e2fsck -fy has exited with 8:"},
		{"other record", []any{"ack-9", otherRecord, "flush", images[10], "flush", "ack-10", "closed"}, 3, nil,
			"(flush), after ack-9: the audit records [" + strings.Join(otherShown, " ") + "], want"},
		{"other device", []any{"ack-9", images[10], "flush", "ack-10", "closed"}, 1, images[9],
			"the device after the log: it differs from "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if reason, ok := pending[c.name]; ok {
				pendingDaemon(t, reason)
			}
			t.Parallel()
			rec := recording(t, c.steps...)
			if c.data != nil {
				if err := os.WriteFile(filepath.Join(rec, "data.img"), c.data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if needsUser[c.name] && os.Geteuid() == 0 {
				t.Skip("root opens and writes everything")
			}
			r, err := checkRecording(t, rec, dmlogCheck{limit: c.points})
			if err != nil {
				t.Fatal(err)
			}
			failures := r.failures(false)
			if r.checked != c.points || len(failures) != 1 || !strings.Contains(failures[0], c.want) {
				t.Fatalf("checkRecording() = %v\n%q", r, failures)
			}
			if got := len(r.unread) == 1 && r.problems == nil; got != unread[c.name] {
				t.Errorf("checkRecording() has %d problems of the store and %d that it has not read: %q", len(r.problems),
					len(r.unread), failures)
			}
			if text, not := strings.CutPrefix(also[c.name], "!"); not == strings.Contains(failures[0], text) {
				t.Errorf("checkRecording() = %v\n%q\nwant it with %q: %v", r, failures, text, !not)
			}
			// A recording that must have a problem of the store passes with one, and only with one.
			if got := r.failures(true) == nil; got == unread[c.name] {
				t.Errorf("the check of a recording that must have a problem fails with %q", r.failures(true))
			}
		})
	}
	t.Run("acks out of order", func(t *testing.T) {
		t.Parallel()
		if _, err := checkRecording(t, recording(t, "ack-9", "ack-8", images[10], "flush"), dmlogCheck{limit: 1}); err == nil ||
			!strings.HasSuffix(err.Error(), `: the mark "ack-8" after ack-9`) {
			t.Errorf("checkRecording() = %v", err)
		}
	})
	t.Run("an order that the log does not tell", func(t *testing.T) {
		t.Parallel()
		// The blocks of the store of 10, the last one with FUA, and then again those of the store of 9.
		_, err := checkRecording(t, recording(t, "ack-9", fua(images[10]), images[9], "flush"), dmlogCheck{limit: 1})
		if err == nil || !strings.Contains(err.Error(), ": the log does not tell the order of the entries ") {
			t.Errorf("checkRecording() = %v", err)
		}
	})
}

// TestDMLogFailures checks what makes the check of a recording fail: a problem of the store, or with wantProblems,
// none; and in both cases, what could not be read.
func TestDMLogFailures(t *testing.T) {
	for _, c := range []struct {
		problems, unread []string
		want, wanted     []string // without and with wantProblems
	}{
		{nil, nil, nil, []string{"no problem of the store in a recording that must have one"}},
		{[]string{"lost"}, nil, []string{"lost"}, nil},
		{[]string{"lost", "torn"}, []string{"mount"}, []string{"mount", "lost", "torn"}, []string{"mount"}},
		{nil, []string{"mount", "device"}, []string{"mount", "device"},
			[]string{"mount", "device", "no problem of the store in a recording that must have one"}},
	} {
		r := &dmlogReport{problems: c.problems, unread: c.unread}
		if got := r.failures(false); !slices.Equal(got, c.want) {
			t.Errorf("failures(false) with %q and %q = %q, want %q", c.problems, c.unread, got, c.want)
		}
		if got := r.failures(true); !slices.Equal(got, c.wanted) {
			t.Errorf("failures(true) with %q and %q = %q, want %q", c.problems, c.unread, got, c.wanted)
		}
	}
}

// TestDMLogMarks checks what the marks of the workload tell of the states after them, and which orders of them
// dmlogPoint.see refuses.
func TestDMLogMarks(t *testing.T) {
	type state struct {
		acked, backup int
		generation    uint64
		closed        bool
		maintaining   bool
	}
	for _, c := range []struct {
		marks string // with spaces between them
		want  state
		bad   string // the error of the last mark, or ""
	}{
		{"", state{-1, -1, 1, false, false}, ""},
		{"mkfs ack-0 ack-1 ack-3", state{3, -1, 1, false, false}, ""},
		{"ack-2 seen-2 seen-3 backup-3 ack-4", state{4, 3, 1, false, false}, ""},
		{"ack-2 closed", state{2, -1, 1, true, false}, ""},
		{"ack-2 closed open seen-2 ack-3", state{3, -1, 1, false, false}, ""},
		{"ack-2 closed maintain", state{2, -1, 1, false, true}, ""},
		{"ack-2 closed maintain maintain generation-3 closed", state{2, -1, 3, true, false}, ""},
		{"ack-2 maintain generation-1 maintain generation-1 dm-log-writes-end", state{2, -1, 1, false, false}, ""},
		{"ack-2 ack-2", state{}, `the mark "ack-2" after ack-2`},
		{"ack-2 ack-1", state{}, `the mark "ack-1" after ack-2`},
		{"ack-2 seen-1", state{}, `the mark "seen-1" after ack-2`},
		{"ack-x", state{}, `the mark "ack-x" before an ack`},
		{"ack-2 closed ack-3", state{}, `the mark "ack-3" after ack-2 and closed`},
		{"ack-2 closed seen-2", state{}, `the mark "seen-2" after ack-2 and closed`},
		{"ack-2 backup-1", state{}, `the mark "backup-1" after ack-2`},
		{"ack-2 backup-2 ack-3 backup-3", state{}, `the mark "backup-3" after ack-3`},
		{"ack-2 closed backup-2", state{}, `the mark "backup-2" after ack-2 and closed`},
		{"ack-2 generation-2", state{}, `the mark "generation-2" after ack-2`},
		{"ack-2 maintain generation-0", state{}, `the mark "generation-0" after ack-2, in the maintenance`},
		{"ack-2 maintain generation-x", state{}, `the mark "generation-x" after ack-2, in the maintenance`},
		{"ack-2 maintain ack-3", state{}, `the mark "ack-3" after ack-2, in the maintenance`},
		{"ack-2 maintain backup-2", state{}, `the mark "backup-2" after ack-2, in the maintenance`},
		{"ack-2 maintain closed", state{}, `the mark "closed" after ack-2, in the maintenance`},
		{"ack-2 maintain open", state{}, `the mark "open" after ack-2, in the maintenance`},
	} {
		p := newDMLogPoint()
		var err error
		for _, mark := range strings.Fields(c.marks) {
			if err != nil {
				t.Errorf("%q: see() = %v before the last mark", c.marks, err)
			}
			err = p.see(dmlog.Entry{Flags: dmlog.FlagMark, Mark: mark})
		}
		got := state{p.acked, p.backup, p.generation, p.closed, p.maintaining}
		switch {
		case c.bad != "":
			if err == nil || err.Error() != c.bad {
				t.Errorf("%q: see() = %v, want %q", c.marks, err, c.bad)
			}
		case err != nil || got != c.want:
			t.Errorf("%q: see() = %v, and the state %+v; want %+v", c.marks, err, got, c.want)
		}
	}
	// An entry that is not a mark tells nothing.
	p := newDMLogPoint()
	if err := p.see(dmlog.Entry{Mark: "ack-x"}); err != nil || p.acked != -1 {
		t.Errorf("see() of a write = %v, and ack-%d", err, p.acked)
	}
}

// TestDMLogStates checks which entries of a log are the states of a check: the points; or with every, each entry
// that changes the device or is a mark.
func TestDMLogStates(t *testing.T) {
	l := &dmlog.Log{Super: dmlog.Super{SectorSize: 512}, Entries: []dmlog.Entry{
		{Sector: 8, Sectors: 8, Data: 1024},                         // 0: a write
		{Flags: dmlog.FlagFlush, Data: -1},                          // 1: a flush without data
		{Flags: dmlog.FlagMark, Mark: "mkfs", Data: -1},             // 2
		{Sector: 16, Sectors: 8, Data: 8192},                        // 3
		{Sector: 24, Sectors: 8, Flags: dmlog.FlagFUA, Data: 16384}, // 4: a write with FUA
		{Sector: 32, Sectors: 8, Flags: dmlog.FlagDiscard, Data: -1},
		{Sector: 40, Sectors: 8, Flags: dmlog.FlagFlush | dmlog.FlagFUA, Data: 24576}, // 6: a flush with data
		{Flags: dmlog.FlagFlush | dmlog.FlagFUA, Data: -1},                            // 7
		{Flags: dmlog.FlagMark, Mark: "dm-log-writes-end", Data: -1},
	}}
	if got, want := dmlogStates(l, false), []int{1, 2, 4, 6, 7, 8}; !slices.Equal(got, want) {
		t.Errorf("the points are the entries %v, want %v", got, want)
	}
	if got, want := dmlogStates(l, true), []int{0, 2, 3, 4, 5, 6, 8}; !slices.Equal(got, want) {
		t.Errorf("the states of every entry are %v, want %v", got, want)
	}
}

func TestExtents(t *testing.T) {
	var x extents
	for _, c := range []struct {
		off, n int64
		want   extents
	}{
		{10, 10, extents{{10, 20}}},
		{30, 5, extents{{10, 20}, {30, 35}}},
		{0, 0, extents{{10, 20}, {30, 35}}},
		{20, 2, extents{{10, 22}, {30, 35}}}, // touching the end of one
		{28, 2, extents{{10, 22}, {28, 35}}}, // touching the start of one
		{40, 1, extents{{10, 22}, {28, 35}, {40, 41}}},
		{0, 5, extents{{0, 5}, {10, 22}, {28, 35}, {40, 41}}},
		{21, 20, extents{{0, 5}, {10, 41}}}, // over three
		{12, 3, extents{{0, 5}, {10, 41}}},  // inside one
		{5, 5, extents{{0, 41}}},
	} {
		if x = x.add(c.off, c.n); !slices.Equal(x, c.want) {
			t.Fatalf("add(%d, %d) = %v, want %v", c.off, c.n, x, c.want)
		}
	}
}
