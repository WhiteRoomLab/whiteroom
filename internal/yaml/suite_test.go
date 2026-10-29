package yaml

import (
	"bytes"
	"compress/gzip"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

// suiteAccepted is the number of tests in yaml-test-suite that ToJSON accepts. Update it when the subset changes on purpose.
const suiteAccepted = 74

type suiteTest struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	YAML  string  `json:"yaml"`
	JSON  *string `json:"json"`
	Error bool    `json:"error"`
}

// readSuite reads yaml-test-suite data-2022-01-17, which go generate writes; see testdata/YAML-TEST-SUITE-LICENSE.txt.
func readSuite(t testing.TB) []suiteTest {
	t.Helper()
	data, err := os.ReadFile("testdata/yaml-test-suite.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var tests []suiteTest
	if err := json.UnmarshalRead(zr, &tests); err != nil {
		t.Fatal(err)
	}
	return tests
}

// jsonValues returns the values in s, which has a JSON value for each YAML document.
func jsonValues(s string) ([]any, error) {
	dec := jsontext.NewDecoder(strings.NewReader(s))
	var vs []any
	for {
		var v any
		err := json.UnmarshalDecode(dec, &v)
		if errors.Is(err, io.EOF) {
			return vs, nil
		}
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
}

func TestSuite(t *testing.T) {
	tests := readSuite(t)
	accepted := 0
	for _, tt := range tests {
		out, _, err := ToJSON([]byte(tt.YAML))
		if err != nil {
			continue
		}
		accepted++
		if tt.Error {
			t.Errorf("%s %s: ToJSON(%q) = %s, want an error", tt.ID, tt.Name, tt.YAML, out)
			continue
		}
		if tt.JSON == nil {
			t.Errorf("%s %s: ToJSON(%q) = %s, but the suite has no JSON for it", tt.ID, tt.Name, tt.YAML, out)
			continue
		}
		want, err := jsonValues(*tt.JSON)
		if err != nil || len(want) != 1 {
			t.Errorf("%s %s: ToJSON(%q) = %s, but the JSON of the suite is not one value: %q, %v",
				tt.ID, tt.Name, tt.YAML, out, *tt.JSON, err)
			continue
		}
		var got any
		if err := json.Unmarshal(out, &got); err != nil || !reflect.DeepEqual(got, want[0]) {
			t.Errorf("%s %s: ToJSON(%q) = %s, %v; want %s", tt.ID, tt.Name, tt.YAML, out, err, strings.TrimSpace(*tt.JSON))
		}
	}
	t.Logf("accepted %d of %d tests", accepted, len(tests))
	if accepted != suiteAccepted {
		t.Errorf("accepted %d tests, want %d", accepted, suiteAccepted)
	}
}
