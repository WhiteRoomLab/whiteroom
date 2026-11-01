package build

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestCheckOutputs checks the duplicates of outputs (4.1) on outputs and redirects made by hand, given in both orders:
// each conflict is reported once, on the later of the two in the order of the check, naming the earlier one.
func TestCheckOutputs(t *testing.T) {
	const ideographicSpace = string(rune(0x3000))
	from := func(name, pointer string) source {
		return source{file: content.SiteFile{Name: name}, pointer: pointer, what: name}
	}
	core := func(what string) source { return source{what: what} }
	page := func(path, name string) output {
		file, err := content.OutputFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return output{path: path, file: file, src: from(name, "/slug"), rank: rankDocument}
	}
	redirectFrom := func(path, name string) redirect { return redirect{from: path, src: from(name, "/from")} }
	feed := output{path: "/feed/", file: "feed/index.xml", src: core("the feed in ja"), rank: rankCore}
	asset := output{
		path: "/assets/index.html", file: "assets/index.html", src: from("theme/assets/index.html", ""), rank: rankDeveloper,
	}
	list := source{
		file: content.SiteFile{Name: "site/schema.yaml"}, pointer: "/types/works/archive", what: `the list of the type "works" in ja`,
	}
	tests := []struct {
		name string
		outs []output
		rs   []redirect
		want []string
	}{
		{
			name: "no duplicates",
			outs: []output{
				page("/about/", "revisions/a.json"),
				page("/about/team/", "revisions/b.json"),
				page("/about.html", "revisions/c.json"),
				page("/css/", "revisions/d.json"),
				{path: "/css/site.css", file: "css/site.css", src: core("the site CSS"), rank: rankCore},
				{path: "/_forms/frm_1", src: from("forms/frm_1.json", "/id"), rank: rankDocument},
				{path: "/_forms/frm_2", src: from("forms/frm_2.json", "/id"), rank: rankDocument},
				page("/_forms/", "revisions/e.json"),
				page("/が/", "revisions/f.json"),
				feed,
			},
			rs: []redirect{
				redirectFrom("/about/?ref=top", "redirects/r.json"),
				redirectFrom("/?p=123", "redirects/s.json"),
				redirectFrom("/about/team/members/", "redirects/t.json"),
				redirectFrom("/か"+string(rune(0x3099))+"/", "redirects/u.json"),
			},
		},
		{
			name: "page at the path of the feed",
			outs: []output{page("/feed/", "revisions/a.json"), feed},
			want: []string{`revisions/a.json: /slug: output path "/feed/" is also that of the feed in ja`},
		},
		{
			name: "pages at the same path, with the same file",
			outs: []output{page("/about/", "revisions/b.json"), page("/about/", "revisions/a.json")},
			want: []string{`revisions/b.json: /slug: output path "/about/" is also that of revisions/a.json`},
		},
		{
			name: "three pages at the same path",
			outs: []output{page("/a/", "revisions/c.json"), page("/a/", "revisions/b.json"), page("/a/", "revisions/a.json")},
			want: []string{
				`revisions/b.json: /slug: output path "/a/" is also that of revisions/a.json`,
				`revisions/c.json: /slug: output path "/a/" is also that of revisions/a.json`,
			},
		},
		{
			name: "asset and page with the same file",
			outs: []output{page("/assets/", "revisions/a.json"), asset},
			want: []string{`revisions/a.json: /slug: output file "assets/index.html" is also that of theme/assets/index.html`},
		},
		{
			name: "path of one output and file of another",
			outs: []output{
				page("/assets/", "revisions/a.json"),
				{path: "/assets/", file: "assets/index.xml", src: core("core output"), rank: rankCore},
				asset,
			},
			want: []string{
				`revisions/a.json: /slug: output file "assets/index.html" is also that of theme/assets/index.html`,
				`revisions/a.json: /slug: output path "/assets/" is also that of core output`,
			},
		},
		{
			name: "files in the file of another",
			outs: []output{
				{path: "/x.html/page/2/", file: "x.html/page/2/index.html", src: from("terms/t.json", "/slug"), rank: rankDocument},
				{path: "/x.html/", file: "x.html/index.html", src: from("terms/t.json", "/slug"), rank: rankDocument},
				page("/x.html", "revisions/a.json"),
			},
			want: []string{
				`terms/t.json: /slug: output file "x.html/index.html" is in "x.html", which is the file of revisions/a.json`,
				`terms/t.json: /slug: output file "x.html/page/2/index.html" is in "x.html", which is the file of revisions/a.json`,
			},
		},
		{
			name: "file that is a directory of others",
			outs: []output{
				page("/list.html", "revisions/a.json"),
				{path: "/list.html/page/2/", file: "list.html/page/2/index.html", src: list, rank: rankDeveloper},
				{path: "/list.html/", file: "list.html/index.html", src: list, rank: rankDeveloper},
			},
			want: []string{
				`revisions/a.json: /slug: output file "list.html" is also a directory of "list.html/index.html", ` +
					`the file of the list of the type "works" in ja`,
			},
		},
		{
			name: "file that is a directory of a deeper file",
			outs: []output{
				page("/w.html", "revisions/a.json"),
				{path: "/w.html/a/b/", file: "w.html/a/b/index.html", src: list, rank: rankDeveloper},
			},
			want: []string{
				`revisions/a.json: /slug: output file "w.html" is also a directory of "w.html/a/b/index.html", ` +
					`the file of the list of the type "works" in ja`,
			},
		},
		{
			name: "file in the files of others",
			outs: []output{
				page("/a.html/b.html/", "revisions/c.json"),
				page("/a.html/b.html", "revisions/b.json"),
				page("/a.html", "revisions/a.json"),
			},
			want: []string{
				`revisions/b.json: /slug: output file "a.html/b.html" is in "a.html", which is the file of revisions/a.json`,
				`revisions/c.json: /slug: output file "a.html/b.html/index.html" is in "a.html", which is the file of ` +
					`revisions/a.json`,
			},
		},
		{
			name: "paths in the canonical form",
			outs: []output{
				page("/a"+ideographicSpace+"b/", "revisions/a.json"),
				{path: "/a%E3%80%80b/", file: "a%E3%80%80b/index.html", src: from("revisions/b.json", "/slug"), rank: rankDocument},
			},
			want: []string{
				fmt.Sprintf("revisions/a.json: /slug: output path %q is also that of revisions/b.json", "/a"+ideographicSpace+"b/"),
			},
		},
		{
			name: "redirects from the paths of outputs",
			outs: []output{page("/about/", "revisions/a.json"), page("/x.html", "revisions/b.json"), feed},
			rs: []redirect{
				redirectFrom("/about", "redirects/r.json"),
				redirectFrom("/x.html/", "redirects/s.json"),
				redirectFrom("/feed", "redirects/t.json"),
				redirectFrom("/about/?x=1", "redirects/u.json"),
				redirectFrom("/about/", "redirects/v.json"),
			},
			want: []string{
				`redirects/r.json: /from: from "/about" is the output path "/about/" of revisions/a.json`,
				`redirects/s.json: /from: from "/x.html/" is the output path "/x.html" of revisions/b.json`,
				`redirects/t.json: /from: from "/feed" is the output path "/feed/" of the feed in ja`,
				`redirects/v.json: /from: from "/about/" is the output path "/about/" of revisions/a.json`,
			},
		},
		{
			name: "redirects from encoded paths",
			outs: []output{page("/概要/", "revisions/a.json"), page("/a"+ideographicSpace+"b/", "revisions/b.json")},
			rs: []redirect{
				redirectFrom("/%E6%A6%82%E8%A6%81", "redirects/r.json"),
				redirectFrom("/a%E3%80%80b/", "redirects/s.json"),
				redirectFrom("/a%e3%80%80b", "redirects/t.json"),
			},
			want: []string{
				`redirects/r.json: /from: from "/%E6%A6%82%E8%A6%81" is the output path "/概要/" of revisions/a.json`,
				fmt.Sprintf("redirects/s.json: /from: from %q is the output path %q of revisions/b.json",
					"/a%E3%80%80b/", "/a"+ideographicSpace+"b/"),
				fmt.Sprintf("redirects/t.json: /from: from %q is the output path %q of revisions/b.json",
					"/a%e3%80%80b", "/a"+ideographicSpace+"b/"),
			},
		},
		{
			name: "redirect from the action of a form",
			outs: []output{{path: "/_forms/frm_1", src: from("forms/frm_1.json", "/id"), rank: rankDocument}},
			rs:   []redirect{redirectFrom("/_forms/frm_1/", "redirects/r.json")},
			want: []string{`redirects/r.json: /from: from "/_forms/frm_1/" is the output path "/_forms/frm_1" of forms/frm_1.json`},
		},
		{
			name: "generated redirect",
			outs: []output{{path: "/media/m/a.jpg", file: "media/m/a.jpg", src: from("media/m.json", ""), rank: rankDocument}},
			rs: []redirect{{
				from:      "/media/m/a.jpg/",
				src:       source{file: content.SiteFile{Name: "site/settings.json"}, pointer: "/frontPage/ja", what: "a redirect"},
				generated: true,
			}},
			want: []string{
				`site/settings.json: /frontPage/ja: from "/media/m/a.jpg/" is the output path "/media/m/a.jpg" of media/m.json`,
			},
		},
		{
			name: "outputs of the core",
			outs: []output{
				{path: "/x/", file: "x/index.html", src: core("core output b"), rank: rankCore},
				{path: "/x/", file: "x/index.html", src: core("core output a"), rank: rankCore},
			},
			want: []string{`output path "/x/" is also that of core output a`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, reversed := range []bool{false, true} {
				outs, rs := slices.Clone(tt.outs), slices.Clone(tt.rs)
				if reversed {
					slices.Reverse(outs)
					slices.Reverse(rs)
				}
				given := slices.Clone(outs)
				var p problems
				checkOutputs(outs, rs, &p)
				var got []string
				if p.len() > 0 {
					got = errorTexts(t, p.sorted())
				}
				if !slices.Equal(got, tt.want) {
					t.Errorf("errors (reversed %t):\n%s\nwant:\n%s", reversed, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
				}
				if !slices.EqualFunc(outs, given, func(a, b output) bool { return compareOutputs(a, b) == 0 }) {
					t.Errorf("checkOutputs() changed the order of the outputs")
				}
			}
		})
	}
}

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
			var p problems
			b.checkPaths(&p)
			var got []string
			if p.len() > 0 {
				got = errorTexts(t, p.sorted())
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			if !slices.EqualFunc(b.outputs, outputs, func(a, b output) bool { return compareOutputs(a, b) == 0 }) {
				t.Errorf("checkPaths() changed the order of the outputs")
			}
		})
	}
}

func TestParentDirs(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{"index.html", nil},
		{"about/index.html", []string{"about"}},
		{"a/b/c/index.xml", []string{"a", "a/b", "a/b/c"}},
		{"2026/10/26/新サービス/index.html", []string{"2026", "2026/10", "2026/10/26", "2026/10/26/新サービス"}},
	} {
		if got := parentDirs(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("parentDirs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
