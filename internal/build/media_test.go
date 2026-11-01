package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/imaging"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
)

var _ render.Resolver = (*resolver)(nil)

// IDs of media that the tests add to the fixture.
var (
	animationID = newID("med", 1)
	iccID       = newID("med", 2)
	unusedID    = newID("med", 3)
)

const missingMediaID = "med_01JCZZZZZZZZZZZZZZZZZZZZZZ"

// testPDF is the original of a small PDF.
const testPDF = "%PDF-1.7\n" +
	"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
	"2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n" +
	"trailer\n<< /Root 1 0 R >>\n%%EOF\n"

// gradient returns an image of w x h pixels whose colors change across it.
func gradient(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.SetRGBA(x, y, color.RGBA{uint8(x * 255 / max(w-1, 1)), uint8(y * 255 / max(h-1, 1)), 128, 255})
		}
	}
	return m
}

// encodeImage returns what encode writes.
func encodeImage(t testing.TB, encode func(w io.Writer) error) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := encode(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// testJPEG returns a JPEG file of a gradient of w x h pixels.
func testJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	return encodeImage(t, func(b io.Writer) error { return jpeg.Encode(b, gradient(w, h), nil) })
}

// testPNG returns a PNG file of a gradient of w x h pixels.
func testPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	return encodeImage(t, func(b io.Writer) error { return png.Encode(b, gradient(w, h)) })
}

// testGIF returns a GIF animation of two frames of 40 x 30 pixels.
func testGIF(t testing.TB) []byte {
	t.Helper()
	palette := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}}
	frames := make([]*image.Paletted, 2)
	for i := range frames {
		frames[i] = image.NewPaletted(image.Rect(0, 0, 40, 30), palette)
		for k := range frames[i].Pix {
			frames[i].Pix[k] = uint8((k/40 + i) % 2)
		}
	}
	return encodeImage(t, func(w io.Writer) error {
		return gif.EncodeAll(w, &gif.GIF{Image: frames, Delay: []int{10, 10}})
	})
}

// withInvalidICC returns file, a PNG file, with an iCCP chunk after IHDR whose profile is not zlib data.
func withInvalidICC(file []byte) []byte {
	data := []byte("test\x00\x00not zlib")
	chunk := append(binary.BigEndian.AppendUint32(nil, uint32(len(data))), "iCCP"...)
	chunk = append(chunk, data...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	const ihdrEnd = 8 + 25 // the signature, and IHDR with its length, type and CRC
	return slices.Concat(file[:ihdrEnd], chunk, file[ihdrEnd:])
}

// mediaHash returns the Media.Hash of data.
func mediaHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// putFile stores data as an original, and returns its hash.
func (s *memSource) putFile(data []byte) string {
	hash := mediaHash(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[strings.TrimPrefix(hash, "sha256:")] = &fstest.MapFile{Data: data}
	return hash
}

// putOriginal stores data as an original, and replaces the document of the medium id with that of data uploaded as
// fileName: with the hash, the size, the MIME type and the size as displayed of data. It returns the document.
func (s *memSource) putOriginal(t testing.TB, id, fileName string, data []byte) content.Media {
	t.Helper()
	info, err := mediafile.Probe(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	m := content.Media{
		ID:         id,
		Hash:       s.putFile(data),
		FileName:   fileName,
		MIMEType:   info.MIMEType,
		Size:       len(data),
		Width:      info.Width,
		Height:     info.Height,
		UploadedAt: "2026-10-25T10:00:00+09:00",
		UploadedBy: userID,
	}
	s.put(t, content.RefMedia, id, m)
	return m
}

// openCount returns the number of calls of OpenMedia with hash.
func (s *memSource) openCount(hash string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened[hash]
}

func newTestImages(t testing.TB, dir string) *imaging.Images {
	t.Helper()
	im, err := imaging.New(imaging.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return im
}

// newTestResolver sets b.res to a resolver with a cache in a new directory, which reads two originals at once at most,
// and returns it.
func newTestResolver(t testing.TB, b *builder) *resolver {
	t.Helper()
	b.res = newResolver(b, newTestImages(t, t.TempDir()), 2)
	return b.res
}

// newPublic returns a new directory for the public files, and its root.
func newPublic(t testing.TB) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return dir, root
}

// listFiles returns the names of the files in dir, with "/", sorted.
func listFiles(t testing.TB, dir string) []string {
	t.Helper()
	var files []string
	err := fs.WalkDir(os.DirFS(dir), ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

// mustPublish publishes the media of b into a new directory, which it returns, failing on errors.
func mustPublish(t *testing.T, b *builder) string {
	t.Helper()
	dir, pub := newPublic(t)
	var p problems
	if err := b.publishMedia(t.Context(), pub, 3, &p); err != nil {
		t.Fatalf("publishMedia() = %v", err)
	}
	if p.len() > 0 {
		t.Fatalf("publishMedia() errors:\n%v", p.sorted())
	}
	return dir
}

// checkMediaOutputs checks that the outputs of b in /media/ are the files in dir, in the same order, from the documents
// of their media.
func checkMediaOutputs(t *testing.T, b *builder, dir string) {
	t.Helper()
	var files []string
	for _, o := range b.outputs {
		if !strings.HasPrefix(o.path, "/media/") {
			continue
		}
		doc := fileOf(content.RefMedia, strings.Split(o.file, "/")[1])
		if o.path != "/"+o.file || o.rank != rankDocument || o.src.file.Name != doc || o.src.pointer != "/fileName" ||
			o.src.what != doc {
			t.Errorf("output %q of the file %q, rank %d, from %s#%s (%s)",
				o.path, o.file, o.rank, o.src.file.Name, o.src.pointer, o.src.what)
		}
		files = append(files, o.file)
	}
	if got := listFiles(t, dir); !slices.Equal(files, got) {
		t.Errorf("outputs of media:\n%s\nfiles:\n%s", strings.Join(files, "\n"), strings.Join(got, "\n"))
	}
}

// TestResolver checks what the resolver tells about media, entries and forms, and that publishMedia writes the files of
// the media that it was asked about, with the published file of each image, as imaging makes them, and no others.
func TestResolver(t *testing.T) {
	src := testSource(t)
	photo := src.putOriginal(t, imageID, "会社外観.JPG", testJPEG(t, 1200, 800))
	guide := src.putOriginal(t, pdfID, "会社案内（2026年版）.PDF", []byte(testPDF))
	favicon := src.putOriginal(t, iconID, "icon.png", testPNG(t, 64, 64))
	anim := src.putOriginal(t, animationID, "loading.gif", testGIF(t))
	scan := src.putOriginal(t, iccID, "scan.png", withInvalidICC(testPNG(t, 48, 32)))
	src.putOriginal(t, unusedID, "unused.png", testPNG(t, 16, 16))
	b := mustLoad(t, src)
	r := newTestResolver(t, b)

	jpegFile := func(w, h int) imaging.Variant { return imaging.Variant{Width: w, Height: h, Format: mediafile.JPEG} }
	pngFile := func(w, h int) imaging.Variant { return imaging.Variant{Width: w, Height: h, Format: mediafile.PNG} }
	animation := imaging.Variant{Width: 40, Height: 30, Format: mediafile.GIF, Animated: true}
	// at returns the path of the file name of m, whose variant is v, with its version.
	at := func(m content.Media, name string, v imaging.Variant) string {
		return "/media/" + m.ID + "/" + name + "?v=" + imaging.Version(m.Hash, v)
	}

	for _, tt := range []struct {
		id   string
		want string
	}{
		{imageID, at(photo, "会社外観.jpg", jpegFile(1200, 800))},
		{pdfID, "/media/" + pdfID + "/会社案内-2026年版.pdf?v=" + imaging.PDFVersion(guide.Hash)},
		{animationID, at(anim, "loading.gif", animation)},
	} {
		if m, path, ok := r.Media(tt.id); m != &b.media[tt.id].v || path != tt.want || !ok {
			t.Errorf("Media(%s) = %v, %q, %v, want its document, %q, true", tt.id, m, path, ok, tt.want)
		}
	}
	if m, path, ok := r.Media(missingMediaID); m != nil || path != "" || ok {
		t.Errorf("Media(%s) = %v, %q, %v, want nil, \"\", false", missingMediaID, m, path, ok)
	}

	for _, tt := range []struct {
		name string
		id   string
		size *content.ImageSize
		want render.ImageFiles
	}{
		{
			name: "default size",
			id:   imageID,
			want: render.ImageFiles{
				Src:   at(photo, "会社外観.jpg", jpegFile(1200, 800)),
				Width: 1200, Height: 800,
				Srcset: []render.ImageFile{
					{Path: at(photo, "会社外観-640x427.jpg", jpegFile(640, 427)), Width: 640},
					{Path: at(photo, "会社外観-960x640.jpg", jpegFile(960, 640)), Width: 960},
					{Path: at(photo, "会社外観.jpg", jpegFile(1200, 800)), Width: 1200},
				},
			},
		},
		{
			name: "cropped size",
			id:   imageID,
			size: &content.ImageSize{Name: "thumbnail", Width: 150, Height: 150, Crop: true},
			want: render.ImageFiles{
				Src:   at(photo, "会社外観-150x150.jpg", jpegFile(150, 150)),
				Width: 150, Height: 150,
				Srcset: []render.ImageFile{
					{Path: at(photo, "会社外観-150x150.jpg", jpegFile(150, 150)), Width: 150},
					{Path: at(photo, "会社外観-300x300.jpg", jpegFile(300, 300)), Width: 300},
				},
			},
		},
		{
			name: "animation",
			id:   animationID,
			want: render.ImageFiles{Src: at(anim, "loading.gif", animation), Width: 40, Height: 30},
		},
		{
			name: "animation at a size",
			id:   animationID,
			size: &content.ImageSize{Name: "small", Width: 20},
			want: render.ImageFiles{Src: at(anim, "loading.gif", animation), Width: 20, Height: 15},
		},
		{
			name: "only sizes",
			id:   iccID,
			size: &content.ImageSize{Name: "tiny", Width: 10, Height: 10, Crop: true},
			want: render.ImageFiles{
				Src:   at(scan, "scan-10x10.png", pngFile(10, 10)),
				Width: 10, Height: 10,
				Srcset: []render.ImageFile{
					{Path: at(scan, "scan-10x10.png", pngFile(10, 10)), Width: 10},
					{Path: at(scan, "scan-20x20.png", pngFile(20, 20)), Width: 20},
				},
			},
		},
		{
			name: "cropped size larger than the image",
			id:   iccID,
			size: &content.ImageSize{Name: "large", Width: 100, Height: 100, Crop: true},
			want: render.ImageFiles{Src: at(scan, "scan-32x32.png", pngFile(32, 32)), Width: 32, Height: 32},
		},
	} {
		if got, ok := r.Image(tt.id, tt.size); !reflect.DeepEqual(got, tt.want) || !ok {
			t.Errorf("%s: Image(%s, %v) = %+v, %v, want %+v, true", tt.name, tt.id, tt.size, got, ok, tt.want)
		}
	}
	for _, id := range []string{pdfID, missingMediaID} {
		if got, ok := r.Image(id, nil); !reflect.DeepEqual(got, render.ImageFiles{}) || ok {
			t.Errorf("Image(%s, nil) = %+v, %v, want none", id, got, ok)
		}
	}

	for _, tt := range []struct {
		id            string
		path          string
		width, height int
	}{
		{imageID, "/media/" + imageID + "/会社外観.jpg", 1200, 800},
		{pdfID, "/media/" + pdfID + "/会社案内-2026年版.pdf", 0, 0},
		{animationID, "/media/" + animationID + "/loading.gif", 40, 30},
	} {
		path, width, height, err := r.file(tt.id)
		if path != tt.path || width != tt.width || height != tt.height || err != nil {
			t.Errorf("file(%s) = %q, %d, %d, %v, want %q, %d, %d, nil",
				tt.id, path, width, height, err, tt.path, tt.width, tt.height)
		}
		if path, err := r.mediaPath(tt.id); path != tt.path || err != nil {
			t.Errorf("mediaPath(%s) = %q, %v, want %q, nil", tt.id, path, err, tt.path)
		}
	}
	wantErr := fmt.Sprintf("media %q does not exist", missingMediaID)
	if path, _, _, err := r.file(missingMediaID); path != "" || err == nil || err.Error() != wantErr {
		t.Errorf("file(%s) = %q, %v, want an error %q", missingMediaID, path, err, wantErr)
	}

	wantIcons := []icon{
		{size: 32, path: at(favicon, "icon-32x32.png", pngFile(32, 32)), width: 32, height: 32},
		{size: 180, path: at(favicon, "icon.png", pngFile(64, 64)), width: 64, height: 64},
		{size: 192, path: at(favicon, "icon.png", pngFile(64, 64)), width: 64, height: 64},
		{size: 512, path: at(favicon, "icon.png", pngFile(64, 64)), width: 64, height: 64},
	}
	if got := r.icons(iconID); !slices.Equal(got, wantIcons) {
		t.Errorf("icons(%s) = %+v, want %+v", iconID, got, wantIcons)
	}
	for _, id := range []string{pdfID, missingMediaID} {
		if got := r.icons(id); got != nil {
			t.Errorf("icons(%s) = %+v, want nil", id, got)
		}
	}

	for _, tt := range []struct {
		id, title, path string
		ok              bool
	}{
		{aboutID, "会社概要", "/about/", true},
		{homeID, "ホーム", "/", true},
		{newsID, "お知らせ", "/news/", true},
		{postID, "新サービスを始めました", "/2026/10/26/新サービス/", true},
		{draftID, "", "", true},
		{missingEntryID, "", "", false},
	} {
		if title, path, ok := r.Entry(tt.id); title != tt.title || path != tt.path || ok != tt.ok {
			t.Errorf("Entry(%s) = %q, %q, %v, want %q, %q, %v", tt.id, title, path, ok, tt.title, tt.path, tt.ok)
		}
	}
	if f, ok := r.Form(formID); f != &b.forms[formID].v || !ok {
		t.Errorf("Form(%s) = %v, %v, want its document", formID, f, ok)
	}
	if f, ok := r.Form("frm_01JCZZZZZZZZZZZZZZZZZZZZZZ"); f != nil || ok {
		t.Errorf("Form of a missing form = %v, %v, want nil, false", f, ok)
	}

	dir := mustPublish(t, b)
	type file struct {
		m    content.Media
		name string
		v    imaging.Variant // none for a PDF
	}
	written := []file{
		{photo, "会社外観-150x150.jpg", jpegFile(150, 150)},
		{photo, "会社外観-300x300.jpg", jpegFile(300, 300)},
		{photo, "会社外観-640x427.jpg", jpegFile(640, 427)},
		{photo, "会社外観-960x640.jpg", jpegFile(960, 640)},
		{photo, "会社外観.jpg", jpegFile(1200, 800)},
		{guide, "会社案内-2026年版.pdf", imaging.Variant{}},
		{favicon, "icon-32x32.png", pngFile(32, 32)},
		{favicon, "icon.png", pngFile(64, 64)},
		{anim, "loading.gif", animation},
		{scan, "scan-10x10.png", pngFile(10, 10)},
		{scan, "scan-20x20.png", pngFile(20, 20)},
		{scan, "scan-32x32.png", pngFile(32, 32)},
		{scan, "scan.png", pngFile(48, 32)},
	}
	var want []string
	for _, f := range written {
		want = append(want, "media/"+f.m.ID+"/"+f.name)
	}
	if got := listFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	checkMediaOutputs(t, b, dir)
	for _, id := range []string{unusedID, avatarID, logoID, ogImageID} {
		if n := src.openCount(b.media[id].v.Hash); n != 0 {
			t.Errorf("the original of %s was opened %d times, want 0", id, n)
		}
	}
	// Once to publish it, never to inspect it.
	if n := src.openCount(guide.Hash); n != 1 {
		t.Errorf("the original of the PDF was opened %d times, want once", n)
	}

	for _, f := range written {
		got, err := os.ReadFile(filepath.Join(dir, "media", f.m.ID, f.name))
		if err != nil {
			t.Fatal(err)
		}
		want := []byte(testPDF)
		if f.v != (imaging.Variant{}) {
			info, err := r.images.Info(f.m.Hash, r.open(f.m.Hash))
			if err != nil {
				t.Fatal(err)
			}
			files, err := r.images.Files(t.Context(), f.m.Hash, r.open(f.m.Hash), info, []imaging.Variant{f.v})
			if err != nil {
				t.Fatal(err)
			}
			want = files[0]
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s/%s differs from the file that imaging makes", f.m.ID, f.name)
		}
	}

	wantWarnings := []string{
		fileOf(content.RefMedia, iccID) + ":1:1: invalid ICC profile (iCCP data: zlib: invalid header); published without it",
	}
	if got := errorTexts(t, b.warnings.sorted()); !slices.Equal(got, wantWarnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantWarnings, "\n"))
	}
	var p problems
	b.checkPaths(&p)
	if p.len() > 0 {
		t.Errorf("checkPaths() errors:\n%v", p.sorted())
	}
}

// TestPublishMediaErrors checks each original against its document: a difference is an error at the document, and the
// medium is not published.
func TestPublishMediaErrors(t *testing.T) {
	imageDoc, pdfDoc := fileOf(content.RefMedia, imageID), fileOf(content.RefMedia, pdfID)
	// replace stores data as the original with hash, and returns the hash of data.
	replace := func(s *memSource, hash string, data []byte) string {
		s.files[strings.TrimPrefix(hash, "sha256:")] = &fstest.MapFile{Data: data}
		return mediaHash(data)
	}
	tests := []struct {
		name string
		// change changes s, whose image and PDF are photo and guide, and returns the errors to expect.
		change func(t *testing.T, s *memSource, photo, guide content.Media) []string
	}{
		{
			name: "width",
			change: func(t *testing.T, s *memSource, _, _ content.Media) []string {
				edit(t, s, content.RefMedia, imageID, func(m *content.Media) { m.Width = 65 })
				return []string{imageDoc + ":8:3: /width: the original is 64x48 pixels as displayed, not 65x48"}
			},
		},
		{
			name: "height",
			change: func(t *testing.T, s *memSource, _, _ content.Media) []string {
				edit(t, s, content.RefMedia, imageID, func(m *content.Media) { m.Height = 50 })
				return []string{imageDoc + ":9:3: /height: the original is 64x48 pixels as displayed, not 64x50"}
			},
		},
		{
			name: "MIME type",
			change: func(t *testing.T, s *memSource, _, _ content.Media) []string {
				edit(t, s, content.RefMedia, imageID, func(m *content.Media) { m.MIMEType = "image/webp" })
				return []string{imageDoc + ":6:3: /mimeType: the original is image/jpeg, not image/webp"}
			},
		},
		{
			name: "MIME type and size as displayed",
			change: func(t *testing.T, s *memSource, _, _ content.Media) []string {
				edit(t, s, content.RefMedia, imageID, func(m *content.Media) {
					m.MIMEType, m.Width, m.Height = "image/png", 48, 64
				})
				return []string{
					imageDoc + ":6:3: /mimeType: the original is image/jpeg, not image/png",
					imageDoc + ":8:3: /width: the original is 64x48 pixels as displayed, not 48x64",
				}
			},
		},
		{
			name: "size",
			change: func(t *testing.T, s *memSource, photo, _ content.Media) []string {
				edit(t, s, content.RefMedia, imageID, func(m *content.Media) { m.Size++ })
				return []string{fmt.Sprintf("%s:7:3: /size: the original has %d bytes, not %d", imageDoc, photo.Size, photo.Size+1)}
			},
		},
		{
			name: "size of a PDF",
			change: func(t *testing.T, s *memSource, _, guide content.Media) []string {
				edit(t, s, content.RefMedia, pdfID, func(m *content.Media) { m.Size-- })
				return []string{fmt.Sprintf("%s:7:3: /size: the original has %d bytes, not %d", pdfDoc, guide.Size, guide.Size-1)}
			},
		},
		{
			name: "missing originals",
			change: func(_ *testing.T, s *memSource, photo, guide content.Media) []string {
				delete(s.files, strings.TrimPrefix(photo.Hash, "sha256:"))
				delete(s.files, strings.TrimPrefix(guide.Hash, "sha256:"))
				return []string{
					imageDoc + ":4:3: /hash: no original with this hash",
					pdfDoc + ":4:3: /hash: no original with this hash",
				}
			},
		},
		{
			name: "hash of an image",
			change: func(_ *testing.T, s *memSource, photo, _ content.Media) []string {
				data := slices.Clone(s.files[strings.TrimPrefix(photo.Hash, "sha256:")].Data)
				data[len(data)-3] ^= 0xff
				hash := replace(s, photo.Hash, data)
				return []string{imageDoc + ":1:1: cannot read the original: " + photo.Hash + ": the original has the hash " + hash}
			},
		},
		{
			name: "hash of a PDF",
			change: func(_ *testing.T, s *memSource, _, guide content.Media) []string {
				hash := replace(s, guide.Hash, []byte(strings.Replace(testPDF, "1.7", "1.6", 1)))
				return []string{pdfDoc + ":4:3: /hash: the original has the hash " + hash}
			},
		},
		{
			name: "image that is a PDF",
			change: func(t *testing.T, s *memSource, _, _ content.Media) []string {
				m := s.putOriginal(t, imageID, "会社外観.jpg", []byte(testPDF))
				edit(t, s, content.RefMedia, imageID, func(m *content.Media) {
					m.MIMEType, m.Width, m.Height = "image/jpeg", 64, 48
				})
				return []string{imageDoc + ":1:1: cannot read the original: " + m.Hash + ": not an image: application/pdf"}
			},
		},
		{
			name: "PDF that is an image",
			change: func(t *testing.T, s *memSource, _, _ content.Media) []string {
				s.putOriginal(t, pdfID, "会社案内.pdf", testPNG(t, 8, 8))
				edit(t, s, content.RefMedia, pdfID, func(m *content.Media) {
					m.MIMEType, m.Width, m.Height = "application/pdf", 0, 0
				})
				return []string{pdfDoc + ":6:3: /mimeType: the original is image/png, not application/pdf"}
			},
		},
		{
			name: "PDF in no format",
			change: func(t *testing.T, s *memSource, _, _ content.Media) []string {
				data := []byte("%PDF-x.y is not the header of a PDF")
				hash := s.putFile(data)
				edit(t, s, content.RefMedia, pdfID, func(m *content.Media) { m.Hash, m.Size = hash, len(data) })
				return []string{pdfDoc + ":6:3: /mimeType: the original is not application/pdf: unsupported file format"}
			},
		},
		{
			name: "image data that cannot be decoded",
			change: func(t *testing.T, s *memSource, photo, _ content.Media) []string {
				data := s.files[strings.TrimPrefix(photo.Hash, "sha256:")].Data
				sos := bytes.Index(data, []byte{0xff, 0xda})
				m := s.putOriginal(t, imageID, "会社外観.jpg", data[:sos+20])
				return []string{imageDoc + ":1:1: cannot make the files: " + m.Hash +
					": decoding the original: invalid JPEG format: short Huffman data"}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			photo := src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 64, 48))
			guide := src.putOriginal(t, pdfID, "会社案内.pdf", []byte(testPDF))
			want := tt.change(t, src, photo, guide)
			b := mustLoad(t, src)
			r := newTestResolver(t, b)
			r.Media(imageID)
			r.Media(pdfID)
			dir, pub := newPublic(t)
			var p problems
			if err := b.publishMedia(t.Context(), pub, 2, &p); err != nil {
				t.Fatalf("publishMedia() = %v", err)
			}
			if got := errorTexts(t, p.sorted()); !slices.Equal(got, want) {
				t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			var files []string
			for _, f := range []struct{ doc, file string }{
				{imageDoc, "media/" + imageID + "/会社外観.jpg"},
				{pdfDoc, "media/" + pdfID + "/会社案内.pdf"},
			} {
				if !slices.ContainsFunc(want, func(e string) bool { return strings.HasPrefix(e, f.doc+":") }) {
					files = append(files, f.file)
				}
			}
			if got := listFiles(t, dir); !slices.Equal(got, files) {
				t.Errorf("files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(files, "\n"))
			}
			checkMediaOutputs(t, b, dir)
		})
	}
}

// TestPublishMediaCache checks that a build with the cache of an earlier one inspects no originals and only opens each
// to check its size, but for a PDF, which it reads; that it writes the same files with the same warnings; and that it
// finds an original missing although the cache has all that it needs.
func TestPublishMediaCache(t *testing.T) {
	src := testSource(t)
	photo := src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 64, 48))
	guide := src.putOriginal(t, pdfID, "会社案内.pdf", []byte(testPDF))
	scan := src.putOriginal(t, iccID, "scan.png", withInvalidICC(testPNG(t, 48, 32)))
	hashes := []string{photo.Hash, guide.Hash, scan.Hash}
	cache := t.TempDir()
	// publish builds the site with the cache, asking about the same media each time, and publishes them into a new
	// directory, which it returns.
	publish := func() (*builder, string, *problems) {
		b := mustLoad(t, src)
		b.res = newResolver(b, newTestImages(t, cache), 2)
		b.res.Image(imageID, nil)
		b.res.Image(imageID, &content.ImageSize{Name: "thumbnail", Width: 16, Height: 16, Crop: true})
		b.res.Media(pdfID)
		b.res.Image(iccID, nil)
		dir, pub := newPublic(t)
		var p problems
		if err := b.publishMedia(t.Context(), pub, 3, &p); err != nil {
			t.Fatalf("publishMedia() = %v", err)
		}
		return b, dir, &p
	}
	var files [2]map[string]string
	var warnings [2][]string
	// The opens of each original in hashes by each build: to inspect it, to check its size and to make the files of an
	// image without a cache, or to check its size and to read a PDF.
	for i, want := range [][]int{{3, 1, 3}, {1, 1, 1}} {
		before := make([]int, len(hashes))
		for k, hash := range hashes {
			before[k] = src.openCount(hash)
		}
		b, dir, p := publish()
		if p.len() > 0 {
			t.Fatalf("publishMedia() errors:\n%v", p.sorted())
		}
		for k, hash := range hashes {
			if n := src.openCount(hash) - before[k]; n != want[k] {
				t.Errorf("build %d opened the original %s %d times, want %d", i+1, hash, n, want[k])
			}
		}
		files[i] = make(map[string]string)
		for _, name := range listFiles(t, dir) {
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
			if err != nil {
				t.Fatal(err)
			}
			files[i][name] = string(data)
		}
		warnings[i] = errorTexts(t, b.warnings.sorted())
	}
	if len(files[0]) != 5 || !reflect.DeepEqual(files[0], files[1]) {
		t.Errorf("the builds wrote %d and %d files, which differ", len(files[0]), len(files[1]))
	}
	if len(warnings[0]) != 1 || !slices.Equal(warnings[0], warnings[1]) {
		t.Errorf("warnings:\n%s\nand then:\n%s", strings.Join(warnings[0], "\n"), strings.Join(warnings[1], "\n"))
	}

	delete(src.files, strings.TrimPrefix(photo.Hash, "sha256:"))
	_, dir, p := publish()
	wantErrors := []string{fileOf(content.RefMedia, imageID) + ":4:3: /hash: no original with this hash"}
	if got := errorTexts(t, p.sorted()); !slices.Equal(got, wantErrors) {
		t.Errorf("errors without the original:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantErrors, "\n"))
	}
	want := []string{"media/" + pdfID + "/会社案内.pdf", "media/" + iccID + "/scan.png"}
	if got := listFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("files without the original:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestPublishMediaCanceled checks that publishMedia stops when its context is done, before it starts or while it makes
// the files of an image, and then writes nothing and reports no problems.
func TestPublishMediaCanceled(t *testing.T) {
	for _, started := range []bool{false, true} {
		src := testSource(t)
		src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 64, 48))
		src.putOriginal(t, pdfID, "会社案内.pdf", []byte(testPDF))
		b := mustLoad(t, src)
		r := newTestResolver(t, b)
		r.Media(imageID)
		r.Media(pdfID)
		ctx, cancel := context.WithCancel(t.Context())
		if started {
			// When it checks the size of the image, which it publishes first with one worker.
			b.src = hookedSource{Source: src, hook: func(string) { cancel() }}
		} else {
			cancel()
		}
		dir, pub := newPublic(t)
		var p problems
		if err := b.publishMedia(ctx, pub, 1, &p); !errors.Is(err, context.Canceled) {
			t.Errorf("publishMedia() canceled (started: %v) = %v, want %v", started, err, context.Canceled)
		}
		if files := listFiles(t, dir); len(files) > 0 || p.len() > 0 {
			t.Errorf("publishMedia() canceled (started: %v) wrote %q, with the errors %v", started, files, p.sorted())
		}
		cancel()
	}
}

// TestResolverConcurrent checks that a medium asked about by many goroutines at once is inspected once, also when its
// original cannot be.
func TestResolverConcurrent(t *testing.T) {
	src := testSource(t)
	photo := src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 120, 80))
	broken := src.putOriginal(t, avatarID, "yamada.png", []byte(testPDF))
	edit(t, src, content.RefMedia, avatarID, func(m *content.Media) { m.MIMEType, m.Width, m.Height = "image/png", 8, 8 })
	b := mustLoad(t, src)
	r := newTestResolver(t, b)
	images := make([]render.ImageFiles, 16)
	var wg sync.WaitGroup
	for i := range images {
		wg.Go(func() {
			images[i], _ = r.Image(imageID, nil)
			r.Media(imageID)
			r.icons(imageID)
			if _, _, _, err := r.file(imageID); err != nil {
				t.Error(err)
			}
			if _, ok := r.Image(avatarID, nil); ok {
				t.Errorf("Image(%s, nil) found files of a PDF", avatarID)
			}
			if m, path, ok := r.Media(avatarID); m != nil || path != "" || ok {
				t.Errorf("Media(%s) = %v, %q, %v, want nil, \"\", false", avatarID, m, path, ok)
			}
			wantErr := "cannot read the original: " + broken.Hash + ": not an image: application/pdf"
			if path, _, _, err := r.file(avatarID); path != "" || err == nil || err.Error() != wantErr {
				t.Errorf("file(%s) = %q, %v, want an error %q", avatarID, path, err, wantErr)
			}
			if icons := r.icons(avatarID); icons != nil {
				t.Errorf("icons(%s) = %+v, want nil", avatarID, icons)
			}
		})
	}
	wg.Wait()
	for _, m := range []content.Media{photo, broken} {
		if n := src.openCount(m.Hash); n != 1 {
			t.Errorf("the original of %s was opened %d times, want once", m.ID, n)
		}
	}
	for _, files := range images {
		if files.Src == "" || !reflect.DeepEqual(files, images[0]) {
			t.Errorf("Image() = %+v, want %+v", files, images[0])
		}
	}

	dir, pub := newPublic(t)
	var p problems
	if err := b.publishMedia(t.Context(), pub, 4, &p); err != nil {
		t.Fatal(err)
	}
	wantErrors := []string{
		fileOf(content.RefMedia, avatarID) + ":1:1: cannot read the original: " + broken.Hash +
			": not an image: application/pdf",
	}
	if got := errorTexts(t, p.sorted()); !slices.Equal(got, wantErrors) {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantErrors, "\n"))
	}
	// Once more, to check its size: the error of inspecting it is that of the first time.
	if n := src.openCount(broken.Hash); n != 2 {
		t.Errorf("the original of %s was opened %d times in all, want twice", avatarID, n)
	}
	want := []string{
		"media/" + imageID + "/会社外観-32x32.png",
		"media/" + imageID + "/会社外観-80x80.png",
		"media/" + imageID + "/会社外観.jpg",
	}
	if got := listFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// hookedSource is a Source that calls hook with the hash before it opens an original.
type hookedSource struct {
	Source
	hook func(hash string)
}

func (s hookedSource) OpenMedia(hash string) (fs.File, error) {
	s.hook(hash)
	return s.Source.OpenMedia(hash)
}

// gate returns a hook that sends the hash of each original opened to arrived, and then waits until release is closed.
func gate(arrived chan<- string, release <-chan struct{}) func(hash string) {
	return func(hash string) {
		arrived <- hash
		<-release
	}
}

// TestResolverInspecting checks that the resolver reads at most as many originals at once as it was made for, two.
func TestResolverInspecting(t *testing.T) {
	src := testSource(t)
	ids := []string{imageID, avatarID, logoID, iconID, ogImageID}
	for i, id := range ids {
		src.putOriginal(t, id, "image.png", testPNG(t, 8+i, 8))
	}
	b := mustLoad(t, src)
	r := newTestResolver(t, b)
	arrived, release := make(chan string, len(ids)), make(chan struct{})
	b.src = hookedSource{Source: src, hook: gate(arrived, release)}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			if _, ok := r.Image(id, nil); !ok {
				t.Errorf("Image(%s, nil) found no files", id)
			}
		})
	}
	<-arrived
	<-arrived
	select {
	case hash := <-arrived:
		t.Errorf("the original %s was opened while two others were read", hash)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
}

// TestPublishMediaWorkers checks that publishMedia publishes as many media at once as it has workers.
func TestPublishMediaWorkers(t *testing.T) {
	src := testSource(t)
	src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 16, 12))
	src.putOriginal(t, pdfID, "会社案内.pdf", []byte(testPDF))
	b := mustLoad(t, src)
	r := newTestResolver(t, b)
	r.Media(imageID)
	r.Media(pdfID)
	arrived, release := make(chan string, 3), make(chan struct{})
	b.src = hookedSource{Source: src, hook: gate(arrived, release)}
	_, pub := newPublic(t)
	var p problems
	done := make(chan error)
	go func() { done <- b.publishMedia(t.Context(), pub, 2, &p) }()
	<-arrived
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Error("publishMedia with 2 workers published one medium at a time")
	}
	close(release)
	if err := <-done; err != nil || p.len() > 0 {
		t.Errorf("publishMedia() = %v, with the errors %v", err, p.sorted())
	}
}

// TestRedirectsToMedia checks that a redirect to a medium goes to its published file, which is published, and that the
// files of media take part in the check of duplicates.
func TestRedirectsToMedia(t *testing.T) {
	src := testSource(t)
	src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 64, 48))
	src.putOriginal(t, pdfID, "会社案内.pdf", []byte(testPDF))
	src.addRedirect(t, 1, "/wp-content/uploads/office.jpg", imageID)
	src.addRedirect(t, 2, "/wp-content/uploads/guide.pdf", pdfID)
	src.addRedirect(t, 3, "/wp-content/uploads/logo.png", logoID)
	src.addRedirect(t, 4, "/media/"+imageID+"/会社外観.jpg", "/about/")
	b := mustLoad(t, src)
	r := newTestResolver(t, b)
	var p problems
	b.resolveRedirects(r.mediaPath, &p)
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	wantRedirects := []string{
		"/?p=123 -> /2026/10/26/新サービス/ 301 " + fileOf(content.RefRedirect, redirectID) + "#/from",
		"/home/ -> / 301 site/settings.json#/frontPage/ja (generated)",
		"/media/" + imageID + "/会社外観.jpg -> /about/ 301 " + rdr(4) + "#/from",
		"/wp-content/uploads/guide.pdf -> /media/" + pdfID + "/会社案内.pdf 301 " + rdr(2) + "#/from",
		"/wp-content/uploads/office.jpg -> /media/" + imageID + "/会社外観.jpg 301 " + rdr(1) + "#/from",
	}
	if got := redirectTexts(b.serverRedirects); !slices.Equal(got, wantRedirects) {
		t.Errorf("redirects:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantRedirects, "\n"))
	}
	logo := b.media[logoID].v.Hash
	digits := strings.TrimPrefix(logo, "sha256:")
	wantErrors := []string{
		rdr(3) + ":5:3: /to: cannot make the path: cannot read the original: " + logo + ": open " + digits +
			": file does not exist",
	}
	if got := errorTexts(t, p.sorted()); !slices.Equal(got, wantErrors) {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantErrors, "\n"))
	}

	dir, pub := newPublic(t)
	p = problems{}
	if err := b.publishMedia(t.Context(), pub, 2, &p); err != nil {
		t.Fatal(err)
	}
	b.checkPaths(&p)
	wantErrors = []string{
		fileOf(content.RefMedia, logoID) + ":4:3: /hash: no original with this hash",
		rdr(4) + `:4:3: /from: from "/media/` + imageID + `/会社外観.jpg" is the output path "/media/` + imageID +
			`/会社外観.jpg" of ` + fileOf(content.RefMedia, imageID),
	}
	if got := errorTexts(t, p.sorted()); !slices.Equal(got, wantErrors) {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantErrors, "\n"))
	}
	want := []string{"media/" + imageID + "/会社外観.jpg", "media/" + pdfID + "/会社案内.pdf"}
	if got := listFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
