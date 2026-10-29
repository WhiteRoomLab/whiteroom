package yaml

import "fmt"

// flowValue reads the flow collection at the cursor, a block value whose parent has the indentation parent, and
// moves to the next line. A node at the start of a line or after "- " could be a key (keyPossible).
func (p *parser) flowValue(ptr string, parent int, keyPossible bool) error {
	start, c := p.here(), p.peek()
	hint := ""
	if parent >= 0 {
		hint = fmt.Sprintf("; to write a text that starts with %q, quote the whole value", string(c))
	}
	if err := p.flow(ptr, parent+1, hint); err != nil {
		return err
	}
	if keyPossible && p.keyColon() >= 0 {
		return p.errorf(start, "[ ] and { } cannot be keys; quote the key for a text")
	}
	closer := "]"
	if c == '{' {
		closer = "}"
	}
	if _, err := p.endLine(fmt.Sprintf("unexpected text after %q%s", closer, hint)); err != nil {
		return err
	}
	return p.nextLine()
}

// flowCollection is a flow collection being read: the place of its "[" or "{", the indentation that its lines
// after the first need, which is that of their parent block plus one, and the hint for its syntax errors.
type flowCollection struct {
	open   Position
	opener byte
	min    int
	hint   string
}

func (f *flowCollection) errorf(pos Position, format string, args ...any) error {
	return &Error{Position: pos, Msg: fmt.Sprintf(format, args...) + f.hint}
}

// flow reads the flow collection at the cursor.
func (p *parser) flow(ptr string, min int, hint string) error {
	f := &flowCollection{open: p.here(), opener: p.peek(), min: min, hint: hint}
	if err := p.enter(f.open); err != nil {
		return err
	}
	p.skip(1)
	var err error
	if f.opener == '[' {
		err = p.flowSequence(ptr, f)
	} else {
		err = p.flowMapping(ptr, f)
	}
	if err != nil {
		return err
	}
	p.skip(1)
	p.depth--
	return nil
}

// skipFlow skips spaces, comments and line breaks in f.
func (p *parser) skipFlow(f *flowCollection) error {
	spaced := false
	for {
		s := p.line()
		for p.off < len(s) && s[p.off] == ' ' {
			p.skip(1)
			spaced = true
		}
		if p.off < len(s) {
			switch s[p.off] {
			case '\t':
				return p.errorf(p.here(), msgTab)
			case '#':
				if !spaced {
					return nil
				}
			default:
				return nil
			}
		}
		if p.ln+1 == len(p.lines) {
			return f.errorf(f.open, "unclosed %q", string(f.opener))
		}
		p.ln++
		p.off, p.col = 0, 1
		spaced = true
		s = p.line()
		if ind := indentOf(s); ind < len(s) && s[ind] != '#' && s[ind] != '\t' {
			if err := p.checkMarker(); err != nil {
				return err
			}
			if ind < f.min {
				return p.errorf(Position{Line: p.ln + 1, Column: ind + 1},
					`%s; indent the lines of [ ] and { } deeper than their key or "-"`, msgIndent)
			}
		}
	}
}

// unexpected returns the error for the character at the cursor in f, where want was expected.
func (p *parser) unexpected(f *flowCollection, want string) error {
	if p.peek() == '#' {
		return p.errorf(p.here(), msgComment)
	}
	return f.errorf(p.here(), "expected %s", want)
}

func (p *parser) flowSequence(ptr string, f *flowCollection) error {
	p.out = append(p.out, '[')
	if err := p.skipFlow(f); err != nil {
		return err
	}
	for i := 0; p.peek() != ']'; i++ {
		if p.peek() == ',' {
			return f.errorf(p.here(), `empty item; remove the extra ","`)
		}
		if i > 0 {
			p.out = append(p.out, ',')
		}
		it := item(ptr, i)
		p.pos[it] = p.here()
		if err := p.flowNode(it, f, removeItem); err != nil {
			return err
		}
		if err := p.skipFlow(f); err != nil {
			return err
		}
		switch p.peek() {
		case ',':
			p.skip(1)
			if err := p.skipFlow(f); err != nil {
				return err
			}
		case ']':
		case ':':
			return f.errorf(p.here(), "a mapping in [ ] must be written in { }")
		default:
			return p.unexpected(f, `"," or "]"`)
		}
	}
	p.out = append(p.out, ']')
	return nil
}

func (p *parser) flowMapping(ptr string, f *flowCollection) error {
	p.out = append(p.out, '{')
	if err := p.skipFlow(f); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for p.peek() != '}' {
		switch p.peek() {
		case ',':
			return f.errorf(p.here(), `empty item; remove the extra ","`)
		case '[', '{':
			return p.errorf(p.here(), "[ ] and { } cannot be keys; quote the key for a text")
		case ']':
			return f.errorf(p.here(), `unexpected "]"`)
		}
		tok, err := p.scalar(true)
		if err != nil {
			return err
		}
		if !tok.quoted {
			if err := p.flowPlainEnd(); err != nil {
				return err
			}
		}
		key, err := p.key(tok)
		if err != nil {
			return err
		}
		if seen[key] {
			return p.errorf(tok.pos, "duplicate key %q", key)
		}
		if len(seen) > 0 {
			p.out = append(p.out, ',')
		}
		seen[key] = true
		p.appendString(key)
		p.out = append(p.out, ':')
		m := member(ptr, key)
		p.pos[m] = tok.pos
		if err := p.skipFlow(f); err != nil {
			return err
		}
		switch p.peek() {
		case ':':
		case ',', '}':
			return f.errorf(tok.pos, "missing value")
		default:
			return p.unexpected(f, `":"`)
		}
		p.skip(1)
		// After a plain key, ":" separates the value only with a space; {a:b} is a key "a:b" as YAML reads it.
		if c := p.peek(); !tok.quoted && c != 0 && c != ' ' && c != '\t' && c != ',' && c != '}' && c != ']' {
			return p.errorf(p.here(), `missing space after ":"`)
		}
		if err := p.skipFlow(f); err != nil {
			return err
		}
		if c := p.peek(); c == ',' || c == '}' {
			return f.errorf(tok.pos, "missing value")
		}
		if err := p.flowNode(m, f, removeKey); err != nil {
			return err
		}
		if err := p.skipFlow(f); err != nil {
			return err
		}
		switch p.peek() {
		case ',':
			p.skip(1)
			if err := p.skipFlow(f); err != nil {
				return err
			}
		case '}':
		default:
			return p.unexpected(f, `"," or "}"`)
		}
	}
	p.out = append(p.out, '}')
	return nil
}

// flowNode reads the node at the cursor in f.
func (p *parser) flowNode(ptr string, f *flowCollection, remove string) error {
	switch c := p.peek(); c {
	case '[', '{':
		return p.flow(ptr, f.min, f.hint)
	case ']', '}':
		return f.errorf(p.here(), "unexpected %q", string(c))
	}
	tok, err := p.scalar(true)
	if err != nil {
		return err
	}
	if tok.quoted {
		p.appendString(tok.text)
		return nil
	}
	if err := p.flowPlainEnd(); err != nil {
		return err
	}
	return p.appendPlain(tok, remove)
}

// flowPlainEnd returns an error if the plain scalar before the cursor ends its line and the next line with content
// would continue it in YAML.
func (p *parser) flowPlainEnd() error {
	if s := p.line(); indentOf(s[p.off:]) < len(s)-p.off {
		return nil
	}
	for ln := p.ln + 1; ln < len(p.lines); ln++ {
		s := p.lines[ln]
		ind := indentOf(s)
		if ind == len(s) {
			continue
		}
		switch c := s[ind]; {
		case c == '#' || c == '\t' || isFlowIndicator(c):
			return nil
		case c == ':' && (ind+1 == len(s) || s[ind+1] == ' ' || s[ind+1] == '\t' || isFlowIndicator(s[ind+1])):
			return nil
		}
		return p.errorf(Position{Line: ln + 1, Column: ind + 1}, msgMultiLine)
	}
	return nil
}
