package wpimport

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

type bodyXML struct {
	id          int
	typ, status string // "" for post and publish
	content     string
	meta        [][2]string // the keys and the values of the postmeta
}

func bodiesWXR(items ...bodyXML) string {
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("<title>Site</title>\n<link>https://example.com</link>\n<description>A tagline</description>\n<language>ja</language>\n")
	for _, it := range items {
		fmt.Fprintf(&b, "<item>\n<title><![CDATA[Item %d]]></title>\n<link>https://example.com/?p=%d</link>\n"+
			"<content:encoded><![CDATA[%s]]></content:encoded>\n<wp:post_id>%d</wp:post_id>\n"+
			"<wp:status><![CDATA[%s]]></wp:status>\n<wp:post_type><![CDATA[%s]]></wp:post_type>\n",
			it.id, it.id, it.content, it.id, cmp.Or(it.status, "publish"), cmp.Or(it.typ, "post"))
		for _, m := range it.meta {
			fmt.Fprintf(&b, "<wp:postmeta><wp:meta_key><![CDATA[%s]]></wp:meta_key><wp:meta_value><![CDATA[%s]]></wp:meta_value></wp:postmeta>\n", m[0], m[1])
		}
		b.WriteString("</item>\n")
	}
	b.WriteString(tail)
	return b.String()
}

// blockEntries lists the entries of the report that are about the names in the blocks, as "kind: message" and, if there is
// a place in the settings, " -> " and the pointer.
func blockEntries(rep *Report) []string {
	var out []string
	for _, e := range rep.Entries() {
		if strings.HasPrefix(e.Message, "the blocks use") || strings.HasPrefix(e.Message, "the images use") ||
			strings.HasPrefix(e.Message, "the text uses") || strings.Contains(e.Message, "custom HTML block") {
			line := string(e.Kind) + ": " + e.Message
			if e.Target != (Target{}) {
				line += " -> " + e.Target.File + "#" + e.Target.Pointer
			}
			out = append(out, line)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

// paragraphDoc writes a paragraph with the attributes attrs and the HTML markup.
func paragraphDoc(attrs, markup string) string {
	return "<!-- wp:paragraph " + attrs + " -->" + markup + "<!-- /wp:paragraph -->"
}

func TestProposeBlocks(t *testing.T) {
	const (
		suffix = " of WordPress: "
		maps   = "; blocks.%s maps them to the %s of the theme of the site"
	)
	presetsMsg := func(n, kind, names, key string) string {
		return "review: the blocks use " + n + " " + kind + suffix + names + fmt.Sprintf(maps, "presets."+key, "presets")
	}
	t.Run("presets", func(t *testing.T) {
		in := bodiesWXR(
			bodyXML{id: 1, content: paragraphDoc(`{"textColor":"vivid-red","fontSize":"large","style":{"color":{"background":"var:preset|color|vivid-cyan","gradient":"var:preset|gradient|sunset"},`+
				`"typography":{"fontFamily":"var(--wp--preset--font-family--system)"},"spacing":{"padding":{"top":"var:preset|spacing|40"}}}}`, "<p>a</p>") + "\n\n" +
				`<!-- wp:group {"backgroundColor":"vivid-red","gradient":"sunset","borderColor":"black","overlayColor":"x"} --><div class="wp-block-group"></div><!-- /wp:group -->`},
			bodyXML{id: 2, typ: "page", content: paragraphDoc(`{"textColor":"vivid-red","fontSize":"large","fontFamily":"system","style":{"color":{"gradient":"linear-gradient(var(--wp--preset--color--vivid-red) 0%, var(--wp--preset--color--teal) 100%)"}}}`, "<p>b</p>")},
		)
		s, imp := proposeFrom(t, in, nil, nil)
		want := emptyBlockSettings()
		want.Presets.Color = map[string]string{"vivid-red": "vivid-red", "vivid-cyan": "vivid-cyan", "black": "black", "x": "x", "teal": "teal"}
		want.Presets.Gradient = map[string]string{"sunset": "sunset"}
		want.Presets.FontSize = map[string]string{"large": "large"}
		want.Presets.FontFamily = map[string]string{"system": "system"}
		want.Presets.Spacing = map[string]string{"40": "40"}
		if !reflect.DeepEqual(s.Blocks, want) {
			t.Errorf("Blocks = %+v, want %+v", s.Blocks, want)
		}
		sameSet(t, "the entries", blockEntries(imp.Report), []string{
			presetsMsg("5", "color presets", `"vivid-red" (4 times), "black" (1 time), "teal" (1 time), "vivid-cyan" (1 time), "x" (1 time)`, "color"),
			presetsMsg("1", "gradient preset", `"sunset" (2 times)`, "gradient"),
			presetsMsg("1", "font size preset", `"large" (2 times)`, "fontSize"),
			presetsMsg("1", "font family preset", `"system" (2 times)`, "fontFamily"),
			presetsMsg("1", "spacing preset", `"40" (1 time)`, "spacing"),
		})
	})

	t.Run("block styles and image sizes", func(t *testing.T) {
		in := bodiesWXR(bodyXML{id: 1, content: strings.Join([]string{
			paragraphDoc(`{"className":"is-style-rounded extra"}`, "<p>a</p>"),
			paragraphDoc(`{"className":"is-style-default is-style-rounded is-style-rounded"}`, "<p>b</p>"),
			paragraphDoc(`{"className":"is-style-日本 is-style-"}`, "<p>c</p>"),
			`<!-- wp:pullquote --><figure class="wp-block-pullquote"><blockquote><p>q</p></blockquote></figure><!-- /wp:pullquote -->`,
			`<!-- wp:pullquote {"className":"is-style-solid-color"} --><figure class="wp-block-pullquote"></figure><!-- /wp:pullquote -->`,
			`<!-- wp:image {"id":5,"sizeSlug":"large"} --><figure class="wp-block-image size-large"><img src="x.png"/></figure><!-- /wp:image -->`,
			`<!-- wp:image {"id":6,"sizeSlug":"large"} --><figure class="wp-block-image size-large"><img src="y.png"/></figure><!-- /wp:image -->`,
			`<!-- wp:image {"id":7,"sizeSlug":"full"} --><figure class="wp-block-image size-full"><img src="z.png"/></figure><!-- /wp:image -->`,
			`<!-- wp:gallery {"sizeSlug":"medium"} --><figure class="wp-block-gallery"></figure><!-- /wp:gallery -->`,
			`<!-- wp:media-text {"mediaSizeSlug":"thumbnail"} --><div class="wp-block-media-text"></div><!-- /wp:media-text -->`,
			`<!-- wp:media-text {"mediaSizeSlug":"full"} --><div class="wp-block-media-text"></div><!-- /wp:media-text -->`,
			paragraphDoc(`{"sizeSlug":"nope"}`, "<p>d</p>"),
			`<!-- wp:image {"sizeSlug":"大"} --><figure class="wp-block-image"></figure><!-- /wp:image -->`,
		}, "\n")})
		s, imp := proposeFrom(t, in, nil, nil)
		wantVariants := map[string]string{"rounded": "rounded", "日本": "", "pullquote": "pullquote", "solid-color": "solid-color"}
		wantSizes := map[string]string{"large": "large", "medium": "medium", "thumbnail": "thumbnail", "大": ""}
		if !reflect.DeepEqual(s.Blocks.Variants, wantVariants) || !reflect.DeepEqual(s.Blocks.ImageSizes, wantSizes) {
			t.Errorf("Variants = %+v, ImageSizes = %+v, want %+v, %+v", s.Blocks.Variants, s.Blocks.ImageSizes, wantVariants, wantSizes)
		}
		sameSet(t, "the entries", blockEntries(imp.Report), []string{
			`review: the blocks use 4 block styles of WordPress: "pullquote" (2 times), "rounded" (2 times), "solid-color" (1 time), "日本" (1 time)` +
				"; blocks.variants maps them to the variants of the theme of the site",
			`review: the images use 4 image sizes of WordPress: "large" (2 times), "medium" (1 time), "thumbnail" (1 time), "大" (1 time)` +
				"; blocks.imageSizes maps them to the image sizes of the theme of the site",
		})
	})

	t.Run("the classes of inline elements", func(t *testing.T) {
		in := bodiesWXR(bodyXML{id: 1, content: strings.Join([]string{
			paragraphDoc("", `<p>a <mark style="color:#f00" class="has-inline-color has-vivid-red-color">b</mark> <span class="marker">c</span> `+
				`<SPAN class="marker note">d</SPAN> <span class="wp-block-foo has-text-color has-background-dim">e</span> <span class="has-yellow-background-color">f</span> `+
				`<b class="not-counted">g</b> <span>h</span> <mark>i</mark> <span class="日本">j</span></p>`),
			// The spans of a cover are not text: the overlay is counted once, by its attribute.
			`<!-- wp:cover {"overlayColor":"black"} --><div class="wp-block-cover"><span aria-hidden="true" class="wp-block-cover__background has-black-background-color has-background-dim"></span></div><!-- /wp:cover -->`,
			`<!-- wp:code --><pre class="wp-block-code"><code><span class="hljs-keyword">func</span></code></pre><!-- /wp:code -->`,
			`<!-- wp:html --><div><span class="from-html">x</span></div><!-- /wp:html -->`,
		}, "\n")})
		s, imp := proposeFrom(t, in, nil, nil)
		wantMarks := map[string]string{"marker": "", "note": "", "日本": ""}
		if !reflect.DeepEqual(s.Blocks.Marks, wantMarks) {
			t.Errorf("Marks = %+v, want %+v", s.Blocks.Marks, wantMarks)
		}
		wantColors := map[string]string{"vivid-red": "vivid-red", "yellow": "yellow", "black": "black"}
		if !reflect.DeepEqual(s.Blocks.Presets.Color, wantColors) {
			t.Errorf("Color = %+v, want %+v", s.Blocks.Presets.Color, wantColors)
		}
		sameSet(t, "the entries", blockEntries(imp.Report), []string{
			presetsMsg("3", "color presets", `"black" (1 time), "vivid-red" (1 time), "yellow" (1 time)`, "color"),
			"review: 1 custom HTML block becomes a legacy block unless site.allowHtml is true -> site/settings.json#/allowHtml",
			`review: the text uses 3 classes of inline elements: "marker" (2 times), "note" (1 time), "日本" (1 time)` +
				"; blocks.marks maps them to the highlight variants of the theme of the site, and the others keep only their text",
		})
	})

	t.Run("custom HTML", func(t *testing.T) {
		html := `<!-- wp:html --><div>x</div><!-- /wp:html -->`
		frame := func(src string) string {
			return `<!-- wp:html --><iframe width="560" height="315" src="` + src + `" frameborder="0" allowfullscreen></iframe><!-- /wp:html -->`
		}
		const one = "review: 1 custom HTML block becomes a legacy block unless site.allowHtml is true -> site/settings.json#/allowHtml"
		for _, tt := range []struct {
			name    string
			content string
			want    []string
		}{
			{"none", paragraphDoc("", "<p>a</p>"), nil},
			{"one", html, []string{"review: 1 custom HTML block becomes a legacy block unless site.allowHtml is true -> site/settings.json#/allowHtml"}},
			{"two", html + html, []string{"review: 2 custom HTML blocks become legacy blocks unless site.allowHtml is true -> site/settings.json#/allowHtml"}},
			{"inside a group", `<!-- wp:group --><div class="wp-block-group">` + html + `</div><!-- /wp:group -->` + html,
				[]string{"review: 2 custom HTML blocks become legacy blocks unless site.allowHtml is true -> site/settings.json#/allowHtml"}},
			// What the conversion makes of the block whatever the site allows is not counted: an embed block, and nothing.
			{"an iframe that a provider embeds", frame("https://www.youtube.com/embed/dQw4w9WgXcQ") + frame("//player.vimeo.com/video/76979871"), nil},
			{"an iframe that a provider embeds, and custom HTML", frame("https://www.youtube.com/embed/dQw4w9WgXcQ") + html, []string{one}},
			{"an iframe of another site", frame("https://example.com/video"), []string{one}},
			{"two iframes", frame("https://www.youtube.com/embed/dQw4w9WgXcQ") + `<!-- wp:html --><iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe>` +
				`<iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe><!-- /wp:html -->`, []string{one}},
			{"white space and a control character only", "<!-- wp:html --> \n\t <!-- /wp:html --><!-- wp:html /--><!-- wp:html -->" + string(rune(0x85)) + " <!-- /wp:html -->", nil},
			{"white space only, and custom HTML", "<!-- wp:html -->\n<!-- /wp:html -->" + html, []string{one}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				s, imp := proposeFrom(t, bodiesWXR(bodyXML{id: 1, content: tt.content}), nil, nil)
				sameSet(t, "the entries", blockEntries(imp.Report), tt.want)
				if s.Site.AllowHTML {
					t.Error("allowHtml is proposed")
				}
			})
		}
	})

	t.Run("names that whiteroom does not accept", func(t *testing.T) {
		in := bodiesWXR(bodyXML{id: 1, content: paragraphDoc(`{"textColor":"赤","fontSize":"a.b","gradient":"ok_1"}`, "<p>a</p>")})
		s, _ := proposeFrom(t, in, nil, nil)
		if want := (map[string]string{"赤": ""}); !reflect.DeepEqual(s.Blocks.Presets.Color, want) {
			t.Errorf("Color = %+v, want %+v", s.Blocks.Presets.Color, want)
		}
		if want := (map[string]string{"a.b": ""}); !reflect.DeepEqual(s.Blocks.Presets.FontSize, want) {
			t.Errorf("FontSize = %+v, want %+v", s.Blocks.Presets.FontSize, want)
		}
		if want := (map[string]string{"ok_1": "ok_1"}); !reflect.DeepEqual(s.Blocks.Presets.Gradient, want) {
			t.Errorf("Gradient = %+v, want %+v", s.Blocks.Presets.Gradient, want)
		}
		var errs []string
		checkBlockNames(&s.Blocks, func(pointer, format string, args ...any) {
			errs = append(errs, pointer+": "+fmt.Sprintf(format, args...))
		})
		if len(errs) > 0 {
			t.Errorf("the proposal is not accepted by Site: %q", errs)
		}
	})

	t.Run("more than ten names", func(t *testing.T) {
		var items []bodyXML
		for i := 1; i <= 12; i++ {
			items = append(items, bodyXML{id: i, content: paragraphDoc(fmt.Sprintf(`{"textColor":"c%02d"}`, i), "<p>a</p>")})
		}
		items = append(items, bodyXML{id: 13, content: paragraphDoc(`{"textColor":"c12"}`, "<p>a</p>")})
		s, imp := proposeFrom(t, bodiesWXR(items...), nil, nil)
		if len(s.Blocks.Presets.Color) != 12 {
			t.Errorf("Color has %d names, want 12", len(s.Blocks.Presets.Color))
		}
		sameSet(t, "the entries", blockEntries(imp.Report), []string{
			presetsMsg("12", "color presets", `"c12" (2 times), "c01" (1 time), "c02" (1 time), "c03" (1 time), "c04" (1 time), "c05" (1 time), `+
				`"c06" (1 time), "c07" (1 time), "c08" (1 time), "c09" (1 time), and 2 more`, "color"),
		})
	})

	t.Run("the items that are read", func(t *testing.T) {
		defs := &content.Site{Types: []content.TypeDef{{Name: "product"}}}
		use := func(slug string) string { return paragraphDoc(`{"textColor":"`+slug+`"}`, "<p>a</p>") }
		in := bodiesWXR(
			bodyXML{id: 1, content: use("post")},
			bodyXML{id: 2, typ: "page", content: use("page")},
			bodyXML{id: 3, typ: "wp_block", content: use("wp-block")},
			bodyXML{id: 4, typ: "product", content: use("product")},
			bodyXML{id: 5, status: "draft", content: use("draft")},
			bodyXML{id: 6, status: "private", content: use("private")},
			bodyXML{id: 7, status: "trash", content: use("trash")},
			bodyXML{id: 8, status: "auto-draft", content: use("auto-draft")},
			bodyXML{id: 9, typ: "event", content: use("undefined-type")},
			bodyXML{id: 10, typ: "attachment", content: use("attachment")},
			bodyXML{id: 11, typ: "nav_menu_item", content: use("menu")},
			bodyXML{id: 12, typ: "wp_template", content: use("template")},
			bodyXML{id: 13, typ: "wpcf7_contact_form", content: use("form")},
			bodyXML{id: 14, content: ""},
		)
		s, _ := proposeFrom(t, in, nil, defs)
		want := map[string]string{"post": "post", "page": "page", "wp-block": "wp-block", "product": "product", "draft": "draft", "private": "private"}
		if !reflect.DeepEqual(s.Blocks.Presets.Color, want) {
			t.Errorf("Color = %+v, want %+v", s.Blocks.Presets.Color, want)
		}
	})

	t.Run("the blocks that are read", func(t *testing.T) {
		in := bodiesWXR(bodyXML{id: 1, content: strings.Join([]string{
			`<!-- wp:acme/box {"textColor":"plugin"} --><div><!-- wp:paragraph {"textColor":"inside-plugin"} --><p>x</p><!-- /wp:paragraph --></div><!-- /wp:acme/box -->`,
			`<!-- wp:latest-posts {"fontSize":"dynamic"} /-->`,
			`<!-- wp:post-title {"textColor":"dynamic2"} /-->`,
			`<!-- wp:freeform --><p>x</p><!-- /wp:freeform -->`,
			`<p class="has-text-color has-classic-color">outside</p>`,
			`<!-- wp:group {"textColor":"outer"} --><div class="wp-block-group"><!-- wp:columns --><div class="wp-block-columns"><!-- wp:column --><div class="wp-block-column">` +
				`<!-- wp:paragraph {"textColor":"inner"} --><p>x</p><!-- /wp:paragraph --></div><!-- /wp:column --></div><!-- /wp:columns --></div><!-- /wp:group -->`,
			`<!-- wp:paragraph {bad json} --><p>x <span class="marker">y</span></p><!-- /wp:paragraph -->`,
		}, "\n")})
		s, _ := proposeFrom(t, in, nil, nil)
		want := map[string]string{"outer": "outer", "inner": "inner"}
		if !reflect.DeepEqual(s.Blocks.Presets.Color, want) || len(s.Blocks.Presets.FontSize) != 0 {
			t.Errorf("Color = %+v, FontSize = %+v, want %+v and none", s.Blocks.Presets.Color, s.Blocks.Presets.FontSize, want)
		}
		if want := map[string]string{"marker": ""}; !reflect.DeepEqual(s.Blocks.Marks, want) {
			t.Errorf("Marks = %+v, want %+v", s.Blocks.Marks, want)
		}
	})

	t.Run("the limits", func(t *testing.T) {
		in := bodiesWXR(
			bodyXML{id: 1, content: paragraphDoc(`{"textColor":"first"}`, `<p><span class="one">a</span></p>`)},
			bodyXML{id: 2, content: paragraphDoc(`{"textColor":"second"}`, `<p><span class="two">`+strings.Repeat("a", 150)+`</span></p>`)},
			bodyXML{id: 3, content: paragraphDoc(`{"textColor":"third"}`, `<p><span class="three">`+strings.Repeat("a", 400)+`</span></p>`)},
		)
		lowerLimit(t, &maxBodyBytes, 300)
		lowerLimit(t, &maxHTMLBytes, 90)
		s, _ := proposeFrom(t, in, nil, nil)
		// The third item is too large to be read at all; the HTML of the second is too much to be read.
		want := map[string]string{"first": "first", "second": "second"}
		if !reflect.DeepEqual(s.Blocks.Presets.Color, want) {
			t.Errorf("Color = %+v, want %+v", s.Blocks.Presets.Color, want)
		}
		if want := map[string]string{"one": ""}; !reflect.DeepEqual(s.Blocks.Marks, want) {
			t.Errorf("Marks = %+v, want %+v", s.Blocks.Marks, want)
		}
	})

	t.Run("a block with more HTML than one block may have", func(t *testing.T) {
		// Its HTML is not read, as the conversion does not read it: a custom HTML block is counted as text that is not read.
		lowerLimit(t, &maxBlockHTMLBytes, 60)
		in := bodiesWXR(bodyXML{id: 1, content: strings.Join([]string{
			paragraphDoc("", `<p><span class="one">a</span></p>`),
			paragraphDoc("", `<p><span class="two">`+strings.Repeat("a", 30)+`</span></p>`),
			`<!-- wp:html --><iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe><!-- /wp:html -->`,
		}, "\n")})
		s, imp := proposeFrom(t, in, nil, nil)
		if want := map[string]string{"one": ""}; !reflect.DeepEqual(s.Blocks.Marks, want) {
			t.Errorf("Marks = %+v, want %+v", s.Blocks.Marks, want)
		}
		const one = "review: 1 custom HTML block becomes a legacy block unless site.allowHtml is true -> site/settings.json#/allowHtml"
		if entries := blockEntries(imp.Report); !slices.Contains(entries, one) {
			t.Errorf("the entries are %q, want one that is %q", entries, one)
		}
	})

	t.Run("too many blocks", func(t *testing.T) {
		in := bodiesWXR(
			bodyXML{id: 1, content: strings.Repeat(`<!-- wp:separator /-->`, 11) + paragraphDoc(`{"textColor":"late"}`, "<p>a</p>")},
			bodyXML{id: 2, content: paragraphDoc(`{"textColor":"early"}`, "<p>a</p>")},
		)
		lowerLimit(t, &maxWPBlocks, 10)
		s, _ := proposeFrom(t, in, nil, nil)
		if want := map[string]string{"early": "early"}; !reflect.DeepEqual(s.Blocks.Presets.Color, want) {
			t.Errorf("Color = %+v, want %+v", s.Blocks.Presets.Color, want)
		}
	})

	const (
		sizesMsg = "; blocks.imageSizes maps them to the image sizes of the theme of the site"
		marksMsg = "; blocks.marks maps them to the highlight variants of the theme of the site, and the others keep only their text"
	)
	sizesEntry := func(n int, names string) string {
		return "review: the images use " + plural(n, "image size", "image sizes") + suffix + names + sizesMsg
	}
	marksEntry := func(n int, names string) string {
		return "review: the text uses " + plural(n, "class", "classes") + " of inline elements: " + names + marksMsg
	}
	// classicNames proposes the settings for the items, and returns the names of the image sizes and of the marks in them.
	classicNames := func(t *testing.T, items ...bodyXML) (sizes, marks []string, imp *Import) {
		t.Helper()
		s, imp := proposeFrom(t, bodiesWXR(items...), nil, nil)
		return sortedKeys(s.Blocks.ImageSizes), sortedKeys(s.Blocks.Marks), imp
	}

	t.Run("the HTML of the classic editor: a content that has no block", func(t *testing.T) {
		in := bodiesWXR(bodyXML{id: 1, content: `<p><img class="alignleft size-medium wp-image-21" src="x.png"> text</p>` + "\n\n" +
			`<img class="size-full" src="y.png"><img class="aligncenter size-large size-huge" src="z.png"><img class="no-size" src="w.png">` + "\n\n" +
			`<span class="marker">a</span> <mark class="note has-vivid-red-color">b</mark>` + "\n\n" +
			`[gallery ids="1" size="large"] [gallery ids="2"] [gallery size="full"] [gallery size=" "] [[gallery size="skipped"]]` + "\n\n" +
			`<a title="[gallery size=hidden]">x</a> [galleryx size="other"] [Gallery size="other"] [gallery-2 size="other"]`})
		s, imp := proposeFrom(t, in, nil, nil)
		want := emptyBlockSettings()
		want.ImageSizes = map[string]string{"medium": "medium", "large": "large", "thumbnail": "thumbnail"}
		want.Marks = map[string]string{"marker": "", "note": ""}
		want.Presets.Color = map[string]string{"vivid-red": "vivid-red"}
		if !reflect.DeepEqual(s.Blocks, want) {
			t.Errorf("Blocks = %+v, want %+v", s.Blocks, want)
		}
		// The first size- class of an image counts, and the size full is none; a gallery with no size is thumbnail.
		sameSet(t, "the entries", blockEntries(imp.Report), []string{
			presetsMsg("1", "color preset", `"vivid-red" (1 time)`, "color"),
			sizesEntry(3, `"large" (2 times), "thumbnail" (2 times), "medium" (1 time)`),
			marksEntry(2, `"marker" (1 time), "note" (1 time)`),
		})
	})

	t.Run("the HTML of the classic editor: the HTML between blocks, the classic block and the shortcode block", func(t *testing.T) {
		in := bodiesWXR(bodyXML{id: 1, content: strings.Join([]string{
			paragraphDoc("", "<p>a</p>"),
			`<p><span class="between">x</span> <img class="size-thumbnail" src="q.png"> [gallery size="medium"]</p>`,
			`<!-- wp:freeform --><p><span class="free">a</span> <img class="size-medium" src="a.png"></p><!-- /wp:freeform -->`,
			`<!-- wp:shortcode -->[gallery size="large"] <span class="sc">x</span> <img class="size-large" src="b.png"><!-- /wp:shortcode -->`,
			// The classic editor's HTML in a block that holds blocks.
			`<!-- wp:group --><div class="wp-block-group"><!-- wp:freeform --><span class="in-group">x</span><!-- /wp:freeform --></div><!-- /wp:group -->`,
		}, "\n")})
		s, imp := proposeFrom(t, in, nil, nil)
		wantSizes := map[string]string{"thumbnail": "thumbnail", "medium": "medium", "large": "large"}
		wantMarks := map[string]string{"between": "", "free": "", "sc": "", "in-group": ""}
		if !reflect.DeepEqual(s.Blocks.ImageSizes, wantSizes) || !reflect.DeepEqual(s.Blocks.Marks, wantMarks) {
			t.Errorf("ImageSizes = %+v, Marks = %+v, want %+v, %+v", s.Blocks.ImageSizes, s.Blocks.Marks, wantSizes, wantMarks)
		}
		// Each is counted once, though the classic block is also a block whose HTML is read for spans.
		sameSet(t, "the entries", blockEntries(imp.Report), []string{
			sizesEntry(3, `"large" (2 times), "medium" (2 times), "thumbnail" (1 time)`),
			marksEntry(4, `"between" (1 time), "free" (1 time), "in-group" (1 time), "sc" (1 time)`),
		})
	})

	t.Run("the HTML of the classic editor: a delimiter that is not one, and a closer that closes no block", func(t *testing.T) {
		// <!-- wp:Foo --> is no block, so all of the text is HTML of the classic editor.
		sizes, marks, _ := classicNames(t, bodyXML{id: 1, content: `<!-- wp:Foo --><span class="foo">x</span><img class="size-large" src="a.png">`})
		if !slices.Equal(sizes, []string{"large"}) || !slices.Equal(marks, []string{"foo"}) {
			t.Errorf("sizes = %q, marks = %q, want [large] and [foo]", sizes, marks)
		}
		// What follows a closer that closes no block is not read by the conversion as HTML: it is a legacy block of it.
		sizes, marks, _ = classicNames(t, bodyXML{id: 1, content: paragraphDoc("", "<p>a</p>") +
			`<p><span class="stray">x</span><img class="size-large" src="a.png"></p><!-- /wp:acme/b --><p><span class="after">y</span></p>`})
		if len(sizes) != 0 || len(marks) != 0 {
			t.Errorf("sizes = %q, marks = %q, want none", sizes, marks)
		}
	})

	t.Run("the HTML of the classic editor: Elementor", func(t *testing.T) {
		// The conversion makes one legacy block of the content of an item made with Elementor, whatever it holds.
		page := `<span class="builder">x</span><img class="size-large" src="a.png">[gallery size="medium"]`
		mode := func(v string) [][2]string { return [][2]string{{"_elementor_edit_mode", v}} }
		s, imp := proposeFrom(t, bodiesWXR(
			bodyXML{id: 1, content: page, meta: mode("builder")},
			bodyXML{id: 2, content: paragraphDoc(`{"textColor":"builder-color"}`, "<p>x</p>"), meta: mode("1")},
			bodyXML{id: 3, typ: "page", content: `<span class="mode-0">x</span>`, meta: mode("0")},
			bodyXML{id: 4, content: `<span class="mode-empty">x</span>`, meta: mode("")},
			bodyXML{id: 5, content: `<span class="other-meta">x</span>`, meta: [][2]string{{"_elementor_data", "[]"}}},
		), nil, nil)
		if want := (map[string]string{"mode-0": "", "mode-empty": "", "other-meta": ""}); !reflect.DeepEqual(s.Blocks.Marks, want) {
			t.Errorf("Marks = %+v, want %+v", s.Blocks.Marks, want)
		}
		if len(s.Blocks.ImageSizes) != 0 || len(s.Blocks.Presets.Color) != 0 {
			t.Errorf("ImageSizes = %+v, Color = %+v, want none: they are in the items made with Elementor", s.Blocks.ImageSizes, s.Blocks.Presets.Color)
		}
		sameSet(t, "the entries", blockEntries(imp.Report), []string{marksEntry(3, `"mode-0" (1 time), "mode-empty" (1 time), "other-meta" (1 time)`)})
	})

	t.Run("the HTML of the classic editor: the items that are read, and a synced pattern", func(t *testing.T) {
		// A synced pattern with no block in it is HTML of the classic editor as well; the draft of a post and a page are read.
		sizes, marks, _ := classicNames(t,
			bodyXML{id: 1, typ: "wp_block", content: `<span class="in-pattern">x</span><img class="size-pattern" src="a.png">`},
			bodyXML{id: 2, typ: "page", status: "draft", content: `<span class="in-page">x</span>`},
			bodyXML{id: 3, status: "trash", content: `<span class="in-trash">x</span>`},
			bodyXML{id: 4, typ: "attachment", content: `<span class="in-attachment">x</span>`},
			bodyXML{id: 5, content: `<span class="in-post">x</span>`},
		)
		if !slices.Equal(sizes, []string{"pattern"}) || !slices.Equal(marks, []string{"in-page", "in-pattern", "in-post"}) {
			t.Errorf("sizes = %q, marks = %q", sizes, marks)
		}
	})

	t.Run("the HTML of the classic editor: the limits", func(t *testing.T) {
		item := func(id int, class string, pad int) bodyXML {
			return bodyXML{id: id, content: `<p><span class="` + class + `">` + strings.Repeat("a", pad) + `</span> [gallery size="` + class + `"]</p>`}
		}
		t.Run("the HTML of one block", func(t *testing.T) {
			lowerLimit(t, &maxBlockHTMLBytes, 100)
			sizes, marks, _ := classicNames(t, item(1, "small", 10), item(2, "large", 200))
			// Neither the span nor the gallery of HTML that is longer than the limit is read.
			if !slices.Equal(sizes, []string{"small"}) || !slices.Equal(marks, []string{"small"}) {
				t.Errorf("sizes = %q, marks = %q, want [small] and [small]", sizes, marks)
			}
		})
		t.Run("the HTML of one content", func(t *testing.T) {
			// Each block of the classic editor is 41 bytes, and three do not fit in 90: the third that is read is not.
			block := func(class string) string {
				return `<!-- wp:freeform --><p><span class="` + class + `">xxxxxxxxxx</span></p><!-- /wp:freeform -->`
			}
			lowerLimit(t, &maxHTMLBytes, 90)
			_, marks, _ := classicNames(t, bodyXML{id: 1, content: block("one") + block("two") + block("three")})
			if len(marks) != 2 {
				t.Errorf("marks = %q, want two of the three, which fit in the HTML that is read", marks)
			}
			// The limit is for one content: the next item is read again.
			_, marks, _ = classicNames(t, bodyXML{id: 1, content: block("one") + block("two")}, bodyXML{id: 2, content: block("three") + block("four")})
			if len(marks) != 4 {
				t.Errorf("marks = %q, want the four of the two contents", marks)
			}
		})
		t.Run("the size of the content", func(t *testing.T) {
			lowerLimit(t, &maxBodyBytes, 100)
			_, marks, _ := classicNames(t, item(1, "fits", 10), item(2, "big", 200))
			if !slices.Equal(marks, []string{"fits"}) {
				t.Errorf("marks = %q, want [fits]", marks)
			}
		})
		t.Run("the attributes of a gallery that are longer than a string may be", func(t *testing.T) {
			attrs := strings.Repeat("a=1 ", content.MaxTextBytes/4+1)
			sizes, _, _ := classicNames(t, bodyXML{id: 1, content: `[gallery size="long" ` + attrs + `] [gallery size="short"]`})
			if !slices.Equal(sizes, []string{"short"}) {
				t.Errorf("sizes = %q, want [short]", sizes)
			}
		})
		t.Run("the galleries", func(t *testing.T) {
			lowerLimit(t, &maxWPBlocks, 3)
			_, _, imp := classicNames(t, bodyXML{id: 1, content: strings.Repeat(`[gallery size="g"] `, 5)})
			sameSet(t, "the entries", blockEntries(imp.Report), []string{sizesEntry(1, `"g" (3 times)`)})
		})
	})

	t.Run("nothing is used", func(t *testing.T) {
		s, imp := proposeFrom(t, bodiesWXR(bodyXML{id: 1, content: paragraphDoc("", "<p>a</p>")}), nil, nil)
		if !reflect.DeepEqual(s.Blocks, emptyBlockSettings()) {
			t.Errorf("Blocks = %+v, want empty tables", s.Blocks)
		}
		if got := blockEntries(imp.Report); len(got) != 0 {
			t.Errorf("the entries are %q, want none", got)
		}
	})
}

func TestProposeBlocksIsTheSameAgain(t *testing.T) {
	in := bodiesWXR(
		bodyXML{id: 1, content: paragraphDoc(`{"textColor":"a","backgroundColor":"b","className":"is-style-s"}`, `<p><span class="m n">x</span></p>`)},
		bodyXML{id: 2, content: paragraphDoc(`{"textColor":"c","fontSize":"d"}`, `<p><mark class="has-e-color">x</mark></p>`)},
	)
	var first []byte
	var firstReport []string
	for range 3 {
		s, imp := proposeFrom(t, in, nil, nil)
		var b bytes.Buffer
		if err := s.WriteJSON(&b); err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first, firstReport = b.Bytes(), reportLines(t, imp.Report)
			continue
		}
		if !bytes.Equal(first, b.Bytes()) {
			t.Errorf("the settings differ:\n%s\n%s", first, b.Bytes())
		}
		sameStrings(t, "the report", reportLines(t, imp.Report), firstReport)
	}
	// The settings that were written can be read again, and the site makes of them.
	s, err := ReadSettings("settings.json", first)
	if err != nil {
		t.Fatalf("ReadSettings = %v", err)
	}
	if want := (map[string]string{"m": "", "n": ""}); !reflect.DeepEqual(s.Blocks.Marks, want) {
		t.Errorf("Marks = %+v, want %+v", s.Blocks.Marks, want)
	}
}
