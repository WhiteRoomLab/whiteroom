//go:build linux

package store

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The benchmarks of 3.5, whose conditions docs/03.05-log-engine.md gives: TestBenchmarks compares the log backend in
// each placement on the data sets of benchdata_test.go. Each scenario of a run is a process of its own,
// TestBenchHelper, which writes what it measures to a file; TestBenchmarks gathers it in the directory of envBench.

const (
	envBench       = "WHITEROOM_BENCH"        // the directory of the results
	envBenchDir    = "WHITEROOM_BENCH_DIR"    // where the data directories are made; os.TempDir() if it is empty
	envBenchSmoke  = "WHITEROOM_BENCH_SMOKE"  // 1: the set smoke, once per backend
	envBenchHelper = "WHITEROOM_BENCH_HELPER" // the job of TestBenchHelper, in JSON
)

const (
	benchRuns      = 3
	benchSingles   = 300           // the single saves of ops
	benchReads     = 1_000         // each of Document, History and Revisions in ops
	benchLists     = 5             // Documents of the entries in ops
	benchLoopSaves = 100           // the single saves of the process that is killed before open-killed, before its kill
	benchLimit     = 2 * time.Hour // a process of a scenario that runs longer is killed, and the run fails
)

// benchBackends are the log backend in each placement with the defaults of the engine, not the options of allBackends:
// Hybrid puts objects of 64 KiB or more in files, segments and automatic checkpoints come every 64 MiB of the log, and
// the checkpoints are written in the background.
var benchBackends = []testBackend{
	{name: "log-inline", log: &logdb.Options{Placement: logdb.Inline}},
	{name: "log-files", log: &logdb.Options{Placement: logdb.Files}},
	{name: "log-hybrid", log: &logdb.Options{Placement: logdb.Hybrid}},
}

// benchScenarios are the scenarios of a run, each a process, in order. Before open-killed, the process of the scenario
// loop makes single saves until it is killed.
var benchScenarios = []string{"load", "ops", "open-clean", "open-replay", "open-killed", "checkpoint", "backup"}

// benchKinds are the kinds of documents, all of which the build-like read of ops reads.
var benchKinds = []content.RefKind{content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor,
	content.RefMenu, content.RefForm, content.RefRedirect, SettingsKind}

const mib = 1 << 20

// benchJob is what a process of TestBenchHelper does.
type benchJob struct {
	Scenario, Set, Backend string
	Run                    int
	Dir                    string // the data directory
	Result                 string // the file that the process writes its result to
}

// benchResult is what a process has measured. Durations are in nanoseconds, and sizes in bytes.
type benchResult struct {
	Job                      benchJob
	Seconds                  float64 // of the process, as TestBenchmarks has seen it
	Durations                map[string]int64
	Latencies                map[string][]int64 // of each call, in order
	StartRSS, MaxRSS         int64              // the largest resident set of the process at the start and the end
	WriteBytes               int64              // write_bytes of /proc/self/io during the scenario, or -1
	Disk, BackupDisk         *benchDisk         // the data directory after the scenario, and the copy of backup
	CheckpointBytes          int64              // the file of the checkpoint of the scenario checkpoint
	Counts, CheckpointCounts *vfs.Counts        // of the data directory during the scenario, and during Checkpoint
	Stats                    *logdb.Stats       // of the engine
	Recovery                 *logdb.RecoveryInfo
	Found                    map[string]int64 // what the scenario has counted
	Heap                     []benchHeap

	written int64 // write_bytes at the start
	ended   bool
}

// benchDisk is what a directory takes on the disk: the blocks of its files and directories (st_blocks), with the sizes
// and the number of its regular files.
type benchDisk struct {
	Allocated, Bytes int64
	Files, Dirs      int
}

// benchHeap is the live heap after a GC at a stage of a scenario.
type benchHeap struct {
	Stage     string
	HeapAlloc uint64
}

func benchSetNamed(name string) (benchSet, bool) {
	for _, bs := range append(slices.Clip(benchSets), benchSmoke) {
		if bs.name == name {
			return bs, true
		}
	}
	return benchSet{}, false
}

func benchBackendNamed(name string) (testBackend, bool) {
	for _, tb := range benchBackends {
		if tb.name == name {
			return tb, true
		}
	}
	return testBackend{}, false
}

// TestBenchHelper is the process of a scenario of TestBenchmarks, which runs the job of envBenchHelper and writes what
// it has measured to the file of the job. On its own, it does nothing.
func TestBenchHelper(t *testing.T) {
	spec := os.Getenv(envBenchHelper)
	if spec == "" {
		t.Skip("run by TestBenchmarks")
	}
	var job benchJob
	if err := json.Unmarshal([]byte(spec), &job); err != nil {
		t.Fatal(err)
	}
	bs, ok := benchSetNamed(job.Set)
	if !ok {
		t.Fatalf("no data set %q", job.Set)
	}
	tb, ok := benchBackendNamed(job.Backend)
	if !ok {
		t.Fatalf("no backend %q", job.Backend)
	}
	res := &benchResult{Job: job, Durations: map[string]int64{}, Latencies: map[string][]int64{},
		Found: map[string]int64{}, StartRSS: benchMaxRSS(t), written: benchWriteBytes()}
	switch job.Scenario {
	case "load":
		benchLoad(t, bs, tb, res)
	case "ops":
		benchOps(t, bs, tb, res)
	case "open-clean", "open-replay", "open-killed":
		benchOpen(t, tb, res)
	case "loop":
		benchLoop(t, bs, tb, job.Dir)
	case "checkpoint":
		benchCheckpoint(t, bs, tb, res)
	case "backup":
		benchBackup(t, tb, res)
	default:
		t.Fatalf("no scenario %q", job.Scenario)
	}
	res.end(t)
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job.Result, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fmt.Println(marker + "done")
}

// end ends the measurement of the scenario, once: its write_bytes and the largest resident set of the process.
func (r *benchResult) end(t *testing.T) {
	if r.ended {
		return
	}
	r.ended = true
	if w := benchWriteBytes(); w >= 0 && r.written >= 0 {
		r.WriteBytes = w - r.written
	} else {
		r.WriteBytes = -1
	}
	r.MaxRSS = benchMaxRSS(t)
}

// benchMaxRSS returns the largest resident set of the process since it has started the test binary, in bytes: VmHWM of
// /proc/self/status. Not ru_maxrss of getrusage, which Linux keeps across execve: a process of os/exec has the memory
// of its parent until then (vfork), and so starts with the largest resident set of the parent.
func benchMaxRSS(t *testing.T) int64 {
	t.Helper()
	kb, err := strconv.ParseInt(strings.TrimSuffix(benchFirst("/proc/self/status", "VmHWM"), " kB"), 10, 64)
	if err != nil {
		t.Fatalf("VmHWM of /proc/self/status: %v", err)
	}
	return kb << 10
}

// benchWriteBytes returns the bytes that the process has caused to be written to the storage, by write_bytes of
// /proc/self/io, or -1 if the kernel does not tell.
func benchWriteBytes() int64 {
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return -1
	}
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "write_bytes: "); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n
			}
		}
	}
	return -1
}

// benchStore opens the store of tb in the data directory dir, or makes it there if fresh is true. It uses dir through a
// vfs.Counter, which it returns too.
func benchStore(t *testing.T, tb testBackend, dir string, fresh bool) (*Store, *vfs.Counter) {
	t.Helper()
	if fresh {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root, err := vfs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := vfs.Count(root)
	var s *Store
	if fresh {
		s, err = create(dir, options{log: tb.log, fs: c})
	} else {
		s, err = open(dir, options{log: tb.log, fs: c})
	}
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	return s, c
}

// benchClose closes s and c, which benchStore has returned, and returns how long Close of s has taken.
func benchClose(t *testing.T, s *Store, c *vfs.Counter) int64 {
	t.Helper()
	start := time.Now()
	err := s.Close()
	d := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	return int64(d)
}

// count sets the counts of the result to those of c.
func (r *benchResult) count(c *vfs.Counter) {
	counts := c.Counts()
	r.Counts = &counts
}

// benchUpdate puts saves in one Update of s, and returns how long the Update has taken.
func benchUpdate(t *testing.T, s *Store, saves ...benchSave) time.Duration {
	t.Helper()
	start := time.Now()
	err := s.Update(t.Context(), func(tx *Tx) error {
		for _, sv := range saves {
			if _, err := tx.Put(sv.kind, sv.id, sv.doc, sv.by); err != nil {
				return err
			}
			if sv.rev != nil {
				if _, err := tx.AddRevision(sv.id, sv.revID, sv.rev, sv.by); err != nil {
					return err
				}
			}
			for _, e := range sv.events {
				if err := tx.appendEvent(e); err != nil {
					return err
				}
			}
		}
		return nil
	})
	d := time.Since(start)
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	return d
}

// benchCount returns the documents, versions, revisions, audit events and objects of s, with the Stats of its engine.
func benchCount(t *testing.T, s *Store) (map[string]int64, *logdb.Stats) {
	t.Helper()
	st := logDB(t, s).Stats()
	return map[string]int64{
		"documents": int64(st.Keys[tableDocuments]), "versions": int64(st.Keys[tableHistory]),
		"revisions": int64(st.Keys[tableRevisions]), "events": int64(st.Events),
		"objects": int64(st.InlineObjects + st.FileObjects),
	}, &st
}

// benchHeapAt returns the live heap after a GC.
func benchHeapAt(stage string) benchHeap {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return benchHeap{Stage: stage, HeapAlloc: m.HeapAlloc}
}

// benchLoad makes the store of the job and loads bs, one Update for each batch of saves of a stage.
func benchLoad(t *testing.T, bs benchSet, tb testBackend, res *benchResult) {
	s, c := benchStore(t, tb, res.Job.Dir, true)
	var load time.Duration
	for i, stage := range bs.stages() {
		load += benchLoadStage(t, s, bs, bs.stage(i), res)
		res.Heap = append(res.Heap, benchHeapAt(stage))
	}
	res.Durations["load"] = int64(load)
	counts, st := benchCount(t, s)
	want := map[string]int64{"documents": int64(bs.documents()), "versions": int64(bs.saves()),
		"revisions": int64(bs.revisions()), "events": int64(bs.saves() * bs.events),
		"objects": int64(bs.saves() + bs.revisions())}
	for name, n := range want {
		if counts[name] != n {
			t.Fatalf("the store has %d %s after the load, want %d", counts[name], name, n)
		}
		res.Found[name] = counts[name]
	}
	res.Stats = st
	res.Durations["close"] = benchClose(t, s, c)
	res.count(c)
	res.Disk = benchDiskOf(t, res.Job.Dir)
}

// benchLoadStage puts the saves refs of bs in s, batch saves in each Update, and returns how long the Updates have
// taken, without the time to make the saves.
func benchLoadStage(t *testing.T, s *Store, bs benchSet, refs []benchRef, res *benchResult) time.Duration {
	t.Helper()
	var d time.Duration
	for len(refs) > 0 {
		batch := refs[:min(bs.batch, len(refs))]
		refs = refs[len(batch):]
		saves := make([]benchSave, len(batch))
		for i, ref := range batch {
			saves[i] = bs.loadSave(ref)
		}
		d += benchUpdate(t, s, saves...)
		res.Found["updates"]++
	}
	return d
}

// benchViews runs n Views of s, the i-th of which calls f with i, and returns how long each has taken.
func benchViews(t *testing.T, s *Store, n int, f func(tx *Tx, i int) error) []int64 {
	t.Helper()
	lat := make([]int64, n)
	for i := range n {
		start := time.Now()
		err := s.View(t.Context(), func(tx *Tx) error { return f(tx, i) })
		lat[i] = int64(time.Since(start))
		if err != nil {
			t.Fatalf("View() = %v", err)
		}
	}
	return lat
}

// benchDoc names a document of a data set.
type benchDoc struct {
	kind content.RefKind
	id   string
}

// benchPick returns a document of bs at random, or an entry if entries is true.
func benchPick(r *rand.Rand, bs benchSet, entries bool) benchDoc {
	n := bs.entries
	if !entries {
		n = bs.documents()
	}
	switch i := 1 + r.IntN(n); {
	case i <= bs.entries:
		return benchDoc{content.RefEntry, benchID("ent_", i)}
	case i <= bs.entries+bs.terms:
		return benchDoc{content.RefTerm, benchID("trm_", i-bs.entries)}
	default:
		return benchDoc{content.RefMedia, benchID("med_", i-bs.entries-bs.terms)}
	}
}

// benchOps opens the store of the job after the load, and measures reads, each in a View, then single saves, each in an
// Update.
func benchOps(t *testing.T, bs benchSet, tb testBackend, res *benchResult) {
	s, c := benchStore(t, tb, res.Job.Dir, false)
	r := benchRand(benchStreamPicks, 0)
	picks := func(entries bool) []benchDoc {
		docs := make([]benchDoc, benchReads)
		for i := range docs {
			docs[i] = benchPick(r, bs, entries)
		}
		return docs
	}
	docs, histories, entries := picks(false), picks(false), picks(true)
	res.Latencies["Document"] = benchViews(t, s, len(docs), func(tx *Tx, i int) error {
		_, err := tx.Document(docs[i].kind, docs[i].id)
		return err
	})
	res.Latencies["History"] = benchViews(t, s, len(histories), func(tx *Tx, i int) error {
		versions, err := tx.History(histories[i].kind, histories[i].id)
		if err == nil && len(versions) == 0 {
			err = fmt.Errorf("%s has no versions", histories[i].id)
		}
		return err
	})
	res.Latencies["Revisions"] = benchViews(t, s, len(entries), func(tx *Tx, i int) error {
		revs, err := tx.Revisions(entries[i].id)
		if err == nil && len(revs) == 0 {
			err = fmt.Errorf("%s has no revisions", entries[i].id)
		}
		return err
	})
	res.Latencies["Documents"] = benchViews(t, s, benchLists, func(tx *Tx, _ int) error {
		d, err := tx.Documents(content.RefEntry)
		if err == nil && len(d) != bs.entries {
			err = fmt.Errorf("Documents() has %d entries, want %d", len(d), bs.entries)
		}
		return err
	})
	res.Durations["build"] = benchBuild(t, s, res)
	if res.Found["build documents"] != int64(bs.documents()) || res.Found["build revisions"] != int64(bs.revisions()) {
		t.Fatalf("the build-like read has read %d documents and %d revisions, want %d and %d",
			res.Found["build documents"], res.Found["build revisions"], bs.documents(), bs.revisions())
	}
	for n := 1; n <= benchSingles; n++ {
		res.Latencies["save"] = append(res.Latencies["save"], int64(benchUpdate(t, s, bs.single(benchStreamOps, n))))
	}
	res.Durations["close"] = benchClose(t, s, c)
	res.count(c)
}

// benchBuild reads in one View what a build reads: the documents of every kind, and the revisions of every entry with
// the JSON of its last one. It returns how long the View has taken.
func benchBuild(t *testing.T, s *Store, res *benchResult) int64 {
	t.Helper()
	var docs, revs, data int64
	start := time.Now()
	err := s.View(t.Context(), func(tx *Tx) error {
		var entries []Document
		for _, kind := range benchKinds {
			d, err := tx.Documents(kind)
			if err != nil {
				return err
			}
			docs += int64(len(d))
			if kind == content.RefEntry {
				entries = d
			}
		}
		for _, e := range entries {
			rs, err := tx.Revisions(e.ID)
			if err != nil {
				return err
			}
			if len(rs) == 0 {
				return fmt.Errorf("%s has no revisions", e.ID)
			}
			revs += int64(len(rs))
			rev, err := tx.Revision(rs[len(rs)-1].ID)
			if err != nil {
				return err
			}
			data += int64(len(rev.Data))
		}
		return nil
	})
	d := time.Since(start)
	if err != nil {
		t.Fatalf("View() = %v", err)
	}
	res.Found["build documents"], res.Found["build revisions"], res.Found["build bytes"] = docs, revs, data
	return int64(d)
}

// benchOpen measures how long the store of the job takes to open, and then to read a document: with the checkpoint
// (open-clean), without it (open-replay: benchOpen removes it), or after the process of the scenario loop has been
// killed (open-killed).
func benchOpen(t *testing.T, tb testBackend, res *benchResult) {
	dir := res.Job.Dir
	if res.Job.Scenario == "open-replay" {
		if err := os.Remove(filepath.Join(dir, "logdb", "checkpoint")); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	s, c := benchStore(t, tb, dir, false)
	res.Durations["open"] = int64(time.Since(start))
	start = time.Now()
	view(t, s, func(tx *Tx) error {
		_, err := tx.Document(content.RefEntry, benchID("ent_", 1))
		return err
	})
	res.Durations["first read"] = int64(time.Since(start))
	info := logDB(t, s).Recovery()
	res.Recovery = &info
	res.Heap = append(res.Heap, benchHeapAt("open"))
	res.Found, res.Stats = benchCount(t, s)
	res.Durations["close"] = benchClose(t, s, c)
	res.count(c)
}

// benchLoop opens the store in dir, and makes single saves until the process is killed. It prints "progress" after
// benchLoopSaves of them.
func benchLoop(t *testing.T, bs benchSet, tb testBackend, dir string) {
	s, _ := benchStore(t, tb, dir, false)
	for n := 1; n <= 100*benchLoopSaves; n++ {
		benchUpdate(t, s, bs.single(benchStreamLoop, n))
		if n == benchLoopSaves {
			fmt.Println(marker + "progress")
		}
	}
	t.Fatal("not killed")
}

// benchCheckpoint opens the store of the job, makes a single save, and measures a checkpoint of the whole state.
func benchCheckpoint(t *testing.T, bs benchSet, tb testBackend, res *benchResult) {
	s, c := benchStore(t, tb, res.Job.Dir, false)
	benchUpdate(t, s, bs.single(benchStreamCheckpoint, 1))
	before := c.Counts()
	start := time.Now()
	err := logDB(t, s).Checkpoint()
	res.Durations["checkpoint"] = int64(time.Since(start))
	if err != nil {
		t.Fatal(err)
	}
	after := c.Counts()
	res.CheckpointCounts = &vfs.Counts{
		Writes: after.Writes - before.Writes, BytesWritten: after.BytesWritten - before.BytesWritten,
		Syncs: after.Syncs - before.Syncs, DirSyncs: after.DirSyncs - before.DirSyncs,
		Renames: after.Renames - before.Renames, Removes: after.Removes - before.Removes,
		Mkdirs: after.Mkdirs - before.Mkdirs,
	}
	if res.CheckpointCounts.Renames != 1 {
		t.Fatalf("Checkpoint() has renamed %d files, want the checkpoint", res.CheckpointCounts.Renames)
	}
	info, err := os.Stat(filepath.Join(res.Job.Dir, "logdb", "checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	res.CheckpointBytes = info.Size()
	res.Durations["close"] = benchClose(t, s, c)
	res.count(c)
}

// benchBackup measures backupLog of the data directory of the job, which no process has open, to a new directory. Then
// it checks that the copy opens with what the store has.
func benchBackup(t *testing.T, tb testBackend, res *benchResult) {
	dir := res.Job.Dir
	dst := filepath.Join(filepath.Dir(dir), "backup")
	start := time.Now()
	err := backupLog(t.Context(), dir, dst)
	res.Durations["backup"] = int64(time.Since(start))
	if err != nil {
		t.Fatal(err)
	}
	res.BackupDisk, res.Disk = benchDiskOf(t, dst), benchDiskOf(t, dir)
	res.end(t)
	var counts [2]map[string]int64
	for i, d := range []string{dir, dst} {
		s, c := benchStore(t, tb, d, false)
		counts[i], _ = benchCount(t, s)
		benchClose(t, s, c)
	}
	if !maps.Equal(counts[0], counts[1]) {
		t.Fatalf("the copy has %v, want %v", counts[1], counts[0])
	}
	res.Found = counts[1]
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
}

// benchDiskOf returns what dir takes on the disk.
func benchDiskOf(t *testing.T, dir string) *benchDisk {
	t.Helper()
	var d benchDisk
	if err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("%s: no blocks", path)
		}
		d.Allocated += st.Blocks * 512
		switch {
		case e.Type().IsRegular():
			d.Files++
			d.Bytes += info.Size()
		case e.IsDir():
			d.Dirs++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &d
}

// TestBenchmarks runs the benchmarks of 3.5 if envBench names a directory for the results: for each data set, run and
// backend, each scenario in a process of TestBenchHelper, on a data directory under envBenchDir that it removes after
// the run. It writes to the directory raw.jsonl (every result), results.json and results.md (the median, the smallest
// and the largest of the runs of each number) and env.json (the machine, the settings and the data sets).
func TestBenchmarks(t *testing.T) {
	out := os.Getenv(envBench)
	if out == "" {
		t.Skip("the benchmarks of 3.5, which write their results to the directory " + envBench)
	}
	if raceEnabled {
		t.Fatal("the benchmarks need a test binary without -race")
	}
	sets, runs := benchSets, benchRuns
	if os.Getenv(envBenchSmoke) == "1" {
		sets, runs = []benchSet{benchSmoke}, 1
	}
	base := cmp.Or(os.Getenv(envBenchDir), os.TempDir())
	for _, dir := range []string{out, base} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	work, err := os.MkdirTemp(base, "whiteroom-bench-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(work); err != nil {
			t.Error(err)
		}
	})
	started := time.Now()
	env := benchEnvironment(t, work, runs)
	// The smoke run checks the saves of the full run too.
	for _, bs := range append(slices.Clip(sets), benchSets...) {
		if _, ok := env.Sets[bs.name]; !ok {
			env.Sets[bs.name] = benchFactsOf(t, bs)
		}
	}
	env.Started = started.Format(time.RFC3339)
	benchWriteJSON(t, filepath.Join(out, "env.json"), env)
	raw, err := os.OpenFile(filepath.Join(out, "raw.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	var results []benchResult
	defer func() {
		if err := raw.Close(); err != nil {
			t.Error(err)
		}
		env.Finished, env.Seconds = time.Now().Format(time.RFC3339), time.Since(started).Seconds()
		benchWriteJSON(t, filepath.Join(out, "env.json"), env)
		tables := benchSummary(results, sets)
		benchWriteJSON(t, filepath.Join(out, "results.json"), map[string]any{"Runs": runs, "Tables": tables})
		md := benchMarkdown(env, runs, tables)
		if err := os.WriteFile(filepath.Join(out, "results.md"), []byte(md), 0o600); err != nil {
			t.Error(err)
		}
	}()
	for _, bs := range sets {
		for run := 1; run <= runs; run++ {
			for _, tb := range benchBackends {
				runDir := filepath.Join(work, fmt.Sprintf("%s-%d-%s", bs.name, run, tb.name))
				if err := os.Mkdir(runDir, 0o700); err != nil {
					t.Fatal(err)
				}
				job := benchJob{Set: bs.name, Backend: tb.name, Run: run, Dir: filepath.Join(runDir, "data")}
				for _, scenario := range benchScenarios {
					if scenario == "open-killed" {
						loop := job
						loop.Scenario, loop.Result = "loop", ""
						kill(t, startProcess(t, benchCommand(t, loop), loop.Scenario, "progress", benchLimit))
					}
					job.Scenario, job.Result = scenario, filepath.Join(runDir, scenario+".json")
					res := benchRun(t, job)
					results = append(results, res)
					line, err := json.Marshal(res)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := raw.Write(append(line, '\n')); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.RemoveAll(runDir); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// benchCommand returns the command of a process of TestBenchHelper with job.
func benchCommand(t *testing.T, job benchJob) *exec.Cmd {
	t.Helper()
	spec, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBenchHelper$")
	cmd.Env = append(os.Environ(), envBenchHelper+"="+string(spec))
	return cmd
}

// benchRun runs job in a process of TestBenchHelper, and returns its result.
func benchRun(t *testing.T, job benchJob) benchResult {
	t.Helper()
	start := time.Now()
	h := startProcess(t, benchCommand(t, job), job.Scenario, "done", benchLimit)
	if err := h.wait(); err != nil {
		t.Fatalf("the helper of %+v has ended with %v", job, err)
	}
	data, err := os.ReadFile(job.Result)
	if err != nil {
		t.Fatal(err)
	}
	var res benchResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	res.Seconds = time.Since(start).Seconds()
	t.Logf("%s %s, run %d, %s, %s: %.1f s", time.Now().Format(time.TimeOnly), job.Set, job.Run, job.Backend,
		job.Scenario, res.Seconds)
	return res
}

func benchWriteJSON(t *testing.T, name string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// benchFacts are the counts of a data set, and the sizes of its JSON by kind: their sum, the smallest and the largest.
type benchFacts struct {
	Documents, Versions, Revisions, Events, Updates int
	Bytes                                           map[string]int64
	Min, Max                                        map[string]int
	LargeRevisions                                  int // of 64 KiB or more, which Hybrid puts in files
	RevisionMedian, RevisionP90                     int
}

// benchFactsOf makes every save of the load of bs, checks that the store takes it, as Put and AddRevision check it, and
// that it has the sizes of benchSizes and benchRevisionSize, and returns the facts of bs.
func benchFactsOf(t *testing.T, bs benchSet) benchFacts {
	t.Helper()
	f := benchFacts{Documents: bs.documents(), Bytes: map[string]int64{}, Min: map[string]int{}, Max: map[string]int{}}
	add := func(what string, n int) {
		if m, ok := f.Min[what]; !ok || n < m {
			f.Min[what] = n
		}
		f.Max[what] = max(f.Max[what], n)
		f.Bytes[what] += int64(n)
		if s, ok := benchSizes[what]; ok && (n < s[0] || n > s[1]) {
			t.Fatalf("%s: a JSON of %s has %d bytes, want %d to %d", bs.name, what, n, s[0], s[1])
		}
	}
	var sizes []int
	for i := range bs.stages() {
		refs := bs.stage(i)
		f.Updates += (len(refs) + bs.batch - 1) / bs.batch
		for _, ref := range refs {
			sv := bs.loadSave(ref)
			if _, err := checkData(sv.kind, sv.id, sv.doc); err != nil {
				t.Fatalf("%s: Put() of %s would fail: %v", bs.name, sv.id, err)
			}
			add(string(sv.kind), len(sv.doc))
			f.Versions++
			if sv.rev != nil {
				file := content.SiteFile{Name: revisionName(sv.revID), Data: sv.rev}
				if err := decodeFile(file, new(content.Revision).UnmarshalJSON); err != nil {
					t.Fatalf("%s: AddRevision() of %s would fail: %v", bs.name, sv.revID, err)
				}
				n := len(sv.rev)
				if (n < benchSmall[0] || n > benchSmall[1]) && (n < benchLarge[0] || n > benchLarge[1]) {
					t.Fatalf("%s: the revision %s has %d bytes", bs.name, sv.revID, n)
				}
				sizes = append(sizes, n)
			}
			for _, e := range sv.events {
				add("event", len(e))
				f.Events++
			}
		}
	}
	for _, n := range sizes {
		add("revision", n)
		if n >= 64<<10 {
			f.LargeRevisions++
		}
	}
	f.Revisions = len(sizes)
	if f.Versions != bs.saves() || f.Revisions != bs.revisions() || f.Events != bs.saves()*bs.events {
		t.Fatalf("%s has %d versions, %d revisions and %d events, want %d, %d and %d", bs.name, f.Versions,
			f.Revisions, f.Events, bs.saves(), bs.revisions(), bs.saves()*bs.events)
	}
	slices.Sort(sizes)
	f.RevisionMedian, f.RevisionP90 = benchRank(sizes, 0.5), benchRank(sizes, 0.9)
	return f
}

// benchRank returns the q-quantile of sorted by the nearest rank: the ceil(q*n)-th smallest of its n values.
func benchRank[T cmp.Ordered](sorted []T, q float64) T {
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

// benchEnv is env.json: the machine, the settings and the data sets of the benchmarks.
type benchEnv struct {
	Started, Finished string
	Seconds           float64
	Args              []string
	Go, OS, Arch      string
	Kernel, CPU       string
	CPUs, GOMAXPROCS  int
	MemoryBytes       int64
	Vars              map[string]string // of the environment that the Go runtime reads
	WorkDir           string
	Mount             benchMount // the file system of WorkDir, from /proc/mounts
	DiskModel         string
	Modules           map[string]string
	Settings          benchSettings
	Sets              map[string]benchFacts
}

type benchMount struct {
	Point, Device, Type, Options string
}

// benchSettings are the settings of the benchmarks.
type benchSettings struct {
	Seed, Runs                              int
	Backends                                map[string]*benchOptions
	EngineDefaults                          string
	Sets                                    []benchSetting
	Scenarios                               []string
	Singles, Reads, Lists, LoopSaves        int
	Limit                                   string
	Sizes                                   map[string][2]int
	SmallRevisions, LargeRevisions          [2]int
	LargeShare, RevisionMedian, RevisionP90 string
	ParagraphText                           [2]int
}

// benchOptions are the options of the engine of a backend, which the store completes with its Hook and Accept.
type benchOptions struct {
	Placement                     string
	Threshold                     int
	SegmentBytes, CheckpointBytes int64
	SyncCheckpoints               bool
	WriterWait                    string
}

type benchSetting struct {
	Name                                  string
	Entries, Terms, Media, Second, Rounds int
	EventsPerSave, SavesPerUpdate         int
	Stages                                []string
}

// benchEnvironment returns what env.json has of the machine and the settings, with the data directories in work.
func benchEnvironment(t *testing.T, work string, runs int) *benchEnv {
	t.Helper()
	abs, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	env := &benchEnv{Args: os.Args, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Kernel: benchFirst("/proc/sys/kernel/osrelease", ""), CPU: benchFirst("/proc/cpuinfo", "model name"),
		CPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), Vars: map[string]string{}, WorkDir: abs,
		Mount: benchMountOf(abs), Modules: map[string]string{}, Sets: map[string]benchFacts{}}
	memory := strings.TrimSuffix(benchFirst("/proc/meminfo", "MemTotal"), " kB")
	if kb, err := strconv.ParseInt(memory, 10, 64); err == nil {
		env.MemoryBytes = kb << 10
	}
	for _, name := range []string{"GOGC", "GOMEMLIMIT", "GODEBUG", "GOMAXPROCS", "GOFLAGS"} {
		env.Vars[name] = os.Getenv(name)
	}
	env.DiskModel = benchDiskModel(env.Mount.Device)
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, m := range info.Deps {
			env.Modules[m.Path] = m.Version
		}
	}
	s := benchSettings{Seed: benchSeed, Runs: runs, Backends: map[string]*benchOptions{},
		EngineDefaults: "Threshold 0: 64 KiB; SegmentBytes 0: 64 MiB; CheckpointBytes 0: 64 MiB; WriterWait 0: 2 s",
		Scenarios:      benchScenarios, Singles: benchSingles, Reads: benchReads, Lists: benchLists,
		LoopSaves: benchLoopSaves, Limit: benchLimit.String(), Sizes: benchSizes, SmallRevisions: benchSmall,
		LargeRevisions: benchLarge, LargeShare: "1%", RevisionMedian: strconv.Itoa(benchMedian) + " bytes",
		RevisionP90: "4 times the median (log-normal)", ParagraphText: [2]int{200, 1000}}
	for _, tb := range benchBackends {
		o := tb.log
		s.Backends[tb.name] = &benchOptions{Placement: strings.TrimPrefix(tb.name, "log-"), Threshold: o.Threshold,
			SegmentBytes: o.SegmentBytes, CheckpointBytes: o.CheckpointBytes, SyncCheckpoints: o.SyncCheckpoints,
			WriterWait: o.WriterWait.String()}
	}
	for _, bs := range append(slices.Clip(benchSets), benchSmoke) {
		s.Sets = append(s.Sets, benchSetting{Name: bs.name, Entries: bs.entries, Terms: bs.terms, Media: bs.media,
			Second: bs.second, Rounds: bs.rounds, EventsPerSave: bs.events, SavesPerUpdate: bs.batch,
			Stages: bs.stages()})
	}
	env.Settings = s
	return env
}

// benchFirst returns the first line of the file name if key is "", or else the value of the first line of key,
// trimmed; "" if there is none.
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

// benchMountOf returns the mount of /proc/mounts that has path, an absolute path without symbolic links.
func benchMountOf(path string) benchMount {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return benchMount{}
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	var best benchMount
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		point := unescape.Replace(f[1])
		inside := point == "/" || path == point || strings.HasPrefix(path, point+"/")
		if inside && len(point) >= len(best.Point) {
			best = benchMount{Point: point, Device: f[0], Type: f[2], Options: f[3]}
		}
	}
	return best
}

// benchDiskModel returns the model of the disk of the block device dev, such as /dev/nvme0n1p2, from /sys; "" if it
// cannot tell.
func benchDiskModel(dev string) string {
	sys, err := filepath.EvalSymlinks(filepath.Join("/sys/class/block", filepath.Base(dev)))
	if err != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(sys, "partition")); err == nil {
		sys = filepath.Dir(sys)
	}
	return benchFirst(filepath.Join(sys, "device", "model"), "")
}

// benchMetric is a row of the tables of the results: a number of the results of a scenario, in unit ("" for a count),
// which value returns if the result has it.
type benchMetric struct {
	name, unit, scenario string
	value                func(bs benchSet, r benchResult) (float64, bool)
}

func benchDuration(key string, scale float64) func(benchSet, benchResult) (float64, bool) {
	return func(_ benchSet, r benchResult) (float64, bool) {
		d, ok := r.Durations[key]
		return float64(d) / scale, ok
	}
}

func benchQuantile(key string, q, scale float64) func(benchSet, benchResult) (float64, bool) {
	return func(_ benchSet, r benchResult) (float64, bool) {
		l := r.Latencies[key]
		if len(l) == 0 {
			return 0, false
		}
		return float64(benchRank(slices.Sorted(slices.Values(l)), q)) / scale, true
	}
}

func benchOf(f func(r benchResult) (float64, bool)) func(benchSet, benchResult) (float64, bool) {
	return func(_ benchSet, r benchResult) (float64, bool) { return f(r) }
}

func benchCounted(f func(c vfs.Counts) int64, scale float64) func(benchSet, benchResult) (float64, bool) {
	return benchOf(func(r benchResult) (float64, bool) {
		if r.Counts == nil {
			return 0, false
		}
		return float64(f(*r.Counts)) / scale, true
	})
}

func benchStat(f func(s logdb.Stats) float64) func(benchSet, benchResult) (float64, bool) {
	return benchOf(func(r benchResult) (float64, bool) {
		if r.Stats == nil {
			return 0, false
		}
		return f(*r.Stats), true
	})
}

func benchHeapOf(stage string) func(benchSet, benchResult) (float64, bool) {
	return benchOf(func(r benchResult) (float64, bool) {
		for _, h := range r.Heap {
			if h.Stage == stage {
				return float64(h.HeapAlloc) / mib, true
			}
		}
		return 0, false
	})
}

var (
	benchRSS     = benchOf(func(r benchResult) (float64, bool) { return float64(r.MaxRSS) / mib, true })
	benchWritten = benchOf(func(r benchResult) (float64, bool) { return float64(r.WriteBytes) / mib, r.WriteBytes >= 0 })
	benchReplay  = benchOf(func(r benchResult) (float64, bool) {
		if r.Recovery == nil {
			return 0, false
		}
		return float64(r.Recovery.Replayed), true
	})
)

// benchMetrics returns the rows of the table of bs.
func benchMetrics(bs benchSet) []benchMetric {
	disk := func(f func(d benchDisk) int64, scale float64) func(benchSet, benchResult) (float64, bool) {
		return benchOf(func(r benchResult) (float64, bool) {
			if r.Disk == nil {
				return 0, false
			}
			return float64(f(*r.Disk)) / scale, true
		})
	}
	m := []benchMetric{
		{"load", "s", "load", benchDuration("load", 1e9)},
		{"Close after the load", "ms", "load", benchDuration("close", 1e6)},
		{"write_bytes of the load", "MiB", "load", benchWritten},
		{"vfs writes of the load", "", "load", benchCounted(func(c vfs.Counts) int64 { return c.Writes }, 1)},
		{"vfs bytes written by the load", "MiB", "load",
			benchCounted(func(c vfs.Counts) int64 { return c.BytesWritten }, mib)},
		{"vfs syncs of files by the load", "", "load", benchCounted(func(c vfs.Counts) int64 { return c.Syncs }, 1)},
		{"vfs syncs of directories by the load", "", "load",
			benchCounted(func(c vfs.Counts) int64 { return c.DirSyncs }, 1)},
		{"allocated after the load", "MiB", "load", disk(func(d benchDisk) int64 { return d.Allocated }, mib)},
		{"size of the files after the load", "MiB", "load", disk(func(d benchDisk) int64 { return d.Bytes }, mib)},
		{"files after the load", "", "load", disk(func(d benchDisk) int64 { return int64(d.Files) }, 1)},
		{"max RSS of the load", "MiB", "load", benchRSS},
	}
	for _, stage := range bs.stages() {
		m = append(m, benchMetric{"heap after GC after the " + stage, "MiB", "load", benchHeapOf(stage)})
	}
	return append(m, []benchMetric{
		{"log of the engine after the load", "MiB", "load",
			benchStat(func(s logdb.Stats) float64 { return float64(s.LogBytes) / mib })},
		{"segments after the load", "", "load", benchStat(func(s logdb.Stats) float64 { return float64(s.Segments) })},
		{"inline objects after the load", "", "load",
			benchStat(func(s logdb.Stats) float64 { return float64(s.InlineObjects) })},
		{"file objects after the load", "", "load",
			benchStat(func(s logdb.Stats) float64 { return float64(s.FileObjects) })},
		{"failed automatic checkpoints of the load", "", "load",
			benchStat(func(s logdb.Stats) float64 { return float64(s.CheckpointErrors) })},
		{"save p50", "ms", "ops", benchQuantile("save", 0.5, 1e6)},
		{"save p99", "ms", "ops", benchQuantile("save", 0.99, 1e6)},
		{"Document p50", "us", "ops", benchQuantile("Document", 0.5, 1e3)},
		{"Document p99", "us", "ops", benchQuantile("Document", 0.99, 1e3)},
		{"History p50", "us", "ops", benchQuantile("History", 0.5, 1e3)},
		{"History p99", "us", "ops", benchQuantile("History", 0.99, 1e3)},
		{"Revisions p50", "us", "ops", benchQuantile("Revisions", 0.5, 1e3)},
		{"Revisions p99", "us", "ops", benchQuantile("Revisions", 0.99, 1e3)},
		{"Documents of the entries p50", "ms", "ops", benchQuantile("Documents", 0.5, 1e6)},
		{"Documents of the entries max", "ms", "ops", benchQuantile("Documents", 1, 1e6)},
		{"build-like read", "ms", "ops", benchDuration("build", 1e6)},
		{"write_bytes of ops", "MiB", "ops", benchWritten},
		{"vfs syncs of files by ops", "", "ops", benchCounted(func(c vfs.Counts) int64 { return c.Syncs }, 1)},
		{"max RSS of ops", "MiB", "ops", benchRSS},
		{"open after Close", "ms", "open-clean", benchDuration("open", 1e6)},
		{"first read after it", "ms", "open-clean", benchDuration("first read", 1e6)},
		{"heap after GC after it", "MiB", "open-clean", benchHeapOf("open")},
		{"max RSS of open after Close", "MiB", "open-clean", benchRSS},
		{"open with a full replay", "ms", "open-replay", benchDuration("open", 1e6)},
		{"records replayed by it", "", "open-replay", benchReplay},
		{"max RSS of open with a full replay", "MiB", "open-replay", benchRSS},
		{"open after SIGKILL", "ms", "open-killed", benchDuration("open", 1e6)},
		{"records replayed by it", "", "open-killed", benchReplay},
		{"saves of the killed process kept", "", "open-killed", func(bs benchSet, r benchResult) (float64, bool) {
			n, ok := r.Found["versions"]
			return float64(n - int64(bs.saves()+benchSingles)), ok
		}},
		{"checkpoint", "ms", "checkpoint", benchDuration("checkpoint", 1e6)},
		{"checkpoint file", "MiB", "checkpoint",
			benchOf(func(r benchResult) (float64, bool) { return float64(r.CheckpointBytes) / mib, true })},
		{"backup", "s", "backup", benchDuration("backup", 1e9)},
		{"allocated by the backup", "MiB", "backup", benchOf(func(r benchResult) (float64, bool) {
			if r.BackupDisk == nil {
				return 0, false
			}
			return float64(r.BackupDisk.Allocated) / mib, true
		})},
		{"write_bytes of the backup", "MiB", "backup", benchWritten},
		{"max RSS of the backup", "MiB", "backup", benchRSS},
	}...)
}

// benchCell is the median, the smallest and the largest of the values of a number in the runs, which it has in the
// order of the runs.
type benchCell struct {
	Median, Min, Max float64
	Values           []float64
}

type benchRow struct {
	Metric, Unit, Scenario string
	Cells                  map[string]*benchCell // by backend
}

type benchTable struct {
	Set  string
	Rows []benchRow
}

// benchSummary returns the table of each set of results.
func benchSummary(results []benchResult, sets []benchSet) []benchTable {
	var tables []benchTable
	for _, bs := range sets {
		table := benchTable{Set: bs.name}
		for _, m := range benchMetrics(bs) {
			row := benchRow{Metric: m.name, Unit: m.unit, Scenario: m.scenario, Cells: map[string]*benchCell{}}
			for _, tb := range benchBackends {
				var values []float64
				for _, r := range results {
					if r.Job.Set != bs.name || r.Job.Backend != tb.name || r.Job.Scenario != m.scenario {
						continue
					}
					if v, ok := m.value(bs, r); ok {
						values = append(values, v)
					}
				}
				if len(values) == 0 {
					continue
				}
				sorted := slices.Sorted(slices.Values(values))
				median := sorted[len(sorted)/2]
				if len(sorted)%2 == 0 {
					median = (sorted[len(sorted)/2-1] + median) / 2
				}
				row.Cells[tb.name] = &benchCell{Median: median, Min: sorted[0], Max: sorted[len(sorted)-1],
					Values: values}
			}
			if len(row.Cells) > 0 {
				table.Rows = append(table.Rows, row)
			}
		}
		tables = append(tables, table)
	}
	return tables
}

// benchMarkdown returns results.md: a table of each set, with a column for each backend.
func benchMarkdown(env *benchEnv, runs int, tables []benchTable) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmarks of 3.5\n\nStarted %s, %d run(s) of each cell", env.Started, runs)
	if runs > 1 {
		b.WriteString(": the median, and in parentheses the smallest and the largest")
	}
	fmt.Fprintf(&b, ". %s, Linux %s, %s (%d CPUs), %s on %s (%s).\n", env.Go, env.Kernel, env.CPU, env.CPUs,
		env.Mount.Type, env.Mount.Device, env.DiskModel)
	for _, table := range tables {
		f := env.Sets[table.Set]
		fmt.Fprintf(&b, "\n## %s\n\n%d documents, %d versions, %d revisions (%d of 64 KiB or more), %d events, "+
			"%d Updates.\n\n| | unit |", table.Set, f.Documents, f.Versions, f.Revisions, f.LargeRevisions, f.Events,
			f.Updates)
		for _, tb := range benchBackends {
			b.WriteString(" " + tb.name + " |")
		}
		b.WriteString("\n| --- | --- |" + strings.Repeat(" ---: |", len(benchBackends)) + "\n")
		for _, row := range table.Rows {
			fmt.Fprintf(&b, "| %s | %s |", row.Metric, row.Unit)
			for _, tb := range benchBackends {
				c, ok := row.Cells[tb.name]
				switch {
				case !ok:
					b.WriteString(" - |")
				case runs > 1:
					fmt.Fprintf(&b, " %s (%s-%s) |", benchFormat(c.Median, row.Unit), benchFormat(c.Min, row.Unit),
						benchFormat(c.Max, row.Unit))
				default:
					fmt.Fprintf(&b, " %s |", benchFormat(c.Median, row.Unit))
				}
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// benchFormat formats v in unit: a count whole, anything else with 3 significant digits.
func benchFormat(v float64, unit string) string {
	if unit == "" || v == 0 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', max(0, 2-int(math.Floor(math.Log10(math.Abs(v))))), 64)
}
