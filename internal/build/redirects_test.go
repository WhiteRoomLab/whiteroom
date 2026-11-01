package build

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// redirectTexts returns rs as texts such as "/home/ -> / 301 site/settings.json#/frontPage/ja (generated)".
func redirectTexts(rs []redirect) []string {
	texts := make([]string, len(rs))
	for i, r := range rs {
		texts[i] = fmt.Sprintf("%s -> %s %d %s#%s", r.from, r.to, r.status, r.src.file.Name, r.src.pointer)
		if r.abs {
			texts[i] += " (absolute)"
		}
		if r.generated {
			texts[i] += " (generated)"
		}
	}
	return texts
}

// noMedia is the mediaPath of resolveRedirects for sites without redirects to media.
func noMedia(id string) (string, error) { return "", fmt.Errorf("unexpected medium %s", id) }

// mustResolve resolves the redirects of b with noMedia, failing on errors.
func mustResolve(t *testing.T, b *builder) {
	t.Helper()
	var p problems
	b.resolveRedirects(noMedia, &p)
	if p.len() > 0 {
		t.Fatalf("resolveRedirects() errors:\n%v", p.sorted())
	}
}

// TestResolveRedirects checks the targets of the redirects: the pages of entries, terms and authors, the files of media,
// paths and URLs as written, and entries that are not published, whose redirects are left out with warnings.
func TestResolveRedirects(t *testing.T) {
	src := testSource(t)
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	for i, x := range []struct{ from, to string }{
		{"/blog/", newsID},
		{"/draft/", draftID},
		{"/en/new-service/", enPostID},
		{"/inquiry/", "/contact/"},
		{"/old-news/", newsCategoryID},
		{"/partner/", "https://example.org/"},
		{"/top/", homeID},
		{"/wp-content/uploads/2024/05/office.jpg", imageID},
		{"/wp-content/uploads/2024/05/guide.pdf", pdfID},
		{"/yamada/", authorID},
		{"/a/", "/%E6%A6%82%E8%A6%81/?x=1#top"},
	} {
		src.addRedirect(t, i+1, x.from, x.to)
	}
	edit(t, src, content.RefRedirect, newID("rdr", 6), func(r *content.Redirect) { r.Status = 302 })
	b := mustLoad(t, src)
	var asked []string
	var p problems
	b.resolveRedirects(func(id string) (string, error) {
		asked = append(asked, id)
		if id == imageID {
			return "/media/" + id + "/会社外観.jpg", nil
		}
		return "", errors.New("no original")
	}, &p)

	want := []string{
		"/?p=123 -> /2026/10/26/新サービス/ 301 " + fileOf(content.RefRedirect, redirectID) + "#/from",
		"/a/ -> /%E6%A6%82%E8%A6%81/?x=1#top 301 " + rdr(11) + "#/from",
		"/blog/ -> /news/ 301 " + rdr(1) + "#/from",
		"/en/new-service/ -> /en/2026/10/26/new-service/ 301 " + rdr(3) + "#/from",
		"/home/ -> / 301 site/settings.json#/frontPage/ja (generated)",
		"/inquiry/ -> /contact/ 301 " + rdr(4) + "#/from",
		"/old-news/ -> /category/news/ 301 " + rdr(5) + "#/from",
		"/partner/ -> https://example.org/ 302 " + rdr(6) + "#/from (absolute)",
		"/top/ -> / 301 " + rdr(7) + "#/from",
		"/wp-content/uploads/2024/05/office.jpg -> /media/" + imageID + "/会社外観.jpg 301 " + rdr(8) + "#/from",
		"/yamada/ -> /author/yamada/ 301 " + rdr(10) + "#/from",
	}
	if got := redirectTexts(b.serverRedirects); !slices.Equal(got, want) {
		t.Errorf("redirects:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got, want := asked, []string{imageID, pdfID}; !slices.Equal(got, want) {
		t.Errorf("mediaPath was called with %q, want %q", got, want)
	}
	for _, x := range []struct {
		r    redirect
		want string
	}{
		{b.serverRedirects[0], fileOf(content.RefRedirect, redirectID)},
		{b.serverRedirects[4], "the redirect to the front page in ja"},
	} {
		if x.r.src.what != x.want {
			t.Errorf("the redirect from %q is %q, want %q", x.r.from, x.r.src.what, x.want)
		}
	}
	wantErrors := []string{rdr(9) + ":5:3: /to: cannot make the path: no original"}
	if got := errorTexts(t, p.sorted()); !slices.Equal(got, wantErrors) {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantErrors, "\n"))
	}
	wantWarnings := []string{rdr(2) + `:5:3: /to: entry "` + draftID + `" is not published; the redirect is left out`}
	if got := errorTexts(t, b.warnings.sorted()); !slices.Equal(got, wantWarnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantWarnings, "\n"))
	}
}

// TestFrontRedirects checks the redirects that the build generates to the front pages, and when they give way to the
// redirects of the site and to the outputs.
func TestFrontRedirects(t *testing.T) {
	const ideographicSpace = string(rune(0x3000))
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	post := "/?p=123 -> /2026/10/26/新サービス/ 301 " + fileOf(content.RefRedirect, redirectID) + "#/from"
	tests := []struct {
		name     string
		change   func(t *testing.T, s *memSource)
		want     []string
		warnings []string
	}{
		{
			name: "front page in ja",
			want: []string{post, "/home/ -> / 301 site/settings.json#/frontPage/ja (generated)"},
		},
		{
			name: "prefixDefaultLanguage",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.PrefixDefaultLanguage = true
				s.putSettings(t, site)
				s.addRedirect(t, 1, "/yamada/", authorID)
			},
			want: []string{
				"/ -> /ja/ 302 site/settings.json#/prefixDefaultLanguage (generated)",
				"/?p=123 -> /ja/2026/10/26/新サービス/ 301 " + fileOf(content.RefRedirect, redirectID) + "#/from",
				"/ja/home/ -> /ja/ 301 site/settings.json#/frontPage/ja (generated)",
				"/yamada/ -> /ja/author/yamada/ 301 " + rdr(1) + "#/from",
			},
		},
		{
			name: "front pages in ja and en",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage["en"] = enAboutID
				s.putSettings(t, site)
			},
			want: []string{
				post,
				"/en/about/ -> /en/ 301 site/settings.json#/frontPage/en (generated)",
				"/home/ -> / 301 site/settings.json#/frontPage/ja (generated)",
			},
		},
		{
			name: "latest posts at the front",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage, site.PostsPage = nil, nil
				s.putSettings(t, site)
			},
			want: []string{post},
		},
		{
			name:   "redirect of the site from the path of the front page",
			change: func(t *testing.T, s *memSource) { s.addRedirect(t, 1, "/home", "/about/") },
			want:   []string{post, "/home -> /about/ 301 " + rdr(1) + "#/from"},
		},
		{
			name: "redirect of the site from /",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.PrefixDefaultLanguage = true
				s.putSettings(t, site)
				s.addRedirect(t, 1, "/", "/ja/about/")
			},
			want: []string{
				"/ -> /ja/about/ 301 " + rdr(1) + "#/from",
				"/?p=123 -> /ja/2026/10/26/新サービス/ 301 " + fileOf(content.RefRedirect, redirectID) + "#/from",
				"/ja/home/ -> /ja/ 301 site/settings.json#/frontPage/ja (generated)",
			},
		},
		{
			name:   "redirect of the site left out",
			change: func(t *testing.T, s *memSource) { s.addRedirect(t, 1, "/home/", draftID) },
			want:   []string{post, "/home/ -> / 301 site/settings.json#/frontPage/ja (generated)"},
			warnings: []string{
				rdr(1) + `:5:3: /to: entry "` + draftID + `" is not published; the redirect is left out`,
			},
		},
		{
			name: "front page whose rule makes the path of another front page",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, homeID, func(r *content.Revision) { r.Slug = "en" })
			},
			want: []string{post},
		},
		{
			name: "front pages whose rules make the same path",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage["en"] = enAboutID
				s.putSettings(t, site)
				s.editRevision(t, enAboutID, func(r *content.Revision) { r.Slug = "home" })
				parent := s.addPage(t, 1, "en", "")
				s.editRevision(t, homeID, func(r *content.Revision) { r.Parent = parent })
			},
			want: []string{post, "/en/home/ -> / 301 site/settings.json#/frontPage/ja (generated)"},
		},
		{
			name: "front page whose rule makes the path of a post",
			change: func(t *testing.T, s *memSource) {
				s.schema.Data = []byte(strings.Replace(testSchema, "url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{slug}/", 1))
				s.editRevision(t, postID, func(r *content.Revision) { r.Slug = "home" })
			},
			want: []string{"/?p=123 -> /home/ 301 " + fileOf(content.RefRedirect, redirectID) + "#/from"},
		},
		{
			name: "canonical from",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, homeID, func(r *content.Revision) { r.Slug = "ホーム" + ideographicSpace + "ページ" })
			},
			want: []string{post, "/ホーム%E3%80%80ページ/ -> / 301 site/settings.json#/frontPage/ja (generated)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			if tt.change != nil {
				tt.change(t, src)
			}
			b := mustLoad(t, src)
			mustResolve(t, b)
			if got := redirectTexts(b.serverRedirects); !slices.Equal(got, tt.want) {
				t.Errorf("redirects:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			var warnings []string
			if b.warnings.len() > 0 {
				warnings = errorTexts(t, b.warnings.sorted())
			}
			if !slices.Equal(warnings, tt.warnings) {
				t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(warnings, "\n"), strings.Join(tt.warnings, "\n"))
			}
		})
	}
}
