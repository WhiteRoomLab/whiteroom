package theme

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

// yamlFile is a YAML file that has loaded, such as theme.yaml, in which the checks across files find problems.
type yamlFile struct {
	name      string
	data      []byte
	positions map[string]yaml.Position // by JSON Pointer, read when first needed
}

func (f *yamlFile) position(ptr string) yaml.Position {
	if f.positions == nil {
		// The file has loaded, so it reads again.
		_, f.positions, _ = yaml.ToJSON(f.data)
	}
	return f.positions[ptr]
}

// addAt adds the problem with the value at ptr in f, at its position.
func (l *loader) addAt(f *yamlFile, ptr, format string, args ...any) {
	pos := f.position(ptr)
	l.errs = append(l.errs, &content.Error{
		Path: ptr, Msg: fmt.Sprintf(format, args...), File: f.name, Line: pos.Line, Column: pos.Column,
	})
}

// translationFile is a translation file that has loaded.
type translationFile struct {
	*yamlFile
	texts content.ThemeTranslation
}

// readTranslations reads the translation files, translations/<language>.yaml, and reports whether it could read the
// directory.
func (l *loader) readTranslations() bool {
	entries, ok := l.readDir("translations")
	if !ok {
		return false
	}
	for _, d := range entries {
		name := d.Name()
		p := "translations/" + name
		lang, isYAML := strings.CutSuffix(name, ".yaml")
		langErr := content.CheckLanguage(lang)
		if isYAML && langErr == nil {
			l.languages = append(l.languages, lang)
		}
		if d.IsDir() {
			l.addf(p, "subdirectories are not allowed")
			continue
		}
		if !l.checkType(p, d, false) {
			continue
		}
		if !isYAML {
			l.addf(p, "unknown file %q; translation files are <language>.yaml files, such as ja.yaml", name)
			continue
		}
		if langErr != nil {
			l.add(p, langErr)
			continue
		}
		l.loadTranslation(p, lang, d)
	}
	return true
}

func (l *loader) loadTranslation(p, lang string, d fs.DirEntry) {
	data, ok := l.readFile(p, d, content.MaxTextBytes)
	if !ok {
		return
	}
	x, err := content.LoadThemeTranslation(p, data)
	if err != nil {
		l.addErrors(p, err)
		return
	}
	texts := make(map[string]string, len(x))
	for _, t := range x {
		texts[t.Key] = t.Text
	}
	l.t.translations[lang] = texts
	l.translations[lang] = &translationFile{yamlFile: &yamlFile{name: p, data: data}, texts: x}
}

// checkTranslations checks the translation files against theme.yaml and the default translation, and keeps the keys
// of the default translation. A theme with translation files needs defaultLanguage, and defaultLanguage needs its
// file. The keys of the other files must be in the default translation, with the same placeholders.
func (l *loader) checkTranslations() {
	if l.settings == nil || l.translationsUnread {
		return
	}
	lang := l.t.Settings.DefaultLanguage
	if lang == "" {
		if len(l.languages) > 0 {
			l.addAt(l.settings, "", `missing key "defaultLanguage"; a theme with translations needs it`)
		}
		return
	}
	if !slices.Contains(l.languages, lang) {
		l.addAt(l.settings, "/defaultLanguage", "missing translations/%s.yaml for the default language", lang)
		return
	}
	def := l.translations[lang]
	if def == nil {
		return
	}
	for _, x := range def.texts {
		l.t.keys = append(l.t.keys, x.Key)
	}
	texts := l.t.translations[lang]
	for _, other := range l.languages {
		f := l.translations[other]
		if other == lang || f == nil {
			continue
		}
		for _, x := range f.texts {
			p := "/" + x.Key
			text, ok := texts[x.Key]
			if !ok {
				l.addAt(f.yamlFile, p, "unknown key; keys must be in the default translation, %s", def.name)
				continue
			}
			want := content.ThemeText{Text: text}.Placeholders()
			unknown, missing := diffNames(x.Placeholders(), want)
			if len(unknown) > 0 {
				has := "no placeholders"
				if len(want) > 0 {
					has = braces(want)
				}
				l.addAt(f.yamlFile, p, "unknown %s; the default translation has %s", placeholders(unknown), has)
			}
			if len(missing) > 0 {
				l.addAt(f.yamlFile, p, "missing %s of the default translation", placeholders(missing))
			}
		}
	}
}

// diffNames returns the names in got that want does not have, and those in want that got does not have, in their
// orders.
func diffNames(got, want []string) (unknown, missing []string) {
	in := func(names []string) map[string]bool {
		m := make(map[string]bool, len(names))
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	inGot, inWant := in(got), in(want)
	for _, n := range got {
		if !inWant[n] {
			unknown = append(unknown, n)
		}
	}
	for _, n := range want {
		if !inGot[n] {
			missing = append(missing, n)
		}
	}
	return unknown, missing
}

// placeholders returns "placeholder {page}" for one name, or "placeholders {page}, {total}" for more.
func placeholders(names []string) string {
	if len(names) == 1 {
		return "placeholder " + braces(names)
	}
	return "placeholders " + braces(names)
}

func braces(names []string) string {
	return "{" + strings.Join(names, "}, {") + "}"
}
