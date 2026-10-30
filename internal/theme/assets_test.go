package theme

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestAssets(t *testing.T) {
	long := strings.Repeat("a", 251) + ".css"
	files := map[string]string{
		"assets/css/style.css":                "body {}",
		"assets/images/logo@2x.png":           "png",
		"assets/fonts/NotoSans-Regular.woff2": "woff2",
		"assets/js/a_b.c~d+e.js":              "js",
		"assets/favicon.ico":                  "ico",
		"assets/a/b/c/d.txt":                  "d",
		"assets/a-c.txt":                      "a-c",
		"assets/a.txt":                        "a",
		"assets/B.txt":                        "B",
		"assets/empty":                        "",
		"assets/" + long:                      "long",
	}
	changes := map[string]*fstest.MapFile{
		"assets/.htaccess":                file("Options +Indexes\n"),
		"assets/.well-known/security.txt": file("Contact: x\n"),
		"assets/css/.style.css.swp":       file("\x00"),
		"assets/fonts/.cache":             dir(),
		"assets/empty-dir":                dir(),
	}
	for name, data := range files {
		changes[name] = file(data)
	}
	th := mustLoad(t, themeFS(changes))
	want := []string{
		"B.txt", "a-c.txt", "a.txt", "a/b/c/d.txt", long, "css/style.css", "empty", "favicon.ico",
		"fonts/NotoSans-Regular.woff2", "images/logo@2x.png", "js/a_b.c~d+e.js",
	}
	if got := th.Assets(); !slices.Equal(got, want) {
		t.Errorf("Assets() = %q, want %q", got, want)
	}
	for _, p := range want {
		data := []byte(files["assets/"+p])
		a := th.assets[p]
		if !bytes.Equal(a.data, data) || a.sum != sha256.Sum256(data) {
			t.Errorf("assets[%q] = %q with %x, want %q", p, a.data, a.sum, data)
		}
	}
	if got := mustLoad(t, themeFS(nil)).Assets(); len(got) != 0 {
		t.Errorf("Assets() without assets = %q", got)
	}
}

func TestAssetErrors(t *testing.T) {
	const use = `: use ASCII letters, digits, "-", "_", ".", "~", "@" and "+"`
	long := strings.Repeat("a", 252) + ".css"
	runLoadTests(t, []loadTest{
		{
			name: "names",
			changes: map[string]*fstest.MapFile{
				"assets/images/my logo.png": file("png"),
				"assets/a%20b.png":          file("png"),
				"assets/a#b.png":            file("png"),
				"assets/a?b.png":            file("png"),
				"assets/a:b.png":            file("png"),
				"assets/a\\b.png":           file("png"),
				"assets/it's.png":           file("png"),
				"assets/(1).png":            file("png"),
				"assets/a,b.png":            file("png"),
				"assets/ロゴ.png":             file("png"),
				"assets/" + long:            file("css"),
			},
			want: []string{
				`assets/(1).png: invalid name "(1).png"` + use,
				`assets/a#b.png: invalid name "a#b.png"` + use,
				`assets/a%20b.png: invalid name "a%20b.png"` + use,
				`assets/a,b.png: invalid name "a,b.png"` + use,
				`assets/a:b.png: invalid name "a:b.png"` + use,
				`assets/a?b.png: invalid name "a?b.png"` + use,
				`assets/a\b.png: invalid name "a\\b.png"` + use,
				"assets/" + long + ": name too long: 256 bytes (max 255)",
				`assets/images/my logo.png: invalid name "my logo.png"` + use,
				`assets/it's.png: invalid name "it's.png"` + use,
				`assets/ロゴ.png: invalid name "ロゴ.png"` + use,
			},
		},
		{
			name: "directories",
			changes: map[string]*fstest.MapFile{
				"assets/my images/a.png":    file("png"),
				"assets/my images/b c.png":  file("png"),
				"assets/画像/a.png":           file("png"),
				"assets/" + long + "/a.png": file("png"),
				"assets/ok/a b/c.png":       file("png"),
			},
			want: []string{
				"assets/" + long + ": name too long: 256 bytes (max 255)",
				`assets/my images: invalid name "my images"` + use,
				`assets/my images/b c.png: invalid name "b c.png"` + use,
				`assets/ok/a b: invalid name "a b"` + use,
				`assets/画像: invalid name "画像"` + use,
			},
		},
	})
}

// allowed returns the permission bits that files made in a temporary directory can have, which the umask limits.
func allowed(t *testing.T) fs.FileMode {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(p, nil, 0o777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestWriteAssets(t *testing.T) {
	th := mustLoad(t, fullTheme())
	out := t.TempDir()
	if err := th.WriteAssets(openRoot(t, out)); err != nil {
		t.Fatalf("WriteAssets() = %v", err)
	}
	perm := allowed(t)
	want := map[string][]byte{
		"assets/css/style.css":   []byte("body { color: var(--color-text); }\n"),
		"assets/images/logo.png": logoPNG,
	}
	got := make(map[string][]byte)
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(out, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := 0o755 & perm
		if !d.IsDir() {
			mode = 0o644 & perm
			if got[filepath.ToSlash(rel)], err = os.ReadFile(p); err != nil {
				return err
			}
		}
		if rel != "." && info.Mode().Perm() != mode {
			t.Errorf("%s: mode %v, want %v", rel, info.Mode().Perm(), mode)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Errorf("files = %q, want %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for name, data := range want {
		if !bytes.Equal(got[name], data) {
			t.Errorf("%s = %q, want %q", name, got[name], data)
		}
	}

	// Into the same directory again, the files exist.
	if err := th.WriteAssets(openRoot(t, out)); !errors.Is(err, fs.ErrExist) {
		t.Errorf("WriteAssets() again = %v, want an error for an existing file", err)
	}
}

func TestWriteAssetsWithoutAssets(t *testing.T) {
	out := t.TempDir()
	if err := mustLoad(t, themeFS(nil)).WriteAssets(openRoot(t, out)); err != nil {
		t.Fatalf("WriteAssets() = %v", err)
	}
	if entries, err := os.ReadDir(out); err != nil || len(entries) != 0 {
		t.Errorf("output = %v, %v, want empty", entries, err)
	}
}

// TestWriteAssetsOutside checks that symbolic links in the output cannot make WriteAssets write outside of it.
func TestWriteAssetsOutside(t *testing.T) {
	th := mustLoad(t, fullTheme())
	rel := func(t *testing.T, base, target string) string {
		t.Helper()
		rel, err := filepath.Rel(base, target)
		if err != nil {
			t.Fatal(err)
		}
		return rel
	}
	mkdir := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		link func(t *testing.T, out, outside string) (target, name string)
	}{
		{name: "absolute", link: func(_ *testing.T, _, outside string) (string, string) { return outside, "assets" }},
		{
			name: "relative",
			link: func(t *testing.T, out, outside string) (string, string) { return rel(t, out, outside), "assets" },
		},
		{
			name: "in a subdirectory",
			link: func(t *testing.T, out, outside string) (string, string) {
				mkdir(t, filepath.Join(out, "assets"))
				return rel(t, filepath.Join(out, "assets"), outside), filepath.Join("assets", "css")
			},
		},
		{
			name: "a file",
			link: func(t *testing.T, out, outside string) (string, string) {
				mkdir(t, filepath.Join(out, "assets", "css"))
				return filepath.Join(outside, "style.css"), filepath.Join("assets", "css", "style.css")
			},
		},
		{
			name: "after the first asset",
			link: func(t *testing.T, out, outside string) (string, string) {
				mkdir(t, filepath.Join(out, "assets"))
				return outside, filepath.Join("assets", "images")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, outside := t.TempDir(), t.TempDir()
			target, name := tt.link(t, out, outside)
			if err := os.Symlink(target, filepath.Join(out, name)); err != nil {
				t.Skipf("cannot make symbolic links: %v", err)
			}
			if err := th.WriteAssets(openRoot(t, out)); err == nil {
				t.Error("WriteAssets() = nil, want an error")
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Errorf("outside = %v, %v, want empty", entries, err)
			}
		})
	}
}
