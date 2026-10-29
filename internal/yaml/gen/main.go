// Command gen writes testdata/yaml-test-suite.json.gz from the data of yaml-test-suite.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const maxSize = 16 << 20

// The tag data-2022-01-17 of yaml-test-suite has the tests of its release v2022-01-17 as files.
const (
	archiveURL    = "https://github.com/yaml/yaml-test-suite/archive/refs/tags/data-2022-01-17.tar.gz"
	archiveSHA256 = "ccae49c1d010d27c20d315969a97752f95b14642fc1e73755db4eb2df4bb0256"
)

var (
	idPattern  = regexp.MustCompile(`^[0-9A-Z]{4}$`)
	subPattern = regexp.MustCompile(`^[0-9]+$`) // Y79Y has 000 to 010
)

type test struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	YAML  string  `json:"yaml"`
	JSON  *string `json:"json,omitzero"` // some in.json files are empty
	Error bool    `json:"error"`
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("gen: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	client := &http.Client{Timeout: 5 * time.Minute}
	data, err := fetch(client, archiveURL)
	if err != nil {
		return err
	}
	if sum := fmt.Sprintf("%x", sha256.Sum256(data)); sum != archiveSHA256 {
		return fmt.Errorf("%s: sha256 is %s, want %s", archiveURL, sum, archiveSHA256)
	}
	tests, err := readArchive(data)
	if err != nil {
		return fmt.Errorf("%s: %w", archiveURL, err)
	}
	js, err := json.Marshal(tests)
	if err != nil {
		return err
	}
	// No name or time in the gzip header, so that the same tests give the same bytes.
	var gz bytes.Buffer
	zw, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(append(js, '\n')); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join("testdata", "yaml-test-suite.json.gz"), gz.Bytes(), 0o644); err != nil {
		return err
	}
	var errs, withJSON int
	for _, t := range tests {
		if t.Error {
			errs++
		}
		if t.JSON != nil {
			withJSON++
		}
	}
	log.Printf("%d tests, %d with error, %d with in.json", len(tests), errs, withJSON)
	return nil
}

func fetch(c *http.Client, url string) ([]byte, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, maxSize)
	}
	return data, nil
}

// readArchive returns the tests in the directories that have in.yaml, sorted by ID.
func readArchive(data []byte) ([]test, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	files := make(map[string]map[string]string)
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		parts := strings.Split(h.Name, "/")
		if h.Typeflag != tar.TypeReg || !inTest(parts) {
			continue
		}
		name := parts[len(parts)-1]
		if name != "===" && name != "in.yaml" && name != "in.json" && name != "error" {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", h.Name, err)
		}
		id := strings.Join(parts[1:len(parts)-1], "/")
		if files[id] == nil {
			files[id] = make(map[string]string)
		}
		files[id][name] = string(b)
	}
	var tests []test
	for id, f := range files {
		in, ok := f["in.yaml"]
		if !ok {
			continue
		}
		t := test{ID: id, Name: strings.TrimSpace(f["==="]), YAML: in}
		if j, ok := f["in.json"]; ok {
			t.JSON = &j
		}
		_, t.Error = f["error"]
		tests = append(tests, t)
	}
	slices.SortFunc(tests, func(a, b test) int { return strings.Compare(a.ID, b.ID) })
	return tests, nil
}

// inTest reports whether parts, a path in the archive, is of a file in <top>/<ID>/ or <top>/<ID>/<NN>/.
func inTest(parts []string) bool {
	switch len(parts) {
	case 3:
		return idPattern.MatchString(parts[1])
	case 4:
		return idPattern.MatchString(parts[1]) && subPattern.MatchString(parts[2])
	}
	return false
}
