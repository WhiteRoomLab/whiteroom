package wpimport

import (
	"fmt"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// runShortcodeCases is runMediaCases for the import of shortcodeExport.
func runShortcodeCases(t *testing.T, cases []textCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			imp := shortcodeImport(t)
			res := convertMedia(t, imp, bodySite(tt.allowHTML), tt.settings, tt.text)
			got, err := res.Blocks.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if want := withIDs(tt.want); string(got) != want {
				t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
			}
			wantLines(t, imp.Report, bodyLines(tt.lines...))
		})
	}
}

func TestShortcodeBlockEditor(t *testing.T) {
	const (
		running  = "the text has the shortcode [foo], which whiteroom does not run"
		gallery1 = "changed: the gallery is 1 image here; whiteroom has no gallery blocks yet (stage 2) -> /body/0"
	)
	record := func(url string) string {
		return fmt.Sprintf("review: the URL %q on its own line is text here; WordPress may have shown it as an embed, and whiteroom embeds only YouTube, Vimeo and X -> /body/0", url)
	}
	form := mediaIDs.contact
	runShortcodeCases(t, []textCase{
		{name: "a paragraph of a gallery", text: wpPara(`[gallery ids="42"]`), want: jsonList(galleryImage(0, 42, "", "thumbnail", false)), lines: []string{gallery1}},
		{name: "a paragraph of a form", text: wpPara(`[contact-form-7 id="1a2b3c4" title="Contact"]`), want: jsonList(jsonFormBlock(0, form))},
		{name: "a paragraph of a form, with white space and no-break spaces around it", text: wpPara("\n&nbsp; [contact-form-7 id=\"31\"] &nbsp;\n"), want: jsonList(jsonFormBlock(0, form))},
		{name: "a paragraph of a form that is not imported", text: wpPara(`[contact-form-7 id="32"]`),
			want:  jsonList(jsonLegacy(0, wpPara(`[contact-form-7 id="32"]`), "the form 32 is not imported")),
			lines: []string{legacyLine("the form 32 is not imported", "/body/0")}},
		{name: "a paragraph of an old form", text: wpPara(`[contact-form 5]`), want: jsonList(jsonFormBlock(0, scIDs[34]))},
		{name: "a paragraph of a caption", text: wpPara(`[caption width="300"]` + classicImg("wp-image-21") + ` Words[/caption]`),
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","caption":[`+jsonText("Words")+`]`))},
		{name: "a paragraph of a caption with text that looks like a shortcode: WordPress shows it as it is", text: wpPara(`[caption width="300"]` + classicImg("wp-image-21") + ` Words [br] and [[x]][/caption]`),
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","caption":[`+jsonText("Words [br] and [[x]]")+`]`))},
		{name: "a paragraph of an embed", text: wpPara(`[embed]` + ytURL + `[/embed]`), want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "a paragraph of a shortcode that is not converted is a legacy block of the block", text: wpPara(`[box]x[/box]`),
			want:  jsonList(jsonLegacy(0, wpPara(`[box]x[/box]`), "the shortcode [box] is not converted")),
			lines: []string{legacyLine("the shortcode [box] is not converted", "/body/0")}},
		{name: "a paragraph of a shortcode with elements in it", text: wpPara(`[foo]<strong>x</strong>[/foo]`),
			want:  jsonList(jsonLegacy(0, wpPara(`[foo]<strong>x</strong>[/foo]`), "the shortcode [foo] is not converted")),
			lines: []string{legacyLine("the shortcode [foo] is not converted", "/body/0")}},
		{name: "a shortcode in a sentence", text: wpPara(`Look [foo] here`),
			want:  jsonList(jsonLegacy(1, wpPara(`Look [foo] here`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		{name: "a shortcode in a link", text: wpPara(`<a href="/x">[foo]</a>`),
			want:  jsonList(jsonLegacy(1, wpPara(`<a href="/x">[foo]</a>`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		{name: "a shortcode after an element", text: wpPara(`<strong>x</strong>[foo]`),
			want:  jsonList(jsonLegacy(1, wpPara(`<strong>x</strong>[foo]`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		{name: "shortcodes on two lines", text: wpPara(`[foo]<br>[bar]`),
			want:  jsonList(jsonLegacy(1, wpPara(`[foo]<br>[bar]`), "the text has the shortcodes [foo], [bar], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcodes [foo], [bar], which whiteroom does not run", "/body/0")}},
		{name: "the shortcode of the paragraph and a shortcode in the HTML of the block before it", text: wpPara(`[foo]`) + wpPara(`[gallery ids="42"]`),
			want: jsonList(jsonLegacy(0, wpPara(`[foo]`), "the shortcode [foo] is not converted"), galleryImage(1, 42, "", "thumbnail", false)),
			lines: []string{
				legacyLine("the shortcode [foo] is not converted", "/body/0"), "changed: the gallery is 1 image here; whiteroom has no gallery blocks yet (stage 2) -> /body/1",
			}},
		{name: "a shortcode that is escaped is text", text: wpPara(`[[foo]]`), want: jsonList(jsonPara(0, "[foo]"))},
		{name: "text that is no shortcode", text: wpPara(`[PR] [1] [注1] [a.b] and [Foo]`), want: jsonList(jsonPara(0, "[PR] [1] [注1] [a.b] and [Foo]"))},
		{name: "an escaped shortcode and text", text: wpPara(`Use [[foo]] and [[bar x=1]].`), want: jsonList(jsonPara(0, "Use [foo] and [bar x=1]."))},
		{name: "a registered shortcode in a heading", text: wpBlock("heading", "", `<h2 class="wp-block-heading">Head [foo]</h2>`),
			want:  jsonList(jsonLegacy(1, wpBlock("heading", "", `<h2 class="wp-block-heading">Head [foo]</h2>`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		{name: "an escaped shortcode in a heading", text: wpBlock("heading", "", `<h2 class="wp-block-heading">Head [[foo]]</h2>`),
			want: jsonList(jsonHeading(0, "", `"level":2`, "Head [foo]"))},
		{name: "a shortcode in the item of a list", text: wpBlock("list", "", `<ul><li>a [foo]</li></ul>`),
			want:  jsonList(jsonLegacy(2, wpBlock("list", "", `<ul><li>a [foo]</li></ul>`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		{name: "a shortcode in a verse", text: wpBlock("verse", "", `<pre class="wp-block-verse">a [foo]</pre>`),
			want:  jsonList(jsonLegacy(1, wpBlock("verse", "", `<pre class="wp-block-verse">a [foo]</pre>`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		{name: "a shortcode in code is code", text: wpBlock("code", "", `<pre class="wp-block-code"><code>[foo]</code></pre>`), want: jsonList(jsonBlock("code", 0, "", "", `"[foo]"`))},
		// In a code element, the brackets are the code's own unless the name is one that WordPress registers.
		{name: "a name that is not registered in a code element is text", text: wpPara(`Use <code>a[i]</code> and <code>[[key]]</code>.`),
			want: jsonList(jsonRich(0, "", jsonText("Use "), jsonText("a[i]", `{"type":"code"}`), jsonText(" and "), jsonText("[[key]]", `{"type":"code"}`), jsonText(".")))},
		{name: "a registered name in a code element is a shortcode", text: wpPara(`Use <code>[gallery]</code>`),
			want:  jsonList(jsonLegacy(1, wpPara(`Use <code>[gallery]</code>`), "the text has the shortcode [gallery], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [gallery], which whiteroom does not run", "/body/0")}},
		{name: "an escaped registered name in a code element is the text of the shortcode", text: wpPara(`<code>[[gallery]]</code>`),
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("[gallery]", `{"type":"code"}`))))},
		{name: "a name that is not registered next to a code element is a shortcode", text: wpPara(`<code>a[i]</code> [foo]`),
			want:  jsonList(jsonLegacy(1, wpPara(`<code>a[i]</code> [foo]`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		{name: "a name that is not registered in an element in a code element is text", text: wpPara(`<code>a<strong>[i]</strong></code>`),
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("a", `{"type":"code"}`), jsonText("[i]", `{"type":"code"}`, jsonBold))))},
		{name: "a name that is not registered after a code element in the same element is a shortcode", text: wpPara(`<strong><code>a[i]</code>b[foo]</strong>`),
			want:  jsonList(jsonLegacy(1, wpPara(`<strong><code>a[i]</code>b[foo]</strong>`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		// The character "<" that a character reference wrote makes no element, so it does not hide the brackets after it.
		{name: "a less than sign hides no shortcode", text: wpPara(`a &lt; b [foo] c`),
			want:  jsonList(jsonLegacy(1, wpPara(`a &lt; b [foo] c`), running)),
			lines: []string{legacyLine(running, "/body/0")}},
		{name: "a less than sign hides no escaped shortcode", text: wpPara(`1 &lt; 2 [[foo]] 3 &lt; 4`), want: jsonList(jsonPara(0, "1 < 2 [foo] 3 < 4"))},
		{name: "a less than sign after the name is not the end of it", text: wpPara(`[foo&lt;x]`), want: jsonList(jsonPara(0, "[foo<x]"))},
		{name: "a registered name followed by a less than sign", text: wpPara(`a [gallery&lt;x] b`),
			want:  jsonList(jsonLegacy(1, wpPara(`a [gallery&lt;x] b`), "the text has the shortcode [gallery], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [gallery], which whiteroom does not run", "/body/0")}},
		{name: "a shortcode in a group", text: wpGroup("", "div", wpPara(`[gallery ids="42"]`), wpPara(`x [foo]`)),
			want: jsonList(jsonHolder("group", 0, "", "", galleryImage(1, 42, "", "thumbnail", false),
				jsonLegacy(3, wpPara(`x [foo]`), running))),
			lines: []string{"changed: the gallery is 1 image here; whiteroom has no gallery blocks yet (stage 2) -> /body/0/content/0", legacyLine(running, "/body/0/content/1")}},
		{name: "a shortcode in the caption of a gallery is not imported", text: wpBlock("gallery", "",
			`<figure class="wp-block-gallery has-nested-images">`+wpImage(`{"id":21,"sizeSlug":"large","linkDestination":"none"}`, imgOf(photoURL, "a", 21))+
				`<figcaption class="blocks-gallery-caption">The [foo]</figcaption></figure>`),
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"a","size":"large"`)),
			lines: []string{
				"changed: the gallery is 1 image here; whiteroom has no gallery blocks yet (stage 2) -> /body/0",
				"skipped: the caption of the gallery is not imported: " + running + " -> /body",
			}},
		{name: "a URL that a provider embeds", text: wpPara(ytURL), want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "a URL with space around it", text: wpPara("\n " + vimeoURL + " \n"), want: jsonList(jsonEmbed(0, "vimeo", vimeoURL))},
		{name: "a URL of X, in a paragraph with a class and an alignment, which the embed does not have", text: wpBlock("paragraph", `{"align":"center","className":"c"}`, `<p class="has-text-align-center c">`+xURL+`</p>`),
			want: jsonList(jsonEmbed(0, "x", xURL)), lines: []string{lostLine("align, className", "core/paragraph", "/body/0")}},
		{name: "a shortcode in a paragraph with an anchor and a font size", text: wpBlock("paragraph", `{"anchor":"a","fontSize":"large","dropCap":false}`, `<p id="a" class="has-large-font-size">[contact-form-7 id="31"]</p>`),
			want: jsonList(jsonFormBlock(0, form)), lines: []string{lostLine("anchor, fontSize", "core/paragraph", "/body/0")}},
		{name: "an anchor that is only in the HTML", text: wpBlock("paragraph", ``, `<p id="b">[contact-form-7 id="31"]</p>`),
			want: jsonList(jsonFormBlock(0, form)), lines: []string{lostLine("anchor", "core/paragraph", "/body/0")}},
		{name: "a URL that no provider embeds", text: wpPara("https://example.org/a"), want: jsonList(jsonPara(0, "https://example.org/a")),
			lines: []string{record("https://example.org/a")}},
		// WordPress embeds the http address of a provider too, so it is looked for as an https address, and is one.
		{name: "a URL with http is embedded as an https URL", text: wpPara("http://www.youtube.com/watch?v=dQw4w9WgXcQ"), want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "a URL with http in capitals", text: wpPara("HTTP://youtu.be/dQw4w9WgXcQ"), want: jsonList(jsonEmbed(0, "youtube", "https://youtu.be/dQw4w9WgXcQ"))},
		{name: "a URL with http of Vimeo and X", text: wpPara("http://vimeo.com/76979871") + wpPara("http://x.com/user/status/1234567890"),
			want: jsonList(jsonEmbed(0, "vimeo", vimeoURL), jsonEmbed(1, "x", xURL))},
		{name: "a URL with http that no provider embeds is text as it was written", text: wpPara("http://example.org/a"), want: jsonList(jsonPara(0, "http://example.org/a")),
			lines: []string{record("http://example.org/a")}},
		{name: "a URL with http and a port is text", text: wpPara("http://www.youtube.com:8080/watch?v=dQw4w9WgXcQ"),
			want:  jsonList(jsonPara(0, "http://www.youtube.com:8080/watch?v=dQw4w9WgXcQ")),
			lines: []string{record("http://www.youtube.com:8080/watch?v=dQw4w9WgXcQ")}},
		{name: "a URL that no provider embeds, with its class", text: wpBlock("paragraph", `{"className":"c"}`, `<p class="c">https://example.org/a</p>`),
			want: jsonList(jsonBlock("paragraph", 0, `"className":"c"`, "", jsonList(jsonText("https://example.org/a")))), lines: []string{record("https://example.org/a")}},
		{name: "a URL in a link is a link", text: wpPara(`<a href="` + ytURL + `">` + ytURL + `</a>`),
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText(ytURL, `{"type":"link","attrs":{"href":"`+ytURL+`"}}`))))},
		{name: "a URL and a word", text: wpPara(ytURL + " and more"), want: jsonList(jsonPara(0, ytURL+" and more"))},
		{name: "a URL in an element", text: wpPara("<em>" + ytURL + "</em>"), want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText(ytURL, `{"type":"italic"}`))))},
		{name: "a URL and a line break", text: wpPara(ytURL + "<br>"), want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText(ytURL), jsonBreak)))},
		{name: "a URL with a quote in it", text: wpPara(`https://example.org/a"b`), want: jsonList(jsonPara(0, `https://example.org/a"b`))},
		{name: "a scheme and nothing else", text: wpPara("https://"), want: jsonList(jsonPara(0, "https://"))},
	})
}

func TestShortcodeBlock(t *testing.T) {
	gallery := func(images int, pointer string) string {
		return fmt.Sprintf("changed: the gallery is %s here; whiteroom has no gallery blocks yet (stage 2) -> %s", count(images, "image"), pointer)
	}
	shortcodeBlock := func(text string) string { return "<!-- wp:shortcode -->" + text + "<!-- /wp:shortcode -->" }
	runShortcodeCases(t, []textCase{
		{name: "a gallery", text: shortcodeBlock("\n" + `[gallery ids="42,41"]` + "\n"),
			want:  jsonList(galleryImage(0, 42, "", "thumbnail", false), galleryImage(1, 41, jsonText("First ")+`,`+jsonText("one", `{"type":"italic"}`), "thumbnail", false)),
			lines: []string{gallery(2, "/body/0")}},
		{name: "a shortcode that is not converted is a legacy block of its own text", text: shortcodeBlock("\n[box]x[/box]\n"),
			want:  jsonList(jsonLegacy(0, "[box]x[/box]", "the shortcode [box] is not converted")),
			lines: []string{legacyLine("the shortcode [box] is not converted", "/body/0")}},
		{name: "paragraphs are made as WordPress makes them", text: shortcodeBlock("Intro\nlines\n\n[gallery ids=\"42\"]\n\n[video src=\"a.mp4\"]\n\nOutro <b>bold</b>"),
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("Intro"), jsonBreak, jsonText("lines"))), galleryImage(1, 42, "", "thumbnail", false),
				jsonLegacy(2, `[video src="a.mp4"]`, "whiteroom has no video blocks yet (stage 2)"),
				jsonBlock("paragraph", 3, "", "", jsonList(jsonText("Outro "), jsonText("bold", jsonBold)))),
			lines: []string{gallery(1, "/body/1"), legacyLine("whiteroom has no video blocks yet (stage 2)", "/body/2")}},
		{name: "a form, a caption and a URL", text: shortcodeBlock("\n[contact-form-7 id=\"31\"]\n\n[caption width=\"300\"]" + classicImg("wp-image-21") + " Words[/caption]\n\n" + ytURL + "\n"),
			want: jsonList(jsonFormBlock(0, mediaIDs.contact), jsonImage(1, mediaIDs.photo, "", `"alt":"Photo","caption":[`+jsonText("Words")+`]`), jsonEmbed(2, "youtube", ytURL))},
		{name: "nothing", text: shortcodeBlock("\n ") + wpPara("x"), want: jsonList(jsonPara(0, "x"))},
		{name: "text with a shortcode in it", text: shortcodeBlock("Look [foo] here"),
			want:  jsonList(jsonLegacy(1, "<p>Look [foo] here</p>", "the text has the shortcode [foo], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [foo], which whiteroom does not run", "/body/0")}},
		{name: "in a group", text: wpGroup("", "div", shortcodeBlock(`[gallery ids="42"]`), wpPara("after")),
			want:  jsonList(jsonHolder("group", 0, "", "", galleryImage(1, 42, "", "thumbnail", false), jsonPara(2, "after"))),
			lines: []string{gallery(1, "/body/0/content/0")}},
		{name: "a read more tag in it is the tag of the content", text: wpPara("a") + shortcodeBlock("One\n\n<!--more-->\n\nTwo") + wpPara("b"),
			want: jsonList(jsonPara(0, "a"), jsonPara(1, "One"), jsonPara(2, "Two"), jsonPara(3, "b"))},
		{name: "a page break in it is a page break", text: shortcodeBlock("One\n\n<!--nextpage-->\n\nTwo"),
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), lines: []string{"skipped: 1 page break is removed; whiteroom has no page breaks -> /body"}},
	})
}

func TestShortcodeBlockMore(t *testing.T) {
	imp := shortcodeImport(t)
	res := convertMedia(t, imp, bodySite(false), nil, wpPara("a")+"<!-- wp:shortcode -->One\n\n<!--more-->\n\nTwo<!-- /wp:shortcode -->"+wpPara("b"))
	if res.More != 2 {
		t.Errorf("More = %d, want 2: the paragraph a and One come before the tag", res.More)
	}
}

func TestShortcodeBlockInASyncedPattern(t *testing.T) {
	// A pattern is not the content of the post: the tag and the page break in it are comments, but the shortcodes in it run.
	e := patternsExport(1, func(int) string {
		return "<!-- wp:shortcode -->[gallery ids=\"42\"]\nOne<!--more-->Two<!--nextpage-->Three<!-- /wp:shortcode -->"
	})
	e.Items = append(e.Items, shortcodeExport().Items...)
	imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
	for _, post := range []int64{42} {
		id, err := imp.IDs.Assign(PostObject(e.Post(post)), content.RefMedia)
		if err != nil {
			t.Fatal(err)
		}
		imp.AddRef(content.RefMedia, id, content.Ref{Type: content.MediaImage})
	}
	_, res := limitedBody(t, imp, wpRef(1)+wpPara("b"))
	var kinds []string
	for _, b := range res.Blocks {
		kinds = append(kinds, string(b.Type))
	}
	// WordPress makes one paragraph of the lines, and the tag and the page break are comments in it.
	if fmt.Sprint(kinds) != "[image paragraph paragraph]" || res.More != -1 {
		t.Errorf("the blocks are %v with More %d, want an image, two paragraphs, and no tag", kinds, res.More)
	}
	wantLines(t, imp.Report, bodyLines(
		`changed: the synced pattern 1 ("Pattern 1") is copied here; whiteroom has no shared blocks yet (stage 2) -> /body/0`,
		"changed: the gallery is 1 image here; whiteroom has no gallery blocks yet (stage 2) -> /body/0"))
}

func TestElementor(t *testing.T) {
	const page = `<div class="elementor"><h2>Title</h2><p>Words</p></div>`
	convert := func(t *testing.T, content string, meta ...wxr.Meta) (*BodyResult, *Import) {
		t.Helper()
		imp := bodyImport()
		item := bodyItem(content)
		item.Meta = meta
		site := bodySite(false)
		res, err := imp.Body(BodyOptions{Site: site, Item: item, Target: bodyTarget, Key: bodyKey})
		if err != nil {
			t.Fatal(err)
		}
		checkBody(t, imp, site, res)
		return res, imp
	}
	mode := func(v string) wxr.Meta { return wxr.Meta{Key: "_elementor_edit_mode", Value: v} }
	for _, v := range []string{"builder", "1", " ", "0.0", "false"} {
		t.Run("the mode "+fmt.Sprintf("%q", v), func(t *testing.T) {
			res, imp := convert(t, page, mode(v))
			sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], page, reasonElementor)})
			if res.More != -1 {
				t.Errorf("More = %d, want -1", res.More)
			}
			wantLines(t, imp.Report, bodyLines(legacyLine(reasonElementor, "/body/0")))
		})
	}
	for _, tt := range []struct {
		name string
		meta []wxr.Meta
	}{
		{"no mode", nil},
		{"a mode that is empty", []wxr.Meta{mode("")}},
		{"a mode that is 0", []wxr.Meta{mode("0")}},
		{"other metadata", []wxr.Meta{{Key: "_elementor_data", Value: "[]"}, {Key: "_elementor_edit_modes", Value: "builder"}}},
	} {
		t.Run(tt.name+" is the content of the classic editor", func(t *testing.T) {
			res, imp := convert(t, page, tt.meta...)
			if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockGroup || len(res.Blocks[0].Children) != 2 {
				t.Errorf("the blocks are %s, want the content converted", bodyDocument(t, res))
			}
			wantLines(t, imp.Report, nil)
		})
	}
	t.Run("the first of the modes counts", func(t *testing.T) {
		res, _ := convert(t, page, mode("0"), mode("builder"))
		if len(res.Blocks) == 0 || res.Blocks[0].Type == content.BlockLegacy {
			t.Errorf("the blocks are %s, want the content converted", bodyDocument(t, res))
		}
	})
	t.Run("blocks of the block editor are not converted either", func(t *testing.T) {
		text := wpPara("a")
		res, imp := convert(t, text, mode("builder"))
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], text, reasonElementor)})
		wantLines(t, imp.Report, bodyLines(legacyLine(reasonElementor, "/body/0")))
	})
	t.Run("no content, no block", func(t *testing.T) {
		res, imp := convert(t, " \n", mode("builder"))
		if len(res.Blocks) != 0 {
			t.Errorf("the blocks are %s, want none", bodyDocument(t, res))
		}
		wantLines(t, imp.Report, nil)
	})
	t.Run("content that is too long for a legacy block is not imported", func(t *testing.T) {
		long := "<div>" + strings.Repeat("x", content.MaxTextBytes) + "</div>"
		res, imp := convert(t, long, mode("builder"))
		if len(res.Blocks) != 0 {
			t.Errorf("the blocks are %s, want none", bodyDocument(t, res))
		}
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf(
			"skipped: the content of the page made with Elementor is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(long), content.MaxTextBytes)))
	})
	t.Run("control characters in the content", func(t *testing.T) {
		res, _ := convert(t, "<p>a"+string(rune(0x11))+"b</p>", mode("builder"))
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], "<p>ab</p>", reasonElementor)})
	})
}

// refuseBlocks makes the validation of the guard find a problem in each block of the type typ, until there are none left.
func refuseBlocks(t *testing.T, typ content.BlockType, attr string) {
	t.Helper()
	old := validateBlocks
	validateBlocks = func(blocks content.Blocks, opts content.ValidateOptions) error {
		if err := old(blocks, opts); err != nil {
			return err
		}
		var errs content.Errors
		var walk func(bs content.Blocks, path string)
		walk = func(bs content.Blocks, path string) {
			for i := range bs {
				at := fmt.Sprintf("%s/%d", path, i)
				if bs[i].Type == typ {
					errs = append(errs, &content.Error{Path: at + "/attrs/" + attr, Msg: "refused"})
				}
				walk(bs[i].Children, at+"/content")
			}
		}
		walk(blocks, "")
		if len(errs) > 0 {
			return errs
		}
		return nil
	}
	t.Cleanup(func() { validateBlocks = old })
}

func TestShortcodeGuard(t *testing.T) {
	convert := func(t *testing.T, text string) *BodyResult {
		t.Helper()
		imp := shortcodeImport(t)
		res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(text), Target: bodyTarget, Key: bodyKey})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	reason := "the conversion does not pass validation: /attrs/media: refused"
	t.Run("the images of a gallery go together, apart from the paragraphs around it", func(t *testing.T) {
		refuseBlocks(t, content.BlockImage, "media")
		res := convert(t, "Before\n[gallery ids=\"41,42\"]\nAfter\n\nx")
		got, _ := res.Blocks.MarshalJSON()
		// The paragraphs are 0, 3, and 4; the images 1 and 2; the legacy block that takes the place of the images is 5.
		want := withIDs(jsonList(jsonPara(0, "Before"), jsonLegacy(5, `[gallery ids="41,42"]`, reason), jsonPara(3, "After"), jsonPara(4, "x")))
		if string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("a caption goes with its text", func(t *testing.T) {
		refuseBlocks(t, content.BlockImage, "media")
		text := `[caption width="300"]` + classicImg("wp-image-21") + ` Words[/caption]`
		res := convert(t, text)
		got, _ := res.Blocks.MarshalJSON()
		if want := withIDs(jsonList(jsonLegacy(1, text, reason))); string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("an embed of a URL goes with the URL", func(t *testing.T) {
		refuseBlocks(t, content.BlockEmbed, "url")
		res := convert(t, ytURL+"\n\n[embed]"+vimeoURL+"[/embed]")
		got, _ := res.Blocks.MarshalJSON()
		reason := "the conversion does not pass validation: /attrs/url: refused"
		if want := withIDs(jsonList(jsonLegacy(2, ytURL, reason), jsonLegacy(3, "[embed]"+vimeoURL+"[/embed]", reason))); string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("a paragraph that does not pass has the URLs in it as they were written", func(t *testing.T) {
		refuseBlocks(t, content.BlockParagraph, "text")
		res := convert(t, "Line one\nhttps://example.org/a\nLine two\n"+ytURL+"&amp;t=5")
		got, _ := res.Blocks.MarshalJSON()
		reason := "the conversion does not pass validation: /attrs/text: refused"
		// The URL that a provider embeds is a block of its own, and the one that none embeds is the text of the paragraph.
		want := withIDs(jsonList(jsonLegacy(2, "<p>Line one<br>\nhttps://example.org/a<br>\nLine two<br>\n</p>", reason), jsonEmbed(1, "youtube", ytURL+"&t=5")))
		if string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("a form goes with its shortcode", func(t *testing.T) {
		refuseBlocks(t, content.BlockForm, "form")
		res := convert(t, `[contact-form-7 id="31"]`)
		got, _ := res.Blocks.MarshalJSON()
		if want := withIDs(jsonList(jsonLegacy(1, `[contact-form-7 id="31"]`, "the conversion does not pass validation: /attrs/form: refused"))); string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
	})
}

func TestShortcodeLimits(t *testing.T) {
	record := func(url, pointer string) string {
		return fmt.Sprintf("review: the URL %q on its own line is text here; WordPress may have shown it as an embed, and whiteroom embeds only YouTube, Vimeo and X -> %s", url, pointer)
	}
	cut := func(n int) string {
		return fmt.Sprintf("review: the shortcodes and URLs after the first %d are left as text -> /body", n)
	}
	t.Run("the shortcodes beyond the limit are text", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 3)
		imp := bodyImport()
		c, res := limitedBody(t, imp, "x [a] y [b] z [c] w [d] v [e]")
		reason := "the text has the shortcodes [a], [b], [c], which whiteroom does not run"
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[1], "<p>x [a] y [b] z [c] w [d] v [e]</p>", reason)})
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0"), cut(3)))
		if len(c.pieces) != 3 {
			t.Errorf("the conversion made %d pieces, want 3", len(c.pieces))
		}
	})
	t.Run("the URLs beyond the limit are text", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 5)
		imp := bodyImport()
		c, res := limitedBody(t, imp, "https://e.example/1\nhttps://e.example/2\nhttps://e.example/3\nhttps://e.example/4\nhttps://e.example/5\nhttps://e.example/6\nhttps://e.example/7")
		if len(res.Blocks) != 1 || len(res.Blocks[0].Text) != 13 || len(c.pieces) != 5 {
			t.Fatalf("the blocks are %s with %d pieces, want a paragraph of 7 lines and 5 pieces", bodyDocument(t, res), len(c.pieces))
		}
		var lines []string
		for i := range 5 {
			lines = append(lines, record(fmt.Sprintf("https://e.example/%d", i+1), "/body/0"))
		}
		wantLines(t, imp.Report, bodyLines(append(lines, cut(5))...))
	})
	t.Run("the [embed] shortcodes come first, and the URLs and the other shortcodes have what is left", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 4)
		imp := bodyImport()
		c, res := limitedBody(t, imp, "[embed]"+ytURL+"[/embed]\n[embed]"+vimeoURL+"[/embed]\n"+xURL+"\n\ntext [a] and [b]")
		if c.pieceLeft != 0 || len(c.pieces) != 4 || !c.piecesCut {
			t.Errorf("the conversion has %d pieces and %d left, cut = %v, want 4, 0, true", len(c.pieces), c.pieceLeft, c.piecesCut)
		}
		// The two [embed] shortcodes and the URL are three pieces, [a] is the fourth, and [b] stays text in the legacy block.
		reason := "the text has the shortcode [a], which whiteroom does not run"
		want := withIDs(jsonList(jsonEmbed(0, "youtube", ytURL), jsonEmbed(1, "vimeo", vimeoURL), jsonEmbed(2, "x", xURL), jsonLegacy(4, "<p>text [a] and [b]</p>", reason)))
		if got, _ := res.Blocks.MarshalJSON(); string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/3"), cut(4)))
	})
	t.Run("the pieces are the limit of a body, not of a source", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 4)
		imp := bodyImport()
		c, _ := limitedBody(t, imp, freeform("x [a] y [b] z")+freeform("x [c] y [d] z [e]"))
		if len(c.pieces) != 4 {
			t.Errorf("the conversion made %d pieces, want 4 for the two sources", len(c.pieces))
		}
		wantLines(t, imp.Report, bodyLines(
			legacyLine("the text has the shortcodes [a], [b], which whiteroom does not run", "/body/0"),
			legacyLine("the text has the shortcodes [c], [d], which whiteroom does not run", "/body/1"),
			cut(4)))
	})
	t.Run("as many shortcodes as the limit are pieces, and no more", func(t *testing.T) {
		n := maxWPBlocks
		imp := bodyImport()
		text := strings.Repeat("[a] ", n+1)
		c, res := limitedBody(t, imp, text)
		if len(c.pieces) != n {
			t.Errorf("the conversion made %d pieces, want %d", len(c.pieces), n)
		}
		want := "<p>" + text + "</p>"
		if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockLegacy || res.Blocks[0].Attrs.HTML != want {
			t.Errorf("the blocks are %d, the first is a %s block, want one legacy block of all of the text", len(res.Blocks), res.Blocks[0].Type)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine("the text has the shortcode [a], which whiteroom does not run", "/body/0"), cut(n)))
	})
	t.Run("the attributes of a shortcode that are longer than a string may be", func(t *testing.T) {
		imp := shortcodeImport(t)
		text := "[gallery ids=\"41\" " + strings.Repeat("a=1 ", content.MaxTextBytes/4+1) + "]"
		res := convertMedia(t, imp, bodySite(false), nil, text)
		if len(res.Blocks) != 0 {
			t.Errorf("the blocks are %.300s, want none", bodyDocument(t, res))
		}
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf(
			"skipped: the shortcode [gallery] is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(text), content.MaxTextBytes)))
	})
	t.Run("a gallery counts its images among the blocks converted", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 3)
		imp := shortcodeImport(t)
		c, res := limitedBody(t, imp, `[gallery ids="41,42,43,47,48"]`+"\n\nafter")
		if !c.stopped || countOfType(res.Blocks, content.BlockImage) != 3 || countOfType(res.Blocks, content.BlockParagraph) != 0 {
			t.Errorf("the conversion stopped = %v and made %s, want a stop, 3 images and no paragraph", c.stopped, bodyDocument(t, res))
		}
	})
}

func TestShortcodeInRichText(t *testing.T) {
	const running = "the text has the shortcode [foo], which whiteroom does not run"
	for _, tt := range []struct {
		name    string
		in      string
		verse   bool
		plain   bool
		classic bool
		want    string // the text, or "!" and the reason of the legacy block
	}{
		{name: "a shortcode", in: "a [foo] b", want: "!" + running},
		{name: "a shortcode in an element", in: "a <b>[foo]</b>", want: "!" + running},
		{name: "a shortcode of a closing tag", in: "a [foo]<b>x</b>[/foo]", want: "!" + running},
		{name: "a shortcode in a verse", in: "a\n[foo]", verse: true, want: "!" + running},
		{name: "a shortcode in a text that is plain", in: "Buy [foo]", plain: true, want: "!" + running},
		{name: "two shortcodes", in: "[foo] [bar] [foo]", want: "!the text has the shortcodes [foo], [bar], which whiteroom does not run"},
		{name: "an escape", in: "a [[foo]] b", want: "a [foo] b"},
		{name: "an escape in a text that is plain", in: "Buy [[foo]]", plain: true, want: "Buy [foo]"},
		{name: "an escape with a closing tag", in: "[[foo]]x[/foo]]", want: "[foo]]x[/foo]"},
		{name: "an escape and a shortcode", in: "[[foo]] [bar]", want: "!the text has the shortcode [bar], which whiteroom does not run"},
		{name: "what is no shortcode", in: "[PR] [1] [注1] [a.b] [Foo] [", want: "[PR] [1] [注1] [a.b] [Foo] ["},
		{name: "a shortcode between elements is in the text of one", in: "<b>x</b>[foo]<i>y</i>", want: "!" + running},
		{name: "a shortcode that the text of an element is cut by", in: "[fo<b>o]</b>", want: "[foo]"},
		{name: "the classic editor has its shortcodes found before: what is left is text", in: "a [foo] [[bar]] b", classic: true, want: "a [foo] [[bar]] b"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := richConv(bodyImport(), "", nil)
			if tt.classic {
				c.classicDepth++
			}
			root := richRoot(t, tt.in)
			var got content.RichText
			var err error
			if tt.plain {
				var s string
				if s, err = c.plainText(root, noteAt{}); err == nil {
					got = content.RichText{rtText(s)}
				}
			} else {
				got, err = c.richText(root, tt.verse, noteAt{})
			}
			if reason, bad := strings.CutPrefix(tt.want, "!"); bad {
				if err == nil || reasonOf(err) != reason {
					t.Errorf("the reading of %q gave %v and the error %v, want the reason %q", tt.in, got, err, reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("the reading of %q gave the error %v", tt.in, err)
			}
			var text strings.Builder
			for _, in := range got {
				text.WriteString(in.Text)
			}
			if text.String() != tt.want {
				t.Errorf("the text of %q is %q, want %q", tt.in, text.String(), tt.want)
			}
		})
	}
}
