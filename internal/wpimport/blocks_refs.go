package wpimport

import (
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

func init() {
	converters["contact-form-7/contact-form-selector"] = convertContactForm
	converters["core/block"] = convertSyncedPattern
	converters["core/pattern"] = convertThemePattern
}

const formType = "wpcf7_contact_form"

// findForm finds the form of Contact Form 7 that a shortcode or a block with the hash, the ID and the title means, as
// Contact Form 7 does: the form whose hash starts with hash, if it has 7 or more hexadecimal digits, else the one with
// the post ID id, else the first one with the title. It returns nil if there is none.
func (imp *Import) findForm(id int64, hash, title string) *wxr.Item {
	if imp.WXR == nil {
		return nil
	}
	forms := imp.contactForms()
	if len(hash) >= 7 && strings.IndexFunc(hash, func(r rune) bool { return !asciiHex(r) }) < 0 {
		hash = strings.ToLower(hash)
		for _, it := range forms {
			if h, _ := it.Meta.Get("_hash"); strings.HasPrefix(strings.ToLower(h), hash) {
				return it
			}
		}
	}
	if it := imp.WXR.Post(id); it != nil && it.Type == formType {
		return it
	}
	if title != "" {
		for _, it := range forms {
			if it.Title == title {
				return it
			}
		}
	}
	return nil
}

// contactForms returns the forms of Contact Form 7 in the WXR, in its order. It looks for them on the first call, so that
// the bodies that use forms do not look at every item of the WXR for each form.
func (imp *Import) contactForms() []*wxr.Item {
	if !imp.formsRead {
		imp.formsRead = true
		for i := range imp.WXR.Items {
			if it := &imp.WXR.Items[i]; it.Type == formType {
				imp.forms = append(imp.forms, it)
			}
		}
	}
	return imp.forms
}

func asciiHex(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

func convertContactForm(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	a := b.Attrs
	id, title := postID(a.Get("id")), str(a.Get("title"))
	formID := ""
	if it := c.imp.findForm(id, str(a.Get("hash")), title); it != nil {
		if kind, to, ok := c.imp.IDs.Lookup(PostObject(it)); ok && kind == content.RefForm {
			if _, ok := c.imp.Ref(content.RefForm, to); ok {
				formID = to
			}
		}
	}
	if formID == "" {
		switch {
		case title != "":
			return nil, legacyf("the form %q is not imported", title)
		case id >= 1:
			return nil, legacyf("the form %d is not imported", id)
		}
		return nil, legacyf("the form is not imported")
	}
	// The form is in the attributes: the HTML only has the anchor and the class.
	root, err := c.root(b, "div")
	if err != nil {
		root = nil
	}
	blk := c.newBlock(content.BlockForm)
	blk.Attrs.Form = formID
	c.applyProps(&blk, b, root, noteAt{id: blk.ID})
	return []content.Block{blk}, nil
}

// patternBody is the content of a synced pattern, read.
type patternBody struct {
	blocks []wpblock.Block
	err    error // wpblock.ErrLimit
}

// readPattern reads the content of the synced pattern it, once.
func (imp *Import) readPattern(it *wxr.Item) *patternBody {
	if body, ok := imp.patternBodies[it.ID]; ok {
		return body
	}
	body := &patternBody{}
	if !blank(it.Content) {
		body.blocks, body.err = wpblock.Parse(it.Content, wpblock.Options{MaxBlocks: maxWPBlocks})
		if body.err == nil {
			body.blocks = withClassic(body.blocks, it.Content)
		}
	}
	if imp.patternBodies == nil {
		imp.patternBodies = make(map[int64]*patternBody)
	}
	imp.patternBodies[it.ID] = body
	return body
}

// convertSyncedPattern copies the content of a synced pattern (a reusable block) to where the block that uses it is:
// whiteroom has no shared blocks yet.
func convertSyncedPattern(c *bodyConv, _ string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	ref := postID(b.Attrs.Get("ref"))
	var it *wxr.Item
	if c.imp.WXR != nil {
		it = c.imp.WXR.Post(ref)
	}
	switch {
	case it == nil || it.Type != "wp_block":
		return nil, legacyf("the synced pattern %d is not in the WXR", ref)
	// WordPress shows nothing for these two: a copy would publish what the old site hid.
	case it.Status != "publish":
		return nil, legacyf("the synced pattern %d is not published; WordPress shows nothing for it", ref)
	case it.Password != "":
		return nil, legacyf("the synced pattern %d has a password; WordPress shows nothing for it", ref)
	case c.patterns[ref]:
		return nil, legacyf("the synced pattern %d includes itself", ref)
	case len(c.patterns) >= content.MaxDepth:
		// A copy is made at the depth of the block that uses the pattern, so the depth of the blocks does not bound a chain.
		return nil, legacyf("the synced patterns are nested more than %d deep", content.MaxDepth)
	}
	body := c.imp.readPattern(it)
	if body.err != nil {
		return nil, legacyf("the synced pattern %d has more than %d blocks", ref, maxWPBlocks)
	}
	// What the copy reads counts as the HTML of the content, so that patterns that use patterns cannot make it endless.
	if err := c.readHTML(len(it.Content)); err != nil {
		return nil, err
	}
	c.patterns[ref] = true
	blocks := c.flow(it.Content, body.blocks, p, at)
	delete(c.patterns, ref)
	first := at
	if len(blocks) > 0 {
		first = noteAt{id: blocks[0].ID}
	}
	name := strconv.FormatInt(ref, 10)
	if it.Title != "" {
		name += " (" + strconv.Quote(it.Title) + ")"
	}
	c.note(first, Changed, "the synced pattern %s is copied here; whiteroom has no shared blocks yet (stage 2)", name)
	if v := b.Attrs.Get("content"); v != nil && v.Kind == wpblock.Object && len(v.Object) > 0 {
		c.note(first, Skipped, "the overrides of the synced pattern %d are not applied", ref)
	}
	return blocks, nil
}

// convertThemePattern: the patterns of a theme are in its files, not in the WXR.
func convertThemePattern(_ *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	return nil, legacyf("the pattern %q of the theme is not in the WXR", str(b.Attrs.Get("slug")))
}
