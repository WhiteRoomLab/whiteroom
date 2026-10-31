package render

import (
	"errors"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// CSS is the CSS that the HTML of a document or of rich text needs: the rules of the classes of its blocks and
// highlights, each once, in the order they were made.
type CSS struct {
	rules []string
}

// String returns the rules with a blank line between each two, or "" if there are none.
func (c CSS) String() string {
	return strings.Join(c.rules, "\n")
}

// Add adds the rules of o that c lacks to c, in their order, such as those of the rich texts of a page to those of its
// document.
func (c *CSS) Add(o CSS) {
	have := make(map[string]bool, len(c.rules)+len(o.rules))
	for _, r := range c.rules {
		have[r] = true
	}
	for _, r := range o.rules {
		if !have[r] {
			have[r] = true
			c.rules = append(c.rules, r)
		}
	}
}

// rule returns the text of a rule: selector, then decls, declarations such as "color: red", each on its own line.
func rule(selector string, decls []string) string {
	return selector + " " + ruleBody(decls)
}

// ruleBody returns the block of a rule with decls: "{", each of decls indented by two spaces and followed by ";" on its
// own line, and "}", each line ending with a line break.
func ruleBody(decls []string) string {
	var b strings.Builder
	b.WriteString("{\n")
	for _, d := range decls {
		b.WriteString("  " + d + ";\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// important returns the declaration of property with value, marked !important so that it wins over the CSS of the
// theme.
func important(property, value string) string {
	return property + ": " + value + " !important"
}

// presetVar returns the custom property of the preset name of kind, a key of the presets in theme.yaml: "--", kind in
// kebab case, "-" and name, such as "--font-size-large" for fontSize and large.
func presetVar(kind, name string) string {
	var b strings.Builder
	b.WriteString("--")
	for _, r := range kind {
		if 'A' <= r && r <= 'Z' {
			b.WriteByte('-')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	b.WriteString("-" + name)
	return b.String()
}

// cssValue returns the CSS of v, a value at path+v.Path() such as a value of a style: the custom property of its preset
// in var(), or its value as written. ok is false if v is invalid or the theme does not allow it, which it warns about.
func (w *walker) cssValue(v content.StyleValue, path string) (css string, ok bool) {
	p := path + v.Path()
	if err := v.Check(); err != nil {
		var e *content.Error
		if !errors.As(err, &e) {
			e = &content.Error{Msg: err.Error()}
		}
		w.warnf(p+e.Path, "%s; ignored", e.Msg)
		return "", false
	}
	if v.Preset != "" {
		if err := w.r.settings.CheckPreset(v.Kind, v.Preset); err != nil {
			w.warnf(p+"/preset", "%v; ignored", err)
			return "", false
		}
		return "var(" + presetVar(v.Kind, v.Preset) + ")", true
	}
	if err := w.r.settings.CheckCustom(v.Kind); err != nil {
		w.warnf(p+"/value", "%v; ignored", err)
		return "", false
	}
	return v.Value, true
}

// addRules adds rules, those of the class name, to the CSS and reports true, unless the CSS has rules of name.
func (w *walker) addRules(name string, rules ...string) bool {
	if w.classes[name] {
		return false
	}
	if w.classes == nil {
		w.classes = make(map[string]bool)
	}
	w.classes[name] = true
	w.css.rules = append(w.css.rules, rules...)
	return true
}
