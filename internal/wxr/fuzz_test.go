package wxr

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// strs returns all the strings in ex.
func strs(ex *Export) []string {
	ch := ex.Channel
	all := []string{ex.Version, ex.WordPress, ch.Title, ch.Link, ch.Description, ch.PubDate, ch.Language, ch.Generator, ch.BaseSiteURL, ch.BaseBlogURL}
	meta := func(l MetaList) {
		for _, m := range l {
			all = append(all, m.Key, m.Value)
		}
	}
	for _, a := range ex.Authors {
		all = append(all, a.Login, a.Email, a.DisplayName, a.FirstName, a.LastName)
	}
	for _, t := range ex.Terms {
		all = append(all, t.Taxonomy, t.Slug, t.Parent, t.Name, t.Description)
		meta(t.Meta)
	}
	for _, it := range ex.Items {
		all = append(all, it.Title, it.Link, it.PubDate, it.Creator, it.GUID, it.Description, it.Content, it.Excerpt,
			it.Date, it.DateGMT, it.Modified, it.ModifiedGMT, it.CommentStatus, it.PingStatus, it.Name, it.Status, it.Type,
			it.Password, it.AttachmentURL)
		for _, r := range it.Terms {
			all = append(all, r.Taxonomy, r.Slug, r.Name)
		}
		meta(it.Meta)
	}
	for _, n := range ex.Notes {
		all = append(all, n.Msg)
	}
	return all
}

func checkExport(t *testing.T, data []byte, ex *Export, opts Options) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("Read(%q): "+format, append([]any{data}, args...)...)
	}
	switch ex.Version {
	case "1.0", "1.1", "1.2":
	default:
		fail("Version = %q", ex.Version)
	}
	for _, s := range strs(ex) {
		if !utf8.ValidString(s) {
			fail("%q is not valid UTF-8", s)
		}
		if i := strings.IndexFunc(s, func(r rune) bool { return !allowed(r) }); i >= 0 {
			fail("%q has a character that XML does not allow", s)
		}
	}
	if len(ex.Items) > opts.MaxItems || len(ex.Terms) > opts.MaxTerms || len(ex.Authors) > opts.MaxAuthors {
		fail("%d items, %d terms, %d authors, over the limits", len(ex.Items), len(ex.Terms), len(ex.Authors))
	}

	positive := func(what string, p Pos) {
		if p.Line < 1 || p.Column < 1 {
			fail("the place of %s is %v", what, p)
		}
	}
	positive("the channel", ex.Channel.Pos)
	posts := make(map[int64]bool)
	for _, it := range ex.Items {
		positive("an item", it.Pos)
		if it.ID < 1 || blank(it.Type) || posts[it.ID] {
			fail("the item %+v has no ID, no type or the ID of another", it)
		}
		posts[it.ID] = true
		for _, r := range it.Terms {
			positive("a category", r.Pos)
			if r.Taxonomy == "" || r.Slug == "" {
				fail("the category %+v has no taxonomy or slug", r)
			}
		}
		for _, m := range it.Meta {
			positive("a postmeta", m.Pos)
			if blank(m.Key) {
				fail("a postmeta has no key")
			}
		}
	}
	ids, slugs := make(map[termKey]bool), make(map[termKey]bool)
	for _, tm := range ex.Terms {
		positive("a term", tm.Pos)
		id, slug := termKey{taxonomy: tm.Taxonomy, id: tm.ID}, termKey{taxonomy: tm.Taxonomy, slug: tm.Slug}
		if blank(tm.Taxonomy) || blank(tm.Slug) || tm.ID < 0 || tm.ID > 0 && ids[id] || slugs[slug] {
			fail("the term %+v has no taxonomy or slug, or the ID or slug of another", tm)
		}
		ids[id], slugs[slug] = true, true
		for _, m := range tm.Meta {
			positive("a termmeta", m.Pos)
			if blank(m.Key) {
				fail("a termmeta has no key")
			}
		}
	}
	logins := make(map[string]bool)
	for _, a := range ex.Authors {
		positive("an author", a.Pos)
		if blank(a.Login) || a.ID < 0 || logins[a.Login] {
			fail("the author %+v has no login, or the login of another", a)
		}
		logins[a.Login] = true
	}

	for i, n := range ex.Notes {
		positive("a note", n.Pos)
		if i > 0 && (n.Line < ex.Notes[i-1].Line || n.Line == ex.Notes[i-1].Line && n.Column < ex.Notes[i-1].Column) {
			fail("the notes %d and %d are not in the order of the file", i-1, i)
		}
		limit := map[RecordKind]int{RecordChannel: 1, RecordAuthor: len(ex.Authors), RecordTerm: len(ex.Terms), RecordItem: len(ex.Items)}[n.Record]
		if n.Index < 0 || n.Index >= limit || n.Msg == "" {
			fail("the note %+v is not about a record", n)
		}
	}

	// The indexes find what a look in order finds.
	plain := *ex
	plain.postIdx, plain.termIdx, plain.slugIdx, plain.authorIdx = nil, nil, nil, nil
	for i := range ex.Items {
		if ex.Post(ex.Items[i].ID) != &ex.Items[i] || plain.Post(ex.Items[i].ID) != &plain.Items[i] {
			fail("Post(%d) is not Items[%d]", ex.Items[i].ID, i)
		}
	}
	for i, tm := range ex.Terms {
		if ex.TermBySlug(tm.Taxonomy, tm.Slug) != &ex.Terms[i] || plain.TermBySlug(tm.Taxonomy, tm.Slug) != &plain.Terms[i] {
			fail("TermBySlug(%q, %q) is not Terms[%d]", tm.Taxonomy, tm.Slug, i)
		}
		if tm.ID > 0 && (ex.Term(tm.Taxonomy, tm.ID) != &ex.Terms[i] || plain.Term(tm.Taxonomy, tm.ID) != &plain.Terms[i]) {
			fail("Term(%q, %d) is not Terms[%d]", tm.Taxonomy, tm.ID, i)
		}
	}
	for i, a := range ex.Authors {
		if ex.Author(a.Login) != &ex.Authors[i] || plain.Author(a.Login) != &plain.Authors[i] {
			fail("Author(%q) is not Authors[%d]", a.Login, i)
		}
	}
}

func FuzzRead(f *testing.F) {
	for _, tt := range errorCases(f) {
		f.Add([]byte(tt.in))
	}
	for _, tt := range acceptCases() {
		f.Add([]byte(tt.in))
	}
	for _, name := range []string{"wordpress-6.xml", "wordpress-3.3.xml", "wordpress-3.0.xml"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(wxrDoc("<title>a" + string(rune(8)) + "b</title>\n<item><wp:post_id>1</wp:post_id><wp:post_type>post</wp:post_type></item>\n")))
	opts := Options{MaxBytes: 1 << 20, MaxItems: 1000, MaxTerms: 1000, MaxAuthors: 1000, MaxDepth: 32, MaxTagBytes: 4096}
	f.Fuzz(func(t *testing.T, data []byte) {
		ex, err := Read(context.Background(), bytes.NewReader(data), opts)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Read(%q) = %v (%T), want an *Error", data, err, err)
			}
			if ex != nil {
				t.Errorf("Read(%q) returned an Export with the error %v", data, err)
			}
			lines := strings.Split(string(data), "\n")
			if e.Line < 1 || e.Line > len(lines) || e.Column < 1 || e.Column > utf8.RuneCountInString(lines[e.Line-1])+1 {
				t.Errorf("Read(%q) = %v, outside the input", data, err)
			}
			return
		}
		checkExport(t, data, ex, opts)
	})
}

func FuzzDecodeSlug(f *testing.F) {
	for _, s := range []string{
		"", "abc", "%e3%81%82", "%E3%81%82", "%", "%e", "%zz", "a+b", "%00", "%0a", "%c2%85", "%e3%81", "%2541", "あ%e3%81%82", "a\tb", "\xff",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := DecodeSlug(s)
		std, stdErr := url.PathUnescape(s)
		stdOK := stdErr == nil && utf8.ValidString(std) && !strings.ContainsFunc(std, unicode.IsControl)
		switch {
		case err == nil:
			if got != std || stdErr != nil {
				t.Errorf("DecodeSlug(%q) = %q, want %q, %v", s, got, std, stdErr)
			}
			if !utf8.ValidString(got) || strings.ContainsFunc(got, unicode.IsControl) {
				t.Errorf("DecodeSlug(%q) = %q, which is not valid UTF-8 or has a control character", s, got)
			}
		case stdOK:
			t.Errorf("DecodeSlug(%q) = %v, want %q", s, err, std)
		}
		if !strings.Contains(s, "%") && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) {
			if err != nil || got != s {
				t.Errorf("DecodeSlug(%q) = %q, %v, want it unchanged", s, got, err)
			}
		}
	})
}
