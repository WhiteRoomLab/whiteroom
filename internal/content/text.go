package content

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/nfc"
)

func normalizeText(s string, multiline bool) string {
	if multiline {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	s = strings.Map(func(r rune) rune {
		if isStrayControl(r, multiline) {
			return -1
		}
		return r
	}, s)
	return nfc.Normalize(s)
}

func checkText(s string, multiline bool) error {
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	if !multiline && strings.ContainsFunc(s, isLineBreakOrTab) {
		return errors.New("contains a line break or tab; use hardBreak for line breaks")
	}
	if strings.ContainsFunc(s, func(r rune) bool { return isStrayControl(r, multiline) }) {
		return errors.New("contains control characters; normalize it first")
	}
	if !nfc.IsNormalized(s) {
		return errors.New("not in Unicode NFC; normalize it first")
	}
	return nil
}

// isStrayControl reports whether r is a control character that normalizeText removes.
func isStrayControl(r rune, multiline bool) bool {
	if !unicode.IsControl(r) {
		return false
	}
	if multiline {
		return r != '\n' && r != '\t'
	}
	// Line breaks and tabs are left for checkText to report.
	return !unicode.IsSpace(r)
}

func isLineBreakOrTab(r rune) bool {
	return (unicode.IsControl(r) && unicode.IsSpace(r)) || r == '\u2028' || r == '\u2029'
}

func checkName(s string) error {
	if s == "" {
		return errors.New("empty name")
	}
	for i := range len(s) {
		if !isNameByte(s[i]) {
			return fmt.Errorf(`invalid name %q: use ASCII letters, digits, "-" and "_"`, s)
		}
	}
	return nil
}

func isNameByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_'
}

func normalizeHref(s string) string {
	// Browsers ignore tabs and newlines anywhere in a URL, as in "java\tscript:".
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimFunc(s, unicode.IsSpace)
}

func checkHref(s string) error {
	if s == "" {
		return errors.New("empty URL")
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	if strings.ContainsFunc(s, unicode.IsControl) || strings.TrimFunc(s, unicode.IsSpace) != s {
		return errors.New("contains control characters or surrounding spaces; normalize it first")
	}
	// Browsers read "\" as "/": "/\evil.example" is "//evil.example".
	if strings.Contains(s, `\`) {
		return errors.New("backslashes are not allowed")
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("invalid URL")
	}
	switch u.Scheme {
	case "":
		if !strings.HasPrefix(s, "/") {
			return errors.New(`must be an absolute URL or a path starting with "/"`)
		}
		if strings.HasPrefix(s, "//") {
			return errors.New("protocol-relative URLs are not allowed")
		}
	case "http", "https":
		if u.Host == "" {
			return errors.New("missing host")
		}
		if u.User != nil {
			return errors.New("credentials in URLs are not allowed")
		}
	case "mailto", "tel":
		if u.Opaque == "" {
			return errors.New("missing address")
		}
	default:
		return fmt.Errorf("scheme %q is not allowed", u.Scheme)
	}
	return nil
}
