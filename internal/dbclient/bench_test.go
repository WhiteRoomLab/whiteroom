package dbclient_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

const envBench = "WHITEROOM_DAEMON_BENCH" // the directory of the results

const (
	benchTable            uint32 = 2 // of the rows that the measurements put
	benchEcho                    = 16
	benchEchoes                  = 100_000
	benchGets                    = 100_000
	benchGetValue                = 200
	benchUpdates                 = 3000
	benchDefaultRun              = 300 // Updates under the default limit of commits
	benchBurst                   = 50  // of that limit
	benchRow                     = 100 // bytes of the row of an Update
	benchEvent                   = 400 // bytes of its event
	benchScanRows                = 20_000
	benchScanValue               = 1 << 10
	benchScanRuns                = 20
	benchWindowKeys              = 256 // a call of GetMany, and of PrefetchObjects
	benchWindowObjects           = 1024
	benchWindowObjectSize        = 4 << 10
	benchWindowPasses            = 5 // Views of the objects
	benchOriginals               = 3
	benchMaintenance             = 3 // runs of Backup and of Verify
	benchRestarts                = 3
	benchProbeEvery              = 10 * time.Millisecond
	benchProbeBefore             = 20 // Updates of the probe before a Backup or a Verify starts
	mib                          = 1 << 20
	benchOriginalSize            = 64 * mib
)

// benchSeries is a measurement: its name, the unit of its values, and every value.
type benchSeries struct {
	Name   string
	Unit   string
	Values []float64
}

// benchStats are the numbers that results.md gives of the values of a series.
type benchStats struct {
	N                       int
	Sum, Min, P50, P99, Max float64
}

func benchSummary(values []float64) benchStats {
	s := slices.Sorted(slices.Values(values))
	n := len(s)
	st := benchStats{N: n, Min: s[0], Max: s[n-1], P50: s[n/2], P99: s[(99*n+99)/100-1]}
	if n%2 == 0 {
		st.P50 = (s[n/2-1] + s[n/2]) / 2
	}
	for _, v := range s {
		st.Sum += v
	}
	return st
}

func benchMs(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }
func benchUs(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e3 }

// benchNum formats v with 3 significant digits, and 0 as 0.
func benchNum(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', max(0, 2-int(math.Floor(math.Log10(math.Abs(v))))), 64)
}

// benchFirst returns the first line of the file name if key is "", else the value of the line of key; "" if there is none.
func benchFirst(name, key string) string {
	data, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(data)) {
		if key == "" {
			return strings.TrimSpace(line)
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// benchStatus returns a line of /proc/self/status that is a size, in bytes (VmRSS, VmHWM); 0 if there is none.
func benchStatus(key string) uint64 {
	kb, err := strconv.ParseUint(strings.TrimSuffix(benchFirst("/proc/self/status", key), " kB"), 10, 64)
	if err != nil {
		return 0
	}
	return kb << 10
}

// benchResetPeak makes VmHWM the resident set of now, and reports whether the system let it.
func benchResetPeak() bool { return os.WriteFile("/proc/self/clear_refs", []byte("5"), 0) == nil }

// benchLoad returns the load averages of the machine of the last 1, 5 and 15 minutes; "unknown" if there are none.
func benchLoad() string {
	f := strings.Fields(benchFirst("/proc/loadavg", ""))
	if len(f) < 3 {
		return "unknown"
	}
	return strings.Join(f[:3], " ")
}

// benchMount returns the type and the device of the file system that has dir, a path without symbolic links.
func benchMount(dir string) string {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return ""
	}
	point, found := "", ""
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if inside := f[1] == "/" || dir == f[1] || strings.HasPrefix(dir, f[1]+"/"); inside && len(f[1]) >= len(point) {
			point, found = f[1], f[2]+" on "+f[0]
		}
	}
	return found
}

// benchSize returns the bytes of the regular files in dir.
func benchSize(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		n += info.Size()
		return nil
	})
	must(t, err)
	return n
}

// benchSocketDir makes a directory for the sockets of a daemon, which has a path short enough for them.
func benchSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wr")
	must(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if n := len(filepath.Join(dir, dbwire.WriterSocket)); n > 100 {
		t.Fatalf("the path of a socket would have %d bytes: %s (TMPDIR should be a short path)", n, dir)
	}
	return dir
}

// benchDaemon is a daemon on a data directory on the OS, in this process, which listens on socket files, with a writer
// and a reader client of it. limits nil: those of dbtest, with no limit of commits per second.
type benchDaemon struct {
	t       *testing.T
	data    string
	sockets string
	limits  *dbwire.Limits
	h       *dbtest.Daemon
	w, r    *dbclient.Client
}

func newBenchDaemon(t *testing.T, data string, limits *dbwire.Limits) *benchDaemon {
	t.Helper()
	d := &benchDaemon{t: t, data: data, sockets: benchSocketDir(t), limits: limits}
	must(t, dbtest.Init(data, dbtest.Options{Limits: limits}))
	return d
}

// benchStart is how long the parts of starting a daemon took, from opening its engine to the first read of a client.
type benchStart struct{ acquire, listen, openWriter, firstRead time.Duration }

func (s benchStart) total() time.Duration { return s.acquire + s.listen + s.openWriter + s.firstRead }

// start opens the engine, makes the sockets, connects the clients, and reads a row with the reader.
func (d *benchDaemon) start() benchStart {
	t := d.t
	t.Helper()
	var s benchStart
	at := time.Now()
	h, err := dbtest.Acquire(d.data, dbtest.Options{Limits: d.limits})
	must(t, err)
	s.acquire, d.h = time.Since(at), h
	at = time.Now()
	must(t, h.Listen(d.sockets))
	s.listen = time.Since(at)
	at = time.Now()
	d.w, err = dbclient.Open(bg, dbclient.SocketDir(d.sockets), dbwire.RoleWriter, dbclient.Options{})
	must(t, err)
	s.openWriter = time.Since(at)
	at = time.Now()
	d.r, err = dbclient.Open(bg, dbclient.SocketDir(d.sockets), dbwire.RoleReader, dbclient.Options{})
	must(t, err)
	must(t, d.r.View(bg, func(tx *dbclient.Tx) error {
		_, _, err := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema))
		return err
	}))
	s.firstRead = time.Since(at)
	return s
}

// stop closes the clients, which have no request in progress, and then the daemon, and returns the time of that.
func (d *benchDaemon) stop() time.Duration {
	t := d.t
	t.Helper()
	_ = d.w.Close()
	_ = d.r.Close()
	at := time.Now()
	must(t, d.h.Release())
	return time.Since(at)
}

// seed makes the first commit of the store, as store.Create does.
func (d *benchDaemon) seed() {
	d.t.Helper()
	must(d.t, d.w.Update(bg, func(tx *dbclient.Tx) error {
		if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion)); err != nil {
			return err
		}
		return tx.AppendEvent([]byte(`{"seed":true}`))
	}))
}

// benchEventData returns an event of benchEvent bytes.
func benchEventData() []byte {
	return []byte(`{"p":"` + strings.Repeat("x", benchEvent-8) + `"}`)
}

// update puts the row key of benchRow bytes and an event in one Update, and returns its time.
func (d *benchDaemon) update(key string, row, event []byte) (time.Duration, error) {
	at := time.Now()
	err := d.w.Update(bg, func(tx *dbclient.Tx) error {
		if err := tx.Put(benchTable, []byte(key), row); err != nil {
			return err
		}
		return tx.AppendEvent(event)
	})
	return time.Since(at), err
}

// probeSample is one Update of a probe.
type probeSample struct{ start, end time.Time }

// probe sends one small Update every benchProbeEvery on the writer client, and keeps when each began and ended.
type probe struct {
	stop chan struct{}
	done chan struct{}

	mu      sync.Mutex
	samples []probeSample
	err     error
}

func startProbe(d *benchDaemon, prefix string) *probe {
	p := &probe{stop: make(chan struct{}), done: make(chan struct{})}
	row, event := make([]byte, benchRow), benchEventData()
	go func() {
		defer close(p.done)
		for i := 0; ; i++ {
			select {
			case <-p.stop:
				return
			case <-time.After(benchProbeEvery):
			}
			at := time.Now()
			_, err := d.update(fmt.Sprintf("%s%08d", prefix, i), row, event)
			p.mu.Lock()
			p.samples = append(p.samples, probeSample{at, time.Now()})
			if err != nil && p.err == nil {
				p.err = err
			}
			p.mu.Unlock()
		}
	}()
	return p
}

func (p *probe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.samples)
}

// end stops the probe, and returns its samples.
func (p *probe) end(t *testing.T) []probeSample {
	t.Helper()
	close(p.stop)
	<-p.done
	if p.err != nil {
		t.Fatalf("an Update of the probe failed: %v", p.err)
	}
	return p.samples
}

// probeWindow is what the samples of a probe say about the time from from to to.
type probeWindow struct {
	Before  int     // the Updates that were over before from
	Median  float64 // of those, in ms
	During  int     // the Updates sent from from until to
	Longest float64 // of those, in ms: the longest that a Commit sent during the window took
	Overlap float64 // in ms: the longest Update that was in progress at any time of the window, one sent before it too
}

func benchWindow(samples []probeSample, from, to time.Time) probeWindow {
	var w probeWindow
	var before []float64
	for _, s := range samples {
		d := benchMs(s.end.Sub(s.start))
		if s.end.After(from) && s.start.Before(to) {
			w.Overlap = max(w.Overlap, d)
		}
		switch {
		case !s.end.After(from):
			before = append(before, d)
		case !s.start.Before(from) && s.start.Before(to):
			w.During++
			w.Longest = max(w.Longest, d)
		}
	}
	w.Before = len(before)
	if len(before) > 0 {
		w.Median = benchSummary(before).P50
	}
	return w
}

// benchClock is how the machine reads the clock: the clock source of the kernel, if it can be read, and the cost of time.Now.
type benchClock struct {
	source string
	nowNs  float64 // the mean of benchClockReads calls of time.Now; 0: not measured
}

const (
	benchClockSource = "/sys/devices/system/clocksource/clocksource0/current_clocksource"
	benchClockReads  = 100_000
)

func measureClock() benchClock {
	c := benchClock{source: benchFirst(benchClockSource, "")}
	var last time.Time
	start := time.Now()
	for range benchClockReads {
		last = time.Now()
	}
	c.nowNs = float64(time.Since(start).Nanoseconds()) / benchClockReads
	_ = last
	return c
}

// bench is the state of TestBenchmarks.
type bench struct {
	t    *testing.T
	raw  *os.File
	work string
	main *benchDaemon
	md   strings.Builder
	src  *rand.ChaCha8

	clock    benchClock
	getKeys  [][]byte // of gets(), which GetMany reads again
	getOrder []int
}

// series writes the series to raw.jsonl and returns its numbers.
func (b *bench) series(name, unit string, values []float64) benchStats {
	b.t.Helper()
	line, err := json.Marshal(benchSeries{Name: name, Unit: unit, Values: values})
	must(b.t, err)
	_, err = b.raw.Write(append(line, '\n'))
	must(b.t, err)
	b.t.Logf("%s %s: %d values, median %s %s", time.Now().Format(time.TimeOnly), name, len(values),
		benchNum(benchSummary(values).P50), unit)
	return benchSummary(values)
}

// random returns n pseudo-random bytes.
func (b *bench) random(n int) []byte {
	p := make([]byte, n)
	_, _ = b.src.Read(p)
	return p
}

func (b *bench) section(heading, text string) {
	fmt.Fprintf(&b.md, "\n## %s\n\n%s\n\n", heading, text)
}

func (b *bench) row(cells ...string) { b.md.WriteString("| " + strings.Join(cells, " | ") + " |\n") }

func (b *bench) head(cells ...string) {
	b.row(cells...)
	b.md.WriteString("|" + strings.Repeat(" ---: |", len(cells)) + "\n")
}

// TestBenchmarks runs the benchmarks of the DB daemon if envBench names a directory for the results: a daemon in this
// process on a data directory under TMPDIR, which should be on a real disk and have a short path (the sockets are under
// it too), and clients of it over socket files. It writes raw.jsonl (every value of every measurement) and results.md.
func TestBenchmarks(t *testing.T) {
	out := os.Getenv(envBench)
	if out == "" {
		t.Skip("the benchmarks of the DB daemon, which write their results to the directory " + envBench)
	}
	if raceEnabled {
		t.Fatal("the benchmarks need a test binary without -race")
	}
	noLeaks(t)
	must(t, os.MkdirAll(out, 0o755))
	raw, err := os.Create(filepath.Join(out, "raw.jsonl"))
	must(t, err)
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Error(err)
		}
	})
	work, err := os.MkdirTemp("", "whiteroom-daemon-bench-")
	must(t, err)
	t.Cleanup(func() {
		if err := os.RemoveAll(work); err != nil {
			t.Error(err)
		}
	})
	work, err = filepath.EvalSymlinks(work)
	must(t, err)

	b := &bench{t: t, raw: raw, work: work, src: rand.NewChaCha8([32]byte{'d', 'a', 'e', 'm', 'o', 'n'}), clock: measureClock()}
	started, load := time.Now(), [2]string{benchLoad(), ""}
	b.main = newBenchDaemon(t, filepath.Join(work, "data"), nil)
	b.main.start()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			b.main.stop()
		}
	})
	b.main.seed()

	b.echo()
	b.gets()
	b.getMany()
	b.objectWindows()
	b.updates()
	b.defaultLimits()
	b.scan()
	b.objects()
	b.originals()
	b.backups()
	b.verifies()
	b.restarts()
	b.main.stop()
	stopped = true
	load[1] = benchLoad()

	md := b.markdown(work, started, time.Since(started), load)
	must(t, os.WriteFile(filepath.Join(out, "results.md"), []byte(md), 0o644))
}

// TestBenchmarkNote checks that results.md says how the benchmark ran.
func TestBenchmarkNote(t *testing.T) {
	b := &bench{}
	md := b.markdown("", time.Time{}, 0, [2]string{})
	for _, want := range []string{"over Unix domain socket files", "in one process", "includes one such read of the clock",
		"Clock source of the kernel (" + benchClockSource + ")", "time.Now takes"} {
		if !strings.Contains(md, want) {
			t.Errorf("results.md does not say %q:\n%s", want, md)
		}
	}
}

func (b *bench) markdown(work string, started time.Time, elapsed time.Duration, load [2]string) string {
	var h strings.Builder
	fmt.Fprintf(&h, "# Benchmarks of the DB daemon\n\n"+
		"A daemon (internal/dbserver, the engine of internal/logdb on a data directory) and its clients (internal/dbclient) "+
		"in one process, over Unix domain socket files that the daemon listens on (writer.sock and reader.sock), on a data "+
		"directory on the disk. The numbers are those of the socket, the protocol, the daemon and the client together; "+
		"the process of the daemon and that of the client are not separate, so the resident set is of both. Each call is "+
		"timed with time.Now before it and time.Since after it, so the time of a call includes one such read of the clock "+
		"(its cost is in the list below). The daemon has the limits of dbtest (%+v), except in the "+
		"measurement that says that it has the defaults. Started %s; %.0f s.\n\n",
		dbwire.Limits{CommitsPerSecond: -1, MinFree: -1}.WithDefaults(), started.Format(time.RFC3339), elapsed.Seconds())
	memory, _ := strconv.ParseInt(strings.TrimSuffix(benchFirst("/proc/meminfo", "MemTotal"), " kB"), 10, 64)
	fmt.Fprintf(&h, "- %s %s/%s\n- %s, %d CPUs, %d MiB of memory, Linux %s\n- Work directory %s (%s)\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, benchFirst("/proc/cpuinfo", "model name"), runtime.NumCPU(),
		memory>>10, benchFirst("/proc/sys/kernel/osrelease", ""), work, benchMount(work))
	source, now := b.clock.source, "unknown (not measured)"
	if source == "" {
		source = "unknown (cannot be read)"
	}
	if b.clock.nowNs > 0 {
		now = benchNum(b.clock.nowNs) + " ns each"
	}
	fmt.Fprintf(&h, "- Clock source of the kernel (%s): %s; time.Now takes %s (the mean of %d calls, measured at the start)\n",
		benchClockSource, source, now, benchClockReads)
	h.WriteString("- Settings of the runtime:")
	for _, name := range []string{"GOGC", "GOMEMLIMIT", "GODEBUG"} {
		v, ok := os.LookupEnv(name)
		fmt.Fprintf(&h, " %s=%q", name, v)
		if !ok {
			h.WriteString(" (unset)")
		}
	}
	fmt.Fprintf(&h, " GOMAXPROCS=%d\n", runtime.GOMAXPROCS(0))
	fmt.Fprintf(&h, "- Load average of the machine (1, 5 and 15 minutes, /proc/loadavg), other processes included: "+
		"%s at the start, %s at the end\n", load[0], load[1])
	return h.String() + b.md.String()
}

// echo measures the floor under every call: a request and its reply of benchEcho bytes each over a Unix domain socket
// between two goroutines of this process, with no protocol.
func (b *bench) echo() {
	t := b.t
	t.Helper()
	path := filepath.Join(benchSocketDir(t), "echo.sock")
	l, err := net.Listen("unix", path)
	must(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, benchEcho)
		for {
			if _, err := io.ReadFull(conn, buf); err != nil {
				return
			}
			if _, err := conn.Write(buf); err != nil {
				return
			}
		}
	}()
	conn, err := net.Dial("unix", path)
	must(t, err)
	buf := make([]byte, benchEcho)
	times := make([]float64, 0, benchEchoes)
	for range benchEchoes {
		at := time.Now()
		_, err := conn.Write(buf)
		if err == nil {
			_, err = io.ReadFull(conn, buf)
		}
		d := time.Since(at)
		must(t, err)
		times = append(times, benchUs(d))
	}
	must(t, conn.Close())
	must(t, l.Close())
	<-done
	st := b.series("Echo", "us", times)
	b.section("Echo", fmt.Sprintf("The floor under every call: a request and its reply of %d bytes each over a Unix domain "+
		"socket between two goroutines of this process (a listener in the socket directory, one connection, the other "+
		"side reads the request with io.ReadFull and writes it back), with no protocol and no daemon. %d round trips.",
		benchEcho, benchEchoes))
	b.head("round trips", "median (us)", "99th percentile (us)", "max (us)")
	b.row(strconv.Itoa(st.N), benchNum(st.P50), benchNum(st.P99), benchNum(st.Max))
}

// gets measures Get: a row of benchGetValue bytes by its key, for each of benchGets rows, in one View.
func (b *bench) gets() {
	t := b.t
	t.Helper()
	keys := make([][]byte, benchGets)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "g%06d", i)
	}
	value := b.random(benchGetValue)
	const batch = 10_000
	for from := 0; from < benchGets; from += batch {
		must(t, b.main.w.Update(bg, func(tx *dbclient.Tx) error {
			for i := from; i < from+batch; i++ {
				binary.BigEndian.PutUint64(value, uint64(i))
				if err := tx.Put(benchTable, keys[i], value); err != nil {
					return err
				}
			}
			return tx.AppendEvent(benchEventData())
		}))
	}
	order := rand.New(rand.NewPCG(1, 2)).Perm(benchGets)
	b.getKeys, b.getOrder = keys, order
	times := make([]float64, 0, benchGets)
	must(t, b.main.r.View(bg, func(tx *dbclient.Tx) error {
		// The first read takes the snapshot.
		if _, _, err := tx.Get(benchTable, []byte("missing")); err != nil {
			return err
		}
		for _, i := range order {
			at := time.Now()
			v, ok, err := tx.Get(benchTable, keys[i])
			d := time.Since(at)
			if err != nil || !ok || len(v) != benchGetValue || binary.BigEndian.Uint64(v) != uint64(i) {
				t.Fatalf("Get(%s) = %d bytes, %v, %v", keys[i], len(v), ok, err)
			}
			times = append(times, benchUs(d))
		}
		return nil
	}))
	st := b.series("Get", "us", times)
	b.section("Get", fmt.Sprintf("Get of a row of %d bytes by its key (7 bytes), on a reader connection: %d rows were put "+
		"before, and one View reads each of them once, in a random order (the client keeps the answer for a key in a View). "+
		"The time of each call.", benchGetValue, benchGets))
	b.head("calls", "median (us)", "99th percentile (us)", "max (us)", "calls per second")
	b.row(strconv.Itoa(st.N), benchNum(st.P50), benchNum(st.P99), benchNum(st.Max), benchNum(float64(st.N)/(st.Sum/1e6)))
}

// getMany measures Get through windows: Tx.GetMany of benchWindowKeys keys a call, over the rows of gets(), in the order of
// gets(), in a View of its own.
func (b *bench) getMany() {
	t := b.t
	t.Helper()
	keys, order := b.getKeys, b.getOrder
	var calls, perKey, withGets []float64
	must(t, b.main.r.View(bg, func(tx *dbclient.Tx) error {
		// The first read takes the snapshot.
		if _, _, err := tx.Get(benchTable, []byte("missing")); err != nil {
			return err
		}
		batch := make([][]byte, 0, benchWindowKeys)
		for from := 0; from < len(order); from += benchWindowKeys {
			ids := order[from:min(from+benchWindowKeys, len(order))]
			batch = batch[:0]
			for _, i := range ids {
				batch = append(batch, keys[i])
			}
			at := time.Now()
			err := tx.GetMany(benchTable, batch)
			many := time.Since(at)
			if err != nil {
				return err
			}
			at = time.Now()
			bad := -1
			for _, i := range ids {
				v, ok, err := tx.Get(benchTable, keys[i])
				if err != nil || !ok || len(v) != benchGetValue || binary.BigEndian.Uint64(v) != uint64(i) {
					bad = i
				}
			}
			gets := time.Since(at)
			if bad >= 0 {
				t.Fatalf("Get(%s) after GetMany is not the row that was put", keys[bad])
			}
			calls = append(calls, benchUs(many))
			perKey = append(perKey, benchUs(many)/float64(len(ids)))
			withGets = append(withGets, benchUs(many+gets)/float64(len(ids)))
		}
		return nil
	}))
	st, pk, wg := b.series("GetMany", "us", calls), b.series("GetMany per key", "us", perKey),
		b.series("GetMany and Get per key", "us", withGets)
	b.section("GetMany", fmt.Sprintf("Get through windows: the same %d rows as Get, read in the same random order in a View of its "+
		"own, with Tx.GetMany of %d keys a call (the last call has %d), which sends the requests of a call in windows of at most "+
		"%d requests and %d bytes before it reads their replies. The time of each call of GetMany; the time for a key is the time "+
		"of a call divided by its keys. Then each key is read with Get, which asks nothing, and the last row has the time of "+
		"GetMany and of those Gets together, for a key.", benchGets, benchWindowKeys, benchGets%benchWindowKeys,
		dbwire.WindowRequests, dbwire.WindowBytes))
	b.head("calls", "keys a call", "median of a call (us)", "99th percentile of a call (us)", "max of a call (us)",
		"median for a key (us)", "99th percentile for a key (us)", "median for a key with the Gets (us)")
	b.row(strconv.Itoa(st.N), strconv.Itoa(benchWindowKeys), benchNum(st.P50), benchNum(st.P99), benchNum(st.Max), benchNum(pk.P50),
		benchNum(pk.P99), benchNum(wg.P50))
}

// objectWindows measures Object through windows: benchWindowObjects objects of benchWindowObjectSize bytes, read in Views
// with Tx.PrefetchObjects of benchWindowKeys hashes and the calls of Object that take them, and, for comparison, with
// Object one at a time.
func (b *bench) objectWindows() {
	t := b.t
	t.Helper()
	hashes := make([][32]byte, benchWindowObjects)
	datas := make([][]byte, benchWindowObjects)
	const batch = 128
	for from := 0; from < benchWindowObjects; from += batch {
		must(t, b.main.w.Update(bg, func(tx *dbclient.Tx) error {
			for i := from; i < from+batch; i++ {
				datas[i] = b.random(benchWindowObjectSize)
				hashes[i] = sha256.Sum256(datas[i])
				if err := tx.PutObject(hashes[i], datas[i]); err != nil {
					return err
				}
			}
			return tx.AppendEvent(benchEventData())
		}))
	}
	var calls, perObject, single []float64
	for range benchWindowPasses {
		must(t, b.main.r.View(bg, func(tx *dbclient.Tx) error {
			if _, _, err := tx.Get(benchTable, []byte("missing")); err != nil { // the snapshot
				return err
			}
			got := make([][]byte, benchWindowKeys)
			for from := 0; from < benchWindowObjects; from += benchWindowKeys {
				at := time.Now()
				err := tx.PrefetchObjects(hashes[from : from+benchWindowKeys])
				for i := range got {
					if err == nil {
						got[i], _, err = tx.Object(hashes[from+i])
					}
				}
				d := time.Since(at)
				if err != nil {
					return err
				}
				for i := range got {
					if !bytes.Equal(got[i], datas[from+i]) {
						t.Fatalf("Object %d after PrefetchObjects is not the object that was put", from+i)
					}
				}
				calls = append(calls, benchUs(d))
				perObject = append(perObject, benchUs(d)/benchWindowKeys)
			}
			return nil
		}))
		must(t, b.main.r.View(bg, func(tx *dbclient.Tx) error {
			for i, h := range hashes {
				at := time.Now()
				data, ok, err := tx.Object(h)
				d := time.Since(at)
				if err != nil || !ok || !bytes.Equal(data, datas[i]) {
					t.Fatalf("Object %d = %d bytes, %v, %v", i, len(data), ok, err)
				}
				single = append(single, benchUs(d))
			}
			return nil
		}))
	}
	st, po, sg := b.series("PrefetchObjects and Object", "us", calls), b.series("PrefetchObjects and Object per object", "us", perObject),
		b.series("Object 4 KiB", "us", single)
	b.section("Objects through windows", fmt.Sprintf("%d objects of %d bytes were put in Updates before the time. In a View, "+
		"Tx.PrefetchObjects of %d hashes and the %d calls of Object that take them are timed together, a call for each %d "+
		"objects in turn, so %d calls for each View; there are %d such Views, each with a snapshot of its own (%d calls in all). "+
		"For comparison, %d more Views read each object on its own with Object (%d calls). The data are compared with what "+
		"was put after the time.", benchWindowObjects, benchWindowObjectSize, benchWindowKeys, benchWindowKeys, benchWindowKeys,
		benchWindowObjects/benchWindowKeys, benchWindowPasses, st.N, benchWindowPasses, sg.N))
	b.head("calls", "median of a call of 256 objects (us)", "99th percentile of a call (us)", "median for an object (us)",
		"99th percentile for an object (us)", "Object on its own: median (us)", "Object on its own: 99th percentile (us)")
	b.row(strconv.Itoa(st.N), benchNum(st.P50), benchNum(st.P99), benchNum(po.P50), benchNum(po.P99), benchNum(sg.P50), benchNum(sg.P99))
}

// updates measures Update with no limit of commits per second.
func (b *bench) updates() {
	t := b.t
	t.Helper()
	row, event := b.random(benchRow), benchEventData()
	times := make([]float64, 0, benchUpdates)
	start := time.Now()
	for i := range benchUpdates {
		d, err := b.main.update(fmt.Sprintf("u%06d", i), row, event)
		must(t, err)
		times = append(times, benchMs(d))
	}
	total := time.Since(start)
	st := b.series("Update", "ms", times)
	b.section("Updates", fmt.Sprintf("Update of one row of %d bytes and one event of %d bytes (one Put and one AppendEvent, "+
		"so one Commit of two ops), on the writer connection, one after another. The daemon has no limit of commits per second.",
		benchRow, benchEvent))
	b.head("Updates", "total (s)", "per second", "median (ms)", "99th percentile (ms)", "max (ms)")
	b.row(strconv.Itoa(st.N), benchNum(total.Seconds()), benchNum(float64(st.N)/total.Seconds()), benchNum(st.P50),
		benchNum(st.P99), benchNum(st.Max))
}

// defaultLimits measures Update on a daemon with every limit at its default, on a store of its own.
func (b *bench) defaultLimits() {
	t := b.t
	t.Helper()
	d := newBenchDaemon(t, filepath.Join(b.work, "defaults"), &dbwire.Limits{})
	d.start()
	refused := ""
	err := d.w.Update(bg, func(tx *dbclient.Tx) error {
		if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion)); err != nil {
			return err
		}
		return tx.AppendEvent([]byte(`{"seed":true}`))
	})
	var re *dbwire.RemoteError
	if errors.As(err, &re) && re.Code == dbwire.CodeNoSpace {
		refused = "The check of the free space (1 GiB, the default) refused the first commit: " + re.Msg + ". "
		d.stop()
		d.limits = &dbwire.Limits{MinFree: -1}
		d.start()
		err = nil
		d.seed()
	}
	must(t, err)
	// The bucket of the limit holds a second of commits: it is full again for the Updates that follow.
	time.Sleep(1100 * time.Millisecond)
	row, event := b.random(benchRow), benchEventData()
	times := make([]float64, 0, benchDefaultRun)
	start := time.Now()
	var firstEnd time.Time
	for i := range benchDefaultRun {
		took, err := d.update(fmt.Sprintf("d%06d", i), row, event)
		must(t, err)
		times = append(times, benchMs(took))
		if i == benchBurst-1 {
			firstEnd = time.Now()
		}
	}
	end := time.Now()
	d.stop()
	b.series("Update with the default limit", "ms", times)
	first, rest := benchSummary(times[:benchBurst]), benchSummary(times[benchBurst:])
	free := "the check of the free space is on"
	if d.limits.MinFree < 0 {
		free = "the check of the free space is off, since it refused the commit"
	}
	b.section("Updates with the default limit of commits", fmt.Sprintf("The same Updates on a daemon with every limit "+
		"at its default, on a store of its own: %d commits per second (a bucket that holds %d, and a commit that finds it "+
		"empty waits), %s. %sThe store was seeded, and the Updates began 1.1 s later, when the bucket was full again.",
		benchBurst, benchBurst, free, refused))
	b.head("Updates", "total (s)", "first 50: total (ms)", "first 50: median (ms)", "the rest: total (s)",
		"the rest: per second", "the rest: median (ms)", "the rest: max (ms)")
	b.row(strconv.Itoa(len(times)), benchNum(end.Sub(start).Seconds()), benchNum(firstEnd.Sub(start).Seconds()*1000),
		benchNum(first.P50), benchNum(end.Sub(firstEnd).Seconds()), benchNum(float64(rest.N)/end.Sub(firstEnd).Seconds()),
		benchNum(rest.P50), benchNum(rest.Max))
}

// scan measures Scan of benchScanRows rows of benchScanValue bytes.
func (b *bench) scan() {
	t := b.t
	t.Helper()
	const batch = 5000
	for from := 0; from < benchScanRows; from += batch {
		must(t, b.main.w.Update(bg, func(tx *dbclient.Tx) error {
			for i := from; i < from+batch; i++ {
				if err := tx.Put(benchTable, fmt.Appendf(nil, "s%06d", i), b.random(benchScanValue)); err != nil {
					return err
				}
			}
			return tx.AppendEvent(benchEventData())
		}))
	}
	times := make([]float64, 0, benchScanRuns)
	for range benchScanRuns {
		rows, bytes := 0, 0
		at := time.Now()
		must(t, b.main.r.View(bg, func(tx *dbclient.Tx) error {
			return tx.Scan(benchTable, []byte("s"), func(_, v []byte) bool {
				rows++
				bytes += len(v)
				return true
			})
		}))
		d := time.Since(at)
		if rows != benchScanRows || bytes != benchScanRows*benchScanValue {
			t.Fatalf("Scan gave %d rows with %d bytes of values, want %d with %d", rows, bytes, benchScanRows,
				benchScanRows*benchScanValue)
		}
		times = append(times, benchMs(d))
	}
	st := b.series("Scan", "ms", times)
	b.section("Scan", fmt.Sprintf("Scan of %d rows of %d bytes (keys of 7 bytes: %.1f MiB of values), in a View on a reader "+
		"connection, %d times; the time of the View, which includes the Snapshot and the Release.", benchScanRows,
		benchScanValue, float64(benchScanRows*benchScanValue)/mib, benchScanRuns))
	b.head("rows", "runs", "median (ms)", "min (ms)", "max (ms)", "MiB/s of values (median)", "rows per second (median)")
	b.row(strconv.Itoa(benchScanRows), strconv.Itoa(st.N), benchNum(st.P50), benchNum(st.Min), benchNum(st.Max),
		benchNum(float64(benchScanRows*benchScanValue)/mib/(st.P50/1000)), benchNum(float64(benchScanRows)/(st.P50/1000)))
}

// objects measures PutObject and Object of 64 KiB and of 8 MiB.
func (b *bench) objects() {
	t := b.t
	t.Helper()
	b.section("Object", "PutObject in an Update (with one event), and Object in a View on a reader connection, of "+
		"objects of random bytes, each with its own hash; the hash is computed before the time. Each call of Object is "+
		"timed on its own; the data are compared with what was put after the time.")
	b.head("size", "objects", "put: median (ms)", "put: 99th percentile (ms)", "put: MiB/s (median)", "reads", "read: median (ms)",
		"read: 99th percentile (ms)", "read: MiB/s (median)")
	for _, c := range []struct {
		name               string
		size, puts, passes int
	}{
		{name: "64 KiB", size: 64 << 10, puts: 200, passes: 5},
		{name: "8 MiB", size: 8 * mib, puts: 10, passes: 5},
	} {
		data := make([][]byte, c.puts)
		hashes := make([][32]byte, c.puts)
		for i := range data {
			data[i] = b.random(c.size)
			hashes[i] = sha256.Sum256(data[i])
		}
		put := make([]float64, 0, c.puts)
		for i := range data {
			at := time.Now()
			must(t, b.main.w.Update(bg, func(tx *dbclient.Tx) error {
				if err := tx.PutObject(hashes[i], data[i]); err != nil {
					return err
				}
				return tx.AppendEvent(benchEventData())
			}))
			put = append(put, benchMs(time.Since(at)))
		}
		var read []float64
		must(t, b.main.r.View(bg, func(tx *dbclient.Tx) error {
			for range c.passes {
				for i := range hashes {
					at := time.Now()
					got, ok, err := tx.Object(hashes[i])
					d := time.Since(at)
					if err != nil || !ok || !bytes.Equal(got, data[i]) {
						t.Fatalf("Object of %s number %d = %d bytes, %v, %v", c.name, i, len(got), ok, err)
					}
					read = append(read, benchMs(d))
				}
			}
			return nil
		}))
		ps, rs := b.series("PutObject "+c.name, "ms", put), b.series("Object "+c.name, "ms", read)
		b.row(c.name, strconv.Itoa(c.puts), benchNum(ps.P50), benchNum(ps.P99), benchNum(float64(c.size)/mib/(ps.P50/1000)),
			strconv.Itoa(rs.N), benchNum(rs.P50), benchNum(rs.P99), benchNum(float64(c.size)/mib/(rs.P50/1000)))
	}
}

// originals measures PutOriginal and Original of benchOriginalSize bytes.
func (b *bench) originals() {
	t := b.t
	t.Helper()
	buf := b.random(benchOriginalSize)
	var put, read []float64
	var last [32]byte
	for i := range benchOriginals {
		binary.BigEndian.PutUint64(buf, uint64(i)) // another original each time
		at := time.Now()
		sum, size, err := b.main.w.PutOriginal(bg, bytes.NewReader(buf))
		d := time.Since(at)
		must(t, err)
		if size != benchOriginalSize || sum != sha256.Sum256(buf) {
			t.Fatalf("PutOriginal gave %d bytes with the hash %x, which is not that of what was sent", size, sum)
		}
		put = append(put, d.Seconds())
		last = sum

		at = time.Now()
		o, err := b.main.r.Original(bg, sum)
		must(t, err)
		n, err := io.Copy(io.Discard, o)
		d = time.Since(at)
		must(t, err)
		must(t, o.Close())
		if n != benchOriginalSize {
			t.Fatalf("Original gave %d bytes, want %d", n, benchOriginalSize)
		}
		read = append(read, d.Seconds())
	}
	// The client does not check the hash of what it reads: do it for the last one, outside the time.
	o, err := b.main.r.Original(bg, last)
	must(t, err)
	h := sha256.New()
	_, err = io.Copy(h, o)
	must(t, err)
	must(t, o.Close())
	if got := [32]byte(h.Sum(nil)); got != last {
		t.Fatalf("the original that was read has the hash %x, want %x", got, last)
	}
	ps, rs := b.series("PutOriginal", "s", put), b.series("Original", "s", read)
	b.section("Original", fmt.Sprintf("PutOriginal of %d MiB of random bytes (the most that the daemon takes), on the writer "+
		"connection, and Original of it on a reader connection read to its end into io.Discard, %d times, each with other "+
		"bytes. The original is read just after it was written, from the page cache.", benchOriginalSize/mib, benchOriginals))
	b.head("call", "runs", "median (s)", "min (s)", "max (s)", "MiB/s (median)")
	b.row("PutOriginal", strconv.Itoa(ps.N), benchNum(ps.P50), benchNum(ps.Min), benchNum(ps.Max),
		benchNum(float64(benchOriginalSize)/mib/ps.P50))
	b.row("Original", strconv.Itoa(rs.N), benchNum(rs.P50), benchNum(rs.Min), benchNum(rs.Max),
		benchNum(float64(benchOriginalSize)/mib/rs.P50))
}

// maintenanceRun is a Backup or a Verify: setup and cleanup are not timed, and cleanup returns the bytes that the run
// made, or 0.
type maintenanceRun struct {
	setup, run func(i int)
	cleanup    func(i int) int64
}

// maintenance runs m benchMaintenance times, each while a probe sends small Updates, and tabulates the runs.
func (b *bench) maintenance(name, what string, m maintenanceRun) {
	t := b.t
	t.Helper()
	b.section(name, what)
	b.head("run", "seconds", "bytes", "MiB/s", "Updates sent during", "longest Update sent during (ms)",
		"longest Update in progress during (ms)",
		"median Update before (ms)", "Updates before", "resident set before (MiB)", "peak before (MiB)",
		"resident set after (MiB)", "peak during (MiB)")
	var secs []float64
	for i := 1; i <= benchMaintenance; i++ {
		m.setup(i)
		runtime.GC()
		debug.FreeOSMemory()
		rssBefore, hwmBefore := benchStatus("VmRSS"), benchStatus("VmHWM")
		reset := benchResetPeak()
		p := startProbe(b.main, fmt.Sprintf("p%s%d-", strings.ToLower(name[:1]), i))
		for p.count() < benchProbeBefore {
			time.Sleep(benchProbeEvery)
		}
		from := time.Now()
		m.run(i)
		to := time.Now()
		rssAfter, hwmAfter := benchStatus("VmRSS"), benchStatus("VmHWM")
		w := benchWindow(p.end(t), from, to)
		size := m.cleanup(i)
		secs = append(secs, to.Sub(from).Seconds())
		peak := fmt.Sprintf("%.0f", float64(hwmAfter)/mib)
		if !reset {
			peak = "(not reset) " + peak
		}
		bytesCell, rate := "-", "-"
		if size > 0 {
			bytesCell, rate = strconv.FormatInt(size, 10), benchNum(float64(size)/mib/to.Sub(from).Seconds())
		}
		b.row(strconv.Itoa(i), benchNum(to.Sub(from).Seconds()), bytesCell, rate, strconv.Itoa(w.During), benchNum(w.Longest),
			benchNum(w.Overlap), benchNum(w.Median), strconv.Itoa(w.Before), fmt.Sprintf("%.0f", float64(rssBefore)/mib),
			fmt.Sprintf("%.0f", float64(hwmBefore)/mib), fmt.Sprintf("%.0f", float64(rssAfter)/mib), peak)
	}
	b.series(name, "s", secs)
}

// backups measures Backup of the store that the measurements before made.
func (b *bench) backups() {
	t := b.t
	t.Helper()
	var fsys vfs.FS
	var bk dbclient.Backup
	dir := func(i int) string { return filepath.Join(b.work, fmt.Sprintf("backup-%d", i)) }
	b.maintenance("Backup", "Backup of the store that the measurements above made (the rows, the objects and the "+
		"originals) into an empty directory on the same disk, with a reader client. While it runs, a probe sends one "+
		"small Update (a row of 100 bytes and an event of 400) every 10 ms on the writer connection and times each: "+
		"the commits wait while the reader of the engine opens. The resident set and its peak (VmRSS and VmHWM of the "+
		"process, which holds the daemon and the clients) are read after a GC and FreeOSMemory just before the run, "+
		"with the peak taken from the start of the process; the peak during is VmHWM after the peak was reset just "+
		"before the run (if the system lets it).", maintenanceRun{
		setup: func(i int) {
			must(t, os.Mkdir(dir(i), 0o700))
			var err error
			fsys, err = vfs.OpenRoot(dir(i))
			must(t, err)
		},
		run: func(int) {
			var err error
			bk, err = b.main.r.Backup(bg, fsys)
			must(t, err)
		},
		cleanup: func(i int) int64 {
			must(t, fsys.Close())
			size := benchSize(t, dir(i))
			must(t, os.RemoveAll(dir(i)))
			t.Logf("the backup %d has the commit %d and %d bytes", i, bk.Seq, size)
			return size
		},
	})
}

// verifies measures Verify.
func (b *bench) verifies() {
	t := b.t
	t.Helper()
	b.maintenance("Verify", "Verify of the same store, with a reader client, with the probe of Backup.", maintenanceRun{
		setup:   func(int) {},
		run:     func(int) { must(t, b.main.r.Verify(bg)) },
		cleanup: func(int) int64 { return 0 },
	})
}

// restarts stops the daemon and starts it again on its store, benchRestarts times.
func (b *bench) restarts() {
	t := b.t
	t.Helper()
	var seq uint64
	must(t, b.main.r.View(bg, func(tx *dbclient.Tx) error {
		seq = tx.Seq()
		return nil
	}))
	size := benchSize(t, b.main.data)
	b.section("Stop and start", fmt.Sprintf("The daemon is stopped (both clients closed, no request in progress, then "+
		"the handle released: the listeners, the connections and the engine are closed) and started again on the store "+
		"that the measurements made (%d commits, %.0f MiB in the data directory), %d times. Start is the opening of the "+
		"engine, the sockets, the connection of the writer client, and the connection of the reader client with its "+
		"first read.", seq, float64(size)/mib, benchRestarts))
	b.head("cycle", "stop (ms)", "open the engine (ms)", "listen (ms)", "writer connects (ms)", "reader connects and reads (ms)",
		"start (ms)", "stop and start (ms)")
	var stops, starts []float64
	for i := 1; i <= benchRestarts; i++ {
		stop := b.main.stop()
		s := b.main.start()
		stops, starts = append(stops, benchMs(stop)), append(starts, benchMs(s.total()))
		b.row(strconv.Itoa(i), benchNum(benchMs(stop)), benchNum(benchMs(s.acquire)), benchNum(benchMs(s.listen)),
			benchNum(benchMs(s.openWriter)), benchNum(benchMs(s.firstRead)), benchNum(benchMs(s.total())),
			benchNum(benchMs(stop+s.total())))
	}
	b.series("Stop", "ms", stops)
	b.series("Start", "ms", starts)
}
