package wpimport

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const (
	// The dates of an item that has no other: 9:45 in Japan, and 10:00 the next day for the modification.
	entryDate        = "2026-10-05 09:45:00"
	entryDateGMT     = "2026-10-05 00:45:00"
	entryModified    = "2026-10-06 10:00:00"
	entryModifiedGMT = "2026-10-06 01:00:00"
	entryTime        = "2026-10-05T09:45:00+09:00"
	entryModifiedAt  = "2026-10-06T10:00:00+09:00"
	zeroDate         = "0000-00-00 00:00:00"
	// omit is a date of wxrEntry that leaves its element out.
	omit = "(omitted)"
	// entryPassword must not be in any document or in the report.
	entryPassword = "s3cret-pw-xyz"
)

type wxrTermRef struct{ domain, nicename, name string }

// wxrEntry is an item of the WXR to write, with every value in a CDATA. A date that is empty is the default of the
// constants above, and omit leaves the element out.
type wxrEntry struct {
	id                                   int
	typ, status                          string // "" for post and publish
	title, name                          string
	date, dateGMT, modified, modifiedGMT string
	password, content, excerpt           string
	link, creator, attachmentURL         string
	parent, menuOrder                    int
	sticky                               bool
	terms                                []wxrTermRef
	meta                                 [][2]string // the key and the value of each postmeta
}

func (x wxrEntry) xml() string {
	typ, status := x.typ, x.status
	if typ == "" {
		typ = "post"
	}
	if status == "" {
		status = "publish"
	}
	var b strings.Builder
	cdata := func(tag, v string) { fmt.Fprintf(&b, "<%s><![CDATA[%s]]></%s>", tag, v, tag) }
	b.WriteString("<item>")
	cdata("title", x.title)
	if x.link != "" {
		cdata("link", x.link)
	}
	if x.creator != "" {
		cdata("dc:creator", x.creator)
	}
	cdata("content:encoded", x.content)
	cdata("excerpt:encoded", x.excerpt)
	fmt.Fprintf(&b, "<wp:post_id>%d</wp:post_id>", x.id)
	for _, d := range []struct{ tag, value, fallback string }{
		{"wp:post_date", x.date, entryDate}, {"wp:post_date_gmt", x.dateGMT, entryDateGMT},
		{"wp:post_modified", x.modified, entryModified}, {"wp:post_modified_gmt", x.modifiedGMT, entryModifiedGMT},
	} {
		switch d.value {
		case omit:
			continue
		case "":
			d.value = d.fallback
		}
		cdata(d.tag, d.value)
	}
	cdata("wp:post_name", x.name)
	if x.attachmentURL != "" {
		cdata("wp:attachment_url", x.attachmentURL)
	}
	cdata("wp:status", status)
	fmt.Fprintf(&b, "<wp:post_parent>%d</wp:post_parent><wp:menu_order>%d</wp:menu_order>", x.parent, x.menuOrder)
	cdata("wp:post_type", typ)
	cdata("wp:post_password", x.password)
	if x.sticky {
		b.WriteString("<wp:is_sticky>1</wp:is_sticky>")
	} else {
		b.WriteString("<wp:is_sticky>0</wp:is_sticky>")
	}
	for _, r := range x.terms {
		fmt.Fprintf(&b, "<category domain=%q nicename=%q><![CDATA[%s]]></category>", r.domain, r.nicename, r.name)
	}
	for _, m := range x.meta {
		fmt.Fprintf(&b, "<wp:postmeta><wp:meta_key><![CDATA[%s]]></wp:meta_key><wp:meta_value><![CDATA[%s]]></wp:meta_value></wp:postmeta>", m[0], m[1])
	}
	b.WriteString("</item>\n")
	return b.String()
}

// entriesWXR writes a WXR of the site https://example.com, with channel after its link, and the items, one to a line.
func entriesWXR(channel string, items ...wxrEntry) string {
	var b strings.Builder
	b.WriteString(head + "<link>https://example.com</link>\n" + channel)
	for _, x := range items {
		b.WriteString(x.xml())
	}
	b.WriteString(tail)
	return b.String()
}

// entrySettings is the settings of the tests of the entries: a site in Japanese, which is in the time zone of Japan.
func entrySettings() *Settings {
	return &Settings{
		Site: SiteSettings{Name: "Test", URL: "https://example.com", Language: "ja", TimeZone: "Asia/Tokyo", PostsPerPage: 10},
		URLs: URLSettings{Types: map[string]TypeURLs{"works": {URL: "/works/{slug}/", Archive: "/works/"}}},
	}
}

// entryDefs is the schema file of the tests of the entries, which defines the custom type works.
func entryDefs() *content.Site {
	return &content.Site{Types: []content.TypeDef{{Name: "works", Label: "Works", Body: true}}}
}

// planEntries reads the WXR in, and does what comes before the conversion of the entries: the plan, the site, the terms
// and the author profiles. s and defs are nil for entrySettings and entryDefs.
func planEntries(t *testing.T, s *Settings, defs *content.Site, in string) (*Import, *content.Site) {
	t.Helper()
	e, rep := readString(t, "site.xml", in)
	imp := New("site.xml", e, rep)
	if s == nil {
		s = entrySettings()
	}
	if defs == nil {
		defs = entryDefs()
	}
	if err := imp.PlanEntries(s, defs); err != nil {
		t.Fatalf("PlanEntries = %v", err)
	}
	if err := imp.PlanMedia(); err != nil {
		t.Fatalf("PlanMedia = %v", err)
	}
	site, err := imp.Site(s, defs)
	if err != nil {
		t.Fatalf("Site = %v", err)
	}
	if _, err := imp.Terms(site); err != nil {
		t.Fatalf("Terms = %v", err)
	}
	if _, err := imp.Authors(site); err != nil {
		t.Fatalf("Authors = %v", err)
	}
	return imp, site
}

// collectEntries calls Entries, and returns what it gave to emit.
func collectEntries(ctx context.Context, imp *Import, s *Settings, site *content.Site) ([]*EntryDoc, error) {
	var docs []*EntryDoc
	err := imp.Entries(ctx, EntryOptions{Site: site, Settings: s, By: mediaTestBy}, func(d *EntryDoc) error {
		docs = append(docs, d)
		return nil
	})
	return docs, err
}

// makeEntries makes the entries of the WXR in, and checks them. s and defs are nil for entrySettings and entryDefs.
func makeEntries(t *testing.T, s *Settings, defs *content.Site, in string) (*Import, *content.Site, []*EntryDoc) {
	t.Helper()
	imp, site := planEntries(t, s, defs, in)
	if s == nil {
		s = entrySettings()
	}
	docs, err := collectEntries(context.Background(), imp, s, site)
	if err != nil {
		t.Fatalf("Entries = %v", err)
	}
	checkEntries(t, imp, site, docs)
	return imp, site, docs
}

// checkEntries checks what holds for the entries of any WXR: each passes Validate with the records of the import, as
// one to publish if it is published or scheduled, and reads back from its JSON; the table and the references have exactly
// the entries that were given, which are in the order of the WXR; the IDs of the revisions and of the translation groups
// differ; the state has the revision that goes with its status and no other; the password of an item is in none of the
// output; and each place of the report that names a document is a document that was given.
func checkEntries(t *testing.T, imp *Import, site *content.Site, docs []*EntryDoc) {
	t.Helper()
	given := make(map[int64]*EntryDoc, len(docs))
	revisions, groups := make(map[string]int64), make(map[string]int64)
	entryFiles, revisionFiles := make(map[string]bool), make(map[string]*EntryDoc)
	var out bytes.Buffer
	last := -1
	position := make(map[int64]int, len(imp.WXR.Items))
	for i := range imp.WXR.Items {
		position[imp.WXR.Items[i].ID] = i
	}
	for _, d := range docs {
		given[d.Post] = d
		pos, ok := position[d.Post]
		if !ok {
			t.Errorf("an entry is given for the item %d, which the WXR does not have", d.Post)
			continue
		}
		it := &imp.WXR.Items[pos]
		if pos <= last {
			t.Errorf("the entry of item %d is not in the order of the WXR", d.Post)
		}
		last = pos
		e, rev := d.Entry, d.Revision
		if err := e.Validate(site); err != nil {
			t.Errorf("entry of item %d: %v", d.Post, err)
		}
		opts := content.RevisionOptions{Site: site, Entry: &d.Entry, Publish: e.Status == content.StatusPublished || e.Status == content.StatusScheduled, Import: true, Lookup: imp.Ref}
		if err := rev.Validate(opts); err != nil {
			t.Errorf("revision of item %d: %v", d.Post, err)
		}
		if e.Number != int(d.Post) || e.CreatedBy != mediaTestBy {
			t.Errorf("entry of item %d has number %d and createdBy %q, want %d and %q", d.Post, e.Number, e.CreatedBy, d.Post, mediaTestBy)
		}
		if kind, id, ok := imp.IDs.Lookup(PostObject(it)); !ok || kind != content.RefEntry || id != e.ID {
			t.Errorf("Lookup of item %d = %q, %q, %v, want entry %q", d.Post, kind, id, ok, e.ID)
		}
		if r, ok := imp.Ref(content.RefEntry, e.ID); !ok || r != (content.Ref{Type: e.Type, Language: e.Language}) {
			t.Errorf("Ref of entry %s = %+v, %v, want the type %q and the language %q", e.ID, r, ok, e.Type, e.Language)
		}
		if want := typeName(it.Type, site); e.Type != want {
			t.Errorf("entry of item %d has the type %q, want %q", d.Post, e.Type, want)
		}
		if other, ok := revisions[d.RevisionID]; ok {
			t.Errorf("items %d and %d have the revision %s", other, d.Post, d.RevisionID)
		}
		revisions[d.RevisionID] = d.Post
		if other, ok := groups[e.TranslationGroup]; ok {
			t.Errorf("items %d and %d have the translation group %s", other, d.Post, e.TranslationGroup)
		}
		groups[e.TranslationGroup] = d.Post
		switch e.Status {
		case content.StatusPublished:
			if e.PublishedRevision != d.RevisionID || e.ScheduledRevision != "" || e.ScheduledAt != "" {
				t.Errorf("the published entry of item %d has %+v", d.Post, e)
			}
		case content.StatusScheduled:
			if e.ScheduledRevision != d.RevisionID || e.ScheduledAt == "" || e.PublishedRevision != "" {
				t.Errorf("the scheduled entry of item %d has %+v", d.Post, e)
			}
		case content.StatusDraft:
			if e.PublishedRevision != "" || e.ScheduledRevision != "" || e.ScheduledAt != "" {
				t.Errorf("the draft of item %d has %+v", d.Post, e)
			}
		case content.StatusArchived:
			t.Errorf("the entry of item %d is archived", d.Post)
		}
		entryFiles["entries/"+e.ID+".json"] = true
		revisionFiles["revisions/"+d.RevisionID+".json"] = d

		eb, err := e.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		rb, err := rev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var backEntry content.Entry
		var backRevision content.Revision
		if err := backEntry.UnmarshalJSON(eb); err != nil || !reflect.DeepEqual(backEntry, e) {
			t.Errorf("entry of item %d reads back as %+v, %v, want %+v", d.Post, backEntry, err, e)
		}
		if err := backRevision.UnmarshalJSON(rb); err != nil || !reflect.DeepEqual(backRevision, rev) {
			t.Errorf("revision of item %d reads back as %+v, %v, want %+v", d.Post, backRevision, err, rev)
		}
		out.Write(eb)
		out.Write(rb)
	}

	checkAcrossEntries(t, imp, site, docs)

	inTable := 0
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		kind, _, ok := imp.IDs.Lookup(PostObject(it))
		if isEntry := ok && kind == content.RefEntry; isEntry != (given[it.ID] != nil) {
			t.Errorf("item %d (%s) has an ID of an entry: %v; an entry was given: %v", it.ID, it.Type, isEntry, given[it.ID] != nil)
		}
	}
	for _, r := range imp.refs {
		if r.kind == content.RefEntry {
			inTable++
		}
	}
	if inTable != len(docs) {
		t.Errorf("%d references of entries, want %d", inTable, len(docs))
	}

	var text, js bytes.Buffer
	if err := imp.Report.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if err := imp.Report.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	out.Write(text.Bytes())
	out.Write(js.Bytes())
	for i := range imp.WXR.Items {
		if pw := imp.WXR.Items[i].Password; pw != "" && strings.Contains(out.String(), pw) {
			t.Errorf("the password of item %d is in the documents or the report", imp.WXR.Items[i].ID)
		}
	}
	for _, e := range imp.Report.Entries() {
		file := e.Target.File
		switch {
		case strings.HasPrefix(file, "entries/") && !entryFiles[file], strings.HasPrefix(file, "revisions/") && revisionFiles[file] == nil:
			t.Errorf("an entry of the report is about %s, which was not given: %s", file, e.Message)
		}
		// What is said of a block is said of one that is in the body that was given.
		if d := revisionFiles[file]; d != nil && (e.Target.Pointer == "/body" || strings.HasPrefix(e.Target.Pointer, "/body/")) {
			if !pointsIntoBlocks(d.Revision.Body, strings.TrimPrefix(e.Target.Pointer, "/body")) {
				t.Errorf("the entry of the report %q is about %s#%s, which is not in the body that was given", e.Message, file, e.Target.Pointer)
			}
		}
		if p := e.Target.Pointer; p != "" && !strings.HasPrefix(p, "/") {
			t.Errorf("the pointer %q of the entry %q is not a JSON Pointer", p, e.Message)
		}
	}
}

// checkAcrossEntries checks what the build checks across entities (6.7) of the entries that were given: a published page
// has a published parent, and a scheduled one a published or scheduled one; the parents make no cycle; the published and
// scheduled entries of a language and a type, and under a parent for a page, have different slugs; and the front page and
// the posts page of the site are published, so the site is valid with the records of the import.
func checkAcrossEntries(t *testing.T, imp *Import, site *content.Site, docs []*EntryDoc) {
	t.Helper()
	byID := make(map[string]*EntryDoc, len(docs))
	for _, d := range docs {
		byID[d.Entry.ID] = d
	}
	type slugKey struct{ language, typ, parent, slug string }
	slugs := make(map[slugKey]int64)
	for _, d := range docs {
		e, rev := d.Entry, d.Revision
		if rev.Parent != "" {
			switch p := byID[rev.Parent]; {
			case p == nil:
				t.Errorf("the parent of item %d is not an entry that was given", d.Post)
			case p.Entry.Type != content.TypePage || e.Type != content.TypePage:
				t.Errorf("item %d (%s) has the parent %d (%s)", d.Post, e.Type, p.Post, p.Entry.Type)
			case e.Status == content.StatusPublished && p.Entry.Status != content.StatusPublished:
				t.Errorf("the published page %d has the parent %d, which is %s", d.Post, p.Post, p.Entry.Status)
			case e.Status == content.StatusScheduled && p.Entry.Status != content.StatusPublished && p.Entry.Status != content.StatusScheduled:
				t.Errorf("the scheduled page %d has the parent %d, which is %s", d.Post, p.Post, p.Entry.Status)
			}
		}
		if e.Status != content.StatusPublished && e.Status != content.StatusScheduled {
			continue
		}
		k := slugKey{language: e.Language, typ: e.Type, slug: rev.Slug}
		if e.Type == content.TypePage {
			k.parent = rev.Parent
		}
		if other, ok := slugs[k]; ok {
			t.Errorf("the items %d and %d are published or scheduled, in the same language, type and parent, with the slug %q", other, d.Post, rev.Slug)
		}
		slugs[k] = d.Post
	}

	// Each page is on a path of parents that ends, which one walk finds for all of them.
	const onPath, done = 1, 2
	state := make(map[string]int, len(docs))
	for _, d := range docs {
		var path []string
		cur := d
		for cur != nil && state[cur.Entry.ID] == 0 {
			state[cur.Entry.ID] = onPath
			path = append(path, cur.Entry.ID)
			cur = byID[cur.Revision.Parent]
		}
		if cur != nil && state[cur.Entry.ID] == onPath {
			t.Errorf("the parents of item %d make a cycle", cur.Post)
		}
		for _, id := range path {
			state[id] = done
		}
	}

	for _, l := range site.Languages {
		for pointer, ids := range map[string]content.Localized{"frontPage": site.FrontPage, "postsPage": site.PostsPage} {
			if id := ids[l]; id != "" && (byID[id] == nil || byID[id].Entry.Status != content.StatusPublished) {
				t.Errorf("the %s of the site in %s is the entry %q, which is not a published entry that was given", pointer, l, id)
			}
		}
	}
	if err := site.Validate(imp.Ref); err != nil {
		t.Errorf("the site after the entries: %v", err)
	}
}

func docOf(docs []*EntryDoc, post int64) *EntryDoc {
	for _, d := range docs {
		if d.Post == post {
			return d
		}
	}
	return nil
}

// itemLines returns the entries of the report that are about the item with the post ID post, each as
// "<kind>: <message> -> <file>#<pointer>".
func itemLines(rep *Report, post int64) []string {
	var lines []string
	for _, e := range rep.Entries() {
		if e.Source.Record != recordItem || e.Source.ID != post {
			continue
		}
		s := string(e.Kind) + ": " + e.Message
		if e.Target != (Target{}) {
			s += " -> " + e.Target.File
			if e.Target.Pointer != "" {
				s += "#" + e.Target.Pointer
			}
		}
		lines = append(lines, s)
	}
	return lines
}

// wantItem checks the report about the item post, in any order. {E} in want is the file of the state of the entry, and
// {R} the file of its revision.
func wantItem(t *testing.T, imp *Import, docs []*EntryDoc, post int64, want ...string) {
	t.Helper()
	files := strings.NewReplacer()
	if d := docOf(docs, post); d != nil {
		files = strings.NewReplacer("{E}", "entries/"+d.Entry.ID+".json", "{R}", "revisions/"+d.RevisionID+".json")
	}
	exp := make([]string, len(want))
	for i, w := range want {
		exp[i] = files.Replace(w)
	}
	got := itemLines(imp.Report, post)
	slices.Sort(got)
	slices.Sort(exp)
	if slices.Equal(got, exp) {
		return
	}
	cut := func(lines []string) string {
		var out []string
		for _, l := range lines {
			if len(l) > 300 {
				l = l[:150] + " ... " + l[len(l)-150:]
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n  ")
	}
	t.Errorf("the report about item %d is\n  %s\nwant\n  %s", post, cut(got), cut(exp))
}

// itemWho is the start of a line of the report, up to the colon after the kind, for the item with the post ID post.
func itemWho(t *testing.T, e *wxr.Export, kind string, post int64) string {
	t.Helper()
	it := e.Post(post)
	if it == nil {
		t.Fatalf("no item %d", post)
	}
	what := fmt.Sprintf("item %d (%s)", it.ID, typeText(it.Type))
	if it.Title != "" {
		what = fmt.Sprintf("item %d (%s %q)", it.ID, typeText(it.Type), it.Title)
	}
	return fmt.Sprintf("site.xml:%d:%d: %s: %s: ", it.Line, it.Column, kind, what)
}

// entryState lists what the table and the references say of each item and the references, to compare before and after.
func entryState(imp *Import) string {
	var b strings.Builder
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		kind, id, ok := imp.IDs.Lookup(PostObject(it))
		ref, refOK := imp.Ref(content.RefEntry, id)
		fmt.Fprintf(&b, "%d: %q %q %v %+v %v\n", it.ID, kind, id, ok, ref, refOK)
	}
	fmt.Fprintf(&b, "%d references\n", len(imp.refs))
	return b.String()
}

// entriesText writes the documents and the report of an import, for the tests that compare two.
func entriesText(t *testing.T, imp *Import, docs []*EntryDoc) string {
	t.Helper()
	var b strings.Builder
	for _, d := range docs {
		eb, err := d.Entry.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		rb, err := d.Revision.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %s\n%s\n%s\n", d.Post, d.RevisionID, eb, rb)
	}
	var js bytes.Buffer
	if err := imp.Report.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	return b.String() + reportText(t, imp.Report) + js.String()
}

func TestPlanEntries(t *testing.T) {
	in := entriesWXR("",
		wxrEntry{id: 1, title: "Post", name: "post"},
		wxrEntry{id: 2, typ: "page", status: "draft"},
		wxrEntry{id: 3, typ: "works", status: "private"},
		wxrEntry{id: 4, typ: "product"},
		wxrEntry{id: 5, typ: "attachment", status: "inherit"},
		wxrEntry{id: 6, typ: "nav_menu_item", status: "publish"},
		wxrEntry{id: 7, status: "trash"},
		wxrEntry{id: 8, typ: "page", status: "auto-draft"},
		wxrEntry{id: 9, typ: "works", status: "trash"},
		wxrEntry{id: 10, status: "pending"},
		wxrEntry{id: 11, status: "future"},
		wxrEntry{id: 12, status: "wc-completed"},
		wxrEntry{id: 13, typ: "revision", status: "inherit"},
	)
	plan := func(t *testing.T, s *Settings, defs *content.Site) *Import {
		t.Helper()
		e, rep := readString(t, "site.xml", in)
		imp := New("site.xml", e, rep)
		if err := imp.PlanEntries(s, defs); err != nil {
			t.Fatalf("PlanEntries = %v", err)
		}
		return imp
	}
	check := func(t *testing.T, imp *Import, language string, want map[int64]string) {
		t.Helper()
		for i := range imp.WXR.Items {
			it := &imp.WXR.Items[i]
			kind, id, ok := imp.IDs.Lookup(PostObject(it))
			typ, planned := want[it.ID]
			if ok != planned || (ok && kind != content.RefEntry) {
				t.Errorf("Lookup of item %d = %q, %q, %v; planned: %v", it.ID, kind, id, ok, planned)
				continue
			}
			if !planned {
				continue
			}
			if r, ok := imp.Ref(content.RefEntry, id); !ok || r != (content.Ref{Type: typ, Language: language}) {
				t.Errorf("Ref of item %d = %+v, %v, want the type %q and the language %q", it.ID, r, ok, typ, language)
			}
			if err := content.CheckID(content.RefEntry, id); err != nil {
				t.Errorf("the ID of item %d: %v", it.ID, err)
			}
			if want := imp.IDs.NewID(content.RefEntry, PostObject(it).key()); id != want {
				t.Errorf("the ID of item %d = %q, want %q", it.ID, id, want)
			}
		}
		if len(imp.refs) != len(want) {
			t.Errorf("%d references, want %d", len(imp.refs), len(want))
		}
		if lines := reportLines(t, imp.Report); len(lines) != 0 {
			t.Errorf("the report has %q, want nothing", lines)
		}
	}
	planned := map[int64]string{1: "post", 2: "page", 3: "works", 10: "post", 11: "post", 12: "post"}

	t.Run("every type that the schema file has", func(t *testing.T) {
		imp := plan(t, entrySettings(), entryDefs())
		check(t, imp, "ja", planned)
		first := entryState(imp)
		if err := imp.PlanEntries(entrySettings(), entryDefs()); err != nil {
			t.Fatal(err)
		}
		check(t, imp, "ja", planned)
		if got := entryState(imp); got != first {
			t.Errorf("a second PlanEntries changed the plan:\n%s\nwas\n%s", got, first)
		}
	})
	t.Run("the language of the settings", func(t *testing.T) {
		s := entrySettings()
		s.Site.Language = "en"
		check(t, plan(t, s, entryDefs()), "en", planned)
	})
	t.Run("no schema file", func(t *testing.T) {
		check(t, plan(t, entrySettings(), nil), "ja", map[int64]string{1: "post", 2: "page", 10: "post", 11: "post", 12: "post"})
	})
	t.Run("a schema file without the type", func(t *testing.T) {
		check(t, plan(t, entrySettings(), &content.Site{}), "ja", map[int64]string{1: "post", 2: "page", 10: "post", 11: "post", 12: "post"})
	})
	t.Run("no settings", func(t *testing.T) {
		e, rep := readString(t, "site.xml", in)
		imp := New("site.xml", e, rep)
		err := imp.PlanEntries(nil, entryDefs())
		if err == nil || err.Error() != "the settings are needed to plan the entries" {
			t.Errorf("PlanEntries = %v, want the error for settings that are missing", err)
		}
		check(t, imp, "ja", nil)
	})
	t.Run("an item that is a medium already", func(t *testing.T) {
		e, rep := readString(t, "site.xml", in)
		imp := New("site.xml", e, rep)
		if _, err := imp.IDs.Assign(PostObject(imp.WXR.Post(10)), content.RefMedia); err != nil {
			t.Fatal(err)
		}
		err := imp.PlanEntries(entrySettings(), entryDefs())
		if err == nil || err.Error() != "post 10 is already imported as media" {
			t.Errorf("PlanEntries = %v, want the error for the item that is a medium", err)
		}
	})
	t.Run("an item that was dropped", func(t *testing.T) {
		imp := plan(t, entrySettings(), entryDefs())
		_, id, _ := imp.IDs.Lookup(PostObject(imp.WXR.Post(2)))
		imp.IDs.Drop(PostObject(imp.WXR.Post(2)))
		imp.DropRef(id)
		if err := imp.PlanEntries(entrySettings(), entryDefs()); err != nil {
			t.Fatal(err)
		}
		check(t, imp, "ja", planned)
	})
}

func TestEntriesFields(t *testing.T) {
	in := entriesWXR("",
		wxrEntry{id: 7, title: "Hello", name: "hello", excerpt: "Short", date: "2026-10-05 09:45:00", dateGMT: "2026-10-05 00:45:00"},
		wxrEntry{id: 8, typ: "page", title: "About", name: "about", status: "draft"},
		wxrEntry{id: 9, status: "future", title: "Soon", name: "soon", date: "2027-01-02 03:04:05", dateGMT: "2027-01-01 18:04:05"},
	)
	imp, _, docs := makeEntries(t, nil, nil, in)
	if len(docs) != 3 {
		t.Fatalf("%d entries, want 3", len(docs))
	}
	idOfPost := func(post int64) string {
		_, id, ok := imp.IDs.Lookup(PostObject(imp.WXR.Post(post)))
		if !ok {
			t.Fatalf("item %d has no ID", post)
		}
		return id
	}
	for _, w := range []struct {
		post  int64
		entry content.Entry
		rev   content.Revision
	}{
		{
			7,
			content.Entry{
				Type: "post", Language: "ja", Number: 7, Status: content.StatusPublished, CreatedAt: entryTime,
			},
			content.Revision{Title: "Hello", Slug: "hello", Excerpt: "Short", Date: entryTime},
		},
		{
			8,
			content.Entry{Type: "page", Language: "ja", Number: 8, Status: content.StatusDraft, CreatedAt: entryTime},
			content.Revision{Title: "About", Slug: "about"},
		},
		{
			9,
			content.Entry{
				Type: "post", Language: "ja", Number: 9, Status: content.StatusScheduled, CreatedAt: "2027-01-02T03:04:05+09:00",
				ScheduledAt: "2027-01-02T03:04:05+09:00",
			},
			content.Revision{Title: "Soon", Slug: "soon", Date: "2027-01-02T03:04:05+09:00"},
		},
	} {
		d := docOf(docs, w.post)
		it := imp.WXR.Post(w.post)
		revID := imp.IDs.NewID(content.RefRevision, PostObject(it).key())
		w.entry.ID = idOfPost(w.post)
		w.entry.TranslationGroup = imp.IDs.NewID(content.RefTranslationGroup, PostObject(it).key())
		w.entry.CreatedBy = mediaTestBy
		switch w.entry.Status {
		case content.StatusPublished:
			w.entry.PublishedRevision = revID
		case content.StatusScheduled:
			w.entry.ScheduledRevision = revID
		case content.StatusDraft, content.StatusArchived:
		}
		if d == nil {
			t.Fatalf("no entry for item %d", w.post)
		}
		if d.Entry != w.entry || d.RevisionID != revID || !reflect.DeepEqual(d.Revision, w.rev) || d.Post != w.post {
			t.Errorf("item %d: entry %+v, revision %s %+v\nwant %+v, %s %+v", w.post, d.Entry, d.RevisionID, d.Revision, w.entry, revID, w.rev)
		}
	}
	if lines := reportLines(t, imp.Report); len(lines) != 0 {
		t.Errorf("the report has %q, want nothing", lines)
	}

	// The settings of the options are not needed yet, and the type and the language come from the plan.
	imp2, site2 := planEntries(t, nil, nil, in)
	var docs2 []*EntryDoc
	err := imp2.Entries(context.Background(), EntryOptions{Site: site2, By: mediaTestBy}, func(d *EntryDoc) error {
		docs2 = append(docs2, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entriesText(t, imp2, docs2), entriesText(t, imp, docs); got != want {
		t.Errorf("without Settings, the entries are\n%s\nwant\n%s", got, want)
	}
}

// TestEntriesIDs pins the IDs, which are the same for the same WXR every time (6.10.5): the ID of the entry is the example
// of the design document, and the keys of the revision and of the translation group are those of the post.
func TestEntriesIDs(t *testing.T) {
	_, _, docs := makeEntries(t, nil, nil, entriesWXR("", wxrEntry{id: 123, title: "T", name: "t"}))
	if len(docs) != 1 {
		t.Fatalf("%d entries, want 1", len(docs))
	}
	d := docs[0]
	for _, w := range []struct{ what, got, want string }{
		{"entry", d.Entry.ID, "ent_0000000000QNCGZPVAWT8HNPBA"},
		{"revision", d.RevisionID, "rev_000000000000M72QAFCP2MHQV8"},
		{"translation group", d.Entry.TranslationGroup, "tg_0000000000QWNKG5SH4RTBEB54"},
	} {
		if w.got != w.want {
			t.Errorf("the ID of the %s = %q, want %q", w.what, w.got, w.want)
		}
	}
}

func TestEntriesLanguage(t *testing.T) {
	s := entrySettings()
	s.Site.Language = "en"
	imp, _, docs := makeEntries(t, s, nil, entriesWXR("", wxrEntry{id: 1, title: "A", name: "a"}, wxrEntry{id: 2, typ: "works", title: "B", name: "b"}))
	if len(docs) != 2 || docs[0].Entry.Language != "en" || docs[1].Entry.Language != "en" || docs[1].Entry.Type != "works" {
		t.Fatalf("entries = %+v", docs)
	}
	if r, ok := imp.Ref(content.RefEntry, docs[0].Entry.ID); !ok || r.Language != "en" {
		t.Errorf("Ref = %+v, %v", r, ok)
	}
}

func TestEntriesOrder(t *testing.T) {
	in := entriesWXR("",
		wxrEntry{id: 30, title: "A", name: "a"},
		wxrEntry{id: 5, typ: "page", title: "B", name: "b"},
		wxrEntry{id: 40, status: "trash", title: "C", name: "c"},
		wxrEntry{id: 10, typ: "works", title: "D", name: "d", status: "draft"},
		wxrEntry{id: 20, typ: "attachment", status: "inherit"},
		wxrEntry{id: 1, title: "E", name: "e", status: "future"},
		wxrEntry{id: 2, typ: "product"},
		wxrEntry{id: 100, title: "F", name: "f"},
	)
	_, _, docs := makeEntries(t, nil, nil, in)
	var posts []int64
	for _, d := range docs {
		posts = append(posts, d.Post)
	}
	if want := []int64{30, 5, 10, 1, 100}; !slices.Equal(posts, want) {
		t.Errorf("the entries are of the items %v, want %v", posts, want)
	}
}

func TestEntriesStatus(t *testing.T) {
	const (
		protected  = "the entry was protected by a password; it is a draft here, and the password is not imported"
		noPassword = "the password of the entry is not imported"
	)
	for _, tt := range []struct {
		name, status, password string
		want                   content.Status
		lines                  []string
	}{
		{"publish", "publish", "", content.StatusPublished, nil},
		{"future", "future", "", content.StatusScheduled, nil},
		{"draft", "draft", "", content.StatusDraft, nil},
		{"pending", "pending", "", content.StatusDraft, []string{`changed: the status "pending" is a draft here -> {E}#/status`}},
		{"private", "private", "", content.StatusDraft, []string{"review: the entry was private; it is a draft here -> {E}#/status"}},
		{"inherit", "inherit", "", content.StatusDraft, []string{`changed: the status "inherit" is a draft here -> {E}#/status`}},
		{"a status of a plugin", "wc-completed", "", content.StatusDraft, []string{`changed: the status "wc-completed" is a draft here -> {E}#/status`}},
		{"publish with a password", "publish", entryPassword, content.StatusDraft, []string{"review: " + protected + " -> {E}#/status"}},
		{"future with a password", "future", entryPassword, content.StatusDraft, []string{"review: " + protected + " -> {E}#/status"}},
		{"draft with a password", "draft", entryPassword, content.StatusDraft, []string{"review: " + noPassword + " -> {E}#/status"}},
		{
			"pending with a password", "pending", entryPassword, content.StatusDraft,
			[]string{`changed: the status "pending" is a draft here -> {E}#/status`, "review: " + noPassword + " -> {E}#/status"},
		},
		{
			"private with a password", "private", entryPassword, content.StatusDraft,
			[]string{"review: the entry was private and protected by a password; it is a draft here, and the password is not imported -> {E}#/status"},
		},
		{
			"a status of a plugin with a password", "wc-completed", entryPassword, content.StatusDraft,
			[]string{`changed: the status "wc-completed" is a draft here -> {E}#/status`, "review: " + noPassword + " -> {E}#/status"},
		},
	} {
		for _, typ := range []string{"post", "page", "works"} {
			t.Run(tt.name+" "+typ, func(t *testing.T) {
				in := entriesWXR("", wxrEntry{id: 5, typ: typ, status: tt.status, password: tt.password, title: "Title", name: "slug"})
				imp, _, docs := makeEntries(t, nil, nil, in)
				if len(docs) != 1 {
					t.Fatalf("%d entries, want 1", len(docs))
				}
				if got := docs[0].Entry.Status; got != tt.want {
					t.Errorf("status = %q, want %q", got, tt.want)
				}
				wantItem(t, imp, docs, 5, tt.lines...)
			})
		}
	}
}

func TestEntriesDates(t *testing.T) {
	notPublished := func(why string) string {
		return "review: the entry cannot be published: " + why + "; it is a draft here -> {E}#/status"
	}
	postDate := func(date string) string {
		return fmt.Sprintf(`changed: the post_date %q cannot be read; the entry was created at its post_modified here -> {E}#/createdAt`, date)
	}
	for _, tt := range []struct {
		name                 string
		item                 wxrEntry
		created, date, sched string
		status               content.Status
		lines                []string
		dropped              bool
	}{
		{name: "the offset of Japan", item: wxrEntry{}, created: entryTime, date: entryTime, status: content.StatusPublished},
		{
			name: "no offset", item: wxrEntry{date: entryDate, dateGMT: entryDate},
			created: "2026-10-05T09:45:00Z", date: "2026-10-05T09:45:00Z", status: content.StatusPublished,
		},
		{
			name: "a half hour", item: wxrEntry{date: entryDate, dateGMT: "2026-10-05 04:15:00"},
			created: "2026-10-05T09:45:00+05:30", date: "2026-10-05T09:45:00+05:30", status: content.StatusPublished,
		},
		{
			name: "behind UTC", item: wxrEntry{date: "2026-01-05 09:45:00", dateGMT: "2026-01-05 17:45:00"},
			created: "2026-01-05T09:45:00-08:00", date: "2026-01-05T09:45:00-08:00", status: content.StatusPublished,
		},
		{
			name: "an offset that is too large is the time zone of the site", item: wxrEntry{date: entryDate, dateGMT: "2026-10-04 00:00:00"},
			created: entryTime, date: entryTime, status: content.StatusPublished,
		},
		{
			name: "a draft whose date was not decided", item: wxrEntry{status: "draft", dateGMT: zeroDate},
			created: entryTime, status: content.StatusDraft,
		},
		{
			name: "a pending post whose date was not decided", item: wxrEntry{status: "pending", dateGMT: zeroDate},
			created: entryTime, status: content.StatusDraft,
			lines: []string{`changed: the status "pending" is a draft here -> {E}#/status`},
		},
		{
			name: "a draft without the date of GMT", item: wxrEntry{status: "draft", dateGMT: omit},
			created: entryTime, status: content.StatusDraft,
		},
		{name: "a draft whose date was decided", item: wxrEntry{status: "draft"}, created: entryTime, date: entryTime, status: content.StatusDraft},
		{
			name: "a post that was private", item: wxrEntry{status: "private", dateGMT: zeroDate},
			created: entryTime, date: entryTime, status: content.StatusDraft,
			lines: []string{"review: the entry was private; it is a draft here -> {E}#/status"},
		},
		{
			name: "a post that is published without the date of GMT", item: wxrEntry{dateGMT: zeroDate},
			created: entryTime, date: entryTime, status: content.StatusPublished,
		},
		{name: "a page has no date", item: wxrEntry{typ: "page"}, created: entryTime, status: content.StatusPublished},
		{name: "a custom type has no date", item: wxrEntry{typ: "works"}, created: entryTime, status: content.StatusPublished},
		{
			name: "a scheduled post", item: wxrEntry{status: "future"},
			created: entryTime, date: entryTime, sched: entryTime, status: content.StatusScheduled,
		},
		{
			name: "a scheduled page", item: wxrEntry{typ: "page", status: "future"},
			created: entryTime, sched: entryTime, status: content.StatusScheduled,
		},
		{
			name: "a scheduled post in the past", item: wxrEntry{status: "future", date: "2001-02-03 04:05:06", dateGMT: "2001-02-02 19:05:06"},
			created: "2001-02-03T04:05:06+09:00", date: "2001-02-03T04:05:06+09:00", sched: "2001-02-03T04:05:06+09:00",
			status: content.StatusScheduled,
		},
		{
			name: "a post whose date cannot be read", item: wxrEntry{date: zeroDate, dateGMT: zeroDate},
			created: entryModifiedAt, status: content.StatusDraft,
			lines: []string{postDate(zeroDate), notPublished("/date: missing date")},
		},
		{
			name: "a draft whose date cannot be read", item: wxrEntry{status: "draft", date: zeroDate, dateGMT: zeroDate},
			created: entryModifiedAt, status: content.StatusDraft, lines: []string{postDate(zeroDate)},
		},
		{
			name: "a scheduled post whose date cannot be read", item: wxrEntry{status: "future", date: zeroDate, dateGMT: zeroDate},
			created: entryModifiedAt, status: content.StatusDraft,
			lines: []string{
				postDate(zeroDate),
				`review: the entry cannot be scheduled: its post_date "0000-00-00 00:00:00" cannot be read; it is a draft here -> {E}#/status`,
			},
		},
		{
			name: "a page whose date cannot be read", item: wxrEntry{typ: "page", date: "not a date", dateGMT: zeroDate},
			created: entryModifiedAt, status: content.StatusPublished, lines: []string{postDate("not a date")},
		},
		{
			name: "a post without a date", item: wxrEntry{status: "draft", date: omit, dateGMT: omit},
			created: entryModifiedAt, status: content.StatusDraft, lines: []string{postDate("")},
		},
		{
			name: "the modification has its own offset", item: wxrEntry{status: "draft", date: zeroDate, dateGMT: zeroDate, modified: "2026-10-06 10:00:00", modifiedGMT: "2026-10-06 04:30:00"},
			created: "2026-10-06T10:00:00+05:30", status: content.StatusDraft, lines: []string{postDate(zeroDate)},
		},
		{
			name: "the modification without the date of GMT", item: wxrEntry{status: "draft", date: zeroDate, modifiedGMT: omit},
			created: entryModifiedAt, status: content.StatusDraft, lines: []string{postDate(zeroDate)},
		},
		{
			name: "no date can be read", item: wxrEntry{date: zeroDate, dateGMT: zeroDate, modified: zeroDate, modifiedGMT: zeroDate}, dropped: true,
			lines: []string{`skipped: the entry is not imported: its post_date "0000-00-00 00:00:00" and post_modified "0000-00-00 00:00:00" cannot be read`},
		},
		{
			name: "there is no date", item: wxrEntry{date: omit, dateGMT: omit, modified: omit, modifiedGMT: omit}, dropped: true,
			lines: []string{`skipped: the entry is not imported: its post_date "" and post_modified "" cannot be read`},
		},
		{
			name: "the modification cannot be read, and is not needed", item: wxrEntry{modified: "garbage", modifiedGMT: "garbage"},
			created: entryTime, date: entryTime, status: content.StatusPublished,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := tt.item
			item.id, item.title, item.name = 3, "Title", "slug"
			in := entriesWXR("", item, wxrEntry{id: 4, title: "Other", name: "other"})
			imp, _, docs := makeEntries(t, nil, nil, in)
			wantItem(t, imp, docs, 3, tt.lines...)
			d := docOf(docs, 3)
			if tt.dropped {
				if d != nil || len(docs) != 1 {
					t.Fatalf("entries %v, want only the one of the item 4", docs)
				}
				if kind, id, ok := imp.IDs.Lookup(PostObject(imp.WXR.Post(3))); ok {
					t.Errorf("the item that is not imported has the ID %q of %s", id, kind)
				}
				return
			}
			if d == nil {
				t.Fatal("no entry")
			}
			if d.Entry.CreatedAt != tt.created || d.Revision.Date != tt.date || d.Entry.ScheduledAt != tt.sched || d.Entry.Status != tt.status {
				t.Errorf("createdAt %q, date %q, scheduledAt %q, status %q\nwant %q, %q, %q, %q",
					d.Entry.CreatedAt, d.Revision.Date, d.Entry.ScheduledAt, d.Entry.Status, tt.created, tt.date, tt.sched, tt.status)
			}
		})
	}
}

func TestEntriesTitle(t *testing.T) {
	complex := strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000)
	tooLong := strings.Repeat("a", content.MaxTextBytes+1)
	noTitle := "review: the entry cannot be published: /title: missing title; it is a draft here -> {E}#/status"
	replaced := "changed: line breaks and tabs in the title are spaces here -> {R}#/title"
	for _, tt := range []struct {
		name, title, status string
		typ                 string
		want                string // "" if the entry has no title, which a draft is
		lines               []string
	}{
		{name: "text", title: "Hello, world", want: "Hello, world"},
		{name: "a character reference", title: "Q&amp;A &lt;3 &#65;", want: "Q&A <3 A"},
		{name: "a less-than sign that starts no tag", title: "1 < 2", want: "1 < 2"},
		{name: "a less-than sign by a reference", title: "x&lt;y", want: "x<y"},
		{
			name: "a tag", title: "<em>Hello</em> there", want: "Hello there",
			lines: []string{"changed: kept only the text of the HTML; lost <em> -> {R}#/title"},
		},
		{
			name: "tags and a link", title: `<b>A</b> <a href="https://example.com/">B</a>`, want: "A B",
			lines: []string{`changed: kept only the text of the HTML; lost <b>, a link to "https://example.com/" -> {R}#/title`},
		},
		{name: "a line break element", title: "Line<br>two", want: "Line two", lines: []string{replaced}},
		{name: "line breaks and tabs", title: "a\nb\tc\r\nd", want: "a b c d", lines: []string{replaced}},
		{name: "space at the ends", title: "  A  B \t", want: "A  B"},
		{name: "NFC", title: "e" + string(rune(0x301)), want: string(rune(0xE9))},
		{name: "a japanese title", title: "お知らせ", want: "お知らせ"},
		{name: "the longest title", title: strings.Repeat("a", content.MaxTextBytes), want: strings.Repeat("a", content.MaxTextBytes)},
		{name: "no title", title: "", lines: []string{noTitle}},
		{name: "a title of space", title: " \n\t ", lines: []string{noTitle}},
		{name: "no title of a scheduled post", title: "", status: "future", lines: []string{noTitle}},
		{name: "no title of a page", title: "", typ: "page", lines: []string{noTitle}},
		{name: "no title of a custom type", title: "", typ: "works", lines: []string{noTitle}},
		{name: "no title of a draft", title: "", status: "draft"},
		{name: "a title of tags only", title: "<img src=\"/a.png\">", lines: []string{
			`changed: kept only the text of the HTML; lost an image "/a.png" -> {R}#/title`,
			noTitle,
		}},
		{name: "a title that is too complex", title: complex, lines: []string{
			"skipped: the title is not imported: it is too complex to read as HTML -> {R}#/title", noTitle,
		}},
		{name: "a title that is too long", title: tooLong, lines: []string{
			"skipped: the title is not imported: it is too long: 1048577 bytes (max 1048576) -> {R}#/title", noTitle,
		}},
		{name: "a title of a draft that is too long", title: tooLong, status: "draft", lines: []string{
			"skipped: the title is not imported: it is too long: 1048577 bytes (max 1048576) -> {R}#/title",
		}},
		{name: "a title that is too long after the characters are joined", title: strings.Repeat(string(rune(0x344)), content.MaxTextBytes/2), lines: []string{
			"skipped: the title is not imported: it is too long: 2097152 bytes (max 1048576) -> {R}#/title", noTitle,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := wxrEntry{id: 9, typ: cmp.Or(tt.typ, "post"), status: tt.status, title: tt.title, name: "slug"}
			imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", item))
			if len(docs) != 1 {
				t.Fatalf("%d entries, want 1", len(docs))
			}
			d := docs[0]
			if d.Revision.Title != tt.want {
				t.Errorf("title = %.60q (%d bytes), want %.60q (%d bytes)", d.Revision.Title, len(d.Revision.Title), tt.want, len(tt.want))
			}
			want := map[string]content.Status{"": content.StatusPublished, "future": content.StatusScheduled, "draft": content.StatusDraft}[tt.status]
			if tt.want == "" {
				want = content.StatusDraft
			}
			if got := d.Entry.Status; got != want {
				t.Errorf("status = %q, want %q", got, want)
			}
			wantItem(t, imp, docs, 9, tt.lines...)
		})
	}
}

func TestEntriesSlug(t *testing.T) {
	const noneLeft = `changed: nothing is left of the slug %q; it is "entry-5" here -> {R}#/slug`
	for _, tt := range []struct {
		name, slug, status string
		typ                string
		want               string
		lines              []string
		draft              bool // the entry is a draft in the end
	}{
		{name: "a slug", slug: "hello-world", want: "hello-world"},
		{name: "a japanese slug", slug: "%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b", want: "お知らせ"},
		{name: "a slug of a page", slug: "about", typ: "page", want: "about"},
		{name: "a slug of a custom type", slug: "work-1", typ: "works", want: "work-1"},
		{
			name: "upper case", slug: "Hello-World", want: "hello-world",
			lines: []string{`changed: the slug "Hello-World" is "hello-world" here: normalized to lower case and NFC -> {R}#/slug`},
		},
		{
			name: "characters that a slug cannot have", slug: "a.b!c", want: "a-b-c",
			lines: []string{`changed: the slug "a.b!c" is "a-b-c" here: characters that a slug cannot have are "-" -> {R}#/slug`},
		},
		{
			name: "a hyphen at the ends", slug: "-abc-", want: "abc",
			lines: []string{`changed: the slug "-abc-" is "abc" here: "-" at the ends is removed -> {R}#/slug`},
		},
		{
			name: "more than 200 bytes", slug: strings.Repeat("a", 201), want: strings.Repeat("a", 200),
			lines: []string{fmt.Sprintf(`changed: the slug %q is %q here: it is cut to 200 bytes -> {R}#/slug`, strings.Repeat("a", 201), strings.Repeat("a", 200))},
		},
		{
			name: "a percent-encoding that cannot be undone", slug: "a%ffb", want: "a-ffb",
			lines: []string{`changed: the slug "a%ffb" is "a-ffb" here: it cannot be decoded: not valid UTF-8 after decoding; characters that a slug cannot have are "-" -> {R}#/slug`},
		},
		{name: "nothing is left", slug: "!!!", want: "entry-5", lines: []string{fmt.Sprintf(noneLeft, "!!!")}},
		{name: "nothing is left of the slug of a draft", slug: "!!!", status: "draft", want: "entry-5", lines: []string{fmt.Sprintf(noneLeft, "!!!")}, draft: true},
		{name: "no slug of a draft", slug: "", status: "draft", want: "", draft: true},
		{
			name: "no slug", slug: "", want: "entry-5",
			lines: []string{`changed: the entry has no slug; it is "entry-5" here -> {R}#/slug`},
		},
		{
			name: "no slug of a page", slug: "", typ: "page", want: "entry-5",
			lines: []string{`changed: the entry has no slug; it is "entry-5" here -> {R}#/slug`},
		},
		{
			name: "no slug of a scheduled entry", slug: "", status: "future", want: "entry-5",
			lines: []string{`changed: the entry has no slug; it is "entry-5" here -> {R}#/slug`},
		},
		{
			name: "no slug of a private entry", slug: "", status: "private", want: "", draft: true,
			lines: []string{"review: the entry was private; it is a draft here -> {E}#/status"},
		},
		{
			name: "no slug of a pending entry", slug: "", status: "pending", want: "", draft: true,
			lines: []string{`changed: the status "pending" is a draft here -> {E}#/status`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := wxrEntry{id: 5, typ: tt.typ, status: tt.status, title: "Title", name: tt.slug}
			imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", item))
			if len(docs) != 1 {
				t.Fatalf("%d entries, want 1", len(docs))
			}
			if got := docs[0].Revision.Slug; got != tt.want {
				t.Errorf("slug = %q, want %q", got, tt.want)
			}
			if got := docs[0].Entry.Status == content.StatusDraft; got != (tt.draft || tt.status == "draft") {
				t.Errorf("status = %q", docs[0].Entry.Status)
			}
			wantItem(t, imp, docs, 5, tt.lines...)
		})
	}
}

// TestEntriesSlugFirst checks which slugs are WordPress's own, so that a conversion of the slugs of all the entries can
// keep them.
func TestEntriesSlugFirst(t *testing.T) {
	for _, tt := range []struct {
		name, slug, status string
		want               bool
	}{
		{"a slug", "hello", "publish", true},
		{"a percent-encoded slug", "%e3%81%8a", "publish", true},
		{"a slug of a draft", "hello", "draft", true},
		{"upper case", "Hello", "publish", false},
		{"characters that a slug cannot have", "a.b", "publish", false},
		{"nothing is left", "!!", "publish", false},
		{"no slug", "", "publish", false},
		{"no slug of a draft", "", "draft", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, site := planEntries(t, nil, nil, entriesWXR("", wxrEntry{id: 5, status: tt.status, title: "T", name: tt.slug}))
			c := &entryConv{imp: imp, site: site, opts: EntryOptions{Site: site, By: mediaTestBy}}
			items, _, err := c.collect()
			if err != nil || len(items) != 1 {
				t.Fatalf("collect = %v, %v", items, err)
			}
			ei := items[0]
			c.setStatus(ei)
			c.setSlug(ei)
			if ei.slugFirst != tt.want {
				t.Errorf("slugFirst = %v, want %v (the slug is %q)", ei.slugFirst, tt.want, ei.rev.Slug)
			}
		})
	}
}

func TestEntriesExcerpt(t *testing.T) {
	complex := strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000)
	for _, tt := range []struct {
		name, excerpt string
		typ           string
		want          string
		lines         []string
	}{
		{name: "text", excerpt: "A short text.", want: "A short text."},
		{name: "lines", excerpt: "One\nTwo\n\nThree", want: "One\nTwo\n\nThree"},
		{name: "a page", excerpt: "A page.", typ: "page", want: "A page."},
		{name: "a custom type", excerpt: "A work.", typ: "works", want: "A work."},
		{
			name: "html", excerpt: `<p>One</p><p>Two <a href="https://example.com/a">link</a></p>`, want: "One\n\nTwo link",
			lines: []string{`changed: kept only the text of the HTML; lost a link to "https://example.com/a" -> {R}#/excerpt`},
		},
		{name: "a character reference", excerpt: "A &amp; B &lt;br&gt;", want: "A & B <br>"},
		{name: "space", excerpt: "  \n\t ", want: ""},
		{name: "space around", excerpt: "\n  A  \n", want: "A"},
		{
			name: "only an image", excerpt: `<img src="/a.png">`, want: "",
			lines: []string{`changed: kept only the text of the HTML; lost an image "/a.png" -> {R}#/excerpt`},
		},
		{
			name: "too complex", excerpt: complex, want: "",
			lines: []string{"skipped: the excerpt is not imported: it is too complex to read as HTML -> {R}#/excerpt"},
		},
		{
			name: "too long", excerpt: strings.Repeat("a", content.MaxTextBytes+1), want: "",
			lines: []string{"skipped: the excerpt is not imported: it is too long: 1048577 bytes (max 1048576) -> {R}#/excerpt"},
		},
		{name: "the longest", excerpt: strings.Repeat("a", content.MaxTextBytes), want: strings.Repeat("a", content.MaxTextBytes)},
		{
			// Normalization makes the text twice as long, which the validation of the revision finds.
			name: "too long after the characters are joined", excerpt: strings.Repeat(string(rune(0x344)), content.MaxTextBytes/2), want: "",
			lines: []string{"skipped: the excerpt is not imported: too long: 2097152 bytes (max 1048576) -> {R}#/excerpt"},
		},
		{name: "NFC", excerpt: "e" + string(rune(0x301)), want: string(rune(0xE9))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := wxrEntry{id: 5, typ: tt.typ, title: "Title", name: "slug", excerpt: tt.excerpt}
			imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", item))
			if len(docs) != 1 {
				t.Fatalf("%d entries, want 1", len(docs))
			}
			if got := docs[0].Revision.Excerpt; got != tt.want {
				t.Errorf("excerpt = %.60q (%d bytes), want %.60q (%d bytes)", got, len(got), tt.want, len(tt.want))
			}
			if got := docs[0].Entry.Status; got != content.StatusPublished {
				t.Errorf("status = %q: the excerpt is not needed to publish", got)
			}
			wantItem(t, imp, docs, 5, tt.lines...)
		})
	}
}

func TestEntriesCannotBePublished(t *testing.T) {
	for _, tt := range []struct {
		name string
		item wxrEntry
		line string
	}{
		{
			"a post without a title and a date",
			wxrEntry{title: "", date: zeroDate, dateGMT: zeroDate},
			"/title: missing title; /date: missing date",
		},
		{"a post without a title", wxrEntry{title: ""}, "/title: missing title"},
		{"a post without a date", wxrEntry{title: "T", date: zeroDate, dateGMT: zeroDate}, "/date: missing date"},
		{"a scheduled post without a title", wxrEntry{title: "", status: "future"}, "/title: missing title"},
		{"a page without a title", wxrEntry{typ: "page", title: ""}, "/title: missing title"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := tt.item
			item.id, item.name = 6, "slug"
			imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", item))
			if len(docs) != 1 || docs[0].Entry.Status != content.StatusDraft {
				t.Fatalf("entries %+v, want a draft", docs)
			}
			var got []string
			for _, l := range itemLines(imp.Report, 6) {
				if strings.Contains(l, "cannot be published") {
					got = append(got, l)
				}
			}
			want := "review: the entry cannot be published: " + tt.line + "; it is a draft here -> entries/" + docs[0].Entry.ID + ".json#/status"
			if !slices.Equal(got, []string{want}) {
				t.Errorf("the record of what is missing is %q, want %q", got, want)
			}
		})
	}
}

func TestEntriesNotImported(t *testing.T) {
	in := entriesWXR("",
		wxrEntry{id: 1, title: "Hello", name: "hello"},
		wxrEntry{id: 2, title: "Old", name: "old", status: "trash", password: entryPassword},
		wxrEntry{id: 3, typ: "page", title: "Auto draft", status: "auto-draft"},
		wxrEntry{id: 4, typ: "product", title: "Mug"},
		wxrEntry{id: 5, typ: "attachment", status: "inherit"},
		wxrEntry{id: 6, typ: "nav_menu_item", title: "Menu"},
		wxrEntry{id: 7, typ: "works", title: "Trashed work", status: "trash"},
		wxrEntry{id: 8, typ: "product", title: "Cup"},
		wxrEntry{id: 9, typ: "event", title: "Party"},
		wxrEntry{id: 10, typ: "revision", status: "inherit"},
		wxrEntry{id: 11, typ: "page", title: "About", name: "about"},
		wxrEntry{id: 12, title: "Gone", name: "gone", status: "trash"},
		wxrEntry{id: 13, typ: "page", status: "auto-draft"},
	)
	imp, _, docs := makeEntries(t, nil, nil, in)
	var posts []int64
	for _, d := range docs {
		posts = append(posts, d.Post)
	}
	if want := []int64{1, 11}; !slices.Equal(posts, want) {
		t.Errorf("the entries are of the items %v, want %v", posts, want)
	}
	e := imp.WXR
	item := func(post int64) *wxr.Item { return e.Post(post) }
	skipped := func(post int64, why string) string {
		return itemWho(t, e, "skipped", post) + "the entry is not imported: " + why
	}
	typeLine := func(post int64, typ, what string) string {
		it := item(post)
		return fmt.Sprintf("site.xml:%d:%d: skipped: item (%s): %s", it.Line, it.Column, typ, what)
	}
	wantLines(t, imp.Report, []string{
		skipped(2, "it is in the trash"),
		skipped(3, "it is an auto-draft"),
		skipped(7, "it is in the trash"),
		skipped(12, "it is in the trash"),
		skipped(13, "it is an auto-draft"),
		typeLine(4, "product", "2 items of the post type product are not imported; define product in the schema file to import them"),
		typeLine(9, "event", "1 item of the post type event is not imported; define event in the schema file to import it"),
	})
	for _, post := range []int64{2, 3, 4, 7, 8, 9, 12, 13} {
		if kind, id, ok := imp.IDs.Lookup(PostObject(item(post))); ok && kind == content.RefEntry {
			t.Errorf("the item %d that is not imported has the ID %q", post, id)
		}
	}

	t.Run("an item that was planned is dropped", func(t *testing.T) {
		imp, site := planEntries(t, nil, nil, in)
		// A caller that made the IDs in another way may have planned the items that are in the trash.
		for _, post := range []int64{2, 3} {
			it := imp.WXR.Post(post)
			id, err := imp.IDs.Assign(PostObject(it), content.RefEntry)
			if err != nil {
				t.Fatal(err)
			}
			imp.AddRef(content.RefEntry, id, content.Ref{Type: typeName(it.Type, site), Language: "ja"})
		}
		docs, err := collectEntries(context.Background(), imp, nil, site)
		if err != nil {
			t.Fatal(err)
		}
		checkEntries(t, imp, site, docs)
		if len(docs) != 2 {
			t.Errorf("%d entries, want 2", len(docs))
		}
	})
}

func TestEntriesArguments(t *testing.T) {
	in := entriesWXR("", wxrEntry{id: 1, title: "A", name: "a"}, wxrEntry{id: 2, status: "trash"}, wxrEntry{id: 3, typ: "product"})
	for _, tt := range []struct {
		name string
		edit func(site *content.Site, opts *EntryOptions, emit *func(*EntryDoc) error)
		want string
	}{
		{"no site", func(_ *content.Site, opts *EntryOptions, _ *func(*EntryDoc) error) { opts.Site = nil }, "the settings of the site are needed to make entries"},
		{"no default language", func(site *content.Site, _ *EntryOptions, _ *func(*EntryDoc) error) { site.DefaultLanguage = "" }, "the default language of the site: "},
		{"a default language that is not one", func(site *content.Site, _ *EntryOptions, _ *func(*EntryDoc) error) {
			site.DefaultLanguage = "Japanese!"
		}, "the default language of the site: "},
		{"no time zone", func(site *content.Site, _ *EntryOptions, _ *func(*EntryDoc) error) { site.TimeZone = "" }, "the time zone of the site: it is empty"},
		{"a time zone that is not one", func(site *content.Site, _ *EntryOptions, _ *func(*EntryDoc) error) { site.TimeZone = "Not/AZone" }, "the time zone of the site: "},
		{"no account", func(_ *content.Site, opts *EntryOptions, _ *func(*EntryDoc) error) { opts.By = "" }, "the account that creates the entries: "},
		{"an account that is not an ID", func(_ *content.Site, opts *EntryOptions, _ *func(*EntryDoc) error) { opts.By = "admin" }, "the account that creates the entries: "},
		{"an ID of another kind than an account", func(_ *content.Site, opts *EntryOptions, _ *func(*EntryDoc) error) { opts.By = mediaTestID }, "the account that creates the entries: "},
		{"no function", func(_ *content.Site, _ *EntryOptions, emit *func(*EntryDoc) error) { *emit = nil }, "a function to take the entries is needed to make entries"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, site := planEntries(t, nil, nil, in)
			before, report := entryState(imp), reportLines(t, imp.Report)
			given := 0
			emit := func(*EntryDoc) error { given++; return nil }
			copied := *site
			opts := EntryOptions{Site: &copied, By: mediaTestBy}
			tt.edit(&copied, &opts, &emit)
			err := imp.Entries(context.Background(), opts, emit)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Entries = %v, want an error with %q", err, tt.want)
			}
			if given != 0 {
				t.Errorf("%d entries were given", given)
			}
			if got := entryState(imp); got != before {
				t.Errorf("the table and the references changed:\n%s\nwas\n%s", got, before)
			}
			if got := reportLines(t, imp.Report); !slices.Equal(got, report) {
				t.Errorf("the report changed:\n%q\nwas\n%q", got, report)
			}
		})
	}
}

func TestEntriesNotPlanned(t *testing.T) {
	in := entriesWXR("",
		wxrEntry{id: 1, title: "A", name: "a"},
		wxrEntry{id: 2, typ: "page", title: "B", name: "b"},
		wxrEntry{id: 3, status: "trash"},
		wxrEntry{id: 4, typ: "product"},
	)
	idOf := func(t *testing.T, imp *Import, post int64) string {
		t.Helper()
		_, id, ok := imp.IDs.Lookup(PostObject(imp.WXR.Post(post)))
		if !ok {
			t.Fatalf("item %d has no ID", post)
		}
		return id
	}
	for _, tt := range []struct {
		name string
		edit func(t *testing.T, imp *Import, site *content.Site)
		want string
	}{
		{"nothing is planned", func(t *testing.T, imp *Import, _ *content.Site) {
			for i := range imp.WXR.Items {
				it := &imp.WXR.Items[i]
				if _, id, ok := imp.IDs.Lookup(PostObject(it)); ok {
					imp.IDs.Drop(PostObject(it))
					imp.DropRef(id)
				}
			}
		}, "item 1 (post) is not planned as an entry; call PlanEntries before Site"},
		{"an item that is not in the table", func(t *testing.T, imp *Import, _ *content.Site) {
			imp.IDs.Drop(PostObject(imp.WXR.Post(2)))
		}, "item 2 (page) is not planned as an entry; call PlanEntries before Site"},
		{"an item without a reference", func(t *testing.T, imp *Import, _ *content.Site) {
			imp.DropRef(idOf(t, imp, 1))
		}, "item 1 (post) is not planned as an entry"},
		{"an item that is a medium", func(t *testing.T, imp *Import, _ *content.Site) {
			imp.IDs = NewTable(Seed(imp.WXR))
			id, err := imp.IDs.Assign(PostObject(imp.WXR.Post(1)), content.RefMedia)
			if err != nil {
				t.Fatal(err)
			}
			// Whatever is recorded for the medium, it is not the entry of the item.
			imp.AddRef(content.RefEntry, id, content.Ref{Type: content.TypePost, Language: "ja"})
		}, "item 1 (post) is not planned as an entry"},
		{"a type that the site does not have", func(t *testing.T, imp *Import, _ *content.Site) {
			imp.AddRef(content.RefEntry, idOf(t, imp, 1), content.Ref{Type: "ghost", Language: "ja"})
		}, "item 1 (post) is not planned as an entry"},
		{"a language that the site does not have", func(t *testing.T, imp *Import, _ *content.Site) {
			imp.AddRef(content.RefEntry, idOf(t, imp, 2), content.Ref{Type: content.TypePage, Language: "en"})
		}, "item 2 (page) is not planned as an entry"},
		{"a reference of another kind", func(t *testing.T, imp *Import, _ *content.Site) {
			imp.AddRef(content.RefMedia, idOf(t, imp, 1), content.Ref{Type: content.MediaImage})
		}, "item 1 (post) is not planned as an entry"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, site := planEntries(t, nil, nil, in)
			tt.edit(t, imp, site)
			before, report := entryState(imp), reportLines(t, imp.Report)
			docs, err := collectEntries(context.Background(), imp, nil, site)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Entries = %v, want an error with %q", err, tt.want)
			}
			if len(docs) != 0 {
				t.Errorf("%d entries were given", len(docs))
			}
			if got := entryState(imp); got != before {
				t.Errorf("the table and the references changed:\n%s\nwas\n%s", got, before)
			}
			if got := reportLines(t, imp.Report); !slices.Equal(got, report) {
				t.Errorf("the report changed:\n%q\nwas\n%q", got, report)
			}
		})
	}
}

func TestEntriesStops(t *testing.T) {
	var items []wxrEntry
	for i := 1; i <= 5; i++ {
		items = append(items, wxrEntry{id: i, title: fmt.Sprintf("Post %d", i), name: fmt.Sprintf("post-%d", i)})
	}
	// The item in the trash and the one of a type that is not imported are what Entries records first.
	items = append(items, wxrEntry{id: 6, status: "trash"}, wxrEntry{id: 7, typ: "product"})
	in := entriesWXR("", items...)
	opts := func(site *content.Site) EntryOptions { return EntryOptions{Site: site, By: mediaTestBy} }

	t.Run("ctx is done at the start", func(t *testing.T) {
		imp, site := planEntries(t, nil, nil, in)
		before, report := entryState(imp), reportLines(t, imp.Report)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		docs, err := collectEntries(ctx, imp, nil, site)
		if !errors.Is(err, context.Canceled) || len(docs) != 0 {
			t.Errorf("Entries = %v, %v; want context.Canceled and no entries", docs, err)
		}
		if got := entryState(imp); got != before {
			t.Errorf("the table and the references changed")
		}
		if got := reportLines(t, imp.Report); !slices.Equal(got, report) {
			t.Errorf("the report changed")
		}
	})
	t.Run("ctx is done after the first entry", func(t *testing.T) {
		imp, site := planEntries(t, nil, nil, in)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var posts []int64
		err := imp.Entries(ctx, opts(site), func(d *EntryDoc) error {
			posts = append(posts, d.Post)
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) || !slices.Equal(posts, []int64{1}) {
			t.Errorf("Entries = %v after the entries %v; want context.Canceled after the first", err, posts)
		}
	})
	t.Run("emit fails", func(t *testing.T) {
		imp, site := planEntries(t, nil, nil, in)
		failure := errors.New("cannot write")
		var posts []int64
		err := imp.Entries(context.Background(), opts(site), func(d *EntryDoc) error {
			posts = append(posts, d.Post)
			if len(posts) == 2 {
				return failure
			}
			return nil
		})
		if !errors.Is(err, failure) || err.Error() != failure.Error() {
			t.Errorf("Entries = %v, want the error of emit as it is", err)
		}
		if !slices.Equal(posts, []int64{1, 2}) {
			t.Errorf("the entries given are of the items %v, want 1 and 2", posts)
		}
	})
}

func TestEntriesSameOutput(t *testing.T) {
	in := entriesWXR("",
		wxrEntry{id: 1, title: "<b>One</b>", name: "One", excerpt: "<i>x</i>"},
		wxrEntry{id: 2, typ: "page", title: "", name: ""},
		wxrEntry{id: 7, typ: "page", title: "Private", name: "private", status: "private", password: entryPassword},
		wxrEntry{id: 3, status: "future", title: "Three", name: "%e3%81%8a"},
		wxrEntry{id: 4, status: "trash"},
		wxrEntry{id: 5, typ: "product"},
		wxrEntry{id: 6, status: "draft", date: zeroDate, dateGMT: zeroDate},
	)
	var texts []string
	for range 2 {
		imp, _, docs := makeEntries(t, nil, nil, in)
		texts = append(texts, entriesText(t, imp, docs))
	}
	if texts[0] != texts[1] {
		t.Errorf("two conversions of the same WXR differ:\n%s\n%s", texts[0], texts[1])
	}
	if !strings.Contains(texts[0], "entry-2") {
		t.Errorf("the output has no slug of an entry without one: %s", texts[0])
	}
}

// newTidy returns what the tests of the steps of the conversion need for the only item of the WXR in: the conversion,
// and the item after begin.
func newTidy(t *testing.T, in string) (*Import, *entryConv, *entryItem) {
	t.Helper()
	imp, site := planEntries(t, nil, nil, in)
	c := &entryConv{imp: imp, site: site, opts: EntryOptions{Site: site, By: mediaTestBy}, zone: mustLocation(t, "Asia/Tokyo")}
	items, _, err := c.collect()
	if err != nil || len(items) != 1 {
		t.Fatalf("collect = %v, %v", items, err)
	}
	if !c.begin(items[0]) {
		t.Fatal("begin: the item is not imported")
	}
	return imp, c, items[0]
}

func TestEntriesTidy(t *testing.T) {
	in := entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"})
	checkReport := func(imp *Import, ei *entryItem, lines ...string) {
		files := strings.NewReplacer("{E}", "entries/"+ei.id+".json", "{R}", "revisions/"+ei.revID+".json")
		var want []string
		for _, l := range lines {
			want = append(want, files.Replace(l))
		}
		slices.Sort(want)
		got := itemLines(imp.Report, 5)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("the report is\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
	t.Run("a revision that is valid", func(t *testing.T) {
		imp, c, ei := newTidy(t, in)
		ei.status = content.StatusPublished
		ei.rev = content.Revision{Title: "T", Slug: "t", Date: entryTime}
		c.tidy(ei)
		checkReport(imp, ei)
		if ei.status != content.StatusPublished || ei.rev.Title != "T" {
			t.Errorf("status %q, revision %+v", ei.status, ei.rev)
		}
	})
	t.Run("a title with a line break", func(t *testing.T) {
		imp, c, ei := newTidy(t, in)
		ei.status = content.StatusDraft
		ei.rev = content.Revision{Title: "a\nb", Slug: "t"}
		c.tidy(ei)
		checkReport(imp, ei, "skipped: the title is not imported: contains a line break or tab -> {R}#/title")
		if ei.rev.Title != "" || ei.rev.Slug != "t" {
			t.Errorf("revision %+v, want only the title taken out", ei.rev)
		}
	})
	t.Run("two items that are wrong", func(t *testing.T) {
		imp, c, ei := newTidy(t, in)
		ei.status = content.StatusPublished
		ei.rev = content.Revision{Title: "a\nb", Slug: "a b", Date: entryTime}
		c.tidy(ei)
		checkReport(imp, ei,
			"skipped: the title is not imported: contains a line break or tab -> {R}#/title",
			`skipped: the slug is not imported: character ' ' is not allowed in slugs: use lower case letters, digits, "-" and "_" in ASCII -> {R}#/slug`,
			"review: the entry cannot be published: /title: missing title; /slug: missing slug; it is a draft here -> {E}#/status",
		)
		if ei.status != content.StatusDraft || ei.rev.Date != entryTime {
			t.Errorf("status %q, revision %+v", ei.status, ei.rev)
		}
	})
	t.Run("an item that cannot be taken out", func(t *testing.T) {
		imp, c, ei := newTidy(t, in)
		c.takeOutUnit = func(*content.Revision, string) bool { return false }
		ei.status = content.StatusScheduled
		ei.scheduledAt = entryTime
		ei.rev = content.Revision{Title: "T", Slug: "t", Date: entryTime, Parent: "x"}
		c.tidy(ei)
		checkReport(imp, ei,
			"skipped: the content of the entry is not imported: /parent: not allowed for post -> {R}",
			"review: the entry cannot be published: /title: missing title; /slug: missing slug; /date: missing date; it is a draft here -> {E}#/status",
		)
		if !reflect.DeepEqual(ei.rev, content.Revision{}) || ei.status != content.StatusDraft {
			t.Errorf("status %q, revision %+v, want a draft with an empty revision", ei.status, ei.rev)
		}
	})
	t.Run("a unit that is wrong again after it was taken out", func(t *testing.T) {
		imp, c, ei := newTidy(t, in)
		asked := 0
		// A clearUnit that says it took the unit out and does not: tidy must end, and say what it lost.
		c.takeOutUnit = func(*content.Revision, string) bool { asked++; return true }
		ei.status = content.StatusDraft
		ei.rev = content.Revision{Title: "a\nb", Slug: "t", Excerpt: "e"}
		c.tidy(ei)
		checkReport(imp, ei,
			"skipped: the title is not imported: contains a line break or tab -> {R}#/title",
			"skipped: the content of the entry is not imported: /title: contains a line break or tab -> {R}",
		)
		if asked != 1 {
			t.Errorf("tidy asked to take the unit out %d times, want 1", asked)
		}
		if !reflect.DeepEqual(ei.rev, content.Revision{}) || ei.status != content.StatusDraft {
			t.Errorf("status %q, revision %+v, want a draft with an empty revision", ei.status, ei.rev)
		}
	})
	t.Run("a unit that stays wrong after others were taken out", func(t *testing.T) {
		imp, c, ei := newTidy(t, in)
		var units []string
		c.takeOutUnit = func(rev *content.Revision, unit string) bool {
			units = append(units, unit)
			if unit == "/slug" {
				return true
			}
			return clearUnit(rev, unit)
		}
		ei.status = content.StatusPublished
		ei.rev = content.Revision{Title: "a\nb", Slug: "a b", Date: entryTime}
		c.tidy(ei)
		const slugProblem = `character ' ' is not allowed in slugs: use lower case letters, digits, "-" and "_" in ASCII`
		checkReport(imp, ei,
			"skipped: the title is not imported: contains a line break or tab -> {R}#/title",
			"skipped: the slug is not imported: "+slugProblem+" -> {R}#/slug",
			"skipped: the content of the entry is not imported: /slug: "+slugProblem+" -> {R}",
			"review: the entry cannot be published: /title: missing title; /slug: missing slug; /date: missing date; it is a draft here -> {E}#/status",
		)
		if want := []string{"/title", "/slug"}; !slices.Equal(units, want) {
			t.Errorf("tidy asked to take out %v, want %v", units, want)
		}
		if !reflect.DeepEqual(ei.rev, content.Revision{}) || ei.status != content.StatusDraft {
			t.Errorf("status %q, revision %+v, want a draft with an empty revision", ei.status, ei.rev)
		}
	})
	t.Run("a draft is not checked for what a publication needs", func(t *testing.T) {
		imp, c, ei := newTidy(t, in)
		ei.status = content.StatusDraft
		ei.rev = content.Revision{}
		c.tidy(ei)
		checkReport(imp, ei)
	})
	t.Run("the text is normalized", func(t *testing.T) {
		_, c, ei := newTidy(t, in)
		ei.status = content.StatusDraft
		ei.rev = content.Revision{Title: "e" + string(rune(0x301)), Slug: "T", Excerpt: "a\r\nb"}
		c.tidy(ei)
		if want := (content.Revision{Title: string(rune(0xE9)), Slug: "t", Excerpt: "a\nb"}); !reflect.DeepEqual(ei.rev, want) {
			t.Errorf("revision %+v, want %+v", ei.rev, want)
		}
	})
}

func TestEntriesBeginGuard(t *testing.T) {
	imp, site := planEntries(t, nil, nil, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"}))
	c := &entryConv{imp: imp, site: site, opts: EntryOptions{Site: site, By: mediaTestBy}, zone: mustLocation(t, "Asia/Tokyo")}
	items, _, err := c.collect()
	if err != nil || len(items) != 1 {
		t.Fatalf("collect = %v, %v", items, err)
	}
	ei := items[0]
	ei.entry.Type = "ghost"
	if c.begin(ei) {
		t.Fatal("begin: an entry that does not pass Validate is imported")
	}
	want := []string{`skipped: the entry is not imported: /type: unknown type "ghost"`}
	if got := itemLines(imp.Report, 5); !slices.Equal(got, want) {
		t.Errorf("the report is %q, want %q", got, want)
	}
	if _, _, ok := imp.IDs.Lookup(PostObject(imp.WXR.Post(5))); ok {
		t.Error("the item that is not imported has an ID")
	}
	if _, ok := imp.Ref(content.RefEntry, ei.id); ok {
		t.Error("the item that is not imported has a reference")
	}
}

func TestEntriesEmitAllLetsGo(t *testing.T) {
	_, c, ei := newTidy(t, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"}))
	c.convert(ei)
	c.tidy(ei)
	var got *EntryDoc
	if err := c.emitAll(context.Background(), []*entryItem{ei}, func(d *EntryDoc) error { got = d; return nil }); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Revision.Title != "T" || got.Entry.Status != content.StatusPublished {
		t.Fatalf("the entry given is %+v", got)
	}
	if !reflect.DeepEqual(ei.rev, content.Revision{}) {
		t.Errorf("the conversion still holds the revision %+v", ei.rev)
	}
}

func TestEntriesEmitAllChecks(t *testing.T) {
	t.Run("an entry that is wrong", func(t *testing.T) {
		_, c, ei := newTidy(t, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"}))
		c.convert(ei)
		c.tidy(ei)
		ei.entry.Type = "ghost"
		err := c.emitAll(context.Background(), []*entryItem{ei}, func(*EntryDoc) error { t.Error("emit was called"); return nil })
		if err == nil || !strings.HasPrefix(err.Error(), "the entry of item 5 does not pass validation: ") {
			t.Errorf("emitAll = %v", err)
		}
	})
	t.Run("a revision that is wrong", func(t *testing.T) {
		_, c, ei := newTidy(t, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"}))
		c.convert(ei)
		c.tidy(ei)
		ei.rev.Title = ""
		err := c.emitAll(context.Background(), []*entryItem{ei}, func(*EntryDoc) error { t.Error("emit was called"); return nil })
		if err == nil || !strings.HasPrefix(err.Error(), "the entry of item 5 does not pass validation: /title: missing title") {
			t.Errorf("emitAll = %v", err)
		}
	})
}

func TestEntriesRevisionUnit(t *testing.T) {
	for _, tt := range []struct{ path, want string }{
		{"", ""},
		{"/title", "/title"},
		{"/excerpt", "/excerpt"},
		{"/terms", "/terms"},
		{"/terms/category", "/terms/category"},
		{"/terms/category/0", "/terms/category"},
		{"/seo/title", "/seo/title"},
		{"/seo", "/seo"},
		{"/fields/client/0/name", "/fields/client"},
		{"/body/0/text/1", "/body"},
		{"/featuredImage", "/featuredImage"},
	} {
		if got := revisionUnit(tt.path); got != tt.want {
			t.Errorf("revisionUnit(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestEntriesClearUnit(t *testing.T) {
	full := func() content.Revision {
		return content.Revision{
			Title: "T", Slug: "s", Excerpt: "e", Date: entryTime, Author: "a", FeaturedImage: "f", Sticky: true, Order: 3, Template: "t",
			Parent: "p", Terms: content.Terms{"category": {"c1", "c2"}, "tag": {"t1"}, "genre": {"g1"}},
			SEO: content.SEO{Title: "st", Description: "sd", OGImage: "so", NoIndex: true, Canonical: "/sc/"},
		}
	}
	for _, unit := range []string{
		"/title", "/slug", "/excerpt", "/date", "/author", "/featuredImage", "/sticky", "/order", "/template", "/parent",
		"/terms/category", "/terms/tag", "/terms/genre", "/seo/title", "/seo/description", "/seo/canonical", "/seo/ogImage",
	} {
		rev := full()
		if !clearUnit(&rev, unit) {
			t.Errorf("clearUnit(%s) = false", unit)
		}
		want := full()
		switch unit {
		case "/title":
			want.Title = ""
		case "/slug":
			want.Slug = ""
		case "/excerpt":
			want.Excerpt = ""
		case "/date":
			want.Date = ""
		case "/author":
			want.Author = ""
		case "/featuredImage":
			want.FeaturedImage = ""
		case "/sticky":
			want.Sticky = false
		case "/order":
			want.Order = 0
		case "/template":
			want.Template = ""
		case "/parent":
			want.Parent = ""
		case "/seo/title":
			want.SEO.Title = ""
		case "/seo/description":
			want.SEO.Description = ""
		case "/seo/canonical":
			want.SEO.Canonical = ""
		case "/seo/ogImage":
			want.SEO.OGImage = ""
		default:
			delete(want.Terms, strings.TrimPrefix(unit, "/terms/"))
		}
		if !reflect.DeepEqual(rev, want) {
			t.Errorf("clearUnit(%s) left %+v, want %+v", unit, rev, want)
		}
		if clearUnit(&rev, unit) {
			t.Errorf("clearUnit(%s) of what is empty = true", unit)
		}
	}
	// /seo/noindex is a flag, which cannot be wrong.
	for _, unit := range []string{"", "/unknown", "/terms", "/terms/", "/terms/nosuch", "/seo", "/seo/", "/seo/noindex", "/seo/nosuch", "/fields/x", "/body"} {
		rev := full()
		if clearUnit(&rev, unit) || !reflect.DeepEqual(rev, full()) {
			t.Errorf("clearUnit(%q) took something out of the revision: %+v", unit, rev)
		}
	}

	t.Run("the last taxonomy leaves no terms", func(t *testing.T) {
		rev := content.Revision{Terms: content.Terms{"category": {"c1"}}}
		if !clearUnit(&rev, "/terms/category") || rev.Terms != nil {
			t.Errorf("clearUnit left the terms %#v, want nil", rev.Terms)
		}
		if clearUnit(&rev, "/terms/category") {
			t.Error("clearUnit of no terms = true")
		}
	})
	t.Run("the name of a taxonomy is a JSON Pointer segment", func(t *testing.T) {
		rev := content.Revision{Terms: content.Terms{"a/b": {"x"}, "c~d": {"y"}}}
		if !clearUnit(&rev, "/terms/a~1b") || !reflect.DeepEqual(rev.Terms, content.Terms{"c~d": {"y"}}) {
			t.Errorf("after /terms/a~1b the terms are %#v", rev.Terms)
		}
		if !clearUnit(&rev, "/terms/c~0d") || rev.Terms != nil {
			t.Errorf("after /terms/c~0d the terms are %#v", rev.Terms)
		}
	})
}
