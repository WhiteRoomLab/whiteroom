package content

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of RFC 5321 and DNS, unlike those of the CMS in limit.go.
const (
	maxEmailBytes       = 254 // a path of 256 bytes without "<" and ">"
	maxEmailLocalBytes  = 64
	maxDomainLabelBytes = 63
)

// normalizeEmail removes control characters and surrounding spaces as normalizeHref does,
// and maps the ASCII letters of the domain, after the last "@", to lower case.
func normalizeEmail(s string) string {
	s = normalizeHref(s)
	at := strings.LastIndexByte(s, '@')
	if at < 0 {
		return s
	}
	// Not strings.ToLower: it maps U+212A KELVIN SIGN to "k", turning an address that checkEmail rejects into another.
	return s[:at] + strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s[at:])
}

// checkEmail accepts an email address as checkAddrSpec does, without control characters or surrounding spaces.
func checkEmail(s string) error {
	if err := checkSize(s); err != nil {
		return err
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	if strings.ContainsFunc(s, unicode.IsControl) || strings.TrimFunc(s, unicode.IsSpace) != s {
		return errors.New("contains control characters or surrounding spaces; normalize it first")
	}
	if err := checkAddrSpec(s); err != nil {
		return fmt.Errorf("invalid email address %q: %w", s, err)
	}
	return nil
}

// checkAddrSpec accepts local@domain in ASCII: a local part of RFC 5322 atext and dots, and a domain of two or more
// labels of letters, digits and "-", in lower case. Quoted local parts, IP addresses, with or without brackets,
// and internationalized addresses are not supported.
func checkAddrSpec(s string) error {
	if i := strings.IndexFunc(s, func(r rune) bool { return r >= utf8.RuneSelf }); i >= 0 {
		if at := strings.LastIndexByte(s, '@'); at >= 0 && i > at {
			return errors.New("non-ASCII characters are not supported; write the domain in the xn-- form")
		}
		return errors.New("non-ASCII characters are not supported")
	}
	if len(s) > maxEmailBytes {
		return fmt.Errorf("longer than %d bytes", maxEmailBytes)
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || strings.Contains(domain, "@") {
		return errors.New("needs exactly one @")
	}
	switch {
	case local == "":
		return errors.New("empty local part")
	case len(local) > maxEmailLocalBytes:
		return fmt.Errorf("the local part is longer than %d bytes", maxEmailLocalBytes)
	}
	for i := range len(local) {
		if c := local[i]; c != '.' && !isAtext(c) {
			return fmt.Errorf("character %q is not allowed in the local part", c)
		}
	}
	if hasBadDots(local) {
		return errors.New(`the local part cannot start or end with "." or contain ".."`)
	}
	if domain == "" {
		return errors.New("empty domain")
	}
	// Before counting the labels, so that the comma in "example,com" is reported.
	for i := range len(domain) {
		if c := domain[i]; c != '.' && c != '-' && !isAlnum(c) {
			return fmt.Errorf("character %q is not allowed in the domain", c)
		}
	}
	if !strings.Contains(domain, ".") {
		return errors.New("the domain needs a dot")
	}
	if hasBadDots(domain) {
		return errors.New(`the domain cannot start or end with "." or contain ".."`)
	}
	for label := range strings.SplitSeq(domain, ".") {
		switch {
		case len(label) > maxDomainLabelBytes:
			return fmt.Errorf("a label of the domain is longer than %d characters", maxDomainLabelBytes)
		case label[0] == '-' || label[len(label)-1] == '-':
			return errors.New(`labels of the domain cannot start or end with "-"`)
		}
	}
	// No top-level domain is all digits (RFC 3696), so this is an IP address such as 192.0.2.1.
	if strings.Trim(domain[strings.LastIndexByte(domain, '.')+1:], "0123456789") == "" {
		return errors.New("the last label of the domain cannot be all digits (IP addresses are not supported)")
	}
	if strings.ContainsFunc(domain, unicode.IsUpper) {
		return errors.New("upper case in the domain; normalize it first")
	}
	return nil
}

// isAtext reports whether c is atext of RFC 5322: an ASCII letter or digit, or one of !#$%&'*+-/=?^_`{|}~.
func isAtext(c byte) bool {
	return isAlnum(c) || strings.IndexByte("!#$%&'*+-/=?^_`{|}~", c) >= 0
}

func isAlnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

// hasBadDots reports whether s starts or ends with "." or has two in a row.
func hasBadDots(s string) bool {
	return strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..")
}
