//go:build linux

package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	envBench       = "WHITEROOM_BUILD_BENCH"        // the directory of the results
	envBenchHelper = "WHITEROOM_BUILD_BENCH_HELPER" // the job of TestBenchHelper, in JSON
)

const (
	benchRuns  = 3                // of each size to measure; one more run writes the heap profiles
	benchProcs = 4                // GOMAXPROCS of a helper, and so the workers of its build
	benchRate  = 4096             // the memory profile rate of the run that writes the heap profiles
	benchLimit = 30 * time.Minute // a helper that runs longer is killed, and the benchmark fails
	mib        = 1 << 20
)

// benchPosts are the numbers of the posts that the sites have more than the sample site.
var benchPosts = []int{0, 1000, 5000, 20000}

// benchJob is what a process of TestBenchHelper does.
type benchJob struct {
	Posts                    int
	Site, Out, Cache, Result string // the directories of the site, the output and the cache, and the file of the result
	Profile                  string // where to write the heap profile of each stage; "" in a run that measures
}

// benchResult is what a process has measured. Seconds is the time of Build. Heap, in a run that writes the heap
// profiles, is the live heap after a GC at the end of each stage; the time and the memory of that run are not measured.
type benchResult struct {
	Job                    benchJob
	Release                string
	Pages, Files, Warnings int
	Seconds                float64
	CPU                    float64 // the user and the system time of the process
	MaxRSS                 int64   // the largest resident set of the process at the end of Build, in bytes
	Heap                   []benchHeap
}

type benchHeap struct {
	Stage string
	Live  uint64
}

// TestBenchHelper is the process of a run of TestBenchmarks, which builds the site of the job and writes the result to
// the file of the job. On its own, it does nothing.
func TestBenchHelper(t *testing.T) {
	spec := os.Getenv(envBenchHelper)
	if spec == "" {
		t.Skip("run by TestBenchmarks")
	}
	var job benchJob
	if err := json.Unmarshal([]byte(spec), &job); err != nil {
		t.Fatal(err)
	}
	res := benchResult{Job: job}
	if job.Profile != "" {
		runtime.MemProfileRate = benchRate
		if err := os.MkdirAll(job.Profile, 0o755); err != nil {
			t.Fatal(err)
		}
		afterStage = func(stage string) {
			res.Heap = append(res.Heap, benchHeap{stage, liveHeap()})
			benchWriteProfile(t, filepath.Join(job.Profile, stage+".pb.gz"))
		}
		t.Cleanup(func() { afterStage = nil })
	}
	src := openLargeSite(t, job.Site)
	start := time.Now()
	built, err := Build(t.Context(), Options{Source: src, Out: job.Out, CacheDir: job.Cache})
	res.Seconds = time.Since(start).Seconds()
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	res.Release, res.Pages, res.Files, res.Warnings = built.Release, built.Pages, built.Files, len(built.Warnings)
	res.MaxRSS = benchMaxRSS(t)
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job.Result, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// benchWriteProfile writes the heap profile, as of the last GC, to the file name.
func benchWriteProfile(t *testing.T, name string) {
	t.Helper()
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
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

// TestBenchmarks runs the benchmarks of the build if envBench names a directory for the results. For each of
// benchPosts, it writes a site into a temporary directory, which should be on a real disk, and builds it in benchRuns
// processes of TestBenchHelper to measure the time and the largest resident set, and in one more to write the heap
// profiles; each build has an empty output directory and an empty cache directory. It writes raw.jsonl (every result),
// results.md (the median, the smallest and the largest of the runs) and heap-<posts>/<stage>.pb.gz to the results.
func TestBenchmarks(t *testing.T) {
	out := os.Getenv(envBench)
	if out == "" {
		t.Skip("the benchmarks of the build, which write their results to the directory " + envBench)
	}
	if raceEnabled {
		t.Fatal("the benchmarks need a test binary without -race")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.Create(filepath.Join(out, "raw.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := raw.Close(); err != nil {
			t.Error(err)
		}
	}()

	started := time.Now()
	var results []benchResult
	for _, n := range benchPosts {
		site := filepath.Join(work, "site-"+strconv.Itoa(n))
		writeLargeSite(t, site, n)
		for run := 1; run <= benchRuns+1; run++ {
			dir := filepath.Join(work, fmt.Sprintf("run-%d-%d", n, run))
			job := benchJob{Posts: n, Site: site, Out: filepath.Join(dir, "out"), Cache: filepath.Join(dir, "cache"),
				Result: dir + ".json"}
			if run > benchRuns {
				job.Profile = filepath.Join(out, "heap-"+strconv.Itoa(n))
				if err := os.RemoveAll(job.Profile); err != nil {
					t.Fatal(err)
				}
			}
			res := benchRun(t, job)
			results = append(results, res)
			line, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Write(append(line, '\n')); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.RemoveAll(site); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range benchPosts {
		rs := benchRunsOf(results, n, false)
		for _, r := range rs {
			if r.Release != rs[0].Release || r.Pages != rs[0].Pages || r.Files != rs[0].Files ||
				r.Warnings != rs[0].Warnings {
				t.Errorf("the runs of %d posts differ: %s with %d pages, %d files and %d warnings, and %s with %d, %d and %d",
					n, rs[0].Release, rs[0].Pages, rs[0].Files, rs[0].Warnings, r.Release, r.Pages, r.Files, r.Warnings)
			}
		}
		if want := largePages(n); rs[0].Pages != want {
			t.Errorf("the build of %d posts wrote %d pages, want %d", n, rs[0].Pages, want)
		}
	}
	md := benchMarkdown(results, work, started, time.Since(started))
	if err := os.WriteFile(filepath.Join(out, "results.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// benchRun runs job in a process of TestBenchHelper, and returns its result.
func benchRun(t *testing.T, job benchJob) benchResult {
	t.Helper()
	spec, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), benchLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBenchHelper$")
	cmd.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(benchProcs), envBenchHelper+"="+string(spec))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the helper of %+v has ended with %v:\n%s", job, err, out)
	}
	data, err := os.ReadFile(job.Result)
	if err != nil {
		t.Fatal(err)
	}
	var res benchResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(job.Result); err != nil {
		t.Fatal(err)
	}
	res.CPU = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Seconds()
	what := "build"
	if job.Profile != "" {
		what = "heap profiles"
	}
	t.Logf("%s %d posts, %s: %.2f s, %.0f MiB", time.Now().Format(time.TimeOnly), job.Posts, what, res.Seconds,
		float64(res.MaxRSS)/mib)
	return res
}

// benchRunsOf returns the results of the runs with posts more posts that write the heap profiles if profile is true,
// else of those that measure.
func benchRunsOf(results []benchResult, posts int, profile bool) []benchResult {
	var rs []benchResult
	for _, r := range results {
		if r.Job.Posts == posts && (r.Job.Profile != "") == profile {
			rs = append(rs, r)
		}
	}
	return rs
}

// benchCell returns the median, the smallest and the largest of values, each written with format.
func benchCell(values []float64, format string) string {
	s := slices.Sorted(slices.Values(values))
	median := s[len(s)/2]
	if len(s)%2 == 0 {
		median = (s[len(s)/2-1] + median) / 2
	}
	return fmt.Sprintf(format+" ("+format+"-"+format+")", median, s[0], s[len(s)-1])
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

// benchMarkdown returns results.md: the machine and the settings, a table of the runs that measure, and a table of the
// live heap at the end of each stage, by the number of posts.
func benchMarkdown(results []benchResult, work string, started time.Time, elapsed time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmarks of the memory of the build\n\n"+
		"The sample site with more posts (largeSource), built by Build with Workers 0 in a process of TestBenchHelper, "+
		"with an empty output directory and an empty cache directory. Started %s; %.0f s. "+
		"%d runs of each size: the median, and in parentheses the smallest and the largest.\n\n",
		started.Format(time.RFC3339), elapsed.Seconds(), benchRuns)
	memory, _ := strconv.ParseInt(strings.TrimSuffix(benchFirst("/proc/meminfo", "MemTotal"), " kB"), 10, 64)
	fmt.Fprintf(&b, "- %s %s/%s\n- %s, %d CPUs, %d MiB of memory, Linux %s\n- Work directory %s (%s)\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, benchFirst("/proc/cpuinfo", "model name"), runtime.NumCPU(),
		memory>>10, benchFirst("/proc/sys/kernel/osrelease", ""), work, benchMount(work))
	b.WriteString("- Settings of the runtime of a build:")
	for _, name := range []string{"GOGC", "GOMEMLIMIT", "GODEBUG"} {
		v, ok := os.LookupEnv(name)
		fmt.Fprintf(&b, " %s=%q", name, v)
		if !ok {
			b.WriteString(" (unset)")
		}
	}
	fmt.Fprintf(&b, " GOMAXPROCS=%d\n\n", benchProcs)

	b.WriteString("| posts | pages | files | warnings | release | time (s) | CPU (s) | max RSS (MiB) |\n")
	b.WriteString("| ---: | ---: | ---: | ---: | --- | ---: | ---: | ---: |\n")
	for _, n := range benchPosts {
		rs := benchRunsOf(results, n, false)
		var seconds, cpu, rss []float64
		for _, r := range rs {
			seconds, cpu, rss = append(seconds, r.Seconds), append(cpu, r.CPU), append(rss, float64(r.MaxRSS)/mib)
		}
		fmt.Fprintf(&b, "| %d | %d | %d | %d | %s | %s | %s | %s |\n", n, rs[0].Pages, rs[0].Files, rs[0].Warnings,
			rs[0].Release, benchCell(seconds, "%.2f"), benchCell(cpu, "%.2f"), benchCell(rss, "%.1f"))
	}

	b.WriteString("\n## The live heap after a GC at the end of each stage (MiB)\n\n" +
		"From the run of each size that writes the heap profiles, heap-<posts>/<stage>.pb.gz (inuse_space, with a " +
		"memory profile rate of " + strconv.Itoa(benchRate) + " bytes). The last column is the growth for each post " +
		"between the two largest sizes, in KiB.\n\n| stage |")
	for _, n := range benchPosts {
		fmt.Fprintf(&b, " %d |", n)
	}
	b.WriteString(" KiB for each post |\n| --- |" + strings.Repeat(" ---: |", len(benchPosts)+1) + "\n")
	live := func(n int, stage string) (float64, bool) {
		for _, r := range benchRunsOf(results, n, true) {
			for _, h := range r.Heap {
				if h.Stage == stage {
					return float64(h.Live), true
				}
			}
		}
		return 0, false
	}
	lo, hi := benchPosts[len(benchPosts)-2], benchPosts[len(benchPosts)-1]
	for _, stage := range buildStages {
		fmt.Fprintf(&b, "| %s |", stage)
		for _, n := range benchPosts {
			if v, ok := live(n, stage); ok {
				fmt.Fprintf(&b, " %.1f |", v/mib)
			} else {
				b.WriteString(" - |")
			}
		}
		vlo, okLo := live(lo, stage)
		vhi, okHi := live(hi, stage)
		if okLo && okHi {
			fmt.Fprintf(&b, " %.2f |\n", (vhi-vlo)/float64(hi-lo)/1024)
		} else {
			b.WriteString(" - |\n")
		}
	}
	return b.String()
}
