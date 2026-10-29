package content

import (
	"encoding/json/v2"
	"maps"
	"reflect"
	"testing"
)

func TestLocalizedUnmarshalJSON(t *testing.T) {
	in := `{"ja": "会社外観", "en": "Our office building"}`
	want := Localized{"ja": "会社外観", "en": "Our office building"}
	var got Localized
	if err := json.Unmarshal([]byte(in), &got); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", in, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, want)
	}
}

func TestLocalizedRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "sorted languages", in: `{"ja":"a","en":"b"}`, want: `{"en":"b","ja":"a"}`},
		{
			name: "many languages",
			in:   `{"zh-Hant":"a","ja":"b","fr":"c","en-US":"d","de":"e","en":"f"}`,
			want: `{"de":"e","en":"f","en-US":"d","fr":"c","ja":"b","zh-Hant":"a"}`,
		},
		{name: "line breaks and tabs", in: `{"ja":"一行目\n二行目\tタブ"}`},
		{name: "spaces", in: `{ "ja" : "a b" }`, want: `{"ja":"a b"}`},
		{name: "language codes as written", in: `{"JA":"a","a/b":"b"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[Localized](t, tt.in, want)
		})
	}
}

func TestLocalizedUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{name: "array", in: `[]`, path: "", msg: "expected object, got array"},
		{name: "empty text", in: `{"ja":""}`, path: "/ja", msg: "empty string is not allowed"},
		{name: "null text", in: `{"ja":null}`, path: "/ja", msg: "expected string, got null"},
		{name: "number for a text", in: `{"ja":1}`, path: "/ja", msg: "expected string, got number"},
		{name: "array for a text", in: `{"ja":["a"]}`, path: "/ja", msg: "expected string, got array"},
		{name: "after another language", in: `{"en":"a","ja":null}`, path: "/ja", msg: "expected string, got null"},
		{name: "duplicate language", in: `{"ja":"a","ja":"b"}`, path: "/ja", msg: "duplicate key"},
		{name: "trailing data", in: `{"ja":"a"} {}`, path: "", msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Localized{"ja": "old"}
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if !reflect.DeepEqual(got, Localized{"ja": "old"}) {
				t.Errorf("UnmarshalJSON(%s) changed the value to %#v", tt.in, got)
			}
		})
	}
}

func TestLocalizedNormalize(t *testing.T) {
	const ctrl = string(rune(0x0001))
	tests := []struct {
		name      string
		in        Localized
		multiline bool
		want      Localized
	}{
		{name: "control characters", in: Localized{"ja": "a" + ctrl + "b", "en": "c" + ctrl}, want: Localized{"ja": "ab", "en": "c"}},
		{name: "NFC", in: Localized{"ja": "cafe" + acute}, want: Localized{"ja": "caf" + eAcute}},
		{name: "line breaks and tabs left in a line", in: Localized{"ja": "a\nb\r\nc\rd\te"}, want: Localized{"ja": "a\nb\r\nc\rd\te"}},
		{name: "multiline CRLF and CR", in: Localized{"ja": "a\r\nb\rc"}, multiline: true, want: Localized{"ja": "a\nb\nc"}},
		{name: "multiline tab", in: Localized{"ja": "a\tb\n"}, multiline: true, want: Localized{"ja": "a\tb\n"}},
		{
			name:      "multiline control characters",
			in:        Localized{"ja": "a\vb" + ctrl + string(rune(0x0085))},
			multiline: true,
			want:      Localized{"ja": "ab"},
		},
		{name: "multiline NFC", in: Localized{"ja": "e" + acute + "\r\n"}, multiline: true, want: Localized{"ja": eAcute + "\n"}},
		{name: "language of which nothing is left", in: Localized{"ja": "a", "en": ctrl}, want: Localized{"ja": "a"}},
		{name: "empty text", in: Localized{"ja": "a", "en": ""}, multiline: true, want: Localized{"ja": "a"}},
		{name: "nothing left", in: Localized{"ja": ctrl, "en": ""}, want: nil},
		{name: "nothing left in multiline", in: Localized{"ja": "\v" + ctrl}, multiline: true, want: nil},
		{name: "empty", in: Localized{}, want: nil},
		{name: "nil", in: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maps.Clone(tt.in)
			got.normalize(tt.multiline)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("normalize(%t) of %#v = %#v, want %#v", tt.multiline, tt.in, got, tt.want)
			}
		})
	}
}

func TestLocalizedCheck(t *testing.T) {
	const ctrl = string(rune(0x0001))
	tests := []struct {
		name      string
		in        Localized
		multiline bool
		paths     []string
		msg       string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "ja and en", in: Localized{"ja": "会社外観", "en": "Our office building"}},
		{name: "en only", in: Localized{"en": "Our office building"}},
		{name: "nil", in: nil},
		{name: "line breaks and tabs in multiline", in: Localized{"ja": "一行目\n二行目\tタブ"}, multiline: true},
		{
			name:  "language not in the site settings",
			in:    Localized{"ja": "a", "fr": "b"},
			paths: []string{"/bio/fr"},
			msg:   `/bio/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "language in another case",
			in:    Localized{"JA": "a"},
			paths: []string{"/bio/JA"},
			msg:   `/bio/JA: language "JA" is not in the site settings (use ja, en)`,
		},
		{name: "empty text", in: Localized{"ja": ""}, paths: []string{"/bio/ja"}, msg: "/bio/ja: empty value; omit it"},
		{name: "empty text in multiline", in: Localized{"en": ""}, multiline: true, paths: []string{"/bio/en"}, msg: "/bio/en: empty value; omit it"},
		{name: "line break in a line", in: Localized{"ja": "a\nb"}, paths: []string{"/bio/ja"}, msg: "/bio/ja: contains a line break or tab"},
		{name: "tab in a line", in: Localized{"en": "a\tb"}, paths: []string{"/bio/en"}, msg: "/bio/en: contains a line break or tab"},
		{name: "not NFC", in: Localized{"ja": "cafe" + acute}, paths: []string{"/bio/ja"}, msg: "/bio/ja: not in Unicode NFC; normalize it first"},
		{
			name:      "not NFC in multiline",
			in:        Localized{"ja": "cafe" + acute},
			multiline: true,
			paths:     []string{"/bio/ja"},
			msg:       "/bio/ja: not in Unicode NFC; normalize it first",
		},
		{
			name:  "control character",
			in:    Localized{"ja": "a" + ctrl},
			paths: []string{"/bio/ja"},
			msg:   "/bio/ja: contains control characters; normalize it first",
		},
		{
			name:      "CR in multiline",
			in:        Localized{"ja": "a\r\nb"},
			multiline: true,
			paths:     []string{"/bio/ja"},
			msg:       "/bio/ja: contains control characters; normalize it first",
		},
		{
			// The site languages in their order, then the others in sorted order, whatever the order in the map.
			name:  "order",
			in:    Localized{"fr": "a", "en": "", "de": "b", "ja": "c\nd", "zh": "e", "ko": "f"},
			paths: []string{"/bio/ja", "/bio/en", "/bio/de", "/bio/fr", "/bio/ko", "/bio/zh"},
			msg: "/bio/ja: contains a line break or tab\n/bio/en: empty value; omit it\n" +
				`/bio/de: language "de" is not in the site settings (use ja, en)` + "\n" +
				`/bio/fr: language "fr" is not in the site settings (use ja, en)` + "\n" +
				`/bio/ko: language "ko" is not in the site settings (use ja, en)` + "\n" +
				`/bio/zh: language "zh" is not in the site settings (use ja, en)`,
		},
		{
			name:  "texts of languages not in the site settings",
			in:    Localized{"fr": "", "de": "a\nb" + ctrl},
			paths: []string{"/bio/de", "/bio/fr"},
			msg: `/bio/de: language "de" is not in the site settings (use ja, en)` + "\n" +
				`/bio/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "escaped language",
			in:    Localized{"a/b": "x", "c~d": "y"},
			paths: []string{"/bio/a~1b", "/bio/c~0d"},
			msg:   `/bio/a~1b: language "a/b" is not in the site settings (use ja, en)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c checker
			tt.in.check(&c, "/bio", testSite(), tt.multiline)
			err := c.result()
			if tt.paths == nil && err != nil {
				t.Fatalf("check() = %v, want nil", err)
			}
			if got := errorPaths(t, err); !reflect.DeepEqual(got, tt.paths) {
				t.Errorf("check() paths = %q, want %q", got, tt.paths)
			}
			if tt.msg != "" {
				assertErrorMsg(t, err, tt.msg)
			}
		})
	}
}

func TestLocalizedCheckRequired(t *testing.T) {
	const (
		ctrl    = string(rune(0x0001))
		missing = "missing value; every language of the site needs one"
	)
	tests := []struct {
		name      string
		in        Localized
		multiline bool
		paths     []string
		msg       string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "every language", in: Localized{"ja": "お名前", "en": "Name"}},
		{name: "every language in multiline", in: Localized{"ja": "一行目\n二行目\tタブ", "en": "Line 1\nLine 2"}, multiline: true},
		{name: "nil", in: nil, paths: []string{"/label"}, msg: "/label: missing label"},
		{name: "empty", in: Localized{}, multiline: true, paths: []string{"/label"}, msg: "/label: missing label"},
		{name: "missing language", in: Localized{"ja": "お名前"}, paths: []string{"/label/en"}, msg: "/label/en: " + missing},
		{
			name:      "missing language in multiline",
			in:        Localized{"en": "Thank you."},
			multiline: true,
			paths:     []string{"/label/ja"},
			msg:       "/label/ja: " + missing,
		},
		{name: "empty value", in: Localized{"ja": "", "en": "Name"}, paths: []string{"/label/ja"}, msg: "/label/ja: " + missing},
		{
			name:  "only languages not in the site settings",
			in:    Localized{"fr": "Nom", "de": "Name"},
			paths: []string{"/label/ja", "/label/en", "/label/de", "/label/fr"},
			msg: "/label/ja: " + missing + "\n/label/en: " + missing + "\n" +
				`/label/de: language "de" is not in the site settings (use ja, en)` + "\n" +
				`/label/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "language in another case",
			in:    Localized{"JA": "お名前", "en": "Name"},
			paths: []string{"/label/ja", "/label/JA"},
			msg:   "/label/ja: " + missing + "\n" + `/label/JA: language "JA" is not in the site settings (use ja, en)`,
		},
		{
			name:  "line break in a line",
			in:    Localized{"ja": "お\n名前", "en": "Name"},
			paths: []string{"/label/ja"},
			msg:   "/label/ja: contains a line break or tab",
		},
		{
			name:  "not NFC",
			in:    Localized{"ja": "お名前", "en": "cafe" + acute},
			paths: []string{"/label/en"},
			msg:   "/label/en: not in Unicode NFC; normalize it first",
		},
		{
			name:      "not NFC in multiline",
			in:        Localized{"ja": "cafe" + acute + "\n", "en": "Thank you."},
			multiline: true,
			paths:     []string{"/label/ja"},
			msg:       "/label/ja: not in Unicode NFC; normalize it first",
		},
		{
			name:  "control character",
			in:    Localized{"ja": "お名前", "en": "Name" + ctrl},
			paths: []string{"/label/en"},
			msg:   "/label/en: contains control characters; normalize it first",
		},
		{
			// The site languages in their order, missing or not, then the others in sorted order, whatever the order in the map.
			name:  "order",
			in:    Localized{"zh": "a", "en": "b\nc", "fr": "", "de": "d"},
			paths: []string{"/label/ja", "/label/en", "/label/de", "/label/fr", "/label/zh"},
			msg: "/label/ja: " + missing + "\n/label/en: contains a line break or tab\n" +
				`/label/de: language "de" is not in the site settings (use ja, en)` + "\n" +
				`/label/fr: language "fr" is not in the site settings (use ja, en)` + "\n" +
				`/label/zh: language "zh" is not in the site settings (use ja, en)`,
		},
		{
			name:  "texts of languages not in the site settings",
			in:    Localized{"ja": "お名前", "en": "Name", "fr": "a\nb" + ctrl},
			paths: []string{"/label/fr"},
			msg:   `/label/fr: language "fr" is not in the site settings (use ja, en)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c checker
			tt.in.checkRequired(&c, "/label", "label", testSite(), tt.multiline)
			err := c.result()
			if tt.paths == nil && err != nil {
				t.Fatalf("checkRequired() = %v, want nil", err)
			}
			if got := errorPaths(t, err); !reflect.DeepEqual(got, tt.paths) {
				t.Errorf("checkRequired() paths = %q, want %q", got, tt.paths)
			}
			if tt.msg != "" {
				assertErrorMsg(t, err, tt.msg)
			}
		})
	}
}
