package html

import (
	"bytes"
	"compress/gzip"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testFile struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// readTestFiles reads testdata/<name>, which go generate writes. html5lib-tokenizer.json.gz has the files
// tokenizer/*.test of html5lib-tests at commit c777c408b61078ea2eb4acefc2535f54dbc8b28a (see
// testdata/HTML5LIB-TESTS-LICENSE.txt), and wpt-tree-construction.json.gz the files html/syntax/parsing/resources/*.dat
// of web-platform-tests at commit 4aa56d3beafc5655fa55491169eb14845b44eab3 (see testdata/WPT-LICENSE.txt).
func readTestFiles(t testing.TB, name string) []testFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var files []testFile
	if err := json.UnmarshalRead(zr, &files, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return files
}

func TestTestFiles(t *testing.T) {
	tests := []struct {
		name  string
		ext   string
		count int
	}{
		{"html5lib-tokenizer.json.gz", ".test", 14},
		{"wpt-tree-construction.json.gz", ".dat", 62},
	}
	for _, tt := range tests {
		files := readTestFiles(t, tt.name)
		if len(files) != tt.count {
			t.Errorf("%s: %d files, want %d", tt.name, len(files), tt.count)
		}
		for i, f := range files {
			if !strings.HasSuffix(f.Name, tt.ext) || strings.Contains(f.Name, "/") || f.Text == "" {
				t.Errorf("%s: file %q of %d bytes", tt.name, f.Name, len(f.Text))
			}
			if i > 0 && files[i-1].Name >= f.Name {
				t.Errorf("%s: %q after %q, want the names sorted and unique", tt.name, f.Name, files[i-1].Name)
			}
		}
	}
}
