package sanitize

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// maxChanges is the most distinct messages of the changes; the changes past them are counted in one more message.
const maxChanges = 100

// maxBytes is the most bytes of a name or value in a message, before "..." is appended.
const maxBytes = 64

// changes collects the changes of a Sanitize: one Change for each distinct message, in the order of first occurrence.
type changes struct {
	list  []Change
	index map[string]int // of each message in list
	more  int            // the changes whose messages did not fit in list
}

func (c *changes) addf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if i, ok := c.index[msg]; ok {
		c.list[i].Count++
		return
	}
	if len(c.list) == maxChanges {
		c.more++
		return
	}
	if c.index == nil {
		c.index = make(map[string]int)
	}
	c.index[msg] = len(c.list)
	c.list = append(c.list, Change{Msg: msg, Count: 1})
}

// result returns the changes, with "and N more changes" last if there were more than maxChanges distinct messages.
func (c *changes) result() []Change {
	switch c.more {
	case 0:
		return c.list
	case 1:
		return append(c.list, Change{Msg: "and 1 more change", Count: 1})
	}
	return append(c.list, Change{Msg: fmt.Sprintf("and %d more changes", c.more), Count: 1})
}

// cut returns s if it has at most maxBytes bytes, or else its longest prefix of at most maxBytes bytes that ends at a
// rune boundary, and "...".
func cut(s string) string {
	if len(s) <= maxBytes {
		return s
	}
	i := maxBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "..."
}

// name returns s, the name of an element or attribute, for a message: cut, and quoted if it has characters that are
// not printable, quotes or backslashes, such as the escape sequences of terminals.
func name(s string) string {
	s = cut(s)
	if q := strconv.Quote(s); q[1:len(q)-1] != s {
		return q
	}
	return s
}
