package content

import (
	"encoding/json/v2"
	"maps"
	"slices"
)

// Localized holds a text in each language, such as {"ja": "会社外観", "en": "Our office building"}, for an entity that is not split by language.
type Localized map[string]string

func (x *Localized) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Localized) decode(d *decoder) error {
	v := make(Localized)
	err := d.object(func(language string) error {
		s, err := d.string()
		v[language] = s
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

// MarshalJSON writes the languages in sorted order.
func (x Localized) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string(x), json.Deterministic(true))
}

// normalize normalizes the texts in place as normalizeText does and removes the languages of which nothing is left.
func (x *Localized) normalize(multiline bool) {
	for language, s := range *x {
		if n := normalizeText(s, multiline); n != "" {
			(*x)[language] = n
		} else {
			delete(*x, language)
		}
	}
	// Empty is nil, as UnmarshalJSON makes it.
	if len(*x) == 0 {
		*x = nil
	}
}

// check checks x at path; the texts may have line breaks and tabs if multiline is true.
// The languages that site does not have are reported in sorted order after the others, without checking their texts.
func (x Localized) check(c *checker, path string, site *Site, multiline bool) {
	for _, language := range site.Languages {
		s, ok := x[language]
		if !ok {
			continue
		}
		p := path + "/" + escapePointer(language)
		switch {
		case s == "":
			c.addf(p, "empty value; omit it")
		case multiline:
			c.add(p, checkText(s, true))
		default:
			c.add(p, checkLine(s))
		}
	}
	for _, language := range slices.Sorted(maps.Keys(x)) {
		c.add(path+"/"+escapePointer(language), checkSiteLanguage(site, language))
	}
}
