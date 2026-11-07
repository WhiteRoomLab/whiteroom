//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package build

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

const (
	largeParagraphs = 20  // of each post
	largeCharacters = 120 // of each paragraph
)

// The post i is created i hours after largeBase, and dated a minute later.
var largeBase = time.Date(2020, 1, 1, 9, 0, 0, 0, time.FixedZone("", 9*60*60))

// largeID returns the ID of the post i without its prefix; the sample site and the fixture have none like it.
func largeID(i int) string { return fmt.Sprintf("01JE%022d", i) }

// largeIndex returns i if id is the ID of the revision of the post i.
func largeIndex(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "rev_01JE")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	return i, err == nil && id == "rev_"+largeID(i)
}

// largePages returns the pages of the sample site with n more posts, if n is a multiple of 10: 50, and for each post
// its page and the 0.4 pages that it adds to the lists of the posts page, the category, the tag and the author.
func largePages(n int) int { return 50 + n*14/10 }

func largeEntry(i int) content.SiteFile {
	id := largeID(i)
	created := largeBase.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
	return content.SiteFile{Name: "entries/ent_" + id + ".json", Data: fmt.Appendf(nil, `{
  "schemaVersion": 1,
  "id": "ent_%[1]s",
  "number": %[2]d,
  "type": "post",
  "language": "ja",
  "translationGroup": "tg_%[1]s",
  "status": "published",
  "publishedRevision": "rev_%[1]s",
  "createdAt": "%[3]s",
  "createdBy": "%[4]s"
}
`, id, 100000+i, created, userID)}
}

func largeParagraph(i, j int) string {
	s := fmt.Sprintf("これは大きいサイトの見本の投稿%dの、%d番目の段落です。", i, j)
	for utf8.RuneCountInString(s) < largeCharacters {
		s += "ビルドのメモリを調べるために、画像の無い段落を並べています。"
	}
	return string([]rune(s)[:largeCharacters])
}

// largeRevision returns the revision of the post i. With warn, the text of its first paragraph links to the entry
// draftID, which is not published, so that rendering the post warns once.
func largeRevision(i int, warn bool) content.SiteFile {
	var body strings.Builder
	for j := 1; j <= largeParagraphs; j++ {
		if j > 1 {
			body.WriteString(",\n")
		}
		marks := ""
		if warn && j == 1 {
			marks = `, "marks": [{ "type": "link", "attrs": { "entry": "` + draftID + `" } }]`
		}
		fmt.Fprintf(&body, `    {
      "type": "paragraph",
      "id": "b_para%04d",
      "content": [{ "type": "text", "text": %q%s }]
    }`, j, largeParagraph(i, j), marks)
	}
	date := largeBase.Add(time.Duration(i)*time.Hour + time.Minute).Format(time.RFC3339)
	return content.SiteFile{Name: "revisions/rev_" + largeID(i) + ".json", Data: fmt.Appendf(nil, `{
  "schemaVersion": 1,
  "title": "大きいサイトの見本の投稿 %[1]d",
  "slug": "post-%[1]d",
  "date": "%[2]s",
  "author": "%[3]s",
  "terms": { "category": ["%[4]s"], "tag": ["%[5]s"] },
  "body": [
%[6]s
  ]
}
`, i, date, authorID, newsCategoryID, eventTagID, body.String())}
}

// largeSource is the sample site with n more posts, made when the build reads them. With warn, each post warns once
// when it is rendered (largeRevision).
type largeSource struct {
	sampleSource
	n    int
	warn bool
}

func (s largeSource) Documents(kind content.RefKind) (map[string]content.SiteFile, error) {
	docs, err := s.sampleSource.Documents(kind)
	if err != nil || kind != content.RefEntry {
		return docs, err
	}
	for i := 1; i <= s.n; i++ {
		docs["ent_"+largeID(i)] = largeEntry(i)
	}
	return docs, nil
}

func (s largeSource) Revision(id string) (content.SiteFile, error) {
	if i, ok := largeIndex(id); ok && i >= 1 && i <= s.n {
		return largeRevision(i, s.warn), nil
	}
	return s.sampleSource.Revision(id)
}

// writeLargeSite writes the sample site with n more posts, the same as largeSource without warn, into the directory
// dir in the layout of internal/sitedir.
func writeLargeSite(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.CopyFS(dir, os.DirFS(sampleSite)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		for _, f := range []content.SiteFile{largeEntry(i), largeRevision(i, false)} {
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(f.Name)), f.Data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func openLargeSite(t *testing.T, dir string) sampleSource {
	t.Helper()
	d, err := sitedir.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return sampleSource{Dir: d, theme: openRoot(t, "../theme/testdata/example").FS()}
}

// liveHeap returns the bytes that the marking of a GC, which it runs, has found live.
func liveHeap() uint64 {
	runtime.GC()
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// buildStages are the stages that end in a build, in order (afterStage).
var buildStages = []string{
	"validated", "planned", "checked", "pages", "feeds", "sitemap", "media", "servers", "named",
}

// maxRetainedPerPost is the most that the live heap may grow for each post of a site, at the end of any stage.
const maxRetainedPerPost = 96 << 10

// TestBuildMemory builds the sample site with 100 and with 400 more posts, and checks that the live heap at the end of
// each stage grows by at most maxRetainedPerPost for each post between them, for posts that do not warn and for posts
// that do (what the build keeps for a warning is then for each post too).
func TestBuildMemory(t *testing.T) {
	// A first build, so that what a build initializes once is in the live heap at every size.
	mustBuild(t, largeSource{openSample(t), 1, true}, t.TempDir(), t.TempDir(), 4)

	var ended []string
	var heap []uint64
	afterStage = func(stage string) {
		ended = append(ended, stage)
		heap = append(heap, liveHeap())
	}
	t.Cleanup(func() { afterStage = nil })

	sizes := [2]int{100, 400}
	var warnings [2][2]int // by warn and size
	for w, warn := range []bool{false, true} {
		var live [2][]uint64
		for s, n := range sizes {
			ended, heap = nil, nil
			res := mustBuild(t, largeSource{openSample(t), n, warn}, t.TempDir(), t.TempDir(), 4)
			if want := largePages(n); res.Pages != want {
				t.Errorf("Build() of the site with %d posts (warn %v) wrote %d pages, want %d", n, warn, res.Pages, want)
			}
			if !slices.Equal(ended, buildStages) {
				t.Fatalf("the stages that ended in the build of the site with %d posts are %q, want %q", n, ended, buildStages)
			}
			live[s], warnings[w][s] = heap, len(res.Warnings)
		}
		for i, stage := range buildStages {
			perPost := (int64(live[1][i]) - int64(live[0][i])) / int64(sizes[1]-sizes[0])
			t.Logf("warn %-5v %-9s %6d bytes for each post (the live heap is %d with %d posts, %d with %d)",
				warn, stage, perPost, live[0][i], sizes[0], live[1][i], sizes[1])
			if perPost > maxRetainedPerPost {
				t.Errorf("warn %v: after the stage %s, the build keeps %d bytes for each post, want at most %d",
					warn, stage, perPost, maxRetainedPerPost)
			}
		}
	}
	for s, n := range sizes {
		if got := warnings[1][s] - warnings[0][s]; got != n {
			t.Errorf("the site with %d posts that warn has %d more warnings than the site without them, want %d", n, got, n)
		}
	}
}

// TestLargeSiteFiles checks that the files of writeLargeSite make the same site as largeSource.
func TestLargeSiteFiles(t *testing.T) {
	const n = 20
	dir := filepath.Join(t.TempDir(), "site")
	writeLargeSite(t, dir, n)
	got := mustBuild(t, openLargeSite(t, dir), t.TempDir(), t.TempDir(), 4)
	want := mustBuild(t, largeSource{openSample(t), n, false}, t.TempDir(), t.TempDir(), 4)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build() of the files of the site with %d posts = %+v, want %+v", n, got, want)
	}
	if got.Pages <= largePages(0) {
		t.Errorf("Build() wrote %d pages, want more than %d", got.Pages, largePages(0))
	}
}
