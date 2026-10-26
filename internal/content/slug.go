package content

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/nfc"
)

// MaxSlugBytes is the maximum length of a slug in UTF-8, which keeps it within a file name.
const MaxSlugBytes = 200

// normalizeSlug removes control characters as normalizeText does, and maps letters to lower case before NFC.
func normalizeSlug(s string) string {
	return nfc.Normalize(strings.Map(unicode.ToLower, cleanText(s, false)))
}

// checkSlug accepts lower case ASCII letters, digits, "-", "_", and characters other than ASCII
// that are letters, marks, numbers, punctuation, symbols or U+3000 IDEOGRAPHIC SPACE, as in WordPress slugs.
func checkSlug(s string) error {
	if len(s) > MaxSlugBytes {
		return fmt.Errorf("too long: %d bytes (max %d)", len(s), MaxSlugBytes)
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	for _, r := range s {
		switch {
		case isStrayControl(r, false):
			return errors.New("contains control characters; normalize it first")
		case unicode.ToLower(r) != r:
			return errors.New("contains upper case letters; normalize it first")
		case r < utf8.RuneSelf:
			if !isSlugByte(byte(r)) {
				return fmt.Errorf(`character %q is not allowed in slugs: use lower case letters, digits, "-" and "_" in ASCII`, r)
			}
		case r == '\u3000', unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S):
		default:
			return fmt.Errorf("character %q (U+%04X) is not allowed in slugs", r, r)
		}
	}
	if strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return errors.New(`slugs cannot start or end with "-"`)
	}
	if !nfc.IsNormalized(s) {
		return errors.New("not in Unicode NFC; normalize it first")
	}
	return nil
}

func isSlugByte(c byte) bool {
	return 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_'
}
