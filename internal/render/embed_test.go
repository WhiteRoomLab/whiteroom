package render

import (
	"reflect"
	"strings"
	"testing"
)

const (
	youTubeAllow = "accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share"
	vimeoAllow   = "autoplay; fullscreen; picture-in-picture; clipboard-write; encrypted-media; web-share"
	// youTubeFrame is the iframe of the video dQw4w9WgXcQ without a caption.
	youTubeFrame = `<iframe src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ" width="560" height="315" ` +
		`title="YouTube" loading="lazy" allow="` + youTubeAllow + `" referrerpolicy="strict-origin-when-cross-origin" ` +
		"allowfullscreen></iframe>"
	mapsURL = "https://www.google.com/maps/embed?pb=!1m18!1m12!1m3!1d3240.8!2d139.76!3d35.68!2m3!1f0!2f0!3f0"
)

// embedTheme is the settings file of a theme whose embeds have the variant wide.
const embedTheme = "schemaVersion: 1\nblocks:\n  embed:\n    variants:\n      wide: ワイド\n"

// embedJSON returns the JSON of a list of an embed with the ID b_000embed, provider and url.
func embedJSON(provider, url string) string {
	return `[{"type": "embed", "id": "b_000embed", "attrs": {"provider": "` + provider + `", "url": "` + url + `"}}]`
}

// captionJSON returns the JSON of a list of an embed with the ID b_000embed, provider, url and caption, rich text.
func captionJSON(provider, url, caption string) string {
	return `[{
		"type": "embed", "id": "b_000embed", "attrs": {"provider": "` + provider + `", "url": "` + url + `", "caption": ` +
		caption + `}
	}]`
}

func TestEmbed(t *testing.T) {
	const video = "<figure>" + youTubeFrame + "</figure>"
	// titled returns the figure of youTubeFrame with the title and the figcaption.
	titled := func(title, caption string) string {
		return "<figure>" + strings.Replace(youTubeFrame, `title="YouTube"`, `title="`+title+`"`, 1) + "<figcaption>" +
			caption + "</figcaption></figure>"
	}
	vimeo := func(query string) string {
		return `<figure><iframe src="https://player.vimeo.com/video/76979871` + query + `" width="640" height="360" ` +
			`title="Vimeo" loading="lazy" allow="` + vimeoAllow + `" referrerpolicy="strict-origin-when-cross-origin" ` +
			"allowfullscreen></iframe></figure>"
	}
	invalid := func(url, msg string) []string {
		return []string{`/0/attrs/url: invalid embed URL "` + url + `": ` + msg + "; not rendered"}
	}
	tests := []blockTest{
		{name: "youtube watch with t", json: embedJSON("youtube", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s"), want: video},
		{name: "youtu.be with t", json: embedJSON("youtube", "https://youtu.be/dQw4w9WgXcQ?t=42"), want: video},
		{name: "shorts", json: embedJSON("youtube", "https://www.youtube.com/shorts/dQw4w9WgXcQ"), want: video},
		{
			name: "nocookie embed with an upper-case host and start",
			json: embedJSON("youtube", "https://WWW.YouTube-NoCookie.com/embed/dQw4w9WgXcQ?start=42"),
			want: video,
		},
		{
			name: "caption with a hardBreak",
			json: captionJSON("youtube", "https://youtu.be/dQw4w9WgXcQ", `[
				{"type": "text", "text": "会社"}, {"type": "text", "text": "紹介", "marks": [{"type": "bold"}]},
				{"type": "hardBreak"}, {"type": "text", "text": "2026年"}
			]`),
			want: titled("会社紹介 2026年", "会社<strong>紹介</strong><br>2026年"),
		},
		{
			name: "caption with surrounding spaces",
			json: captionJSON("youtube", "https://youtu.be/dQw4w9WgXcQ", `[{"type": "text", "text": " 動画 "}, {"type": "hardBreak"}]`),
			want: titled("動画", " 動画 <br>"),
		},
		{
			name: "caption of spaces",
			json: captionJSON("youtube", "https://youtu.be/dQw4w9WgXcQ", `[{"type": "hardBreak"}, {"type": "text", "text": " "}]`),
			want: titled("YouTube", "<br> "),
		},
		{
			name: "escaping in the title",
			json: captionJSON("youtube", "https://youtu.be/dQw4w9WgXcQ", `[{"type": "text", "text": "\"A\" & <B> 'C'"}]`),
			want: titled("&#34;A&#34; &amp; &lt;B&gt; &#39;C&#39;", `"A" &amp; &lt;B&gt; 'C'`),
		},
		{name: "vimeo", json: embedJSON("vimeo", "https://vimeo.com/76979871"), want: vimeo("?dnt=1")},
		{name: "unlisted vimeo", json: embedJSON("vimeo", "https://vimeo.com/76979871/8272103f6e"), want: vimeo("?h=8272103f6e&amp;dnt=1")},
		{
			name: "vimeo player with h and other queries",
			json: embedJSON("vimeo", "https://player.vimeo.com/video/76979871?h=8272103f6e&badge=0&dnt=0"),
			want: vimeo("?h=8272103f6e&amp;dnt=1"),
		},
		{
			name: "vimeo player with h in upper case",
			json: embedJSON("vimeo", "https://player.vimeo.com/video/76979871?h=8272103F6E"),
			want: vimeo("?dnt=1"),
		},
		{
			name: "vimeo player with h that is not hex",
			json: embedJSON("vimeo", "https://player.vimeo.com/video/76979871?h=x%22y"),
			want: vimeo("?dnt=1"),
		},
		{
			name: "googleMaps",
			json: embedJSON("googleMaps", mapsURL),
			want: `<figure><iframe src="` + mapsURL + `" width="600" height="450" title="Google Maps" loading="lazy" ` +
				`referrerpolicy="no-referrer-when-downgrade" allowfullscreen></iframe></figure>`,
		},
		{
			name: "googleMaps with characters to encode",
			json: embedJSON("googleMaps", "https://www.google.com/maps/embed?pb=!1s'a b<c>&x=日本"),
			want: `<figure><iframe src="https://www.google.com/maps/embed?pb=!1s%27a%20b%3Cc%3E&amp;x=%E6%97%A5%E6%9C%AC" ` +
				`width="600" height="450" title="Google Maps" loading="lazy" referrerpolicy="no-referrer-when-downgrade" ` +
				"allowfullscreen></iframe></figure>",
		},
		{
			name: "x",
			json: embedJSON("x", "https://x.com/jack/status/20"),
			want: `<figure><p><a href="https://x.com/jack/status/20">https://x.com/jack/status/20</a></p></figure>`,
		},
		{
			name: "twitter with a query and a caption",
			json: captionJSON("x", "https://twitter.com/jack/status/20?s=20&t=a'b+c", `[{"type": "text", "text": "最初の投稿"}]`),
			want: `<figure><p><a href="https://twitter.com/jack/status/20?s=20&amp;t=a%27b&#43;c">` +
				"https://twitter.com/jack/status/20?s=20&amp;t=a%27b&#43;c</a></p><figcaption>最初の投稿</figcaption></figure>",
		},
		{
			name:     "javascript",
			json:     embedJSON("x", "javascript:alert(1)"),
			warnings: invalid("javascript:alert(1)", `scheme "javascript" is not allowed`),
		},
		{
			name:     "credentials",
			json:     embedJSON("youtube", "https://user:pass@www.youtube.com/watch?v=dQw4w9WgXcQ"),
			warnings: invalid("https://user:pass@www.youtube.com/watch?v=dQw4w9WgXcQ", "credentials in URLs are not allowed"),
		},
		{
			name:     "a backslash",
			json:     embedJSON("googleMaps", `https://www.google.com/maps/embed?pb=!1s\\evil`),
			warnings: invalid(`https://www.google.com/maps/embed?pb=!1s\\evil`, "backslashes are not allowed"),
		},
		{
			name:     "a playlist",
			json:     embedJSON("youtube", "https://www.youtube.com/playlist?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI"),
			warnings: invalid("https://www.youtube.com/playlist?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI", "not a URL that youtube can embed"),
		},
		{
			name:     "http",
			json:     embedJSON("youtube", "http://www.youtube.com/watch?v=dQw4w9WgXcQ"),
			warnings: invalid("http://www.youtube.com/watch?v=dQw4w9WgXcQ", "must be an https URL"),
		},
		{
			name:     "a URL of another provider",
			json:     embedJSON("vimeo", "https://youtu.be/dQw4w9WgXcQ"),
			warnings: invalid("https://youtu.be/dQw4w9WgXcQ", "not a URL that vimeo can embed"),
		},
		{
			name:     "no url",
			json:     `[{"type": "embed", "id": "b_000embed", "variant": "x", "attrs": {"provider": "youtube"}}]`,
			warnings: invalid("", "empty URL"),
		},
		{
			name: "unknown provider",
			json: `[
				` + paragraphJSON("b_0paragr1", "a") + `,
				{
					"type": "embed", "id": "b_000embed", "variant": "x",
					"attrs": {"provider": "tiktok", "url": "javascript:alert(1)", "caption": [{"type": "text", "text": "c"}]}
				},
				` + paragraphJSON("b_0paragr2", "b") + `
			]`,
			want:     "<p>a</p>\n<p>b</p>",
			warnings: []string{`/1/attrs/provider: unknown provider "tiktok"; not rendered`},
		},
		{
			name:     "no provider",
			json:     `[{"type": "embed", "id": "b_000embed", "attrs": {"url": "https://youtu.be/dQw4w9WgXcQ"}}]`,
			warnings: []string{`/0/attrs/provider: unknown provider ""; not rendered`},
		},
		{
			name:     "a provider in another case",
			json:     embedJSON("YouTube", "https://youtu.be/dQw4w9WgXcQ"),
			warnings: []string{`/0/attrs/provider: unknown provider "YouTube"; not rendered`},
		},
		{
			name: "classes and anchor",
			json: `[{
				"type": "embed", "id": "b_000embed", "anchor": "video", "variant": "wide", "className": "big",
				"style": {"margin": {"top": {"value": "1rem"}}},
				"attrs": {"provider": "x", "url": "https://x.com/jack/status/20"}
			}]`,
			want: `<figure id="video" class="b-000embed is-variant-wide big"><p><a href="https://x.com/jack/status/20">` +
				"https://x.com/jack/status/20</a></p></figure>",
			css: ".b-000embed {\n  margin-top: 1rem !important;\n}\n",
		},
		{
			name: "warnings in document order",
			json: `[{
				"type": "embed", "id": "b_000embed", "variant": "narrow",
				"attrs": {
					"provider": "youtube", "url": "https://youtu.be/dQw4w9WgXcQ",
					"caption": [{"type": "text", "text": "下書き", "marks": [{"type": "link", "attrs": {"entry": "` + draftEntry + `"}}]}]
				}
			}]`,
			want: titled("下書き", "下書き"),
			warnings: []string{
				`/0/variant: the theme has no embed variant "narrow"; rendered without its class`,
				`/0/attrs/caption/0/marks/0: entry "` + draftEntry + `" is not published; rendered without the link`,
			},
		},
	}
	checkBlocks(t, tests, embedTheme, "ja", "", linkResolver)
	// The URLs of the providers do not take the base.
	checkBlocks(t, []blockTest{
		{name: "youtube with the base", json: embedJSON("youtube", "https://youtu.be/dQw4w9WgXcQ"), want: video},
		{
			name: "x with the base",
			json: embedJSON("x", "https://x.com/jack/status/20"),
			want: `<figure><p><a href="https://x.com/jack/status/20">https://x.com/jack/status/20</a></p></figure>`,
		},
	}, embedTheme, "en", "/blog", linkResolver)
}

// TestEmbedData checks the data of embeds, also the fields that the default templates do not write.
func TestEmbedData(t *testing.T) {
	r := newRenderer(t, testTheme(t, map[string]string{"theme.yaml": embedTheme}), "ja", "/blog", linkResolver)
	tests := []struct {
		name string
		json string // a list of one embed
		want Embed
	}{
		{
			name: "youtube",
			json: captionJSON("youtube", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s", `[{"type": "text", "text": "動画"}]`),
			want: Embed{
				Common:         Common{ID: "b_000embed"},
				Provider:       "youtube",
				URL:            "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s",
				Src:            "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ",
				Title:          "動画",
				Width:          560,
				Height:         315,
				Allow:          youTubeAllow,
				ReferrerPolicy: "strict-origin-when-cross-origin",
				Caption:        "動画",
			},
		},
		{
			name: "vimeo",
			json: `[{
				"type": "embed", "id": "b_000embed", "variant": "wide",
				"attrs": {"provider": "vimeo", "url": "https://vimeo.com/76979871/8272103f6e"}
			}]`,
			want: Embed{
				Common:         Common{ID: "b_000embed", Class: "is-variant-wide"},
				Provider:       "vimeo",
				URL:            "https://vimeo.com/76979871/8272103f6e",
				Src:            "https://player.vimeo.com/video/76979871?h=8272103f6e&dnt=1",
				Title:          "Vimeo",
				Width:          640,
				Height:         360,
				Allow:          vimeoAllow,
				ReferrerPolicy: "strict-origin-when-cross-origin",
			},
		},
		{
			name: "x",
			json: embedJSON("x", "https://x.com/jack/status/20?s=日本"),
			want: Embed{
				Common:   Common{ID: "b_000embed"},
				Provider: "x",
				URL:      "https://x.com/jack/status/20?s=%E6%97%A5%E6%9C%AC",
				Title:    "X",
			},
		},
		{
			name: "googleMaps",
			json: embedJSON("googleMaps", mapsURL+"&q=日本"),
			want: Embed{
				Common:         Common{ID: "b_000embed"},
				Provider:       "googleMaps",
				URL:            mapsURL + "&q=%E6%97%A5%E6%9C%AC",
				Src:            mapsURL + "&q=%E6%97%A5%E6%9C%AC",
				Title:          "Google Maps",
				Width:          600,
				Height:         450,
				ReferrerPolicy: "no-referrer-when-downgrade",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := decodeBlocks(t, tt.json)[0]
			w := &walker{r: r}
			got, ok := w.embed(&b, "/0")
			if !ok || !reflect.DeepEqual(got, tt.want) || w.warnings != nil {
				t.Errorf("embed() = %+v, %v, warnings %q, want %+v", got, ok, w.warnings, tt.want)
			}
		})
	}
	// Not rendered: the zero data.
	b := decodeBlocks(t, embedJSON("youtube", "https://www.youtube.com/playlist?list=x"))[0]
	w := &walker{r: r}
	if got, ok := w.embed(&b, "/0"); ok || !reflect.DeepEqual(got, Embed{}) || len(w.warnings) != 1 {
		t.Errorf("embed() of a playlist = %+v, %v, warnings %q, want nothing and a warning", got, ok, w.warnings)
	}
}
