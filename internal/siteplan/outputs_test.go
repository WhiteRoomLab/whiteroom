package siteplan

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestCheckOutputs checks the duplicates of outputs (4.1) on outputs and redirects made by hand, given in both orders:
// each conflict is reported once, on the later of the two in the order of the check, naming the earlier one.
func TestCheckOutputs(t *testing.T) {
	const ideographicSpace = string(rune(0x3000))
	from := func(name, pointer string) Source {
		return Source{File: content.SiteFile{Name: name}, Pointer: pointer, What: name}
	}
	core := func(what string) Source { return Source{What: what} }
	page := func(path, name string) Output {
		file, err := content.OutputFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return Output{Path: path, File: file, Src: from(name, "/slug"), Rank: RankDocument}
	}
	redirectFrom := func(path, name string) Redirect { return Redirect{From: path, Src: from(name, "/from")} }
	feed := Output{Path: "/feed/", File: "feed/index.xml", Src: core("the feed in ja"), Rank: RankCore}
	asset := Output{
		Path: "/assets/index.html", File: "assets/index.html", Src: from("theme/assets/index.html", ""), Rank: RankDeveloper,
	}
	list := Source{
		File: content.SiteFile{Name: "site/schema.yaml"}, Pointer: "/types/works/archive", What: `the list of the type "works" in ja`,
	}
	tests := []struct {
		name string
		outs []Output
		rs   []Redirect
		want []string
	}{
		{
			name: "no duplicates",
			outs: []Output{
				page("/about/", "revisions/a.json"),
				page("/about/team/", "revisions/b.json"),
				page("/about.html", "revisions/c.json"),
				page("/css/", "revisions/d.json"),
				{Path: "/css/site.css", File: "css/site.css", Src: core("the site CSS"), Rank: RankCore},
				{Path: "/_forms/frm_1", Src: from("forms/frm_1.json", "/id"), Rank: RankDocument},
				{Path: "/_forms/frm_2", Src: from("forms/frm_2.json", "/id"), Rank: RankDocument},
				page("/_forms/", "revisions/e.json"),
				page("/が/", "revisions/f.json"),
				feed,
			},
			rs: []Redirect{
				redirectFrom("/about/?ref=top", "redirects/r.json"),
				redirectFrom("/?p=123", "redirects/s.json"),
				redirectFrom("/about/team/members/", "redirects/t.json"),
				redirectFrom("/か"+string(rune(0x3099))+"/", "redirects/u.json"),
			},
		},
		{
			name: "page at the path of the feed",
			outs: []Output{page("/feed/", "revisions/a.json"), feed},
			want: []string{`revisions/a.json: /slug: output path "/feed/" is also that of the feed in ja`},
		},
		{
			name: "pages at the same path, with the same file",
			outs: []Output{page("/about/", "revisions/b.json"), page("/about/", "revisions/a.json")},
			want: []string{`revisions/b.json: /slug: output path "/about/" is also that of revisions/a.json`},
		},
		{
			name: "three pages at the same path",
			outs: []Output{page("/a/", "revisions/c.json"), page("/a/", "revisions/b.json"), page("/a/", "revisions/a.json")},
			want: []string{
				`revisions/b.json: /slug: output path "/a/" is also that of revisions/a.json`,
				`revisions/c.json: /slug: output path "/a/" is also that of revisions/a.json`,
			},
		},
		{
			name: "asset and page with the same file",
			outs: []Output{page("/assets/", "revisions/a.json"), asset},
			want: []string{`revisions/a.json: /slug: output file "assets/index.html" is also that of theme/assets/index.html`},
		},
		{
			name: "path of one output and file of another",
			outs: []Output{
				page("/assets/", "revisions/a.json"),
				{Path: "/assets/", File: "assets/index.xml", Src: core("core output"), Rank: RankCore},
				asset,
			},
			want: []string{
				`revisions/a.json: /slug: output file "assets/index.html" is also that of theme/assets/index.html`,
				`revisions/a.json: /slug: output path "/assets/" is also that of core output`,
			},
		},
		{
			name: "files in the file of another",
			outs: []Output{
				{Path: "/x.html/page/2/", File: "x.html/page/2/index.html", Src: from("terms/t.json", "/slug"), Rank: RankDocument},
				{Path: "/x.html/", File: "x.html/index.html", Src: from("terms/t.json", "/slug"), Rank: RankDocument},
				page("/x.html", "revisions/a.json"),
			},
			want: []string{
				`terms/t.json: /slug: output file "x.html/index.html" is in "x.html", which is the file of revisions/a.json`,
				`terms/t.json: /slug: output file "x.html/page/2/index.html" is in "x.html", which is the file of revisions/a.json`,
			},
		},
		{
			name: "file that is a directory of others",
			outs: []Output{
				page("/list.html", "revisions/a.json"),
				{Path: "/list.html/page/2/", File: "list.html/page/2/index.html", Src: list, Rank: RankDeveloper},
				{Path: "/list.html/", File: "list.html/index.html", Src: list, Rank: RankDeveloper},
			},
			want: []string{
				`revisions/a.json: /slug: output file "list.html" is also a directory of "list.html/index.html", ` +
					`the file of the list of the type "works" in ja`,
			},
		},
		{
			name: "file that is a directory of a deeper file",
			outs: []Output{
				page("/w.html", "revisions/a.json"),
				{Path: "/w.html/a/b/", File: "w.html/a/b/index.html", Src: list, Rank: RankDeveloper},
			},
			want: []string{
				`revisions/a.json: /slug: output file "w.html" is also a directory of "w.html/a/b/index.html", ` +
					`the file of the list of the type "works" in ja`,
			},
		},
		{
			name: "file in the files of others",
			outs: []Output{
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
			outs: []Output{
				page("/a"+ideographicSpace+"b/", "revisions/a.json"),
				{Path: "/a%E3%80%80b/", File: "a%E3%80%80b/index.html", Src: from("revisions/b.json", "/slug"), Rank: RankDocument},
			},
			want: []string{
				fmt.Sprintf("revisions/a.json: /slug: output path %q is also that of revisions/b.json", "/a"+ideographicSpace+"b/"),
			},
		},
		{
			name: "redirects from the paths of outputs",
			outs: []Output{page("/about/", "revisions/a.json"), page("/x.html", "revisions/b.json"), feed},
			rs: []Redirect{
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
			outs: []Output{page("/概要/", "revisions/a.json"), page("/a"+ideographicSpace+"b/", "revisions/b.json")},
			rs: []Redirect{
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
			outs: []Output{{Path: "/_forms/frm_1", Src: from("forms/frm_1.json", "/id"), Rank: RankDocument}},
			rs:   []Redirect{redirectFrom("/_forms/frm_1/", "redirects/r.json")},
			want: []string{`redirects/r.json: /from: from "/_forms/frm_1/" is the output path "/_forms/frm_1" of forms/frm_1.json`},
		},
		{
			name: "generated redirect",
			outs: []Output{{Path: "/media/m/a.jpg", File: "media/m/a.jpg", Src: from("media/m.json", ""), Rank: RankDocument}},
			rs: []Redirect{{
				From:      "/media/m/a.jpg/",
				Src:       Source{File: content.SiteFile{Name: "site/settings.json"}, Pointer: "/frontPage/ja", What: "a redirect"},
				Generated: true,
			}},
			want: []string{
				`site/settings.json: /frontPage/ja: from "/media/m/a.jpg/" is the output path "/media/m/a.jpg" of media/m.json`,
			},
		},
		{
			name: "outputs of the core",
			outs: []Output{
				{Path: "/x/", File: "x/index.html", Src: core("core output b"), Rank: RankCore},
				{Path: "/x/", File: "x/index.html", Src: core("core output a"), Rank: RankCore},
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
				got := reported(func(report Report) { CheckOutputs(outs, rs, report) })
				if !slices.Equal(got, tt.want) {
					t.Errorf("errors (reversed %t):\n%s\nwant:\n%s", reversed, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
				}
				if !slices.EqualFunc(outs, given, func(a, b Output) bool { return compareOutputs(a, b) == 0 }) {
					t.Errorf("CheckOutputs() changed the order of the outputs")
				}
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
