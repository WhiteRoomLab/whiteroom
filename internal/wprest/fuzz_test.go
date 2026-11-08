package wprest

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkSnapshotValues checks what holds for every snapshot that ReadSnapshot returns.
func checkSnapshotValues(t *testing.T, data []byte, s *Snapshot, opts Options) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("ReadSnapshot(%q): "+format, append([]any{data}, args...)...)
	}
	if len(s.Taxonomies) > opts.MaxTaxonomies || len(s.Types) > opts.MaxTaxonomies || len(s.Terms) > opts.MaxTerms || len(s.Users) > opts.MaxUsers {
		fail("%d taxonomies, %d types, %d terms and %d users, over the limits", len(s.Taxonomies), len(s.Types), len(s.Terms), len(s.Users))
	}
	utf8s := []string{s.API, s.Index.Name, s.Index.Description, s.Index.URL, s.Index.Home, s.Index.GMTOffset, s.Index.TimezoneString, s.Index.ShowOnFront}
	if s.Index.PageOnFront < 0 || s.Index.PageForPosts < 0 || s.Index.SiteLogo < 0 || s.Index.SiteIcon < 0 {
		fail("the index %+v has a negative ID", s.Index)
	}
	// A list has no elements, or it is not empty.
	for i, empty := range []bool{
		s.Taxonomies != nil && len(s.Taxonomies) == 0, s.Types != nil && len(s.Types) == 0, s.Terms != nil && len(s.Terms) == 0,
		s.Users != nil && len(s.Users) == 0, s.Unavailable != nil && len(s.Unavailable) == 0,
	} {
		if empty {
			fail("the list %d is empty and not nil", i)
		}
	}
	for i, tx := range s.Taxonomies {
		utf8s = append(utf8s, tx.Slug, tx.Name, tx.RestBase, tx.RestNamespace)
		utf8s = append(utf8s, tx.Types...)
		if tx.Slug == "" || tx.Types != nil && len(tx.Types) == 0 || i > 0 && tx.Slug <= s.Taxonomies[i-1].Slug {
			fail("the taxonomy %+v is not after the one before it, or has no slug", tx)
		}
	}
	for i, ty := range s.Types {
		utf8s = append(utf8s, ty.Slug, ty.Name, ty.RestBase, ty.RestNamespace, ty.HasArchive.Slug)
		utf8s = append(utf8s, ty.Taxonomies...)
		if ty.Slug == "" || ty.Taxonomies != nil && len(ty.Taxonomies) == 0 || i > 0 && ty.Slug <= s.Types[i-1].Slug {
			fail("the type %+v is not after the one before it, or has no slug", ty)
		}
		if a := ty.HasArchive; !a.Set && (a.On || a.Slug != "") || a.Slug != "" && !a.On {
			fail("the archive %+v is not one that is read", a)
		}
	}
	for i, tm := range s.Terms {
		utf8s = append(utf8s, tm.Taxonomy, tm.Slug, tm.Name, tm.Link)
		if tm.ID < 1 || tm.Taxonomy == "" || tm.Parent < 0 {
			fail("the term %+v has no ID, no taxonomy or a negative parent", tm)
		}
		if i > 0 {
			if before := s.Terms[i-1]; tm.Taxonomy < before.Taxonomy || tm.Taxonomy == before.Taxonomy && tm.ID <= before.ID {
				fail("the term %+v is not after the one before it", tm)
			}
		}
	}
	for i, u := range s.Users {
		utf8s = append(utf8s, u.Name, u.Slug, u.Description, u.URL, u.Link)
		if u.ID < 1 || i > 0 && u.ID <= s.Users[i-1].ID {
			fail("the user %+v has no ID, or is not after the one before it", u)
		}
	}
	for i, u := range s.Unavailable {
		utf8s = append(utf8s, u.Route, u.Taxonomy, u.Code)
		if u.Route != "" && u.Route[0] != '/' || u.Status != 0 && (u.Status < 100 || u.Status > 599) {
			fail("the unavailable %+v has a route that does not start with a slash, or a status that is not one", u)
		}
		if i > 0 && lessUnavailable(u, s.Unavailable[i-1]) {
			fail("the unavailable %+v is before the one before it", u)
		}
	}
	for _, str := range utf8s {
		if !utf8.ValidString(str) {
			fail("%q is not valid UTF-8", str)
		}
	}
}

func lessUnavailable(a, b Unavailable) bool {
	switch {
	case a.Route != b.Route:
		return a.Route < b.Route
	case a.Taxonomy != b.Taxonomy:
		return a.Taxonomy < b.Taxonomy
	case a.Status != b.Status:
		return a.Status < b.Status
	}
	return a.Code < b.Code
}

func FuzzReadSnapshot(f *testing.F) {
	f.Add([]byte(fullJSON))
	f.Add([]byte("{\n  \"index\": {}\n}\n"))
	f.Add([]byte(`{"index": {"gmt_offset": 5.5, "site_logo": "3", "site_icon": false, "namespaces": ["wp/v2"], "_links": {"a": [1, null, {}]}}}`))
	f.Add([]byte(`{"index": {}, "types": [{"slug": "a", "has_archive": "x"}, {"slug": "b", "has_archive": false}, {"slug": "c", "has_archive": ""}]}`))
	f.Add([]byte(`{"index": {}, "terms": [{"id": 3, "taxonomy": "b"}, {"id": 3, "taxonomy": "a", "name": "n"}], "users": [{"id": 2}, {"id": 1}]}`))
	f.Add([]byte("{\"index\": {\"name\": \"\xff\"}}"))
	f.Add([]byte(`{"index": {"name": "` + `\` + `ud800"}}`))
	for _, tt := range snapshotErrors {
		f.Add([]byte(tt.in))
	}
	opts := Options{MaxTerms: 50, MaxUsers: 50, MaxTaxonomies: 20, MaxSnapshotBytes: 1 << 20}
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := ReadSnapshot(bytes.NewReader(data), opts)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("ReadSnapshot(%q) = %v (%T), want an *Error", data, err, err)
			}
			if s != nil {
				t.Errorf("ReadSnapshot(%q) returned a Snapshot with the error %v", data, err)
			}
			if e.Pointer != "" && e.Pointer[0] != '/' || e.Msg == "" || strings.ContainsAny(e.Msg, "\n") {
				t.Errorf("ReadSnapshot(%q) = %q at %q, which is not an error that points to the JSON", data, e.Msg, e.Pointer)
			}
			return
		}
		checkSnapshotValues(t, data, s, opts)

		// What is written is what is read, and it is written the same again.
		var first, second bytes.Buffer
		if err := s.WriteJSON(&first); err != nil {
			t.Fatalf("ReadSnapshot(%q): WriteJSON() = %v", data, err)
		}
		again, err := ReadSnapshot(bytes.NewReader(first.Bytes()), Options{MaxTerms: opts.MaxTerms, MaxUsers: opts.MaxUsers, MaxTaxonomies: opts.MaxTaxonomies})
		if err != nil {
			t.Fatalf("ReadSnapshot(%q): reading the file that WriteJSON wrote, %q, = %v", data, first.String(), err)
		}
		if !reflect.DeepEqual(again, s) {
			t.Errorf("ReadSnapshot(%q): the snapshot read from %q is\n%+v\nwant\n%+v", data, first.String(), again, s)
		}
		if err := again.WriteJSON(&second); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Errorf("ReadSnapshot(%q): WriteJSON() after reading the file = %q, %v, want %q", data, second.String(), err, first.String())
		}
	})
}

// FuzzDecodeResponses feeds the readers of the responses of a site, which are not to be trusted, with whatever bytes.
func FuzzDecodeResponses(f *testing.F) {
	for _, seed := range []string{
		`[{"id":1,"taxonomy":"category","slug":"a","name":"A","parent":0,"link":"https://example.com/"}]`,
		`[{"id":2,"slug":"b","_links":{"self":[{"href":"x"}]}},{"id":3,"taxonomy":null}]`,
		`[{"id":1,"name":"n","url":"","description":"d","link":"l","slug":"s","avatar_urls":{"24":"x"}}]`,
		standardTaxonomies, standardTypes, fmt.Sprintf(standardIndex, "https://example.com", "https://example.com"),
		`{"a":{"slug":"a"},"b":{}}`, `{"namespaces":["wp/v2"],"site_logo":"7","site_icon":false,"gmt_offset":5.5}`,
		`[]`, `{}`, `null`, `[null]`, `{"a":null}`, ``, `[{"id":1}`, `[{"id":0}]`, `[{"id":1,"id":2}]`, "[\"\xff\"]",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		ok := func(what string, err error) bool {
			t.Helper()
			if err == nil {
				return true
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("%s(%q) = %v (%T), want an *Error", what, data, err, err)
			}
			if e.Pointer != "" && e.Pointer[0] != '/' || e.Msg == "" {
				t.Errorf("%s(%q) = %q at %q, which is not an error that points to the JSON", what, data, e.Msg, e.Pointer)
			}
			return false
		}
		terms, err := decodeTerms(data, "category")
		if ok("decodeTerms", err) {
			for _, tm := range terms {
				if tm.ID < 1 || tm.Taxonomy != "category" || tm.Parent < 0 {
					t.Errorf("decodeTerms(%q): the term %+v has no ID, another taxonomy, or a negative parent", data, tm)
				}
			}
		}
		users, err := decodeUsers(data)
		if ok("decodeUsers", err) {
			for _, u := range users {
				if u.ID < 1 {
					t.Errorf("decodeUsers(%q): the user %+v has no ID", data, u)
				}
			}
		}
		taxonomies, err := decodeKeyed(data, "taxonomies", 5, (*decoder).taxonomy)
		if ok("decodeKeyed taxonomies", err) {
			if len(taxonomies) > 5 {
				t.Errorf("decodeKeyed(%q) read %d taxonomies, over the limit", data, len(taxonomies))
			}
			for _, tx := range taxonomies {
				if tx.Slug == "" {
					t.Errorf("decodeKeyed(%q): the taxonomy %+v has no slug", data, tx)
				}
			}
		}
		types, err := decodeKeyed(data, "types", 5, (*decoder).postType)
		if ok("decodeKeyed types", err) {
			for _, ty := range types {
				if ty.Slug == "" || len(types) > 5 {
					t.Errorf("decodeKeyed(%q): the type %+v has no slug, or there are too many", data, ty)
				}
			}
		}
		var ix Index
		var namespaces bool
		err = decodeBody(data, func(d *decoder) (err error) {
			ix, err = d.index(&namespaces)
			return err
		})
		if ok("index", err) && (ix.PageOnFront < 0 || ix.PageForPosts < 0 || ix.SiteLogo < 0 || ix.SiteIcon < 0) {
			t.Errorf("index(%q) = %+v, which has a negative ID", data, ix)
		}
	})
}
