package content

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/nfc"
)

// Media is the metadata of an uploaded file, an image or a PDF.
type Media struct {
	ID            string
	Hash          string    // "sha256:" and 64 lower case hex digits of the file, which is stored by it
	FileName      string    // the name of the file when uploaded; never used to store the file
	MIMEType      string    // read from the content of the file, not from the upload or the file name
	Size          int       // bytes
	Width, Height int       // images: in pixels as displayed, after the EXIF orientation
	Alt           Localized // images: one line
	Caption       Localized // images: with line breaks
	UploadedAt    string    // RFC 3339, as checkDatetime accepts
	UploadedBy    string    // ID of the user
}

// mediaTypes are the MIME types of media and their kinds, in the order that messages list them.
var mediaTypes = []struct{ mimeType, kind string }{
	{"image/jpeg", MediaImage},
	{"image/png", MediaImage},
	{"image/webp", MediaImage},
	{"image/gif", MediaImage},
	{"application/pdf", MediaPDF},
}

// MediaKind returns the kind of media of the MIME type, MediaImage or MediaPDF, or "" if media cannot have the type.
func MediaKind(mimeType string) string {
	for _, t := range mediaTypes {
		if t.mimeType == mimeType {
			return t.kind
		}
	}
	return ""
}

// Kind returns the kind of x as MediaKind does, which ValidateOptions.Lookup tells as Ref.Type.
func (x Media) Kind() string { return MediaKind(x.MIMEType) }

func (x *Media) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Media) decode(d *decoder) error {
	var v Media
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "id":
			v.ID, err = d.string()
		case "hash":
			v.Hash, err = d.string()
		case "fileName":
			v.FileName, err = d.string()
		case "mimeType":
			v.MIMEType, err = d.string()
		case "size":
			v.Size, err = readInt(d, func(n int) error { return checkPositive("size", n) })
		case "width":
			v.Width, err = readInt(d, func(n int) error { return checkPositive("width", n) })
		case "height":
			v.Height, err = readInt(d, func(n int) error { return checkPositive("height", n) })
		case "alt":
			err = v.Alt.decode(d)
		case "caption":
			err = v.Caption.decode(d)
		case "uploadedAt":
			v.UploadedAt, err = d.string()
		case "uploadedBy":
			v.UploadedBy, err = d.string()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	if !version {
		return d.errorf(`missing key "schemaVersion"`)
	}
	*x = v
	return nil
}

func (x Media) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion int       `json:"schemaVersion"`
		ID            string    `json:"id,omitempty"`
		Hash          string    `json:"hash,omitempty"`
		FileName      string    `json:"fileName,omitempty"`
		MIMEType      string    `json:"mimeType,omitempty"`
		Size          int       `json:"size,omitzero"`
		Width         int       `json:"width,omitzero"`
		Height        int       `json:"height,omitzero"`
		Alt           Localized `json:"alt,omitempty"`
		Caption       Localized `json:"caption,omitempty"`
		UploadedAt    string    `json:"uploadedAt,omitempty"`
		UploadedBy    string    `json:"uploadedBy,omitempty"`
	}{
		SchemaVersion: SchemaVersion,
		ID:            x.ID,
		Hash:          x.Hash,
		FileName:      x.FileName,
		MIMEType:      x.MIMEType,
		Size:          x.Size,
		Width:         x.Width,
		Height:        x.Height,
		Alt:           x.Alt,
		Caption:       x.Caption,
		UploadedAt:    x.UploadedAt,
		UploadedBy:    x.UploadedBy,
	})
}

// Normalize removes control characters and surrounding spaces from the file name before NFC, cleans up the alt
// and the caption in place as Revision.Normalize does texts, and removes the languages of which nothing is left.
func (x *Media) Normalize() {
	x.FileName = normalizeFileName(x.FileName)
	x.Alt.normalize(false)
	x.Caption.normalize(true)
}

// Validate checks x in site. Every item is required apart from Width, Height, Alt and Caption, which are only for images;
// images must have Width and Height.
func (x Media) Validate(site *Site) error {
	var c checker
	x.check(&c, site)
	return c.result()
}

func (x Media) check(c *checker, site *Site) {
	c.required("id", x.ID, func(s string) error { return checkID(RefMedia, s) })
	c.required("hash", x.Hash, checkHash)
	c.required("fileName", x.FileName, checkFileName)
	c.required("mimeType", x.MIMEType, checkMIMEType)
	switch {
	case x.Size == 0:
		c.addf("/size", "missing size")
	case x.Size > MaxMediaBytes:
		c.addf("/size", "too large: %d bytes (max %d)", x.Size, MaxMediaBytes)
	default:
		c.add("/size", checkPositive("size", x.Size))
	}
	kind := x.Kind() // "" for an unknown MIME type, to which neither the rules of images nor those of PDF files apply
	// forImages reports whether to check key, which is set; it reports key instead for a PDF file.
	forImages := func(key string) bool {
		if kind == MediaPDF {
			c.addf("/"+key, "not allowed for %s", x.MIMEType)
			return false
		}
		return true
	}
	dimension := func(key string, v int) {
		switch {
		case v == 0 && kind == MediaImage:
			c.addf("/"+key, "missing %s", key)
		case v != 0 && forImages(key):
			c.add("/"+key, checkPositive(key, v))
		}
	}
	dimension("width", x.Width)
	dimension("height", x.Height)
	// Not x.Width*x.Height, which can overflow.
	if kind == MediaImage && x.Height > 0 && x.Width > MaxImagePixels/x.Height {
		c.addf("", "too many pixels: %dx%d (max %d)", x.Width, x.Height, MaxImagePixels)
	}
	if len(x.Alt) > 0 && forImages("alt") {
		x.Alt.check(c, "/alt", site, false)
	}
	if len(x.Caption) > 0 && forImages("caption") {
		x.Caption.check(c, "/caption", site, true)
	}
	c.required("uploadedAt", x.UploadedAt, checkDatetime)
	c.required("uploadedBy", x.UploadedBy, func(s string) error { return checkID(RefUser, s) })
}

func checkPositive(name string, v int) error {
	if v < 1 {
		return fmt.Errorf("%s %d is out of range (1 or more)", name, v)
	}
	return nil
}

func checkMIMEType(s string) error {
	if MediaKind(s) != "" {
		return nil
	}
	names := make([]string, len(mediaTypes))
	for i, t := range mediaTypes {
		names[i] = t.mimeType
	}
	return fmt.Errorf("unsupported MIME type %q (use %s)", s, strings.Join(names, ", "))
}

var hashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func checkHash(s string) error {
	if !hashPattern.MatchString(s) {
		return fmt.Errorf(`invalid hash %q: use "sha256:" and 64 lower case hex digits`, s)
	}
	return nil
}

// normalizeFileName removes control characters, which include line breaks and tabs, and surrounding spaces
// as normalizeHref does, before NFC.
func normalizeFileName(s string) string {
	return nfc.Normalize(normalizeHref(s))
}

// checkFileName accepts the name of an uploaded file in NFC without control characters, surrounding spaces, "/", "\"
// or bidirectional formatting characters, which can disguise the extension, as in "photo" + U+202E + "gpj.exe".
func checkFileName(s string) error {
	if len(s) > MaxFileNameBytes {
		return fmt.Errorf("too long: %d bytes (max %d)", len(s), MaxFileNameBytes)
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	for _, r := range s {
		switch {
		case unicode.IsControl(r):
			return errors.New("contains control characters; normalize it first")
		case r == '/' || r == '\\':
			return fmt.Errorf("character %q is not allowed in file names", r)
		case unicode.Is(unicode.Bidi_Control, r):
			return fmt.Errorf("character %q (U+%04X) is not allowed in file names", r, r)
		}
	}
	if strings.TrimFunc(s, unicode.IsSpace) != s {
		return errors.New("starts or ends with a space; normalize it first")
	}
	if s == "." || s == ".." {
		return errors.New(`file names cannot be "." or ".."`)
	}
	if !nfc.IsNormalized(s) {
		return errors.New("not in Unicode NFC; normalize it first")
	}
	return nil
}

// ImageAlt returns the alt to render x, an image block, with m, its medium (nil if not found), on a page in language:
// "" if x is decorative, else the alt of x, else the alt of m in language.
// ok is false if there is none, as for some imported images, which the renderer should warn about.
func (x *Block) ImageAlt(m *Media, language string) (alt string, ok bool) {
	switch {
	case x.Attrs.Decorative:
		return "", true
	case x.Attrs.Alt != "":
		return x.Attrs.Alt, true
	case m != nil && m.Alt[language] != "":
		return m.Alt[language], true
	}
	return "", false
}
