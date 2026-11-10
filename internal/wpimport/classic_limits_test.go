package wpimport

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// classicTimeLimit is the time that the conversion of one input of TestClassicBodyLimits may take. The inputs are some megabytes
// long, and the conversion takes some tenths of a second, or some seconds with -race: time that grew with the square of the length
// of an input would be minutes or hours.
func classicTimeLimit() time.Duration {
	if raceEnabled {
		return 240 * time.Second
	}
	return 60 * time.Second
}

// limitedClassic is limitedBody, and fails the test if the conversion takes more than classicTimeLimit.
func limitedClassic(t *testing.T, imp *Import, text string) (*bodyConv, *BodyResult) {
	t.Helper()
	return limitedClassicItem(t, imp, bodyItem(text))
}

func limitedClassicItem(t *testing.T, imp *Import, item *wxr.Item) (*bodyConv, *BodyResult) {
	t.Helper()
	start := time.Now()
	c, res := limitedItem(t, imp, item)
	if d := time.Since(start); d > classicTimeLimit() {
		t.Errorf("the conversion of %d bytes took %v, more than %v: the time must be in proportion to the length", len(item.Content), d, classicTimeLimit())
	}
	return c, res
}

// textLines returns the texts of the paragraphs in bs, in order, without the line breaks between them.
func textLines(bs content.Blocks) []string {
	var out []string
	for _, b := range bs {
		for _, in := range b.Text {
			if in.Type == content.InlineText {
				out = append(out, in.Text)
			}
		}
	}
	return out
}

// TestClassicBodyLimits converts the contents that are the most that the HTML of the classic editor can be, or more: the
// conversion keeps to the limits (limitedItem), says what it left out, and takes time that is in proportion to the length of the
// content. The sizes are those of the limits where that is quick, and the limits are lowered where it is not.
func TestClassicBodyLimits(t *testing.T) {
	hasLine := func(t *testing.T, imp *Import, short string) {
		t.Helper()
		if lines := reportText(t, imp.Report); !strings.Contains(lines, bodyLine(short)+"\n") {
			t.Errorf("the report has no line %q:\n%.2000s", bodyLine(short), lines)
		}
	}
	hasNote := func(t *testing.T, imp *Import, part string) {
		t.Helper()
		if lines := reportText(t, imp.Report); !strings.Contains(lines, part) {
			t.Errorf("the report has no line with %q:\n%.2000s", part, lines)
		}
	}
	tooLong := "is not imported: its HTML is too long"
	if raceEnabled {
		// The same shapes with fewer blocks: -race makes the conversion several times slower.
		lowerLimit(t, &maxWPBlocks, 3*content.MaxBlocks/2)
	}

	t.Run("a content over the size limit is not taken", func(t *testing.T) {
		lowerLimit(t, &maxBodyBytes, 1<<20)
		const para = "A paragraph of text.\n\n"
		text := strings.Repeat(para, maxBodyBytes/len(para)+1)
		imp := bodyImport()
		c, res := limitedClassic(t, imp, text)
		sameBlocks(t, res.Blocks, nil)
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf("skipped: the content is not imported: it is too large: %d bytes (max %d) -> /body", len(text), maxBodyBytes)))
		if c.blocks != 0 || c.htmlBytes != 0 {
			t.Errorf("the conversion read %d blocks and %d bytes of a content that it does not take", c.blocks, c.htmlBytes)
		}
	})

	t.Run("paragraphs up to the size limit: wpautop makes them longer, and the HTML that is read stops the conversion", func(t *testing.T) {
		lowerLimit(t, &maxBodyBytes, 256<<10)
		lowerLimit(t, &maxHTMLBytes, 256<<10)
		lowerLimit(t, &maxBlockHTMLBytes, 32<<10)
		para := strings.Repeat("a line of the paragraph ", 4) + "\n\n"
		count := maxBodyBytes / len(para)
		text := strings.Repeat(para, count)
		imp := bodyImport()
		c, res := limitedClassic(t, imp, text)
		if !c.stopped || c.htmlBytes <= maxHTMLBytes || len(res.Blocks) == 0 || len(res.Blocks) >= count {
			t.Fatalf("the conversion stopped = %v, read %d bytes of HTML (max %d) and made %d blocks of %d paragraphs, want a stop after some", c.stopped,
				c.htmlBytes, maxHTMLBytes, len(res.Blocks), count)
		}
		// The part that went over the limit is a legacy block of its HTML, and the last.
		last := res.Blocks[len(res.Blocks)-1]
		if reason := fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes); last.Type != content.BlockLegacy || last.Attrs.Reason != reason {
			t.Errorf("the last block is %+v, want a legacy block with the reason %q", last, reason)
		}
		hasLine(t, imp, stopLine("/body"))
	})

	t.Run("a content over the size of a block is cut before wpautop, and nothing is lost", func(t *testing.T) {
		lowerLimit(t, &maxBlockHTMLBytes, 32<<10)
		lengths := autopLengths(t)
		var lines []string
		var text strings.Builder
		for i := range 8000 {
			line := fmt.Sprintf("line %d of the text", i)
			lines = append(lines, line)
			text.WriteString(line + "\n")
			if i%7 == 6 {
				text.WriteString("\n")
			}
		}
		imp := bodyImport()
		c, res := limitedClassic(t, imp, text.String())
		if len(*lengths) < text.Len()/maxBlockHTMLBytes || slicesMax(*lengths) > maxBlockHTMLBytes {
			t.Errorf("wpautop was given %d texts, the longest of %d bytes, want at least %d and at most %d bytes", len(*lengths), slicesMax(*lengths),
				text.Len()/maxBlockHTMLBytes, maxBlockHTMLBytes)
		}
		if got := textLines(res.Blocks); !slices.Equal(got, lines) {
			t.Errorf("the paragraphs have %d lines of text, want the %d lines of the content, in order", len(got), len(lines))
		}
		if c.stopped {
			t.Error("the conversion stopped")
		}
		wantLines(t, imp.Report, nil)
	})

	t.Run("a line longer than a block may be cannot be cut, and is not imported", func(t *testing.T) {
		line := strings.Repeat("a", maxBlockHTMLBytes+1<<20)
		imp := bodyImport()
		c, res := limitedClassic(t, imp, "before\n"+line+"\nafter")
		sameBlocks(t, res.Blocks, []content.Block{paragraphBlock(bodyIDs[0], "before"), paragraphBlock(bodyIDs[1], "after")})
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf("skipped: a part of the HTML of the classic editor is not imported: its HTML is too long: %d bytes (max %d) -> /body",
			len(line)+1, content.MaxTextBytes)))
		// The part that is not read does not count among the HTML that is read.
		if c.htmlBytes > 100 {
			t.Errorf("the conversion read %d bytes of HTML, want only the lines around the long one", c.htmlBytes)
		}
	})

	t.Run("more paragraphs than the conversion converts", func(t *testing.T) {
		imp := bodyImport()
		c, res := limitedClassic(t, imp, strings.Repeat("x\n\n", maxWPBlocks+1))
		if !c.stopped || c.blocks != maxWPBlocks+1 || len(res.Blocks) != content.MaxBlocks {
			t.Errorf("the conversion stopped = %v with %d blocks counted and made %d blocks, want true, %d and %d", c.stopped, c.blocks, len(res.Blocks),
				maxWPBlocks+1, content.MaxBlocks)
		}
		hasLine(t, imp, stopLine("/body"))
		hasLine(t, imp, fmt.Sprintf("skipped: the blocks from /body/%d on are not imported: a document has at most %d blocks -> /body", content.MaxBlocks, content.MaxBlocks))
	})

	t.Run("more elements that make no block than the conversion converts", func(t *testing.T) {
		// An empty heading makes no block and a note, and a body can have millions of them.
		imp := bodyImport()
		c, res := limitedClassic(t, imp, strings.Repeat("<h2></h2>\n", maxWPBlocks+10)+"after")
		if !c.stopped || c.blocks != maxWPBlocks+1 || len(res.Blocks) != 0 {
			t.Errorf("the conversion stopped = %v with %d blocks counted and made %d blocks, want true, %d and 0", c.stopped, c.blocks, len(res.Blocks), maxWPBlocks+1)
		}
		hasLine(t, imp, stopLine("/body"))
		if n := len(imp.Report.Entries()); n > maxWPBlocks+2 {
			t.Errorf("the report has %d entries, want at most %d", n, maxWPBlocks+2)
		}

		// A paragraph of nothing but an element that has no text counts as well, and so does a script in one.
		for name, para := range map[string]string{"an empty element": "<p><b></b></p>\n", "a script": "<p><script></script></p>\n", "a place": "<p><a name=\"a\"></a></p>\n"} {
			imp := bodyImport()
			c, res := limitedClassic(t, imp, strings.Repeat(para, maxWPBlocks+10)+"after")
			if !c.stopped || c.blocks != maxWPBlocks+1 || len(res.Blocks) != 0 {
				t.Errorf("%s: the conversion stopped = %v with %d blocks counted and made %d blocks, want true, %d and 0", name, c.stopped, c.blocks, len(res.Blocks), maxWPBlocks+1)
			}
			hasLine(t, imp, stopLine("/body"))
		}
	})

	t.Run("ten thousand page breaks", func(t *testing.T) {
		imp := bodyImport()
		c, res := limitedClassic(t, imp, strings.Repeat("page of text\n<!--nextpage-->\n", 10000)+"last")
		if c.stopped || len(res.Blocks) != content.MaxBlocks {
			t.Errorf("the conversion stopped = %v and made %d blocks, want false and %d", c.stopped, len(res.Blocks), content.MaxBlocks)
		}
		hasLine(t, imp, "skipped: 10000 page breaks are removed; whiteroom has no page breaks -> /body")
	})

	t.Run("elements nested so deep that the parser gives up", func(t *testing.T) {
		nest := func(n int) string { return strings.Repeat("<div>", n) + "x" + strings.Repeat("</div>", n) }
		// 30000 levels are still short enough to be the HTML of a legacy block.
		imp := bodyImport()
		_, res := limitedClassic(t, imp, nest(30000))
		if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockLegacy || res.Blocks[0].Attrs.Reason != reasonComplex {
			t.Errorf("30000 nested elements became %.200v, want a legacy block of the HTML that is too complex to read", res.Blocks)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine(reasonComplex, "/body/0")))
		if !raceEnabled {
			// 100000 are not: their HTML is more than a legacy block may hold.
			imp = bodyImport()
			_, res = limitedClassic(t, imp, nest(100000))
			sameBlocks(t, res.Blocks, nil)
			hasNote(t, imp, "a part of the HTML of the classic editor "+tooLong)
		}
	})

	t.Run("more shortcodes than the limit", func(t *testing.T) {
		imp := bodyImport()
		c, res := limitedClassic(t, imp, strings.Repeat("[a]\n", maxWPBlocks+1))
		if !c.stopped || len(res.Blocks) != content.MaxBlocks || len(c.pieces) != maxWPBlocks {
			t.Errorf("the conversion stopped = %v, made %d blocks and %d pieces, want true, %d and %d", c.stopped, len(res.Blocks), len(c.pieces),
				content.MaxBlocks, maxWPBlocks)
		}
		hasLine(t, imp, fmt.Sprintf("review: the shortcodes and URLs after the first %d are left as text -> /body", maxWPBlocks))
	})

	t.Run("one shortcode over all of a long content", func(t *testing.T) {
		imp := bodyImport()
		_, res := limitedClassic(t, imp, "[box]\n"+strings.Repeat("a line of text in the box\n\n", 120000)+"[/box]")
		sameBlocks(t, res.Blocks, nil)
		hasNote(t, imp, "the shortcode [box] "+tooLong)
	})

	t.Run("shortcodes that are not closed, a million times", func(t *testing.T) {
		for _, tt := range []struct{ name, text string }{
			{"an opening bracket and a name", strings.Repeat("[foo ", 1000000)},
			{"attributes without the closing bracket", "[foo " + strings.Repeat(`a="1" `, 500000)},
			{"a name and attributes that are never closed, on lines", strings.Repeat("[foo a=\"1\"\n", 200000)},
		} {
			t.Run(tt.name, func(t *testing.T) {
				imp := bodyImport()
				_, res := limitedClassic(t, imp, tt.text)
				// All of it is one paragraph, which is more than a text may be.
				sameBlocks(t, res.Blocks, nil)
				hasNote(t, imp, tooLong)
			})
		}
	})

	t.Run("a hundred thousand lines of URLs", func(t *testing.T) {
		lines := 100000
		if raceEnabled {
			lines = maxWPBlocks + 10000
		}
		var text strings.Builder
		for i := range lines {
			fmt.Fprintf(&text, "https://example.org/page/%d\n", i)
		}
		imp := bodyImport()
		_, res := limitedClassic(t, imp, text.String())
		// The lines are one paragraph, which is more than a text may be, or a legacy block of its HTML if it is not.
		if len(res.Blocks) > 1 {
			t.Errorf("%d lines of URLs became %d blocks, want at most one", lines, len(res.Blocks))
		}
		hasLine(t, imp, fmt.Sprintf("review: the shortcodes and URLs after the first %d are left as text -> /body", maxWPBlocks))
	})

	t.Run("input that is slow for wpautop", func(t *testing.T) {
		for _, tt := range []struct{ name, text string }{
			{"a long run of white space", "a" + strings.Repeat(" ", 2<<20) + "b"},
			{"a line feed in a tag that never ends", "<" + strings.Repeat("\n", 1<<20)},
			{"a tag with line feeds in it, again and again", strings.Repeat("<a"+strings.Repeat("\n", 100)+">\n", 10000)},
			{"a line of one letter, again and again", strings.Repeat("a\n", 1<<18)},
			{"an element of the start of a paragraph, again and again", "<x" + strings.Repeat("<p ", 500000)},
			{"an element that is not closed, with paragraphs in it", "<script>" + strings.Repeat("<p>x</p>\n\n", 200000)},
		} {
			t.Run(tt.name, func(t *testing.T) {
				imp := bodyImport()
				limitedClassic(t, imp, tt.text)
			})
		}
		// Elements of code, again and again: each is a block of its own.
		imp := bodyImport()
		c, res := limitedClassic(t, imp, strings.Repeat("<pre>x</pre>", maxWPBlocks+2000))
		if !c.stopped || len(res.Blocks) != content.MaxBlocks {
			t.Errorf("the conversion stopped = %v and made %d blocks, want true and %d", c.stopped, len(res.Blocks), content.MaxBlocks)
		}
	})

	t.Run("an image of data that is longer than a legacy block may be", func(t *testing.T) {
		img := `<img src="data:image/png;base64,` + strings.Repeat("A", 2<<20) + `" alt="x">`
		imp := bodyImport()
		_, res := limitedClassic(t, imp, img)
		sameBlocks(t, res.Blocks, nil)
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf("skipped: the element <img> is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(img), content.MaxTextBytes)))

		// In a sentence, it makes the paragraph a legacy block, which is too long as well.
		para := "<p>before " + img + " after</p>"
		imp = bodyImport()
		_, res = limitedClassic(t, imp, "before "+img+" after")
		sameBlocks(t, res.Blocks, nil)
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf("skipped: the element <p> is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(para), content.MaxTextBytes)))
	})

	t.Run("the link of the item, and relative addresses in legacy blocks", func(t *testing.T) {
		// An address without a scheme counts the link of the item, which is as long as 8192 bytes, among the HTML that is read.
		lowerLimit(t, &maxHTMLBytes, 1<<21)
		item := bodyItem(strings.Repeat(`<dl><dt><a href="x/">a</a></dt></dl>`+"\n", 400))
		item.Link = "https://old.example.net" + longBasePath()
		imp := bodyImport()
		c, res := limitedClassicItem(t, imp, item)
		blocks := countOfType(res.Blocks, content.BlockLegacy)
		if !c.stopped || blocks < 200 || blocks >= 400 {
			t.Errorf("the conversion stopped = %v and made %d legacy blocks of 400 elements, want true and some 250", c.stopped, blocks)
		}
		hasLine(t, imp, stopLine("/body"))
	})
}
