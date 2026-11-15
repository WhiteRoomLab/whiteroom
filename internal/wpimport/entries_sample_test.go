package wpimport

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// entriesOut holds the golden files of TestEntriesSample, which go test -update writes: settings.json and propose.txt, the
// settings that the proposal gives for entries.xml and its report; and what the import makes of the corrected settings,
// which is site/settings.json, site/schema.yaml, terms/<ID>.json, authors/<ID>.json, media/<ID>.json, entries/<ID>.json,
// revisions/<ID>.json and import.txt, the report of the import.
const entriesOut = "testdata/out-entries"

// What becomes of each item of entries.xml that is not an attachment, as the WXR and the rules say (the numbers are the
// wp:post_id). The test does not take them from what the import made: an item that became a draft by mistake would then
// be missing from both sides.
var (
	// The pages 10 to 16 and the posts 101 to 110 and 120 are the published posts and pages that are not drafts; 201 and 202
	// are of the type works, and 211 of the type staff.
	entriesPublished = []int64{10, 11, 12, 13, 14, 15, 16, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 120, 201, 202, 211}
	entriesScheduled = []int64{118}
	// 17 is a draft; 18 is published under it; 19 is private with a password; 111 has no title; 112 and 113 are drafts; 114
	// is pending; 115 is private; 116 is published with a password and 117 scheduled with one; 212 lacks the required field.
	entriesDrafts = []int64{17, 18, 19, 111, 112, 113, 114, 115, 116, 117, 212}
	// 119 is in the trash and 301 is of a type that the schema file does not define.
	entriesNotImported = []int64{119, 301}
)

const (
	// entriesPaths is the number of the published posts and pages: 7 pages and 11 posts.
	entriesPaths = 18
	// entriesProtected is the number of the items that are private or have a password: 19, 115, 116 and 117.
	entriesProtected = 4
	// entriesPasswords is the number of the different passwords in the WXR.
	entriesPasswords = 3
)

// entriesExceptions are the published posts and pages whose paths are not those of their links, with their paths. The
// link of the post 102 has the category with the smaller term_id, and Yoast SEO has the other as its primary category.
var entriesExceptions = map[int64]string{102: "/columns/autumn-event/"}

func entriesSampleDefs(t *testing.T) *content.Site {
	t.Helper()
	defs := new(content.Site)
	if err := defs.UnmarshalSchema(readTestdata(t, "entries-schema.yaml")); err != nil {
		t.Fatal(err)
	}
	return defs
}

// entriesSampleImport reads entries.xml into an import with a report of its own. There is no REST API for it.
func entriesSampleImport(t *testing.T) *Import {
	t.Helper()
	e, rep := readBodiesFile(t, "entries.xml")
	return New("entries.xml", e, rep)
}

// entriesSampleFiles are the files of the attachments of entries.xml in the copy of the uploads of the old site.
func entriesSampleFiles() map[string][]byte {
	return map[string][]byte{
		"2026/10/photo.png": testPNG(64, 32, 1),
		"2026/10/share.png": testPNG(60, 30, 2),
		"2026/10/guide.pdf": testPDF(),
	}
}

// entriesSampleProposal proposes the settings of entries.xml, and returns them as the file that the person corrects,
// read back, with the report of the proposal.
func entriesSampleProposal(t *testing.T, defs *content.Site) (settings *Settings, file, report string) {
	t.Helper()
	imp := entriesSampleImport(t)
	proposed, err := imp.ProposeSettings(defs)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := proposed.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	settings, err = ReadSettings("settings.json", b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings, proposed) {
		t.Errorf("ReadSettings gave %+v, want %+v", settings, proposed)
	}
	return settings, b.String(), reportText(t, imp.Report)
}

// correctEntriesSettings makes the changes that the person who imports makes in the file that the proposal wrote, which
// the proposal could not know: the time zone, and the page of the posts, which is page 11 of the WXR; the names of the page
// templates in the theme of the site, of which the person leaves one out, for the import to name it as it does by default,
// and gives none to another; and the field of the schema file that a custom field is the value of.
func correctEntriesSettings(t *testing.T, s *Settings) {
	t.Helper()
	fields := s.Fields["works"]
	for _, key := range []string{"templates/landing.php", "templates/retired.php", "templates/wide.php"} {
		if _, ok := s.PageTemplates[key]; !ok {
			t.Fatalf("the proposal has no page template %q: %v", key, s.PageTemplates)
		}
	}
	if _, ok := fields["work_year"]; !ok || s.Site.TimeZone != "" || s.Site.PostsPage != 0 || s.Site.FrontPage != 10 || s.SEO.Plugin != "yoast" {
		t.Fatalf("the proposal is not what the corrections are for: %+v, %+v, %v", s.Site, s.SEO, fields)
	}
	s.Site.TimeZone = "Asia/Tokyo"
	s.Site.PostsPage = 11
	s.PageTemplates["templates/wide.php"] = "full-width"
	s.PageTemplates["templates/retired.php"] = ""
	delete(s.PageTemplates, "templates/landing.php")
	fields["work_year"] = "year"
}

// entriesRun is an import of entries.xml that went through every stage.
type entriesRun struct {
	imp    *Import
	docs   []*EntryDoc
	golden map[string]string
	loaded *content.Site // the site read back from its documents, as the build reads it
}

// document adds the file name, which marshal writes, to the golden files.
func (r *entriesRun) document(t *testing.T, name string, marshal func() ([]byte, error)) {
	t.Helper()
	b, err := marshal()
	if err != nil {
		t.Fatal(err)
	}
	r.golden[name] = string(b) + "\n"
}

// runEntriesSample imports entries.xml with settings, the corrected settings, and the schema file defs, as a new import
// with a new report: the plan, the site, the terms, the author profiles, the media, and the entries.
func runEntriesSample(t *testing.T, settings *Settings, defs *content.Site) *entriesRun {
	t.Helper()
	imp := entriesSampleImport(t)
	if err := imp.PlanEntries(settings, defs); err != nil {
		t.Fatal(err)
	}
	if err := imp.PlanMedia(); err != nil {
		t.Fatal(err)
	}
	site, err := imp.Site(settings, defs)
	if err != nil {
		t.Fatalf("Site = %v", err)
	}
	terms, err := imp.Terms(site)
	if err != nil {
		t.Fatal(err)
	}
	authors, err := imp.Authors(site)
	if err != nil {
		t.Fatal(err)
	}
	media, err := imp.Media(context.Background(), site, MediaOptions{Files: filesDir(t), Uploads: uploads(entriesSampleFiles()), By: mediaTestBy})
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != len(entriesSampleFiles()) {
		t.Fatalf("Media gave %d documents, want %d", len(media), len(entriesSampleFiles()))
	}
	docs, err := collectEntries(context.Background(), imp, settings, site)
	if err != nil {
		t.Fatalf("Entries = %v", err)
	}
	r := &entriesRun{imp: imp, docs: docs, golden: make(map[string]string)}
	for _, term := range terms {
		r.document(t, "terms/"+term.ID+".json", term.MarshalJSON)
	}
	for _, a := range authors {
		r.document(t, "authors/"+a.ID+".json", a.MarshalJSON)
	}
	for _, m := range media {
		r.document(t, "media/"+m.ID+".json", m.MarshalJSON)
	}
	for _, d := range docs {
		r.document(t, "entries/"+d.Entry.ID+".json", d.Entry.MarshalJSON)
		r.document(t, "revisions/"+d.RevisionID+".json", d.Revision.MarshalJSON)
	}
	r.golden["import.txt"] = reportText(t, imp.Report)

	// The site settings are written after the entries, which can leave the front page and the posts page out of them.
	settingsDoc, err := site.MarshalSettings()
	if err != nil {
		t.Fatal(err)
	}
	schemaDoc, err := site.MarshalSchema()
	if err != nil {
		t.Fatal(err)
	}
	r.golden["site/settings.json"] = string(settingsDoc) + "\n"
	r.golden["site/schema.yaml"] = string(schemaDoc)
	r.loaded, err = content.LoadSite(content.SiteFile{Name: "site/settings.json", Data: settingsDoc},
		content.SiteFile{Name: "site/schema.yaml", Data: schemaDoc}, imp.Ref)
	if err != nil {
		t.Fatalf("LoadSite = %v", err)
	}
	return r
}

// checkEntriesStatuses checks the status of every entry against the lists above, and that the lists have every item that
// is not an attachment.
func checkEntriesStatuses(t *testing.T, r *entriesRun) {
	t.Helper()
	want := make(map[int64]content.Status)
	for status, posts := range map[content.Status][]int64{
		content.StatusPublished: entriesPublished, content.StatusScheduled: entriesScheduled, content.StatusDraft: entriesDrafts,
	} {
		for _, post := range posts {
			if _, dup := want[post]; dup {
				t.Fatalf("the item %d is in two lists", post)
			}
			want[post] = status
		}
	}
	for i := range r.imp.WXR.Items {
		it := &r.imp.WXR.Items[i]
		_, imported := want[it.ID]
		gone := slices.Contains(entriesNotImported, it.ID)
		if it.Type != "attachment" && imported == gone {
			t.Errorf("the item %d (%s) is in the lists of the imported items: %t, of the others: %t, want one of them", it.ID, it.Type, imported, gone)
		}
	}
	if len(r.docs) != len(want) {
		t.Errorf("%d entries were given, want %d", len(r.docs), len(want))
	}
	for _, d := range r.docs {
		if status, ok := want[d.Post]; !ok || d.Entry.Status != status {
			t.Errorf("the entry of the item %d is %q, want %q (given: %t)", d.Post, d.Entry.Status, status, ok)
		}
	}
	if d := docOf(r.docs, 118); d == nil || d.Entry.ScheduledAt != "2026-12-28T10:00:00+09:00" || d.Entry.ScheduledRevision != d.RevisionID {
		t.Errorf("the scheduled entry of the item 118 is %+v", d)
	}
	var paths int
	for _, post := range entriesPublished {
		if it := r.imp.WXR.Post(post); it.Type == "post" || it.Type == "page" {
			paths++
		}
	}
	if paths != entriesPaths {
		t.Errorf("the lists have %d published posts and pages, want %d", paths, entriesPaths)
	}
}

// checkCategoryRules checks the posts that have two categories, which tell the rules of the first category apart: the
// link names the one with the larger term_id (101); the link names the smaller and Yoast SEO names the other (102);
// neither (103); and the link names the larger and Yoast SEO the smaller, where the link comes first (120). In each, the
// WXR lists them in the order that none of the rules gives.
func checkCategoryRules(t *testing.T, r *entriesRun) {
	t.Helper()
	columns, events := termIDOf(t, r.imp, "category", "columns"), termIDOf(t, r.imp, "category", "events")
	if r.imp.WXR.Term("category", 4).Slug != "events" || r.imp.WXR.Term("category", 5).Slug != "columns" {
		t.Fatal("events must have the smaller term_id, and columns the larger")
	}
	for _, tt := range []struct {
		post int64
		want []string
		path string
	}{
		{101, []string{columns, events}, "/columns/spring-news/"},
		{102, []string{columns, events}, "/columns/autumn-event/"},
		{103, []string{events, columns}, "/events/winter-reading/"},
		{120, []string{columns, events}, "/columns/summer-event/"},
	} {
		d := docOf(r.docs, tt.post)
		if d == nil {
			t.Fatalf("no entry for the item %d", tt.post)
		}
		path, err := r.loaded.EntryPath(&d.Entry, &d.Revision, r.imp.PathRef)
		if got := d.Revision.Terms[content.TaxonomyCategory]; !slices.Equal(got, tt.want) || path != tt.path || err != nil {
			t.Errorf("the categories of the item %d are %v and its path is %q, %v, want %v and %q", tt.post, got, path, err, tt.want, tt.path)
		}
	}
}

// checkEntriesSecrets checks that none of the output has a password or an email address of the WXR, which the WXR has.
func checkEntriesSecrets(t *testing.T, imp *Import, golden map[string]string) {
	t.Helper()
	passwords := make(map[string]bool)
	for i := range imp.WXR.Items {
		if pw := imp.WXR.Items[i].Password; pw != "" {
			passwords[pw] = true
		}
	}
	emails := 0
	secrets := slices.Collect(maps.Keys(passwords))
	for _, a := range imp.WXR.Authors {
		if a.Email != "" {
			emails++
			secrets = append(secrets, a.Email)
		}
	}
	if len(passwords) != entriesPasswords || emails != 2 {
		t.Fatalf("the WXR has %d passwords and %d email addresses, want %d and 2", len(passwords), emails, entriesPasswords)
	}
	for name, text := range golden {
		if strings.Contains(text, "@") {
			t.Errorf("%s has an @: an email address of the WXR got into the output", name)
		}
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s has %q, which the WXR has as a password or an email address", name, secret)
			}
		}
	}
}

// checkSettingsPagesAreDrafts imports entries.xml with settings in which the front page, or the posts page, is page 17,
// which the import makes a draft. The site settings must leave it out, and the posts page too if the front page is: it
// cannot be without one.
func checkSettingsPagesAreDrafts(t *testing.T, settings *Settings, defs *content.Site) {
	t.Helper()
	const front, posts = "front page", "posts page"
	for _, tt := range []struct {
		name       string
		frontPage  int64
		postsPage  int64
		wantFront  bool
		lines      []string
		exceptions map[int64]string
	}{
		{
			name: front, frontPage: 17, postsPage: 11,
			lines: []string{
				"skipped: the front page, page 17, is not published here; it is left out of the site settings -> site/settings.json#/frontPage",
				"skipped: the posts page, page 11, is left out: the site has no front page -> site/settings.json#/postsPage",
			},
			// The page 10 is no longer the front page, so its path is that of the URL rule of the pages.
			exceptions: map[int64]string{10: "/home/"},
		},
		{
			name: posts, frontPage: 10, postsPage: 17, wantFront: true,
			lines: []string{"skipped: the posts page, page 17, is not published here; it is left out of the site settings -> site/settings.json#/postsPage"},
		},
	} {
		t.Run("the "+tt.name+" is a draft", func(t *testing.T) {
			s := *settings
			s.Site.FrontPage, s.Site.PostsPage = tt.frontPage, tt.postsPage
			r := runEntriesSample(t, &s, defs)
			if got := r.loaded.FrontPage != nil; got != tt.wantFront || r.loaded.PostsPage != nil {
				t.Errorf("the site has the front page %v and the posts page %v, want a front page: %t and no posts page", r.loaded.FrontPage, r.loaded.PostsPage, tt.wantFront)
			}
			except := maps.Clone(entriesExceptions)
			maps.Copy(except, tt.exceptions)
			if counts := checkSampleEntries(t, r.imp, r.loaded, r.docs, except); counts.paths != entriesPaths {
				t.Errorf("%d paths were compared, want %d", counts.paths, entriesPaths)
			}
			var lines []string
			for _, e := range r.imp.Report.Entries() {
				if e.Target.File == "site/settings.json" {
					lines = append(lines, fmt.Sprintf("%s: %s -> %s#%s", e.Kind, e.Message, e.Target.File, e.Target.Pointer))
				}
			}
			sameSet(t, "the entries of the report about the site settings", lines, tt.lines)
		})
	}
}

// TestEntriesSample proposes the settings of entries.xml, which has the posts, pages and items of custom types of a
// WordPress in every status, with the settings of Yoast SEO and custom fields, corrects the settings as a person does,
// imports it, and compares the files with the golden files in testdata/out-entries.
func TestEntriesSample(t *testing.T) {
	defs := entriesSampleDefs(t)
	settings, file, report := entriesSampleProposal(t, defs)
	if again, againFile, againReport := entriesSampleProposal(t, defs); !reflect.DeepEqual(again, settings) || againFile != file || againReport != report {
		t.Error("the proposals of two imports of entries.xml differ")
	}
	golden := map[string]string{"settings.json": file, "propose.txt": report}
	correctEntriesSettings(t, settings)
	r := runEntriesSample(t, settings, defs)
	maps.Copy(golden, r.golden)

	checkEntriesStatuses(t, r)
	checkCategoryRules(t, r)
	checkEntriesSecrets(t, r.imp, golden)
	counts := checkSampleEntries(t, r.imp, r.loaded, r.docs, entriesExceptions)
	if counts.paths != entriesPaths || counts.protected != entriesProtected {
		t.Errorf("%d paths were compared and %d items are private or have a password, want %d and %d", counts.paths, counts.protected, entriesPaths, entriesProtected)
	}

	// The same input gives the same files and the same report, from another import.
	if again := runEntriesSample(t, settings, defs); !maps.Equal(again.golden, r.golden) || entriesText(t, again.imp, again.docs) != entriesText(t, r.imp, r.docs) {
		t.Error("two imports of entries.xml with the same settings differ")
	}
	checkSettingsPagesAreDrafts(t, settings, defs)

	if *update {
		if err := os.RemoveAll(entriesOut); err != nil {
			t.Fatal(err)
		}
	}
	names := slices.Sorted(maps.Keys(golden))
	for _, name := range names {
		checkGolden(t, filepath.Join(entriesOut, filepath.FromSlash(name)), golden[name])
	}
	if got := goldenFiles(t, entriesOut); !slices.Equal(got, names) {
		t.Errorf("%s has %q, want %q", entriesOut, got, names)
	}
}
