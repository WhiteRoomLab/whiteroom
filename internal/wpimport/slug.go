package wpimport

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// importSlug returns the slug of the whiteroom for raw, a slug of WordPress as it is written (percent-encoded), and why
// it is not the slug that raw means, apart from the percent-encoding. The slug is empty if nothing is left of it, and
// otherwise passes content.CheckSlug.
func importSlug(raw string) (slug string, why []string) {
	s, err := wxr.DecodeSlug(raw)
	if err != nil {
		// WordPress does not write such a slug (item 149 of 6.12); it is made from the characters as written.
		s = raw
		why = append(why, "it cannot be decoded: "+err.Error())
	}
	if n := content.NormalizeSlug(s); n != s {
		s = n
		why = append(why, "normalized to lower case and NFC")
	}
	s, replaced := replaceSlugChars(s)
	if replaced {
		why = append(why, `characters that a slug cannot have are "-"`)
	}
	if t := strings.Trim(s, "-"); t != s {
		// WordPress does not write such a slug; it is not the one in the old URLs.
		if !replaced {
			why = append(why, `"-" at the ends is removed`)
		}
		s = t
	}
	if len(s) > content.MaxSlugBytes {
		s = strings.TrimRight(cutBytes(s, content.MaxSlugBytes), "-")
		why = append(why, fmt.Sprintf("it is cut to %d bytes", content.MaxSlugBytes))
	}
	if content.NormalizeSlug(s) != s || content.CheckSlug(s) != nil {
		return "", why
	}
	return s, why
}

// replaceSlugChars replaces each run of the characters that a slug cannot have with "-", or with nothing next to a "-"
// of s. It reports whether there was a run.
func replaceSlugChars(s string) (string, bool) {
	var b strings.Builder
	var last rune
	replaced, pending := false, false
	for _, r := range s {
		if r != '-' && content.CheckSlug(string(r)) != nil {
			replaced, pending = true, true
			continue
		}
		if pending && r != '-' && last != '-' {
			b.WriteByte('-')
		}
		pending = false
		b.WriteRune(r)
		last = r
	}
	if pending && last != '-' {
		b.WriteByte('-')
	}
	return b.String(), replaced
}

// cutBytes returns s cut to at most n bytes, before a character.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// slugSet is the slugs that are used, each in a scope such as a taxonomy and a language.
type slugSet struct {
	used map[[2]string]bool
	next map[[2]string]int // where to go on from, for a slug that was used more than once
}

// take uses slug in scope, and reports whether it was not used.
func (s *slugSet) take(scope, slug string) bool {
	if s.used == nil {
		s.used, s.next = make(map[[2]string]bool), make(map[[2]string]int)
	}
	key := [2]string{scope, slug}
	if s.used[key] {
		return false
	}
	s.used[key] = true
	return true
}

// unique returns slug if it is not used in scope, and otherwise the first of slug-2, slug-3 and so on that is not,
// cutting slug to fit in content.MaxSlugBytes, and uses what it returns.
func (s *slugSet) unique(scope, slug string) string {
	if s.take(scope, slug) {
		return slug
	}
	key := [2]string{scope, slug}
	n := max(2, s.next[key])
	for ; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		candidate := cutBytes(slug, content.MaxSlugBytes-len(suffix)) + suffix
		if s.take(scope, candidate) {
			s.next[key] = n + 1
			return candidate
		}
	}
}

// slugClaim is the slug that an item wants in a scope, such as a taxonomy and a language.
type slugClaim struct {
	scope, slug string
	first       bool // WordPress had this slug, so no slug that the import made may take it
}

// claimSlugs returns the slugs that the items get, where those that want the same slug in a scope get slug-2 and so on.
// The slugs that WordPress had come first: the items with first take their slugs in order, so that an item whose URL
// is that of its slug keeps it. Then the first items that could not take theirs, and then the others, each in order,
// get the first slug that is free.
func claimSlugs(claims []slugClaim) []string {
	var set slugSet
	slugs := make([]string, len(claims))
	var later []int
	for i, c := range claims {
		switch {
		case !c.first:
		case set.take(c.scope, c.slug):
			slugs[i] = c.slug
		default:
			later = append(later, i)
		}
	}
	for i, c := range claims {
		if !c.first {
			later = append(later, i)
		}
	}
	for _, i := range later {
		slugs[i] = set.unique(claims[i].scope, claims[i].slug)
	}
	return slugs
}

// nicename returns the user_nicename, the slug of the author page, that WordPress makes of the login: of its first 50
// characters (wp_insert_user), as sanitize_title does. ASCII letters become lower case, "." and white space "-", other
// ASCII characters but digits and "_" are dropped, and there is no "-" at the ends or twice in a row. It can be empty.
func nicename(login string) string {
	var b strings.Builder
	hyphen := true // the text so far is empty or ends with "-"
	n := 0
	for _, r := range login {
		if n++; n > 50 {
			break
		}
		switch {
		case 'A' <= r && r <= 'Z':
			r += 'a' - 'A'
		case r == '.', r == ' ', r == '\t', r == '\n', r == '\r', r == '\f', r == '\v':
			r = '-'
		}
		switch {
		case r >= utf8.RuneSelf, 'a' <= r && r <= 'z', '0' <= r && r <= '9', r == '_':
			hyphen = false
		case r == '-':
			if hyphen {
				continue
			}
			hyphen = true
		default:
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimRight(b.String(), "-")
}
