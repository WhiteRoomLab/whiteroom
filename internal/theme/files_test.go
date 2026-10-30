package theme

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const unknownTemplate = ` (use entry.html, entry-{type}.html, page-{template}.html, list.html, posts.html, term.html, ` +
	`term-{taxonomy}.html, archive-{type}.html, author.html, 404.html)`

// TestLoadIgnored checks that Load reads nothing of hidden entries and of the other entries at the root.
func TestLoadIgnored(t *testing.T) {
	tests := []struct {
		name    string
		changes map[string]*fstest.MapFile
	}{
		{
			name: "other entries at the root",
			changes: map[string]*fstest.MapFile{
				"README.md":            file("# A theme\n"),
				"LICENSE":              file("Apache License"),
				"screenshot.png":       {Data: logoPNG},
				"docs/index.md":        file("# Docs\n"),
				"docs/link":            symlink("/etc/passwd"),
				"src":                  symlink("/"),
				"fifo":                 special(fs.ModeNamedPipe),
				"Templates/single.htm": file("x"),
				"theme.json":           file("{}"),
			},
		},
		{
			name: "hidden entries",
			changes: map[string]*fstest.MapFile{
				".git/HEAD":                       file("ref: refs/heads/main\n"),
				".git/hooks/link":                 symlink("/bin/sh"),
				".theme.yaml":                     file("not: [yaml"),
				".DS_Store":                       file("\x00"),
				"templates/.entry.html.swp":       file("\x00"),
				"templates/.#entry.html":          symlink("user@host.1234"),
				"templates/.drafts/single.html":   file("x"),
				"components/.header.html":         file("x"),
				"components/.cache/x.bin":         symlink("/dev/zero"),
				"components/entry/.card.html.swp": special(fs.ModeNamedPipe),
				"assets/.htaccess":                file("Options +Indexes\n"),
				"assets/.well-known/security.txt": file("Contact: x\n"),
				"assets/css/.DS_Store":            file("\x00"),
				"translations/.ja.yaml.swp":       file("\x00"),
				"translations/.old/fr.yaml":       file("x"),
			},
		},
	}
	want, wantTexts := withoutNamespaces(mustLoad(t, themeFS(nil)))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotTexts := withoutNamespaces(mustLoad(t, themeFS(tt.changes)))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Load() = %+v, want %+v", got, want)
			}
			if !reflect.DeepEqual(gotTexts, wantTexts) {
				t.Errorf("namespaces = %q, want %q", gotTexts, wantTexts)
			}
		})
	}
}

func TestLoadEntries(t *testing.T) {
	runLoadTests(t, []loadTest{
		{
			name:    "empty directories",
			changes: map[string]*fstest.MapFile{"components": dir(), "assets/fonts": dir(), "translations": dir()},
		},
		{name: "missing theme.yaml", changes: map[string]*fstest.MapFile{"theme.yaml": nil}, want: []string{"theme.yaml: missing file"}},
		{
			name:    "missing templates",
			changes: map[string]*fstest.MapFile{"templates/entry.html": nil, "templates/list.html": nil},
			want:    []string{"templates: missing directory"},
		},
		{
			name:    "empty theme",
			changes: map[string]*fstest.MapFile{"theme.yaml": nil, "templates/entry.html": nil, "templates/list.html": nil},
			want:    []string{"theme.yaml: missing file", "templates: missing directory"},
		},
		{
			name:    "theme.yaml is a directory",
			changes: map[string]*fstest.MapFile{"theme.yaml": nil, "theme.yaml/schema.yaml": file("schemaVersion: 1\n")},
			want:    []string{"theme.yaml: must be a regular file"},
		},
		{
			name:    "theme.yaml is a symbolic link",
			changes: map[string]*fstest.MapFile{"theme.yaml": symlink("src/theme.yaml"), "src/theme.yaml": file("schemaVersion: 1\n")},
			want:    []string{"theme.yaml: symbolic links are not allowed"},
		},
		{
			name:    "theme.yaml is a named pipe",
			changes: map[string]*fstest.MapFile{"theme.yaml": special(fs.ModeNamedPipe)},
			want:    []string{"theme.yaml: must be a regular file"},
		},
		{
			name: "templates is a file",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": nil, "templates/list.html": nil, "templates": file("entry.html\n"),
			},
			want: []string{"templates: must be a directory"},
		},
		{
			name: "templates is a symbolic link",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html":     nil,
				"templates/list.html":      nil,
				"templates":                symlink("src"),
				"src/entry.html":           file("entry"),
				"src/list.html":            file("list"),
				"src/templates/entry.html": file("entry"),
			},
			want: []string{"templates: symbolic links are not allowed"},
		},
		{
			name: "directories of the wrong kinds",
			changes: map[string]*fstest.MapFile{
				"components":   file("header.html\n"),
				"assets":       symlink("/var/www"),
				"translations": special(fs.ModeSocket),
			},
			want: []string{
				"translations: must be a directory",
				"components: must be a directory",
				"assets: symbolic links are not allowed",
			},
		},
		{
			name: "subdirectories",
			changes: map[string]*fstest.MapFile{
				"templates/parts/header.html": file("header"),
				"templates/entry.html":        nil,
				"templates/entry.html/x":      file("x"),
				"translations/old/ja.yaml":    file("next: 次へ\n"),
			},
			want: []string{
				"translations/old: subdirectories are not allowed",
				"templates/entry.html: subdirectories are not allowed; put components in components/",
				"templates/parts: subdirectories are not allowed; put components in components/",
			},
		},
		{
			name: "symbolic links",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":             file("schemaVersion: 1\ndefaultLanguage: ja\n"),
				"templates/posts.html":   symlink("list.html"),
				"components/header.html": symlink("../templates/entry.html"),
				"components/parts":       symlink("../templates"),
				"components/a/b.html":    symlink("/etc/passwd"),
				"assets/passwd":          symlink("/etc/passwd"),
				"assets/css":             symlink("../src/css"),
				"src/css/style.css":      file("body {}"),
				"translations/ja.yaml":   symlink("ja.yml"),
				"translations/ja.yml":    file("next: 次へ\n"),
			},
			want: []string{
				"translations/ja.yaml: symbolic links are not allowed",
				`translations/ja.yml: unknown file "ja.yml"; translation files are <language>.yaml files, such as ja.yaml`,
				"components/a/b.html: symbolic links are not allowed",
				"components/header.html: symbolic links are not allowed",
				"components/parts: symbolic links are not allowed",
				"templates/posts.html: symbolic links are not allowed",
				"assets/css: symbolic links are not allowed",
				"assets/passwd: symbolic links are not allowed",
			},
		},
		{
			name: "other irregular entries",
			changes: map[string]*fstest.MapFile{
				"templates/posts.html":   special(fs.ModeNamedPipe),
				"components/header.html": special(fs.ModeSocket),
				"assets/null":            special(fs.ModeDevice | fs.ModeCharDevice),
				"assets/disk":            special(fs.ModeDevice),
				"assets/x":               special(fs.ModeIrregular),
				"translations/en.yaml":   special(fs.ModeNamedPipe),
			},
			want: []string{
				`theme.yaml:1:1: missing key "defaultLanguage"; a theme with translations needs it`,
				"translations/en.yaml: must be a regular file",
				"components/header.html: must be a regular file or a directory",
				"templates/posts.html: must be a regular file",
				"assets/disk: must be a regular file or a directory",
				"assets/null: must be a regular file or a directory",
				"assets/x: must be a regular file or a directory",
			},
		},
	})
}

func TestLoadSizes(t *testing.T) {
	limit := content.MaxTextBytes
	// A valid settings file of exactly n bytes.
	settings := func(n int) *fstest.MapFile {
		const head = "schemaVersion: 1\n# "
		return file(head + strings.Repeat("x", n-len(head)))
	}
	text := func(n int) *fstest.MapFile { return file(strings.Repeat("x", n)) }
	runLoadTests(t, []loadTest{
		{
			name: "largest",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":             settings(limit),
				"templates/entry.html":   text(limit),
				"components/header.html": text(limit),
			},
		},
		{
			name: "too large",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":             settings(limit + 1),
				"templates/entry.html":   text(limit + 1),
				"components/header.html": text(limit + 1),
				"translations/ja.yaml":   file("next: " + strings.Repeat("x", limit-5)),
			},
			want: []string{
				"theme.yaml: too large: 1048577 bytes (max 1048576)",
				"translations/ja.yaml: too large: 1048577 bytes (max 1048576)",
				"components/header.html: too large: 1048577 bytes (max 1048576)",
				"templates/entry.html: too large: 1048577 bytes (max 1048576)",
			},
		},
	})
}

// faultyFS is a MapFS with failures: Open and ReadDir fail for the paths in open with their errors, Read for those
// in read, and DirEntry.Info for those in info. The directory entries of the paths in sizes report those sizes, as if
// the files had changed since they were listed.
type faultyFS struct {
	fstest.MapFS
	open, read, info map[string]error
	sizes            map[string]int64
}

func (f faultyFS) Open(name string) (fs.File, error) {
	if err := f.open[name]; err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	file, err := f.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	if err := f.read[name]; err != nil {
		return faultyFile{File: file, name: name, err: err}, nil
	}
	return file, nil
}

func (f faultyFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := f.open[name]; err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	entries, err := f.MapFS.ReadDir(name)
	for i, d := range entries {
		p := path.Join(name, d.Name())
		size, sized := f.sizes[p]
		if err := f.info[p]; err != nil || sized {
			entries[i] = faultyEntry{DirEntry: d, size: size, sized: sized, err: err}
		}
	}
	return entries, err
}

type faultyFile struct {
	fs.File
	name string
	err  error
}

func (f faultyFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: f.name, Err: f.err}
}

type faultyEntry struct {
	fs.DirEntry
	size  int64
	sized bool
	err   error
}

func (e faultyEntry) Info() (fs.FileInfo, error) {
	if e.err != nil {
		return nil, e.err
	}
	info, err := e.DirEntry.Info()
	if err != nil || !e.sized {
		return info, err
	}
	return sizedInfo{FileInfo: info, size: e.size}, nil
}

type sizedInfo struct {
	fs.FileInfo
	size int64
}

func (i sizedInfo) Size() int64 { return i.size }

func TestLoadFaults(t *testing.T) {
	denied := fs.ErrPermission
	broken := errors.New("input/output error")
	tests := []struct {
		name string
		fsys faultyFS
		want []string
	}{
		{
			name: "root",
			fsys: faultyFS{MapFS: themeFS(nil), open: map[string]error{".": denied}},
			want: []string{"cannot read the theme directory: permission denied"},
		},
		{
			name: "directories",
			fsys: faultyFS{
				MapFS: themeFS(map[string]*fstest.MapFile{
					"theme.yaml":              file("schemaVersion: 1\ndefaultLanguage: ja\n"),
					"components/parts/a.html": file("a"),
					"components/header.html":  file("header"),
					"assets/css/style.css":    file("body {}"),
					"translations/ja.yaml":    file("next: 次へ\n"),
				}),
				open: map[string]error{"templates": denied, "components/parts": denied, "assets": broken, "translations": denied},
			},
			want: []string{
				"translations: cannot read: permission denied",
				"components/parts: cannot read: permission denied",
				"templates: cannot read: permission denied",
				"assets: cannot read: input/output error",
			},
		},
		{
			name: "files",
			fsys: faultyFS{
				MapFS: themeFS(map[string]*fstest.MapFile{
					"theme.yaml":           file("schemaVersion: 1\ndefaultLanguage: ja\n"),
					"assets/css/style.css": file("body {}"),
					"translations/ja.yaml": file("next: 次へ\n"),
				}),
				open: map[string]error{"templates/entry.html": denied},
				read: map[string]error{"assets/css/style.css": broken, "translations/ja.yaml": broken},
				info: map[string]error{"templates/list.html": fs.ErrNotExist},
			},
			want: []string{
				"translations/ja.yaml: cannot read: input/output error",
				"templates/entry.html: cannot read: permission denied",
				"templates/list.html: cannot read: file does not exist",
				"assets/css/style.css: cannot read: input/output error",
			},
		},
		{
			name: "sizes",
			fsys: faultyFS{
				MapFS: themeFS(map[string]*fstest.MapFile{
					"templates/posts.html":   file(strings.Repeat("x", content.MaxTextBytes+100)),
					"assets/movie.mp4":       file("mp4"),
					"assets/largest.bin":     file("bin"),
					"components/header.html": file("header"),
				}),
				sizes: map[string]int64{
					"theme.yaml":             content.MaxTextBytes + 1,
					"templates/posts.html":   10,
					"assets/movie.mp4":       MaxAssetBytes + 1,
					"assets/largest.bin":     MaxAssetBytes,
					"components/header.html": -1 << 20,
				},
			},
			want: []string{
				"theme.yaml: too large: 1048577 bytes (max 1048576)",
				"templates/posts.html: too large: at least 1048577 bytes (max 1048576)",
				"assets/movie.mp4: too large: 67108865 bytes (max 67108864)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErrors(t, tt.fsys, tt.want)
		})
	}
}

func TestCheckTemplateName(t *testing.T) {
	tests := []struct {
		name string
		msg  string
	}{
		{name: "entry.html"},
		{name: "list.html"},
		{name: "posts.html"},
		{name: "term.html"},
		{name: "author.html"},
		{name: "404.html"},
		{name: "entry-works.html"},
		{name: "entry-page.html"},
		{name: "entry-post.html"},
		{name: "entry-My_Type-2.html"},
		{name: "entry-entry.html"},
		{name: "page-landing.html"},
		{name: "page-a.html"},
		{name: "term-category.html"},
		{name: "term-tag.html"},
		{name: "archive-works.html"},
		{name: "single.html", msg: `unknown template name "single.html"` + unknownTemplate},
		{name: "index.html", msg: `unknown template name "index.html"` + unknownTemplate},
		{name: "page.html", msg: `unknown template name "page.html"` + unknownTemplate},
		{name: "archive.html", msg: `unknown template name "archive.html"` + unknownTemplate},
		{name: "entry", msg: `unknown template name "entry"` + unknownTemplate},
		{name: "entry.htm", msg: `unknown template name "entry.htm"` + unknownTemplate},
		{name: "entry.HTML", msg: `unknown template name "entry.HTML"` + unknownTemplate},
		{name: "Entry.html", msg: `unknown template name "Entry.html"` + unknownTemplate},
		{name: "entry.html~", msg: `unknown template name "entry.html~"` + unknownTemplate},
		{name: "#entry.html#", msg: `unknown template name "#entry.html#"` + unknownTemplate},
		{name: "entry-.html", msg: `unknown template name "entry-.html"` + unknownTemplate},
		{name: "page-.html", msg: `unknown template name "page-.html"` + unknownTemplate},
		{name: "term-.html", msg: `unknown template name "term-.html"` + unknownTemplate},
		{name: "archive-.html", msg: `unknown template name "archive-.html"` + unknownTemplate},
		{name: "entrywork.html", msg: `unknown template name "entrywork.html"` + unknownTemplate},
		{name: "list-works.html", msg: `unknown template name "list-works.html"` + unknownTemplate},
		{name: "posts-news.html", msg: `unknown template name "posts-news.html"` + unknownTemplate},
		{name: "author-admin.html", msg: `unknown template name "author-admin.html"` + unknownTemplate},
		{name: "404-ja.html", msg: `unknown template name "404-ja.html"` + unknownTemplate},
		{name: "entry-works.htm", msg: `unknown template name "entry-works.htm"` + unknownTemplate},
		{name: "entry-a b.html", msg: `invalid name "a b": use ASCII letters, digits, "-" and "_"`},
		{name: "entry-works.old.html", msg: `invalid name "works.old": use ASCII letters, digits, "-" and "_"`},
		{name: "page-ランディング.html", msg: `invalid name "ランディング": use ASCII letters, digits, "-" and "_"`},
		{name: "term-a+b.html", msg: `invalid name "a+b": use ASCII letters, digits, "-" and "_"`},
		{name: "archive-a.b.html", msg: `invalid name "a.b": use ASCII letters, digits, "-" and "_"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkTemplateName(tt.name)
			if got := errorMsg(err); got != tt.msg {
				t.Errorf("checkTemplateName(%q) = %q, want %q", tt.name, got, tt.msg)
			}
		})
	}
}

func errorMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestLoadTemplates(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"theme.yaml":                   file("schemaVersion: 1\npageTemplates:\n  landing: ランディングページ\n  wide: 幅広\n"),
		"templates/posts.html":         file("posts"),
		"templates/term.html":          file("term"),
		"templates/author.html":        file("author"),
		"templates/404.html":           file("404"),
		"templates/entry-works.html":   file("entry-works"),
		"templates/entry-page.html":    file("entry-page"),
		"templates/page-landing.html":  file("page-landing"),
		"templates/page-wide.html":     file("page-wide"),
		"templates/term-category.html": file("term-category"),
		"templates/archive-works.html": file("archive-works"),
	}))
	want := []string{
		"404.html", "archive-works.html", "author.html", "entry-page.html", "entry-works.html", "entry.html",
		"list.html", "page-landing.html", "page-wide.html", "posts.html", "term-category.html", "term.html",
	}
	if got := slices.Sorted(maps.Keys(th.templates)); !slices.Equal(got, want) {
		t.Errorf("templates = %q, want %q", got, want)
	}
	if got := th.templates["page-wide.html"]; got != "page-wide" {
		t.Errorf("templates[page-wide.html] = %q, want %q", got, "page-wide")
	}
}

func TestLoadTemplateErrors(t *testing.T) {
	const landing = "schemaVersion: 1\npageTemplates:\n  landing: ランディングページ\n"
	runLoadTests(t, []loadTest{
		{
			name:    "unknown name",
			changes: map[string]*fstest.MapFile{"templates/single.html": file("single"), "templates/entry-a b.html": file("x")},
			want: []string{
				`templates/entry-a b.html: invalid name "a b": use ASCII letters, digits, "-" and "_"`,
				`templates/single.html: unknown template name "single.html"` + unknownTemplate,
			},
		},
		{
			name:    "missing entry.html",
			changes: map[string]*fstest.MapFile{"templates/entry.html": nil},
			want:    []string{"templates/entry.html: missing file; every theme needs entry.html and list.html"},
		},
		{
			name:    "missing list.html",
			changes: map[string]*fstest.MapFile{"templates/list.html": nil, "templates/posts.html": file("posts")},
			want:    []string{"templates/list.html: missing file; every theme needs entry.html and list.html"},
		},
		{
			name:    "empty templates",
			changes: map[string]*fstest.MapFile{"templates/entry.html": nil, "templates/list.html": nil, "templates": dir()},
			want: []string{
				"templates/entry.html: missing file; every theme needs entry.html and list.html",
				"templates/list.html: missing file; every theme needs entry.html and list.html",
			},
		},
		{
			name:    "hidden entry.html",
			changes: map[string]*fstest.MapFile{"templates/entry.html": nil, "templates/.entry.html": file("entry")},
			want:    []string{"templates/entry.html: missing file; every theme needs entry.html and list.html"},
		},
		{
			name:    "entry.html that does not load",
			changes: map[string]*fstest.MapFile{"templates/entry.html": symlink("list.html")},
			want:    []string{"templates/entry.html: symbolic links are not allowed"},
		},
		{
			name:    "declared page template",
			changes: map[string]*fstest.MapFile{"theme.yaml": file(landing), "templates/page-landing.html": file("landing")},
		},
		{
			name:    "undeclared page template",
			changes: map[string]*fstest.MapFile{"templates/page-landing.html": file("landing")},
			want: []string{
				`templates/page-landing.html: unknown page template "landing"; declare it in pageTemplates of theme.yaml`,
			},
		},
		{
			name:    "page template without its file",
			changes: map[string]*fstest.MapFile{"theme.yaml": file(landing)},
			want:    []string{"theme.yaml:3:3: /pageTemplates/landing: missing templates/page-landing.html"},
		},
		{
			name: "page templates in flow style",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":              file("schemaVersion: 1\npageTemplates: { a: A, b: B }\n"),
				"templates/page-a.html":   file("a"),
				"templates/page-c.html":   file("c"),
				"templates/page-c.htm":    file("c"),
				"templates/page-c d.html": file("c d"),
			},
			want: []string{
				"theme.yaml:2:24: /pageTemplates/b: missing templates/page-b.html",
				`templates/page-c d.html: invalid name "c d": use ASCII letters, digits, "-" and "_"`,
				`templates/page-c.htm: unknown template name "page-c.htm"` + unknownTemplate,
				`templates/page-c.html: unknown page template "c"; declare it in pageTemplates of theme.yaml`,
			},
		},
		{
			name: "page template that does not load",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":                  file(landing),
				"templates/page-landing.html": special(fs.ModeNamedPipe),
			},
			want: []string{"templates/page-landing.html: must be a regular file"},
		},
		{
			name: "page template without settings",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":                file("schemaVersion: 2\npageTemplates:\n  landing: ランディングページ\n"),
				"templates/page-other.html": file("other"),
			},
			want: []string{"theme.yaml:1:1: /schemaVersion: unsupported schemaVersion 2 (use 1)"},
		},
		{
			name: "page template without templates",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(landing),
				"templates/entry.html": nil,
				"templates/list.html":  nil,
			},
			want: []string{"templates: missing directory"},
		},
	})
}

func TestLoadComponents(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"components/header.html":       file("header"),
		"components/entry.html":        file("entry"),
		"components/entry/card.html":   file("card"),
		"components/a-b_C9/x-y/z.html": file("z"),
		"components/layouts":           dir(),
		"components/.drafts/x y.html":  file("hidden"),
		"components/.footer.html":      file("hidden"),
		"components/entry/.card.html~": file("hidden"),
	}))
	want := map[string]string{"header": "header", "entry": "entry", "entry/card": "card", "a-b_C9/x-y/z": "z"}
	if !reflect.DeepEqual(th.components, want) {
		t.Errorf("components = %q, want %q", th.components, want)
	}
}

func TestLoadComponentErrors(t *testing.T) {
	runLoadTests(t, []loadTest{
		{
			name: "names",
			changes: map[string]*fstest.MapFile{
				"components/README.md":         file("# Components\n"),
				"components/header.htm":        file("header"),
				"components/footer.html~":      file("footer"),
				"components/my header.html":    file("header"),
				"components/card.old.html":     file("card"),
				"components/my parts/a.html":   file("a"),
				"components/my parts/b c.html": file("b c"),
				"components/my parts/c.txt":    file("c"),
				"components/パーツ/a.html":        file("a"),
				"components/ok/x.y/a.html":     file("a"),
			},
			want: []string{
				`components/README.md: unknown file "README.md"; components are <name>.html files`,
				`components/card.old.html: invalid name "card.old": use ASCII letters, digits, "-" and "_"`,
				`components/footer.html~: unknown file "footer.html~"; components are <name>.html files`,
				`components/header.htm: unknown file "header.htm"; components are <name>.html files`,
				`components/my header.html: invalid name "my header": use ASCII letters, digits, "-" and "_"`,
				`components/my parts: invalid name "my parts": use ASCII letters, digits, "-" and "_"`,
				`components/my parts/b c.html: invalid name "b c": use ASCII letters, digits, "-" and "_"`,
				`components/my parts/c.txt: unknown file "c.txt"; components are <name>.html files`,
				`components/ok/x.y: invalid name "x.y": use ASCII letters, digits, "-" and "_"`,
				`components/パーツ: invalid name "パーツ": use ASCII letters, digits, "-" and "_"`,
			},
		},
	})
}

// TestLoadOS loads themes from directories with symbolic links through os.DirFS and os.Root.
func TestLoadOS(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Skipf("cannot make symbolic links: %v", err)
		}
	}
	write("theme.yaml", "schemaVersion: 1\n")
	write("templates/entry.html", "entry")
	write("templates/list.html", "list")
	write("assets/css/style.css", "body {}")
	write("shared/footer.html", "footer")
	link("/", "root")
	write(".git/HEAD", "ref: refs/heads/main\n")
	link("/etc", ".git/etc")
	filesystems := map[string]func(*testing.T) fs.FS{
		"DirFS": func(*testing.T) fs.FS { return os.DirFS(dir) },
		"Root":  func(t *testing.T) fs.FS { return openRoot(t, dir).FS() },
	}
	for name, fsys := range filesystems {
		t.Run(name+" without links", func(t *testing.T) {
			th := mustLoad(t, fsys(t))
			if got, want := th.Assets(), []string{"css/style.css"}; !slices.Equal(got, want) {
				t.Errorf("Assets() = %q, want %q", got, want)
			}
		})
	}
	link("../shared/footer.html", "templates/posts.html")
	link("shared", "components")
	link("/etc/passwd", "assets/passwd")
	link("../../shared", "assets/css/shared")
	link("/etc/hostname", "assets/css/.hidden")
	want := []string{
		"components: symbolic links are not allowed",
		"templates/posts.html: symbolic links are not allowed",
		"assets/css/shared: symbolic links are not allowed",
		"assets/passwd: symbolic links are not allowed",
	}
	for name, fsys := range filesystems {
		t.Run(name, func(t *testing.T) {
			checkErrors(t, fsys(t), want)
		})
	}
}
