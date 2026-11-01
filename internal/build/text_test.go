package build

import (
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func TestFormatTitle(t *testing.T) {
	for _, tt := range []struct{ format, title, siteName, want string }{
		{content.DefaultTitleFormat, "会社概要", "株式会社〇〇", "会社概要 | 株式会社〇〇"},
		{"{title}｜{siteName}", "会社概要", "株式会社〇〇", "会社概要｜株式会社〇〇"},
		{"{siteName}: {title}", "About us", "Example Inc.", "Example Inc.: About us"},
		{"{title}", "About us", "Example Inc.", "About us"},
		// What the values have stays as it is.
		{"{title} - {siteName}", "{siteName}", "{title}", "{siteName} - {title}"},
		{"{title} | {siteName}", "", "", " | "},
	} {
		if got := formatTitle(tt.format, tt.title, tt.siteName); got != tt.want {
			t.Errorf("formatTitle(%q, %q, %q) = %q, want %q", tt.format, tt.title, tt.siteName, got, tt.want)
		}
	}
}

// TestTitles checks the titles of the pages of the fixture with 12 posts in each language, for <title> and of the
// pages themselves.
func TestTitles(t *testing.T) {
	type title struct{ path, pageTitle, title string }
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		want   []title
	}{
		{
			name: "the title formats of the fixture",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, servicesID, func(r *content.Revision) { r.SEO.Title = "Webサイトの制作サービス" })
				s.editRevision(t, homeID, func(r *content.Revision) { r.SEO.Title = "トップ" })
			},
			want: []title{
				{"/", "株式会社〇〇", "ホーム"},
				{"/about/", "会社概要｜株式会社〇〇", "会社概要"},
				{"/services/", "Webサイトの制作サービス｜株式会社〇〇", "サービス"},
				{"/news/", "お知らせ｜株式会社〇〇", "お知らせ"},
				{"/news/page/2/", "お知らせ｜株式会社〇〇", "お知らせ"},
				{"/2026/10/26/新サービス/", "新サービスを始めました｜株式会社〇〇", "新サービスを始めました"},
				{"/works/example-corporate-site/", "〇〇株式会社のコーポレートサイト｜株式会社〇〇", "〇〇株式会社のコーポレートサイト"},
				{"/category/news/", "お知らせ｜株式会社〇〇", "お知らせ"},
				{"/category/news/page/2/", "お知らせ｜株式会社〇〇", "お知らせ"},
				{"/tag/イベント/", "イベント｜株式会社〇〇", "イベント"},
				{"/works/", "制作実績｜株式会社〇〇", "制作実績"},
				{"/author/yamada/", "山田 太郎｜株式会社〇〇", "山田 太郎"},
				{"/author/yamada/page/2/", "山田 太郎｜株式会社〇〇", "山田 太郎"},
				{"/404.html", "株式会社〇〇", ""},
				{"/en/", "Example Inc.", ""},
				{"/en/page/2/", "Example Inc.", ""},
				{"/en/about/", "About us | Example Inc.", "About us"},
				{"/en/works/", "制作実績 | Example Inc.", "制作実績"},
				{"/en/tag/events/", "Events | Example Inc.", "Events"},
				{"/en/author/yamada/", "山田 太郎 | Example Inc.", "山田 太郎"},
				{"/en/404.html", "Example Inc.", ""},
			},
		},
		{
			name: "the default title format, and a type without a label",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				delete(site.SEO.TitleFormat, "ja")
				s.putSettings(t, site)
				s.schema.Data = []byte(strings.Replace(testSchema, "    label: 制作実績\n", "", 1))
			},
			want: []title{
				{"/", "株式会社〇〇", "ホーム"},
				{"/about/", "会社概要 | 株式会社〇〇", "会社概要"},
				{"/works/", "works | 株式会社〇〇", "works"},
				{"/en/works/", "works | Example Inc.", "works"},
			},
		},
		{
			name: "the latest posts at the front in ja",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage, site.PostsPage = nil, nil
				s.putSettings(t, site)
			},
			want: []title{
				{"/", "株式会社〇〇", ""},
				{"/page/2/", "株式会社〇〇", ""},
				{"/home/", "ホーム｜株式会社〇〇", "ホーム"},
				{"/news/", "お知らせ｜株式会社〇〇", "お知らせ"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			src.addPosts(t, "ja", 11)
			src.addPosts(t, "en", 11)
			tt.change(t, src)
			b := mustLoad(t, src)
			for _, w := range tt.want {
				p := pageAt(b, w.path)
				if p == nil {
					t.Errorf("no page at %q", w.path)
					continue
				}
				if got := b.pageTitle(p); got != w.pageTitle {
					t.Errorf("pageTitle() of %q = %q, want %q", w.path, got, w.pageTitle)
				}
				if got := b.heading(p); got != w.title {
					t.Errorf("heading() of %q = %q, want %q", w.path, got, w.title)
				}
			}
		})
	}
}

// TestDescriptions checks the descriptions of the pages of the fixture with 12 posts in each language.
func TestDescriptions(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 11)
	src.addPosts(t, "en", 11)
	src.editRevision(t, servicesID, func(r *content.Revision) { r.Excerpt = "Webサイトの企画、\n\n  制作と運用。" })
	src.editRevision(t, privacyID, func(r *content.Revision) {
		r.Excerpt = "抜粋"
		r.SEO.Description = "個人情報の  取り扱いについて。"
	})
	site := testSite()
	site.Tagline["en"] = "Websites,  planned and made."
	src.putSettings(t, site)
	b := mustLoad(t, src)
	newsDescription := "会社からのお知らせです。 新しいサービスやイベントの情報を掲載します。"
	for _, tt := range []struct{ path, want string }{
		{"/", "Webサイトの企画と制作の会社です。"},
		{"/about/", "株式会社〇〇の会社概要です。"},
		{"/services/", "Webサイトの企画、 制作と運用。"},
		{"/privacy/", "個人情報の 取り扱いについて。"},
		{"/news/", "会社からのお知らせです。"},
		{"/news/page/2/", "会社からのお知らせです。"},
		{"/2026/10/26/新サービス/", "Webサイトの更新と運用をお手伝いするサービスです。"},
		{"/category/news/", newsDescription},
		{"/category/news/page/2/", newsDescription},
		{"/tag/イベント/", ""},
		{"/works/", ""},
		{"/author/yamada/", "編集長。Webデザインとアクセシビリティについて書いています。 著書に『はじめてのWebアクセシビリティ』があります。"},
		{"/en/author/yamada/", "Editor in chief. Writes about web design and accessibility."},
		{"/en/", "Websites, planned and made."},
		{"/en/page/2/", "Websites, planned and made."},
		{"/en/about/", "We plan and make websites."},
		{"/404.html", ""},
		{"/en/404.html", ""},
	} {
		p := pageAt(b, tt.path)
		if p == nil {
			t.Errorf("no page at %q", tt.path)
			continue
		}
		if got := b.description(p); got != tt.want {
			t.Errorf("description() of %q = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestAutoExcerpt(t *testing.T) {
	const ideographicSpace = rune(0x3000)
	text := func(s string) content.RichText { return content.RichText{{Type: content.InlineText, Text: s}} }
	block := func(typ content.BlockType, children ...content.Block) content.Block {
		return content.Block{Type: typ, Children: children}
	}
	cell := func(s string) content.Block { return content.Block{Type: content.BlockTableCell, Text: text(s)} }
	tests := []struct {
		name string
		body content.Blocks
		want string
	}{
		{name: "no body"},
		{name: "white space", body: content.Blocks{paragraph("b_1", " \tこんにちは\n\n世界  ")}, want: "こんにちは 世界"},
		{
			name: "an ideographic space",
			body: content.Blocks{paragraph("b_1", string(ideographicSpace)+"字下げ"+string(ideographicSpace)+"の段落")},
			want: "字下げ の段落",
		},
		{
			name: "texts with marks and hardBreaks",
			body: content.Blocks{{Type: content.BlockParagraph, Text: content.RichText{
				{Type: content.InlineText, Text: "株式会社"},
				{Type: content.InlineText, Text: "〇〇", Marks: []content.Mark{{Type: content.MarkBold}}},
				{Type: content.InlineHardBreak},
				{Type: content.InlineText, Text: "東京都"},
				{Type: content.InlineHardBreak},
				{Type: content.InlineHardBreak},
				{Type: content.InlineText, Text: "千代田区"},
			}}},
			want: "株式会社〇〇 東京都 千代田区",
		},
		{
			name: "blocks in order, in their containers",
			body: content.Blocks{
				{Type: content.BlockHeading, Attrs: content.Attrs{Level: 2}, Text: text("見出し")},
				block(content.BlockList,
					block(content.BlockListItem, paragraph("b_1", "項目1"),
						block(content.BlockList, block(content.BlockListItem, paragraph("b_2", "項目1.1")))),
					block(content.BlockListItem, paragraph("b_3", "項目2")),
				),
				{
					Type:     content.BlockQuote,
					Attrs:    content.Attrs{Citation: text("出典")},
					Children: content.Blocks{paragraph("b_4", "引用1"), paragraph("b_5", "引用2")},
				},
				{
					Type:     content.BlockTable,
					Attrs:    content.Attrs{Caption: text("表の見出し")},
					Children: content.Blocks{block(content.BlockTableRow, cell("セル1"), cell("セル2"))},
				},
				block(content.BlockColumns,
					block(content.BlockColumn, paragraph("b_6", "左")),
					block(content.BlockColumn, block(content.BlockGroup, paragraph("b_7", "右"))),
				),
				paragraph("b_8", "最後"),
			},
			want: "見出し 項目1 項目1.1 項目2 引用1 引用2 出典 セル1 セル2 左 右 最後",
		},
		{
			name: "blocks left out",
			body: content.Blocks{
				{Type: content.BlockImage, Attrs: content.Attrs{Media: imageID, Alt: "代替", Caption: text("キャプション")}},
				block(content.BlockButtons,
					content.Block{Type: content.BlockButton, Attrs: content.Attrs{Label: "ボタン"}}),
				{Type: content.BlockSeparator},
				{Type: content.BlockSpacer},
				{Type: content.BlockCode, Code: "fmt.Println()"},
				{Type: content.BlockEmbed, Attrs: content.Attrs{URL: "https://example.com/", Caption: text("埋め込み")}},
				{Type: content.BlockFile, Attrs: content.Attrs{Media: pdfID, Label: "ファイル"}},
				{Type: content.BlockForm, Attrs: content.Attrs{Form: formID}},
				{Type: content.BlockHTML, Attrs: content.Attrs{HTML: "<p>HTML</p>"}},
				{Type: content.BlockLegacy, Attrs: content.Attrs{HTML: "<p>legacy</p>"}},
				paragraph("b_1", "本文"),
			},
			want: "本文",
		},
		{
			name: "exactly excerptLength code points",
			body: content.Blocks{paragraph("b_1", strings.Repeat("あ", 120))},
			want: strings.Repeat("あ", 120),
		},
		{
			name: "one code point more",
			body: content.Blocks{paragraph("b_1", strings.Repeat("あ", 121))},
			want: strings.Repeat("あ", 120) + "…",
		},
		{
			name: "cut in a long body",
			body: content.Blocks{paragraph("b_1", strings.Repeat("あ", 100)), paragraph("b_2", strings.Repeat("い", 1000))},
			want: strings.Repeat("あ", 100) + " " + strings.Repeat("い", 19) + "…",
		},
		{
			name: "with a space between blocks",
			body: content.Blocks{paragraph("b_1", strings.Repeat("a", 60)), paragraph("b_2", strings.Repeat("b", 59))},
			want: strings.Repeat("a", 60) + " " + strings.Repeat("b", 59),
		},
		{
			name: "one code point more with a space between blocks",
			body: content.Blocks{paragraph("b_1", strings.Repeat("a", 60)), paragraph("b_2", strings.Repeat("b", 60))},
			want: strings.Repeat("a", 60) + " " + strings.Repeat("b", 59) + "…",
		},
		{
			name: "more text after excerptLength code points",
			body: content.Blocks{
				paragraph("b_1", strings.Repeat("a", 60)), paragraph("b_2", strings.Repeat("b", 59)), paragraph("b_3", "c"),
			},
			want: strings.Repeat("a", 60) + " " + strings.Repeat("b", 59) + "…",
		},
		{
			name: "cut at a space",
			body: content.Blocks{paragraph("b_1", strings.Repeat("a", 119)+" "+strings.Repeat("b", 10))},
			want: strings.Repeat("a", 119) + "…",
		},
		{
			name: "white space not counted",
			body: content.Blocks{paragraph("b_1", strings.Repeat("a", 60)+"\n\n\n"+strings.Repeat("b", 59)+"  ")},
			want: strings.Repeat("a", 60) + " " + strings.Repeat("b", 59),
		},
	}
	for _, tt := range tests {
		if got := autoExcerpt(tt.body); got != tt.want {
			t.Errorf("%s: autoExcerpt() = %q, want %q", tt.name, got, tt.want)
		}
	}
}
