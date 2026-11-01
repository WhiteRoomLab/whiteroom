package imaging

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

// droppedExts are the extensions that baseName drops from file names.
var droppedExts = []string{".jpg", ".jpeg", ".jpe", ".jfif", ".png", ".apng", ".gif", ".webp", ".pdf"}

const maxBaseBytes = 200 // so that a name with "-<width>x<height>" and an extension has at most 255 bytes

// baseName returns the start of the names of the files made from an original uploaded as fileName: fileName without
// the trailing dots and the extension of an image or a PDF, with each run of characters other than letters, numbers,
// marks after them and "-_.~@+" replaced by "-", without "-" and "." at both ends, and cut to 200 bytes; or "file".
func baseName(fileName string) string {
	s := strings.TrimRight(fileName, ".")
	if i := strings.LastIndexByte(s, '.'); i >= 0 &&
		slices.ContainsFunc(droppedExts, func(ext string) bool { return asciiEqualFold(s[i:], ext) }) {
		s = s[:i]
	}
	b := make([]byte, 0, len(s))
	word := false // the last rune is a letter, a number or a mark after them
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r), word && unicode.IsMark(r):
			b = utf8.AppendRune(b, r)
			word = true
			continue
		case strings.ContainsRune("_.~@+", r):
			b = append(b, byte(r))
		case len(b) == 0 || b[len(b)-1] != '-':
			b = append(b, '-')
		}
		word = false
	}
	name := strings.Trim(string(b), "-.")
	if len(name) > maxBaseBytes {
		// Cut where a rune other than a mark starts, so that marks stay with the letters before them.
		cut := maxBaseBytes
		for cut > 0 && (!utf8.RuneStart(name[cut]) || isMark(name[cut:])) {
			cut--
		}
		name = strings.Trim(name[:cut], "-.")
	}
	if name == "" {
		return "file"
	}
	return name
}

func isMark(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsMark(r)
}

// asciiEqualFold reports whether s and t are equal apart from the case of ASCII letters.
func asciiEqualFold(s, t string) bool {
	if len(s) != len(t) {
		return false
	}
	for i := range len(s) {
		if lower(s[i]) != lower(t[i]) {
			return false
		}
	}
	return true
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// Name returns the name of the file of variant v made from an image uploaded as fileName, which Inspect read as info:
// the base name from fileName and the extension of v's format, with "-<width>x<height>" between them unless v is the
// published file, as in "photo.jpg" and "photo-640x427.jpg".
func Name(fileName string, info Info, v Variant) string {
	name := baseName(fileName)
	if v != Published(info) {
		name += "-" + strconv.Itoa(v.Width) + "x" + strconv.Itoa(v.Height)
	}
	return name + ext(v.Format)
}

// PDFName returns the name of the published file of a PDF uploaded as fileName, as in "report.pdf".
func PDFName(fileName string) string {
	return baseName(fileName) + ext(mediafile.PDF)
}

// Path returns the path from the site of the file named name of the medium with the ID, not percent-encoded.
func Path(id, name string) string {
	return "/media/" + id + "/" + name
}

func ext(format string) string {
	switch format {
	case mediafile.JPEG:
		return ".jpg"
	case mediafile.PNG:
		return ".png"
	case mediafile.GIF:
		return ".gif"
	case mediafile.WebP:
		return ".webp"
	case mediafile.PDF:
		return ".pdf"
	}
	return ""
}

// goVersion is the version of Go whose encoders make the files, in the cache keys: its major and minor versions, as in
// "go1.27", or runtime.Version as it is if it has another form, as development versions have.
var goVersion = goMinor(runtime.Version())

// goMinor returns the version of Go v without its patch version, as "go1.27" for "go1.27.3" and "go1.27", or v as it
// is if it has another form.
func goMinor(v string) string {
	s, ok := strings.CutPrefix(v, "go")
	parts := strings.Split(s, ".")
	if !ok || len(parts) < 2 || len(parts) > 3 {
		return v
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return v
		}
	}
	return "go" + parts[0] + "." + parts[1]
}

// cacheKey returns the key of the file of variant v made from the original whose Media.Hash is hash: the hex SHA-256
// of keyText.
func cacheKey(hash string, v Variant) string {
	sum := sha256.Sum256([]byte(keyText(goVersion, hash, v)))
	return hex.EncodeToString(sum[:])
}

// keyText returns a text of everything that the bytes of the file of variant v made from the original whose Media.Hash
// is hash with the version of Go goVer depend on.
func keyText(goVer, hash string, v Variant) string {
	return fmt.Sprintf(
		"whiteroom imaging\nversion %d\ngo %q\nhash %q\nsize %dx%d\nformat %q\nanimated %t\nquality %d\n",
		processingVersion, goVer, hash, v.Width, v.Height, v.Format, v.Animated, jpegQuality)
}

// Version returns the version of the file of variant v made from the original whose Media.Hash is hash, which pages
// put after its path as "?v=<version>": the first 8 hex digits of its cache key, which changes with anything that the
// bytes of the file depend on, the minor version of Go included.
func Version(hash string, v Variant) string {
	return cacheKey(hash, v)[:8]
}

// PDFVersion returns the version of the published file of a PDF whose Media.Hash is hash: the first 8 hex digits of
// its SHA-256.
func PDFVersion(hash string) string {
	d := strings.TrimPrefix(hash, "sha256:")
	return d[:min(8, len(d))]
}
