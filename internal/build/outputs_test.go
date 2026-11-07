package build

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
)

// TestCheckPaths checks the duplicates of outputs (4.1) in sites, with the positions of the errors.
func TestCheckPaths(t *testing.T) {
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	htmlPages := func(t *testing.T, s *memSource, replacements ...string) {
		s.schema.Data = []byte(strings.NewReplacer(
			append([]string{"  works:\n", "  page:\n    url: /{path}.html\n  works:\n"}, replacements...)...,
		).Replace(testSchema))
		site := testSite()
		site.PostsPage = nil
		s.putSettings(t, site)
	}
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		want   []string
	}{
		{
			name: "fixture with 12 posts in each language",
			change: func(t *testing.T, s *memSource) {
				s.addPosts(t, "ja", 11)
				s.addPosts(t, "en", 11)
			},
		},
		{
			name: "post at the path of a page",
			change: func(t *testing.T, s *memSource) {
				s.schema.Data = []byte(strings.Replace(testSchema, "url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{slug}/", 1))
				s.editRevision(t, postID, func(r *content.Revision) { r.Slug = "about" })
			},
			want: []string{revisionFile(postID) + `:4:3: /slug: output path "/about/" is also that of ` + revisionFile(aboutID)},
		},
		{
			name: "front page whose rule makes the path of a post",
			change: func(t *testing.T, s *memSource) {
				s.schema.Data = []byte(strings.Replace(testSchema, "url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{slug}/", 1))
				s.editRevision(t, postID, func(r *content.Revision) { r.Slug = "home" })
			},
		},
		{
			name: "pages at the paths of a feed and of the list of a type",
			change: func(t *testing.T, s *memSource) {
				s.addPage(t, 1, "feed", "")
				s.addPage(t, 2, "works", "")
			},
			want: []string{
				revisionFile(newID("ent", 1)) + `:4:3: /slug: output path "/feed/" is also that of the feed in ja`,
				revisionFile(newID("ent", 2)) + `:4:3: /slug: output path "/works/" is also that of the list of the type "works" in ja`,
			},
		},
		{
			name: "page with the file of an asset",
			change: func(t *testing.T, s *memSource) {
				fsys := testTheme()
				fsys["assets/index.html"] = &fstest.MapFile{Data: []byte("<p>asset</p>\n")}
				s.theme = fsys
				s.addPage(t, 1, "assets", "")
			},
			want: []string{
				revisionFile(newID("ent", 1)) + `:4:3: /slug: output file "assets/index.html" is also that of theme/assets/index.html`,
			},
		},
		{
			name: "page at the path of the 404 page",
			change: func(t *testing.T, s *memSource) {
				htmlPages(t, s)
				s.addPage(t, 1, "404", "")
			},
			want: []string{revisionFile(newID("ent", 1)) + `:4:3: /slug: output path "/404.html" is also that of the 404 page in ja`},
		},
		{
			name: "lists of a type in the files of pages",
			change: func(t *testing.T, s *memSource) {
				htmlPages(t, s, "archive: /works/", "archive: /about.html/")
			},
			want: []string{
				revisionFile(aboutID) + `:4:3: /slug: output file "about.html" is also a directory of "about.html/index.html", ` +
					`the file of the list of the type "works" in ja`,
				revisionFile(enAboutID) + `:4:3: /slug: output file "en/about.html" is also a directory of ` +
					`"en/about.html/index.html", the file of the list of the type "works" in en`,
			},
		},
		{
			name: "entry in the file of the 404 page",
			change: func(t *testing.T, s *memSource) {
				s.schema.Data = []byte(strings.Replace(testSchema, "url: /works/{slug}/", "url: /404.html/{slug}/", 1))
			},
			want: []string{
				revisionFile(worksID) + `:4:3: /slug: output file "404.html/example-corporate-site/index.html" is in "404.html", ` +
					`which is the file of the 404 page in ja`,
			},
		},
		{
			name: "redirects from the paths of outputs",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/about", "/contact/")
				s.addRedirect(t, 2, "/en/feed", "/en/")
				s.addRedirect(t, 3, "/_forms/"+formID+"/", "/contact/")
				s.addRedirect(t, 4, "/about/?ref=top", "/contact/")
				s.addRedirect(t, 5, "/category/news/press-release", "/news/")
				s.addRedirect(t, 6, "/.htaccess/", "/about/")
			},
			want: []string{
				rdr(1) + `:4:3: /from: from "/about" is the output path "/about/" of ` + revisionFile(aboutID),
				rdr(2) + `:4:3: /from: from "/en/feed" is the output path "/en/feed/" of the feed in en`,
				rdr(3) + `:4:3: /from: from "/_forms/` + formID + `/" is the output path "/_forms/` + formID + `" of ` +
					fileOf(content.RefForm, formID),
				rdr(5) + `:4:3: /from: from "/category/news/press-release" is the output path "/category/news/press-release/" of ` +
					fileOf(content.RefTerm, pressReleaseID),
				rdr(6) + `:4:3: /from: from "/.htaccess/" is the output path "/.htaccess" of the .htaccess file for Apache`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			tt.change(t, src)
			b := mustLoad(t, src)
			mustResolve(t, b)
			outputs := slices.Clone(b.outputs)
			p := problems{read: b.revisionData}
			b.checkPaths(&p)
			var got []string
			if p.len() > 0 {
				got = errorTexts(t, p.sorted())
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			if !slices.EqualFunc(b.outputs, outputs, func(a, b siteplan.Output) bool { return reflect.DeepEqual(a, b) }) {
				t.Errorf("checkPaths() changed the order of the outputs")
			}
		})
	}
}
