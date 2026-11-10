package edit

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// Media of the sample site that the tests use.
const (
	jpegMedium = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5" // 会社外観.jpg, 1200x800, which published revisions use
	pngMedium  = "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9" // yamada.png, 256x256, the avatar of an author
	pdfMedium  = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7" // 会社案内.pdf, which a redirect and a published revision use
	logoMedium = "med_01JBG3N6V3X6Z9C2E5G8J1K4M7" // logo.png, 400x100, the logo of the site
)

// sampleMedium returns the document of the medium id of the sample site and its file.
func sampleMedium(t *testing.T, id string) (content.Media, []byte) {
	t.Helper()
	var m content.Media
	if err := m.UnmarshalJSON(sampleFile(t, docName(content.RefMedia, id))); err != nil {
		t.Fatal(err)
	}
	return m, sampleFile(t, "files/"+strings.TrimPrefix(m.Hash, "sha256:"))
}

func fileHash(file []byte) string {
	sum := sha256.Sum256(file)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// testImage returns the file of an image of w by h pixels that encode writes; the seed makes files that differ.
func testImage(t *testing.T, w, h int, seed byte, encode func(io.Writer, image.Image) error) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = byte(i*7) + seed
	}
	var b bytes.Buffer
	if err := encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testPNG(t *testing.T, w, h int, seed byte) []byte {
	return testImage(t, w, h, seed, func(out io.Writer, m image.Image) error { return png.Encode(out, m) })
}

func testJPEG(t *testing.T, w, h int, seed byte) []byte {
	return testImage(t, w, h, seed, func(out io.Writer, m image.Image) error { return jpeg.Encode(out, m, nil) })
}

func testGIF(t *testing.T, w, h int, seed byte) []byte {
	return testImage(t, w, h, seed, func(out io.Writer, m image.Image) error { return gif.Encode(out, m, nil) })
}

func testPDF(seed int) []byte {
	return []byte("%PDF-1.7\n% " + strconv.Itoa(seed) + "\n%%EOF\n")
}

// pngHeader returns the start of a PNG file whose header says that the image is w by h pixels: the signature and the IHDR
// chunk with its checksum, which is all that a size is read from.
func pngHeader(w, h uint32) []byte {
	b := []byte("\x89PNG\r\n\x1a\n")
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0) // 8 bits, RGBA, deflate, adaptive filtering, no interlace
	b = binary.BigEndian.AppendUint32(b, uint32(len(ihdr)))
	b = append(b, "IHDR"...)
	b = append(b, ihdr...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[12:]))
}

// jpegWithOrientation returns the JPEG file file with an Exif segment, which says the orientation o, after its SOI.
func jpegWithOrientation(file []byte, o uint16) []byte {
	tiff := []byte("MM\x00\x2a")
	tiff = binary.BigEndian.AppendUint32(tiff, 8)      // IFD0 follows the header
	tiff = binary.BigEndian.AppendUint16(tiff, 1)      // one entry
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112) // Orientation
	tiff = binary.BigEndian.AppendUint16(tiff, 3)      // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)      // one value
	tiff = binary.BigEndian.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0)                     // the rest of the value field
	tiff = binary.BigEndian.AppendUint32(tiff, 0) // no other IFD
	segment := append([]byte("Exif\x00\x00"), tiff...)
	out := binary.BigEndian.AppendUint16([]byte{0xff, 0xd8, 0xff, 0xe1}, uint16(2+len(segment)))
	return append(append(out, segment...), file[2:]...)
}

// wantMedium returns the document that AddMedia makes of file, which has the type mimeType and w by h pixels, with the ID
// id and the file name fileName, at the time of the clock of the tests.
func wantMedium(id, fileName string, file []byte, mimeType string, w, h int) content.Media {
	return content.Media{
		ID: id, Hash: fileHash(file), FileName: fileName, MIMEType: mimeType, Size: len(file), Width: w, Height: h,
		UploadedAt: testNowJST, UploadedBy: userID,
	}
}

// mediaText returns the JSON that MarshalJSON writes of m.
func mediaText(t *testing.T, m content.Media) string {
	t.Helper()
	data, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func hasOriginal(t *testing.T, s *store.Store, hash string) bool {
	t.Helper()
	f, err := s.OpenOriginal(hash)
	switch {
	case err == nil:
		_ = f.Close()
		return true
	case errors.Is(err, fs.ErrNotExist):
		return false
	}
	t.Fatalf("OpenOriginal(%q) = %v", hash, err)
	return false
}

func readOriginal(t *testing.T, s *store.Store, hash string) []byte {
	t.Helper()
	f, err := s.OpenOriginal(hash)
	if err != nil {
		t.Fatalf("OpenOriginal(%q) = %v", hash, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading the original %q = %v", hash, err)
	}
	return data
}

// writeOriginal puts file, whose hash is hash, into the files of the site directory dir.
func writeOriginal(t *testing.T, dir, hash string, file []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "files", strings.TrimPrefix(hash, "sha256:")), file, 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildPublic builds the site directory dir with the example theme and returns the public directory of the release.
func buildPublic(t *testing.T, dir string) string {
	t.Helper()
	d, err := sitedir.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	out := t.TempDir()
	_, err = build.Build(t.Context(), build.Options{
		Source: source{Dir: d, theme: os.DirFS(themeDir)}, Out: out, CacheDir: t.TempDir(), Workers: 2,
	})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return filepath.Join(out, "current", "public")
}

// TestAddMedia adds files and checks the documents that it makes and the originals that it saves.
func TestAddMedia(t *testing.T) {
	// The files of the sample site: the documents are those of the sample, which the build checks against the files, but for
	// the alt and the caption, which AddMedia does not give, and the time of the upload and the user.
	for _, id := range []string{jpegMedium, pngMedium, pdfMedium, logoMedium} {
		sample, file := sampleMedium(t, id)
		t.Run(sample.FileName, func(t *testing.T) {
			ed, s := newSample(t)
			newID := predictIDs(t, content.RefMedia)[0]
			res, err := ed.AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(file), sample.FileName, sample.MIMEType)
			if err != nil {
				t.Fatal(err)
			}
			want := sample
			want.ID, want.Alt, want.Caption, want.UploadedAt, want.UploadedBy = newID, nil, nil, testNowJST, userID
			doc := read(t, s, content.RefMedia, newID)
			if res.Kind != content.RefMedia || res.ID != newID || res.Version != doc.Version || len(res.Warnings) != 0 ||
				res.Number != 0 || res.Revision != "" {
				t.Errorf("AddMedia() = %+v, want the medium %s at %+v without warnings", res, newID, doc.Version)
			}
			// What MarshalJSON writes, the hash, the size, the format and the size in pixels of the sample's document.
			if text := mediaText(t, want); string(doc.Data) != text {
				t.Errorf("the document is\n%s\nwant\n%s", doc.Data, text)
			}
			if doc.Kind != content.RefMedia || doc.ID != newID || doc.SavedBy != userID {
				t.Errorf("the version is %+v", doc.Version)
			}
		})
	}

	// Files that the store does not have yet, in every format, with the sizes that the contents give.
	for _, tt := range []struct {
		name     string
		file     []byte
		fileName string
		mimeType string
		w, h     int
	}{
		{"a PNG", testPNG(t, 5, 4, 1), "upload.png", "image/png", 5, 4},
		{"a JPEG", testJPEG(t, 8, 6, 1), "upload.jpg", "image/jpeg", 8, 6},
		{"a GIF", testGIF(t, 7, 3, 1), "upload.gif", "image/gif", 7, 3},
		{"a PDF", testPDF(1), "upload.pdf", "application/pdf", 0, 0},
		// The size as it is displayed: the orientations 5 to 8 turn the image.
		{"a JPEG turned by its orientation", jpegWithOrientation(testJPEG(t, 8, 6, 2), 6), "turned.jpg", "image/jpeg", 6, 8},
		{"a JPEG with the orientation 3", jpegWithOrientation(testJPEG(t, 8, 6, 3), 3), "upside-down.jpg", "image/jpeg", 8, 6},
		// The contents decide: the name does not give the format.
		{"a PNG named as a PDF", testPNG(t, 5, 4, 2), "upload.pdf", "image/png", 5, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			newID := predictIDs(t, content.RefMedia)[0]
			hash := fileHash(tt.file)
			if hasOriginal(t, s, hash) {
				t.Fatal("the store has the file already")
			}
			res := must(t)(ed.AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(tt.file), tt.fileName, ""))
			want := wantMedium(newID, tt.fileName, tt.file, tt.mimeType, tt.w, tt.h)
			if got := read(t, s, content.RefMedia, newID); string(got.Data) != mediaText(t, want) || got.Version != res.Version {
				t.Errorf("the document is %s, want %s", got.Data, mediaText(t, want))
			}
			if got := readOriginal(t, s, hash); !bytes.Equal(got, tt.file) {
				t.Errorf("the original has %d bytes, want the file of %d", len(got), len(tt.file))
			}
			err := s.View(t.Context(), func(tx *store.Tx) error {
				sum, err := tx.Summary(content.RefMedia, newID)
				if err != nil || sum.Media == nil || sum.Media.Hash != hash || sum.Media.MIMEType != tt.mimeType {
					t.Errorf("Summary() = %+v, %v; want the medium of the file", sum, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestAddMediaErrors checks what AddMedia refuses: nothing is saved but the file, which is saved first, before the
// document, so that a file that is refused for what its document would be stays in the store.
func TestAddMediaErrors(t *testing.T) {
	good := testPNG(t, 5, 4, 3)
	user := Op{By: userID}
	tooLong := strings.Repeat("a", 252) + ".png"
	// located returns the error at key in the document that the operation would write for good, with the file name fileName.
	located := func(msg, fileName, key string) func(t *testing.T, name string, doc content.Media) string {
		return func(t *testing.T, name string, doc content.Media) string {
			doc.FileName = fileName
			return where(t, name, mediaText(t, doc), `"`+key+`"`) + msg
		}
	}
	plain := func(msg string) func(t *testing.T, name string, doc content.Media) string {
		return func(_ *testing.T, name string, _ content.Media) string {
			return strings.ReplaceAll(msg, "{name}", name)
		}
	}
	for _, tt := range []struct {
		name     string
		op       Op
		file     []byte
		fileName string
		saved    bool // whether the store has the file afterwards
		// want returns the error, for the name of the document and for the document that good would make.
		want   func(t *testing.T, name string, doc content.Media) string
		prefix bool // whether the error starts with want
	}{
		{"a text file", user, []byte("hello"), "hello.txt", true, plain("{name}: cannot read the file: unsupported file format"), false},
		{"a PNG that stops after its signature", user, good[:8], "a.png", true,
			plain("{name}: cannot read the file: invalid PNG: "), true},
		{"an empty file", user, nil, "a.png", false, plain("the original is empty"), false},
		{"a malformed user ID", Op{By: "usr_x"}, good, "a.png", false,
			plain(`invalid user ID "usr_x": use "usr_" and a ULID (26 characters in upper case)`), false},
		{"a malformed operation ID", Op{By: userID, ID: "op_x"}, good, "a.png", false,
			plain(`invalid operation ID "op_x": use "op_" and a ULID (26 characters in upper case)`), false},
		{"no file name", user, good, "", true, plain("{name}:1:1: /fileName: missing fileName"), false},
		{"a file name with a slash", user, good, "a/b.png", true,
			located("/fileName: character '/' is not allowed in file names", "a/b.png", "fileName"), false},
		{"a file name of 256 bytes", user, good, tooLong, true,
			located("/fileName: too long: 256 bytes (max 255)", tooLong, "fileName"), false},
		{"an image of 400 million pixels", user, pngHeader(20000, 20000), "a.png", true,
			plain("{name}:1:1: too many pixels: 20000x20000 (max 100000000)"), false},
		{"an image of 10001 by 10000 pixels", user, pngHeader(10001, 10000), "a.png", true,
			plain("{name}:1:1: too many pixels: 10001x10000 (max 100000000)"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			before := snapshot(t, s)
			id := predictIDs(t, content.RefMedia)[0]
			name := docName(content.RefMedia, id)
			res, err := ed.AddMedia(t.Context(), tt.op, bytes.NewReader(tt.file), tt.fileName, "")
			want := tt.want(t, name, wantMedium(id, tt.fileName, good, "image/png", 5, 4))
			if err == nil || (tt.prefix && !strings.HasPrefix(err.Error(), want)) || (!tt.prefix && err.Error() != want) {
				t.Errorf("AddMedia() = %+v, %v\nwant the error %q", res, err, want)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("AddMedia() has changed the documents:\n%s\nwant\n%s", got, before)
			}
			if got := hasOriginal(t, s, fileHash(tt.file)); got != tt.saved {
				t.Errorf("the store has the file = %t, want %t", got, tt.saved)
			}
		})
	}
}

// TestAddMediaUnpublishable checks that a file that the build cannot publish is not refused: the sizes are read from
// the header, and an image of 100 million pixels is the largest that a document accepts.
func TestAddMediaUnpublishable(t *testing.T) {
	for _, tt := range []struct {
		name string
		file []byte
		w, h int
	}{
		{"100 million pixels", pngHeader(10000, 10000), 10000, 10000},
		{"a side of more than 65535 pixels", pngHeader(70000, 100), 70000, 100},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			res, err := ed.AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(tt.file), "big.png", "")
			if err != nil || len(res.Warnings) != 0 {
				t.Fatalf("AddMedia() = %+v, %v", res, err)
			}
			want := mediaText(t, wantMedium(res.ID, "big.png", tt.file, "image/png", tt.w, tt.h))
			if got := read(t, s, content.RefMedia, res.ID); string(got.Data) != want {
				t.Errorf("the document is %s, want %s", got.Data, want)
			}
		})
	}
}

// TestAddMediaDeclaredType checks that the type that the upload says only gives a warning if it differs from the one that
// the contents give, which the document has.
func TestAddMediaDeclaredType(t *testing.T) {
	file := testPNG(t, 5, 4, 4)
	for _, tt := range []struct {
		name, declared string
		warns          bool
	}{
		{"the type of the contents", "image/png", false},
		{"no type", "", false},
		{"the type in upper case", "IMAGE/PNG", false},
		{"another image type", "image/jpeg", true},
		{"a type that is not supported", "application/octet-stream", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			id := predictIDs(t, content.RefMedia)[0]
			res, err := ed.AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(file), "a.png", tt.declared)
			if err != nil {
				t.Fatal(err)
			}
			doc := mediaText(t, wantMedium(id, "a.png", file, "image/png", 5, 4))
			if got := read(t, s, content.RefMedia, id); string(got.Data) != doc {
				t.Errorf("the document is %s, want %s", got.Data, doc)
			}
			var want []string
			if tt.warns {
				want = []string{where(t, docName(content.RefMedia, id), doc, `"mimeType"`) +
					"/mimeType: the file is image/png, not " + tt.declared + " as uploaded"}
			}
			if got := lines(res.Warnings); !reflect.DeepEqual(got, want) {
				t.Errorf("the warnings are %q, want %q", got, want)
			}
		})
	}
}

// TestAddMediaFileName checks that the file name is normalized as the document normalizes it: without control characters
// and surrounding spaces, in NFC.
func TestAddMediaFileName(t *testing.T) {
	ed, s := newSample(t)
	file := testPNG(t, 5, 4, 5)
	// "a" and a combining acute accent, which NFC writes as one character.
	given := "  a" + string(rune(0x301)) + ".png\t "
	want := string(rune(0xe1)) + ".png"
	res := must(t)(ed.AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(file), given, ""))
	doc := mediaText(t, wantMedium(res.ID, want, file, "image/png", 5, 4))
	if got := read(t, s, content.RefMedia, res.ID); string(got.Data) != doc {
		t.Errorf("the document is %s, want %s", got.Data, doc)
	}
}

// TestAddMediaOriginalFirst checks the order of 3.4: the file is saved before the document, so that a store that cannot
// take the document, as it has no settings, has the file and no document.
func TestAddMediaOriginalFirst(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	file := testPNG(t, 5, 4, 6)
	_, err = testEditor(t, s).AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(file), "a.png", "")
	if !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != "site/settings.json: not found; save the settings first" {
		t.Errorf("AddMedia() on a store without settings = %v", err)
	}
	if !hasOriginal(t, s, fileHash(file)) {
		t.Error("the file was not saved before the document")
	}
	err = s.View(t.Context(), func(tx *store.Tx) error {
		if sums, err := tx.Summaries(content.RefMedia); err != nil || len(sums) != 0 {
			t.Errorf("Summaries() = %+v, %v; want no medium", sums, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestProbe checks the read of an original: a file that is not of a supported format is a problem of the file, and a read
// of the store that fails is an error of the store.
func TestProbe(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := store.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ed := testEditor(t, s)
	put := func(file []byte) string {
		t.Helper()
		o, err := s.PutOriginal(t.Context(), bytes.NewReader(file))
		if err != nil {
			t.Fatal(err)
		}
		return o.Hash
	}

	info, bad, err := ed.probe(put(testPNG(t, 5, 4, 7)))
	if want := (mediafile.Info{MIMEType: mediafile.PNG, Width: 5, Height: 4, Orientation: 1}); info != want || bad != nil || err != nil {
		t.Errorf("probe() of a PNG = %+v, %v, %v; want %+v", info, bad, err, want)
	}
	if _, bad, err = ed.probe(put([]byte("hello"))); !errors.Is(bad, mediafile.ErrUnsupported) || err != nil {
		t.Errorf("probe() of a text = %v, %v; want the file's unsupported format", bad, err)
	}
	if _, bad, err = ed.probe("sha256:" + strings.Repeat("0", 64)); bad != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("probe() of an original that the store does not have = %v, %v; want an error of the store", bad, err)
	}

	// The data of an original that has another hash is an error of the store, found as the read ends.
	hash := put(testPNG(t, 5, 4, 8))
	digits := strings.TrimPrefix(hash, "sha256:")
	path := filepath.Join(dir, "files", digits[:2], digits)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, bad, err = ed.probe(hash); bad != nil || err == nil || !strings.Contains(err.Error(), "the data has the hash") {
		t.Errorf("probe() of a damaged original = %v, %v; want an error of the store about its hash", bad, err)
	}
}

// TestAddMediaParity adds an image that the EXIF orientation turns, and checks that the build, which checks the document of
// a medium against its file, builds a site that uses it, and publishes it under its file name.
func TestAddMediaParity(t *testing.T) {
	file := jpegWithOrientation(testJPEG(t, 8, 6, 9), 6)
	p := newParity(t)
	res := must(t)(p.ed.AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(file), "tall.jpg", "image/jpeg"))
	doc := read(t, p.s, content.RefMedia, res.ID)
	var m content.Media
	if err := m.UnmarshalJSON(doc.Data); err != nil || m.Width != 6 || m.Height != 8 {
		t.Fatalf("the medium is %+v, %v; want the size as displayed, 6x8", m, err)
	}

	p.write(docName(content.RefMedia, res.ID), string(doc.Data))
	writeOriginal(t, p.dir, m.Hash, file)
	const rev = "revisions/rev_01JD0000000000000000000005.json" // its featured image is jpegMedium
	p.write(rev, replaceOnce(t, string(sampleFile(t, rev)), jpegMedium, res.ID))
	public := buildPublic(t, p.dir)
	if _, err := os.Stat(filepath.Join(public, "media", res.ID, "tall.jpg")); err != nil {
		t.Errorf("the release does not have the file of the medium: %v", err)
	}
}

// TestSaveMedia saves the metadata of a medium: its alt, its caption and its file name.
func TestSaveMedia(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	name := docName(content.RefMedia, jpegMedium)
	orig := string(sampleFile(t, name))
	first := read(t, s, content.RefMedia, jpegMedium)
	if string(first.Data) != orig {
		t.Fatal("the store has other bytes than the sample file")
	}

	// A change of the alt, saved with the bytes that were given.
	changed := replaceOnce(t, orig, `"ja": "会社外観"`, `"ja": "本社の外観"`)
	res, err := ed.Save(t.Context(), op, content.RefMedia, jpegMedium, first.Seq, []byte(changed))
	if err != nil {
		t.Fatal(err)
	}
	cur := read(t, s, content.RefMedia, jpegMedium)
	if res.Kind != content.RefMedia || res.ID != jpegMedium || res.Version != cur.Version || cur.Seq <= first.Seq ||
		cur.SavedBy != userID || string(cur.Data) != changed || len(res.Warnings) != 0 {
		t.Errorf("Save() = %+v; the store has %+v with %q", res, cur.Version, cur.Data)
	}

	// The caption and the file name, together, with a file name that is normalized.
	more := replaceOnce(t, changed, `"本社の外観（2024年撮影）"`, `"本社の外観"`)
	more = replaceOnce(t, more, `"fileName": "会社外観.jpg"`, `"fileName": "  本社.jpg "`)
	res = must(t)(ed.Save(t.Context(), op, content.RefMedia, jpegMedium, cur.Seq, []byte(more)))
	var want content.Media
	if err := want.UnmarshalJSON([]byte(more)); err != nil {
		t.Fatal(err)
	}
	want.Normalize()
	if got := read(t, s, content.RefMedia, jpegMedium); want.FileName != "本社.jpg" || string(got.Data) != mediaText(t, want) ||
		got.Version != res.Version {
		t.Errorf("Save() stored %q, want %q", got.Data, mediaText(t, want))
	}

	// The same bytes save nothing.
	cur = read(t, s, content.RefMedia, jpegMedium)
	before := snapshot(t, s)
	res, err = ed.Save(t.Context(), op, content.RefMedia, jpegMedium, cur.Seq, cur.Data)
	if err != nil || res.Version != cur.Version || snapshot(t, s) != before {
		t.Errorf("Save() of the same bytes = %+v, %v; want the current version and no change", res, err)
	}
}

// TestMediaCost checks what the operations on media read of the index. None changes what the validation of other documents
// reads of a medium (its kind), so none validates what refers to it again: no referrer is read, though published revisions
// use the medium. Each runs the checks across the site as the other operations do, which cannot find a problem that a
// medium brings, as the checks across the site read of a medium only its kind, which these operations keep: the model of
// the site after the change and the model before it, which the sample's own warning (a redirect to a draft) makes, each
// read the summaries of the seven kinds.
func TestMediaCost(t *testing.T) {
	const models = 2 * 7
	op := Op{By: userID}
	orig := string(sampleFile(t, docName(content.RefMedia, jpegMedium)))
	alt := replaceOnce(t, orig, `"ja": "会社外観"`, `"ja": "本社の外観"`)
	file := testPNG(t, 5, 4, 23)
	own := "Summary media " + jpegMedium

	for _, tt := range []struct {
		name string
		call func(ed *Editor, s *store.Store) (Result, error)
		own  int // the reads of the summary of the medium jpegMedium: before the save, and after it to tell what it changed
	}{
		{"Save", func(ed *Editor, s *store.Store) (Result, error) {
			return ed.Save(t.Context(), op, content.RefMedia, jpegMedium, read(t, s, content.RefMedia, jpegMedium).Seq, []byte(alt))
		}, 2},
		{"AddMedia", func(ed *Editor, s *store.Store) (Result, error) {
			return ed.AddMedia(t.Context(), op, bytes.NewReader(file), "a.png", "")
		}, 0},
		{"ReplaceMedia", func(ed *Editor, s *store.Store) (Result, error) {
			return ed.ReplaceMedia(t.Context(), op, jpegMedium, read(t, s, content.RefMedia, jpegMedium).Seq, bytes.NewReader(file), "")
		}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			var calls []string
			ed.wrap = func(r reader) reader { return counting{r, &calls} }
			must(t)(tt.call(ed, s))
			if got := callsTo(calls, "Referrers"); len(got) != 0 {
				t.Errorf("%s() read the referrers: %q", tt.name, got)
			}
			if got := len(callsTo(calls, "Summaries")); got != models {
				t.Errorf("%s() read the summaries of a kind %d times, want %d: the model of the site after the change and before it", tt.name, got, models)
			}
			if got := count(callsTo(calls, "Summary"), own); got != tt.own {
				t.Errorf("%s() read the summary of the medium %d times, want %d", tt.name, got, tt.own)
			}
		})
	}

	// A deletion reads the references of the medium, once, to refuse it or to warn; there are none to validate again.
	t.Run("Delete", func(t *testing.T) {
		ed, _ := newSample(t)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(file), "a.png", ""))
		var calls []string
		ed.wrap = func(r reader) reader { return counting{r, &calls} }
		must(t)(ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq))
		if got := callsTo(calls, "Referrers"); !reflect.DeepEqual(got, []string{"Referrers media " + added.ID}) {
			t.Errorf("Delete() read the referrers %q, want those of the medium once", got)
		}
		if got := len(callsTo(calls, "Summaries")); got != models {
			t.Errorf("Delete() read the summaries of a kind %d times, want %d", got, models)
		}
	})

	// The same bytes save nothing, and read nothing of it.
	t.Run("Save of the same bytes", func(t *testing.T) {
		ed, s := newSample(t)
		var calls []string
		ed.wrap = func(r reader) reader { return counting{r, &calls} }
		must(t)(ed.Save(t.Context(), op, content.RefMedia, jpegMedium, read(t, s, content.RefMedia, jpegMedium).Seq, []byte(orig)))
		if len(callsTo(calls, "Referrers")) != 0 || len(callsTo(calls, "Summaries")) != 0 || count(callsTo(calls, "Summary"), own) != 0 {
			t.Errorf("Save() of the bytes that the store has read %q", calls)
		}
	})
}

// TestAddMediaTime checks the time of the upload: to the second, as the clock of the operation tells it in the time zone of
// the site (Asia/Tokyo here).
func TestAddMediaTime(t *testing.T) {
	ed, s := newSample(t)
	ed.now = func() time.Time { return testNow.Add(999 * time.Millisecond) }
	res := must(t)(ed.AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(testPNG(t, 5, 4, 25)), "a.png", ""))
	var m content.Media
	if err := m.UnmarshalJSON(read(t, s, content.RefMedia, res.ID).Data); err != nil || m.UploadedAt != testNowJST {
		t.Errorf("uploadedAt = %q, %v; want %q, the second of the clock", m.UploadedAt, err, testNowJST)
	}
}

// TestSaveMediaErrors checks what a save of a medium refuses: a change of what comes from the file and the upload, and what
// the document refuses.
func TestSaveMediaErrors(t *testing.T) {
	name := docName(content.RefMedia, jpegMedium)
	orig := string(sampleFile(t, name))
	sample, _ := sampleMedium(t, jpegMedium)
	fromFile := "set from the file; replace the file to change it"
	zeros := "sha256:" + strings.Repeat("0", 64)
	pdfName := docName(content.RefMedia, pdfMedium)
	pdf := string(sampleFile(t, pdfName))
	pdfWithAlt := replaceOnce(t, pdf, `"size": 676,`, "\"size\": 676,\n  \"alt\": { \"ja\": \"説明\" },")
	user := Op{By: userID}

	for _, tt := range []struct {
		key, old, new string
	}{
		{"hash", sample.Hash, zeros},
		{"mimeType", `"image/jpeg"`, `"image/png"`},
		{"size", `"size": 36153`, `"size": 36154`},
		{"width", `"width": 1200`, `"width": 1201`},
		{"height", `"height": 800`, `"height": 801`},
		{"uploadedAt", "2026-10-25T10:00:00+09:00", "2026-10-26T10:00:00+09:00"},
		{"uploadedBy", sample.UploadedBy, testID(content.RefUser, 2)},
	} {
		t.Run(tt.key, func(t *testing.T) {
			ed, s := newSample(t)
			before := snapshot(t, s)
			data := replaceOnce(t, orig, tt.old, tt.new)
			_, err := ed.Save(t.Context(), user, content.RefMedia, jpegMedium, read(t, s, content.RefMedia, jpegMedium).Seq, []byte(data))
			want := where(t, name, data, `"`+tt.key+`"`) + "/" + tt.key + ": " + fromFile
			if err == nil || err.Error() != want {
				t.Errorf("Save() = %v\nwant the error %q", err, want)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("Save() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}

	// A change that is allowed does not save with one that is not.
	t.Run("an alt and a hash", func(t *testing.T) {
		ed, s := newSample(t)
		before := snapshot(t, s)
		data := replaceOnce(t, replaceOnce(t, orig, `"ja": "会社外観"`, `"ja": "本社の外観"`), sample.Hash, zeros)
		_, err := ed.Save(t.Context(), user, content.RefMedia, jpegMedium, read(t, s, content.RefMedia, jpegMedium).Seq, []byte(data))
		want := where(t, name, data, `"hash"`) + "/hash: " + fromFile
		if err == nil || err.Error() != want || snapshot(t, s) != before {
			t.Errorf("Save() = %v\nwant the error %q and no change", err, want)
		}
	})

	t.Run("an alt of a PDF file", func(t *testing.T) {
		ed, s := newSample(t)
		before := snapshot(t, s)
		_, err := ed.Save(t.Context(), user, content.RefMedia, pdfMedium, read(t, s, content.RefMedia, pdfMedium).Seq, []byte(pdfWithAlt))
		want := where(t, pdfName, pdfWithAlt, `"alt"`) + "/alt: not allowed for application/pdf"
		if err == nil || err.Error() != want || snapshot(t, s) != before {
			t.Errorf("Save() = %v\nwant the error %q and no change", err, want)
		}
	})

	t.Run("a file name with a slash", func(t *testing.T) {
		ed, s := newSample(t)
		data := replaceOnce(t, orig, `"fileName": "会社外観.jpg"`, `"fileName": "a/b.jpg"`)
		_, err := ed.Save(t.Context(), user, content.RefMedia, jpegMedium, read(t, s, content.RefMedia, jpegMedium).Seq, []byte(data))
		want := where(t, name, data, `"fileName"`) + "/fileName: character '/' is not allowed in file names"
		if err == nil || err.Error() != want {
			t.Errorf("Save() = %v\nwant the error %q", err, want)
		}
	})

	t.Run("an old version", func(t *testing.T) {
		ed, s := newSample(t)
		seq := read(t, s, content.RefMedia, jpegMedium).Seq
		must(t)(ed.Save(t.Context(), user, content.RefMedia, jpegMedium, seq, []byte(replaceOnce(t, orig, `"ja": "会社外観"`, `"ja": "本社の外観"`))))
		cur := read(t, s, content.RefMedia, jpegMedium).Seq
		_, err := ed.Save(t.Context(), user, content.RefMedia, jpegMedium, seq, []byte(orig))
		want := fmt.Sprintf("%s: changed since it was read: it is at version %d, not %d", name, cur, seq)
		if !errors.Is(err, ErrConflict) || err == nil || err.Error() != want {
			t.Errorf("Save() with an old base = %v, want the error %q that wraps ErrConflict", err, want)
		}
	})

	t.Run("a medium that does not exist", func(t *testing.T) {
		ed, _ := newSample(t)
		missing := testID(content.RefMedia, 9)
		_, err := ed.Save(t.Context(), user, content.RefMedia, missing, 1, []byte(orig))
		if !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != "media/"+missing+".json: not found" {
			t.Errorf("Save() of a medium that does not exist = %v", err)
		}
	})
}

// TestMediaDocReplaces checks what a document that a save has decoded is compared with: the version that it replaces, if
// that decodes. A version that this program cannot read has nothing that the document changes.
func TestMediaDocReplaces(t *testing.T) {
	ed, s := newSample(t)
	sample, _ := sampleMedium(t, jpegMedium)
	changed := sample
	changed.Hash, changed.Width = "sha256:"+strings.Repeat("0", 64), 1201
	for _, tt := range []struct {
		name string
		cur  []byte
		want []string
	}{
		{"a version that decodes", sampleFile(t, docName(content.RefMedia, jpegMedium)), []string{
			"/hash: set from the file; replace the file to change it",
			"/width: set from the file; replace the file to change it",
		}},
		{"a version that does not decode", []byte("{"), nil},
		{"no version", nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.View(t.Context(), func(tx *store.Tx) error {
				r := newRun(ed, t.Context(), tx, "")
				site, _, err := r.site()
				if err != nil {
					return err
				}
				d := &mediaDoc{m: changed}
				d.replaces(tt.cur)
				var es content.Errors
				if err := d.validate(site, r.lookup); err != nil && !errors.As(err, &es) {
					return err
				}
				if got := lines(es); !reflect.DeepEqual(got, tt.want) {
					t.Errorf("validate() = %q, want %q", got, tt.want)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestReplaceMedia replaces the file of a medium by that of another format, which the document follows, as the sample's
// build checks them against each other, and keeps the name that the published file has before its extension.
func TestReplaceMedia(t *testing.T) {
	p := newParity(t)
	var calls []string
	p.ed.wrap = func(r reader) reader { return counting{r, &calls} }
	op := Op{By: userID}
	name := docName(content.RefMedia, jpegMedium)
	was := read(t, p.s, content.RefMedia, jpegMedium)
	sample, _ := sampleMedium(t, jpegMedium)
	file := testPNG(t, 64, 48, 10)
	hash := fileHash(file)
	if hasOriginal(t, p.s, hash) {
		t.Fatal("the store has the file already")
	}

	res, err := p.ed.ReplaceMedia(t.Context(), op, jpegMedium, was.Seq, bytes.NewReader(file), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	// The file name, the alt and the caption stay; the rest is of the file and of the upload.
	want := sample
	want.Hash, want.Size, want.MIMEType, want.Width, want.Height = hash, len(file), "image/png", 64, 48
	want.UploadedAt, want.UploadedBy = testNowJST, userID
	cur := read(t, p.s, content.RefMedia, jpegMedium)
	if res.Kind != content.RefMedia || res.ID != jpegMedium || res.Version != cur.Version || cur.Seq <= was.Seq ||
		cur.SavedBy != userID || res.Revision != "" || res.Number != 0 || len(res.Warnings) != 0 {
		t.Errorf("ReplaceMedia() = %+v; the store has %+v", res, cur.Version)
	}
	if text := mediaText(t, want); string(cur.Data) != text {
		t.Errorf("the document is\n%s\nwant\n%s", cur.Data, text)
	}
	if !bytes.Equal(readOriginal(t, p.s, hash), file) {
		t.Error("the original of the new file is not the file")
	}
	// The original that it replaces stays, as the releases that use it need it (6.6.2).
	if _, old := sampleMedium(t, jpegMedium); !bytes.Equal(readOriginal(t, p.s, sample.Hash), old) {
		t.Error("the original that was replaced is not as it was")
	}
	err = p.s.View(t.Context(), func(tx *store.Tx) error {
		sum, err := tx.Summary(content.RefMedia, jpegMedium)
		if err != nil || sum.Media == nil || sum.Media.Hash != hash || sum.Media.MIMEType != "image/png" {
			t.Errorf("Summary() = %+v, %v; want the medium of the new file", sum, err)
		}
		h, err := tx.History(content.RefMedia, jpegMedium)
		if err != nil || len(h) != 2 || h[1] != res.Version {
			t.Errorf("History() = %+v, %v; want the version before and the new one", h, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Published revisions use the medium, and its kind has not changed: nothing that refers to it is validated again. The
	// summary of the medium is read before the save and after it, to tell that.
	if got := callsTo(calls, "Referrers"); len(got) != 0 {
		t.Errorf("ReplaceMedia() read the referrers: %q", got)
	}
	if got := count(callsTo(calls, "Summary"), "Summary media "+jpegMedium); got != 2 {
		t.Errorf("ReplaceMedia() read the summary of the medium %d times, want 2", got)
	}

	// The build checks the document against the file, builds the pages that use it, and names the published file as before
	// but for the extension, which the format gives.
	p.write(name, string(cur.Data))
	writeOriginal(t, p.dir, hash, file)
	public := buildPublic(t, p.dir)
	if _, err := os.Stat(filepath.Join(public, "media", jpegMedium, "会社外観.png")); err != nil {
		t.Errorf("the release does not have the file with the new extension: %v", err)
	}
	if _, err := os.Stat(filepath.Join(public, "media", jpegMedium, "会社外観.jpg")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the release has the file with the old extension: %v", err)
	}
}

// TestReplaceMediaKinds replaces the file of an image by an image of the same format, and that of a PDF file by a PDF
// file, which are allowed, and by a file of the other kind, which is refused.
func TestReplaceMediaKinds(t *testing.T) {
	op := Op{By: userID}
	t.Run("an image by an image of the same format", func(t *testing.T) {
		ed, s := newSample(t)
		file := testJPEG(t, 30, 20, 1)
		was := read(t, s, content.RefMedia, jpegMedium)
		res := must(t)(ed.ReplaceMedia(t.Context(), op, jpegMedium, was.Seq, bytes.NewReader(file), ""))
		sample, _ := sampleMedium(t, jpegMedium)
		want := sample
		want.Hash, want.Size, want.Width, want.Height, want.UploadedAt = fileHash(file), len(file), 30, 20, testNowJST
		if got := read(t, s, content.RefMedia, jpegMedium); string(got.Data) != mediaText(t, want) || got.Version != res.Version {
			t.Errorf("the document is %s, want %s", got.Data, mediaText(t, want))
		}
	})

	t.Run("a PDF file by a PDF file", func(t *testing.T) {
		ed, s := newSample(t)
		file := testPDF(2)
		was := read(t, s, content.RefMedia, pdfMedium)
		must(t)(ed.ReplaceMedia(t.Context(), op, pdfMedium, was.Seq, bytes.NewReader(file), "application/pdf"))
		sample, _ := sampleMedium(t, pdfMedium)
		want := sample
		want.Hash, want.Size, want.UploadedAt = fileHash(file), len(file), testNowJST
		if got := read(t, s, content.RefMedia, pdfMedium); string(got.Data) != mediaText(t, want) {
			t.Errorf("the document is %s, want %s", got.Data, mediaText(t, want))
		}
	})

	t.Run("the same file", func(t *testing.T) {
		ed, s := newSample(t)
		sample, file := sampleMedium(t, pngMedium)
		was := read(t, s, content.RefMedia, pngMedium)
		must(t)(ed.ReplaceMedia(t.Context(), op, pngMedium, was.Seq, bytes.NewReader(file), "image/png"))
		want := sample
		want.UploadedAt = testNowJST
		if got := read(t, s, content.RefMedia, pngMedium); string(got.Data) != mediaText(t, want) || got.Seq <= was.Seq {
			t.Errorf("the document is %s, want %s", got.Data, mediaText(t, want))
		}
	})

	for _, tt := range []struct {
		name string
		id   string
		file []byte
		want string
	}{
		{"an image by a PDF file", jpegMedium, testPDF(3), "the file is application/pdf; a medium cannot change between an image and a PDF"},
		{"a PDF file by an image", pdfMedium, testPNG(t, 5, 4, 11), "the file is image/png; a medium cannot change between an image and a PDF"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			before := snapshot(t, s)
			doc := read(t, s, content.RefMedia, tt.id)
			name := docName(content.RefMedia, tt.id)
			_, err := ed.ReplaceMedia(t.Context(), op, tt.id, doc.Seq, bytes.NewReader(tt.file), "")
			// Located in the document that the file would replace, which has the kind.
			want := where(t, name, string(doc.Data), `"mimeType"`) + "/mimeType: " + tt.want
			if err == nil || err.Error() != want {
				t.Errorf("ReplaceMedia() = %v\nwant the error %q", err, want)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("ReplaceMedia() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}
}

// TestReplaceMediaSameBytes checks that a replacement that makes the document that the store has saves nothing, as a save of
// the same bytes does, and reads nothing more: here the same file, by the same user, at the same time.
func TestReplaceMediaSameBytes(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	file := testPNG(t, 5, 4, 22)
	added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(file), "a.png", ""))
	cur := read(t, s, content.RefMedia, added.ID)
	before := snapshot(t, s)
	var calls []string
	ed.wrap = func(r reader) reader { return counting{r, &calls} }
	res, err := ed.ReplaceMedia(t.Context(), op, added.ID, cur.Seq, bytes.NewReader(file), "")
	if err != nil || res.Version != cur.Version || snapshot(t, s) != before {
		t.Errorf("ReplaceMedia() of the same file = %+v, %v; want the current version %+v and no change", res, err, cur.Version)
	}
	for _, method := range []string{"Referrers", "Summaries"} {
		if got := callsTo(calls, method); len(got) != 0 {
			t.Errorf("ReplaceMedia() of the same file read %q", got)
		}
	}
	if got := count(callsTo(calls, "Summary"), "Summary media "+added.ID); got != 0 {
		t.Errorf("ReplaceMedia() of the same file read the summary of the medium %d times, want 0", got)
	}
}

// TestReplaceMediaErrors checks the other errors of ReplaceMedia: nothing is saved but the file, which is saved first.
func TestReplaceMediaErrors(t *testing.T) {
	op := Op{By: userID}
	name := docName(content.RefMedia, pngMedium)
	good := testPNG(t, 9, 7, 12)
	missing := testID(content.RefMedia, 9)
	for _, tt := range []struct {
		name  string
		op    Op
		id    string
		base  func(seq int64) int64
		file  []byte
		saved bool // whether the store has the file afterwards
		want  func(seq int64) string
		is    error
	}{
		{"an old version", op, pngMedium, func(seq int64) int64 { return seq + 1 }, good, true, func(seq int64) string {
			return fmt.Sprintf("%s: changed since it was read: it is at version %d, not %d", name, seq, seq+1)
		}, ErrConflict},
		{"a medium that does not exist", op, missing, func(int64) int64 { return 1 }, good, true, func(int64) string {
			return "media/" + missing + ".json: not found"
		}, fs.ErrNotExist},
		{"a malformed ID", op, "med_x", func(seq int64) int64 { return seq }, good, false, func(int64) string {
			return `invalid media ID "med_x": use "med_" and a ULID (26 characters in upper case)`
		}, nil},
		{"a malformed user ID", Op{By: "x"}, pngMedium, func(seq int64) int64 { return seq }, good, false, func(int64) string {
			return `invalid user ID "x": use "usr_" and a ULID (26 characters in upper case)`
		}, nil},
		{"a text file", op, pngMedium, func(seq int64) int64 { return seq }, []byte("hello"), true, func(int64) string {
			return name + ": cannot read the file: unsupported file format"
		}, nil},
		{"an empty file", op, pngMedium, func(seq int64) int64 { return seq }, nil, false, func(int64) string {
			return "the original is empty"
		}, nil},
		{"an image of more than 100 million pixels", op, pngMedium, func(seq int64) int64 { return seq }, pngHeader(10001, 10000), true,
			func(int64) string { return name + ":1:1: too many pixels: 10001x10000 (max 100000000)" }, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			seq := read(t, s, content.RefMedia, pngMedium).Seq
			before := snapshot(t, s)
			_, err := ed.ReplaceMedia(t.Context(), tt.op, tt.id, tt.base(seq), bytes.NewReader(tt.file), "")
			if want := tt.want(seq); err == nil || err.Error() != want || tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("ReplaceMedia() error = %v, want %q wrapping %v", err, want, tt.is)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("ReplaceMedia() has changed the store:\n%s\nwant\n%s", got, before)
			}
			if got := hasOriginal(t, s, fileHash(tt.file)); got != tt.saved {
				t.Errorf("the store has the file = %t, want %t", got, tt.saved)
			}
		})
	}
}

// TestReplaceMediaDeclaredType checks the warning for a file that is not of the declared type: the file is saved.
func TestReplaceMediaDeclaredType(t *testing.T) {
	ed, s := newSample(t)
	file := testPNG(t, 5, 4, 13)
	was := read(t, s, content.RefMedia, jpegMedium)
	res := must(t)(ed.ReplaceMedia(t.Context(), Op{By: userID}, jpegMedium, was.Seq, bytes.NewReader(file), "image/jpeg"))
	got := read(t, s, content.RefMedia, jpegMedium)
	want := []string{where(t, docName(content.RefMedia, jpegMedium), string(got.Data), `"mimeType"`) +
		"/mimeType: the file is image/png, not image/jpeg as uploaded"}
	if lines := lines(res.Warnings); !reflect.DeepEqual(lines, want) || got.Version != res.Version || got.Seq <= was.Seq {
		t.Errorf("ReplaceMedia() warnings = %q, want %q", lines, want)
	}
}

// TestDeleteMedia deletes media that nothing uses, and refuses those that published content uses: the logo of the site,
// the images and the files of published revisions, the avatar of an author and the file that a redirect goes to.
func TestDeleteMedia(t *testing.T) {
	op := Op{By: userID}
	for _, id := range []string{logoMedium, jpegMedium, pngMedium, pdfMedium} {
		name := docName(content.RefMedia, id)
		t.Run(name, func(t *testing.T) {
			ed, s := newSample(t)
			files := usedBy(t, name, id)
			if len(files) == 0 {
				t.Fatal("the sample site does not use the medium")
			}
			before := snapshot(t, s)
			_, err := ed.Delete(t.Context(), op, content.RefMedia, id, read(t, s, content.RefMedia, id).Seq)
			if want := refusal(name, files); err == nil || err.Error() != want || snapshot(t, s) != before {
				t.Errorf("Delete() = %v\nwant the error %q and no change", err, want)
			}
		})
	}

	// A medium that nothing uses is deleted, and its original stays, as the store has no way to remove it.
	t.Run("a medium that nothing uses", func(t *testing.T) {
		ed, s := newSample(t)
		file := testPNG(t, 6, 5, 14)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(file), "unused.png", ""))
		res, err := ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != content.RefMedia || res.ID != added.ID || res.Version.Hash != "" || res.Version.Seq <= added.Version.Seq ||
			res.Version.SavedBy != userID || len(res.Warnings) != 0 {
			t.Errorf("Delete() = %+v, want the version that marks the deletion", res)
		}
		err = s.View(t.Context(), func(tx *store.Tx) error {
			if d, err := tx.Document(content.RefMedia, added.ID); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Document() after Delete() = %+v, %v; want fs.ErrNotExist", d, err)
			}
			if h, err := tx.History(content.RefMedia, added.ID); err != nil || len(h) != 2 || h[1] != res.Version {
				t.Errorf("History() = %+v, %v; want the deletion as the last version", h, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := readOriginal(t, s, fileHash(file)); !bytes.Equal(got, file) {
			t.Error("the original is not as it was")
		}
	})

	// A medium that only the last revision of an entry that is not published uses is deleted, with a warning.
	t.Run("a medium that a draft uses", func(t *testing.T) {
		ed, s := newSample(t)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 6, 5, 15)), "draft.png", ""))
		entry := newDraft(t, ed, content.TypePost, "ja")
		rev := must(t)(ed.SaveRevision(t.Context(), op, entry, "",
			[]byte(`{"schemaVersion":1,"title":"Draft","slug":"draft","featuredImage":"`+added.ID+`"}`)))
		res, err := ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{docName(content.RefMedia, added.ID) + ": deleted although used by " + revisionName(rev.Revision) +
			", the latest revision of " + docName(content.RefEntry, entry) + ", which is not published"}
		if got := lines(res.Warnings); !reflect.DeepEqual(got, want) {
			t.Errorf("Delete() warnings = %q, want %q", got, want)
		}
		if revs := revisionIDs(t, s, entry); len(revs) != 1 || revs[0] != rev.Revision {
			t.Errorf("the entry has the revisions %v, want %s, which stays", revs, rev.Revision)
		}
	})

	t.Run("an old version", func(t *testing.T) {
		ed, s := newSample(t)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 6, 5, 16)), "old.png", ""))
		before := snapshot(t, s)
		_, err := ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq+1)
		if !errors.Is(err, ErrConflict) || snapshot(t, s) != before {
			t.Errorf("Delete() with a base that is not the current version = %v, want ErrConflict and no change", err)
		}
	})
}

// TestMediaOperationID checks the operation IDs of the operations on files: a retry returns the first result and saves
// no second document, also after the document has changed; another file, name or type with the ID fails; one that failed
// has recorded nothing.
func TestMediaOperationID(t *testing.T) {
	ed, s := newSample(t)
	file := testPNG(t, 5, 4, 17)
	op := Op{By: userID, ID: testOpID(11)}
	first := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(file), "a.png", ""))
	after := snapshot(t, s)
	again, err := ed.AddMedia(t.Context(), op, bytes.NewReader(file), "a.png", "")
	if err != nil || !reflect.DeepEqual(first, again) || snapshot(t, s) != after {
		t.Errorf("a retry of AddMedia() = %+v, %v; want the first result %+v and no change", again, err, first)
	}
	wantErr := fmt.Sprintf("operation ID %q was used for another request", op.ID)
	for name, call := range map[string]func() (Result, error){
		"another file": func() (Result, error) {
			return ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 5, 4, 18)), "a.png", "")
		},
		"another name": func() (Result, error) { return ed.AddMedia(t.Context(), op, bytes.NewReader(file), "b.png", "") },
		"another type": func() (Result, error) {
			return ed.AddMedia(t.Context(), op, bytes.NewReader(file), "a.png", "image/png")
		},
		"another operation": func() (Result, error) {
			return ed.ReplaceMedia(t.Context(), op, first.ID, first.Version.Seq, bytes.NewReader(file), "")
		},
	} {
		if res, err := call(); err == nil || err.Error() != wantErr {
			t.Errorf("%s with the same operation ID = %+v, %v; want the error %q", name, res, err, wantErr)
		}
	}
	if got := snapshot(t, s); got != after {
		t.Errorf("the refused operations have changed the store:\n%s\nwant\n%s", got, after)
	}

	// The same for the replacement, whose base is not the current version at the retry, as it was changed by the first try.
	replaceOp := Op{By: userID, ID: testOpID(12)}
	other := testPNG(t, 7, 6, 19)
	firstReplace := must(t)(ed.ReplaceMedia(t.Context(), replaceOp, first.ID, first.Version.Seq, bytes.NewReader(other), ""))
	after = snapshot(t, s)
	again, err = ed.ReplaceMedia(t.Context(), replaceOp, first.ID, first.Version.Seq, bytes.NewReader(other), "")
	if err != nil || !reflect.DeepEqual(firstReplace, again) || snapshot(t, s) != after {
		t.Errorf("a retry of ReplaceMedia() = %+v, %v; want the first result %+v and no change", again, err, firstReplace)
	}
	wantErr = fmt.Sprintf("operation ID %q was used for another request", replaceOp.ID)
	seq := first.Version.Seq
	for name, call := range map[string]func() (Result, error){
		"another file": func() (Result, error) {
			return ed.ReplaceMedia(t.Context(), replaceOp, first.ID, seq, bytes.NewReader(testPNG(t, 7, 6, 24)), "")
		},
		"another type": func() (Result, error) {
			return ed.ReplaceMedia(t.Context(), replaceOp, first.ID, seq, bytes.NewReader(other), "image/png")
		},
		"another base": func() (Result, error) {
			return ed.ReplaceMedia(t.Context(), replaceOp, first.ID, seq+1, bytes.NewReader(other), "")
		},
		"another medium": func() (Result, error) {
			return ed.ReplaceMedia(t.Context(), replaceOp, pngMedium, seq, bytes.NewReader(other), "")
		},
	} {
		if res, err := call(); err == nil || err.Error() != wantErr {
			t.Errorf("%s with the same operation ID = %+v, %v; want the error %q", name, res, err, wantErr)
		}
	}
	if got := snapshot(t, s); got != after {
		t.Errorf("the refused operations have changed the store:\n%s\nwant\n%s", got, after)
	}

	// A failed operation has recorded nothing: its retry runs, and the ID is free.
	failed := Op{By: userID, ID: testOpID(13)}
	third := testPNG(t, 5, 4, 20)
	var problems content.Errors
	for range 2 {
		if _, err := ed.AddMedia(t.Context(), failed, bytes.NewReader(third), "a/b.png", ""); !errors.As(err, &problems) {
			t.Errorf("AddMedia() with a file name that is not valid = %v, want its problems", err)
		}
	}
	if snapshot(t, s) != after {
		t.Error("an operation that failed has changed the store")
	}
	if ok, err := ed.AddMedia(t.Context(), failed, bytes.NewReader(third), "c.png", ""); err != nil || ok.ID == first.ID {
		t.Errorf("AddMedia() with the ID of an operation that failed = %+v, %v; want a new medium", ok, err)
	}
}

// TestAddMediaConcurrent checks that an Editor adds files from several goroutines: every operation succeeds and makes a
// medium of its own, also of one file, which is saved once.
func TestAddMediaConcurrent(t *testing.T) {
	ed, s := newSample(t)
	file := testPNG(t, 5, 4, 21)
	const goroutines = 8
	ids := make([]string, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := ed.AddMedia(t.Context(), Op{By: userID}, bytes.NewReader(file), fmt.Sprintf("c%d.png", g), "")
			if err != nil {
				t.Error(err)
			}
			ids[g] = res.ID
		}()
	}
	wg.Wait()
	seen := make(map[string]bool)
	for _, id := range ids {
		seen[id] = true
	}
	var n int
	err := s.View(t.Context(), func(tx *store.Tx) error {
		sums, err := tx.Summaries(content.RefMedia)
		n = len(sums)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != goroutines || n != 6+goroutines || !bytes.Equal(readOriginal(t, s, fileHash(file)), file) {
		t.Errorf("%d IDs and %d media in the store, want %d and %d", len(seen), n, goroutines, 6+goroutines)
	}
}
