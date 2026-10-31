package render

import (
	"reflect"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	aboutEntry   = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" // published at /about/
	draftEntry   = "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" // not published
	missingEntry = "ent_01JB8Y6T2ZZ9X9C3E6G9J2K5M8"
	guideMedia   = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7" // at /media/会社案内.pdf
	missingMedia = "med_01JB8Z3K5ZZ9A1C5F9V3J6K2N7"
)

// linkResolver has aboutEntry, draftEntry and guideMedia.
var linkResolver = testResolver{
	entries: map[string][2]string{aboutEntry: {"会社概要", "/about/"}, draftEntry: {"下書き", ""}},
	media: map[string]testMedium{guideMedia: {
		m:    &content.Media{ID: guideMedia, FileName: "会社案内.pdf", MIMEType: "application/pdf", Size: 245760},
		path: "/media/会社案内.pdf",
	}},
}

func TestLink(t *testing.T) {
	const (
		guide     = "/blog/media/%E4%BC%9A%E7%A4%BE%E6%A1%88%E5%86%85.pdf"
		draft     = `entry "` + draftEntry + `" is not published; rendered without the link`
		noTarget  = "link without a target; rendered without the link"
		badScheme = `invalid href "javascript:alert(1)": scheme "javascript" is not allowed; rendered without the link`
	)
	tests := []struct {
		name    string
		link    *content.Link
		want    *Link
		warning string // "" for none
	}{
		{name: "nil"},
		{
			name: "href with spaces and quotes",
			link: &content.Link{Href: `https://example.com/search?q="a b"&lang='ja'`},
			want: &Link{Href: "https://example.com/search?q=%22a%20b%22&lang=%27ja%27"},
		},
		{name: "site path", link: &content.Link{Href: "/about/?x=1&y=2#access"}, want: &Link{Href: "/blog/about/?x=1&y=2#access"}},
		{
			name: "mailto",
			link: &content.Link{Href: "mailto:info@example.com?subject=資料請求"},
			want: &Link{Href: "mailto:info@example.com?subject=%E8%B3%87%E6%96%99%E8%AB%8B%E6%B1%82"},
		},
		{name: "tel", link: &content.Link{Href: "tel:+81-3-1234-5678"}, want: &Link{Href: "tel:+81-3-1234-5678"}},
		{name: "javascript", link: &content.Link{Href: "javascript:alert(1)"}, warning: badScheme},
		{
			name:    "protocol-relative",
			link:    &content.Link{Href: "//evil.example/"},
			warning: `invalid href "//evil.example/": protocol-relative URLs are not allowed; rendered without the link`,
		},
		{
			name: "href not normalized",
			link: &content.Link{Href: " https://example.com/"},
			warning: `invalid href " https://example.com/": contains control characters or surrounding spaces; ` +
				"normalize it first; rendered without the link",
		},
		{name: "entry", link: &content.Link{Entry: aboutEntry}, want: &Link{Href: "/blog/about/"}},
		{name: "entry and anchor", link: &content.Link{Entry: aboutEntry, Anchor: "access"}, want: &Link{Href: "/blog/about/#access"}},
		{name: "unpublished entry", link: &content.Link{Entry: draftEntry}, warning: draft},
		{name: "unpublished entry and anchor", link: &content.Link{Entry: draftEntry, Anchor: "access"}, warning: draft},
		{
			name:    "missing entry",
			link:    &content.Link{Entry: missingEntry},
			warning: `entry "` + missingEntry + `" does not exist; rendered without the link`,
		},
		{name: "media", link: &content.Link{Media: guideMedia}, want: &Link{Href: guide}},
		{
			name:    "missing media",
			link:    &content.Link{Media: missingMedia},
			warning: `media "` + missingMedia + `" does not exist; rendered without the link`,
		},
		{name: "anchor", link: &content.Link{Anchor: "top"}, want: &Link{Href: "#top"}},
		{name: "no target", link: &content.Link{}, warning: noTarget},
		{
			name:    "no target with newTab and rel",
			link:    &content.Link{NewTab: true, Rel: []content.Rel{content.RelNofollow}},
			warning: noTarget,
		},
		{
			name: "newTab",
			link: &content.Link{Href: "https://example.com/", NewTab: true},
			want: &Link{Href: "https://example.com/", Target: "_blank", Rel: "noopener"},
		},
		{
			name: "newTab and nofollow",
			link: &content.Link{Href: "https://example.com/", NewTab: true, Rel: []content.Rel{content.RelNofollow}},
			want: &Link{Href: "https://example.com/", Target: "_blank", Rel: "noopener nofollow"},
		},
		{
			name: "newTab, nofollow and sponsored",
			link: &content.Link{
				Href: "https://example.com/", NewTab: true, Rel: []content.Rel{content.RelNofollow, content.RelSponsored},
			},
			want: &Link{Href: "https://example.com/", Target: "_blank", Rel: "noopener nofollow sponsored"},
		},
		{
			name: "ugc alone",
			link: &content.Link{Entry: aboutEntry, Rel: []content.Rel{content.RelUGC}},
			want: &Link{Href: "/blog/about/", Rel: "ugc"},
		},
		{
			name: "rel in its order",
			link: &content.Link{Media: guideMedia, Rel: []content.Rel{content.RelSponsored, content.RelNofollow}},
			want: &Link{Href: guide, Rel: "sponsored nofollow"},
		},
		// Invalid links, which validation rejects.
		{
			name: "unknown rel values",
			link: &content.Link{Anchor: "top", NewTab: true, Rel: []content.Rel{"noopener", `a"b`}},
			want: &Link{Href: "#top", Target: "_blank", Rel: `noopener noopener a"b`},
		},
		{
			name: "href and entry",
			link: &content.Link{Href: "https://example.com/", Entry: aboutEntry},
			want: &Link{Href: "https://example.com/"},
		},
		{name: "invalid href and entry", link: &content.Link{Href: "javascript:alert(1)", Entry: aboutEntry}, warning: badScheme},
		{name: "href and anchor", link: &content.Link{Href: "/contact/", Anchor: "form"}, want: &Link{Href: "/blog/contact/"}},
		{name: "entry and media", link: &content.Link{Entry: aboutEntry, Media: guideMedia}, want: &Link{Href: "/blog/about/"}},
		{name: "unpublished entry and media", link: &content.Link{Entry: draftEntry, Media: guideMedia}, warning: draft},
		{name: "media and anchor", link: &content.Link{Media: guideMedia, Anchor: "top"}, want: &Link{Href: guide}},
		{name: "invalid anchor", link: &content.Link{Anchor: `a b"<`}, want: &Link{Href: "#a%20b%22%3C"}},
		{
			name: "invalid anchor of an entry",
			link: &content.Link{Entry: aboutEntry, Anchor: "a'b"},
			want: &Link{Href: "/blog/about/#a%27b"},
		},
	}
	r := newRenderer(t, testTheme(t, nil), "ja", "/blog", linkResolver)
	const path = "/3/attrs/link"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &walker{r: r, list: 2}
			if got := w.link(tt.link, path); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("link() = %+v, want %+v", got, tt.want)
			}
			var want []Warning
			if tt.warning != "" {
				want = []Warning{{List: 2, Path: path, Msg: tt.warning}}
			}
			if !slices.Equal(w.warnings, want) {
				t.Errorf("warnings = %q, want %q", w.warnings, want)
			}
		})
	}
}
