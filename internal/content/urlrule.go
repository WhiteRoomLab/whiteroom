package content

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/nfc"
)

// urlPlaceholders are the names of the placeholders of URL rules, such as "slug" for {slug}.
var urlPlaceholders = []string{"slug", "path", "number", "yyyy", "mm", "dd", "category"}

// ruleKind tells what a URL rule can have, which depends on what the rule is for.
type ruleKind struct {
	name     string   // what the rule is for in messages, such as "post" or "author pages"
	allowed  []string // the placeholders that the rule can have, in the order of urlPlaceholders
	required []string // the rule needs one of these
	html     bool     // the rule can end with ".html" as well as "/"
	archive  bool     // the rule is the path of the list of a type, which has no placeholders
}

var (
	authorRuleKind  = ruleKind{name: "author pages", allowed: []string{"slug"}, required: []string{"slug"}}
	archiveRuleKind = ruleKind{archive: true}
)

func entryRuleKind(t *TypeDef) ruleKind {
	k := ruleKind{name: t.Name, allowed: []string{"slug", "number"}, required: []string{"slug", "number"}, html: true}
	switch t.Name {
	case TypePost:
		k.allowed = []string{"slug", "number", "yyyy", "mm", "dd", "category"}
	case TypePage:
		k.allowed = []string{"slug", "path", "number"}
		k.required = k.allowed
	default:
		if slices.Contains(t.Taxonomies, TaxonomyCategory) {
			k.allowed = append(k.allowed, "category")
		}
	}
	return k
}

func taxonomyRuleKind(t *TaxonomyDef) ruleKind {
	k := ruleKind{name: t.Name, allowed: []string{"slug"}, required: []string{"slug"}}
	if t.Hierarchical {
		k.allowed = []string{"slug", "path"}
		k.required = k.allowed
	}
	return k
}

// rulePart is literal text of a URL rule, or a placeholder.
type rulePart struct {
	text        string // the text, or the name of the placeholder, such as "slug" for {slug}
	placeholder bool
}

// parseURLRule splits rule into literal text and placeholders, such as "/archives/", {number} and "/" for
// "/archives/{number}/". A placeholder is a "{" and the text up to the next "}", which is its name.
func parseURLRule(rule string) ([]rulePart, error) {
	var parts []rulePart
	for rule != "" {
		i := strings.IndexAny(rule, "{}")
		switch {
		case i < 0:
			return append(parts, rulePart{text: rule}), nil
		case i > 0:
			parts = append(parts, rulePart{text: rule[:i]})
		}
		if rule[i] == '}' {
			return nil, errors.New(`unexpected "}"`)
		}
		name, rest, ok := strings.Cut(rule[i+1:], "}")
		if !ok {
			return nil, errors.New(`unclosed "{"`)
		}
		parts = append(parts, rulePart{text: name, placeholder: true})
		rule = rest
	}
	return parts, nil
}

// hasPlaceholder reports whether rule has the placeholder name, such as "category" for {category}.
func hasPlaceholder(rule, name string) bool {
	parts, err := parseURLRule(rule)
	return err == nil && slices.Contains(parts, rulePart{text: name, placeholder: true})
}

// checkURLRule checks rule, a URL rule of the kind k in s, and reports the first problem.
func checkURLRule(rule string, k ruleKind, s *Site) error {
	if err := checkSize(rule); err != nil {
		return err
	}
	if !utf8.ValidString(rule) {
		return errors.New("invalid UTF-8")
	}
	if err := k.check(rule, s); err != nil {
		what := "URL rule"
		if k.archive {
			what = "archive path"
		}
		return fmt.Errorf("invalid %s %q: %w", what, rule, err)
	}
	return nil
}

func (k ruleKind) check(rule string, s *Site) error {
	switch {
	case !strings.HasPrefix(rule, "/"):
		return errors.New(`must start with "/"`)
	case strings.HasSuffix(rule, "/"):
	case !k.html:
		return errors.New(`must end with "/"`)
	case !strings.HasSuffix(rule, ".html"):
		return errors.New(`must end with "/" or ".html"`)
	}
	if strings.HasSuffix(rule, "/.html") {
		return errors.New(`missing a name before ".html"`)
	}
	// Without the empty strings before the first "/" and after a trailing one.
	segs := strings.Split(rule, "/")[1:]
	if strings.HasSuffix(rule, "/") {
		segs = segs[:len(segs)-1]
	}
	for _, seg := range segs {
		switch {
		case seg == "":
			return errors.New("empty segment")
		case seg == "." || seg == "..":
			return errors.New(`dot segments ("." and "..") are not allowed`)
		case strings.HasPrefix(seg, "."):
			// Web servers often refuse to serve hidden files.
			return errors.New(`segments cannot start with "."`)
		}
	}
	parts, err := parseURLRule(rule)
	if err != nil {
		return err
	}
	var names []string
	for _, p := range parts {
		if !p.placeholder {
			for _, r := range p.text {
				if !isRuleChar(r) {
					return fmt.Errorf("character %q (U+%04X) is not allowed", r, r)
				}
			}
			continue
		}
		name := "{" + p.text + "}"
		switch {
		case len(k.allowed) == 0:
			return errors.New("placeholders are not allowed")
		case !slices.Contains(urlPlaceholders, p.text):
			return fmt.Errorf("unknown placeholder %q (use %s)", name, joinPlaceholders(k.allowed, ", "))
		case !slices.Contains(k.allowed, p.text):
			return fmt.Errorf("placeholder %q is not allowed for %s (use %s)", name, k.name, joinPlaceholders(k.allowed, ", "))
		case slices.Contains(names, p.text):
			return fmt.Errorf("%s can be used only once", name)
		}
		names = append(names, p.text)
	}
	// Only known placeholders are left, each once at most.
	for _, name := range []string{"path", "category"} {
		before, after, ok := strings.Cut(rule, "{"+name+"}")
		whole := strings.HasSuffix(before, "/") && (strings.HasPrefix(after, "/") || name == "path" && after == ".html")
		if ok && !whole {
			return fmt.Errorf("{%s} must be a whole segment", name)
		}
	}
	if len(k.required) > 0 && !slices.ContainsFunc(k.required, func(n string) bool { return slices.Contains(names, n) }) {
		return fmt.Errorf("missing %s", joinPlaceholders(k.required, " or "))
	}
	if slices.Contains(names, "slug") && slices.Contains(names, "path") {
		return errors.New("{slug} and {path} cannot be used together")
	}
	if !nfc.IsNormalized(rule) {
		return errors.New("not in Unicode NFC; normalize it first")
	}
	for _, l := range s.Languages {
		if prefix := s.LanguagePath(l); prefix != "/" && strings.HasPrefix(rule, prefix) {
			return fmt.Errorf("cannot start with %q, the path of the language %s", prefix, l)
		}
	}
	if k.archive && rule == "/" {
		return errors.New(`cannot be "/", the path of the front page`)
	}
	return nil
}

// isRuleChar reports whether r can be in the text of a URL rule: "/", ASCII letters, digits, "-", "_" and ".", and
// characters other than ASCII that are letters, marks, numbers, punctuation or symbols. Unlike slugs, not U+3000.
func isRuleChar(r rune) bool {
	if r < utf8.RuneSelf {
		return isNameByte(byte(r)) || r == '.' || r == '/'
	}
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)
}

// joinPlaceholders returns names as placeholders, such as "{slug}, {path} or {number}" if last is " or ".
func joinPlaceholders(names []string, last string) string {
	var b strings.Builder
	for i, name := range names {
		switch {
		case i == 0:
		case i == len(names)-1:
			b.WriteString(last)
		default:
			b.WriteString(", ")
		}
		b.WriteString("{" + name + "}")
	}
	return b.String()
}
