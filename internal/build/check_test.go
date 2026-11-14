package build

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// eventGroup is the translation group of the tags eventTagID and enEventTagID.
const eventGroup = "tg_01JBA4C7M8P2S5V9Y2B6D9G3J6"

// newID returns an ID with prefix, such as "ent", numbered n, which sorts after the IDs of the fixture.
func newID(prefix string, n int) string { return fmt.Sprintf("%s_01JF%022d", prefix, n) }

func fileOf(kind content.RefKind, id string) string { return kindDirs[kind] + "/" + id + ".json" }

// revisionFile returns the name of the file of the published revision of the entry id.
func revisionFile(id string) string { return "revisions/" + revisionID(id) + ".json" }

// addPage adds a page published in ja with the ID newID("ent", n), slug and parent ("" for none), and returns its ID.
func (s *memSource) addPage(t testing.TB, n int, slug, parent string) string {
	t.Helper()
	id := newID("ent", n)
	s.putEntry(t, newEntry(id, 100+n, content.TypePage, "ja", groupOf(id)), content.Revision{Title: slug, Slug: slug, Parent: parent})
	return id
}

// addRedirect adds a redirect with the ID newID("rdr", n) from from to to, with the status 301.
func (s *memSource) addRedirect(t testing.TB, n int, from, to string) {
	t.Helper()
	id := newID("rdr", n)
	s.put(t, content.RefRedirect, id, content.Redirect{ID: id, From: from, To: to, Status: 301})
}

// newTerm returns a term with the ID newID("trm", n) in a translation group of its own, named after its slug.
func newTerm(n int, taxonomy, language, slug string) content.Term {
	return content.Term{
		ID:               newID("trm", n),
		Taxonomy:         taxonomy,
		Language:         language,
		TranslationGroup: newID("tg", n),
		Name:             slug,
		Slug:             slug,
	}
}

func TestCheckAcross(t *testing.T) {
	const ideographicSpace = string(rune(0x3000))
	cycle := func(ids ...string) string { return strings.Join(append(ids, ids[0]), " -> ") }
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		want   []string // Error() of each error; none if the site passes
	}{
		{
			name: "published front pages",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage["en"] = enAboutID
				s.putSettings(t, site)
			},
		},
		{
			name: "front page and posts page not published",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage["ja"] = draftID
				s.putSettings(t, site)
				edit(t, s, content.RefEntry, newsID, func(e *content.Entry) { e.Status, e.PublishedRevision = content.StatusArchived, "" })
			},
			want: []string{
				`site/settings.json:20:5: /frontPage/ja: entry "` + draftID + `" is not published`,
				`site/settings.json:23:5: /postsPage/ja: entry "` + newsID + `" is not published`,
			},
		},
		{
			name: "same slugs of entries in other languages, types and parents, and of entries not published",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, worksID, func(r *content.Revision) { r.Slug = "about" })
				s.editRevision(t, postID, func(r *content.Revision) { r.Slug = "about" })
				s.addPage(t, 1, "team", aboutID)
				s.addPage(t, 2, "team", servicesID)
				s.addPage(t, 3, "team", "")
				id := newID("ent", 4)
				archived := newEntry(id, 104, content.TypePage, "ja", groupOf(id))
				archived.Status = content.StatusArchived
				s.putEntry(t, archived, content.Revision{Title: "About", Slug: "about"})
			},
		},
		{
			name: "slugs of entries",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Slug = "about" })
				s.addPage(t, 1, "team", aboutID)
				s.addPage(t, 2, "team", aboutID)
				s.addPage(t, 3, "team", aboutID)
				id := newID("ent", 4)
				s.putEntry(t, newEntry(id, 104, content.TypePost, "ja", groupOf(id)), content.Revision{
					Title: "新サービス",
					Slug:  "新サービス",
					Date:  "2026-10-27T09:00:00+09:00",
				})
			},
			want: []string{
				revisionFile(servicesID) + `:4:3: /slug: slug "about" is also used by ` + revisionFile(aboutID),
				revisionFile(newID("ent", 2)) + `:4:3: /slug: slug "team" is also used by ` + revisionFile(newID("ent", 1)),
				revisionFile(newID("ent", 3)) + `:4:3: /slug: slug "team" is also used by ` + revisionFile(newID("ent", 1)),
				revisionFile(newID("ent", 4)) + `:4:3: /slug: slug "新サービス" is also used by ` + revisionFile(postID),
			},
		},
		{
			name: "same slugs of terms in other taxonomies and languages",
			change: func(t *testing.T, s *memSource) {
				for _, x := range []content.Term{
					newTerm(1, content.TaxonomyTag, "en", "イベント"),
					newTerm(2, content.TaxonomyCategory, "ja", "イベント"),
				} {
					s.put(t, content.RefTerm, x.ID, x)
				}
			},
		},
		{
			name: "slugs of terms",
			change: func(t *testing.T, s *memSource) {
				x := newTerm(1, content.TaxonomyTag, "ja", "イベント")
				s.put(t, content.RefTerm, x.ID, x)
			},
			want: []string{
				fileOf(content.RefTerm, newID("trm", 1)) + `:8:3: /slug: slug "イベント" is also used by ` + fileOf(content.RefTerm, eventTagID),
			},
		},
		{
			name: "slugs of authors",
			change: func(t *testing.T, s *memSource) {
				for i, slug := range []string{"taro", "yamada"} {
					id := newID("aut", i+1)
					s.put(t, content.RefAuthor, id, content.AuthorProfile{ID: id, Name: "山田", Slug: slug})
				}
			},
			want: []string{
				fileOf(content.RefAuthor, newID("aut", 2)) + `:5:3: /slug: slug "yamada" is also used by ` + fileOf(content.RefAuthor, authorID),
			},
		},
		{
			name: "translation groups with an entry in each language, and drafts without groups or languages",
			change: func(t *testing.T, s *memSource) {
				for i, x := range []struct{ language, group string }{{"", ""}, {"", ""}, {"ja", ""}, {"", aboutGroup}, {"en", groupOf(servicesID)}} {
					e := newEntry(newID("ent", i+1), 101+i, content.TypePage, x.language, x.group)
					e.Status, e.PublishedRevision = content.StatusDraft, ""
					s.putEntry(t, e, content.Revision{})
				}
			},
		},
		{
			name: "translation groups",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefEntry, draftID, func(e *content.Entry) { e.TranslationGroup = aboutGroup })
				edit(t, s, content.RefEntry, postID, func(e *content.Entry) { e.TranslationGroup = groupOf(servicesID) })
				x := newTerm(1, content.TaxonomyTag, "ja", "催し")
				x.TranslationGroup = eventGroup
				s.put(t, content.RefTerm, x.ID, x)
			},
			want: []string{
				fileOf(content.RefEntry, postID) + `:7:3: /translationGroup: translation group "` + groupOf(servicesID) +
					`" already has an entry in the language "ja": ` + fileOf(content.RefEntry, servicesID),
				fileOf(content.RefEntry, draftID) + `:7:3: /translationGroup: translation group "` + aboutGroup +
					`" already has an entry in the language "ja": ` + fileOf(content.RefEntry, aboutID),
				fileOf(content.RefTerm, newID("trm", 1)) + `:6:3: /translationGroup: translation group "` + eventGroup +
					`" already has a term in the language "ja": ` + fileOf(content.RefTerm, eventTagID),
			},
		},
		{
			name: "menus at the same location in another language and menus without locations",
			change: func(t *testing.T, s *memSource) {
				for i, x := range []struct{ location, language string }{{"header", "en"}, {"", "ja"}, {"", "ja"}} {
					id := newID("mnu", i+1)
					s.put(t, content.RefMenu, id, content.Menu{ID: id, Title: "Menu", Location: x.location, Language: x.language})
				}
			},
		},
		{
			name: "menus at the same location",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefMenu, footerID, func(m *content.Menu) { m.Location = "header" })
			},
			want: []string{
				fileOf(content.RefMenu, footerID) + `:5:3: /location: location "header" already has a menu in the language "ja": ` +
					fileOf(content.RefMenu, headerID),
			},
		},
		{
			name: "parents",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, aboutID, func(r *content.Revision) { r.Parent = servicesID })
				s.editRevision(t, contactID, func(r *content.Revision) { r.Parent = aboutID })
				x := newTerm(1, content.TaxonomyCategory, "ja", "award")
				x.Parent = pressReleaseID
				s.put(t, content.RefTerm, x.ID, x)
			},
		},
		{
			name: "parent not published",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Parent = draftID })
			},
			want: []string{revisionFile(servicesID) + `:5:3: /parent: entry "` + draftID + `" is not published`},
		},
		{
			name: "cycle of two pages",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, aboutID, func(r *content.Revision) { r.Parent = servicesID })
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Parent = aboutID })
			},
			want: []string{
				revisionFile(aboutID) + ":5:3: /parent: the parents make a cycle: " + cycle(aboutID, servicesID),
				revisionFile(servicesID) + ":5:3: /parent: the parents make a cycle: " + cycle(aboutID, servicesID),
			},
		},
		{
			name: "cycle of three pages and a page leading to it",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Parent = contactID })
				s.editRevision(t, contactID, func(r *content.Revision) { r.Parent = privacyID })
				s.editRevision(t, privacyID, func(r *content.Revision) { r.Parent = servicesID })
				s.editRevision(t, aboutID, func(r *content.Revision) { r.Parent = servicesID })
			},
			want: []string{
				revisionFile(servicesID) + ":5:3: /parent: the parents make a cycle: " + cycle(servicesID, contactID, privacyID),
				revisionFile(privacyID) + ":5:3: /parent: the parents make a cycle: " + cycle(servicesID, contactID, privacyID),
				revisionFile(contactID) + ":5:3: /parent: the parents make a cycle: " + cycle(servicesID, contactID, privacyID),
			},
		},
		{
			name: "cycle of terms",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefTerm, newsCategoryID, func(x *content.Term) { x.Parent = pressReleaseID })
			},
			want: []string{
				fileOf(content.RefTerm, newsCategoryID) + ":9:3: /parent: the parents make a cycle: " + cycle(newsCategoryID, pressReleaseID),
				fileOf(content.RefTerm, pressReleaseID) + ":9:3: /parent: the parents make a cycle: " + cycle(newsCategoryID, pressReleaseID),
			},
		},
		{
			name: "froms with and without queries",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/old/", "/new/")
				s.addRedirect(t, 2, "/old/?x=1", "/new/")
				s.addRedirect(t, 3, "/old?x=1&y=2", "/new/")
				s.addRedirect(t, 4, "/?p=1", "/new/")
			},
		},
		{
			name: "froms that match the same requests",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/old/", "/new/")
				s.addRedirect(t, 2, "/old", "/new/")
				s.addRedirect(t, 3, "/?p=123", "/new/")
				s.addRedirect(t, 4, "/old/?x=1", "/new/")
				s.addRedirect(t, 5, "/old?x=1", "/new/")
			},
			want: []string{
				rdr(2) + `:4:3: /from: from "/old" matches the same requests as that of ` + rdr(1),
				rdr(3) + `:4:3: /from: from "/?p=123" matches the same requests as that of ` + fileOf(content.RefRedirect, redirectID),
				rdr(5) + `:4:3: /from: from "/old?x=1" matches the same requests as that of ` + rdr(4),
			},
		},
		{
			name: "chains of redirects",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/a", "/b")
				s.addRedirect(t, 2, "/b", "/c")
				s.addRedirect(t, 3, "/c", "/d")
				// No from matches /f?x=1.
				s.addRedirect(t, 4, "/e", "/f?x=1")
				s.addRedirect(t, 5, "/f?y=1", "/e")
				s.addRedirect(t, 17, "/f?x=1&y=1", "/e")
				// /h?x=1, with more parameters, goes on to /i, and /p?x=1, before /p?y=1, to /q.
				s.addRedirect(t, 6, "/g", "/h?x=1")
				s.addRedirect(t, 7, "/h", "/g")
				s.addRedirect(t, 8, "/h?x=1", "/i")
				s.addRedirect(t, 9, "/o", "/p?y=1&x=1")
				s.addRedirect(t, 10, "/p?x=1", "/q")
				s.addRedirect(t, 11, "/p?y=1", "/o")
				// Not in the site, or not followed.
				s.addRedirect(t, 12, "/j", "http://example.com/k")
				s.addRedirect(t, 13, "/k", "/j")
				s.addRedirect(t, 14, "/l", "https://example.org/m")
				s.addRedirect(t, 15, "/m", "/l")
				s.addRedirect(t, 16, "/n", postID)
			},
		},
		{
			name: "loop of two redirects",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/a", "/b")
				s.addRedirect(t, 2, "/b/", "/a/#top")
			},
			want: []string{
				rdr(1) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2)),
				rdr(2) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2)),
			},
		},
		{
			name: "loop through a URL in the site",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/a", "https://example.com/b/?x=1#top")
				s.addRedirect(t, 2, "/b", "/a")
			},
			want: []string{
				rdr(1) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2)),
				rdr(2) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2)),
			},
		},
		{
			name: "loop through a query",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/a", "/b?x=1")
				s.addRedirect(t, 2, "/b?x=1", "/a")
			},
			want: []string{
				rdr(1) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2)),
				rdr(2) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2)),
			},
		},
		{
			name: "loops through the redirects that come first",
			change: func(t *testing.T, s *memSource) {
				// More parameters first.
				s.addRedirect(t, 1, "/a", "/b?y=2&x=1")
				s.addRedirect(t, 2, "/b", "/c")
				s.addRedirect(t, 3, "/b?x=1&y=2", "/a")
				s.addRedirect(t, 4, "/b?x=1", "/d")
				// The smaller from first.
				s.addRedirect(t, 5, "/e", "/f?x=1&y=1")
				s.addRedirect(t, 6, "/f?x=1", "/e")
				s.addRedirect(t, 7, "/f?y=1", "/g")
				// Into a loop, but not in it.
				s.addRedirect(t, 8, "/h", "/a")
			},
			want: []string{
				rdr(1) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 3)),
				rdr(3) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 3)),
				rdr(5) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 5), newID("rdr", 6)),
				rdr(6) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 5), newID("rdr", 6)),
			},
		},
		{
			name: "loop of three redirects in other forms",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/会社/", "/%E6%A6%82%E8%A6%81/")
				s.addRedirect(t, 2, "/概要", "/a"+ideographicSpace+"b/")
				s.addRedirect(t, 3, "/a%E3%80%80b/", "/%e4%bc%9a%e7%a4%be")
			},
			want: []string{
				rdr(1) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2), newID("rdr", 3)),
				rdr(2) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2), newID("rdr", 3)),
				rdr(3) + ":5:3: /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2), newID("rdr", 3)),
			},
		},
		{
			name: "validation before the checks across",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Title, r.Slug = "", "about" })
			},
			want: []string{revisionFile(servicesID) + ":1:1: /title: missing title"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			tt.change(t, src)
			if tt.want == nil {
				mustLoad(t, src)
				return
			}
			b, err := load(src, "")
			if b != nil {
				t.Errorf("load() = %+v with an error", b)
			}
			if got := errorTexts(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// cancelingSource is a source that ends the context of its check when the check reads the theme.
type cancelingSource struct {
	*memSource
	cancel context.CancelFunc
}

func (s cancelingSource) Theme() (fs.FS, error) {
	s.cancel()
	return s.memSource.Theme()
}

// TestCheck checks Check with the fixture: it passes a site that passes, also one that has warnings; it needs no original;
// it returns the errors of the source as they are, and the end of the context; and it writes nothing.
func TestCheck(t *testing.T) {
	failure := errors.New("disk failure")

	t.Run("sites that pass", func(t *testing.T) {
		src := testSource(t)
		if err := Check(t.Context(), src, ""); err != nil {
			t.Errorf("Check() of the fixture = %v", err)
		}
		// A redirect to a page that is a draft is left out, with a warning.
		src.addRedirect(t, 1, "/old-draft/", draftID)
		b := mustLoad(t, src)
		mustResolve(t, b)
		if b.warnings.len() == 0 {
			t.Fatalf("the redirect to a draft has no warning")
		}
		if err := Check(t.Context(), src, ""); err != nil {
			t.Errorf("Check() of a site with a warning = %v", err)
		}
	})

	t.Run("no original is opened", func(t *testing.T) {
		src := testSource(t)
		src.addRedirect(t, 1, "/old-logo.png", logoID)
		src.fail = map[string]error{"OpenMedia": failure}
		if err := Check(t.Context(), src, ""); err != nil {
			t.Errorf("Check() of a site with a redirect to a medium and a source that cannot open an original = %v", err)
		}
		if len(src.opened) > 0 {
			t.Errorf("Check() opened the originals %v", src.opened)
		}
	})

	t.Run("errors of the source", func(t *testing.T) {
		for _, method := range []string{"Settings", "Documents", "Theme", "Revision"} {
			src := testSource(t)
			src.fail = map[string]error{method: failure}
			var es content.Errors
			if err := Check(t.Context(), src, ""); !errors.Is(err, failure) || errors.As(err, &es) {
				t.Errorf("Check() of a source whose %s fails = %v, want its error as it is", method, err)
			}
		}
	})

	t.Run("a context that is done", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		src := testSource(t)
		if err := Check(ctx, src, ""); !errors.Is(err, context.Canceled) {
			t.Errorf("Check() with a context that is done = %v, want %v", err, context.Canceled)
		}
		if len(src.read) > 0 {
			t.Errorf("Check() with a context that is done read the revisions %v", src.read)
		}
	})

	t.Run("a context that is done during the check", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := Check(ctx, cancelingSource{testSource(t), cancel}, ""); !errors.Is(err, context.Canceled) {
			t.Errorf("Check() with a context that is done during it = %v, want %v", err, context.Canceled)
		}
	})

	t.Run("nothing is written", func(t *testing.T) {
		t.Chdir(t.TempDir())
		src := testSource(t)
		src.addRedirect(t, 1, "/about", "/contact/")
		if err := Check(t.Context(), src, ""); err == nil {
			t.Fatalf("Check() of a site that has a redirect from the path of a page = nil")
		}
		if entries, err := os.ReadDir("."); err != nil || len(entries) > 0 {
			t.Errorf("Check() made %v in the working directory (%v)", entries, err)
		}
	})
}

// stagingURL is a URL that replaces the https://example.com of the fixture in the tests of the override.
const stagingURL = "https://staging.example.com"

// badSiteURLs are URLs that the override refuses, with the secret that one of them has in it.
var badSiteURLs = []string{
	"https://staging.example.com/", "https://Staging.example.com", "ftp://staging.example.com",
	"https://user:s3cret@staging.example.com", "staging.example.com", "https://staging.example.com:443",
}

// untouchedSource is a Source that fails the test when it is read.
type untouchedSource struct{ t testing.TB }

func (s untouchedSource) Settings() (settings, schema content.SiteFile, err error) {
	s.t.Error("Settings() was called")
	return content.SiteFile{}, content.SiteFile{}, errors.New("read")
}

func (s untouchedSource) Documents(kind content.RefKind) (map[string]content.SiteFile, error) {
	s.t.Errorf("Documents(%q) was called", kind)
	return nil, errors.New("read")
}

func (s untouchedSource) Revision(id string) (content.SiteFile, error) {
	s.t.Errorf("Revision(%q) was called", id)
	return content.SiteFile{}, errors.New("read")
}

func (s untouchedSource) OpenMedia(hash string) (fs.File, error) {
	s.t.Errorf("OpenMedia(%q) was called", hash)
	return nil, errors.New("read")
}

func (s untouchedSource) Theme() (fs.FS, error) {
	s.t.Error("Theme() was called")
	return nil, errors.New("read")
}

// checkBadURL checks that err is the refusal of the URL bad of the override, with nothing of bad in its text.
func checkBadURL(t *testing.T, what string, err error, bad string) {
	t.Helper()
	var es content.Errors
	switch {
	case err == nil:
		t.Errorf("%s with the URL %q = nil, want an error", what, bad)
	case errors.As(err, &es), !strings.HasPrefix(err.Error(), "invalid site URL: "):
		t.Errorf("%s with a URL that does not pass = %v, want an error that is not about the documents, "+
			`starting with "invalid site URL: "`, what, err)
	case strings.Contains(err.Error(), bad), strings.Contains(err.Error(), "s3cret"):
		t.Errorf("%s: the error %q has the URL %q in it", what, err, bad)
	}
}

// TestCheckURL checks Check with a URL that replaces that of the site: what is a problem only under one of the two URLs
// is one only under it, whether a redirect leads back to itself, loops, or is for the path of the site; a URL that does not
// pass is an error before the source is read.
func TestCheckURL(t *testing.T) {
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	loop := strings.Join([]string{newID("rdr", 1), newID("rdr", 2), newID("rdr", 1)}, " -> ")
	tests := []struct {
		name    string
		url     string // the override
		change  func(t *testing.T, s *memSource)
		without []string // the errors of Check without the override
		with    []string // the errors of Check with it
	}{
		{
			name: "a redirect to itself under the override",
			url:  stagingURL,
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/staging-only", stagingURL+"/staging-only")
			},
			with: []string{rdr(1) + ":5:3: /to: redirects to itself"},
		},
		{
			name: "a redirect to itself under the URL of the settings",
			url:  stagingURL,
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/production-only", "https://example.com/production-only")
			},
			without: []string{rdr(1) + ":5:3: /to: redirects to itself"},
		},
		{
			name: "a loop of redirects under the override",
			url:  stagingURL,
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/loop-a", stagingURL+"/loop-b")
				s.addRedirect(t, 2, "/loop-b", stagingURL+"/loop-a")
			},
			with: []string{rdr(1) + ":5:3: /to: redirect loop: " + loop, rdr(2) + ":5:3: /to: redirect loop: " + loop},
		},
		{
			name: "the path of the URL of the override",
			url:  stagingURL + "/blog",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/old", stagingURL+"/blog/old")
			},
			with: []string{rdr(1) + ":5:3: /to: redirects to itself"},
		},
		{
			name: "the URL of the settings again",
			url:  "https://example.com",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/old", "https://example.com/old")
			},
			without: []string{rdr(1) + ":5:3: /to: redirects to itself"},
			with:    []string{rdr(1) + ":5:3: /to: redirects to itself"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			tt.change(t, src)
			settings := src.settings
			for _, c := range []struct {
				url  string
				want []string
			}{{"", tt.without}, {tt.url, tt.with}} {
				err := Check(t.Context(), src, c.url)
				if c.want == nil {
					if err != nil {
						t.Errorf("Check() with the URL %q = %v, want nil", c.url, err)
					}
					continue
				}
				if got := errorTexts(t, err); !slices.Equal(got, c.want) {
					t.Errorf("Check() with the URL %q:\n%s\nwant:\n%s", c.url, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
				}
			}
			if !slices.Equal(src.settings.Data, settings.Data) {
				t.Errorf("Check() changed the settings:\n%s\nwere:\n%s", src.settings.Data, settings.Data)
			}
		})
	}

	t.Run("a URL that does not pass", func(t *testing.T) {
		for _, bad := range badSiteURLs {
			checkBadURL(t, "Check()", Check(t.Context(), untouchedSource{t}, bad), bad)
		}
	})

	t.Run("the URL of the settings is checked as it is written", func(t *testing.T) {
		src := testSource(t)
		src.putSettings(t, func() *content.Site { s := testSite(); s.URL = "https://example.com/"; return s }())
		for _, url := range []string{"", stagingURL} {
			if err := Check(t.Context(), src, url); err == nil || !strings.Contains(err.Error(), "/url: must not end with") {
				t.Errorf("Check() with the URL %q of settings that have a url that does not pass = %v, want that problem", url, err)
			}
		}
	})
}
