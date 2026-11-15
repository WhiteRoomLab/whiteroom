package wpimport

import (
	"math"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The bounds of this file are of the JSON that MarshalJSON of the content package writes, which has no white space. They
// are found by counting, not by writing a block whole: a paragraph of tens of thousands of pieces of text under one link with
// a long address takes gigabytes as JSON. Each constant is a little more than what its part takes.
const (
	// boundBlock is what a block takes that its copy without the text, the blocks, the caption and the citation does not:
	// "content":, "attrs":{, "caption":, "citation": and the comma after the block.
	boundBlock = 64
	// boundPiece is what a piece of text takes besides the JSON strings of its type and text and its marks: {"type":,
	// "text":, "marks":[, ]} and the comma.
	boundPiece = 32
	// boundMark is {"type":, } and the comma of a mark, besides the string of its type and its attributes.
	boundMark = 12
	// boundBody is "body":[ and ] of the revision, besides the blocks.
	boundBody = 16
	// boundCap keeps the sums from overflowing: a bound is never more than it, which no document can have.
	boundCap = math.MaxInt / 4
)

// jsonExtra is how many more bytes than its own a byte takes in a string of JSON: the escapes of the quote, the backslash
// and the control characters. Nothing else is escaped.
var jsonExtra = func() (t [256]uint8) {
	for c := range 0x20 {
		t[c] = 5 // \u00XX
	}
	for _, c := range []byte{'"', '\\', '\n', '\r', '\t', '\b', '\f'} {
		t[c] = 1
	}
	return t
}()

// jsonStringLen returns how many bytes s takes as a string of JSON, with its quotes.
func jsonStringLen(s string) int {
	n := len(s) + 2
	for i := range len(s) {
		n += int(jsonExtra[s[i]])
	}
	return n
}

// jsonMember returns how many bytes a member of an object takes, with the comma after it, if its value takes n.
func jsonMember(key string, n int) int {
	return len(key) + 4 + n
}

// blocksBound returns a bound of the JSON of bs: never less than what MarshalJSON writes, and more by a few bytes for each
// block, piece of text and mark. A block is counted by the JSON of a copy that has no text, blocks, caption or citation,
// which is small, and by the bounds of those.
func blocksBound(bs content.Blocks) int {
	n := 2
	for i := range bs {
		n = min(n+blockBound(&bs[i]), boundCap)
	}
	return n
}

// blockBound returns the bound of the JSON of b and the comma after it.
func blockBound(b *content.Block) int {
	cp := *b
	cp.Text, cp.Children = nil, nil
	cp.Attrs.Caption, cp.Attrs.Citation = nil, nil
	data, err := cp.MarshalJSON()
	if err != nil {
		// What cannot be written does not fit.
		return boundCap
	}
	n := len(data) + boundBlock
	if len(b.Text) > 0 {
		n += richTextBound(b.Text)
	}
	if len(b.Attrs.Caption) > 0 {
		n += richTextBound(b.Attrs.Caption)
	}
	if len(b.Attrs.Citation) > 0 {
		n += richTextBound(b.Attrs.Citation)
	}
	if len(b.Children) > 0 {
		n += blocksBound(b.Children)
	}
	return min(n, boundCap)
}

// richTextBound returns the bound of the JSON of rt by adding up what each piece and mark takes. The pieces of one element a
// share one link, so what a link takes is found once for the pieces that follow one another with it (a highlight likewise).
func richTextBound(rt content.RichText) int {
	n := 2
	var link *content.Link
	var linkLen int
	var highlight *content.Highlight
	var highlightLen int
	for i := range rt {
		x := &rt[i]
		n += boundPiece + jsonStringLen(string(x.Type)) + jsonStringLen(x.Text)
		for j := range x.Marks {
			m := &x.Marks[j]
			n += boundMark + jsonStringLen(string(m.Type))
			if m.Link != nil {
				if m.Link != link {
					link, linkLen = m.Link, linkBound(m.Link)
				}
				n += linkLen
			}
			if m.Highlight != nil {
				if m.Highlight != highlight {
					highlight, highlightLen = m.Highlight, highlightBound(m.Highlight)
				}
				n += highlightLen
			}
		}
		n = min(n, boundCap)
	}
	return n
}

const attrsKey = `,"attrs":`

// linkBound returns the bound of the attributes of a link mark, with their key.
func linkBound(l *content.Link) int {
	n := len(attrsKey) + 2
	for _, m := range [...]struct{ key, value string }{
		{"href", l.Href}, {"entry", l.Entry}, {"media", l.Media}, {"anchor", l.Anchor},
	} {
		if m.value != "" {
			n += jsonMember(m.key, jsonStringLen(m.value))
		}
	}
	if l.NewTab {
		n += jsonMember("newTab", len("true"))
	}
	if len(l.Rel) > 0 {
		rel := 2
		for _, r := range l.Rel {
			rel += jsonStringLen(string(r)) + 1
		}
		n += jsonMember("rel", rel)
	}
	return n
}

// highlightBound returns the bound of the attributes of a highlight mark, with their key.
func highlightBound(h *content.Highlight) int {
	n := len(attrsKey) + 2
	if h.Variant != "" {
		n += jsonMember("variant", jsonStringLen(h.Variant))
	}
	if h.Color != (content.Color{}) {
		n += jsonMember("color", colorBound(h.Color))
	}
	if h.BackgroundColor != (content.Color{}) {
		n += jsonMember("backgroundColor", colorBound(h.BackgroundColor))
	}
	return n
}

func colorBound(c content.Color) int {
	n := 2
	if c.Preset != "" {
		n += jsonMember("preset", jsonStringLen(c.Preset))
	}
	if c.Value != "" {
		n += jsonMember("value", jsonStringLen(c.Value))
	}
	return n
}

// revisionBound returns the bound of the JSON of the revision r without its body, and that of each block of its body, as
// blockBound counts it. The JSON of the revision is at most rest and the sum of blocks.
func revisionBound(r *content.Revision) (rest int, blocks []int) {
	cp := *r
	cp.Body = nil
	data, err := cp.MarshalJSON()
	if err != nil {
		rest = boundCap
	} else {
		rest = len(data) + boundBody
	}
	blocks = make([]int, len(r.Body))
	for i := range r.Body {
		blocks[i] = blockBound(&r.Body[i])
	}
	return rest, blocks
}
