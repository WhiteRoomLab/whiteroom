package edit

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	envBench      = "WHITEROOM_EDIT_BENCH"       // the directory of the results
	envBenchPosts = "WHITEROOM_EDIT_BENCH_POSTS" // the numbers of posts to try, such as 0,1000, instead of benchPosts
)

const (
	benchRuns       = 5    // of each operation on each site
	benchBatch      = 1000 // posts that an Update adds, at most
	benchParagraphs = 20   // of each post
	benchCharacters = 120  // of each paragraph
	benchAuthor     = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"
	mib             = 1 << 20
)

// benchPosts are the numbers of the posts that the sites have more than the sample site.
var benchPosts = []int{0, 1000, 5000, 20000}

// The post i is created i hours after benchBase, and dated a minute later.
var benchBase = time.Date(2020, 1, 1, 9, 0, 0, 0, time.FixedZone("", 9*60*60))

// The operations that the benchmark runs on each site, in order, and the adding of the posts.
const (
	benchAdd       = "add"
	opSaveTerm     = "SaveTerm"
	opSaveRevision = "SaveRevision"
	opCreateEntry  = "CreateEntry"
	opSaveSettings = "SaveSettings"
	opRefusedSave  = "SaveTermRefused"
)

var benchOps = []struct{ name, what string }{
	{opSaveTerm, "Save of the category that every post uses, with another description (its Ref stays, so no referrer is validated)"},
	{opSaveRevision, "SaveRevision of a draft revision of a published post, with the base that it read"},
	{opCreateEntry, "CreateEntry of a post in ja"},
	{opSaveSettings, "SaveSettings with another tagline (every document, every published revision and the latest revision of every entry is validated again)"},
	{opRefusedSave, "Save of that category with the language en, which the published revision of every post refuses (nothing is saved)"},
}

// benchResult is one run of an operation, or the adding of the posts (the operation benchAdd, as one run).
type benchResult struct {
	Posts    int     // that the site has more than the sample site
	Op       string  // of benchOps, or benchAdd
	Run      int     // from 1
	Millis   float64 // the time of the run
	Alloc    uint64  // the bytes that it allocated: the difference of TotalAlloc
	Problems int     `json:",omitempty"` // in the error of a refused save
	Bytes    int64   `json:",omitempty"` // of the files of the store after the posts have been added
	MaxRSS   uint64  // the largest resident set that the process has had by the end of the run
}

// benchLog collects the results, and writes each of them to raw.jsonl as it comes.
type benchLog struct {
	raw     *os.File
	results []benchResult
}

func (l *benchLog) add(t *testing.T, r benchResult) {
	t.Helper()
	line, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.raw.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	l.results = append(l.results, r)
	what := fmt.Sprintf("%s run %d", r.Op, r.Run)
	if r.Op == benchAdd {
		what = fmt.Sprintf("added, store %.0f MiB", float64(r.Bytes)/mib)
	}
	t.Logf("%s %d posts, %s: %.1f ms, %.1f MiB allocated", time.Now().Format(time.TimeOnly), r.Posts, what, r.Millis,
		float64(r.Alloc)/mib)
}

func benchCounts(t *testing.T) []int {
	t.Helper()
	v := os.Getenv(envBenchPosts)
	if v == "" {
		return benchPosts
	}
	var counts []int
	for f := range strings.SplitSeq(v, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 0 {
			t.Fatalf("%s=%q: want numbers of posts, 0 or more, separated by commas", envBenchPosts, v)
		}
		counts = append(counts, n)
	}
	return counts
}

// TestBenchmarks runs the benchmarks of the save operations if envBench names a directory for the results. For each of
// the numbers of posts, it imports the sample site into a new store under TMPDIR, which should be on a real disk, adds
// the posts, and runs each operation of benchOps benchRuns times. It writes raw.jsonl and results.md to the results.
func TestBenchmarks(t *testing.T) {
	out := os.Getenv(envBench)
	if out == "" {
		t.Skip("the benchmarks of the save operations, which write their results to the directory " + envBench)
	}
	if raceEnabled {
		t.Fatal("the benchmarks need a test binary without -race")
	}
	counts := benchCounts(t)
	if err := os.MkdirAll(out, 0o755); err != nil {
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

	started, load := time.Now(), [2]string{benchLoad(), ""}
	bl := &benchLog{raw: raw}
	for _, n := range counts {
		if !t.Run(strconv.Itoa(n)+" posts", func(t *testing.T) { benchSite(t, n, bl) }) {
			return
		}
	}
	load[1] = benchLoad()
	work, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	md := benchMarkdown(bl.results, counts, work, started, time.Since(started), load)
	if err := os.WriteFile(filepath.Join(out, "results.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// benchSite makes a store that has the sample site and posts more posts, and runs the operations on it.
func benchSite(t *testing.T, posts int, bl *benchLog) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := store.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := Import(t.Context(), s, openSample(t), userID); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	ed := testEditor(t, s)
	if posts > 0 {
		took, alloc := addPosts(t, s, posts)
		bl.add(t, benchResult{Posts: posts, Op: benchAdd, Run: 1, Millis: benchMillis(took), Alloc: alloc,
			Bytes: benchSize(t, dir), MaxRSS: benchMaxRSS()})
		checkPosts(t, ed, s, posts)
	}
	benchOperations(t, ed, s, posts, bl)
}

// benchID returns the ID of kind of the post i; none of the sample site or of the Editors of the tests is like it.
func benchID(kind content.RefKind, i int) string {
	var ulid [16]byte
	ulid[0], ulid[1] = 0x01, 0xc0
	binary.BigEndian.PutUint64(ulid[8:], uint64(i))
	return content.FormatID(kind, ulid)
}

func benchParagraph(i, j int) string {
	s := fmt.Sprintf("これは保存の時間を測る見本の投稿%dの、%d番目の段落です。", i, j)
	for utf8.RuneCountInString(s) < benchCharacters {
		s += "保存の時間を調べるために、画像の無い段落を並べています。"
	}
	return string([]rune(s)[:benchCharacters])
}

// benchRevision returns a revision of the post i shaped like the posts of large_test.go of internal/build.
func benchRevision(i int, title string) []byte {
	var body strings.Builder
	for j := 1; j <= benchParagraphs; j++ {
		if j > 1 {
			body.WriteString(",\n")
		}
		fmt.Fprintf(&body, `    {
      "type": "paragraph",
      "id": "b_para%04d",
      "content": [{ "type": "text", "text": %q }]
    }`, j, benchParagraph(i, j))
	}
	date := benchBase.Add(time.Duration(i)*time.Hour + time.Minute).Format(time.RFC3339)
	return fmt.Appendf(nil, `{
  "schemaVersion": 1,
  "title": %q,
  "slug": "post-%d",
  "date": %q,
  "author": %q,
  "terms": { "category": [%q], "tag": [%q] },
  "body": [
%s
  ]
}
`, title, i, date, benchAuthor, newsID, tagJA, body.String())
}

type benchPost struct {
	entry, rev         string // the IDs
	entryData, revData []byte
}

func newBenchPost(i, number int) benchPost {
	entry, rev, group := benchID(content.RefEntry, i), benchID(content.RefRevision, i), benchID(content.RefTranslationGroup, i)
	return benchPost{
		entry: entry, rev: rev,
		entryData: []byte(storedEntry(entry, number, content.TypePost, "ja", group, content.StatusPublished, rev)),
		revData:   benchRevision(i, fmt.Sprintf("保存の時間を測る見本の投稿 %d", i)),
	}
}

// addPosts adds the published posts 1 to posts to s with the store's own API, in Updates of at most benchBatch posts,
// and returns the time and the allocation of the Updates only.
func addPosts(t *testing.T, s *store.Store, posts int) (took time.Duration, alloc uint64) {
	t.Helper()
	var last int
	err := s.View(t.Context(), func(tx *store.Tx) (err error) {
		last, err = tx.LastNumber()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for from := 1; from <= posts; from += benchBatch {
		to := min(from+benchBatch-1, posts)
		batch := make([]benchPost, 0, to-from+1)
		for i := from; i <= to; i++ {
			batch = append(batch, newBenchPost(i, last+i))
		}
		d, a := benchMeasure(func() {
			err = s.Update(t.Context(), func(tx *store.Tx) error {
				for _, p := range batch {
					if _, err := tx.Put(content.RefEntry, p.entry, p.entryData, userID); err != nil {
						return err
					}
					if _, err := tx.AddRevision(p.entry, p.rev, p.revData, userID); err != nil {
						return err
					}
				}
				return nil
			})
		})
		if err != nil {
			t.Fatalf("adding the posts %d to %d: %v", from, to, err)
		}
		took, alloc = took+d, alloc+a
		t.Logf("%s %d posts: %d added, %.1f s", time.Now().Format(time.TimeOnly), posts, to, took.Seconds())
	}
	return took, alloc
}

// checkPosts fails the test if the first, the middle or the last post that addPosts has added does not validate as the
// build validates it.
func checkPosts(t *testing.T, ed *Editor, s *store.Store, posts int) {
	t.Helper()
	for _, i := range []int{1, (posts + 1) / 2, posts} {
		id := benchID(content.RefEntry, i)
		data := read(t, s, content.RefEntry, id).Data
		if es := unitProblems(t, ed, s, func(r *run) unit { return r.publishedUnit(id, data) }); len(es) > 0 {
			t.Fatalf("the published post %d does not validate:\n%s", i, benchError(es))
		}
	}
}

// benchError returns the text of err, cut to its first lines if it has many, as the problems of a large site do.
func benchError(err error) string {
	lines := strings.Split(err.Error(), "\n")
	if len(lines) <= 5 {
		return err.Error()
	}
	return strings.Join(lines[:5], "\n") + fmt.Sprintf("\n... and %d more lines", len(lines)-5)
}

// benchMeasure runs f after a GC, and returns the time that it took and the bytes that it allocated.
func benchMeasure(f func()) (time.Duration, uint64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	return took, after.TotalAlloc - before.TotalAlloc
}

func benchMillis(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// benchRun runs f as the run run of the operation op, and returns the result of the run and the error of f.
func benchRun(posts int, op string, run int, f func() error) (benchResult, error) {
	var err error
	took, alloc := benchMeasure(func() { err = f() })
	return benchResult{Posts: posts, Op: op, Run: run, Millis: benchMillis(took), Alloc: alloc, MaxRSS: benchMaxRSS()}, err
}

// benchOperations runs each operation of benchOps benchRuns times. One that fails or has a warning fails the test, except
// the refused save, which must fail with a problem in the published revision of each post and save nothing.
func benchOperations(t *testing.T, ed *Editor, s *store.Store, posts int, bl *benchLog) {
	t.Helper()
	ctx, op := t.Context(), Op{By: userID}
	news, settings := string(sampleFile(t, newsName)), string(sampleFile(t, settingsName))

	do := func(name string, run int, f func() (Result, error)) Result {
		t.Helper()
		var res Result
		r, err := benchRun(posts, name, run, func() (err error) {
			res, err = f()
			return err
		})
		if err != nil {
			t.Fatalf("%s (run %d, %d posts) error = %s", name, run, posts, benchError(err))
		}
		if len(res.Warnings) > 0 {
			t.Fatalf("%s (run %d, %d posts) has warnings:\n%s", name, run, posts, benchError(res.Warnings))
		}
		bl.add(t, r)
		return res
	}

	base := read(t, s, content.RefTerm, newsID).Seq
	for run := 1; run <= benchRuns; run++ {
		data := replaceOnce(t, news, "会社からのお知らせです。", fmt.Sprintf("会社からのお知らせです（%d）。", run))
		res := do(opSaveTerm, run, func() (Result, error) { return ed.Save(ctx, op, content.RefTerm, newsID, base, []byte(data)) })
		if res.Version.Seq == base {
			t.Fatalf("%s (run %d, %d posts) has saved nothing", opSaveTerm, run, posts)
		}
		base = res.Version.Seq
	}

	// A post of the sample site if there are no others.
	target := postJA
	if posts > 0 {
		target = benchID(content.RefEntry, 1)
	}
	latest := summaryOf(t, s, target).Latest
	for run := 1; run <= benchRuns; run++ {
		data := benchRevision(1, fmt.Sprintf("保存の時間を測る下書き %d", run))
		res := do(opSaveRevision, run, func() (Result, error) { return ed.SaveRevision(ctx, op, target, latest, data) })
		if res.Revision == "" || res.Revision == latest {
			t.Fatalf("%s (run %d, %d posts) has added no revision: %+v", opSaveRevision, run, posts, res)
		}
		latest = res.Revision
	}

	for run := 1; run <= benchRuns; run++ {
		res := do(opCreateEntry, run, func() (Result, error) { return ed.CreateEntry(ctx, op, content.TypePost, "ja") })
		if res.ID == "" {
			t.Fatalf("%s (run %d, %d posts) has made no entry: %+v", opCreateEntry, run, posts, res)
		}
	}

	base = read(t, s, store.SettingsKind, "").Seq
	for run := 1; run <= benchRuns; run++ {
		data := replaceOnce(t, settings, "Webサイトの企画と制作", fmt.Sprintf("Webサイトの企画と制作（%d）", run))
		res := do(opSaveSettings, run, func() (Result, error) { return ed.SaveSettings(ctx, op, base, []byte(data)) })
		if res.Version.Seq == base {
			t.Fatalf("%s (run %d, %d posts) has saved nothing", opSaveSettings, run, posts)
		}
		base = res.Version.Seq
	}

	english := replaceOnce(t, news, `"language": "ja"`, `"language": "en"`)
	for run := 1; run <= benchRuns; run++ {
		cur := read(t, s, content.RefTerm, newsID)
		r, err := benchRun(posts, opRefusedSave, run, func() error {
			_, err := ed.Save(ctx, op, content.RefTerm, newsID, cur.Seq, []byte(english))
			return err
		})
		var es content.Errors
		if !errors.As(err, &es) || len(es) == 0 {
			t.Fatalf("%s (run %d, %d posts) error = %v, want the problems of the documents that it makes invalid", opRefusedSave, run, posts, err)
		}
		in := make(map[string]bool, len(es))
		for _, e := range es {
			in[e.File] = true
		}
		for i := 1; i <= posts; i++ {
			if f := revisionName(benchID(content.RefRevision, i)); !in[f] {
				t.Fatalf("%s (run %d, %d posts) has no problem in %s, one of its %d problems is %q", opRefusedSave, run, posts, f, len(es), es[0])
			}
		}
		if got := read(t, s, content.RefTerm, newsID); got.Version != cur.Version {
			t.Fatalf("%s (run %d, %d posts) has saved the version %+v, the store has had %+v", opRefusedSave, run, posts, got.Version, cur.Version)
		}
		r.Problems = len(es)
		bl.add(t, r)
	}
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

// benchMaxRSS returns the largest resident set of the process so far, in bytes (VmHWM); 0 if there is none.
func benchMaxRSS() uint64 {
	kb, err := strconv.ParseUint(strings.TrimSuffix(benchFirst("/proc/self/status", "VmHWM"), " kB"), 10, 64)
	if err != nil {
		return 0
	}
	return kb << 10
}

// benchLoad returns the load averages of the machine of the last 1, 5 and 15 minutes; "unknown" if there are none.
func benchLoad() string {
	f := strings.Fields(benchFirst("/proc/loadavg", ""))
	if len(f) < 3 {
		return "unknown"
	}
	return strings.Join(f[:3], " ")
}

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
	if err != nil {
		t.Fatal(err)
	}
	return n
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

func benchMedian(values []float64) float64 {
	s := slices.Sorted(slices.Values(values))
	median := s[len(s)/2]
	if len(s)%2 == 0 {
		median = (s[len(s)/2-1] + median) / 2
	}
	return median
}

// benchMarkdown returns results.md.
func benchMarkdown(results []benchResult, counts []int, work string, started time.Time, elapsed time.Duration, load [2]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmarks of the save operations\n\n"+
		"The sample site with more posts: a store that Import has filled with the sample site, and the posts added "+
		"with the store's own API in Updates of at most %d posts (a published entry and its revision for each: %d "+
		"paragraphs of %d characters, no image, an author, a category and a tag, as the posts of the large sites of "+
		"internal/build). Each operation of an Editor ran %d times on each site; the time is that of the call, and the "+
		"allocation is the difference of TotalAlloc of runtime.MemStats. Max RSS is VmHWM of the test process by the end "+
		"of the runs, so it counts the sites before and the garbage that the runtime has not given back. "+
		"Started %s; %.0f s.\n\n",
		benchBatch, benchParagraphs, benchCharacters, benchRuns, started.Format(time.RFC3339), elapsed.Seconds())
	memory, _ := strconv.ParseInt(strings.TrimSuffix(benchFirst("/proc/meminfo", "MemTotal"), " kB"), 10, 64)
	fmt.Fprintf(&b, "- %s %s/%s\n- %s, %d CPUs, %d MiB of memory, Linux %s\n- Work directory %s (%s)\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, benchFirst("/proc/cpuinfo", "model name"), runtime.NumCPU(),
		memory>>10, benchFirst("/proc/sys/kernel/osrelease", ""), work, benchMount(work))
	b.WriteString("- Settings of the runtime:")
	for _, name := range []string{"GOGC", "GOMEMLIMIT", "GODEBUG"} {
		v, ok := os.LookupEnv(name)
		fmt.Fprintf(&b, " %s=%q", name, v)
		if !ok {
			b.WriteString(" (unset)")
		}
	}
	fmt.Fprintf(&b, " GOMAXPROCS=%d\n", runtime.GOMAXPROCS(0))
	fmt.Fprintf(&b, "- Load average of the machine (1, 5 and 15 minutes, /proc/loadavg), other processes included: "+
		"%s at the start, %s at the end\n", load[0], load[1])
	b.WriteString("- The sample site has one warning of the checks across the site (a redirect to a draft). An operation " +
		"that ends with these checks finds it in the site after its change, and so makes the site before the change " +
		"too, to see that the warning is old: it makes the model of the site from the index, and runs the checks, twice " +
		"(SaveRevision makes its model once).\n")

	of := func(posts int, op string) (rs []benchResult) {
		for _, r := range results {
			if r.Posts == posts && r.Op == op {
				rs = append(rs, r)
			}
		}
		return rs
	}
	b.WriteString("\n## Adding the posts\n\nThe time of the Updates (not of making the posts), the bytes that they allocated, " +
		"the size of the files of the store after them, and the largest resident set of the process by then.\n\n" +
		"| posts | time (s) | ms for each post | allocated (MiB) | store (MiB) | max RSS (MiB) |\n" +
		"| ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, n := range counts {
		for _, r := range of(n, benchAdd) {
			fmt.Fprintf(&b, "| %d | %.1f | %.2f | %.0f | %.0f | %.0f |\n", n, r.Millis/1000, r.Millis/float64(n),
				float64(r.Alloc)/mib, float64(r.Bytes)/mib, float64(r.MaxRSS)/mib)
		}
	}

	for _, op := range benchOps {
		fmt.Fprintf(&b, "\n## %s\n\n%s.\n\n", op.name, op.what)
		refused := op.name == opRefusedSave
		b.WriteString("| posts | median (ms) | max (ms) | allocated (MiB) |")
		if refused {
			b.WriteString(" problems |")
		}
		b.WriteString(" max RSS (MiB) |\n| ---: | ---: | ---: | ---: |")
		if refused {
			b.WriteString(" ---: |")
		}
		b.WriteString(" ---: |\n")
		for _, n := range counts {
			rs := of(n, op.name)
			if len(rs) == 0 {
				continue
			}
			var millis, alloc []float64
			var rss uint64
			for _, r := range rs {
				millis, alloc, rss = append(millis, r.Millis), append(alloc, float64(r.Alloc)/mib), max(rss, r.MaxRSS)
			}
			fmt.Fprintf(&b, "| %d | %.1f | %.1f | %.1f |", n, benchMedian(millis), slices.Max(millis), benchMedian(alloc))
			if refused {
				fmt.Fprintf(&b, " %d |", rs[0].Problems)
			}
			fmt.Fprintf(&b, " %.0f |\n", float64(rss)/mib)
		}
	}
	return b.String()
}
