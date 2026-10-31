package html

import (
	"unicode"
	"unicode/utf8"
)

// c1Replacements is the table of 13.2.5.84: the code points that replace the numbers 0x80-0x9F of numeric character
// references. The numbers that it does not list are kept.
var c1Replacements = map[int]rune{
	0x80: 0x20AC,
	0x82: 0x201A,
	0x83: 0x0192,
	0x84: 0x201E,
	0x85: 0x2026,
	0x86: 0x2020,
	0x87: 0x2021,
	0x88: 0x02C6,
	0x89: 0x2030,
	0x8A: 0x0160,
	0x8B: 0x2039,
	0x8C: 0x0152,
	0x8E: 0x017D,
	0x91: 0x2018,
	0x92: 0x2019,
	0x93: 0x201C,
	0x94: 0x201D,
	0x95: 0x2022,
	0x96: 0x2013,
	0x97: 0x2014,
	0x98: 0x02DC,
	0x99: 0x2122,
	0x9A: 0x0161,
	0x9B: 0x203A,
	0x9C: 0x0153,
	0x9E: 0x017E,
	0x9F: 0x0178,
}

// inAttribute reports whether the character reference is consumed as part of an attribute.
func (z *tokenizer) inAttribute() bool {
	r := z.returnState
	return r == attributeValueDoubleQuotedState || r == attributeValueSingleQuotedState ||
		r == attributeValueUnquotedState
}

// flushCharRef flushes the code points consumed as a character reference: those in the temporary buffer.
func (z *tokenizer) flushCharRef() {
	if z.inAttribute() {
		z.attrValue = append(z.attrValue, z.tmp...)
	} else {
		z.text = append(z.text, z.tmp...)
	}
}

// characterReference is the character reference state (13.2.5.77).
func (z *tokenizer) characterReference() {
	z.tmp = append(z.tmp[:0], '&')
	switch c := z.consume(); {
	case isAlnum(c):
		z.reconsume(c, namedCharacterReferenceState)
	case c == '#':
		z.tmp = append(z.tmp, '#')
		z.state = numericCharacterReferenceState
	default:
		z.flushCharRef()
		z.reconsume(c, z.returnState)
	}
}

// namedCharacterReference is the named character reference state (13.2.5.78).
func (z *tokenizer) namedCharacterReference() {
	name, chars := z.longestEntity()
	if name == "" {
		z.flushCharRef()
		z.state = ambiguousAmpersandState
		return
	}
	z.pos += len(name)
	z.tmp = append(z.tmp, name...)
	historical := z.inAttribute() && name[len(name)-1] != ';' && z.pos < len(z.s) &&
		(z.s[z.pos] == '=' || isAlnum(int(z.s[z.pos])))
	if !historical {
		z.tmp = append(z.tmp[:0], chars...)
	}
	z.flushCharRef()
	z.state = z.returnState
}

// longestEntity returns the longest name of the named character reference table that the input starts with, and its
// characters; name is "" if there is none.
func (z *tokenizer) longestEntity() (name, chars string) {
	rest := z.s[z.pos:]
	// A name is ASCII letters and digits, and an optional ';' at the end.
	n := 0
	for n < len(rest) && n < maxEntityLen && isAlnum(int(rest[n])) {
		n++
	}
	if n < len(rest) && rest[n] == ';' {
		if s, ok := entities[rest[:n+1]]; ok {
			return rest[:n+1], s
		}
	}
	for ; n > 0; n-- {
		if s, ok := entities[rest[:n]]; ok {
			return rest[:n], s
		}
	}
	return "", ""
}

// ambiguousAmpersand is the ambiguous ampersand state (13.2.5.79).
func (z *tokenizer) ambiguousAmpersand() {
	c := z.consume()
	if !isAlnum(c) {
		// A ';' (an unknown-named-character-reference parse error) too.
		z.reconsume(c, z.returnState)
		return
	}
	if z.inAttribute() {
		z.attrValue = append(z.attrValue, byte(c))
	} else {
		z.text = append(z.text, byte(c))
	}
}

// numericCharacterReference is the numeric character reference state (13.2.5.80).
func (z *tokenizer) numericCharacterReference() {
	z.code = 0
	switch c := z.consume(); {
	case c == 'x' || c == 'X':
		z.tmp = append(z.tmp, byte(c))
		z.state = hexadecimalCharacterReferenceStartState
	case isDigit(c):
		z.reconsume(c, decimalCharacterReferenceState)
	default:
		z.flushCharRef()
		z.reconsume(c, z.returnState)
	}
}

// hexadecimalCharacterReferenceStart is the hexadecimal character reference start state (13.2.5.81).
func (z *tokenizer) hexadecimalCharacterReferenceStart() {
	if c := z.consume(); isDigit(c) || 'A' <= c && c <= 'F' || 'a' <= c && c <= 'f' {
		z.reconsume(c, hexadecimalCharacterReferenceState)
	} else {
		z.flushCharRef()
		z.reconsume(c, z.returnState)
	}
}

// hexadecimalCharacterReference is the hexadecimal character reference state (13.2.5.82).
func (z *tokenizer) hexadecimalCharacterReference() {
	switch c := z.consume(); {
	case isDigit(c):
		z.addDigit(16, c-0x30)
	case 'A' <= c && c <= 'F':
		z.addDigit(16, c-0x37)
	case 'a' <= c && c <= 'f':
		z.addDigit(16, c-0x57)
	case c == ';':
		z.state = numericCharacterReferenceEndState
	default:
		z.reconsume(c, numericCharacterReferenceEndState)
	}
}

// decimalCharacterReference is the decimal character reference state (13.2.5.83).
func (z *tokenizer) decimalCharacterReference() {
	switch c := z.consume(); {
	case isDigit(c):
		z.addDigit(10, c-0x30)
	case c == ';':
		z.state = numericCharacterReferenceEndState
	default:
		z.reconsume(c, numericCharacterReferenceEndState)
	}
}

// addDigit multiplies the character reference code by base and adds d. A code above 0x10FFFF stays as it is, which
// keeps it from overflowing; it becomes U+FFFD in the end.
func (z *tokenizer) addDigit(base, d int) {
	if z.code <= unicode.MaxRune {
		z.code = z.code*base + d
	}
}

// numericCharacterReferenceEnd is the numeric character reference end state (13.2.5.84). The parse errors for
// noncharacters and controls change nothing but the controls of the table.
func (z *tokenizer) numericCharacterReferenceEnd() {
	r := rune(z.code)
	switch {
	case z.code == 0 || z.code > unicode.MaxRune || 0xD800 <= z.code && z.code <= 0xDFFF:
		r = utf8.RuneError
	case 0x80 <= z.code && z.code <= 0x9F:
		if c, ok := c1Replacements[z.code]; ok {
			r = c
		}
	}
	z.tmp = utf8.AppendRune(z.tmp[:0], r)
	z.flushCharRef()
	z.state = z.returnState
}
