// Package phpserialize reads the values that PHP's serialize() writes into WordPress data, such as the
// _wp_attachment_metadata of an attachment. It reads null, booleans, integers, floats, strings, and arrays only.
// Objects, enums, and references are errors, so reading a value never has side effects.
package phpserialize

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits on a value: arrays nest at most MaxDepth levels (the outermost array is level 1), and a value is at most
// MaxBytes long.
const (
	MaxDepth = 32
	MaxBytes = 1 << 20
)

const minEntryLen = len("i:0;N;")

// phpSpace is the set of characters that PHP's trim removes by default.
const phpSpace = " \t\n\r\v\x00"

type Kind uint8

const (
	Null Kind = iota
	Bool
	Int
	Float
	String
	Array
)

// Value is one PHP value. Only the field for Kind is set. Str holds the bytes as written, which need not be UTF-8,
// and Array holds the entries in the written order.
type Value struct {
	Kind  Kind
	Bool  bool
	Int   int64
	Float float64
	Str   string
	Array []Entry
}

// Entry is a key and a value of an array. Key is an Int or a String, as written.
type Entry struct{ Key, Value Value }

// Error is a syntax error, or a value that the package does not read, at Offset bytes from the start of the value.
type Error struct {
	Offset int
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("byte %d: %s", e.Offset, e.Msg)
}

// Unserialize reads s as one value, with no whitespace around or inside it. As in PHP, a string key that is the
// canonical form of an int64, such as "5", is the same key as that integer, and a repeated key is an error.
// Errors are *Error.
func Unserialize(s string) (Value, error) {
	return (&parser{s: s}).whole()
}

// maxRereads is how many times UnserializeCRLF reads a value again, after the first read, with other lengths.
const maxRereads = 16

// UnserializeCRLF reads s as Unserialize does, except that it also reads a string whose written length does not end it
// with a quote and a semicolon, by counting each LF in it as CRLF, two bytes. An XML parser turns the CRLF of a value
// of a WXR into LF, which makes PHP's length of such a string too long. The strings keep their LF. It also returns the
// number of the strings that it read so. Errors are *Error.
//
// When both lengths end a string, the read goes on with the written one, and if the value then fails, UnserializeCRLF
// reads it again from the start, taking the counted length at the last such string that still has the written one, and
// so on, up to maxRereads times. If no read succeeds, the error is that of the first.
func UnserializeCRLF(s string) (Value, int, error) {
	return unserializeCRLF(s, maxRereads)
}

func unserializeCRLF(s string, rereads int) (Value, int, error) {
	var choices []bool
	var first error
	for read := 0; ; read++ {
		p := &parser{s: s, crlf: true, choices: choices}
		v, err := p.whole()
		if err == nil {
			return v, p.recounted, nil
		}
		if read == 0 {
			first = err
		}
		// Go back to the last branch point that has the written length, and take the counted one there.
		choices = p.choices
		i := len(choices) - 1
		for i >= 0 && choices[i] {
			i--
		}
		if i < 0 || read == rereads {
			return Value{}, 0, first
		}
		choices = append(choices[:i], true)
	}
}

// LooksSerialized reports whether WordPress's is_serialized (strict) takes s for a serialized value. It checks only
// the shape, after trimming PHP's whitespace, so a true result does not mean that Unserialize succeeds.
func LooksSerialized(s string) bool {
	s = strings.Trim(s, phpSpace)
	if s == "N;" {
		return true
	}
	if len(s) < 4 || s[1] != ':' {
		return false
	}
	if last := s[len(s)-1]; last != ';' && last != '}' {
		return false
	}
	switch s[0] {
	case 's':
		if s[len(s)-2] != '"' {
			return false
		}
		fallthrough
	case 'a', 'O', 'E':
		return startsWithCount(s[2:])
	case 'b', 'i', 'd':
		// As in WordPress, INF, NAN, and a lowercase e are not number characters here.
		body, ok := strings.CutSuffix(s[2:], ";")
		return ok && body != "" && strings.Trim(body, "0123456789.E+-") == ""
	}
	return false
}

// Lookup returns the value of the first entry of an array whose key is key. As in PHP, the string "5" and the integer
// 5 are the same key, but only for the canonical decimal form of an int64: "05" and "-0" are strings.
func (v Value) Lookup(key string) (Value, bool) {
	if v.Kind != Array {
		return Value{}, false
	}
	n, isInt := canonicalInt(key)
	for _, e := range v.Array {
		if (e.Key.Kind == String && e.Key.Str == key) || (isInt && e.Key.Kind == Int && e.Key.Int == n) {
			return e.Value, true
		}
	}
	return Value{}, false
}

// canonicalInt returns the int64 that s is the canonical form of: "0", or digits without a leading 0 after an
// optional "-". As in PHP, "-0" is not canonical.
func canonicalInt(s string) (int64, bool) {
	digits := strings.TrimPrefix(s, "-")
	if digits == "" || len(digits) > 19 || (digits[0] == '0' && len(s) > 1) {
		return 0, false
	}
	for i := range len(digits) {
		if !isDigit(digits[i]) {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// startsWithCount reports whether s starts with one or more digits and a colon.
func startsWithCount(s string) bool {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i > 0 && i < len(s) && s[i] == ':'
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

func errorAt(offset int, format string, args ...any) error {
	return &Error{Offset: offset, Msg: fmt.Sprintf(format, args...)}
}

func quote(c byte) string {
	if c < utf8.RuneSelf {
		return strconv.QuoteRune(rune(c))
	}
	return fmt.Sprintf(`'\x%02x'`, c)
}

type parser struct {
	s   string
	pos int

	// For UnserializeCRLF. A branch point is a string that both lengths end, and choices[i] is whether the read takes
	// the counted length at the ith one it meets. A read adds the written length to choices for the points it meets
	// that choices does not have yet.
	crlf      bool
	choices   []bool
	branches  int
	recounted int // the strings read with the counted length
}

// whole reads p.s as one value.
func (p *parser) whole() (Value, error) {
	if len(p.s) > MaxBytes {
		return Value{}, errorAt(MaxBytes, "too long: %d bytes (max %d)", len(p.s), MaxBytes)
	}
	v, err := p.value(0)
	if err != nil {
		return Value{}, err
	}
	if p.pos < len(p.s) {
		return Value{}, errorAt(p.pos, "unexpected data after the value")
	}
	return v, nil
}

func (p *parser) eof() error {
	return errorAt(len(p.s), "unexpected end of the value")
}

func (p *parser) expect(c byte) error {
	if p.pos >= len(p.s) {
		return p.eof()
	}
	if got := p.s[p.pos]; got != c {
		return errorAt(p.pos, "expected %s, found %s", quote(c), quote(got))
	}
	p.pos++
	return nil
}

func (p *parser) noDigit() error {
	if p.pos >= len(p.s) {
		return p.eof()
	}
	return errorAt(p.pos, "expected a digit, found %s", quote(p.s[p.pos]))
}

func (p *parser) skipSign() {
	if p.pos < len(p.s) && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
		p.pos++
	}
}

func (p *parser) skipDigits() int {
	start := p.pos
	for p.pos < len(p.s) && isDigit(p.s[p.pos]) {
		p.pos++
	}
	return p.pos - start
}

func (p *parser) digits() error {
	if p.skipDigits() == 0 {
		return p.noDigit()
	}
	return nil
}

// count reads the unsigned decimal number of a string length or an array size. A number beyond twice the length of the
// input, which no length of a string with its LF counted as two bytes reaches, is returned as that plus 1, which cannot
// overflow.
func (p *parser) count() (int, error) {
	start := p.pos
	if err := p.digits(); err != nil {
		return 0, err
	}
	limit := 2 * len(p.s)
	n := 0
	for i := start; i < p.pos; i++ {
		n = n*10 + int(p.s[i]-'0')
		if n > limit {
			return limit + 1, nil
		}
	}
	return n, nil
}

// value reads a value at p.pos inside depth arrays.
func (p *parser) value(depth int) (Value, error) {
	if p.pos >= len(p.s) {
		return Value{}, p.eof()
	}
	at := p.pos
	c := p.s[at]
	p.pos++
	switch c {
	case 'N':
		if err := p.expect(';'); err != nil {
			return Value{}, err
		}
		return Value{Kind: Null}, nil
	case 'b':
		return p.boolean()
	case 'i':
		return p.integer()
	case 'd':
		return p.float()
	case 's':
		return p.str()
	case 'a':
		return p.array(depth, at)
	case 'O', 'C', 'o':
		return Value{}, errorAt(at, "objects are not supported")
	case 'E':
		return Value{}, errorAt(at, "enums are not supported")
	case 'r', 'R':
		return Value{}, errorAt(at, "references are not supported")
	case 'S':
		return Value{}, errorAt(at, "escaped strings are not supported")
	}
	return Value{}, errorAt(at, "unexpected %s", quote(c))
}

func (p *parser) boolean() (Value, error) {
	if err := p.expect(':'); err != nil {
		return Value{}, err
	}
	if p.pos >= len(p.s) {
		return Value{}, p.eof()
	}
	c := p.s[p.pos]
	if c != '0' && c != '1' {
		return Value{}, errorAt(p.pos, "expected 0 or 1, found %s", quote(c))
	}
	p.pos++
	if err := p.expect(';'); err != nil {
		return Value{}, err
	}
	return Value{Kind: Bool, Bool: c == '1'}, nil
}

func (p *parser) integer() (Value, error) {
	if err := p.expect(':'); err != nil {
		return Value{}, err
	}
	start := p.pos
	p.skipSign()
	if err := p.digits(); err != nil {
		return Value{}, err
	}
	n, err := strconv.ParseInt(p.s[start:p.pos], 10, 64)
	if err != nil {
		return Value{}, errorAt(start, "integer is out of the int64 range")
	}
	if err := p.expect(';'); err != nil {
		return Value{}, err
	}
	return Value{Kind: Int, Int: n}, nil
}

func (p *parser) float() (Value, error) {
	if err := p.expect(':'); err != nil {
		return Value{}, err
	}
	var f float64
	switch rest := p.s[p.pos:]; {
	case strings.HasPrefix(rest, "INF"):
		f = math.Inf(1)
		p.pos += len("INF")
	case strings.HasPrefix(rest, "-INF"):
		f = math.Inf(-1)
		p.pos += len("-INF")
	case strings.HasPrefix(rest, "NAN"):
		f = math.NaN()
		p.pos += len("NAN")
	default:
		var err error
		if f, err = p.number(); err != nil {
			return Value{}, err
		}
	}
	if err := p.expect(';'); err != nil {
		return Value{}, err
	}
	return Value{Kind: Float, Float: f}, nil
}

// number reads a decimal float: digits with an optional point, and an optional exponent. The syntax is checked here
// because ParseFloat also reads hexadecimal numbers, underscores, and "Infinity".
func (p *parser) number() (float64, error) {
	start := p.pos
	p.skipSign()
	whole := p.skipDigits()
	frac := 0
	if p.pos < len(p.s) && p.s[p.pos] == '.' {
		p.pos++
		frac = p.skipDigits()
	}
	if whole+frac == 0 {
		return 0, p.noDigit()
	}
	if p.pos < len(p.s) && (p.s[p.pos] == 'e' || p.s[p.pos] == 'E') {
		p.pos++
		p.skipSign()
		if err := p.digits(); err != nil {
			return 0, err
		}
	}
	f, err := strconv.ParseFloat(p.s[start:p.pos], 64)
	if err != nil {
		return 0, errorAt(start, "float is out of range")
	}
	return f, nil
}

func (p *parser) str() (Value, error) {
	if err := p.expect(':'); err != nil {
		return Value{}, err
	}
	lenAt := p.pos
	n, err := p.count()
	if err != nil {
		return Value{}, err
	}
	if err := p.expect(':'); err != nil {
		return Value{}, err
	}
	if err := p.expect('"'); err != nil {
		return Value{}, err
	}
	if p.crlf {
		if s, ok := p.recountedString(n); ok {
			return Value{Kind: String, Str: s}, nil
		}
	}
	if n > len(p.s)-p.pos {
		return Value{}, errorAt(lenAt, "string is longer than the rest of the input")
	}
	s := p.s[p.pos : p.pos+n]
	p.pos += n
	if err := p.expect('"'); err != nil {
		return Value{}, err
	}
	if err := p.expect(';'); err != nil {
		return Value{}, err
	}
	return Value{Kind: String, Str: s}, nil
}

// recountedString reads the string at p.pos, whose written length is n, with the length that counts each LF in it as
// two bytes, if that length ends the string and the written one does not, or the read takes the counted one.
func (p *parser) recountedString(n int) (string, bool) {
	m := p.recount(n)
	if m < 0 || m == n || !p.closedAfter(m) {
		return "", false
	}
	if p.closedAfter(n) && !p.choose() {
		return "", false
	}
	s := p.s[p.pos : p.pos+m]
	p.pos += m + len(`";`)
	p.recounted++
	return s, true
}

// recount returns the number of bytes from p.pos that are n bytes long when each LF counts as two, or -1 if there is
// no such number. It looks at no more than n bytes.
func (p *parser) recount(n int) int {
	sum := 0
	for i := p.pos; i < len(p.s); i++ {
		sum++
		if p.s[i] == '\n' {
			sum++
		}
		if sum >= n {
			if sum == n {
				return i + 1 - p.pos
			}
			break
		}
	}
	return -1
}

// closedAfter reports whether `";` follows the n bytes from p.pos.
func (p *parser) closedAfter(n int) bool {
	end := p.pos + n
	return end+2 <= len(p.s) && p.s[end] == '"' && p.s[end+1] == ';'
}

// choose says whether the read takes the counted length at the branch point that it meets now.
func (p *parser) choose() bool {
	i := p.branches
	p.branches++
	if i == len(p.choices) {
		p.choices = append(p.choices, false)
	}
	return p.choices[i]
}

// mapKey is an array key after PHP's normalization: a string that is the canonical form of an int64 becomes the int64.
type mapKey struct {
	num   int64
	str   string
	isNum bool
}

func normalize(k Value) mapKey {
	if k.Kind == Int {
		return mapKey{num: k.Int, isNum: true}
	}
	if n, ok := canonicalInt(k.Str); ok {
		return mapKey{num: n, isNum: true}
	}
	return mapKey{str: k.Str}
}

func (p *parser) key() (Value, error) {
	if p.pos >= len(p.s) {
		return Value{}, p.eof()
	}
	switch p.s[p.pos] {
	case 'i':
		p.pos++
		return p.integer()
	case 's':
		p.pos++
		return p.str()
	}
	return Value{}, errorAt(p.pos, "array keys must be integers or strings")
}

// array reads an array after its "a", which is at offset at, inside depth arrays.
func (p *parser) array(depth, at int) (Value, error) {
	if depth >= MaxDepth {
		return Value{}, errorAt(at, "arrays are nested too deeply: more than %d levels", MaxDepth)
	}
	if err := p.expect(':'); err != nil {
		return Value{}, err
	}
	n, err := p.count()
	if err != nil {
		return Value{}, err
	}
	if err := p.expect(':'); err != nil {
		return Value{}, err
	}
	if err := p.expect('{'); err != nil {
		return Value{}, err
	}
	// The size comes from the input, so what it claims must not decide how much is allocated.
	hint := min(n, (len(p.s)-p.pos)/minEntryLen)
	entries := make([]Entry, 0, hint)
	seen := make(map[mapKey]bool, hint)
	for len(entries) < n {
		if p.pos < len(p.s) && p.s[p.pos] == '}' {
			return Value{}, errorAt(p.pos, "array has fewer elements than declared")
		}
		keyAt := p.pos
		k, err := p.key()
		if err != nil {
			return Value{}, err
		}
		nk := normalize(k)
		if seen[nk] {
			return Value{}, errorAt(keyAt, "duplicate key")
		}
		seen[nk] = true
		v, err := p.value(depth + 1)
		if err != nil {
			return Value{}, err
		}
		entries = append(entries, Entry{Key: k, Value: v})
	}
	if err := p.expect('}'); err != nil {
		return Value{}, err
	}
	return Value{Kind: Array, Array: entries}, nil
}
