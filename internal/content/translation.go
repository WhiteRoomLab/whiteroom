package content

import (
	"errors"
	"fmt"
	"strings"
)

// ThemeTranslation is a translation file of a theme, translations/<language>.yaml, which developers write: its texts
// by key, in the written order.
type ThemeTranslation []ThemeText

// ThemeText is a text that templates show by Key, such as "{page} / {total} ページ", in which {page} and {total} are
// placeholders for values that the template gives.
type ThemeText struct{ Key, Text string }

// Unmarshal reads data, a translation file of a theme in the subset of YAML that internal/yaml reads, into x: a
// mapping from keys to texts, which become a list in the written order. x changes only if all of data is read without
// error. An error is an *Error at the line and the column of the problem. The texts stay as written, without
// normalization: Validate reports the other problems, such as invalid keys and placeholders.
func (x *ThemeTranslation) Unmarshal(data []byte) error {
	var v ThemeTranslation
	if _, err := readYAML(data, v.decode); err != nil {
		return err
	}
	*x = v
	return nil
}

// LoadThemeTranslation reads data, a translation file of a theme, as Unmarshal does and validates it. The error is
// Errors, each with File set to name, such as "translations/ja.yaml", and the line and the column of the problem. If
// data does not read, the error is that of reading it, without validation. The translation is nil with an error.
func LoadThemeTranslation(name string, data []byte) (ThemeTranslation, error) {
	var x ThemeTranslation
	// Not the method value x.Validate, which would copy x before it is read.
	if err := loadYAML(name, data, x.decode, func() error { return x.Validate() }); err != nil {
		return nil, err
	}
	return x, nil
}

func (x *ThemeTranslation) decode(d *decoder) error {
	return d.object(func(key string) error {
		text, err := d.string()
		*x = append(*x, ThemeText{Key: key, Text: text})
		return err
	})
}

// Validate checks x. Keys follow the name rule, and texts are lines in which braces only enclose the names of
// placeholders, which follow the name rule too, such as {page}. A placeholder may appear more than once in a text.
func (x ThemeTranslation) Validate() error {
	var c checker
	seen := make(map[string]bool, len(x))
	for _, t := range x {
		p := "/" + escapePointer(t.Key)
		if err := checkName(t.Key); err != nil {
			c.add(p, err)
		} else if seen[t.Key] {
			c.addf(p, "duplicate key %q", t.Key)
		}
		seen[t.Key] = true
		if t.Text == "" {
			c.addf(p, "missing text")
		} else {
			c.add(p, checkThemeText(t.Text))
		}
	}
	return c.result()
}

// Placeholders returns the names of the placeholders of a valid text in order of first appearance, without repeats:
// page and total for "{page} / {total} ページ".
func (t ThemeText) Placeholders() []string {
	var names []string
	seen := make(map[string]bool)
	_ = scanPlaceholders(t.Text, func(name string) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	})
	return names
}

// checkThemeText checks s, a text of a theme: a line in which braces only enclose the names of placeholders.
func checkThemeText(s string) error {
	if err := checkLine(s); err != nil {
		return err
	}
	return scanPlaceholders(s, nil)
}

// scanPlaceholders calls placeholder, unless it is nil, with the name of each placeholder of s in order, repeats
// included, and returns the error for the first brace of s that is not part of a placeholder. As in titleFormat,
// braces cannot be written as text.
func scanPlaceholders(s string, placeholder func(name string)) error {
	for {
		i := strings.IndexAny(s, "{}")
		if i < 0 {
			return nil
		}
		if s[i] == '}' {
			return errors.New(`unexpected "}"`)
		}
		name, rest, ok := strings.Cut(s[i+1:], "}")
		switch {
		case !ok:
			return errors.New(`unclosed "{"`)
		case name == "":
			return errors.New(`empty placeholder "{}"`)
		case checkName(name) != nil:
			return fmt.Errorf(`invalid placeholder %q: use ASCII letters, digits, "-" and "_"`, "{"+name+"}")
		}
		if placeholder != nil {
			placeholder(name)
		}
		s = rest
	}
}
