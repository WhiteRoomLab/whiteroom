package imaging

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

func TestBaseName(t *testing.T) {
	r := func(rs ...rune) string { return string(rs) }
	var (
		emoji     = r(0x1f389)        // PARTY POPPER, a symbol
		heart     = r(0x2764, 0xfe0f) // HEAVY BLACK HEART and VARIATION SELECTOR-16, a mark
		namaste   = r(0x0928, 0x092e, 0x0938, 0x094d, 0x0924, 0x0947)
		tilde     = r(0x0303) // COMBINING TILDE, which no letter has precomposed with q
		keycap    = r(0xfe0f, 0x20e3)
		a199      = strings.Repeat("a", 199)
		japanese  = strings.Repeat("あ", 83) // 249 bytes
		ideoSpace = r(0x3000)
		narrowNBS = r(0x202f)
	)
	tests := []struct{ fileName, want string }{
		{"IMG 0001 (2).JPG", "IMG-0001-2"},
		{"report.png", "report"},
		{"会社外観.jpg", "会社外観"},
		{".jpg", "file"},
		{"my.photo.final.webp", "my.photo.final"},
		{"a?b#c%20d.pdf", "a-b-c-20d"},
		{emoji + " party " + emoji + ".png", "party"},
		{emoji + ".png", "file"},
		{heart + " love.png", "love"},
		{"写真（1）.png", "写真-1"},
		{"夏" + ideoSpace + "休み.jpg", "夏-休み"},
		{"コーヒー.png", "コーヒー"},
		{"Ünïcödé.png", "Ünïcödé"},
		{"Screenshot 2024-01-01 at 10.00.00" + narrowNBS + "AM.png", "Screenshot-2024-01-01-at-10.00.00-AM"},
		// Marks after letters and numbers are kept, and the others are replaced.
		{namaste + ".png", namaste},
		{"q" + tilde + "x.png", "q" + tilde + "x"},
		{"1" + keycap + ".png", "1" + keycap},
		{tilde + "abc.png", "abc"},
		{"a-" + tilde + "b.png", "a-b"},
		// Trailing dots, and the last extension of images and PDFs in any case of ASCII.
		{"photo.png.", "photo"},
		{"photo...", "photo"},
		{"photo..png", "photo"},
		{"photo.JPEG", "photo"},
		{"photo.JfIf", "photo"},
		{"photo.jpe", "photo"},
		{"anim.APNG", "anim"},
		{"document.pdf", "document"},
		{"photo.tiff", "photo.tiff"},
		{"photo.jpeg.bak", "photo.jpeg.bak"},
		{"photo.ｊｐｇ", "photo.ｊｐｇ"},
		{"photo", "photo"},
		// Runs of "-", and "-" and "." at both ends.
		{"a - b.png", "a-b"},
		{"--a--.png", "a"},
		{"a..b.png", "a..b"},
		{".hidden.png", "hidden"},
		{"-.-.png", "file"},
		{"a_b.c~d@e+f.png", "a_b.c~d@e+f"},
		{"2024-01-01.jpg", "2024-01-01"},
		{"!!!.png", "file"},
		{"tab\there.png", "tab-here"},
		{"a\xffb.png", "a-b"},
		{"", "file"},
		{".", "file"},
		{"..", "file"},
		// At most 200 bytes, cut where a rune other than a mark starts.
		{japanese + ".jpg", strings.Repeat("あ", 66)},
		{strings.Repeat("a", 251) + ".png", strings.Repeat("a", 200)},
		{a199 + "q" + tilde + "bbb.png", a199},
		{a199 + "qbbb.png", a199 + "q"},
		{a199 + "あb.png", a199},
		{strings.Repeat("a", 198) + "-.bbb.png", strings.Repeat("a", 198)},
	}
	for _, tt := range tests {
		if got := baseName(tt.fileName); got != tt.want {
			t.Errorf("baseName(%q) = %q, want %q", tt.fileName, got, tt.want)
		}
	}
}

func FuzzBaseName(f *testing.F) {
	for _, s := range []string{"IMG 0001 (2).JPG", "会社外観.jpg", ".jpg", "a?b#c%20d.pdf", "写真（1）.png"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, fileName string) {
		name := baseName(fileName)
		if name == "" || len(name) > maxBaseBytes || !utf8.ValidString(name) || strings.Contains(name, "--") ||
			strings.Trim(name, "-.") != name {
			t.Fatalf("baseName(%q) = %q", fileName, name)
		}
		for i, r := range name {
			if !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r) && !strings.ContainsRune("-_.~@+", r) {
				t.Fatalf("baseName(%q) = %q, with %q at %d", fileName, name, r, i)
			}
		}
		if r, _ := utf8.DecodeRuneInString(name); unicode.IsMark(r) {
			t.Fatalf("baseName(%q) = %q, which starts with a mark", fileName, name)
		}
		if baseName(name) != name && !hasDroppedExt(name) {
			t.Fatalf("baseName(%q) = %q, but baseName(%[2]q) = %q", fileName, name, baseName(name))
		}
	})
}

// hasDroppedExt reports whether baseName drops an extension from name.
func hasDroppedExt(name string) bool {
	for _, ext := range droppedExts {
		if len(name) >= len(ext) && asciiEqualFold(name[len(name)-len(ext):], ext) {
			return true
		}
	}
	return false
}

func TestName(t *testing.T) {
	medium := &content.ImageSize{Width: 300, Height: 300}
	landscape := still(4000, 3000, mediafile.JPEG)
	small := still(500, 400, mediafile.PNG)
	lossy := Info{MIMEType: mediafile.WebP, Width: 32, Height: 24, Orientation: 1, Format: mediafile.JPEG}
	animatedWebP := Info{MIMEType: mediafile.WebP, Width: 48, Height: 32, Orientation: 1, Animated: true,
		Format: mediafile.PNG}
	tests := []struct {
		fileName string
		info     Info
		v        Variant
		want     string
	}{
		{"IMG 0001 (2).JPG", landscape, Published(landscape), "IMG-0001-2.jpg"},
		{"IMG 0001 (2).JPG", landscape, jpegVariant(640, 480), "IMG-0001-2-640x480.jpg"},
		{"report.png", landscape, Published(landscape), "report.jpg"},
		{".jpg", landscape, Published(landscape), "file.jpg"},
		{"会社外観.jpg", landscape, jpegVariant(300, 200), "会社外観-300x200.jpg"},
		{"my.photo.final.webp", lossy, Published(lossy), "my.photo.final.jpg"},
		{"anim.gif", animation(400, 300), Published(animation(400, 300)), "anim.gif"},
		{"anim.gif", animation(400, 300), Icon(animation(400, 300), 192), "anim-192x192.png"},
		{"anim.webp", animatedWebP, Published(animatedWebP), "anim.webp"},
		{"anim.webp", animatedWebP, Icon(animatedWebP, 32), "anim-32x32.png"},
		// The variants that are the published file have its name.
		{"icon.png", still(512, 512, mediafile.PNG), Icon(still(512, 512, mediafile.PNG), 512), "icon.png"},
		{"small.png", small, ForSize(small, medium).Srcset[1], "small.png"},
		{"small.png", small, ForSize(small, medium).Srcset[0], "small-300x240.png"},
		// An APNG and its first frame of the same size differ in Animated.
		{
			"anim.png",
			Info{MIMEType: mediafile.PNG, Width: 32, Height: 32, Orientation: 1, Animated: true, Format: mediafile.PNG},
			pngVariant(32, 32),
			"anim-32x32.png",
		},
	}
	for _, tt := range tests {
		if got := Name(tt.fileName, tt.info, tt.v); got != tt.want {
			t.Errorf("Name(%q, %+v) = %q, want %q", tt.fileName, tt.v, got, tt.want)
		}
	}
}

func TestNameLength(t *testing.T) {
	info := still(100_000_000, 1, mediafile.WebP) // the most digits of a size
	for _, fileName := range []string{
		strings.Repeat("あ", 85), strings.Repeat("a", 255), strings.Repeat("a"+string(rune(0x0303)), 85),
	} {
		for _, v := range []Variant{{Width: 100_000_000, Height: 1, Format: mediafile.WebP}, Published(info)} {
			if name := Name(fileName, info, v); len(name) > content.MaxFileNameBytes {
				t.Errorf("Name() = %q, %d bytes (max %d)", name, len(name), content.MaxFileNameBytes)
			}
		}
	}
}

func TestPDFName(t *testing.T) {
	tests := []struct{ fileName, want string }{
		{"a?b#c%20d.pdf", "a-b-c-20d.pdf"},
		{"報告書.PDF", "報告書.pdf"},
		{".pdf", "file.pdf"},
		{"report", "report.pdf"},
	}
	for _, tt := range tests {
		if got := PDFName(tt.fileName); got != tt.want {
			t.Errorf("PDFName(%q) = %q, want %q", tt.fileName, got, tt.want)
		}
	}
}

func TestPath(t *testing.T) {
	got := Path("med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", "会社外観-300x200.jpg")
	if want := "/media/med_01JB8Z3K5QH2V6T9C4R7Y1E8B5/会社外観-300x200.jpg"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

const testHash = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCacheKey(t *testing.T) {
	v := jpegVariant(640, 480)
	// A change of this key changes every cached file and every version; change it with processingVersion.
	const want = "a3ce738e4fefa70d265f7476390025a2740dd03e5480f9684f751bdec16cbfe7"
	if got := cacheKey(testHash, v); got != want {
		t.Errorf("cacheKey() = %q, want %q", got, want)
	}
	keys := map[string]string{"": cacheKey(testHash, v)}
	for what, key := range map[string]string{
		"hash":     cacheKey("sha256:"+strings.Repeat("0", 64), v),
		"width":    cacheKey(testHash, jpegVariant(641, 480)),
		"height":   cacheKey(testHash, jpegVariant(640, 481)),
		"format":   cacheKey(testHash, pngVariant(640, 480)),
		"animated": cacheKey(testHash, Variant{Width: 640, Height: 480, Format: mediafile.JPEG, Animated: true}),
	} {
		for other, k := range keys {
			if k == key {
				t.Errorf("the keys with %q and %q changed are the same", what, other)
			}
		}
		keys[what] = key
	}
}

func TestVersion(t *testing.T) {
	v := pngVariant(32, 32)
	if got, want := Version(testHash, v), cacheKey(testHash, v)[:8]; got != want {
		t.Errorf("Version() = %q, want %q", got, want)
	}
	if got, want := PDFVersion(testHash), "01234567"; got != want {
		t.Errorf("PDFVersion() = %q, want %q", got, want)
	}
}
