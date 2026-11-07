package siteplan

import (
	"cmp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// reported returns the problems that run reports, as the build shows them: sorted by file, pointer and message, without
// duplicates, as texts such as `revisions/rev_a.json: /slug: slug "x" is also used by revisions/rev_b.json`.
func reported(run func(report Report)) []string {
	var es content.Errors
	run(func(f content.SiteFile, pointer, msg string) {
		es = append(es, &content.Error{File: f.Name, Path: pointer, Msg: msg})
	})
	slices.SortFunc(es, func(a, b *content.Error) int {
		return cmp.Or(strings.Compare(a.File, b.File), strings.Compare(a.Path, b.Path), strings.Compare(a.Msg, b.Msg))
	})
	es = slices.CompactFunc(es, func(a, b *content.Error) bool { return *a == *b })
	var texts []string
	for _, e := range es {
		texts = append(texts, e.Error())
	}
	return texts
}

// smallSite returns a site in Japanese made by hand, as a caller without the build fills a Site: the settings and the
// theme as values, and two published pages with the slugs slugA and slugB, whose revisions have their names alone.
func smallSite(t *testing.T, slugA, slugB string) *Site {
	t.Helper()
	th, err := theme.Load(fstest.MapFS{
		"theme.yaml":           {Data: []byte("schemaVersion: 1\nlocations:\n  header: ヘッダー\n")},
		"templates/entry.html": {Data: []byte("<p>entry</p>\n")},
		"templates/list.html":  {Data: []byte("<p>list</p>\n")},
		"templates/404.html":   {Data: []byte("<p>not found</p>\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Site{
		Settings: content.SiteFile{Name: "site/settings.json"},
		Schema:   content.SiteFile{Name: "site/schema.yaml"},
		Site: &content.Site{
			Name:            content.Localized{"ja": "株式会社〇〇"},
			URL:             "https://example.com",
			TimeZone:        "Asia/Tokyo",
			Languages:       []string{"ja"},
			DefaultLanguage: "ja",
		},
		Theme:     th,
		Entries:   make(map[string]*Entry),
		Redirects: make(map[string]*Doc[content.Redirect]),
	}
	for i, slug := range []string{slugA, slugB} {
		id := "ent_01JF000000000000000000000" + string(rune('1'+i))
		rev := "rev_" + strings.TrimPrefix(id, "ent_")
		s.Entries[id] = &Entry{
			File: content.SiteFile{Name: "entries/" + id + ".json"},
			E: content.Entry{
				ID:                id,
				Number:            i + 1,
				Type:              content.TypePage,
				Language:          "ja",
				TranslationGroup:  "tg_" + strings.TrimPrefix(id, "ent_"),
				Status:            content.StatusPublished,
				PublishedRevision: rev,
				CreatedAt:         "2026-10-25T10:00:00+09:00",
			},
			RevFile: content.SiteFile{Name: "revisions/" + rev + ".json"},
			Rev:     &content.Revision{Title: slug, Slug: slug},
		}
	}
	return s
}

func TestSmallSite(t *testing.T) {
	const (
		revA = "revisions/rev_01JF0000000000000000000001.json"
		revB = "revisions/rev_01JF0000000000000000000002.json"
		rdr  = "redirects/rdr_01JF0000000000000000000001.json"
	)

	same := smallSite(t, "x", "x")
	want := []string{revB + `: /slug: slug "x" is also used by ` + revA}
	if got := reported(same.CheckAcross); !slices.Equal(got, want) {
		t.Errorf("CheckAcross() reported:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	s := smallSite(t, "x", "y")
	s.Redirects["rdr_01JF0000000000000000000001"] = &Doc[content.Redirect]{
		File: content.SiteFile{Name: rdr},
		V: content.Redirect{
			ID: "rdr_01JF0000000000000000000001", From: "/x/", To: "ent_01JF0000000000000000000002", Status: 301,
		},
	}
	if got := reported(s.CheckAcross); got != nil {
		t.Errorf("CheckAcross() reported:\n%s", strings.Join(got, "\n"))
	}
	var plan *Plan
	var rs []Redirect
	got := reported(func(report Report) {
		plan = s.Plan(report)
		noMedia := func(id string) (string, error) {
			t.Errorf("mediaPath(%q)", id)
			return "", nil
		}
		rs = s.ResolveRedirects(plan, noMedia, report, report)
		CheckOutputs(plan.Outputs, rs, report)
	})
	want = []string{rdr + `: /from: from "/x/" is the output path "/x/" of ` + revA}
	if !slices.Equal(got, want) {
		t.Errorf("Plan, ResolveRedirects and CheckOutputs reported:\n%s\nwant:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(rs) != 1 || rs[0].From != "/x/" || rs[0].To != "/y/" || rs[0].Status != 301 || rs[0].Src.File.Name != rdr {
		t.Errorf("ResolveRedirects() = %+v, want the redirect from /x/ to /y/", rs)
	}
	paths := make(map[string]string)
	for _, o := range plan.Outputs {
		paths[o.Path] = o.File
	}
	for path, file := range map[string]string{
		"/x/":          "x/index.html",
		"/y/":          "y/index.html",
		"/":            "index.html",
		"/404.html":    "404.html",
		"/feed/":       "feed/index.xml",
		"/sitemap.xml": "sitemap.xml",
	} {
		if got, ok := paths[path]; !ok || got != file {
			t.Errorf("output at %q = %q, %t, want %q", path, got, ok, file)
		}
	}
	if plan.Base != "" || plan.Location == nil || plan.EntryPaths["ent_01JF0000000000000000000001"] != "/x/" {
		t.Errorf("Plan() = %+v", plan)
	}
}

func TestFromKey(t *testing.T) {
	const ideographicSpace = string(rune(0x3000))
	for _, tt := range []struct{ in, want string }{
		{"/", "/"},
		{"/old/", "/old"},
		{"/old", "/old"},
		{"/old//", "/old/"},
		{"/?p=123", "/?p=123"},
		{"/old/?y=2&x=1", "/old?x=1&y=2"},
		{"/%E4%BC%9A%E7%A4%BE/", "/会社"},
		{"/tag/a" + ideographicSpace + "b/", "/tag/a%E3%80%80b"},
		{"/a?", "/a"},
	} {
		if got := FromKey(tt.in); got != tt.want {
			t.Errorf("FromKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCycles(t *testing.T) {
	next := map[string]string{
		"a": "c", "c": "b", "b": "a", // a cycle
		"d": "a",           // into a cycle
		"z": "y", "y": "z", // another cycle, found first from x
		"x": "z",
		"e": "f", // a chain that ends
	}
	want := [][]string{{"a", "c", "b"}, {"y", "z"}}
	if got := cycles(next); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("cycles(%v) = %q, want %q", next, got, want)
	}
	if got, want := cycleText(want[0]), "a -> c -> b -> a"; got != want {
		t.Errorf("cycleText(%q) = %q, want %q", want[0], got, want)
	}
}
