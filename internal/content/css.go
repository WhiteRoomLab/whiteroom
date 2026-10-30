package content

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// yamlCommentHint follows the errors of a value that a YAML comment may have cut short, such as
// "linear-gradient(135deg, #f093fb 0%)" read as "linear-gradient(135deg,".
const yamlCommentHint = `; a "#" after a space starts a comment in YAML, so quote the whole value`

// checkCSSValue checks a preset of a theme, a CSS value written as that of a custom property, such as
// "--color-primary: #1e73be;" in a CSS file or a <style> element: it cannot end the declaration, the rule or the
// element. The kind of the value, such as a color, is not checked.
func checkCSSValue(s string) error {
	if err := checkLine(s); err != nil {
		return err
	}
	if s == "" {
		return errors.New("empty value")
	}
	if strings.TrimFunc(s, unicode.IsSpace) != s {
		return errors.New("starts or ends with a space")
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\'':
			n := strings.IndexByte(s[i+1:], c)
			if n < 0 {
				return errors.New("unclosed string" + yamlCommentHint)
			}
			if j := strings.IndexAny(s[i+1:i+1+n], `\<>`); j >= 0 {
				return cssCharError(s[i+1+j])
			}
			i += 1 + n
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			return errors.New("comments are not allowed")
		case c == '(':
			if name := cssFunctionName(s[:i]); isURLFunction(name) {
				return fmt.Errorf("%s() is not allowed; write URLs in the CSS files of assets/ instead", name)
			}
			depth++
		case c == ')':
			if depth == 0 {
				return errors.New(`unexpected ")"`)
			}
			depth--
		case c >= utf8.RuneSelf || isCSSValueByte(c):
		default:
			return cssCharError(c)
		}
	}
	if depth > 0 {
		return errors.New(`unclosed "("` + yamlCommentHint)
	}
	return nil
}

// isCSSValueByte reports whether a preset can have c, an ASCII character, outside quotes, apart from the quotes and
// the parentheses.
func isCSSValueByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte(" #%,./+-*_", c) >= 0
}

// cssCharError returns the error for c, an ASCII character that a preset cannot have where it is.
func cssCharError(c byte) error {
	switch c {
	case ';', '{', '}':
		return fmt.Errorf("%q is not allowed; a preset is a value, not a declaration or a rule", string(c))
	case '!':
		return errors.New(`"!" is not allowed; a preset cannot be !important`)
	case '\\':
		return errors.New("backslashes are not allowed; write the characters themselves, not CSS escapes")
	case '<', '>':
		return fmt.Errorf("%q is not allowed", string(c))
	}
	return fmt.Errorf("%q is not allowed outside quotes", string(c))
}

// cssFunctionName returns the name of the function whose "(" follows s: the identifier at the end of s, which is
// empty if s ends with a space, a quote or a symbol.
func cssFunctionName(s string) string {
	i := len(s)
	for i > 0 && (isNameByte(s[i-1]) || s[i-1] >= utf8.RuneSelf) {
		i--
	}
	return s[i:]
}

// isURLFunction reports whether name is that of a function that loads URLs, also from strings: url(), src(), image()
// and image-set(), matching it ASCII case-insensitively as CSS does.
func isURLFunction(name string) bool {
	// Not strings.EqualFold: it matches U+017F LATIN SMALL LETTER LONG S to "s", but CSS does not.
	lower := strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, name)
	switch lower {
	case "url", "src", "image", "image-set", "-webkit-image-set":
		return true
	}
	return false
}
