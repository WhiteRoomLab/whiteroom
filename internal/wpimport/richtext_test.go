package wpimport

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// phpArray serializes an array as PHP does, from its keys and values, each already serialized.
func phpArray(pairs ...string) string {
	return fmt.Sprintf("a:%d:{%s}", len(pairs)/2, strings.Join(pairs, ""))
}

func phpInt(n int) string { return fmt.Sprintf("i:%d;", n) }

// photoMeta is the _wp_attachment_metadata of an image of 1200 x 800 pixels that WordPress scaled down: its smaller sizes
// are next to the file, and so is the original image.
var photoMeta = phpArray(
	phpString("width"), phpInt(1200), phpString("height"), phpInt(800), phpString("file"), phpString("2026/10/photo-scaled.jpg"),
	phpString("sizes"), phpArray(
		phpString("medium"), phpArray(phpString("file"), phpString("photo-300x200-scaled.jpg"), phpString("width"), phpInt(300)),
		phpString("thumbnail"), phpArray(phpString("file"), phpString("photo-150x150.jpg")),
	),
	phpString("original_image"), phpString("photo.jpg"),
)

// linkExport is the WXR of a site with the pages that the links of the tests go to.
func linkExport() *wxr.Export {
	return &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items: []wxr.Item{
			{ID: 11, Type: "post", Status: "publish", Title: "Hello", Link: "https://example.com/2026/10/hello/"},
			{ID: 12, Type: "page", Status: "publish", Title: "About", Link: "https://example.com/about/"},
			{
				ID: 13, Type: "attachment", Status: "inherit", Title: "Photo", Link: "https://example.com/photo/",
				AttachmentURL: "https://example.com/wp-content/uploads/2026/10/photo-scaled.jpg",
				Meta: wxr.MetaList{
					{Key: "_wp_attached_file", Value: "2026/10/photo-scaled.jpg"}, {Key: "_wp_attachment_metadata", Value: photoMeta},
				},
			},
			{
				ID: 14, Type: "attachment", Status: "inherit", Title: "Report", Link: "https://example.com/?attachment_id=14",
				AttachmentURL: "https://example.com/wp-content/uploads/2026/10/%E5%A0%B1%E5%91%8A.pdf",
				Meta:          wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/10/報告.pdf"}},
			},
			{ID: 15, Type: "post", Status: "draft", Title: "Draft", Link: "https://example.com/?p=15"},
			{ID: 16, Type: "post", Status: "publish", Title: "Not imported", Link: "https://example.com/2026/10/gone/"},
		},
	}
}

// linkImport is an import of linkExport in which the posts 11 and 12 are entries, 13 is an image and 14 a PDF; the
// others are not imported.
func linkImport(t testing.TB) *Import {
	t.Helper()
	e := linkExport()
	imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
	for _, a := range []struct {
		id   int64
		kind content.RefKind
		ref  content.Ref
	}{
		{11, content.RefEntry, content.Ref{Type: content.TypePost, Language: "ja"}},
		{12, content.RefEntry, content.Ref{Type: content.TypePage, Language: "ja"}},
		{13, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{14, content.RefMedia, content.Ref{Type: content.MediaPDF}},
	} {
		id, err := imp.IDs.Assign(PostObject(e.Post(a.id)), a.kind)
		if err != nil {
			t.Fatal(err)
		}
		imp.AddRef(a.kind, id, a.ref)
	}
	return imp
}

func linkTarget(t testing.TB, imp *Import, post int64) string {
	t.Helper()
	_, id, ok := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: post})
	if !ok {
		t.Fatalf("post %d has no ID", post)
	}
	return id
}

// richSettings are the settings that the tests of the text use: the color vivid-red is red in the theme, the color gone is
// not imported, and the classes marker and plain are a variant and nothing.
func richSettings() *Settings {
	s := &Settings{Blocks: emptyBlockSettings()}
	s.Blocks.Presets.Color["vivid-red"] = "red"
	s.Blocks.Presets.Color["gone"] = ""
	s.Blocks.Marks["marker"] = "marker-yellow"
	s.Blocks.Marks["plain"] = ""
	return s
}

// richConv makes the conversion of the body of an item whose link is itemLink.
func richConv(imp *Import, itemLink string, settings *Settings) *bodyConv {
	item := bodyItem("")
	item.Link = itemLink
	return newBodyConv(imp, BodyOptions{Site: bodySite(false), Settings: settings, Item: item, Target: bodyTarget, Key: bodyKey})
}

// richRoot reads markup as the content of a div, and returns the div.
func richRoot(t testing.TB, markup string) *html.Node {
	t.Helper()
	nodes, err := parseIn("div", markup)
	if err != nil {
		t.Fatalf("parseIn(%q) = %v", markup, err)
	}
	root := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "div"}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	return root
}

// checkRichText checks what holds for every text that richText makes: it passes Validate after Normalize.
func checkRichText(t testing.TB, markup string, rt content.RichText) {
	t.Helper()
	norm := slices.Clone(rt)
	norm.Normalize()
	if err := norm.Validate(); err != nil {
		t.Errorf("the text of %q does not pass Validate: %v", markup, err)
	}
}

// Constructors of the expected texts.
var (
	rtBold      = content.Mark{Type: content.MarkBold}
	rtItalic    = content.Mark{Type: content.MarkItalic}
	rtUnderline = content.Mark{Type: content.MarkUnderline}
	rtStrike    = content.Mark{Type: content.MarkStrike}
	rtCode      = content.Mark{Type: content.MarkCode}
	rtSub       = content.Mark{Type: content.MarkSubscript}
	rtSup       = content.Mark{Type: content.MarkSuperscript}
)

func rtText(text string, marks ...content.Mark) content.Inline {
	return content.Inline{Type: content.InlineText, Text: text, Marks: marks}
}

func rtBreak() content.Inline { return content.Inline{Type: content.InlineHardBreak} }

func rtLink(l content.Link) content.Mark { return content.Mark{Type: content.MarkLink, Link: &l} }

func rtHighlight(h content.Highlight) content.Mark {
	return content.Mark{Type: content.MarkHighlight, Highlight: &h}
}

// bodyNoteLine is the short line for a note at the body.
func bodyNoteLine(kind Kind, msg string) string { return string(kind) + ": " + msg + " -> /body" }

// colorsLost is the note for the colors of the elements (such as "<mark>, <span>") that a variant wins over.
func colorsLost(tags string) string {
	return "the colors of " + tags + " are not imported: a highlight with a variant has no colors"
}

func TestRichText(t *testing.T) {
	nbsp := string(rune(0xA0))
	nel, ls := string(rune(0x85)), string(rune(0x2028))
	const itemLink = "https://example.com/2026/10/hello/"
	kept := func(tags string) string { return bodyNoteLine(Changed, "kept only the text of "+tags) }
	for _, tt := range []struct {
		name  string
		in    string
		verse bool
		want  content.RichText
		notes []string
		err   string
	}{
		{name: "nothing", in: "", want: nil},
		{name: "text", in: "Hello", want: content.RichText{rtText("Hello")}},
		{name: "the marks", in: "<b>1</b><strong>2</strong><i>3</i><em>4</em><u>5</u><s>6</s><strike>7</strike><del>8</del><code>9</code><sub>a</sub><sup>b</sup>",
			want: content.RichText{
				rtText("12", rtBold), rtText("34", rtItalic), rtText("5", rtUnderline), rtText("678", rtStrike), rtText("9", rtCode),
				rtText("a", rtSub), rtText("b", rtSup),
			}},
		{name: "nested marks are listed from the outside", in: "<strong>a<em>b</em>c</strong><em><strong>d</strong></em>",
			want: content.RichText{rtText("a", rtBold), rtText("b", rtBold, rtItalic), rtText("c", rtBold), rtText("d", rtItalic, rtBold)}},
		{name: "the same mark inside itself is one", in: "<b>a<strong>b<b>c</b></strong></b>d",
			want: content.RichText{rtText("abc", rtBold), rtText("d")}},
		{name: "text with the same marks is joined", in: "<b>a</b><strong>b</strong>c<em>d</em><em>e</em>",
			want: content.RichText{rtText("ab", rtBold), rtText("c"), rtText("de", rtItalic)}},
		{name: "a mark between text of the same marks", in: "<b>a</b><i></i><b>b</b>", want: content.RichText{rtText("ab", rtBold)}},
		{name: "a subscript in a superscript is left out", in: "<sup>a<sub>b</sub></sup>c<sub>d<sup>e</sup></sub>",
			want:  content.RichText{rtText("ab", rtSup), rtText("c"), rtText("de", rtSub)},
			notes: []string{bodyNoteLine(Changed, "removed <sub> inside <sup>, <sup> inside <sub>")}},
		{name: "a superscript in a superscript is one", in: "<sup>a<sup>b</sup></sup>", want: content.RichText{rtText("ab", rtSup)}},
		{name: "underline by a span", in: `<span style="text-decoration: underline;">a</span><span style="Text-Decoration:UNDERLINE">b</span>`,
			want: content.RichText{rtText("ab", rtUnderline)}},
		{name: "another text decoration is lost", in: `<span style="text-decoration: overline">a</span><span style="text-decoration: none">b</span>`,
			want: content.RichText{rtText("ab")}, notes: []string{kept("<span>")}},
		{name: "a strike by a span", in: `<span style="text-decoration: line-through">a</span><span style="TEXT-DECORATION:Line-Through !important">b</span>`,
			want: content.RichText{rtText("ab", rtStrike)}},
		{name: "an underline and a strike by a span", in: `<span style="text-decoration: underline line-through">a</span>`,
			want: content.RichText{rtText("a", rtUnderline, rtStrike)}},
		{name: "bold by a span", in: `<span style="font-weight: bold">a</span><span style="font-weight:BOLDER !important">b</span>` +
			`<span style="font-weight: 600">c</span><span style="font-weight:700">d</span><span style="font-weight: 900">e</span>`,
			want: content.RichText{rtText("abcde", rtBold)}},
		{name: "a weight that is not bold", in: `<span style="font-weight: normal">a</span><span style="font-weight: 400">b</span><span style="font-weight: 599">c</span>` +
			`<span style="font-weight: 901">d</span><span style="font-weight: lighter">e</span><span style="font-weight: heavy">f</span>`,
			want: content.RichText{rtText("abcdef")}, notes: []string{kept("<span>")}},
		{name: "italic by a span", in: `<span style="font-style: italic">a</span><span style="font-style: oblique 10deg">b</span><span style="font-style:Oblique">c</span>`,
			want: content.RichText{rtText("abc", rtItalic)}},
		{name: "a style that is not italic", in: `<span style="font-style: normal">a</span>`,
			want: content.RichText{rtText("a")}, notes: []string{kept("<span>")}},
		{name: "a color by a span", in: `<span style="color: #cf2e2e">a</span><span style="color:#cf2e2e;background-color: rgb(255 244 229 / 50%)">b</span>`,
			want: content.RichText{
				rtText("a", rtHighlight(content.Highlight{Color: content.Color{Value: "#cf2e2e"}})),
				rtText("b", rtHighlight(content.Highlight{Color: content.Color{Value: "#cf2e2e"}, BackgroundColor: content.Color{Value: "rgb(255 244 229 / 50%)"}})),
			}},
		{name: "a background color by a span", in: `<span style="background-color:hsl(10, 20%, 30%)">a</span>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{BackgroundColor: content.Color{Value: "hsl(10, 20%, 30%)"}}))}},
		{name: "colors of a span that are clear or not colors are not imported", in: `<span style="color:transparent">a</span><span style="color:red;background-color:rgba(0, 0, 0, 0)">b</span>` +
			`<span style="color:#0000">c</span><span style="color:calc(1)">d</span><span style="color:#xyz">e</span>`,
			want: content.RichText{rtText("abcde")}, notes: []string{kept("<span>")}},
		{name: "a clear background keeps the color of a span", in: `<span style="background-color:rgba(0, 0, 0, 0);color:#111">a</span>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Color: content.Color{Value: "#111"}}))}},
		{name: "several settings of a span", in: `<span style="font-weight:700;font-style:italic;text-decoration:underline line-through;color:#111;font-size:20px">a</span>`,
			want: content.RichText{rtText("a", rtUnderline, rtStrike, rtBold, rtItalic, rtHighlight(content.Highlight{Color: content.Color{Value: "#111"}}))}},
		{name: "a span that makes a mark is not noted for the settings it does not read", in: `<span style="font-size:20px;font-weight:bold" class="x">a</span>`,
			want: content.RichText{rtText("a", rtBold)}},
		{name: "a span with settings that are not read keeps its text", in: `<span style="font-size:20px;margin:0">a</span>`,
			want: content.RichText{rtText("a")}, notes: []string{kept("<span>")}},
		{name: "the settings of a span in a mark", in: `<b><span style="font-weight:bold">a</span></b><i><span style="font-style:italic">b</span>c</i>`,
			want: content.RichText{rtText("a", rtBold), rtText("bc", rtItalic)}},
		{name: "a span with a class of a highlight takes its color from the class and the rest from its style", in: `<span style="font-weight:bold;color:#111" class="has-inline-color has-vivid-red-color">a</span>`,
			want: content.RichText{rtText("a", rtBold, rtHighlight(content.Highlight{Color: content.Color{Preset: "red"}}))}},
		{name: "a span with no attributes", in: "<span>a</span><span> b</span>", want: content.RichText{rtText("a b")}},
		{name: "a span with a class", in: `<span class="x">a</span>`, want: content.RichText{rtText("a")}, notes: []string{kept("<span>")}},

		// White space.
		{name: "white space is collapsed", in: "a  b\n\tc\r\nd\fe", want: content.RichText{rtText("a b c d e")}},
		{name: "white space at the ends", in: "  \n a b \t", want: content.RichText{rtText("a b")}},
		{name: "white space across marks", in: "a <b> b</b>  <i> c </i>", want: content.RichText{rtText("a "), rtText("b", rtBold), rtText(" "), rtText("c", rtItalic)}},
		{name: "white space only", in: " \n\t ", want: nil},
		{name: "a space inside a mark at the end", in: "a<b> </b>", want: content.RichText{rtText("a")}},
		{name: "no-break spaces stay", in: nbsp + "a&nbsp;&nbsp;b" + nbsp, want: content.RichText{rtText(nbsp + "a" + nbsp + nbsp + "b" + nbsp)}},
		{name: "other separators are spaces", in: "a" + string(rune(0x0B)) + nel + ls + "b", want: content.RichText{rtText("a b")}},
		{name: "references", in: "1 &lt; 2 &amp; 3", want: content.RichText{rtText("1 < 2 & 3")}},
		{name: "br", in: "a<br>b<br/><br>c", want: content.RichText{rtText("a"), rtBreak(), rtText("b"), rtBreak(), rtBreak(), rtText("c")}},
		{name: "space around br", in: "a <br> b", want: content.RichText{rtText("a"), rtBreak(), rtText("b")}},
		{name: "br at the ends", in: "<br>a<br>", want: content.RichText{rtBreak(), rtText("a"), rtBreak()}},
		{name: "br in a mark", in: "<b>a<br>b</b>", want: content.RichText{rtText("a", rtBold), rtBreak(), rtText("b", rtBold)}},
		{name: "wbr", in: "a<wbr>b", want: content.RichText{rtText("ab")}},
		{name: "comments", in: "a<!-- x -->b", want: content.RichText{rtText("ab")}},
		{name: "a control character", in: "a" + string(rune(1)) + "b", want: content.RichText{rtText("a" + string(rune(1)) + "b")}},

		// Verses.
		{name: "a verse", verse: true, in: "Roses are red,\nViolets are blue", want: content.RichText{rtText("Roses are red,"), rtBreak(), rtText("Violets are blue")}},
		{name: "a verse with CRLF and CR", verse: true, in: "a\r\nb\rc", want: content.RichText{rtText("a"), rtBreak(), rtText("b"), rtBreak(), rtText("c")}},
		{name: "a verse keeps the spaces", verse: true, in: "  a  b \n  c", want: content.RichText{rtText("  a  b "), rtBreak(), rtText("  c")}},
		{name: "a verse: tabs and separators", verse: true, in: "a\tb" + string(rune(0x0B)) + "c&#xC;d", want: content.RichText{rtText("a b c d")}},
		{name: "a verse: br", verse: true, in: "a<br>b\n<i>c</i>", want: content.RichText{rtText("a"), rtBreak(), rtText("b"), rtBreak(), rtText("c", rtItalic)}},
		{name: "a verse: the end", verse: true, in: "a\n\n<br>\n", want: content.RichText{rtText("a")}},
		{name: "a verse: the start", verse: true, in: "\na", want: content.RichText{rtBreak(), rtText("a")}},

		// Elements that cannot be kept.
		{name: "unknown elements", in: `<kbd>a</kbd> <span class="x">b</span> <font color=red>c</font><foo-bar>d</foo-bar><my_tag>e</my_tag>`,
			want:  content.RichText{rtText("a b cde")},
			notes: []string{kept(`<kbd>, <span>, <font>, <foo-bar>, <"my_tag">`)}},
		{name: "more than ten elements", in: "<a1>1</a1><a2>2</a2><a3>3</a3><a4>4</a4><a5>5</a5><a6>6</a6><a7>7</a7><a8>8</a8><a9>9</a9><b1>0</b1><b2>1</b2><b3>2</b3>",
			want:  content.RichText{rtText("123456789012")},
			notes: []string{kept("<a1>, <a2>, <a3>, <a4>, <a5>, <a6>, <a7>, <a8>, <a9>, <b1>, and 2 more")}},
		{name: "script and style go with their content", in: "a<script>alert(1)</script>b<style>p{}</style>c<template><b>x</b></template><noscript>n</noscript>",
			want:  content.RichText{rtText("abc")},
			notes: []string{bodyNoteLine(Skipped, "removed <script>, <style>, <template>, <noscript> with their content")}},
		{name: "a block element", in: "a<div>b</div>c", want: content.RichText{rtText("a"), rtBreak(), rtText("b"), rtBreak(), rtText("c")},
			notes: []string{kept("<div>")}},
		{name: "block elements at the ends", in: "<div>a</div><p>b</p>", want: content.RichText{rtText("a"), rtBreak(), rtText("b")},
			notes: []string{kept("<div>, <p>")}},
		{name: "a center element sets its content apart", in: "a<center>b</center>c", want: content.RichText{rtText("a"), rtBreak(), rtText("b"), rtBreak(), rtText("c")},
			notes: []string{kept("<center>")}},
		{name: "a block element after a break", in: "a<br><div>b</div>", want: content.RichText{rtText("a"), rtBreak(), rtText("b")},
			notes: []string{kept("<div>")}},
		{name: "block elements in marks", in: "<b>a<div>b</div></b>", want: content.RichText{rtText("a", rtBold), rtBreak(), rtText("b", rtBold)},
			notes: []string{kept("<div>")}},
		{name: "an empty block element", in: "a<div></div>b", want: content.RichText{rtText("a"), rtBreak(), rtText("b")},
			notes: []string{kept("<div>")}},
		{name: "an image", in: `a<img src="x.png">b`, err: "the text has <img>, which rich text cannot have"},
		{name: "an image and a ruby", in: `<img src="x.png"><ruby>漢<rt>kan</rt></ruby><img>`, err: "the text has <img>, <ruby>, which rich text cannot have"},
		{name: "a list", in: "<ul><li>a</li></ul>", err: "the text has <ul>, which rich text cannot have"},
		{name: "foreign elements", in: `<svg><path d="x"/></svg><math><mi>x</mi></math>`, err: "the text has <svg>, <math>, which rich text cannot have"},
		{name: "an image in a link", in: `<a href="/about/"><img src="x.png"></a>`, err: "the text has <img>, which rich text cannot have"},

		// Links.
		{name: "a link to an entry", in: `<a href="https://example.com/about/">About</a>`, want: content.RichText{rtText("About", rtLink(content.Link{Entry: "@12"}))}},
		{name: "a link to an entry with an anchor", in: `<a href="https://example.com/about/#team">About</a>`,
			want: content.RichText{rtText("About", rtLink(content.Link{Entry: "@12", Anchor: "team"}))}},
		{name: "a link to an entry with an anchor that is not accepted", in: `<a href="https://example.com/about/#%E3%83%81%E3%83%BC%E3%83%A0">About</a>`,
			want:  content.RichText{rtText("About", rtLink(content.Link{Entry: "@12"}))},
			notes: []string{bodyNoteLine(Skipped, `the anchor "チーム" of the link is removed: invalid name "チーム": use ASCII letters, digits, "-" and "_"`)}},
		{name: "a link to an entry without a slash", in: `<a href="HTTPS://Example.COM/about">About</a>`, want: content.RichText{rtText("About", rtLink(content.Link{Entry: "@12"}))}},
		{name: "a link to an entry by the number", in: `<a href="https://example.com/?p=11">a</a><a href="/index.php?page_id=12">b</a><a href="/?attachment_id=13&p=11#x">c</a>`,
			want: content.RichText{
				rtText("a", rtLink(content.Link{Entry: "@11"})), rtText("b", rtLink(content.Link{Entry: "@12"})),
				rtText("c", rtLink(content.Link{Media: "@13"})),
			}},
		{name: "a link by a path", in: `<a href="/about/">a</a><a href="//example.com/about/">b</a><a href="../../../about/#team">c</a>`,
			want: content.RichText{rtText("ab", rtLink(content.Link{Entry: "@12"})), rtText("c", rtLink(content.Link{Entry: "@12", Anchor: "team"}))}},
		{name: "a link by a path with more than one slash", in: `<a href="https://example.com//about/">a</a><a href="//example.com//about">b</a>` +
			`<a href="https://example.com/2026//10///hello/">c</a><a href="https://example.com//wp-content//uploads/2026/10/%E5%A0%B1%E5%91%8A.pdf">d</a>`,
			want: content.RichText{
				rtText("ab", rtLink(content.Link{Entry: "@12"})), rtText("c", rtLink(content.Link{Entry: "@11"})), rtText("d", rtLink(content.Link{Media: "@14"})),
			}},
		{name: "a link to a page that is not imported, by a path with more than one slash", in: `<a href="https://example.com//2026/10//gone/?x=1#top">a</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Href: "/2026/10/gone/?x=1#top"}))}},
		{name: "a link by a relative path", in: `<a href="sub/">a</a><a href="?s=x">b</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Href: "/2026/10/hello/sub/"})), rtText("b", rtLink(content.Link{Entry: "@11"}))}},
		{name: "a link to a file", in: `<a href="https://example.com/wp-content/uploads/2026/10/photo-scaled.jpg">a</a>` +
			`<a href="https://example.com/wp-content/uploads/2026/10/photo-300x200-scaled.jpg#frag">b</a>` +
			`<a href="https://example.com/wp-content/uploads/2026/10/photo-150x150.jpg">c</a>` +
			`<a href="https://example.com/wp-content/uploads/2026/10/photo.jpg">d</a>` +
			`<a href="https://example.com/wp-content/uploads/2026/10/%E5%A0%B1%E5%91%8A.pdf">e</a>` +
			`<a href="https://example.com/photo/">f</a><a href="https://example.com/photo/#x">g</a><a href="/?attachment_id=14">h</a>`,
			want: content.RichText{
				rtText("abcd", rtLink(content.Link{Media: "@13"})), rtText("e", rtLink(content.Link{Media: "@14"})),
				rtText("fg", rtLink(content.Link{Media: "@13"})), rtText("h", rtLink(content.Link{Media: "@14"})),
			}},
		{name: "a link to another file of the old site", in: `<a href="https://example.com/wp-content/uploads/2026/10/other.pdf">a</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Href: "/wp-content/uploads/2026/10/other.pdf"}))}},
		{name: "a link to a page that is not imported", in: `<a href="https://example.com/2026/10/gone/">a</a><a href="/?p=15">b</a><a href="/?p=99">c</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Href: "/2026/10/gone/"})), rtText("b", rtLink(content.Link{Href: "/?p=15"})), rtText("c", rtLink(content.Link{Href: "/?p=99"}))}},
		{name: "a link to the old site", in: `<a href="https://example.com/category/news/?page=2#top">a</a><a href="https://example.com">b</a><a href="http://example.com:80/x">c</a>`,
			want: content.RichText{
				rtText("a", rtLink(content.Link{Href: "/category/news/?page=2#top"})), rtText("b", rtLink(content.Link{Href: "/"})),
				rtText("c", rtLink(content.Link{Href: "/x"})),
			}},
		{name: "a link to another site", in: `<a href="https://other.example.org/x?y=1#z">a</a><a href="https://example.com:8080/x">b</a><a href="http://sub.example.com/">c</a>`,
			want: content.RichText{
				rtText("a", rtLink(content.Link{Href: "https://other.example.org/x?y=1#z"})), rtText("b", rtLink(content.Link{Href: "https://example.com:8080/x"})),
				rtText("c", rtLink(content.Link{Href: "http://sub.example.com/"})),
			}},
		{name: "mailto and tel", in: `<a href="mailto:info@example.org">a</a><a href="tel:+81312345678">b</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Href: "mailto:info@example.org"})), rtText("b", rtLink(content.Link{Href: "tel:+81312345678"}))}},
		{name: "an address with space and controls", in: "<a href=\"  https://other.example.org/\tx  \">a</a>",
			want: content.RichText{rtText("a", rtLink(content.Link{Href: "https://other.example.org/x"}))}},
		{name: "a link to a place in the page", in: `<a href="#top">a</a><a href="#section-2">b</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Anchor: "top"})), rtText("b", rtLink(content.Link{Anchor: "section-2"}))}},
		{name: "a link to a place with a name that is not accepted", in: `<a href="#%E6%97%A5%E6%9C%AC%E8%AA%9E">a</a><a href="#">b</a><a href="#a.b">c</a>`,
			want: content.RichText{rtText("abc")},
			notes: []string{
				bodyNoteLine(Skipped, `the link to "#%E6%97%A5%E6%9C%AC%E8%AA%9E" is removed: the anchor is not a name that whiteroom accepts`),
				bodyNoteLine(Skipped, `the link to "#" is removed: the anchor is not a name that whiteroom accepts`),
				bodyNoteLine(Skipped, `the link to "#a.b" is removed: the anchor is not a name that whiteroom accepts`),
			}},
		{name: "a link that cannot be one", in: `<a href="javascript:alert(1)">a</a><a href="ftp://example.org/">b</a><a href="https://u:p@example.org/">c</a>`,
			want: content.RichText{rtText("abc")},
			notes: []string{
				bodyNoteLine(Skipped, `the link to "javascript:alert(1)" is removed: scheme "javascript" is not allowed`),
				bodyNoteLine(Skipped, `the link to "ftp://example.org/" is removed: scheme "ftp" is not allowed`),
				bodyNoteLine(Skipped, `the link to "https://u:p@example.org/" is removed: credentials in URLs are not allowed`),
			}},
		{name: "a note quotes the address as it is written, not as it is made from the link of the item", in: `<a href="x/?\">a</a>`,
			want: content.RichText{rtText("a")}, notes: []string{bodyNoteLine(Skipped, `the link to "x/?\\" is removed: backslashes are not allowed`)}},
		{name: "an anchor with no address", in: `<a name="top">a</a><a href="">b</a><a>c</a>`, want: content.RichText{rtText("abc")}, notes: []string{kept("<a>")}},
		{name: "a link to a blank", in: `<a href="https://other.example.org/" target="_BLANK" rel=" NoFollow noopener sponsored noreferrer ugc nofollow ">a</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Href: "https://other.example.org/", NewTab: true, Rel: []content.Rel{content.RelNofollow, content.RelSponsored, content.RelUGC}}))}},
		{name: "a target that is not a blank", in: `<a href="/about/" target="_self" rel="noopener">a</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Entry: "@12"}))}},
		{name: "marks in a link", in: `<a href="/about/"><b>a</b>b</a>`,
			want: content.RichText{rtText("a", rtLink(content.Link{Entry: "@12"}), rtBold), rtText("b", rtLink(content.Link{Entry: "@12"}))}},
		{name: "links to the same place are joined", in: `<a href="/about/">a</a><a href="https://example.com/about/">b</a><a href="/about/" target="_blank">c</a>`,
			want: content.RichText{rtText("ab", rtLink(content.Link{Entry: "@12"})), rtText("c", rtLink(content.Link{Entry: "@12", NewTab: true}))}},

		// Highlights.
		{name: "a text color", in: `<mark style="background-color:rgba(0, 0, 0, 0);color:#cf2e2e" class="has-inline-color has-vivid-red-color">a</mark>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Color: content.Color{Preset: "red"}}))}},
		{name: "a text color that is not mapped", in: `<span style="color:#cf2e2e" class="has-inline-color has-luminous-vivid-orange-color">a</span>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Color: content.Color{Preset: "luminous-vivid-orange"}}))}},
		{name: "a background color", in: `<mark style="background-color:#fcb900" class="has-inline-color has-yellow-background-color">a</mark>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{BackgroundColor: content.Color{Preset: "yellow"}}))}},
		{name: "a color and a background", in: `<mark class="has-vivid-red-color has-inline-color has-yellow-background-color">a</mark>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Color: content.Color{Preset: "red"}, BackgroundColor: content.Color{Preset: "yellow"}}))}},
		{name: "a color written in the style", in: `<mark style="color:#cf2e2e;background-color: rgb(255 244 229 / 50%)">a</mark><mark style="COLOR: hsl(10, 20%, 30%)">b</mark>`,
			want: content.RichText{
				rtText("a", rtHighlight(content.Highlight{Color: content.Color{Value: "#cf2e2e"}, BackgroundColor: content.Color{Value: "rgb(255 244 229 / 50%)"}})),
				rtText("b", rtHighlight(content.Highlight{Color: content.Color{Value: "hsl(10, 20%, 30%)"}})),
			}},
		{name: "the preset wins over the value", in: `<mark style="color:#cf2e2e" class="has-inline-color has-vivid-red-color">a</mark>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Color: content.Color{Preset: "red"}}))}},
		{name: "a color that is not imported", in: `<mark style="color:#cf2e2e" class="has-inline-color has-gone-color">a</mark>`,
			want: content.RichText{rtText("a")},
			notes: []string{
				bodyNoteLine(Skipped, `the color preset "gone" is not imported: blocks.presets.color maps it to nothing`), kept("<mark>"),
			}},
		{name: "a color with a name that is not accepted", in: `<mark class="has-inline-color has-赤-color">a</mark>`,
			want: content.RichText{rtText("a")},
			notes: []string{
				bodyNoteLine(Skipped, `the color preset "赤" is not imported: it is not a name that whiteroom accepts`), kept("<mark>"),
			}},
		{name: "colors that are clear or not colors", in: `<mark style="color:transparent;background-color:rgba(0, 0, 0, 0)">a</mark><mark style="color:red;background-color:#00000000">b</mark>` +
			`<mark style="background-color:hsla(0 0% 0% / 0%)">c</mark><mark style="color:#0000">d</mark><mark style="color:calc(1)">e</mark>`,
			want: content.RichText{rtText("abcde")}, notes: []string{kept("<mark>")}},
		{name: "a clear background keeps the color", in: `<mark style="background-color:rgba(0, 0, 0, 0);color:#111" class="has-inline-color">a</mark>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Color: content.Color{Value: "#111"}}))}},
		{name: "a class that is a variant", in: `<mark class="marker">a</mark><span class="has-inline-color marker has-vivid-red-color">b</span>`,
			want:  content.RichText{rtText("ab", rtHighlight(content.Highlight{Variant: "marker-yellow"}))},
			notes: []string{bodyNoteLine(Skipped, colorsLost("<span>"))}},
		{name: "a class that is mapped wins over one that is not", in: `<span class="extra marker">a</span><mark class="swl-marker marker">b</mark>`,
			want: content.RichText{rtText("ab", rtHighlight(content.Highlight{Variant: "marker-yellow"}))}},
		{name: "the classes of WordPress are not variants", in: `<span class="has-small-font-size marker">a</span><mark class="has-inline-color has-small-font-size wp-x">b</mark>` +
			`<span class="has-inline-color wp-x">c</span><mark class="wp-x">d</mark>`,
			want:  content.RichText{rtText("a", rtHighlight(content.Highlight{Variant: "marker-yellow"})), rtText("bcd")},
			notes: []string{kept("<mark>, <span>")}},
		{name: "a class that is not mapped, and the colors that it wins over", in: `<mark class="has-inline-color extra has-vivid-red-color">a</mark>`,
			want:  content.RichText{rtText("a", rtHighlight(content.Highlight{Variant: "extra"}))},
			notes: []string{bodyNoteLine(Skipped, colorsLost("<mark>"))}},
		{name: "a variant wins over the colors of the style", in: `<mark style="color:#cf2e2e" class="marker">a</mark><span style="background-color:#fcb900" class="marker">b</span>`,
			want:  content.RichText{rtText("ab", rtHighlight(content.Highlight{Variant: "marker-yellow"}))},
			notes: []string{bodyNoteLine(Skipped, colorsLost("<mark>, <span>"))}},
		{name: "a variant and a color that is clear has nothing to note", in: `<mark style="background-color:rgba(0, 0, 0, 0)" class="marker">a</mark>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Variant: "marker-yellow"}))}},
		{name: "a class that stays", in: `<mark class="swl-marker mark_yellow">a</mark>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Variant: "swl-marker"}))}},
		{name: "a class that is nothing", in: `<mark class="plain">a</mark><span class="plain">b</span>`,
			want: content.RichText{rtText("ab")}, notes: []string{kept("<mark>, <span>")}},
		{name: "a class of a span is a variant", in: `<span class="marker">a</span><span class="other">b</span>`,
			want: content.RichText{rtText("a", rtHighlight(content.Highlight{Variant: "marker-yellow"})), rtText("b")}, notes: []string{kept("<span>")}},
		{name: "a mark with nothing", in: `<mark>a</mark>`, want: content.RichText{rtText("a")}, notes: []string{kept("<mark>")}},
		{name: "a highlight and an underline", in: `<span style="text-decoration:underline" class="has-inline-color has-vivid-red-color">a</span>`,
			want: content.RichText{rtText("a", rtUnderline, rtHighlight(content.Highlight{Color: content.Color{Preset: "red"}}))}},
		{name: "a highlight in a highlight", in: `<mark class="marker">a<span class="has-inline-color has-vivid-red-color">b</span></mark>`,
			want:  content.RichText{rtText("ab", rtHighlight(content.Highlight{Variant: "marker-yellow"}))},
			notes: []string{bodyNoteLine(Changed, "removed <span> inside <mark>")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := linkImport(t)
			c := richConv(imp, itemLink, richSettings())
			got, err := c.richText(richRoot(t, tt.in), tt.verse, noteAt{})
			if tt.err != "" {
				var le *legacyError
				if !errors.As(err, &le) || le.reason != tt.err {
					t.Errorf("richText(%q) = %v, %v, want the legacy reason %q", tt.in, got, err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("richText(%q) = %v", tt.in, err)
			}
			checkRichText(t, tt.in, got)
			want := expandIDs(t, imp, tt.want)
			if len(got) != len(want) || len(got) > 0 && !reflect.DeepEqual(got, want) {
				g, _ := got.MarshalJSON()
				w, _ := want.MarshalJSON()
				t.Errorf("richText(%q) =\n%s\nwant\n%s", tt.in, g, w)
			}
			c.flush(nil)
			var lines []string
			for _, n := range tt.notes {
				lines = append(lines, bodyLine(n))
			}
			wantLines(t, imp.Report, lines)
		})
	}
}

func TestRichTextOfTheClassicEditor(t *testing.T) {
	// What is noted of the styles, the places that links go to, and the addresses that have a shortcode, is noted for the HTML of
	// the classic editor only.
	kept := func(tags string) string { return bodyNoteLine(Changed, "kept only the text of "+tags) }
	lost := func(items, tag string) string { return bodyNoteLine(Skipped, "lost "+items+" of "+tag+lostSuffix) }
	for _, tt := range []struct {
		name, in string
		classic  bool
		notes    []string
		targets  []string
	}{
		{name: "a style no mark took, in the block editor", in: `<span style="font-size: 1px">a</span>`, notes: []string{kept("<span>")}},
		{name: "a style no mark took", in: `<span style="font-size: 1px">a</span>`, classic: true, notes: []string{kept("<span>"), lost("style.font-size", "<span>")}},
		{name: "the style that a mark took", in: `<span style="font-weight: 700; color: #f00; font-size: 1px">a</span>`, classic: true,
			notes: []string{lost("style.font-size", "<span>")}},
		{name: "the style of an element that makes no mark", in: `<b style="font-size: 1px">a</b><div style="margin: 0">b</div>`, classic: true,
			notes: []string{lost("style.font-size", "<b>"), lost("style.margin", "<div>"), kept("<div>")}},
		{name: "places in the block editor", in: `<a name="x">a</a><a id="y"></a>`, notes: []string{kept("<a>")}},
		{name: "places", in: `<a name="x">a</a><a id="y"></a>`, classic: true, targets: []string{"x", "y"}},
		{name: "a place that has an id and another name", in: `<a id="i" name="n"></a>`, classic: true, targets: []string{"i"},
			notes: []string{bodyNoteLine(Skipped, `the anchor "n" is not imported: the element has the id "i"`)}},
		{name: "a link is not a place", in: `<a href="/about/" id="i" name="n">a</a>`, classic: true},
		{name: "an anchor with no name and no id", in: `<a>a</a>`, classic: true, notes: []string{kept("<a>")}},
		{name: "an address with a shortcode in the block editor", in: `<a href="[home_url]/x/">a</a>`},
		{name: "an address with a shortcode", in: `<a href="[home_url]/x/">a</a>`, classic: true,
			notes: []string{bodyNoteLine(Review, `the address "[home_url]/x/" has a shortcode, which whiteroom does not run`)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := linkImport(t)
			c := richConv(imp, "", nil)
			c.inClassic = tt.classic
			if _, err := c.richText(richRoot(t, tt.in), false, noteAt{}); err != nil {
				t.Fatalf("richText(%q) = %v", tt.in, err)
			}
			if !slices.Equal(c.targets, tt.targets) {
				t.Errorf("the places are %q, want %q", c.targets, tt.targets)
			}
			c.flush(nil)
			var lines []string
			for _, n := range tt.notes {
				lines = append(lines, bodyLine(n))
			}
			wantLines(t, imp.Report, lines)
		})
	}

	// A text can have any number of elements with any number of properties; the text keeps few, and the note names only the first.
	lostNotes := func(t *testing.T, markup string) []string {
		t.Helper()
		imp := linkImport(t)
		c := richConv(imp, "", nil)
		c.inClassic = true
		if _, err := c.richText(richRoot(t, markup), false, noteAt{}); err != nil {
			t.Fatal(err)
		}
		c.flush(nil)
		var notes []string
		for _, e := range imp.Report.Entries() {
			if strings.HasPrefix(e.Message, "lost ") {
				notes = append(notes, e.Message)
			}
		}
		return notes
	}
	t.Run("many elements", func(t *testing.T) {
		var b strings.Builder
		for i := range 3 * maxStyleNotes {
			fmt.Fprintf(&b, `<x-%d style="a: 1"></x-%d>`, i, i)
		}
		if notes := lostNotes(t, b.String()); len(notes) != maxStyleNotes {
			t.Errorf("there are %d notes, want %d", len(notes), maxStyleNotes)
		}
	})
	t.Run("many properties", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(`<span style="`)
		for i := range 3 * maxStyleNotes {
			fmt.Fprintf(&b, "p%d: 1;", i)
		}
		b.WriteString(`">a</span>`)
		notes := lostNotes(t, b.String())
		if want := fmt.Sprintf(", and %d more of <span>%s", maxStyleNotes-maxLostShown, lostTail); len(notes) != 1 || !strings.Contains(notes[0], want) {
			t.Errorf("the notes are %.300q, want one that ends with %q", notes, want)
		}
	})
}

// expandIDs puts the IDs of the posts, written @ and the number in the tests, in the links of want.
func expandIDs(t testing.TB, imp *Import, want content.RichText) content.RichText {
	t.Helper()
	out := make(content.RichText, len(want))
	for i, in := range want {
		out[i] = in
		out[i].Marks = make([]content.Mark, len(in.Marks))
		for j, m := range in.Marks {
			if m.Link != nil {
				l := *m.Link
				for _, id := range []*string{&l.Entry, &l.Media} {
					if num, ok := strings.CutPrefix(*id, "@"); ok {
						var post int64
						_, _ = fmt.Sscan(num, &post)
						*id = linkTarget(t, imp, post)
					}
				}
				m.Link = &l
			}
			out[i].Marks[j] = m
		}
		if len(in.Marks) == 0 {
			out[i].Marks = nil
		}
	}
	return out
}

func TestPlainText(t *testing.T) {
	kept := func(tags string) []string { return []string{bodyNoteLine(Changed, "kept only the text of "+tags)} }
	for _, tt := range []struct {
		name, in, want string
		notes          []string
		err            string
	}{
		{name: "nothing", in: ""},
		{name: "text", in: "Buy now", want: "Buy now"},
		{name: "white space", in: "  Buy \n\t now  ", want: "Buy now"},
		{name: "marks are dropped", in: "<strong>Buy</strong> <em>now</em>", want: "Buy now", notes: kept("<strong>, <em>")},
		{name: "a link is dropped", in: `<a href="/about/">About</a> us`, want: "About us", notes: kept("<a>")},
		{name: "a highlight is dropped", in: `<mark class="has-inline-color has-gone-color">a</mark>`, want: "a", notes: kept("<mark>")},
		{name: "a span", in: `<span class="x">a</span><span>b</span>`, want: "ab", notes: kept("<span>")},
		{name: "br is a space", in: "a<br>b<br><br>c<br>", want: "a b c"},
		{name: "no-break spaces stay", in: "a&nbsp;b", want: "a" + string(rune(0xA0)) + "b"},
		{name: "an image", in: `<img src="x.png">`, err: "the text has <img>, which rich text cannot have"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := linkImport(t)
			c := richConv(imp, "", richSettings())
			got, err := c.plainText(richRoot(t, tt.in), noteAt{})
			if tt.err != "" {
				var le *legacyError
				if !errors.As(err, &le) || le.reason != tt.err {
					t.Errorf("plainText(%q) = %q, %v, want the legacy reason %q", tt.in, got, err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("plainText(%q) = %q, %v, want %q", tt.in, got, err, tt.want)
			}
			c.flush(nil)
			var lines []string
			for _, n := range tt.notes {
				lines = append(lines, bodyLine(n))
			}
			wantLines(t, imp.Report, lines)
		})
	}
}

func TestCodeText(t *testing.T) {
	for _, tt := range []struct {
		name, in, want string
		notes          []string
	}{
		{name: "nothing", in: ""},
		{name: "text as it is", in: "  func main() {\n\treturn\n  }\n\n", want: "  func main() {\n\treturn\n  }\n\n"},
		{name: "line breaks", in: "a\r\nb\rc", want: "a\nb\nc"},
		{name: "references", in: "if a &lt; b &amp;&amp; c &gt; d", want: "if a < b && c > d"},
		{name: "br", in: "a<br>b<br/>", want: "a\nb\n"},
		{name: "elements keep their text", in: `<span class="k">func</span> <b>main</b>()<wbr>`, want: "func main()",
			notes: []string{bodyNoteLine(Changed, "kept only the text of <span>, <b>")}},
		{name: "comments", in: "a<!-- x -->b", want: "ab"},
		{name: "script", in: "<script>x()</script>", want: "x()", notes: []string{bodyNoteLine(Changed, "kept only the text of <script>")}},
		{name: "svg", in: "<svg><title>t</title></svg>", want: "t", notes: []string{bodyNoteLine(Changed, "kept only the text of <svg>, <title>")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := linkImport(t)
			c := richConv(imp, "", nil)
			if got := c.codeText(richRoot(t, tt.in), noteAt{}); got != tt.want {
				t.Errorf("codeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
			c.flush(nil)
			var lines []string
			for _, n := range tt.notes {
				lines = append(lines, bodyLine(n))
			}
			wantLines(t, imp.Report, lines)
		})
	}
}

func TestLinkWithoutTheHome(t *testing.T) {
	for _, tt := range []struct {
		name     string
		export   *wxr.Export
		itemLink string
		href     string
		want     *content.Link
		note     string
	}{
		{name: "a path", export: &wxr.Export{}, href: "/about/", want: &content.Link{Href: "/about/"}},
		{name: "a relative path is from the root", export: &wxr.Export{}, href: "about/", want: &content.Link{Href: "/about/"}},
		{name: "an address that is not one", export: &wxr.Export{}, href: "a b:c",
			note: `the link to "a b:c" is removed: invalid URL`},
		{name: "a protocol-relative address", export: &wxr.Export{}, href: "//cdn.example.org/x",
			note: `the link to "//cdn.example.org/x" is removed: protocol-relative URLs are not allowed`},
		{name: "an address", export: &wxr.Export{}, href: "https://example.org/x", want: &content.Link{Href: "https://example.org/x"}},
		{name: "an address that cannot be read", export: &wxr.Export{}, href: "http://[::1",
			note: `the link to "http://[::1" is removed: invalid URL`},
		{name: "the home of the blog", export: &wxr.Export{Channel: wxr.Channel{BaseBlogURL: "https://example.com/blog"}},
			href: "https://example.com/blog/about/?x=1", want: &content.Link{Href: "/about/?x=1"}},
		{name: "a link of the item on another host", export: &wxr.Export{Channel: wxr.Channel{Link: "https://example.com"}},
			itemLink: "https://old.example.net/post/", href: "a/b", want: &content.Link{Href: "https://old.example.net/post/a/b"}},
		{name: "a link of the item that is not an address", export: &wxr.Export{Channel: wxr.Channel{Link: "https://example.com"}},
			itemLink: "not a link", href: "/a", want: &content.Link{Href: "/a"}},
		{name: "a note quotes the address as it is written", export: &wxr.Export{Channel: wxr.Channel{Link: "https://example.com"}},
			itemLink: "https://old.example.net/post/", href: `?\`, note: `the link to "?\\" is removed: backslashes are not allowed`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := New("site.xml", tt.export, NewReport(Info{File: "site.xml"}))
			c := richConv(imp, tt.itemLink, nil)
			got, err := c.link(tt.href, "", "", noteAt{})
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("link(%q) = %+v, %v, want %+v", tt.href, got, err, tt.want)
			}
			c.flush(nil)
			var lines []string
			if tt.note != "" {
				lines = append(lines, bodyLine("skipped: "+tt.note+" -> /body"))
			}
			wantLines(t, imp.Report, lines)
		})
	}
}

func TestLinkToTheHomeWithAQuery(t *testing.T) {
	// The home with a query that is not one of a post, such as ?cat=3 and ?s=tea, is a list that WordPress makes. It is not the
	// front page, which is the page of the address without a query.
	imp := linkImport(t)
	imp.WXR.Items = append(imp.WXR.Items, wxr.Item{ID: 21, Type: "page", Status: "publish", Title: "Home", Link: "https://example.com/"})
	front, err := imp.IDs.Assign(PostObject(imp.WXR.Post(21)), content.RefEntry)
	if err != nil {
		t.Fatal(err)
	}
	imp.AddRef(content.RefEntry, front, content.Ref{Type: content.TypePage, Language: "ja"})
	c := richConv(imp, "https://example.com/2026/10/hello/", nil)
	entry := func(post int64) *content.Link { return &content.Link{Entry: linkTarget(t, imp, post)} }
	for _, tt := range []struct {
		href string
		want *content.Link
	}{
		{"https://example.com/", &content.Link{Entry: front}},
		{"/", &content.Link{Entry: front}},
		{"https://example.com/?", &content.Link{Entry: front}},
		{"/?utm_source=x", &content.Link{Href: "/?utm_source=x"}},
		{"https://example.com/?cat=3", &content.Link{Href: "/?cat=3"}},
		{"https://example.com/?s=tea#top", &content.Link{Href: "/?s=tea#top"}},
		{"/?tag=x", &content.Link{Href: "/?tag=x"}},
		{"https://example.com/?author=2", &content.Link{Href: "/?author=2"}},
		{"/?m=202610", &content.Link{Href: "/?m=202610"}},
		{"/?feed=rss2", &content.Link{Href: "/?feed=rss2"}},
		{"https://example.com/index.php?cat=3", &content.Link{Href: "/index.php?cat=3"}},
		{"https://example.com/?p=11", entry(11)},
		{"https://example.com/?x=1&p=11", entry(11)},
		{"https://example.com/index.php?page_id=12", entry(12)},
		{"https://example.com/?p=0", &content.Link{Href: "/?p=0"}},
		{"https://example.com/?p=abc", &content.Link{Href: "/?p=abc"}},
		{"https://example.com/about/?cat=3", entry(12)},
	} {
		if got, err := c.link(tt.href, "", "", noteAt{}); err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("link(%q) = %+v, %v, want %+v", tt.href, got, err, tt.want)
		}
	}

	// The same in the paragraph of a body: the list stays a path with its query, and the home is the front page.
	text := wpPara(`<a href="https://example.com/?cat=3">News</a> <a href="/">Home</a>`)
	res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(text), Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatal(err)
	}
	checkBody(t, imp, bodySite(false), res)
	want := withIDs(jsonList(jsonBlock("paragraph", 0, "", "", fmt.Sprintf(
		`[{"type":"text","text":"News","marks":[{"type":"link","attrs":{"href":"/?cat=3"}}]},{"type":"text","text":" "},`+
			`{"type":"text","text":"Home","marks":[{"type":"link","attrs":{"entry":%q}}]}]`, front))))
	if got, _ := res.Blocks.MarshalJSON(); string(got) != want {
		t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
	}
	wantLines(t, imp.Report, nil)
}

func TestLinkToAddressesWithMoreThanOneSlash(t *testing.T) {
	// WordPress writes more than one slash after the host in every link of a site whose home option ends with a slash, and in
	// the address of a file whose uploads URL ends with one. The links to the entry and the medium are found by those
	// addresses, and by the addresses with one slash.
	e := &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com/"},
		Items: []wxr.Item{
			{ID: 11, Type: "post", Status: "publish", Title: "Hello", Link: "https://example.com//2026/10/hello/"},
			{
				ID: 14, Type: "attachment", Status: "inherit", Title: "Report", Link: "https://example.com//report/",
				AttachmentURL: "https://example.com/wp-content/uploads//2026/10/r.pdf",
				Meta:          wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/10/r.pdf"}},
			},
		},
	}
	imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
	for _, a := range []struct {
		id   int64
		kind content.RefKind
		ref  content.Ref
	}{
		{11, content.RefEntry, content.Ref{Type: content.TypePost, Language: "ja"}},
		{14, content.RefMedia, content.Ref{Type: content.MediaPDF}},
	} {
		id, err := imp.IDs.Assign(PostObject(e.Post(a.id)), a.kind)
		if err != nil {
			t.Fatal(err)
		}
		imp.AddRef(a.kind, id, a.ref)
	}
	entry, media := linkTarget(t, imp, 11), linkTarget(t, imp, 14)
	hrefs := []struct {
		href string
		link content.Link
	}{
		{"https://example.com//2026/10/hello/", content.Link{Entry: entry}},
		{"https://example.com/2026/10/hello/", content.Link{Entry: entry}},
		{"/2026/10/hello", content.Link{Entry: entry}},
		{"https://example.com/wp-content/uploads//2026/10/r.pdf", content.Link{Media: media}},
		{"https://example.com/wp-content/uploads/2026/10/r.pdf", content.Link{Media: media}},
		{"//example.com/wp-content/uploads//2026/10/r.pdf", content.Link{Media: media}},
	}
	var text strings.Builder
	var want []content.Block
	for i, h := range hrefs {
		text.WriteString(wpPara(`<a href="` + h.href + `">x</a>`))
		want = append(want, content.Block{Type: content.BlockParagraph, ID: bodyIDs[i], Text: content.RichText{rtText("x", rtLink(h.link))}})
	}
	res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(text.String()), Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatal(err)
	}
	checkBody(t, imp, bodySite(false), res)
	sameBlocks(t, res.Blocks, want)
	wantLines(t, imp.Report, nil)
}

func TestLinkBase(t *testing.T) {
	// A relative address is from the link of the item, which is read once for a body. A link that is longer than a path may
	// be is not used, as it would be copied into every address.
	link := func(n int) string { return "https://example.com/" + strings.Repeat("a", n) + "/" }
	fits := permPathBytes - len(link(0))
	for _, tt := range []struct {
		name, itemLink, want string
	}{
		{"a link of the most bytes", link(fits), "/" + strings.Repeat("a", fits) + "/x"},
		{"a link of a byte more", link(fits + 1), "/x"},
		{"a link of a million bytes", link(1000000), "/x"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := richConv(linkImport(t), tt.itemLink, nil)
			if got, err := c.link("x", "", "", noteAt{}); err != nil || got == nil || got.Href != tt.want {
				t.Errorf("link(x) = %+v, %v, want the address %q", got, err, tt.want)
			}
		})
	}

	c := richConv(linkImport(t), "https://example.com/2026/10/hello/", nil)
	if first := c.baseURL(); first == nil || c.baseURL() != first {
		t.Error("baseURL does not return the same address each time")
	}
	if want := len("https://example.com/2026/10/hello/"); c.baseLen != want {
		t.Errorf("the length of the base is %d, want %d", c.baseLen, want)
	}
}

// blockHTMLBytes returns the bytes of the HTML of the blocks of WordPress in text, those inside blocks too: what the
// conversion counts among the bytes of HTML that it reads when it converts them.
func blockHTMLBytes(t testing.TB, text string) int {
	t.Helper()
	parsed, err := wpblock.Parse(text, wpblock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var sum func(bs []wpblock.Block) int
	sum = func(bs []wpblock.Block) int {
		n := 0
		for i := range bs {
			n += len(bs[i].HTML()) + sum(bs[i].Inner)
		}
		return n
	}
	return sum(parsed)
}

// linkBytesBody converts six blocks, unit(1) to unit(6), each with a link to a relative address, as the content of an item
// whose link is itemLink. The limit of the HTML that is read is lowered so that two of the blocks fit, and the third is over
// it only with the bytes that its link counts: added is what resolving the address of unit(n) counts.
func linkBytesBody(t *testing.T, imp *Import, itemLink string, added int, unit func(n int) string) (*bodyConv, *BodyResult) {
	t.Helper()
	each := blockHTMLBytes(t, unit(1))
	lowerLimit(t, &maxHTMLBytes, 2*(each+added)+each+added/2)
	var text strings.Builder
	for n := 1; n <= 6; n++ {
		text.WriteString(unit(n))
	}
	item := bodyItem(text.String())
	item.Link = itemLink
	return limitedItem(t, imp, item)
}

// longBasePath is the path of the longest link of an item that relative addresses are made from: with "https://old.example.net"
// in front of it, it is permPathBytes bytes.
func longBasePath() string {
	return "/" + strings.Repeat("a", permPathBytes-len("https://old.example.net")-2) + "/"
}

func TestLinkBaseCountsAsHTML(t *testing.T) {
	// A relative address is resolved against the link of the item, the base, and what results has the path of the base in front
	// of the written one: a short address can become long, and the work of resolving it is done again for every link. The length
	// of the base counts among the bytes of HTML that the conversion reads each time that an address without a scheme is
	// resolved, whatever the link becomes and however short the result is; an address with a scheme adds nothing. The block
	// whose link goes over the limit is a legacy block, and the conversion stops.
	path := longBasePath()
	para := func(n int) string { return wpPara(fmt.Sprintf(`<a href="%d">y</a>`, n)) }
	for _, tt := range []struct {
		name, itemLink, made string // made is what an address gets in front of it as an href
	}{
		{"a page of the site", "https://example.com" + path, path},
		{"a page of another site", "https://old.example.net" + path, "https://old.example.net" + path},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, res := linkBytesBody(t, linkImport(t), tt.itemLink, len(tt.itemLink), para)
			linked := func(n int) content.RichText {
				return content.RichText{rtText("y", rtLink(content.Link{Href: fmt.Sprintf("%s%d", tt.made, n)}))}
			}
			reason := fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes)
			// The third paragraph took the ID 2 before its link was over the limit.
			sameBlocks(t, res.Blocks, []content.Block{
				{Type: content.BlockParagraph, ID: bodyIDs[0], Text: linked(1)},
				{Type: content.BlockParagraph, ID: bodyIDs[1], Text: linked(2)},
				legacyBlock(bodyIDs[3], para(3), reason),
			})
			wantLines(t, c.imp.Report, bodyLines(legacyLine(reason, "/body/2"), stopLine("/body")))
			if !c.stopped {
				t.Error("the conversion did not stop")
			}
		})
	}

	// The count is the HTML of the six blocks and, for each of the six addresses without a scheme, the length of the base,
	// whether the link is to an entry, a medium or an address and however long the result is; an address with a scheme adds
	// nothing.
	about := "https://example.com/about/"
	for _, tt := range []struct {
		name, itemLink, href string
		counted              bool // the address has no scheme
	}{
		{"a relative address and a short link of the item", "https://example.com/", "%d", true},
		{"a relative link to an entry that pops the base", "https://example.com" + path, "../about/", true},
		{"a protocol-relative link to an entry", "https://example.com" + path, "//example.com/about/", true},
		{"a link to an entry by a path with a slash", "https://example.com" + path, "/about/", true},
		{"a relative link to a medium", "https://example.com" + path, "../wp-content/uploads/2026/10/photo-scaled.jpg", true},
		{"a long relative address that resolves to a shorter one", "https://example.com" + path, "../" + strings.Repeat("./", 20) + "about/", true},
		{"a link to an entry with the scheme", "https://example.com" + path, about, false},
		{"a link to an address with the scheme that is shorter", "https://example.com" + path, "https://example.com/category/news/?page=%d", false},
		{"a link to an address with the scheme and characters that are escaped", "https://example.com" + path, "https://example.com/日本語/?page=%d", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unit := func(n int) string {
				href := tt.href
				if strings.Contains(href, "%d") {
					href = fmt.Sprintf(href, n)
				}
				return wpPara(`<a href="` + href + `">y</a>`)
			}
			extra := 0
			if tt.counted {
				extra = len(tt.itemLink)
			}
			// The limit leaves room for all the blocks.
			c, res := linkBytesBody(t, linkImport(t), tt.itemLink, 10*len(tt.itemLink)+100, unit)
			if want := 6 * (blockHTMLBytes(t, unit(1)) + extra); len(res.Blocks) != 6 || c.stopped || c.htmlBytes != want {
				t.Errorf("the conversion gave %d blocks, stopped = %v and read %d bytes of HTML, want 6, false and %d", len(res.Blocks), c.stopped, c.htmlBytes, want)
			}
			wantLines(t, c.imp.Report, nil)
		})
	}

	// Links that pop the base resolve to short addresses, and count the base all the same: the conversion stops.
	t.Run("links to an entry that pop the base", func(t *testing.T) {
		imp := linkImport(t)
		itemLink := "https://example.com" + path
		pop := func(int) string { return wpPara(`<a href="../about/">y</a>`) }
		c, res := linkBytesBody(t, imp, itemLink, len(itemLink), pop)
		linked := content.RichText{rtText("y", rtLink(content.Link{Entry: linkTarget(t, imp, 12)}))}
		reason := fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes)
		sameBlocks(t, res.Blocks, []content.Block{
			{Type: content.BlockParagraph, ID: bodyIDs[0], Text: linked},
			{Type: content.BlockParagraph, ID: bodyIDs[1], Text: linked},
			legacyBlock(bodyIDs[3], pop(3), reason),
		})
		wantLines(t, c.imp.Report, bodyLines(legacyLine(reason, "/body/2"), stopLine("/body")))
	})

	// A link that is removed counts as the others do, and its note has the address as it is written, not the long one that it
	// was made.
	t.Run("the notes of links that are removed", func(t *testing.T) {
		imp := linkImport(t)
		text := wpPara(strings.Repeat(`<a href="?\">y</a>`, 3))
		item := bodyItem(text)
		item.Link = "https://example.com" + path
		c, _ := limitedItem(t, imp, item)
		note := bodyLine(`skipped: the link to "?\\" is removed: backslashes are not allowed -> /body/0`)
		wantLines(t, imp.Report, []string{note, note, note})
		if want := blockHTMLBytes(t, text) + 3*len(item.Link); c.htmlBytes != want {
			t.Errorf("the conversion read %d bytes of HTML, want %d", c.htmlBytes, want)
		}
	})
}

// withStackLimit runs f with the stack of a goroutine limited to 1 MiB, so that a walk that recurses as deep as the HTML does
// runs out of stack and crashes the test: the stack of Go grows to 1 GB, which a hundred thousand levels do not fill. No test
// of the package runs at the same time as another.
func withStackLimit(f func()) {
	old := debug.SetMaxStack(1 << 20)
	defer debug.SetMaxStack(old)
	f()
}

// TestRichTextDeep checks that nesting that is as deep as the text is long does not run out of stack: the HTML is read, and
// the text is made of it, with a stack that a walk of a hundred thousand levels would fill.
func TestRichTextDeep(t *testing.T) {
	for _, tt := range []struct {
		name, open, close string
		n                 int
	}{
		{"marks", "<b>", "</b>", 100000}, {"spans", "<span>", "</span>", 100000},
		{"links", `<a href="https://example.org/">`, "</a>", 100000}, {"unknown elements", "<x-y>", "</x-y>", 100000},
		{"sub and sup", "<sub><sup>", "</sup></sub>", 50000},
		// The parser limits the work, which grows with the square of the depth of divs.
		{"blocks", "<div>", "</div>", 1000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			markup := strings.Repeat(tt.open, tt.n) + "x" + strings.Repeat(tt.close, tt.n)
			c := richConv(linkImport(t), "", nil)
			root := richRoot(t, markup)
			var got content.RichText
			var err error
			withStackLimit(func() { got, err = c.richText(root, false, noteAt{}) })
			if err != nil {
				t.Fatalf("richText = %v", err)
			}
			checkRichText(t, tt.name, got)
			if len(got) != 1 || got[0].Text != "x" {
				t.Errorf("richText = %+v, want the text x", got)
			}
		})
	}
}

// TestRichTextMany checks that many pieces of text are joined without work that grows with the square: with twice the pieces,
// the bytes that are allocated are twice, not four times.
func TestRichTextMany(t *testing.T) {
	allocated := func(n int) uint64 {
		root := richRoot(t, strings.Repeat("<b>a</b><strong>b</strong>", n))
		c := richConv(linkImport(t), "", nil)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := c.richText(root, false, noteAt{})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatalf("richText = %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("richText has %d pieces, want one", len(got))
		}
		if len(got[0].Text) != 2*n {
			t.Fatalf("the piece has %d bytes, want %d", len(got[0].Text), 2*n)
		}
		checkRichText(t, "many", got)
		return after.TotalAlloc - before.TotalAlloc
	}
	small, big := allocated(5000), allocated(10000)
	if big > 3*small {
		t.Errorf("10000 pieces allocated %d bytes and 5000 allocated %d: the work does not grow in line with the pieces", big, small)
	}
}

func TestRichTextBudget(t *testing.T) {
	// The text of a rich text has at most maxRichBytes bytes, counting richPieceBytes more for each piece of text and each
	// break, so that one paragraph of many pieces does not take the memory of all of them.
	reasonFor := func(max int) string {
		return fmt.Sprintf("the text has more than %d bytes, counting %d for each piece of text and each break", max, richPieceBytes)
	}
	// over reports whether the text of markup is more than max, which the budget is lowered to, allows.
	over := func(t *testing.T, markup string, verse bool, max int) bool {
		t.Helper()
		lowerLimit(t, &maxRichBytes, max)
		_, err := richConv(linkImport(t), "", nil).richText(richRoot(t, markup), verse, noteAt{})
		var le *legacyError
		switch {
		case err == nil:
			return false
		case errors.As(err, &le) && le.reason == reasonFor(max):
			return true
		}
		t.Fatalf("richText(%q) = %v", markup, err)
		return false
	}

	t.Run("the limit is the size of a string", func(t *testing.T) {
		if maxRichBytes != content.MaxTextBytes || richPieceBytes != 20 {
			t.Errorf("the limit is %d bytes with %d for each piece, want %d with 20", maxRichBytes, richPieceBytes, content.MaxTextBytes)
		}
	})

	t.Run("the most that fits", func(t *testing.T) {
		for _, tt := range []struct {
			name, in string
			verse    bool
			max      int
			want     bool
		}{
			{"four pieces", "<b>a</b><i>b</i><u>c</u><s>d</s>", false, 103, false},
			{"five pieces", "<b>a</b><i>b</i><u>c</u><s>d</s><sub>e</sub>", false, 103, true},
			{"two breaks and three pieces", "a<br>b<br>c", false, 103, false},
			{"a piece more", "a<br>b<br>c<b>d</b>", false, 103, true},
			{"one piece of the most bytes", strings.Repeat("a", 83), false, 103, false},
			{"one piece of a byte more", strings.Repeat("a", 84), false, 103, true},
			{"breaks only", strings.Repeat("<br>", 5), false, 103, false},
			{"a break more", strings.Repeat("<br>", 6), false, 103, true},
			// The lines of a verse are pieces and breaks, though it is a piece of text.
			{"a verse of four lines", "a\nb\nc\nd", true, 87, false},
			{"a verse of a line more", "a\nb\nc\nd\ne", true, 87, true},
			{"a verse with CR LF and CR", "a&#13;&#10;b&#13;c", true, 66, false},
			{"a verse with CR LF and CR, over", "a&#13;&#10;b&#13;c", true, 65, true},
			{"lines that are not a verse are white space", "a\nb\nc\nd\ne", false, 29, false},
		} {
			t.Run(tt.name, func(t *testing.T) {
				if got := over(t, tt.in, tt.verse, tt.max); got != tt.want {
					t.Errorf("richText(%q) is over the limit %d = %v, want %v", tt.in, tt.max, got, tt.want)
				}
			})
		}
	})

	t.Run("a text of many pieces", func(t *testing.T) {
		// 24966 times a piece of a letter and a piece of a space fill the limit of a string, and one more does not fit.
		for _, tt := range []struct {
			n    int
			want bool
		}{{24966, false}, {24967, true}} {
			if got := over(t, strings.Repeat("<b>x</b> ", tt.n), false, content.MaxTextBytes); got != tt.want {
				t.Errorf("%d times a letter and a space: over the limit = %v, want %v", tt.n, got, tt.want)
			}
		}
	})

	t.Run("a paragraph of many pieces is a legacy block", func(t *testing.T) {
		text := wpPara(strings.Repeat("<b>x</b> ", 30000))
		res, imp := textBody(t, bodySite(false), nil, text)
		reason := reasonFor(content.MaxTextBytes)
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[1], text, reason)})
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
	})

	t.Run("a verse of many lines is a legacy block", func(t *testing.T) {
		text := wpBlock("verse", "", "<pre class=\"wp-block-verse\">"+strings.Repeat("a\n", 60000)+"</pre>")
		res, imp := textBody(t, bodySite(false), nil, text)
		reason := reasonFor(content.MaxTextBytes)
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[1], text, reason)})
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
	})

	t.Run("the label of a button", func(t *testing.T) {
		lowerLimit(t, &maxRichBytes, 50)
		c := richConv(linkImport(t), "", nil)
		_, err := c.plainText(richRoot(t, "<b>a</b><i>b</i><u>c</u>"), noteAt{})
		var le *legacyError
		if !errors.As(err, &le) || le.reason != reasonFor(50) {
			t.Errorf("plainText = %v, want the reason %q", err, reasonFor(50))
		}
	})
}

func FuzzRichText(f *testing.F) {
	for _, seed := range []string{
		"", "text", "<b>a<i>b</i></b>", "a<br>b", `<a href="/about/#x" target=_blank rel="nofollow ugc">a</a>`, `<a href="javascript:x">a</a>`,
		`<mark class="has-inline-color has-vivid-red-color" style="color:#cf2e2e;background-color:rgba(0,0,0,0)">a</mark>`,
		`<span class="marker plain">a</span>`, "<sup>a<sub>b</sub></sup>", "<div>a<p>b</p></div>", "<script>x</script>y", "<img src=x>",
		"<ul><li>a</li></ul>", "<svg><a href=x>y</a></svg>", "a\u00a0 b\u2028c\v", "<table><tr><td>x", "&amp;&lt;&#0;&#x110000;", "&#X0C", "a&#xB;b&#x85;c&#x2029;d",
		"<a><a><b>x</a></b>", "<span style='text-decoration:underline;color:#fff'>a</span>", "\xff\xfe<b>\xc3</b>",
		"<a href=\"https://example.com/?p=11#" + "a\x00b\">x</a>", "<mark style='color:red'>", `<a href="#日本語">x</a>`,
		// Shortcodes in the text, which WordPress runs, and the ones that are escaped or are no shortcodes.
		"a [foo] b", "[[foo]] [x] [PR] [1] [注1]", "<b>[a]</b> [b c=\"d\"]e[/b]", "[[a]]]]", "[ [] [a", "[a][b][c][a]\n[[d]]",
	} {
		f.Add(seed, false)
		f.Add(seed, true)
	}
	imp := linkImport(f)
	f.Fuzz(func(t *testing.T, markup string, verse bool) {
		nodes, err := parseIn("div", markup)
		if err != nil {
			t.Skip()
		}
		root := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "div"}
		for _, n := range nodes {
			root.AppendChild(n)
		}
		c := richConv(imp, "https://example.com/2026/10/hello/", richSettings())
		got, err := c.richText(root, verse, noteAt{})
		if err != nil {
			var le *legacyError
			if !errors.As(err, &le) {
				t.Errorf("richText(%q) = %v, want a legacy error", markup, err)
			}
			return
		}
		norm := slices.Clone(got)
		norm.Normalize()
		if err := norm.Validate(); err != nil {
			t.Errorf("the text of %q does not pass Validate: %v", markup, err)
		}
	})
}

func TestBodyWithRichText(t *testing.T) {
	imp := linkImport(t)
	item := bodyItem("")
	item.Link = "https://example.com/2026/10/hello/"
	item.Content = strings.Join([]string{
		`<!-- wp:paragraph {"className":"is-style-fancy lead extra","textColor":"vivid-red","style":{"typography":{"fontStyle":"italic"}}} -->`,
		`<p id="intro" class="has-text-color">Hello <a href="https://example.com/about/#team" target="_blank">About</a> <kbd>x</kbd></p>`,
		`<!-- /wp:paragraph -->`,
		`<!-- wp:paragraph --><p id="intro">Again <img src="x.png"> <kbd>y</kbd></p><!-- /wp:paragraph -->`,
		`<!-- wp:paragraph --><p id="intro">Third <mark class="has-inline-color has-gone-color">z</mark></p><!-- /wp:paragraph -->`,
	}, "\n")
	site := bodySite(false)
	res, err := imp.Body(BodyOptions{Site: site, Settings: styleSettings(), Item: item, Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatal(err)
	}
	checkBody(t, imp, site, res)
	entry := linkTarget(t, imp, 12)
	want := []content.Block{
		{
			Type: content.BlockParagraph, ID: bodyIDs[0], Anchor: "intro", ClassName: "lead", Variant: "fancy-theme",
			Style: content.Style{TextColor: content.Color{Preset: "red"}},
			Text: content.RichText{
				rtText("Hello "), rtText("About", rtLink(content.Link{Entry: entry, Anchor: "team", NewTab: true})), rtText(" x"),
			},
		},
		legacyBlock(bodyIDs[2], `<!-- wp:paragraph --><p id="intro">Again <img src="x.png"> <kbd>y</kbd></p><!-- /wp:paragraph -->`,
			"the text has <img>, which rich text cannot have"),
		{
			Type: content.BlockParagraph, ID: bodyIDs[3], Text: content.RichText{rtText("Third z")},
		},
	}
	sameBlocks(t, res.Blocks, want)
	wantLines(t, imp.Report, bodyLines(
		`skipped: the class "extra" is not imported: whiteroom blocks have one class name -> /body/0`,
		`skipped: lost style.typography.fontStyle of the block "core/paragraph"; whiteroom has no such settings -> /body/0`,
		`changed: kept only the text of <kbd> -> /body/0`,
		legacyLine("the text has <img>, which rich text cannot have", "/body/1"),
		`skipped: the anchor "intro" is not imported: another block has it -> /body/2`,
		`skipped: the color preset "gone" is not imported: blocks.presets.color maps it to nothing -> /body/2`,
		`changed: kept only the text of <mark> -> /body/2`,
	))
}
