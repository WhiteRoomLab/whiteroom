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

func checkSize(s string) error {
	if len(s) > MaxTextBytes {
		return fmt.Errorf("too long: %d bytes (max %d)", len(s), MaxTextBytes)
	}
	return nil
}

func cleanText(s string, multiline bool) string {
	if multiline {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	return strings.Map(func(r rune) rune {
		if isStrayControl(r, multiline) {
			return -1
		}
		return r
	}, s)
}

func normalizeText(s string, multiline bool) string {
	return nfc.Normalize(cleanText(s, multiline))
}

func checkText(s string, multiline bool) error {
	return checkTextWith(s, multiline, "contains a line break or tab; use hardBreak for line breaks")
}

// checkLine checks a line of plain text such as alt, which has no hardBreak.
func checkLine(s string) error {
	return checkTextWith(s, false, "contains a line break or tab")
}

// checkTextWith is checkText with lineBreak as the message for a line break or tab.
func checkTextWith(s string, multiline bool, lineBreak string) error {
	if err := checkSize(s); err != nil {
		return err
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	if !multiline && strings.ContainsFunc(s, isLineBreakOrTab) {
		return errors.New(lineBreak)
	}
	if strings.ContainsFunc(s, func(r rune) bool { return isStrayControl(r, multiline) }) {
		return errors.New("contains control characters; normalize it first")
	}
	if !nfc.IsNormalized(s) {
		return errors.New("not in Unicode NFC; normalize it first")
	}
	return nil
}

// checkRaw checks HTML. It does not require NFC, which would break URLs in src and href.
func checkRaw(s string) error {
	if err := checkSize(s); err != nil {
		return err
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	if strings.ContainsFunc(s, func(r rune) bool { return isStrayControl(r, true) }) {
		return errors.New("contains control characters; normalize it first")
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

// CheckName checks s by the rule for names, such as those of presets and types: ASCII letters, digits, "-" and "_".
func CheckName(s string) error {
	return checkName(s)
}

func checkName(s string) error {
	if err := checkSize(s); err != nil {
		return err
	}
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

// checkClassName checks s, the className of a block: a name that does not have the form of the classes that the core
// gives blocks by their IDs and highlights by their colors.
func checkClassName(s string) error {
	if err := checkName(s); err != nil {
		return err
	}
	reserved := func(prefix, chars string) bool {
		rest, ok := strings.CutPrefix(s, prefix)
		return ok && len(rest) == 8 && strings.TrimLeft(rest, chars) == ""
	}
	if reserved("b-", "0123456789abcdefghijklmnopqrstuvwxyz") || reserved("hl-", "0123456789abcdef") {
		return fmt.Errorf("reserved className %q: the core gives classes of the forms b-xxxxxxxx and hl-xxxxxxxx", s)
	}
	return nil
}

// CheckBlockID checks s by the rule for block IDs: "b_" and 8 lower case ASCII letters or digits.
func CheckBlockID(s string) error {
	return checkBlockID(s)
}

func checkBlockID(s string) error {
	rest, ok := strings.CutPrefix(s, "b_")
	if !ok || len(rest) != 8 || strings.TrimLeft(rest, "0123456789abcdefghijklmnopqrstuvwxyz") != "" {
		return fmt.Errorf(`invalid block ID %q: use "b_" and 8 lowercase ASCII letters or digits`, s)
	}
	return nil
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

// CheckHref checks s by the rule for the href of links: http, https, mailto and tel URLs and paths starting with "/",
// without control characters or surrounding spaces.
func CheckHref(s string) error {
	return checkHref(s)
}

func checkHref(s string) error {
	if err := checkSize(s); err != nil {
		return err
	}
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
