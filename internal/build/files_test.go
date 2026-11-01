package build

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir, root := newPublic(t)
	files := map[string]string{
		"sitemap.xml":                  "<urlset/>",
		"media/med_X/会社外観.jpg":         "published",
		"media/med_X/会社外観-640x427.jpg": "smaller",
		"news/page/2/index.html":       "<p>2</p>",
		"2026/10/26/新サービス/index.html":  "<p>post</p>",
	}
	for name, data := range files {
		if err := writeFile(root, name, []byte(data)); err != nil {
			t.Errorf("writeFile(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"sitemap.xml", "news/page/2/index.html", "media/med_X"} {
		if err := writeFile(root, name, []byte("again")); !errors.Is(err, fs.ErrExist) {
			t.Errorf("writeFile(%q) again = %v, want an error of %v", name, err, fs.ErrExist)
		}
	}
	for _, name := range []string{"sitemap.xml/index.html", "../outside.html", "/root.html"} {
		if err := writeFile(root, name, []byte("x")); err == nil {
			t.Errorf("writeFile(%q) = nil, want an error", name)
		}
	}
	if got, want := listFiles(t, dir), slices.Sorted(maps.Keys(files)); !slices.Equal(got, want) {
		t.Errorf("files %q, want %q", got, want)
	}
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))); err != nil || string(got) != want {
			t.Errorf("%s has %q, %v, want %q", name, got, err, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "..", "outside.html")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file was written outside the root: %v", err)
	}
}
