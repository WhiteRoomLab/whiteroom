package wxr

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DecodeSlug decodes the percent-encoding of a slug that WordPress writes, such as %e3%81%82 for a non-ASCII character.
// It decodes only the % and the two hex digits after it, in lower or upper case; every other character stays as it is,
// including + and the non-ASCII characters that are not encoded. The result must be valid UTF-8 without control
// characters. It does not change the case or normalize the result, which is for the caller to do.
func DecodeSlug(s string) (string, error) {
	if strings.IndexByte(s, '%') >= 0 {
		var sb strings.Builder
		sb.Grow(len(s))
		for i := 0; i < len(s); i++ {
			if s[i] != '%' {
				sb.WriteByte(s[i])
				continue
			}
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return "", fmt.Errorf("invalid percent-encoding at byte %d", i)
			}
			sb.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
		}
		s = sb.String()
	}
	if !utf8.ValidString(s) {
		return "", errors.New("not valid UTF-8 after decoding")
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return "", errors.New("contains a control character after decoding")
	}
	return s, nil
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	}
	return c - 'a' + 10
}
