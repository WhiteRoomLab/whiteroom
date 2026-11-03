package content

import "strings"

// SiteFile is a file of a site, such as a file of the site settings or a JSON document ("entries/<ID>.json"). Name is
// its name in errors, such as "site/schema.yaml".
type SiteFile struct {
	Name string
	Data []byte
}

var documentDirs = map[RefKind]string{
	RefEntry:    "entries",
	RefTerm:     "terms",
	RefMedia:    "media",
	RefAuthor:   "authors",
	RefMenu:     "menus",
	RefForm:     "forms",
	RefRedirect: "redirects",
}

// DocumentDir returns the directory of the documents of kind in a site directory (4.3), such as "entries", which also
// names them in errors, as in "entries/<ID>.json". Only the kinds of the entries, the terms, the media, the authors,
// the menus, the forms and the redirects have one: for the others, such as RefRevision, it returns "".
func DocumentDir(kind RefKind) string {
	return documentDirs[kind]
}

// LoadSite reads the site settings from settings, the settings document in JSON that the admin screen writes, and
// schema, the schema file in YAML that developers write, and validates them with lookup as Validate does. The error is
// Errors, each with the Name of its file and the line and the column of the problem; for a missing item, those of the
// item that would have it. If either file does not read, the errors are those of reading the two, settings first,
// without validation; otherwise, those of Validate, in its order. The site is nil with an error.
func LoadSite(settings, schema SiteFile, lookup func(kind RefKind, id string) (Ref, bool)) (*Site, error) {
	s := new(Site)
	settingsPositions, err := jsonPositions(settings.Data)
	if err == nil {
		err = s.UnmarshalSettings(settings.Data)
	}
	errs := locate(err, settings.Name, settingsPositions)
	schemaPositions, err := s.unmarshalSchema(schema.Data)
	errs = append(errs, locate(err, schema.Name, schemaPositions)...)
	if len(errs) > 0 {
		return nil, errs
	}
	for _, e := range locate(s.Validate(lookup), "", nil) {
		file, positions := settings.Name, settingsPositions
		if inSchemaFile(e.Path) {
			file, positions = schema.Name, schemaPositions
		}
		errs = append(errs, locate(e, file, positions)...)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return s, nil
}

// inSchemaFile reports whether path, the JSON Pointer of an error of Validate, is in the schema file: whether its first
// key is one that decodeSchema reads, apart from schemaVersion.
func inSchemaFile(path string) bool {
	key, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return key == "authorUrl" || key == "taxonomies" || key == "types"
}
