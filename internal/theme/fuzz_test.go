package theme

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// exampleFS returns the files of the sample theme in a MapFS.
func exampleFS(tb testing.TB) fstest.MapFS {
	tb.Helper()
	dir := os.DirFS(exampleDir)
	fsys := fstest.MapFS{}
	err := fs.WalkDir(dir, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(dir, p)
		fsys[p] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		tb.Fatal(err)
	}
	return fsys
}

// FuzzLoad loads the sample theme with other theme.yaml and translations/en.yaml files. Load returns a theme that
// chooses the page templates of the sample and renders its pages in en, or sorted Errors, each in a file and, in
// theme.yaml and the translation files, at a position in it.
func FuzzLoad(f *testing.F) {
	sample := exampleFS(f)
	settings, en := sample["theme.yaml"].Data, sample["translations/en.yaml"].Data
	replace := func(data []byte, from, to string) []byte {
		if !bytes.Contains(data, []byte(from)) {
			f.Fatalf("%q is not in %q", from, data)
		}
		return bytes.Replace(data, []byte(from), []byte(to), 1)
	}
	f.Add(settings, en)
	f.Add(settings, []byte(string(en)+"postedBy: \"Posted by {name}\"\n"))
	for _, s := range [][]byte{
		replace(settings, `"#1e73be"`, "#1e73be"),
		replace(settings, "defaultLanguage: ja", "defaultLanguage: en"),
		replace(settings, "defaultLanguage: ja", "defaultLanguage: fr"),
		replace(settings, "defaultLanguage: ja\n", ""),
		replace(settings, "  landing: ランディングページ\n", "  landing: ランディングページ\n  wide: 幅広\n"),
		replace(settings, "pageTemplates:\n  landing: ランディングページ\n", ""),
		replace(settings, "styles: [textColor,", "styles: [color,"),
		replace(settings, "schemaVersion: 1", "schemaVersion: 2"),
		nil,
	} {
		f.Add(s, en)
	}
	for _, e := range [][]byte{
		replace(en, `"Page {page} of {total}"`, `"Page {page}"`),
		replace(en, "readMore: Read more", `readMore: "Read {more}"`),
		replace(en, `"Page {page} of {total}"`, "{page} of {total}"),
		replace(en, "next: Next", "next: [Next]"),
		[]byte(string(en) + "home: Home\n"),
		nil,
	} {
		f.Add(settings, e)
	}
	f.Fuzz(func(t *testing.T, settings, en []byte) {
		fsys := maps.Clone(sample)
		fsys["theme.yaml"] = &fstest.MapFile{Data: settings}
		fsys["translations/en.yaml"] = &fstest.MapFile{Data: en}
		th, err := Load(fsys)
		if err != nil {
			var es content.Errors
			if th != nil || !errors.As(err, &es) || len(es) == 0 {
				t.Fatalf("Load() = %v, %v (%T); want nil and Errors", th, err, err)
			}
			if !slices.IsSortedFunc(es, compareErrors) {
				t.Errorf("Load() errors are not sorted:\n%v", err)
			}
			for _, e := range es {
				if e.File == "" || e.Msg == "" {
					t.Errorf("Load() = %q, without a file or a message", e)
					continue
				}
				inYAML := e.File == "theme.yaml" || strings.HasPrefix(e.File, "translations/")
				if !inYAML || strings.HasPrefix(e.Msg, "too large") {
					continue
				}
				file := fsys[e.File]
				if file == nil {
					t.Errorf("Load() = %q, in a file that the theme does not have", e)
					continue
				}
				lines := strings.Split(string(file.Data), "\n")
				if e.Line < 1 || e.Line > len(lines) ||
					e.Column < 1 || e.Column > utf8.RuneCountInString(lines[e.Line-1])+1 {
					t.Errorf("Load() = %q, outside the file", e)
				}
			}
			return
		}
		r, err := th.Renderer("en", "")
		if err != nil {
			t.Fatalf("Renderer(en) = %v", err)
		}
		for _, p := range examplePages {
			if got := th.Template(p.page); got != p.template {
				t.Errorf("Template(%+v) = %q, want %q", p.page, got, p.template)
			}
			data := exampleData(p.template, "en", "")
			if err := r.Execute(io.Discard, p.template, data); err != nil {
				t.Errorf("Execute(%s) in en = %v", p.template, err)
			}
		}
	})
}
