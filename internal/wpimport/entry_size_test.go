package wpimport

import (
	"encoding/json/v2"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// What a bound may be more than the JSON, for each block, piece of text and mark of what it is of, and for the rest of a
// revision. They are those of entry_size.go with room to spare: the tests find a bound that is too small, and one that adds
// what it should not.
const (
	slackBlock    = 64
	slackPiece    = 32
	slackMark     = 8
	slackRevision = 64
)

// boundShape is what a bound is more than the JSON by, for blocks.
type boundShape struct {
	blocks, pieces, marks int
	texts                 []content.RichText
}

func (s *boundShape) add(bs content.Blocks) {
	for i := range bs {
		b := &bs[i]
		s.blocks++
		s.richText(b.Text)
		s.richText(b.Attrs.Caption)
		s.richText(b.Attrs.Citation)
		s.add(b.Children)
	}
}

func (s *boundShape) richText(rt content.RichText) {
	if len(rt) > 0 {
		s.texts = append(s.texts, rt)
	}
	s.pieces += len(rt)
	for i := range rt {
		s.marks += len(rt[i].Marks)
	}
}

func (s boundShape) slack() int {
	return s.blocks*slackBlock + s.pieces*slackPiece + s.marks*slackMark
}

// checkBound checks the bounds of bs, as blocks and as the body of a revision: they are at least the length of the JSON,
// and more than it by at most the slack of the shape of bs.
func checkBound(t *testing.T, name string, bs content.Blocks) {
	t.Helper()
	var shape boundShape
	shape.add(bs)
	data, err := bs.MarshalJSON()
	if err != nil {
		t.Fatalf("%s: MarshalJSON = %v", name, err)
	}
	got := blocksBound(bs)
	if got < len(data) || got > len(data)+shape.slack()+2 {
		t.Errorf("%s: blocksBound = %d for JSON of %d bytes, want it between that and %d more", name, got, len(data), shape.slack()+2)
	}
	// The text of a block is bounded by the bound of its own: the rest of a block is more than it needs, which hides a text
	// that is bounded too low.
	for i, rt := range shape.texts {
		var texts boundShape
		texts.richText(rt)
		data, err := rt.MarshalJSON()
		if err != nil {
			t.Fatalf("%s: MarshalJSON of the text %d = %v", name, i, err)
		}
		if got := richTextBound(rt); got < len(data) || got > len(data)+texts.slack()+2 {
			t.Errorf("%s: richTextBound = %d for JSON of %d bytes (%s), want it between that and %d more", name, got, len(data), data, texts.slack()+2)
		}
	}

	rev := content.Revision{Title: "Title", Slug: "slug", Excerpt: "Excerpt\nwith lines", Body: bs}
	data, err = rev.MarshalJSON()
	if err != nil {
		t.Fatalf("%s: MarshalJSON of a revision = %v", name, err)
	}
	rest, blocks := revisionBound(&rev)
	if len(blocks) != len(bs) {
		t.Fatalf("%s: revisionBound gave %d bounds of blocks for %d blocks", name, len(blocks), len(bs))
	}
	sum := rest
	for _, n := range blocks {
		sum += n
	}
	if sum < len(data) || sum > len(data)+shape.slack()+slackRevision {
		t.Errorf("%s: revisionBound adds up to %d for JSON of %d bytes, want it between that and %d more", name, sum, len(data), shape.slack()+slackRevision)
	}
}

func TestJSONStringLen(t *testing.T) {
	var ascii strings.Builder
	for c := range 0x80 {
		ascii.WriteByte(byte(c))
	}
	for _, s := range []string{
		"", "abc", `"`, `\`, `a"b\c`, "\n\r\t", ascii.String(), "<>&", string(rune(0x2028)) + string(rune(0x2029)), string(rune(0x7f)),
		string(rune(0x8)) + string(rune(0xc)) + string(rune(0x1f)) + string(rune(0)), "日本語😀é", strings.Repeat(`"`, 1000),
		string(rune(0x85)) + string(rune(0xfeff)) + string(rune(0xfffd)),
	} {
		want, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := jsonStringLen(s); got != len(want) {
			t.Errorf("jsonStringLen(%q) = %d, want %d: %s", s, got, len(want), want)
		}
	}
}

// boundGen makes blocks from a seed, with all that the JSON of a block can have, and the same link under the pieces that
// follow one another, as a conversion of HTML makes them.
type boundGen struct {
	r     *rand.Rand
	depth int
}

var boundTexts = []string{
	"a", "bc", "日本", `"`, `\`, "\n", "\t", "<", ">", "&", "😀", " ", "é", "/", string(rune(0x2028)), string(rune(0x8)),
	string(rune(0x7f)), string(rune(0x1f)),
}

func (g *boundGen) text() string {
	var b strings.Builder
	for range g.r.IntN(5) {
		b.WriteString(boundTexts[g.r.IntN(len(boundTexts))])
	}
	return b.String()
}

func (g *boundGen) link() *content.Link {
	l := &content.Link{NewTab: g.r.IntN(2) == 0}
	switch g.r.IntN(4) {
	case 0:
		l.Href = "/" + g.text() + "x"
	case 1:
		l.Entry = "ent_" + g.text()
	case 2:
		l.Media = "med_" + g.text()
	case 3:
		l.Anchor = "a" + g.text()
	}
	for _, rel := range []content.Rel{content.RelNofollow, content.RelSponsored, content.RelUGC} {
		if g.r.IntN(3) == 0 {
			l.Rel = append(l.Rel, rel)
		}
	}
	return l
}

func (g *boundGen) highlight() content.Highlight {
	switch g.r.IntN(3) {
	case 0:
		return content.Highlight{Variant: "marker" + g.text()}
	case 1:
		return content.Highlight{Color: content.Color{Preset: "accent"}, BackgroundColor: content.Color{Value: "#fea" + g.text()}}
	}
	return content.Highlight{BackgroundColor: content.Color{Preset: g.text() + "p", Value: g.text() + "v"}}
}

func (g *boundGen) richText() content.RichText {
	var rt content.RichText
	var link *content.Link
	for range g.r.IntN(7) {
		if g.r.IntN(6) == 0 {
			rt = append(rt, rtBreak())
			continue
		}
		var marks []content.Mark
		for _, m := range []content.Mark{rtBold, rtItalic, rtUnderline, rtStrike, rtCode, rtSub, rtSup} {
			if g.r.IntN(4) == 0 {
				marks = append(marks, m)
			}
		}
		switch g.r.IntN(5) {
		case 0:
			link = g.link()
			marks = append(marks, content.Mark{Type: content.MarkLink, Link: link})
		case 1:
			if link != nil {
				marks = append(marks, content.Mark{Type: content.MarkLink, Link: link})
			}
		case 2:
			marks = append(marks, rtHighlight(g.highlight()))
		}
		rt = append(rt, rtText(g.text()+"x", marks...))
	}
	return rt
}

func (g *boundGen) blocks(n int) content.Blocks {
	var out content.Blocks
	for range n {
		out = append(out, g.block())
	}
	return out
}

func (g *boundGen) paragraph() content.Block {
	return content.Block{Type: content.BlockParagraph, ID: "b_" + g.text(), Text: g.richText()}
}

func (g *boundGen) block() content.Block {
	b := g.paragraph()
	if g.r.IntN(3) == 0 {
		b.ClassName, b.Anchor, b.Variant = "c"+g.text(), "a"+g.text(), "v"+g.text()
	}
	if g.r.IntN(4) == 0 {
		b.Style = content.Style{TextColor: content.Color{Preset: "accent"}, Padding: content.Sides{Top: content.Length{Value: "1" + g.text()}}}
	}
	deep := g.depth >= 4
	switch g.r.IntN(13) {
	case 0:
		b.Type, b.Attrs.Level, b.Attrs.TextAlign = content.BlockHeading, 2+g.r.IntN(5), "center"
	case 1:
		b.Type, b.Text = content.BlockList, nil
		b.Attrs.Ordered, b.Attrs.Start = true, 1+g.r.IntN(5)
		for range 1 + g.r.IntN(3) {
			b.Children = append(b.Children, content.Block{Type: content.BlockListItem, Children: content.Blocks{g.paragraph()}})
		}
	case 2:
		b.Type, b.Text = content.BlockQuote, nil
		b.Children = content.Blocks{g.paragraph()}
		b.Attrs.Citation = g.richText()
	case 3:
		b.Type, b.Text = content.BlockImage, nil
		b.Attrs.Media, b.Attrs.Alt, b.Attrs.Caption, b.Attrs.Link = "med_"+g.text(), g.text(), g.richText(), g.link()
	case 4:
		b.Type, b.Text = content.BlockTable, nil
		b.Attrs.Caption = g.richText()
		for range 1 + g.r.IntN(3) {
			row := content.Block{Type: content.BlockTableRow, Attrs: content.Attrs{Section: "body"}}
			for range 1 + g.r.IntN(3) {
				row.Children = append(row.Children, content.Block{Type: content.BlockTableCell, Text: g.richText(), Attrs: content.Attrs{Colspan: 2}})
			}
			b.Children = append(b.Children, row)
		}
	case 5:
		b.Type, b.Text, b.Code, b.Attrs.Language = content.BlockCode, nil, g.text()+g.text(), "go"
	case 6:
		b.Type, b.Text = content.BlockGroup, nil
		b.Attrs.Layout = content.Layout{Type: "row", Justify: "center", NoWrap: true}
		if !deep {
			g.depth++
			b.Children = g.blocks(1 + g.r.IntN(3))
			g.depth--
		} else {
			b.Children = content.Blocks{g.paragraph()}
		}
	case 7:
		b.Type, b.Text = content.BlockColumns, nil
		for range 1 + g.r.IntN(3) {
			column := content.Block{Type: content.BlockColumn, Attrs: content.Attrs{Width: "50%"}, Children: content.Blocks{g.paragraph()}}
			b.Children = append(b.Children, column)
		}
	case 8:
		b.Type, b.Text = content.BlockLegacy, nil
		b.Attrs.HTML, b.Attrs.Reason = "<p>"+g.text()+"</p>", g.text()
	case 9:
		b.Type, b.Text = content.BlockButtons, nil
		b.Children = content.Blocks{{Type: content.BlockButton, Attrs: content.Attrs{Label: g.text(), Link: g.link()}}}
	case 10:
		b.Type, b.Text = content.BlockEmbed, nil
		b.Attrs.Provider, b.Attrs.URL, b.Attrs.Caption = "youtube", "https://example.com/"+g.text(), g.richText()
	case 11:
		b.Type, b.Text = content.BlockSpacer, nil
		b.Attrs.Height = content.Length{Preset: "m"}
	}
	return b
}

func TestBounds(t *testing.T) {
	t.Run("random blocks", func(t *testing.T) {
		for seed := range uint64(300) {
			g := &boundGen{r: rand.New(rand.NewPCG(seed, seed*7+1))}
			bs := g.blocks(1 + g.r.IntN(6))
			checkBound(t, "seed "+strconv.FormatUint(seed, 10), bs)
		}
	})

	t.Run("the bodies of the golden files", func(t *testing.T) {
		var files []string
		for _, dir := range []string{"out-blocks", "out-classic"} {
			found, err := filepath.Glob(filepath.Join("testdata", dir, "*.json"))
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, found...)
		}
		if len(files) < 20 {
			t.Fatalf("%d golden bodies, want 20 or more", len(files))
		}
		types := make(map[content.BlockType]bool)
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				More int            `json:"more"`
				Body content.Blocks `json:"body"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			checkBound(t, file, doc.Body)
			var walk func(bs content.Blocks)
			walk = func(bs content.Blocks) {
				for i := range bs {
					types[bs[i].Type] = true
					walk(bs[i].Children)
				}
			}
			walk(doc.Body)
		}
		for _, typ := range []content.BlockType{
			content.BlockParagraph, content.BlockHeading, content.BlockList, content.BlockQuote, content.BlockImage, content.BlockTable,
			content.BlockButtons, content.BlockCode, content.BlockEmbed, content.BlockColumns, content.BlockGroup, content.BlockLegacy,
		} {
			if !types[typ] {
				t.Errorf("no golden body has a %s block", typ)
			}
		}
	})

	t.Run("characters that are escaped", func(t *testing.T) {
		nasty := "\"\\\n\r\t" + string(rune(8)) + string(rune(12)) + string(rune(1)) + string(rune(0x1f)) + "<>&" + string(rune(0x7f)) +
			string(rune(0x2028)) + string(rune(0x2029)) + "日😀"
		link := &content.Link{Href: "/" + nasty, Rel: []content.Rel{content.RelNofollow}, NewTab: true}
		b := content.Block{
			Type: content.BlockParagraph, ID: nasty, Anchor: nasty,
			Text: content.RichText{
				rtText(nasty, content.Mark{Type: content.MarkLink, Link: link}, rtHighlight(content.Highlight{Variant: nasty})),
				rtBreak(),
				// A type that is not one is written as it is.
				{Type: content.InlineType(strings.Repeat("t", 300)), Text: "a"},
				rtText(nasty, rtHighlight(content.Highlight{Color: content.Color{Preset: nasty, Value: nasty}, BackgroundColor: content.Color{Value: nasty}})),
			},
		}
		quote := content.Block{
			Type: content.BlockQuote, Children: content.Blocks{b},
			Attrs: content.Attrs{Citation: content.RichText{rtText(nasty)}, Alt: nasty},
		}
		code := content.Block{Type: content.BlockCode, Code: nasty, Attrs: content.Attrs{Language: "x"}}
		checkBound(t, "nasty", content.Blocks{b, quote, code})
	})

	t.Run("long texts in every place", func(t *testing.T) {
		long := func(n int) content.RichText {
			return content.RichText{rtText(strings.Repeat("é", n), rtBold), rtBreak(), rtText(strings.Repeat("\"", n/2))}
		}
		item := content.Block{Type: content.BlockListItem, Children: content.Blocks{{Type: content.BlockParagraph, Text: long(1500)}}}
		cell := content.Block{Type: content.BlockTableCell, Text: long(1200)}
		bs := content.Blocks{
			{Type: content.BlockGroup, Children: content.Blocks{
				{Type: content.BlockQuote, Attrs: content.Attrs{Citation: long(1000)}, Children: content.Blocks{
					{Type: content.BlockParagraph, Text: long(2000)},
					{Type: content.BlockList, Children: content.Blocks{item}},
				}},
				{Type: content.BlockTable, Attrs: content.Attrs{Caption: long(800)}, Children: content.Blocks{
					{Type: content.BlockTableRow, Children: content.Blocks{cell}},
				}},
			}},
			{Type: content.BlockImage, Attrs: content.Attrs{Media: "med_1", Caption: long(900)}},
			{Type: content.BlockEmbed, Attrs: content.Attrs{URL: "https://example.com/", Caption: long(700)}},
			{Type: content.BlockCode, Code: strings.Repeat("c", 3000)},
		}
		checkBound(t, "long", bs)
	})

	t.Run("blocks nested as deep as they may be", func(t *testing.T) {
		inner := content.Block{Type: content.BlockParagraph, Text: content.RichText{rtText("deep", rtBold)}}
		for range content.MaxDepth - 1 {
			inner = content.Block{Type: content.BlockGroup, Children: content.Blocks{inner}}
		}
		checkBound(t, "deep", content.Blocks{inner})
	})

	t.Run("a revision with the other items", func(t *testing.T) {
		rev := content.Revision{
			Title: "T\"\\", Slug: "s", Excerpt: "e\nx", FeaturedImage: "med_1", Date: entryTime, Author: "aut_1", Sticky: true, Parent: "ent_1",
			Order: -3, Template: "wide", Terms: content.Terms{"category": {"trm_1", "trm_2"}, "tag": {"trm_3"}},
			SEO:    content.SEO{Title: "t", Description: "d", OGImage: "med_2", NoIndex: true, Canonical: "https://example.com/"},
			Fields: content.Fields{"a": []byte(`"value"`), "b": []byte(`{"x":[1,2,3]}`)},
		}
		data, err := rev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		rest, blocks := revisionBound(&rev)
		// The JSON has the body after the rest, with its key, if the revision has a body.
		if len(blocks) != 0 || rest < len(data)+len(`,"body":[]`) || rest > len(data)+slackRevision {
			t.Errorf("revisionBound = %d, %v for a revision of %d bytes", rest, blocks, len(data))
		}
	})
}

// TestBoundsOfAttributes checks the bound of the attributes of a mark by itself: a block has room to spare, in which too
// low a bound of them would not show.
func TestBoundsOfAttributes(t *testing.T) {
	nasty := "\"\\\n" + string(rune(1)) + "<日"
	for _, l := range []content.Link{
		{}, {Href: "/a"}, {Entry: "ent_1"}, {Media: "med_1"}, {Anchor: "top"}, {Href: "/a", NewTab: true}, {Href: nasty},
		{Href: "/a", Rel: []content.Rel{content.RelNofollow}}, {Entry: "ent_1", Rel: []content.Rel{content.RelNofollow, content.RelSponsored, content.RelUGC}},
		{Href: "h", Entry: "e", Media: "m", Anchor: "a", NewTab: true, Rel: []content.Rel{content.RelUGC}},
	} {
		data, err := json.Marshal(&l)
		if err != nil {
			t.Fatal(err)
		}
		// One comma that the last member does not have, and one for the items of rel.
		want := len(attrsKey) + len(data)
		if got := linkBound(&l); got < want || got > want+2 {
			t.Errorf("linkBound = %d for the attributes %s of %d bytes, want %d to %d", got, data, want, want, want+2)
		}
	}
	for _, h := range []content.Highlight{
		{}, {Variant: "marker"}, {Variant: nasty}, {Color: content.Color{Preset: "accent"}}, {Color: content.Color{Value: "#fff"}},
		{BackgroundColor: content.Color{Preset: "p", Value: "v"}},
		{Color: content.Color{Preset: "p", Value: "v"}, BackgroundColor: content.Color{Preset: "q", Value: "w"}},
	} {
		data, err := json.Marshal(&h)
		if err != nil {
			t.Fatal(err)
		}
		want := len(attrsKey) + len(data)
		if got := highlightBound(&h); got < want || got > want+3 {
			t.Errorf("highlightBound = %d for the attributes %s of %d bytes, want %d to %d", got, data, want, want, want+3)
		}
	}
}

// TestBoundConstants checks that each constant covers the keys and punctuation that it stands for.
func TestBoundConstants(t *testing.T) {
	for _, tt := range []struct {
		name  string
		bound int
		keys  []string
	}{
		// The key of the text, of the attributes with a caption and a citation, the brace that closes them, and a comma.
		{"block", boundBlock, []string{`,"content":`, `,"attrs":{`, `"caption":`, `,"citation":`, `}`, `,`}},
		{"piece", boundPiece, []string{`{"type":`, `,"text":`, `,"marks":[`, `]}`, `,`}},
		{"mark", boundMark, []string{`{"type":`, `}`, `,`}},
		{"body", boundBody, []string{`,"body":[`, `]`}},
	} {
		need := 0
		for _, k := range tt.keys {
			need += len(k)
		}
		if tt.bound < need {
			t.Errorf("the bound for a %s is %d, and its keys take %d", tt.name, tt.bound, need)
		}
	}
}

func TestBoundsCannotBeWritten(t *testing.T) {
	bad := string([]byte{0xff, 0xfe})
	if got := blockBound(&content.Block{Type: content.BlockCode, Code: bad}); got <= content.MaxDocumentBytes {
		t.Errorf("blockBound of a block that cannot be written = %d, want more than %d", got, content.MaxDocumentBytes)
	}
	rest, _ := revisionBound(&content.Revision{Title: bad})
	if rest <= content.MaxDocumentBytes {
		t.Errorf("revisionBound of a revision that cannot be written = %d, want more than %d", rest, content.MaxDocumentBytes)
	}
}

// bigParagraph makes a paragraph of 2n pieces of text, bold and not, that all have the mark, which holds one value as a
// conversion of an element a makes it: it is the mark that has the pointer, not a copy of it for each piece.
func bigParagraph(n int, mark content.Mark) content.Block {
	text := make(content.RichText, 0, 2*n)
	for range n {
		text = append(text, rtText("x", mark, rtBold), rtText("y", mark))
	}
	return content.Block{Type: content.BlockParagraph, ID: "b_big", Text: text}
}

func linkMark(href string) content.Mark {
	return content.Mark{Type: content.MarkLink, Link: &content.Link{Href: href}}
}

func highlightMark(variant string) content.Mark {
	return content.Mark{Type: content.MarkHighlight, Highlight: &content.Highlight{Variant: variant}}
}

// TestBoundsLargeParagraph finds the bound of a paragraph that has tens of thousands of pieces under a link with a long
// address, whose JSON is gigabytes, without writing it. A highlight is one value in the same way.
func TestBoundsLargeParagraph(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the JSON of the paragraph is more than an int holds")
	}
	const pieces = 24000
	for _, tt := range []struct {
		name string
		mark func(value string) content.Mark
		long int
	}{
		{"a link", linkMark, 200000},
		{"a highlight", highlightMark, 100000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The JSON with a value of a byte is small; the rest is the value, once for each piece.
			small, err := bigParagraph(pieces, tt.mark("a")).MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			want := len(small) + 2*pieces*(tt.long-1)
			block := bigParagraph(pieces, tt.mark(strings.Repeat("a", tt.long)))

			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			got := blockBound(&block)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)

			if want <= 4<<30 {
				t.Fatalf("the JSON of the paragraph is %d bytes, want more than 4 GiB", want)
			}
			if limit := slackBlock + 2*pieces*(slackPiece+2*slackMark); got < want || got > want+limit {
				t.Errorf("blockBound = %d, want between %d and %d", got, want, want+limit)
			}
			if elapsed > time.Second {
				t.Errorf("the count took %v, want much less than a second", elapsed)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
				t.Errorf("the count allocated %d bytes, want at most 16 MiB: it must not write the JSON", alloc)
			}
			rev := content.Revision{Body: content.Blocks{block}}
			rest, blocks := revisionBound(&rev)
			if len(blocks) != 1 || blocks[0] != got || rest > slackRevision+len(`{"schemaVersion":1}`) {
				t.Errorf("revisionBound = %d, %v, want a small rest and %d", rest, blocks, got)
			}
		})
	}
}

// TestBoundsLinksThatAreNotShared checks the count of links that are not one value: each address is counted for its
// pieces, also where one follows another, and where the same link comes again after another.
func TestBoundsLinksThatAreNotShared(t *testing.T) {
	// The addresses have different lengths: a bound that takes the length of an earlier link is too low.
	href := func(i int) string { return "/" + strings.Repeat(strconv.Itoa(i%10), 1000+10*i) }
	var apart, again, same content.RichText
	shared := &content.Link{Href: href(7)}
	other := &content.Link{Href: href(280)}
	for i := range 300 {
		apart = append(apart, rtText("x", rtLink(content.Link{Href: href(i)})))
		// A, B, A, B: a link comes again after another.
		l := shared
		if i%2 == 1 {
			l = other
		}
		again = append(again, rtText("x", content.Mark{Type: content.MarkLink, Link: l}))
		// The same link with a piece between, which has no link.
		same = append(same, rtText("x", content.Mark{Type: content.MarkLink, Link: shared}), rtText("y"))
	}
	for name, text := range map[string]content.RichText{"apart": apart, "again": again, "same": same} {
		checkBound(t, name, content.Blocks{{Type: content.BlockParagraph, Text: text}})
	}
	block := content.Block{Type: content.BlockParagraph, Text: apart}
	if got := blockBound(&block); got < 300*1000 {
		t.Errorf("blockBound = %d for 300 addresses of 1000 bytes or more", got)
	}
}

func TestBoundsHighlightShared(t *testing.T) {
	h := &content.Highlight{Variant: strings.Repeat("v", 5000), BackgroundColor: content.Color{Value: strings.Repeat("c", 3000)}}
	var text content.RichText
	for range 500 {
		text = append(text, rtText("x", content.Mark{Type: content.MarkHighlight, Highlight: h}))
	}
	bs := content.Blocks{{Type: content.BlockParagraph, Text: text}}
	checkBound(t, "highlight", bs)
}
