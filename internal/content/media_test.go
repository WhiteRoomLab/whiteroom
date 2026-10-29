package content

import (
	"encoding/json/v2"
	"maps"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The media in testdata: the image of the example in CONCEPT.md 6.11.1, and a PDF file.
var (
	officeImage = Media{ // office-image.json
		ID:         testImage,
		Hash:       "sha256:fa6b1a13e38782a24e72fe6ec39feb7a16bd15ce3e3a9efe8750ff6c8b0c0f01",
		FileName:   "会社外観.jpg",
		MIMEType:   "image/jpeg",
		Size:       482133,
		Width:      1600,
		Height:     1067,
		Alt:        Localized{"en": "Our office building", "ja": "会社外観"},
		Caption:    Localized{"ja": "本社の外観（2024年撮影）"},
		UploadedAt: "2026-10-25T10:00:00+09:00",
		UploadedBy: testUser,
	}
	companyGuidePDF = Media{ // company-guide-pdf.json
		ID:         testPDF,
		Hash:       "sha256:7ba5e322e1c7f0ed36cbdcca013f17f25ef7f3877393833c2b4372731112dccd",
		FileName:   "会社案内.pdf",
		MIMEType:   "application/pdf",
		Size:       2345678,
		UploadedAt: "2026-10-25T10:05:00+09:00",
		UploadedBy: testUser,
	}
)

func TestMediaUnmarshalJSON(t *testing.T) {
	tests := []struct {
		file string
		want Media
	}{
		{file: "office-image.json", want: officeImage},
		{file: "company-guide-pdf.json", want: companyGuidePDF},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			in := readTestdata(t, tt.file)
			var got Media
			if err := json.Unmarshal([]byte(in), &got); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, tt.want)
			}
		})
	}
}

// TestMediaTestdata checks that testRefs agrees with testdata about the kinds of the media.
func TestMediaTestdata(t *testing.T) {
	for _, m := range []Media{officeImage, companyGuidePDF} {
		if want := (Ref{Type: m.Kind()}); testRefs[m.ID] != want {
			t.Errorf("testRefs[%q] = %#v, want %#v", m.ID, testRefs[m.ID], want)
		}
	}
}

func TestMediaRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "image", in: readTestdata(t, "office-image.json")},
		{name: "PDF", in: readTestdata(t, "company-guide-pdf.json")},
		{name: "schemaVersion only", in: `{"schemaVersion":1}`},
		{
			name: "reversed key order",
			in: `{"uploadedBy":"` + testUser + `","uploadedAt":"2026-10-25T10:00:00+09:00","caption":{"ja":"本社の外観"},` +
				`"alt":{"ja":"会社外観","en":"Our office building"},"height":1067,"width":1600,"size":482133,` +
				`"mimeType":"image/jpeg","fileName":"会社外観.jpg","hash":"` + officeImage.Hash + `",` +
				`"id":"` + testImage + `","schemaVersion":1}`,
			want: `{"schemaVersion":1,"id":"` + testImage + `","hash":"` + officeImage.Hash + `","fileName":"会社外観.jpg",` +
				`"mimeType":"image/jpeg","size":482133,"width":1600,"height":1067,` +
				`"alt":{"en":"Our office building","ja":"会社外観"},"caption":{"ja":"本社の外観"},` +
				`"uploadedAt":"2026-10-25T10:00:00+09:00","uploadedBy":"` + testUser + `"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[Media](t, tt.in, want)
		})
	}
}

func TestMediaMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   Media
	}{
		{name: "empty", in: Media{}},
		{name: "empty alt and caption", in: Media{Alt: Localized{}, Caption: Localized{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if want := `{"schemaVersion":1}`; string(got) != want {
				t.Errorf("Marshal(%#v) = %s, want %s", tt.in, got, want)
			}
		})
	}
}

func TestMediaUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{
			name: "missing schemaVersion",
			in: `{"id":"x","hash":"x","fileName":"x","mimeType":"x","size":1,"width":1,"height":1,` +
				`"alt":{"ja":"x"},"caption":{"ja":"x"},"uploadedAt":"x","uploadedBy":"x"}`,
			path: "",
			msg:  `missing key "schemaVersion"`,
		},
		{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
		{name: "title", in: `{"schemaVersion":1,"title":"会社外観"}`, path: "/title", msg: "unknown key"},
		{name: "type", in: `{"schemaVersion":1,"type":"image"}`, path: "/type", msg: "unknown key"},
		{name: "kind", in: `{"schemaVersion":1,"kind":"image"}`, path: "/kind", msg: "unknown key"},
		{name: "url", in: `{"schemaVersion":1,"url":"https://example.com/office.jpg"}`, path: "/url", msg: "unknown key"},
		{name: "key case", in: `{"schemaVersion":1,"mimetype":"image/jpeg"}`, path: "/mimetype", msg: "unknown key"},
		{name: "empty hash", in: `{"schemaVersion":1,"hash":""}`, path: "/hash", msg: "empty string is not allowed"},
		{name: "number for the file name", in: `{"schemaVersion":1,"fileName":1}`, path: "/fileName", msg: "expected string, got number"},
		{name: "size 0", in: `{"schemaVersion":1,"size":0}`, path: "/size", msg: "size 0 is out of range (1 or more)"},
		{name: "negative size", in: `{"schemaVersion":1,"size":-1}`, path: "/size", msg: "size -1 is out of range (1 or more)"},
		{name: "size with a fraction", in: `{"schemaVersion":1,"size":1.5}`, path: "/size", msg: "must be an integer"},
		{name: "size with an exponent", in: `{"schemaVersion":1,"size":1e3}`, path: "/size", msg: "must be an integer"},
		{name: "size as a string", in: `{"schemaVersion":1,"size":"482133"}`, path: "/size", msg: "expected number, got string"},
		{name: "width 0", in: `{"schemaVersion":1,"width":0}`, path: "/width", msg: "width 0 is out of range (1 or more)"},
		{name: "negative height", in: `{"schemaVersion":1,"height":-1}`, path: "/height", msg: "height -1 is out of range (1 or more)"},
		{name: "null width", in: `{"schemaVersion":1,"width":null}`, path: "/width", msg: "expected number, got null"},
		{name: "empty alt", in: `{"schemaVersion":1,"alt":{}}`, path: "/alt", msg: "empty object is not allowed"},
		{name: "string for the alt", in: `{"schemaVersion":1,"alt":"会社外観"}`, path: "/alt", msg: "expected object, got string"},
		{name: "number in the alt", in: `{"schemaVersion":1,"alt":{"ja":1}}`, path: "/alt/ja", msg: "expected string, got number"},
		{name: "empty text in the caption", in: `{"schemaVersion":1,"caption":{"ja":""}}`, path: "/caption/ja", msg: "empty string is not allowed"},
		{name: "null uploadedBy", in: `{"schemaVersion":1,"uploadedBy":null}`, path: "/uploadedBy", msg: "expected string, got null"},
		{name: "duplicate key", in: `{"schemaVersion":1,"size":1,"size":2}`, path: "/size", msg: "duplicate key"},
		{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	}
	// old makes new media on each call, so that changes to its alt or caption in place show.
	old := func() Media {
		return Media{ID: "old", Size: 1, Alt: Localized{"ja": "old"}, Caption: Localized{"ja": "old"}}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := old()
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if !reflect.DeepEqual(got, old()) {
				t.Errorf("UnmarshalJSON(%s) changed the media to %#v", tt.in, got)
			}
		})
	}
}

func TestMediaNormalize(t *testing.T) {
	const ctrl = string(rune(0x0001))
	var (
		nfdPu            = string(rune(0x30D5)) + string(rune(0x309A)) // "プ" in NFD
		ideographicSpace = string(rune(0x3000))
	)
	tests := []struct {
		name string
		in   Media
		want Media
	}{
		{
			name: "control characters",
			in:   Media{FileName: "a" + ctrl + "b.jpg", Alt: Localized{"ja": "c" + ctrl + "d"}, Caption: Localized{"ja": "e" + ctrl + "f"}},
			want: Media{FileName: "ab.jpg", Alt: Localized{"ja": "cd"}, Caption: Localized{"ja": "ef"}},
		},
		{name: "line breaks and tabs in the file name", in: Media{FileName: "a\nb\r\nc\td\v.jpg"}, want: Media{FileName: "abcd.jpg"}},
		{
			name: "spaces around the file name",
			in:   Media{FileName: " \t会社外観.jpg" + ideographicSpace + "\n"},
			want: Media{FileName: "会社外観.jpg"},
		},
		{name: "space before a control character", in: Media{FileName: "会社外観.jpg " + ctrl}, want: Media{FileName: "会社外観.jpg"}},
		{name: "spaces inside the file name", in: Media{FileName: "会社 外観 (1).jpg"}, want: Media{FileName: "会社 外観 (1).jpg"}},
		{name: "kana in NFD", in: Media{FileName: nfdPu + "レスリリース.pdf"}, want: Media{FileName: "プレスリリース.pdf"}},
		{
			name: "NFC",
			in:   Media{FileName: "cafe" + acute + ".jpg", Alt: Localized{"en": "cafe" + acute}, Caption: Localized{"en": "cafe" + acute}},
			want: Media{FileName: "caf" + eAcute + ".jpg", Alt: Localized{"en": "caf" + eAcute}, Caption: Localized{"en": "caf" + eAcute}},
		},
		{name: "line breaks and tabs left in the alt", in: Media{Alt: Localized{"ja": "a\r\nb\tc"}}, want: Media{Alt: Localized{"ja": "a\r\nb\tc"}}},
		{
			name: "line breaks in the caption",
			in:   Media{Caption: Localized{"ja": "a\r\nb\rc\td", "en": "e\r\n"}},
			want: Media{Caption: Localized{"ja": "a\nb\nc\td", "en": "e\n"}},
		},
		{
			name: "languages of which nothing is left",
			in:   Media{Alt: Localized{"ja": "a", "en": ctrl}, Caption: Localized{"ja": ctrl, "en": "\v"}},
			want: Media{Alt: Localized{"ja": "a"}},
		},
		{name: "nothing left in the file name", in: Media{FileName: " " + ctrl + "\n"}, want: Media{}},
		{name: "empty alt and caption", in: Media{Alt: Localized{}, Caption: Localized{}}, want: Media{}},
		{
			name: "other items left as they are",
			in:   Media{ID: " x", Hash: "SHA256:" + ctrl, MIMEType: "IMAGE/JPEG ", UploadedAt: "\tx", UploadedBy: "x" + ctrl},
			want: Media{ID: " x", Hash: "SHA256:" + ctrl, MIMEType: "IMAGE/JPEG ", UploadedAt: "\tx", UploadedBy: "x" + ctrl},
		},
		{name: "image", in: officeImage, want: officeImage},
		{name: "PDF", in: companyGuidePDF, want: companyGuidePDF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			// Normalize changes the alt and the caption in place.
			got.Alt, got.Caption = maps.Clone(tt.in.Alt), maps.Clone(tt.in.Caption)
			got.Normalize()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Normalize() of %#v = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestMediaValidate(t *testing.T) {
	const (
		ctrl    = string(rune(0x0001))
		pdfOnly = "not allowed for application/pdf"
	)
	var (
		nfdPu            = string(rune(0x30D5)) + string(rune(0x309A)) // "プ" in NFD
		ideographicSpace = string(rune(0x3000))
		zwj              = string(rune(0x200D))
		family           = string(rune(0x1F468)) + zwj + string(rune(0x1F469)) + zwj + string(rune(0x1F467))
		rainbowFlag      = string(rune(0x1F3F3)) + string(rune(0xFE0F)) + zwj + string(rune(0x1F308))
		hex              = strings.TrimPrefix(officeImage.Hash, "sha256:")
	)
	// media returns x changed by f, which gets copies of the alt and the caption of x.
	media := func(x Media, f func(x *Media)) Media {
		x.Alt, x.Caption = maps.Clone(x.Alt), maps.Clone(x.Caption)
		f(&x)
		return x
	}
	hash := func(s string) Media { return media(officeImage, func(x *Media) { x.Hash = s }) }
	badHash := func(s string) string {
		return "/hash: invalid hash " + strconv.Quote(s) + `: use "sha256:" and 64 lower case hex digits`
	}
	mimeType := func(x Media, s string) Media { return media(x, func(x *Media) { x.MIMEType = s }) }
	unsupported := func(s string) string {
		return "/mimeType: unsupported MIME type " + strconv.Quote(s) + " (use image/jpeg, image/png, image/webp, image/gif, application/pdf)"
	}
	pixels := func(w, h int) Media { return media(officeImage, func(x *Media) { x.Width, x.Height = w, h }) }
	fileName := func(s string) Media { return media(officeImage, func(x *Media) { x.FileName = s }) }
	badCharacter := func(r rune) string {
		return "/fileName: character " + strconv.QuoteRune(r) + " (U+" + strings.ToUpper(strconv.FormatInt(int64(r), 16)) +
			") is not allowed in file names"
	}
	tests := []struct {
		name  string
		in    Media
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "image", in: officeImage},
		{name: "PDF", in: companyGuidePDF},
		{name: "required items of an image only", in: media(officeImage, func(x *Media) { x.Alt, x.Caption = nil, nil })},
		{
			name:  "missing items",
			in:    Media{},
			paths: []string{"/id", "/hash", "/fileName", "/mimeType", "/size", "/uploadedAt", "/uploadedBy"},
			msg: "/id: missing id\n/hash: missing hash\n/fileName: missing fileName\n/mimeType: missing mimeType\n" +
				"/size: missing size\n/uploadedAt: missing uploadedAt\n/uploadedBy: missing uploadedBy",
		},
		{
			name:  "missing items of an image",
			in:    Media{MIMEType: "image/gif"},
			paths: []string{"/id", "/hash", "/fileName", "/size", "/width", "/height", "/uploadedAt", "/uploadedBy"},
			msg:   "/size: missing size\n/width: missing width\n/height: missing height\n/uploadedAt: missing uploadedAt",
		},
		{
			name:  "missing items of a PDF file",
			in:    Media{MIMEType: "application/pdf"},
			paths: []string{"/id", "/hash", "/fileName", "/size", "/uploadedAt", "/uploadedBy"},
		},
		{
			name:  "missing width",
			in:    media(officeImage, func(x *Media) { x.Width = 0 }),
			paths: []string{"/width"},
			msg:   "/width: missing width",
		},
		{
			name:  "missing height",
			in:    media(officeImage, func(x *Media) { x.Height = 0 }),
			paths: []string{"/height"},
			msg:   "/height: missing height",
		},
		{
			// The errors are in document order.
			name: "invalid items",
			in: Media{
				ID:         testUser,
				Hash:       "sha256:" + strings.ToUpper(hex),
				FileName:   "../会社外観.jpg",
				MIMEType:   "image/svg+xml",
				Size:       -1,
				Width:      -1,
				Height:     -2,
				Alt:        Localized{"fr": "x", "ja": "a\nb"},
				Caption:    Localized{"de": "x", "en": "a" + ctrl},
				UploadedAt: "2026-10-25 10:00:00+09:00",
				UploadedBy: testAuthor,
			},
			paths: []string{
				"/id", "/hash", "/fileName", "/mimeType", "/size", "/width", "/height",
				"/alt/ja", "/alt/fr", "/caption/en", "/caption/de", "/uploadedAt", "/uploadedBy",
			},
			msg: `/id: invalid media ID "` + testUser + `": use "med_" and a ULID (26 characters in upper case)` + "\n" +
				badHash("sha256:"+strings.ToUpper(hex)) + "\n" +
				"/fileName: character '/' is not allowed in file names\n" +
				unsupported("image/svg+xml") + "\n" +
				"/size: size -1 is out of range (1 or more)\n/width: width -1 is out of range (1 or more)\n" +
				"/height: height -2 is out of range (1 or more)\n/alt/ja: contains a line break or tab\n" +
				`/alt/fr: language "fr" is not in the site settings (use ja, en)` + "\n" +
				"/caption/en: contains control characters; normalize it first\n" +
				`/caption/de: language "de" is not in the site settings (use ja, en)` + "\n" +
				`/uploadedAt: invalid date and time "2026-10-25 10:00:00+09:00": use RFC 3339 with seconds and a time zone offset, ` +
				`such as "2026-10-25T10:00:00+09:00"` + "\n" +
				`/uploadedBy: invalid user ID "` + testAuthor + `": use "usr_" and a ULID (26 characters in upper case)`,
		},
		{
			// The pixels of an image are reported at the root, after its width and height.
			name: "invalid items of an image",
			in: media(officeImage, func(x *Media) {
				x.Size, x.Width, x.Height = 64<<20+1, 20000, 20000
				x.Alt["en"] = "a\tb"
				x.Caption["ja"] = "a\r\nb"
			}),
			paths: []string{"/size", "", "/alt/en", "/caption/ja"},
			msg: "/size: too large: 67108865 bytes (max 67108864)\ntoo many pixels: 20000x20000 (max 100000000)\n" +
				"/alt/en: contains a line break or tab\n/caption/ja: contains control characters; normalize it first",
		},
		{
			name: "items of an image in a PDF file",
			in: media(companyGuidePDF, func(x *Media) {
				x.Width, x.Height, x.Alt, x.Caption = 1600, -1, Localized{"ja": "会社案内"}, Localized{"fr": "a\r\nb"}
			}),
			paths: []string{"/width", "/height", "/alt", "/caption"},
			msg:   "/width: " + pdfOnly + "\n/height: " + pdfOnly + "\n/alt: " + pdfOnly + "\n/caption: " + pdfOnly,
		},

		{
			name:  "ID of another kind",
			in:    media(officeImage, func(x *Media) { x.ID = testAboutPage }),
			paths: []string{"/id"},
			msg:   `/id: invalid media ID "` + testAboutPage + `": use "med_" and a ULID (26 characters in upper case)`,
		},
		{name: "hash with every hex digit", in: hash("sha256:" + strings.Repeat("0123456789abcdef", 4))},
		{name: "hash in upper case", in: hash("sha256:" + strings.ToUpper(hex)), paths: []string{"/hash"}, msg: badHash("sha256:" + strings.ToUpper(hex))},
		{name: "hash with 63 digits", in: hash("sha256:" + hex[:63]), paths: []string{"/hash"}, msg: badHash("sha256:" + hex[:63])},
		{name: "hash with 65 digits", in: hash("sha256:" + hex + "0"), paths: []string{"/hash"}, msg: badHash("sha256:" + hex + "0")},
		{name: "hash with a letter that is not a hex digit", in: hash("sha256:" + hex[:63] + "g"), paths: []string{"/hash"}},
		{name: "MD5 hash", in: hash("md5:" + hex[:32]), paths: []string{"/hash"}, msg: badHash("md5:" + hex[:32])},
		{name: "hash without the prefix", in: hash(hex), paths: []string{"/hash"}, msg: badHash(hex)},
		{name: "prefix in upper case", in: hash("SHA256:" + hex), paths: []string{"/hash"}, msg: badHash("SHA256:" + hex)},
		{name: "text before the hash", in: hash("x" + officeImage.Hash), paths: []string{"/hash"}, msg: badHash("x" + officeImage.Hash)},
		{name: "line break after the hash", in: hash(officeImage.Hash + "\n"), paths: []string{"/hash"}, msg: badHash(officeImage.Hash + "\n")},

		{name: "PNG", in: mimeType(officeImage, "image/png")},
		{name: "WebP", in: mimeType(officeImage, "image/webp")},
		{name: "GIF", in: mimeType(officeImage, "image/gif")},
		{name: "SVG", in: mimeType(officeImage, "image/svg+xml"), paths: []string{"/mimeType"}, msg: unsupported("image/svg+xml")},
		{name: "AVIF", in: mimeType(officeImage, "image/avif"), paths: []string{"/mimeType"}, msg: unsupported("image/avif")},
		{name: "nonstandard MIME type of JPEG", in: mimeType(officeImage, "image/jpg"), paths: []string{"/mimeType"}, msg: unsupported("image/jpg")},
		{name: "MIME type in upper case", in: mimeType(officeImage, "IMAGE/JPEG"), paths: []string{"/mimeType"}, msg: unsupported("IMAGE/JPEG")},
		{
			name:  "MIME type with a parameter",
			in:    mimeType(companyGuidePDF, "application/pdf; charset=binary"),
			paths: []string{"/mimeType"},
			msg:   unsupported("application/pdf; charset=binary"),
		},
		{name: "unknown MIME type of a PDF file", in: mimeType(companyGuidePDF, "application/zip"), paths: []string{"/mimeType"}},
		{
			name:  "alt with a line break of an unknown MIME type",
			in:    media(officeImage, func(x *Media) { x.MIMEType, x.Alt["ja"] = "image/avif", "a\nb" }),
			paths: []string{"/mimeType", "/alt/ja"},
			msg:   unsupported("image/avif") + "\n/alt/ja: contains a line break or tab",
		},
		{
			name:  "PDF file as an image",
			in:    mimeType(companyGuidePDF, "image/png"),
			paths: []string{"/width", "/height"},
			msg:   "/width: missing width\n/height: missing height",
		},
		{
			name:  "image as a PDF file",
			in:    mimeType(officeImage, "application/pdf"),
			paths: []string{"/width", "/height", "/alt", "/caption"},
			msg:   "/width: " + pdfOnly + "\n/height: " + pdfOnly + "\n/alt: " + pdfOnly + "\n/caption: " + pdfOnly,
		},

		{name: "size 1", in: media(officeImage, func(x *Media) { x.Size = 1 })},
		{name: "size at the limit", in: media(officeImage, func(x *Media) { x.Size = 64 << 20 })},
		{name: "PDF file at the limit", in: media(companyGuidePDF, func(x *Media) { x.Size = 64 << 20 })},
		{
			name:  "size over the limit",
			in:    media(officeImage, func(x *Media) { x.Size = 64<<20 + 1 }),
			paths: []string{"/size"},
			msg:   "/size: too large: 67108865 bytes (max 67108864)",
		},
		{
			name:  "largest size",
			in:    media(companyGuidePDF, func(x *Media) { x.Size = math.MaxInt }),
			paths: []string{"/size"},
			msg:   "/size: too large: " + strconv.Itoa(math.MaxInt) + " bytes (max 67108864)",
		},
		{
			name:  "negative size",
			in:    media(officeImage, func(x *Media) { x.Size = -1 }),
			paths: []string{"/size"},
			msg:   "/size: size -1 is out of range (1 or more)",
		},

		{name: "1 pixel", in: pixels(1, 1)},
		{name: "pixels at the limit", in: pixels(10000, 10000)},
		{name: "row of pixels at the limit", in: pixels(100000000, 1)},
		{name: "column of pixels at the limit", in: pixels(1, 100000000)},
		{name: "too many pixels", in: pixels(10001, 10000), paths: []string{""}, msg: "too many pixels: 10001x10000 (max 100000000)"},
		{name: "too many pixels in height", in: pixels(10000, 10001), paths: []string{""}, msg: "too many pixels: 10000x10001 (max 100000000)"},
		{name: "row of too many pixels", in: pixels(100000001, 1), paths: []string{""}, msg: "too many pixels: 100000001x1 (max 100000000)"},
		{name: "column of too many pixels", in: pixels(1, 100000001), paths: []string{""}, msg: "too many pixels: 1x100000001 (max 100000000)"},
		{
			// The product overflows to 1.
			name:  "largest width and height",
			in:    pixels(math.MaxInt, math.MaxInt),
			paths: []string{""},
			msg:   "too many pixels: " + strconv.Itoa(math.MaxInt) + "x" + strconv.Itoa(math.MaxInt) + " (max 100000000)",
		},
		{
			// The product overflows to a negative number.
			name:  "pixels that overflow",
			in:    pixels(math.MaxInt/2+1, 2),
			paths: []string{""},
			msg:   "too many pixels: " + strconv.Itoa(math.MaxInt/2+1) + "x2 (max 100000000)",
		},
		{
			name:  "negative width",
			in:    pixels(-1, 1067),
			paths: []string{"/width"},
			msg:   "/width: width -1 is out of range (1 or more)",
		},
		{
			name:  "negative width and height of an unknown MIME type",
			in:    media(pixels(-1, -1), func(x *Media) { x.MIMEType = "image/avif" }),
			paths: []string{"/mimeType", "/width", "/height"},
			msg:   "/width: width -1 is out of range (1 or more)\n/height: height -1 is out of range (1 or more)",
		},
		{
			name:  "too many pixels of an unknown MIME type",
			in:    media(pixels(20000, 20000), func(x *Media) { x.MIMEType = "image/avif" }),
			paths: []string{"/mimeType"},
		},
		{
			name:  "too many pixels of a PDF file",
			in:    media(companyGuidePDF, func(x *Media) { x.Width, x.Height = 20000, 20000 }),
			paths: []string{"/width", "/height"},
			msg:   "/width: " + pdfOnly + "\n/height: " + pdfOnly,
		},

		{
			name:  "width of a PDF file",
			in:    media(companyGuidePDF, func(x *Media) { x.Width = 1600 }),
			paths: []string{"/width"},
			msg:   "/width: " + pdfOnly,
		},
		{
			name:  "height of a PDF file",
			in:    media(companyGuidePDF, func(x *Media) { x.Height = 1067 }),
			paths: []string{"/height"},
			msg:   "/height: " + pdfOnly,
		},
		{
			name:  "alt of a PDF file",
			in:    media(companyGuidePDF, func(x *Media) { x.Alt = Localized{"ja": "会社案内"} }),
			paths: []string{"/alt"},
			msg:   "/alt: " + pdfOnly,
		},
		{
			name:  "caption of a PDF file",
			in:    media(companyGuidePDF, func(x *Media) { x.Caption = Localized{"ja": "会社案内（2024年版）"} }),
			paths: []string{"/caption"},
			msg:   "/caption: " + pdfOnly,
		},
		{name: "empty alt and caption of a PDF file", in: media(companyGuidePDF, func(x *Media) { x.Alt, x.Caption = Localized{}, Localized{} })},

		{name: "alt in one language", in: media(officeImage, func(x *Media) { delete(x.Alt, "en") })},
		{
			name:  "line break in the alt",
			in:    media(officeImage, func(x *Media) { x.Alt["ja"] = "会社\n外観" }),
			paths: []string{"/alt/ja"},
			msg:   "/alt/ja: contains a line break or tab",
		},
		{
			name:  "tab in the alt",
			in:    media(officeImage, func(x *Media) { x.Alt["en"] = "Our\toffice" }),
			paths: []string{"/alt/en"},
			msg:   "/alt/en: contains a line break or tab",
		},
		{
			name:  "alt in a language not in the site settings",
			in:    media(officeImage, func(x *Media) { x.Alt["fr"] = "Nos bureaux" }),
			paths: []string{"/alt/fr"},
			msg:   `/alt/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "empty text in the alt",
			in:    media(officeImage, func(x *Media) { x.Alt["en"] = "" }),
			paths: []string{"/alt/en"},
			msg:   "/alt/en: empty value; omit it",
		},
		{
			name:  "alt not in NFC",
			in:    media(officeImage, func(x *Media) { x.Alt["en"] = "cafe" + acute }),
			paths: []string{"/alt/en"},
			msg:   "/alt/en: not in Unicode NFC; normalize it first",
		},
		{name: "caption with line breaks and tabs", in: media(officeImage, func(x *Media) { x.Caption["ja"] = "本社の外観\n2024年\t撮影\n" })},
		{
			name:  "CR in the caption",
			in:    media(officeImage, func(x *Media) { x.Caption["ja"] = "本社の外観\r\n2024年撮影" }),
			paths: []string{"/caption/ja"},
			msg:   "/caption/ja: contains control characters; normalize it first",
		},
		{
			name:  "caption in a language not in the site settings",
			in:    media(officeImage, func(x *Media) { x.Caption["de"] = "Unser Hauptsitz" }),
			paths: []string{"/caption/de"},
			msg:   `/caption/de: language "de" is not in the site settings (use ja, en)`,
		},

		{name: "file name without an extension", in: fileName("会社外観")},
		{name: "file name with spaces inside", in: fileName("会社 外観 (1).jpg")},
		{name: "file name of dots", in: fileName("...")},
		{name: "file name of 255 bytes", in: fileName(strings.Repeat("a", 251) + ".jpg")},
		{name: "Japanese file name of 255 bytes", in: fileName(strings.Repeat("あ", 85))},
		{name: "emoji ZWJ sequence", in: fileName(family + ".png")},
		{name: "emoji ZWJ sequence with a variation selector", in: fileName(rainbowFlag + ".gif")},
		{
			name:  "file name of 256 bytes",
			in:    fileName(strings.Repeat("a", 252) + ".jpg"),
			paths: []string{"/fileName"},
			msg:   "/fileName: too long: 256 bytes (max 255)",
		},
		{
			name:  "Japanese file name of 258 bytes",
			in:    fileName(strings.Repeat("あ", 86)),
			paths: []string{"/fileName"},
			msg:   "/fileName: too long: 258 bytes (max 255)",
		},
		{
			name:  "slash",
			in:    fileName("2024/会社外観.jpg"),
			paths: []string{"/fileName"},
			msg:   "/fileName: character '/' is not allowed in file names",
		},
		{
			name:  "backslash",
			in:    fileName(`C:\写真\会社外観.jpg`),
			paths: []string{"/fileName"},
			msg:   `/fileName: character '\\' is not allowed in file names`,
		},
		{name: "dot", in: fileName("."), paths: []string{"/fileName"}, msg: `/fileName: file names cannot be "." or ".."`},
		{name: "two dots", in: fileName(".."), paths: []string{"/fileName"}, msg: `/fileName: file names cannot be "." or ".."`},
		{
			name:  "right-to-left override",
			in:    fileName("photo" + string(rune(0x202E)) + "gpj.exe"),
			paths: []string{"/fileName"},
			msg:   badCharacter(0x202E),
		},
		{
			name:  "left-to-right isolate",
			in:    fileName(string(rune(0x2066)) + "会社外観.jpg"),
			paths: []string{"/fileName"},
			msg:   badCharacter(0x2066),
		},
		{
			name:  "right-to-left mark",
			in:    fileName("会社外観" + string(rune(0x200F)) + ".jpg"),
			paths: []string{"/fileName"},
			msg:   badCharacter(0x200F),
		},
		{
			name:  "control character in the file name",
			in:    fileName("会社" + ctrl + "外観.jpg"),
			paths: []string{"/fileName"},
			msg:   "/fileName: contains control characters; normalize it first",
		},
		{
			name:  "tab in the file name",
			in:    fileName("会社\t外観.jpg"),
			paths: []string{"/fileName"},
			msg:   "/fileName: contains control characters; normalize it first",
		},
		{
			name:  "line break at the end of the file name",
			in:    fileName("会社外観.jpg\n"),
			paths: []string{"/fileName"},
			msg:   "/fileName: contains control characters; normalize it first",
		},
		{
			name:  "space before the file name",
			in:    fileName(" 会社外観.jpg"),
			paths: []string{"/fileName"},
			msg:   "/fileName: starts or ends with a space; normalize it first",
		},
		{
			name:  "ideographic space after the file name",
			in:    fileName("会社外観.jpg" + ideographicSpace),
			paths: []string{"/fileName"},
			msg:   "/fileName: starts or ends with a space; normalize it first",
		},
		{
			name:  "file name not in NFC",
			in:    fileName("cafe" + acute + ".jpg"),
			paths: []string{"/fileName"},
			msg:   "/fileName: not in Unicode NFC; normalize it first",
		},
		{
			name:  "kana in NFD in the file name",
			in:    fileName(nfdPu + "レスリリース.pdf"),
			paths: []string{"/fileName"},
			msg:   "/fileName: not in Unicode NFC; normalize it first",
		},
		{
			name:  "invalid UTF-8 in the file name",
			in:    fileName("a\xffb.jpg"),
			paths: []string{"/fileName"},
			msg:   "/fileName: invalid UTF-8",
		},

		{name: "uploadedAt in UTC", in: media(officeImage, func(x *Media) { x.UploadedAt = "2026-10-25T01:00:00Z" })},
		{
			name:  "uploadedAt without an offset",
			in:    media(officeImage, func(x *Media) { x.UploadedAt = "2026-10-25T10:00:00" }),
			paths: []string{"/uploadedAt"},
			msg:   `/uploadedAt: invalid date and time "2026-10-25T10:00:00"`,
		},
		{
			name:  "uploadedBy of another kind",
			in:    media(officeImage, func(x *Media) { x.UploadedBy = testAuthor }),
			paths: []string{"/uploadedBy"},
			msg:   `/uploadedBy: invalid user ID "` + testAuthor + `": use "usr_" and a ULID (26 characters in upper case)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate(testSite())
			if tt.paths == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if got := errorPaths(t, err); !reflect.DeepEqual(got, tt.paths) {
				t.Errorf("Validate() paths = %q, want %q", got, tt.paths)
			}
			if tt.msg != "" {
				assertErrorMsg(t, err, tt.msg)
			}
		})
	}
}

func TestMediaKind(t *testing.T) {
	tests := []struct {
		mimeType string
		want     string
	}{
		{mimeType: "image/jpeg", want: MediaImage},
		{mimeType: "image/png", want: MediaImage},
		{mimeType: "image/webp", want: MediaImage},
		{mimeType: "image/gif", want: MediaImage},
		{mimeType: "application/pdf", want: MediaPDF},
		{mimeType: ""},
		{mimeType: "image/svg+xml"},
		{mimeType: "image/avif"},
		{mimeType: "image/jpg"},
		{mimeType: "image/pjpeg"},
		{mimeType: "IMAGE/JPEG"},
		{mimeType: "image/jpeg "},
		{mimeType: "application/octet-stream"},
		{mimeType: "text/html"},
	}
	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			if got := MediaKind(tt.mimeType); got != tt.want {
				t.Errorf("MediaKind(%q) = %q, want %q", tt.mimeType, got, tt.want)
			}
			if got := (Media{MIMEType: tt.mimeType}).Kind(); got != tt.want {
				t.Errorf("Kind() of %q = %q, want %q", tt.mimeType, got, tt.want)
			}
		})
	}
}

func TestImageAlt(t *testing.T) {
	// image returns an image block of testImage with the attributes a.
	image := func(a Attrs) *Block {
		a.Media = testImage
		return &Block{Type: BlockImage, ID: "b_000image", Attrs: a}
	}
	office := officeImage
	tests := []struct {
		name     string
		block    *Block
		media    *Media
		language string
		want     string
		ok       bool
	}{
		{name: "decorative with the alt of the media", block: image(Attrs{Decorative: true}), media: &office, language: "ja", ok: true},
		{name: "decorative without the media", block: image(Attrs{Decorative: true}), language: "ja", ok: true},
		{name: "decorative with alt", block: image(Attrs{Decorative: true, Alt: "本社"}), media: &office, language: "ja", ok: true},
		{name: "alt of the block", block: image(Attrs{Alt: "本社"}), media: &office, language: "ja", want: "本社", ok: true},
		{name: "alt of the block without the media", block: image(Attrs{Alt: "本社"}), language: "en", want: "本社", ok: true},
		{name: "alt of the media", block: image(Attrs{}), media: &office, language: "ja", want: "会社外観", ok: true},
		{name: "alt of the media in English", block: image(Attrs{}), media: &office, language: "en", want: "Our office building", ok: true},
		{
			name:     "alt of the media only in another language",
			block:    image(Attrs{}),
			media:    &Media{Alt: Localized{"en": "Our office building"}},
			language: "ja",
		},
		{name: "media without alt", block: image(Attrs{}), media: &Media{}, language: "ja"},
		{name: "without the media", block: image(Attrs{}), language: "ja"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alt, ok := tt.block.ImageAlt(tt.media, tt.language)
			if alt != tt.want || ok != tt.ok {
				t.Errorf("ImageAlt(%v, %q) = %q, %t, want %q, %t", tt.media, tt.language, alt, ok, tt.want, tt.ok)
			}
		})
	}
}
