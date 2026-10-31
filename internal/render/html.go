package render

import (
	"errors"
	"html/template"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sanitize"
)

// html returns the HTML of b, an html or legacy block at path: its HTML sanitized by the policy of its type, with the
// URLs written as those of links and only the ids that are not anchors of the document yet, which it takes, and without
// the ASCII whitespace at its start and end; in a div element with the anchor and the class of b if it has either. It
// warns about the changes that sanitizing made. HTML that cannot be sanitized is not rendered, which it warns about
// before anything else.
func (w *walker) html(b *content.Block, path string) string {
	out, changes, err := sanitize.Sanitize(b.Attrs.HTML, sanitize.Options{
		Kind: sanitizeKind(b.Type),
		URL:  w.r.hrefURL,
		ID: func(id string) bool {
			if w.anchors[id] {
				return false
			}
			w.anchors[id] = true
			return true
		},
	})
	if err != nil {
		w.warnf(path+"/attrs/html", "%s", sanitizeError(err))
		return ""
	}
	out = strings.Trim(out, "\t\n\f\r ")
	s := w.begin(b, path)
	for _, c := range changes {
		if c.Count > 1 {
			w.warnf(path+"/attrs/html", "%s (%d times)", c.Msg, c.Count)
		} else {
			w.warnf(path+"/attrs/html", "%s", c.Msg)
		}
	}
	c := w.finish(s)
	if c.Anchor == "" && c.Class == "" {
		return out
	}
	var h strings.Builder
	h.WriteString("<div")
	if c.Anchor != "" {
		h.WriteString(` id="` + template.HTMLEscapeString(c.Anchor) + `"`)
	}
	if c.Class != "" {
		h.WriteString(` class="` + template.HTMLEscapeString(c.Class) + `"`)
	}
	h.WriteString(">")
	if out != "" {
		h.WriteString("\n" + out + "\n")
	}
	h.WriteString("</div>")
	return h.String()
}

// sanitizeKind returns the kind of sanitizing for the HTML of blocks of type t, html or legacy.
func sanitizeKind(t content.BlockType) sanitize.Kind {
	if t == content.BlockHTML {
		return sanitize.HTML
	}
	return sanitize.Legacy
}

// sanitizeError returns the warning about err, the error of sanitizing the HTML of a block.
func sanitizeError(err error) string {
	if errors.Is(err, sanitize.ErrLimit) {
		return "the HTML is too complex to sanitize; not rendered"
	}
	return "the HTML cannot be sanitized into a stable form; not rendered"
}
