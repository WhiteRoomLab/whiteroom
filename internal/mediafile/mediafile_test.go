package mediafile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

var (
	rect    = image.Rect(0, 0, 3, 2) // not square, to tell the width from the height
	palette = color.Palette{color.Black, color.White}
)

type probeTest struct {
	name string
	data []byte
	want Info
}

type errorTest struct {
	name string
	data []byte
	msg  string // the start of the error message
	is   error  // wrapped in the error, if not nil
}

type testFile struct {
	name string
	data []byte
}

func encode(t testing.TB, enc func(io.Writer) error) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := enc(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodePNG(t testing.TB, m image.Image) []byte {
	t.Helper()
	return encode(t, func(w io.Writer) error { return png.Encode(w, m) })
}

func encodeJPEG(t testing.TB, m image.Image) []byte {
	t.Helper()
	return encode(t, func(w io.Writer) error { return jpeg.Encode(w, m, nil) })
}

func encodeGIF(t testing.TB, m image.Image) []byte {
	t.Helper()
	return encode(t, func(w io.Writer) error { return gif.Encode(w, m, nil) })
}

func encodeGIFAll(t testing.TB, g *gif.GIF) []byte {
	t.Helper()
	return encode(t, func(w io.Writer) error { return gif.EncodeAll(w, g) })
}

func pngChunk(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(b, typ...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

// pngHeader returns the signature and the IHDR chunk of a PNG file of w by h pixels in 8-bit RGBA.
func pngHeader(w, h uint32) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	return append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
}

// jpegSegment returns the offset of the first segment with marker in data, a file from jpeg.Encode.
func jpegSegment(t testing.TB, data []byte, marker byte) int {
	t.Helper()
	for i := 2; i+4 <= len(data) && data[i] == 0xff; i += 2 + int(binary.BigEndian.Uint16(data[i+2:])) {
		if data[i+1] == marker {
			return i
		}
	}
	t.Fatalf("no segment with marker %#x", marker)
	return 0
}

// patch returns a copy of data with b written at off.
func patch(data []byte, off int, b ...byte) []byte {
	c := slices.Clone(data)
	copy(c[off:], b)
	return c
}

// failingReader returns the rest of data with err from every Read.
type failingReader struct {
	data []byte
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

func probeTests(t testing.TB) []probeTest {
	jpegRGBA := encodeJPEG(t, image.NewRGBA(rect))
	sof := jpegSegment(t, jpegRGBA, 0xc0)
	sos := jpegSegment(t, jpegRGBA, 0xda)
	sosEnd := sos + 2 + int(binary.BigEndian.Uint16(jpegRGBA[sos+2:]))
	gif89a := encodeGIF(t, image.NewPaletted(rect, palette))
	frames := []*image.Paletted{
		image.NewPaletted(image.Rect(0, 0, 3, 2), palette),
		image.NewPaletted(image.Rect(2, 2, 5, 4), palette),
	}

	img := func(mimeType string, w, h int) Info {
		return Info{MIMEType: mimeType, Width: w, Height: h, Orientation: 1}
	}
	pdf := Info{MIMEType: PDF}
	return []probeTest{
		{name: "PNG RGBA", data: encodePNG(t, image.NewRGBA(rect)), want: img(PNG, 3, 2)},
		{name: "PNG gray", data: encodePNG(t, image.NewGray(rect)), want: img(PNG, 3, 2)},
		{name: "PNG paletted", data: encodePNG(t, image.NewPaletted(rect, palette)), want: img(PNG, 3, 2)},
		{
			name: "PNG paletted with tRNS",
			data: encodePNG(t, image.NewPaletted(rect, color.Palette{color.Transparent, color.White})),
			want: img(PNG, 3, 2),
		},
		{
			name: "PNG with image data that is not zlib",
			data: slices.Concat(pngHeader(3, 2), pngChunk("IDAT", []byte("not zlib")), pngChunk("IEND", nil)),
			want: img(PNG, 3, 2),
		},
		{name: "JPEG color", data: jpegRGBA, want: img(JPEG, 3, 2)},
		{name: "JPEG gray", data: encodeJPEG(t, image.NewGray(rect)), want: img(JPEG, 3, 2)},
		{name: "JPEG progressive", data: patch(jpegRGBA, sof+1, 0xc2), want: img(JPEG, 3, 2)},
		{
			name: "JPEG with broken scan data",
			data: append(slices.Clone(jpegRGBA[:sosEnd]), "\xff\xff\x00\x00not scan data"...),
			want: img(JPEG, 3, 2),
		},
		{name: "GIF89a", data: gif89a, want: img(GIF, 3, 2)},
		{name: "GIF87a", data: patch(gif89a, 3, []byte("87a")...), want: img(GIF, 3, 2)},
		{
			name: "GIF with broken image data",
			data: append(slices.Clone(gif89a[:13+3*len(palette)]), "not image data"...),
			want: img(GIF, 3, 2),
		},
		{
			name: "GIF animation",
			data: encodeGIFAll(t, &gif.GIF{
				Image:  frames,
				Delay:  []int{10, 10},
				Config: image.Config{ColorModel: palette, Width: 5, Height: 4},
			}),
			want: img(GIF, 5, 4),
		},
		{
			name: "GIF animation without a global color table",
			data: encodeGIFAll(t, &gif.GIF{Image: frames, Delay: []int{10, 10}, Config: image.Config{Width: 5, Height: 4}}),
			want: img(GIF, 5, 4),
		},
		{name: "PDF 1.7", data: []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n1 0 obj\n"), want: pdf},
		{name: "PDF 2.0", data: []byte("%PDF-2.0\r\n%\xe2\xe3\xcf\xd3\r\n"), want: pdf},
		{name: "PDF header only", data: []byte("%PDF-1.4"), want: pdf},
	}
}

func unsupportedTests(t testing.TB) []testFile {
	pngRGBA := encodePNG(t, image.NewRGBA(rect))
	jpegRGBA := encodeJPEG(t, image.NewRGBA(rect))
	gif89a := encodeGIF(t, image.NewPaletted(rect, palette))
	return []testFile{
		{name: "empty", data: []byte{}},
		{name: "one byte", data: []byte{0xff}},
		{name: "JPEG SOI only", data: []byte("\xff\xd8")},
		{name: "PNG signature cut", data: []byte("\x89PNG\r\n\x1a")},
		{name: "GIF signature cut", data: []byte("GIF89")},
		{name: "PDF header cut", data: []byte("%PDF-1.")},
		{name: "JPEG without FF after SOI", data: patch(jpegRGBA, 2, 0x00)},
		{name: "JPEG first byte", data: patch(jpegRGBA, 0, 0x00)},
		{name: "PNG first byte", data: patch(pngRGBA, 0, 0x88)},
		{name: "PNG last byte of signature", data: patch(pngRGBA, 7, 0x00)},
		{name: "GIF lower case", data: patch(gif89a, 0, 'g')},
		{name: "GIF88a", data: patch(gif89a, 4, '8')},
		{name: "GIF89b", data: patch(gif89a, 5, 'b')},
		{name: "SVG with XML declaration", data: []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<svg xmlns="http://www.w3.org/2000/svg" width="3" height="2"/>`)},
		{name: "SVG", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="3" height="2"><script>alert(1)</script></svg>`)},
		{name: "HTML", data: []byte("<!DOCTYPE html>\n<html><body>%PDF-1.7</body></html>\n")},
		{name: "BMP", data: []byte("BM\x46\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00\x28\x00\x00\x00\x03\x00\x00\x00\x02\x00\x00\x00\x01\x00\x18\x00")},
		{name: "TIFF little endian", data: []byte("II*\x00\x08\x00\x00\x00\x00\x00")},
		{name: "TIFF big endian", data: []byte("MM\x00*\x00\x00\x00\x08\x00\x00")},
		{name: "HEIC", data: []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic")},
		{name: "AVIF", data: []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf")},
		{name: "RIFF WAVE", data: []byte("RIFF\x24\x08\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00")},
		{name: "ZIP", data: []byte("PK\x03\x04\x14\x00\x00\x00\x08\x00")},
		{name: "PDF after a space", data: []byte(" %PDF-1.7\n")},
		{name: "PDF after a line break", data: []byte("\n%PDF-1.7\n")},
		{name: "PDF after a BOM", data: []byte("\xef\xbb\xbf%PDF-1.7\n")},
		{name: "PDF lower case", data: []byte("%pdf-1.7\n")},
		{name: "PDF without version", data: []byte("%PDF-\n\n\n\n")},
		{name: "PDF major version not a digit", data: []byte("%PDF-x.7\n")},
		{name: "PDF version without dot", data: []byte("%PDF-1x7\n")},
		{name: "PDF minor version not a digit", data: []byte("%PDF-1.x\n")},
		{name: "PDF version with a slash", data: []byte("%PDF-/.7\n")},
		{name: "PDF version with a colon", data: []byte("%PDF-1.:\n")},
	}
}

func invalidTests(t testing.TB) []errorTest {
	pngRGBA := encodePNG(t, image.NewRGBA(rect))
	badCRC := pngHeader(3, 2)
	badCRC[len(badCRC)-1] ^= 0xff
	jpegRGBA := encodeJPEG(t, image.NewRGBA(rect))
	sof := jpegSegment(t, jpegRGBA, 0xc0)
	sos := jpegSegment(t, jpegRGBA, 0xda)
	gif89a := encodeGIF(t, image.NewPaletted(rect, palette))
	return []errorTest{
		{name: "PNG signature only", data: []byte("\x89PNG\r\n\x1a\n"), msg: "invalid PNG: ", is: io.ErrUnexpectedEOF},
		{name: "PNG IHDR cut", data: pngRGBA[:20], msg: "invalid PNG: ", is: io.ErrUnexpectedEOF},
		{name: "PNG IHDR checksum", data: badCRC, msg: "invalid PNG: ", is: png.FormatError("invalid checksum")},
		{name: "PNG zero width", data: pngHeader(0, 2), msg: "invalid PNG: ", is: png.FormatError("non-positive dimension")},
		{name: "PNG zero height", data: pngHeader(3, 0), msg: "invalid PNG: ", is: png.FormatError("non-positive dimension")},
		{name: "JPEG signature only", data: []byte("\xff\xd8\xff"), msg: "invalid JPEG: ", is: io.ErrUnexpectedEOF},
		{name: "JPEG cut before SOF", data: jpegRGBA[:sof], msg: "invalid JPEG: ", is: io.ErrUnexpectedEOF},
		{
			name: "JPEG arithmetic coding",
			data: patch(jpegRGBA, sof+1, 0xc9),
			msg:  "invalid JPEG: ",
			is:   jpeg.UnsupportedError("unknown marker"),
		},
		{
			name: "JPEG without SOS",
			data: append(slices.Clone(jpegRGBA[:sos]), 0xff, 0xd9),
			msg:  "invalid JPEG: ",
			is:   jpeg.FormatError("missing SOS marker"),
		},
		{name: "JPEG zero height", data: patch(jpegRGBA, sof+5, 0, 0), msg: "invalid JPEG: zero width or height (3x0)"},
		{name: "JPEG zero width", data: patch(jpegRGBA, sof+7, 0, 0), msg: "invalid JPEG: zero width or height (0x2)"},
		{name: "GIF signature only", data: []byte("GIF89a"), msg: "invalid GIF: "},
		{name: "GIF color table cut", data: gif89a[:15], msg: "invalid GIF: "},
		{name: "GIF zero width", data: patch(gif89a, 6, 0, 0), msg: "invalid GIF: zero width or height (0x2)"},
		{name: "GIF zero height", data: patch(gif89a, 8, 0, 0), msg: "invalid GIF: zero width or height (3x0)"},
	}
}

func testFiles(t testing.TB) []testFile {
	var files []testFile
	for _, tt := range probeTests(t) {
		files = append(files, testFile{name: tt.name, data: tt.data})
	}
	files = append(files, unsupportedTests(t)...)
	for _, tt := range invalidTests(t) {
		files = append(files, testFile{name: tt.name, data: tt.data})
	}
	return files
}

func TestProbe(t *testing.T) {
	for _, tt := range probeTests(t) {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Probe(bytes.NewReader(tt.data))
			if err != nil || got != tt.want {
				t.Fatalf("Probe() = %+v, %v; want %+v", got, err, tt.want)
			}
			if !slices.Contains(Formats(), got.MIMEType) {
				t.Errorf("MIME type %q is not in Formats", got.MIMEType)
			}
		})
	}
}

func TestProbeUnsupported(t *testing.T) {
	for _, tt := range unsupportedTests(t) {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Probe(bytes.NewReader(tt.data))
			if !errors.Is(err, ErrUnsupported) || got != (Info{}) {
				t.Errorf("Probe() = %+v, %v; want ErrUnsupported", got, err)
			}
		})
	}
}

func TestProbeInvalid(t *testing.T) {
	for _, tt := range invalidTests(t) {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Probe(bytes.NewReader(tt.data))
			if err == nil || errors.Is(err, ErrUnsupported) || !strings.HasPrefix(err.Error(), tt.msg) {
				t.Fatalf("Probe() = %+v, %v; want an error starting with %q", got, err, tt.msg)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("error %q does not wrap %q", err, tt.is)
			}
			if got != (Info{}) {
				t.Errorf("Info = %+v, want zero", got)
			}
		})
	}
}

func TestProbeReader(t *testing.T) {
	readers := []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{name: "OneByteReader", wrap: iotest.OneByteReader},
		{name: "HalfReader", wrap: iotest.HalfReader},
		{name: "DataErrReader", wrap: iotest.DataErrReader},
	}
	for _, f := range testFiles(t) {
		want, wantErr := Probe(bytes.NewReader(f.data))
		for _, r := range readers {
			t.Run(f.name+"/"+r.name, func(t *testing.T) {
				got, err := Probe(r.wrap(bytes.NewReader(f.data)))
				if got != want || fmt.Sprint(err) != fmt.Sprint(wantErr) {
					t.Errorf("Probe() = %+v, %v; want %+v, %v", got, err, want, wantErr)
				}
			})
		}
	}
}

func TestProbeReadError(t *testing.T) {
	errRead := errors.New("read error")
	isReadError := func(got Info, err error) bool {
		return errors.Is(err, errRead) && err.Error() == errRead.Error() && got == (Info{})
	}

	t.Run("first read", func(t *testing.T) {
		if got, err := Probe(iotest.ErrReader(errRead)); !isReadError(got, err) {
			t.Errorf("Probe() = %+v, %v; want %v", got, err, errRead)
		}
	})
	t.Run("no progress", func(t *testing.T) {
		got, err := Probe(iotest.ErrReader(nil))
		if !errors.Is(err, io.ErrNoProgress) || got != (Info{}) {
			t.Errorf("Probe() = %+v, %v; want %v", got, err, io.ErrNoProgress)
		}
	})
	for _, tt := range probeTests(t) {
		t.Run("with the data/"+tt.name, func(t *testing.T) {
			if got, err := Probe(&failingReader{data: tt.data, err: errRead}); !isReadError(got, err) {
				t.Errorf("Probe() = %+v, %v; want %v", got, err, errRead)
			}
		})
	}
	for _, f := range testFiles(t) {
		t.Run("after each byte/"+f.name, func(t *testing.T) {
			want, wantErr := Probe(bytes.NewReader(f.data))
			for n := range len(f.data) + 1 {
				got, err := Probe(io.MultiReader(bytes.NewReader(f.data[:n]), iotest.ErrReader(errRead)))
				// Probe either reads past n bytes and returns the error as it is, or does not and is unaffected.
				if !isReadError(got, err) && (got != want || fmt.Sprint(err) != fmt.Sprint(wantErr)) {
					t.Fatalf("error after %d bytes: Probe() = %+v, %v; want %v, or %+v, %v", n, got, err, errRead, want, wantErr)
				}
			}
		})
	}
}

func TestProbeReadsHead(t *testing.T) {
	errTooFar := errors.New("read too far")
	for _, tt := range probeTests(t) {
		t.Run(tt.name, func(t *testing.T) {
			r := io.MultiReader(bytes.NewReader(tt.data), bytes.NewReader(make([]byte, 1<<20)), iotest.ErrReader(errTooFar))
			if got, err := Probe(r); err != nil || got != tt.want {
				t.Errorf("Probe() = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestFormats(t *testing.T) {
	want := []string{"image/jpeg", "image/png", "image/webp", "image/gif", "application/pdf"}
	got := Formats()
	if !slices.Equal(got, want) {
		t.Fatalf("Formats() = %q, want %q", got, want)
	}
	got[0] = "image/svg+xml"
	if got := Formats(); !slices.Equal(got, want) {
		t.Errorf("after changing the result, Formats() = %q, want %q", got, want)
	}
}
