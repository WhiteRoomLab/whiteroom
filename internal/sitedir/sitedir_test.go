package sitedir

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// IDs from the tests of internal/content.
const (
	entryID    = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5"
	pageID     = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"
	termID     = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8"
	mediaID    = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
	authorID   = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"
	menuID     = "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4"
	redirectID = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"
	revisionID = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9"
)

// original is the original of the medium in validSite.
const original = "GIF89a"

// hashOf returns the Media.Hash of data and its hex digits, the name of its file in files/.
func hashOf(data string) (hash, digits string) {
	sum := sha256.Sum256([]byte(data))
	digits = hex.EncodeToString(sum[:])
	return "sha256:" + digits, digits
}

// validSite returns the files of a site directory without problems: documents of every kind but forms, and files
// that Dir ignores or does not read.
func validSite() map[string]string {
	_, digits := hashOf(original)
	return map[string]string{
		"site/settings.json":                `{"schemaVersion": 1}`,
		"site/schema.yaml":                  "schemaVersion: 1\n",
		"entries/" + entryID + ".json":      `{"id": "` + entryID + `"}`,
		"entries/" + pageID + ".json":       `{"id": "` + pageID + `"}`,
		"terms/" + termID + ".json":         `{"id": "` + termID + `"}`,
		"media/" + mediaID + ".json":        `{"id": "` + mediaID + `"}`,
		"authors/" + authorID + ".json":     `{"id": "` + authorID + `"}`,
		"menus/" + menuID + ".json":         `{"id": "` + menuID + `"}`,
		"redirects/" + redirectID + ".json": `{"id": "` + redirectID + `"}`,
		"revisions/" + revisionID + ".json": `{"title": "About"}`,
		"files/" + digits:                   original,
		"theme/theme.yaml":                  "schemaVersion: 1\n",

		"README.md":           "# Site\n",
		".git/HEAD":           "ref: refs/heads/main\n",
		"site/README.md":      "# Settings\n",
		"entries/.DS_Store":   "",
		"entries/.git/HEAD":   "ref: refs/heads/main\n",
		"revisions/README.md": "# Revisions\n",
		"files/README.md":     "# Originals\n",
	}
}

// writeTree writes files, by their paths with "/", into a new directory, and returns the directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// symlink makes name, a path with "/" in dir, a symbolic link to target.
func symlink(t *testing.T, dir, target, name string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(name))); err != nil {
		t.Skipf("cannot make symbolic links: %v", err)
	}
}

func openDir(t *testing.T, dir string) *Dir {
	t.Helper()
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// checkFile checks that f is the file name of files.
func checkFile(t *testing.T, what string, f content.SiteFile, name string, files map[string]string) {
	t.Helper()
	if f.Name != name || string(f.Data) != files[name] {
		t.Errorf("%s = %q, %q; want %q, %q", what, f.Name, f.Data, name, files[name])
	}
}

// checkErrors checks that err is content.Errors with the messages of want, in their order.
func checkErrors(t *testing.T, what string, err error, want []string) {
	t.Helper()
	var es content.Errors
	if !errors.As(err, &es) {
		t.Errorf("%s = %v (%T), want content.Errors", what, err, err)
		return
	}
	got := make([]string, len(es))
	for i, e := range es {
		got[i] = e.Error()
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s errors:\n%s\nwant:\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestValidSite(t *testing.T) {
	files := validSite()
	dir := writeTree(t, files)
	symlink(t, dir, "site", "link")
	symlink(t, dir, "../site/settings.json", "entries/.link.json")
	symlink(t, dir, "../site/settings.json", "revisions/rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W8.json")
	d := openDir(t, dir)

	settings, schema, err := d.Settings()
	if err != nil {
		t.Fatalf("Settings() = %v", err)
	}
	checkFile(t, "Settings() settings", settings, "site/settings.json", files)
	checkFile(t, "Settings() schema", schema, "site/schema.yaml", files)

	for _, tt := range []struct {
		kind content.RefKind
		dir  string
		ids  []string
	}{
		{content.RefEntry, "entries", []string{entryID, pageID}},
		{content.RefTerm, "terms", []string{termID}},
		{content.RefMedia, "media", []string{mediaID}},
		{content.RefAuthor, "authors", []string{authorID}},
		{content.RefMenu, "menus", []string{menuID}},
		{content.RefForm, "forms", nil},
		{content.RefRedirect, "redirects", []string{redirectID}},
	} {
		docs, err := d.Documents(tt.kind)
		if err != nil || docs == nil {
			t.Errorf("Documents(%q) = %v, %v", tt.kind, docs, err)
			continue
		}
		if got := slices.Sorted(maps.Keys(docs)); !slices.Equal(got, tt.ids) {
			t.Errorf("Documents(%q) has %q, want %q", tt.kind, got, tt.ids)
		}
		for id, f := range docs {
			checkFile(t, fmt.Sprintf("Documents(%q)[%s]", tt.kind, id), f, tt.dir+"/"+id+".json", files)
		}
	}

	rev, err := d.Revision(revisionID)
	if err != nil {
		t.Fatalf("Revision() = %v", err)
	}
	checkFile(t, "Revision()", rev, "revisions/"+revisionID+".json", files)

	hash, _ := hashOf(original)
	f, err := d.OpenMedia(hash)
	if err != nil {
		t.Fatalf("OpenMedia() = %v", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() != int64(len(original)) {
		t.Errorf("Stat() of OpenMedia() = %v, %v; want the size %d", info, err, len(original))
	}
	if data, err := io.ReadAll(f); err != nil || string(data) != original {
		t.Errorf("OpenMedia() reads %q, %v; want %q", data, err, original)
	}

	fsys, err := d.Theme()
	if err != nil {
		t.Fatalf("Theme() = %v", err)
	}
	if data, err := fs.ReadFile(fsys, "theme.yaml"); err != nil || string(data) != files["theme/theme.yaml"] {
		t.Errorf("theme.yaml of Theme() = %q, %v", data, err)
	}
}

func TestDocumentsErrors(t *testing.T) {
	const (
		bigID  = "ent_01JB8Y6T2SA3F6H9J2K5M8N1P4"
		linkID = "ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9"
		dirID  = "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8"
	)
	dir := writeTree(t, map[string]string{
		"entries/" + entryID + ".json":   "{}",
		"entries/" + pageID + ".txt":     "{}",
		"entries/" + dirID + ".json/a":   "{}",
		"entries/foo.json":               "{}",
		"entries/" + termID + ".json":    "{}",
		"entries/.DS_Store":              "",
		"terms/" + entryID + ".json":     "{}",
		"redirects/" + redirectID + "/a": "{}",
	})
	symlink(t, dir, entryID+".json", "entries/"+linkID+".json")
	big := filepath.Join(dir, "entries", bigID+".json")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, MaxDocumentBytes+1); err != nil {
		t.Fatal(err)
	}
	d := openDir(t, dir)
	const use = ` and a ULID (26 characters in upper case)`
	tests := []struct {
		kind content.RefKind
		want []string
	}{
		{
			kind: content.RefEntry,
			want: []string{
				"entries/" + pageID + ".txt: invalid name: use <ID>.json",
				"entries/" + bigID + ".json: too large: 67108865 bytes (max 67108864)",
				"entries/" + linkID + ".json: symbolic links are not allowed",
				"entries/" + dirID + ".json: must be a regular file",
				`entries/foo.json: invalid name: use <ID>.json; invalid entry ID "foo": use "ent_"` + use,
				"entries/" + termID + `.json: invalid name: use <ID>.json; invalid entry ID "` + termID + `": use "ent_"` + use,
			},
		},
		{
			kind: content.RefTerm,
			want: []string{
				"terms/" + entryID + `.json: invalid name: use <ID>.json; invalid term ID "` + entryID + `": use "trm_"` + use,
			},
		},
		{
			kind: content.RefRedirect,
			want: []string{"redirects/" + redirectID + ": must be a regular file"},
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			docs, err := d.Documents(tt.kind)
			if docs != nil {
				t.Errorf("Documents() = %v with an error", docs)
			}
			checkErrors(t, "Documents()", err, tt.want)
			if _, again := d.Documents(tt.kind); again == nil || err == nil || again.Error() != err.Error() {
				t.Errorf("Documents() again = %v, want the same errors", again)
			}
		})
	}
}

func TestDocumentsDirectories(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"terms":                      "",
		"other/" + entryID + ".json": "{}",
	})
	symlink(t, dir, "other", "entries")
	d := openDir(t, dir)
	for _, tt := range []struct {
		kind content.RefKind
		want string
	}{
		{content.RefEntry, "entries: symbolic links are not allowed"},
		{content.RefTerm, "terms: must be a directory"},
	} {
		docs, err := d.Documents(tt.kind)
		if docs != nil {
			t.Errorf("Documents(%q) = %v with an error", tt.kind, docs)
		}
		checkErrors(t, fmt.Sprintf("Documents(%q)", tt.kind), err, []string{tt.want})
	}
	for _, kind := range []content.RefKind{content.RefRevision, content.RefTranslationGroup, content.RefUser, "page"} {
		docs, err := d.Documents(kind)
		if want := `unsupported kind "` + string(kind) + `"`; docs != nil || err == nil || err.Error() != want {
			t.Errorf("Documents(%q) = %v, %v; want an error %q", kind, docs, err, want)
		}
	}
}

func TestSettingsErrors(t *testing.T) {
	const (
		settings = "site/settings.json"
		schema   = "site/schema.yaml"
	)
	tests := []struct {
		name  string
		files map[string]string
		links map[string]string // targets by name
		want  []string
	}{
		{
			name:  "missing schema",
			files: map[string]string{settings: "{}"},
			want:  []string{"site/schema.yaml: missing file"},
		},
		{
			name: "missing directory",
			want: []string{"site/schema.yaml: missing file", "site/settings.json: missing file"},
		},
		{
			name:  "settings as a symbolic link",
			files: map[string]string{schema: "", "settings.json": "{}"},
			links: map[string]string{settings: "../settings.json"},
			want:  []string{"site/settings.json: symbolic links are not allowed"},
		},
		{
			name:  "settings as a directory",
			files: map[string]string{schema: "", settings + "/a": "{}"},
			want:  []string{"site/settings.json: must be a regular file"},
		},
		{
			name:  "problems of both",
			files: map[string]string{schema + "/a": "", "settings.json": "{}"},
			links: map[string]string{settings: "../settings.json"},
			want: []string{
				"site/schema.yaml: must be a regular file",
				"site/settings.json: symbolic links are not allowed",
			},
		},
		{
			name:  "directory as a symbolic link",
			files: map[string]string{"other/settings.json": "{}", "other/schema.yaml": ""},
			links: map[string]string{"site": "other"},
			want:  []string{"site: symbolic links are not allowed"},
		},
		{
			name:  "directory as a file",
			files: map[string]string{"site": ""},
			want:  []string{"site: must be a directory"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeTree(t, tt.files)
			for name, target := range tt.links {
				symlink(t, dir, target, name)
			}
			settings, schema, err := openDir(t, dir).Settings()
			if settings.Name != "" || settings.Data != nil || schema.Name != "" || schema.Data != nil {
				t.Errorf("Settings() = %q, %q with an error", settings, schema)
			}
			checkErrors(t, "Settings()", err, tt.want)
		})
	}
}

func TestRevision(t *testing.T) {
	const (
		linkID = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W1"
		dirID  = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W2"
		bigID  = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W3"
	)
	files := map[string]string{
		"revisions/" + revisionID + ".json": `{"title": "About"}`,
		"revisions/" + dirID + ".json/a":    "{}",
		"revisions/rev_/a.json":             "{}",
		"site/settings.json":                `{"schemaVersion": 1}`,
	}
	dir := writeTree(t, files)
	symlink(t, dir, revisionID+".json", "revisions/"+linkID+".json")
	big := filepath.Join(dir, "revisions", bigID+".json")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, MaxDocumentBytes+1); err != nil {
		t.Fatal(err)
	}
	d := openDir(t, dir)
	f, err := d.Revision(revisionID)
	if err != nil {
		t.Fatalf("Revision() = %v", err)
	}
	checkFile(t, "Revision()", f, "revisions/"+revisionID+".json", files)

	invalid := func(id string) string {
		return `invalid revision ID "` + id + `": use "rev_" and a ULID (26 characters in upper case)`
	}
	for _, tt := range []struct {
		id      string
		want    string
		problem bool // content.Errors
	}{
		{id: "rev_01jb8x9y3ze5g8k1n4q7s2v6w9", want: invalid("rev_01jb8x9y3ze5g8k1n4q7s2v6w9")},
		{id: "rev_../x", want: invalid("rev_../x")},
		{id: "rev_/../../site/settings", want: invalid("rev_/../../site/settings")},
		{id: "ent_01JB8X9Y3ZE5G8K1N4Q7S2V6W9", want: invalid("ent_01JB8X9Y3ZE5G8K1N4Q7S2V6W9")},
		{id: "", want: invalid("")},
		{id: linkID, want: "revisions/" + linkID + ".json: symbolic links are not allowed", problem: true},
		{id: dirID, want: "revisions/" + dirID + ".json: must be a regular file", problem: true},
		{id: bigID, want: "revisions/" + bigID + ".json: too large: 67108865 bytes (max 67108864)", problem: true},
	} {
		f, err := d.Revision(tt.id)
		var es content.Errors
		if f.Name != "" || f.Data != nil || err == nil || err.Error() != tt.want || errors.As(err, &es) != tt.problem ||
			errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Revision(%q) = %q, %v (%T); want an error %q", tt.id, f, err, err, tt.want)
		}
	}

	const missingID = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W4"
	f, err = d.Revision(missingID)
	var pe *fs.PathError
	if want := "open revisions/" + missingID + ".json: file does not exist"; f.Name != "" || f.Data != nil ||
		!errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pe) || err.Error() != want {
		t.Errorf("Revision() of a missing revision = %q, %v (%T); want %q", f, err, err, want)
	}
}

func TestRevisionsDirectory(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		link  string // the target of revisions
		want  string // the message of the problem; "" for a missing revision
	}{
		{name: "missing", files: map[string]string{"site/settings.json": "{}"}},
		{
			name:  "symbolic link",
			files: map[string]string{"other/" + revisionID + ".json": "{}"},
			link:  "other",
			want:  "revisions: symbolic links are not allowed",
		},
		{name: "file", files: map[string]string{"revisions": ""}, want: "revisions: must be a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeTree(t, tt.files)
			if tt.link != "" {
				symlink(t, dir, tt.link, "revisions")
			}
			_, err := openDir(t, dir).Revision(revisionID)
			if tt.want == "" {
				if !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Revision() = %v, want an error that wraps fs.ErrNotExist", err)
				}
				return
			}
			checkErrors(t, "Revision()", err, []string{tt.want})
		})
	}
}

func TestOpenMedia(t *testing.T) {
	hash, digits := hashOf(original)
	_, linkDigits := hashOf("link")
	_, dirDigits := hashOf("directory")
	missing, missingDigits := hashOf("missing")
	dir := writeTree(t, map[string]string{
		"files/" + digits:           original,
		"files/" + dirDigits + "/a": original,
	})
	symlink(t, dir, digits, "files/"+linkDigits)
	d := openDir(t, dir)

	use := `: use "sha256:" and 64 lower case hex digits`
	for _, h := range []string{
		"sha256:" + strings.ToUpper(digits),
		digits,
		"sha256:" + digits[:63],
		"sha256:" + digits + "0",
		"sha512:" + digits,
		"SHA256:" + digits,
		"sha256:../" + digits[3:],
		"",
	} {
		f, err := d.OpenMedia(h)
		if want := `invalid hash "` + h + `"` + use; f != nil || err == nil || err.Error() != want {
			t.Errorf("OpenMedia(%q) = %v, %v; want an error %q", h, f, err, want)
		}
	}

	for _, tt := range []struct {
		digits string
		want   string
	}{
		{linkDigits, "files/" + linkDigits + ": symbolic links are not allowed"},
		{dirDigits, "files/" + dirDigits + ": must be a regular file"},
	} {
		f, err := d.OpenMedia("sha256:" + tt.digits)
		if f != nil {
			t.Errorf("OpenMedia() = %v with an error", f)
		}
		checkErrors(t, "OpenMedia()", err, []string{tt.want})
	}

	f, err := d.OpenMedia(missing)
	var pe *fs.PathError
	if want := "open files/" + missingDigits + ": file does not exist"; f != nil || !errors.Is(err, fs.ErrNotExist) ||
		!errors.As(err, &pe) || err.Error() != want {
		t.Errorf("OpenMedia() of a missing original = %v, %v (%T); want %q", f, err, err, want)
	}

	f, err = d.OpenMedia(hash)
	if err != nil {
		t.Fatalf("OpenMedia() = %v", err)
	}
	defer func() { _ = f.Close() }()
	if data, err := io.ReadAll(f); err != nil || string(data) != original {
		t.Errorf("OpenMedia() reads %q, %v; want %q", data, err, original)
	}
}

func TestFilesDirectory(t *testing.T) {
	hash, digits := hashOf(original)
	tests := []struct {
		name  string
		files map[string]string
		link  string // the target of files
		want  string // the message of the problem; "" for a missing original
	}{
		{name: "missing", files: map[string]string{"site/settings.json": "{}"}},
		{
			name:  "symbolic link",
			files: map[string]string{"other/" + digits: original},
			link:  "other",
			want:  "files: symbolic links are not allowed",
		},
		{name: "file", files: map[string]string{"files": ""}, want: "files: must be a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeTree(t, tt.files)
			if tt.link != "" {
				symlink(t, dir, tt.link, "files")
			}
			f, err := openDir(t, dir).OpenMedia(hash)
			if f != nil {
				t.Errorf("OpenMedia() = %v with an error", f)
			}
			if tt.want == "" {
				if !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("OpenMedia() = %v, want an error that wraps fs.ErrNotExist", err)
				}
				return
			}
			checkErrors(t, "OpenMedia()", err, []string{tt.want})
		})
	}
}

func TestTheme(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"theme/theme.yaml":           "schemaVersion: 1\n",
		"theme/templates/entry.html": "entry",
		"site/settings.json":         `{"schemaVersion": 1}`,
	})
	outside := writeTree(t, map[string]string{"secret.txt": "secret"})
	symlink(t, dir, filepath.Join(outside, "secret.txt"), "theme/outside.txt")
	symlink(t, dir, "../site/settings.json", "theme/settings.json")
	symlink(t, dir, "theme.yaml", "theme/inside.yaml")
	d := openDir(t, dir)
	fsys, err := d.Theme()
	if err != nil {
		t.Fatalf("Theme() = %v", err)
	}
	if data, err := fs.ReadFile(fsys, "theme.yaml"); err != nil || string(data) != "schemaVersion: 1\n" {
		t.Errorf("theme.yaml = %q, %v", data, err)
	}
	for _, name := range []string{"outside.txt", "settings.json"} {
		if data, err := fs.ReadFile(fsys, name); err == nil {
			t.Errorf("%s, a symbolic link out of the theme, = %q", name, data)
		}
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	types := make(map[string]fs.FileMode)
	for _, e := range entries {
		types[e.Name()] = e.Type()
	}
	want := map[string]fs.FileMode{
		"inside.yaml":   fs.ModeSymlink,
		"outside.txt":   fs.ModeSymlink,
		"settings.json": fs.ModeSymlink,
		"templates":     fs.ModeDir,
		"theme.yaml":    0,
	}
	if !maps.Equal(types, want) {
		t.Errorf("types in ReadDir() = %v, want %v", types, want)
	}

	again, err := d.Theme()
	if err != nil {
		t.Fatalf("Theme() again = %v", err)
	}
	if data, err := fs.ReadFile(again, "templates/entry.html"); err != nil || string(data) != "entry" {
		t.Errorf("templates/entry.html = %q, %v", data, err)
	}

	if err := d.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if data, err := fs.ReadFile(fsys, "theme.yaml"); err == nil {
		t.Errorf("theme.yaml after Close() = %q", data)
	}
	if _, err := d.Theme(); err == nil {
		t.Error("Theme() after Close() = nil error")
	}
}

func TestThemeErrors(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		link  string // the target of theme
		want  string
	}{
		{name: "missing", files: map[string]string{"site/settings.json": "{}"}, want: "theme: missing directory"},
		{
			name:  "symbolic link",
			files: map[string]string{"other/theme.yaml": "schemaVersion: 1\n"},
			link:  "other",
			want:  "theme: symbolic links are not allowed",
		},
		{name: "file", files: map[string]string{"theme": ""}, want: "theme: must be a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeTree(t, tt.files)
			if tt.link != "" {
				symlink(t, dir, tt.link, "theme")
			}
			fsys, err := openDir(t, dir).Theme()
			if fsys != nil {
				t.Errorf("Theme() = %v with an error", fsys)
			}
			checkErrors(t, "Theme()", err, []string{tt.want})
		})
	}
}

func TestUnreadable(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("every file is readable") // to root, or with a mode that Windows ignores
	}
	dir := writeTree(t, map[string]string{
		"entries/" + entryID + ".json": "{}",
		"terms/" + termID + ".json":    "{}",
	})
	unreadable := func(name string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
	}
	unreadable("entries/" + entryID + ".json")
	unreadable("terms")
	d := openDir(t, dir)
	_, err := d.Documents(content.RefEntry)
	checkErrors(t, "Documents(entry)", err, []string{"entries/" + entryID + ".json: cannot read: permission denied"})
	_, err = d.Documents(content.RefTerm)
	checkErrors(t, "Documents(term)", err, []string{"terms: cannot read: permission denied"})
}

// TestConcurrent calls the methods of a Dir at once, for the race detector.
func TestConcurrent(t *testing.T) {
	d := openDir(t, writeTree(t, validSite()))
	hash, _ := hashOf(original)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, _, err := d.Settings(); err != nil {
				t.Errorf("Settings() = %v", err)
			}
			if _, err := d.Documents(content.RefEntry); err != nil {
				t.Errorf("Documents() = %v", err)
			}
			if _, err := d.Revision(revisionID); err != nil {
				t.Errorf("Revision() = %v", err)
			}
			if f, err := d.OpenMedia(hash); err != nil {
				t.Errorf("OpenMedia() = %v", err)
			} else {
				_ = f.Close()
			}
			fsys, err := d.Theme()
			if err == nil {
				_, err = fs.ReadFile(fsys, "theme.yaml")
			}
			if err != nil {
				t.Errorf("Theme() = %v", err)
			}
		})
	}
	wg.Wait()
}

func TestOpenErrors(t *testing.T) {
	dir := writeTree(t, map[string]string{"file": ""})
	for _, name := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "file")} {
		if d, err := Open(name); err == nil {
			_ = d.Close()
			t.Errorf("Open(%q) = nil error", name)
		}
	}
}
