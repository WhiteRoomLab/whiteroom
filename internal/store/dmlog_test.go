package store

import (
	"bufio"
	"bytes"
	"context"
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
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/dmlog"
)

// The check of the store on ext4 with dm-log-writes of Linux (3.5; internal/logdb/dmlog): record.sh runs
// TestDMLogWorkload on ext4 through the target, as root, and TestDMLogCheck replays the log of the recording without
// root. TestExt4Extraction checks the way of TestDMLogCheck from an image of ext4 to a store, with images that mke2fs
// makes of stores of the workload.

const (
	envDMLogWorkload = "WHITEROOM_DMLOG_WORKLOAD" // the data directory of the workload
	envDMLog         = "WHITEROOM_DMLOG"          // the directory of a recording
	envDMLogPoints   = "WHITEROOM_DMLOG_POINTS"   // how many points TestDMLogCheck checks
)

// dmlogUpdates is the number of Updates of the workload of the recording.
const dmlogUpdates = 200

// workloadOptions are those of the store of the workload: the media documents in files and the terms inline, and
// segments and checkpoints small enough that the recording has rollovers and checkpoints.
func workloadOptions() options {
	return options{
		log: &logdb.Options{Placement: logdb.Hybrid, Threshold: 100, SegmentBytes: 16 << 10, CheckpointBytes: 32 << 10,
			SyncCheckpoints: true},
		now: func() time.Time { return testTime },
	}
}

// workloadOriginal is the original that the workload puts before the Update n, a multiple of 10, which puts the media
// document workloadMedia(n) that names it.
func workloadOriginal(n int) []byte {
	return bytes.Repeat(fmt.Appendf(nil, "the original of the Update %d of the workload ", n), 50)
}

func workloadMedia(n int) string {
	return fmt.Sprintf("med_01JD%022d", n)
}

// workloadUpdate makes the Update n of the workload in s: the pair n and the record store.write of the pair n, and for
// a multiple of 10, the media document of workloadOriginal(n), which it puts before.
func workloadUpdate(ctx context.Context, s *Store, n int) error {
	var original []byte
	if n%10 == 0 {
		original = workloadOriginal(n)
		want := Original{Hash: sum(original), Size: int64(len(original))}
		switch o, err := s.PutOriginal(ctx, bytes.NewReader(original)); {
		case err != nil:
			return fmt.Errorf("PutOriginal() before the Update %d: %w", n, err)
		case o != want:
			return fmt.Errorf("PutOriginal() before the Update %d = %+v, want %+v", n, o, want)
		}
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := putPair(tx, n); err != nil {
			return err
		}
		if original != nil {
			id := workloadMedia(n)
			if _, err := tx.Put(content.RefMedia, id, mediaJSON(id, original), userID); err != nil {
				return err
			}
		}
		return tx.Record(workloadRecord(n))
	}); err != nil {
		return fmt.Errorf("Update() %d: %w", n, err)
	}
	return nil
}

// workloadRecord is the record of the Update n of the workload.
func workloadRecord(n int) audit.Record {
	return audit.Record{Log: audit.ActivityLog, Actor: userID, Action: audit.StoreWrite,
		Detail: map[string]string{"pair": strconv.Itoa(n)}}
}

// runWorkload makes the Updates from+1 to to of the workload in the store of the data directory dir, which it creates
// if from is 0, and closes it. It calls mark with "ack 0" after create, "ack <n>" after the Update n, and "closed"
// after Close.
func runWorkload(ctx context.Context, dir string, from, to int, mark func(text string) error) error {
	var s *Store
	var err error
	if from == 0 {
		if s, err = create(dir, workloadOptions()); err == nil {
			err = mark("ack 0")
		}
	} else {
		s, err = open(dir, workloadOptions())
	}
	if s == nil {
		return err
	}
	for n := from + 1; err == nil && n <= to; n++ {
		if err = workloadUpdate(ctx, s, n); err == nil {
			err = mark(fmt.Sprintf("ack %d", n))
		}
	}
	if err := errors.Join(err, s.Close()); err != nil {
		return err
	}
	return mark("closed")
}

// TestDMLogWorkload is the workload of the recording, which record.sh runs on ext4 through dm-log-writes with
// envDMLogWorkload set to its data directory: dmlogUpdates Updates of runWorkload. For each mark, it prints
// "dmlog: <text>", and waits for the reply "go" on stdin, which record.sh writes after it has set the mark of the
// words of the text joined by "-" (ack-<n>, closed): each mark is in the log before the next Update starts.
func TestDMLogWorkload(t *testing.T) {
	dir := os.Getenv(envDMLogWorkload)
	if dir == "" {
		t.Skip("the workload of internal/logdb/dmlog/record.sh, which sets " + envDMLogWorkload)
	}
	replies := bufio.NewReader(os.Stdin)
	if err := runWorkload(t.Context(), dir, 0, dmlogUpdates, func(text string) error {
		fmt.Println("dmlog: " + text)
		switch reply, err := replies.ReadString('\n'); {
		case err != nil:
			return fmt.Errorf("the reply of record.sh to %q: %w", text, err)
		case reply != "go\n":
			return fmt.Errorf("record.sh has replied %q to %q", reply, text)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDMLogCheck checks the recording of record.sh in the directory envDMLog with checkRecording, at every point after
// the mark mkfs, or at the first envDMLogPoints of them. It needs e2fsck and debugfs, and no root.
func TestDMLogCheck(t *testing.T) {
	dir := os.Getenv(envDMLog)
	if dir == "" {
		t.Skip("checks a recording of internal/logdb/dmlog/record.sh in the directory " + envDMLog)
	}
	limit := 0
	if v := os.Getenv(envDMLogPoints); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("%s=%s: want a number of points from 1", envDMLogPoints, v)
		}
		limit = n
	}
	if missing := missingCommands("e2fsck", "debugfs"); missing != nil {
		t.Fatalf("needs %s of e2fsprogs", strings.Join(missing, " and "))
	}
	r, err := checkRecording(t, dir, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(r)
	for _, out := range r.fixed {
		t.Logf("e2fsck -fy has fixed the file system at %s", out)
	}
	for _, p := range r.problems {
		t.Error(p)
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

// dmlogPoint is a point of a log of the workload, with the marks of the workload before it.
type dmlogPoint struct {
	index  int // of the entry
	entry  dmlog.Entry
	acked  int  // n of the last mark ack-<n>, or -1
	closed bool // after the mark closed
}

// see takes e, an entry of the log up to the point, if it is a mark of the workload.
func (p *dmlogPoint) see(e dmlog.Entry) error {
	if e.Flags&dmlog.FlagMark == 0 {
		return nil
	}
	if v, ok := strings.CutPrefix(e.Mark, "ack-"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= p.acked || p.closed {
			return fmt.Errorf("the mark %q after ack-%d", e.Mark, p.acked)
		}
		p.acked = n
	}
	if e.Mark == "closed" {
		p.closed = true
	}
	return nil
}

func (p *dmlogPoint) String() string {
	after := "before an ack"
	if p.acked >= 0 {
		after = fmt.Sprintf("after ack-%d", p.acked)
	}
	if p.closed {
		after += " and closed"
	}
	return fmt.Sprintf("entry %d (%s), %s", p.index, p.entry.String(), after)
}

// dmlogReport is what checkRecording has found.
type dmlogReport struct {
	entries, points, checked int
	fsck                     map[int]int // the points by the exit code of e2fsck -fy
	fixed                    []string    // the points of the first outputs of e2fsck -fy with the code 1, with them
	missing, unopened, early int         // the points before an ack: without the store, where it did not open, others
	acked, running           int         // the points after one with the pair of the last ack, and of the next Update
	recovered                tally
	end                      string // how the device after the log compares with data.img
	problems                 []string
}

func (r *dmlogReport) String() string {
	var codes []string
	for _, code := range slices.Sorted(maps.Keys(r.fsck)) {
		codes = append(codes, fmt.Sprintf("%d at %d", code, r.fsck[code]))
	}
	return fmt.Sprintf("%d entries, %d points after the mark mkfs, %d checked: e2fsck -fy exited with %s; "+
		"before an ack, the store was missing at %d, did not open at %d and opened at %d; after one, it had the pair "+
		"of the last ack at %d and that of the Update after it at %d; recovered: %v; the device after the log: %s; "+
		"%d problems", r.entries, r.points, r.checked, strings.Join(codes, ", "), r.missing, r.unopened, r.early,
		r.acked, r.running, r.recovered, r.end, len(r.problems))
}

// checkRecording replays the recording in dir to each point after the mark mkfs, or to the first limit of them if
// limit is not 0, and checks the device at each with checkPoint. The recording is the files that record.sh makes:
// log.img, the log; data-size, the size of the device in bytes; and data.img, the device at the end, which the device
// after the whole log must equal, if it is there. checkRecording returns an error if it cannot read the recording, or
// it is not one of the workload, and the problems of the points in the report.
func checkRecording(t *testing.T, dir string, limit int) (*dmlogReport, error) {
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

	work := t.TempDir()
	dev, err := os.Create(filepath.Join(work, "device.img"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = dev.Close() }()
	if err := dev.Truncate(size); err != nil {
		return nil, err
	}
	r := &dmlogReport{entries: len(l.Entries), fsck: map[int]int{}, recovered: tally{}}
	var written extents
	p := &dmlogPoint{acked: -1}
	from := 0
	for _, i := range l.Points() {
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
		if limit > 0 && r.checked == limit {
			continue
		}
		r.checked++
		p.index, p.entry = i, l.Entries[i]
		if err := r.checkPoint(t, work, dev, size, written, p); err != nil {
			r.problems = append(r.problems, fmt.Sprintf("%s: %v", p.String(), err))
		}
	}
	switch end, err := compareDevice(dev, filepath.Join(dir, "data.img"), size); {
	case err != nil:
		r.problems = append(r.problems, fmt.Sprintf("the device after the log: %v", err))
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

// checkPoint checks the device at the point p, dev of size bytes, of which a replay has written the bytes written:
// e2fsck -fy fixes a copy of it, and exits with 0 or 1; debugfs copies /store out of it; and the store opens and passes
// checkWorkload with the pair n of the last ack or n+1, or after the mark closed n. Before an ack, the store may be
// missing, or not open, and if it opens, it has no pair. After the next Update of the workload and Close, the store
// opens with it.
func (r *dmlogReport) checkPoint(t *testing.T, work string, dev io.ReaderAt, size int64, written extents,
	p *dmlogPoint) error {
	t.Helper()
	img := filepath.Join(work, "fsck.img")
	if err := copyExtents(img, dev, size, written); err != nil {
		return err
	}
	code, out, err := e2fsck(img)
	if err != nil {
		return fmt.Errorf("e2fsck: %w\n%s", err, out)
	}
	r.fsck[code]++
	switch {
	case code == 1 && len(r.fixed) < 3:
		r.fixed = append(r.fixed, p.String()+":\n"+out)
	case code > 1:
		return fmt.Errorf("e2fsck -fy has exited with %d:\n%s", code, out)
	}
	dump := filepath.Join(work, "dump")
	if err := os.RemoveAll(dump); err != nil {
		return err
	}
	if err := os.Mkdir(dump, 0o700); err != nil {
		return err
	}
	switch err := rdump(img, "/store", dump); {
	case errors.Is(err, errNotInImage) && p.acked < 0:
		r.missing++
		return nil
	case err != nil:
		return err
	}
	dir := filepath.Join(dump, "store")
	s, err := open(dir, workloadOptions())
	if err != nil {
		if p.acked < 0 {
			r.unopened++
			return nil
		}
		return fmt.Errorf("open() = %w", err)
	}
	r.recovered.add(logDB(t, s).Recovery())
	lo, hi := p.acked, p.acked+1
	switch {
	case p.acked < 0:
		lo, hi = 0, 0
	case p.closed:
		hi = lo
	}
	n, err := checkWorkload(t, s, lo, hi, p.closed)
	if err == nil {
		err = workloadUpdate(t.Context(), s, n+1)
	}
	if err := errors.Join(err, s.Close()); err != nil {
		return err
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
		return fmt.Errorf("open() after the Update %d = %w", n+1, err)
	}
	if _, err = checkWorkload(t, again, n+1, n+1, true); err != nil {
		err = fmt.Errorf("after the Update %d: %w", n+1, err)
	}
	return errors.Join(err, again.Close())
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
// mke2fs makes of an empty file system and of the store of the workload after 9 Updates and after 10, closed: e2fsck
// -fy finds them clean; debugfs copies out of them the files of the store as they were, and the store opens with them
// (or the store is missing from the empty one, and debugfs fails on an image of zeros). checkRecording finds no
// problem in a recording of a log made from the images, with writes of the blocks that change (the last of the store
// of 10 with FUA), discards of those that become zeros, flushes and the marks of the workload, and finds the problem
// in recordings of other logs: the mark ack-10 before the Update 10; the store with two Updates after the last ack,
// with one before an ack, with one after the mark closed, lost after an ack, or with another audit record; a damaged
// file system; another data.img; and acks out of order.
func TestExt4Extraction(t *testing.T) {
	if missing := missingCommands("mke2fs", "e2fsck", "debugfs"); missing != nil {
		t.Skipf("needs %s of e2fsprogs, which the dev shell has on Linux", strings.Join(missing, ", "))
	}
	t.Parallel() // with the matrices, after the tests that do not run in parallel
	work := t.TempDir()
	dir := filepath.Join(work, "src", "store")
	none := func(string) error { return nil }
	empty := mke2fs(t, filepath.Join(work, "empty.img"), "")
	images := map[int][]byte{}
	trees := map[int]map[string]string{}
	from := 0
	for _, n := range []int{9, 10} {
		if err := runWorkload(t.Context(), dir, from, n, none); err != nil {
			t.Fatalf("the workload to %d: %v", n, err)
		}
		images[n] = mke2fs(t, filepath.Join(work, fmt.Sprintf("store-%d.img", n)), filepath.Dir(dir))
		trees[n] = snapshot(t, dir)
		from = n
	}
	// A store of 10 whose last Update has another record.
	other := filepath.Join(work, "other", "store")
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
	otherRecord := mke2fs(t, filepath.Join(work, "other.img"), filepath.Dir(other))
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
			if err := rdump(img, "/store", dump); err != nil {
				t.Fatalf("rdump of the store of %d: %v", n, err)
			}
			if got := snapshot(t, filepath.Join(dump, "store")); !maps.Equal(got, trees[n]) {
				t.Errorf("rdump of the store of %d has %d files and directories, mke2fs %d:\n%q\nwant\n%q", n,
					len(got), len(trees[n]), got, trees[n])
			}
			s, err := open(filepath.Join(dump, "store"), workloadOptions())
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
		if err := rdump(filepath.Join(work, "empty.img"), "/store", t.TempDir()); !errors.Is(err, errNotInImage) {
			t.Errorf("rdump of /store of the empty file system = %v, want %v", err, errNotInImage)
		}
		zeros := filepath.Join(work, "zeros.img")
		if err := os.WriteFile(zeros, make([]byte, 1<<20), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := rdump(zeros, "/store", t.TempDir()); err == nil || errors.Is(err, errNotInImage) ||
			!strings.Contains(err.Error(), "Bad magic number in super-block") {
			t.Errorf("rdump of an image of zeros = %v, want the error of debugfs", err)
		}
	})

	// fua is a state of the device whose last write has FUA, which makes it a point.
	type fua []byte
	// recording returns a recording of a log of steps after the empty file system, a flush, the mark mkfs and the store
	// of 9: a state of the device (its image), "flush", or the text of a mark; the end is the mark dm-log-writes-end.
	recording := func(t *testing.T, steps ...any) string {
		l := newSyntheticLog()
		prev := make([]byte, len(empty))
		for _, step := range append([]any{empty, "flush", "mkfs", images[9]}, steps...) {
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

	t.Run("replay", func(t *testing.T) {
		t.Parallel()
		r, err := checkRecording(t, recording(t, "ack-9", fua(images[10]), "flush", "ack-10", "closed"), 0)
		if err != nil {
			t.Fatal(err)
		}
		// The points: mkfs (the store missing), ack-9 (9), the write with FUA and the flush (10), ack-10, closed and
		// the end (10).
		if r.points != 7 || r.checked != 7 || !maps.Equal(r.fsck, map[int]int{0: 7}) || r.missing != 1 ||
			r.unopened != 0 || r.early != 0 || r.acked != 4 || r.running != 2 || r.end != "the same as data.img" ||
			r.problems != nil {
			t.Errorf("checkRecording() = %v\n%q", r, r.problems)
		}
		t.Log(r)
	})

	damaged := slices.Clone(images[9])
	clear(damaged[1024+56 : 1024+58]) // the magic of the super block
	for _, c := range []struct {
		name   string
		steps  []any
		points int    // checked
		data   []byte // in data.img, if not the last state
		want   string // the end of the problem
	}{
		{"early ack", []any{"ack-9", "ack-10", images[10], "flush", "closed"}, 3, nil,
			`(mark "ack-10"), after ack-10: the pair of 9, want one from 10 to 11`},
		{"late ack", []any{"ack-8", images[10], "flush", "ack-10", "closed"}, 3, nil,
			`(flush), after ack-8: the pair of 10, want one from 8 to 9`},
		{"no ack", []any{"flush", "ack-9", images[10], "flush", "ack-10", "closed"}, 2, nil,
			`(flush), before an ack: the pair of 9, want one from 0 to 0`},
		{"lost store", []any{"ack-9", empty, "flush", images[10], "flush", "ack-10", "closed"}, 3, nil,
			`(flush), after ack-9: /store: not in the image`},
		{"closed early", []any{"ack-9", "closed", images[10], "flush"}, 4, nil,
			`(flush), after ack-9 and closed: the pair of 10, want one from 9 to 9`},
		{"damaged file system", []any{"ack-9", damaged, "flush", images[10], "flush", "ack-10", "closed"}, 3, nil,
			"e2fsck -fy has exited with 8:"},
		{"other record", []any{"ack-9", otherRecord, "flush", images[10], "flush", "ack-10", "closed"}, 3, nil,
			"(flush), after ack-9: the audit records [" + strings.Join(otherShown, " ") + "], want"},
		{"other device", []any{"ack-9", images[10], "flush", "ack-10", "closed"}, 1, images[9],
			"the device after the log: it differs from "},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rec := recording(t, c.steps...)
			if c.data != nil {
				if err := os.WriteFile(filepath.Join(rec, "data.img"), c.data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r, err := checkRecording(t, rec, c.points)
			if err != nil {
				t.Fatal(err)
			}
			if r.checked != c.points || len(r.problems) != 1 || !strings.Contains(r.problems[0], c.want) {
				t.Errorf("checkRecording() = %v\n%q", r, r.problems)
			}
		})
	}
	t.Run("acks out of order", func(t *testing.T) {
		t.Parallel()
		if _, err := checkRecording(t, recording(t, "ack-9", "ack-8", images[10], "flush"), 1); err == nil ||
			!strings.HasSuffix(err.Error(), `: the mark "ack-8" after ack-9`) {
			t.Errorf("checkRecording() = %v", err)
		}
	})
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
