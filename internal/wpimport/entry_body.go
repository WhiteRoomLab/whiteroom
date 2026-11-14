package wpimport

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// maxRevisionBytes is the size that the JSON of a revision may have; the tests lower it.
var maxRevisionBytes = content.MaxDocumentBytes

// validateRevision is Revision.Validate, which checks a revision with its body; the tests make it fail.
var validateRevision = content.Revision.Validate

// bodyMade is what setBody made of the body, which checkRevision takes back if the revision does not pass.
type bodyMade struct {
	reports int  // the entries of the report before the conversion of the body added its own
	excerpt bool // the excerpt is the text before the read more tag
}

func revisionFile(revID string) string { return "revisions/" + revID + ".json" }

// setBody makes the body of ei, and the excerpt of the text before the read more tag if the revision has none, and leaves
// out the blocks that would make the JSON of the revision larger than maxRevisionBytes. It calls Body, once, for an entry
// that is about to be given.
func (c *entryConv) setBody(ei *entryItem) (bodyMade, error) {
	made := bodyMade{reports: c.imp.Report.count()}
	if !c.site.Type(ei.entry.Type).Body {
		if !blank(ei.it.Content) {
			c.revisionNote(Skipped, ei, "", "the content is not imported; the type %s has no body", typeText(ei.entry.Type))
		}
		return made, nil
	}
	res, err := c.imp.Body(BodyOptions{
		Site: c.site, Settings: c.opts.Settings, Item: ei.it,
		Target: Target{File: revisionFile(ei.revID), Pointer: "/body"}, Key: ei.revID,
	})
	if err != nil {
		return made, fmt.Errorf("the content of item %d: %w", ei.it.ID, err)
	}
	// An empty body is nil, as UnmarshalJSON makes it.
	if len(res.Blocks) > 0 {
		ei.rev.Body = res.Blocks
	}
	if ei.rev.Excerpt == "" && res.More > 0 {
		made.excerpt = c.setMoreExcerpt(ei, res.Blocks[:min(res.More, len(res.Blocks))])
	}
	c.fitBody(ei, made.reports)
	return made, nil
}

// setMoreExcerpt makes the excerpt of ei from bs, the blocks before the read more tag, and reports whether it made one.
func (c *entryConv) setMoreExcerpt(ei *entryItem, bs content.Blocks) bool {
	var lines excerptLines
	lines.blocks(bs)
	if len(lines) == 0 {
		return false
	}
	// Normalize the text alone: the revision would walk every piece of the body again.
	rev := content.Revision{Excerpt: tidyText(strings.Join(lines, "\n"))}
	rev.Normalize(nil)
	text := rev.Excerpt
	switch {
	case text == "":
		return false
	case len(text) > content.MaxTextBytes:
		c.revisionNote(Skipped, ei, "/excerpt", "the excerpt before the read more tag is not imported: it is too long: %d bytes (max %d)",
			len(text), content.MaxTextBytes)
		return false
	}
	ei.rev.Excerpt = text
	return true
}

// excerptLines are the texts of the blocks that an excerpt is made of, one for each block that has some. It takes the
// blocks that the automatic excerpt of the build takes (6.8), which is shown when the excerpt is empty.
type excerptLines []string

func (w *excerptLines) blocks(bs content.Blocks) {
	for i := range bs {
		b := &bs[i]
		switch b.Type {
		case content.BlockParagraph, content.BlockHeading, content.BlockTableCell:
			w.text(b.Text)
		case content.BlockList, content.BlockListItem, content.BlockTable, content.BlockTableRow, content.BlockColumns,
			content.BlockColumn, content.BlockGroup:
			w.blocks(b.Children)
		case content.BlockQuote:
			w.blocks(b.Children)
			w.text(b.Attrs.Citation)
		case content.BlockImage, content.BlockButtons, content.BlockButton, content.BlockSeparator, content.BlockSpacer,
			content.BlockCode, content.BlockEmbed, content.BlockFile, content.BlockForm, content.BlockHTML,
			content.BlockLegacy:
		}
	}
}

// text adds the characters of rt, with a line break for each hardBreak, unless there are only spaces.
func (w *excerptLines) text(rt content.RichText) {
	var s strings.Builder
	for i := range rt {
		switch rt[i].Type {
		case content.InlineText:
			s.WriteString(rt[i].Text)
		case content.InlineHardBreak:
			s.WriteByte('\n')
		}
	}
	if strings.TrimSpace(s.String()) != "" {
		*w = append(*w, s.String())
	}
}

// fitBody keeps the blocks of the body of ei from the start that leave the JSON of the revision at most maxRevisionBytes
// by the count of revisionBound, and records the others. reports is the number of entries of the report before Body.
func (c *entryConv) fitBody(ei *entryItem, reports int) {
	rest, bounds := revisionBound(&ei.rev)
	total, keep := rest, len(bounds)
	for i, n := range bounds {
		if total += n; total > maxRevisionBytes {
			keep = i
			break
		}
	}
	if keep == len(bounds) {
		return
	}
	c.dropBodyNotes(reports, keep)
	body := ei.rev.Body
	clear(body[keep:])
	ei.rev.Body = body[:keep:keep]
	if keep == 0 {
		ei.rev.Body = nil
	}
	c.revisionNote(Skipped, ei, "/body", "the blocks from /body/%d on are not imported: the revision would be larger than %d bytes as JSON",
		keep, maxRevisionBytes)
}

// dropBodyNotes takes out of the report the entries about the blocks from the index first on of the body, among those added
// after the first reports: Body noted them before it was known that the blocks are left out.
func (c *entryConv) dropBodyNotes(reports, first int) {
	c.imp.Report.dropFrom(reports, func(e Entry) bool {
		rest, ok := strings.CutPrefix(e.Target.Pointer, "/body/")
		if !ok {
			return false
		}
		index, _, _ := strings.Cut(rest, "/")
		n, err := strconv.Atoi(index)
		return err == nil && n >= first
	})
}

// checkRevision checks the revision of ei as one of entry. The body is what the revision got after tidy checked it, so if
// it does not pass, the body is not imported, nor the excerpt that was made of it, and the revision is checked again.
func (c *entryConv) checkRevision(ei *entryItem, entry *content.Entry, made bodyMade) error {
	opts := c.revisionOptions(entry, publishing(ei.status))
	err := validateRevision(ei.rev, opts)
	if err != nil && (len(ei.rev.Body) > 0 || made.excerpt) {
		c.revisionNote(Skipped, ei, "/body", "the content is not imported: it does not pass validation: %s", firstError(err).Error())
		c.dropBodyNotes(made.reports, 0)
		ei.rev.Body = nil
		if made.excerpt {
			ei.rev.Excerpt = ""
		}
		err = validateRevision(ei.rev, opts)
	}
	if err != nil {
		return fmt.Errorf("the entry of item %d does not pass validation: %s", ei.it.ID, errorText(err))
	}
	return nil
}
