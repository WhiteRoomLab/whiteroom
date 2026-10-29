package mediafile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"testing"
)

const (
	vp8xICC       = 0x20
	vp8xAlpha     = 0x10
	vp8xEXIF      = 0x08
	vp8xXMP       = 0x04
	vp8xAnimation = 0x02
)

// riffChunk returns a RIFF chunk, with the padding byte after data of odd size.
func riffChunk(fourCC string, data []byte) []byte {
	b := binary.LittleEndian.AppendUint32([]byte(fourCC), uint32(len(data)))
	b = append(b, data...)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// webpFile returns a WebP file of chunks from riffChunk.
func webpFile(chunks ...[]byte) []byte {
	data := slices.Concat(chunks...)
	b := binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(4+len(data)))
	b = append(b, "WEBP"...)
	return append(b, data...)
}

func le32(v uint32) []byte {
	return binary.LittleEndian.AppendUint32(nil, v)
}

func appendUint24(b []byte, v int) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16))
}

// vp8Frame returns a VP8 key frame of w by h pixels, with the upscaling in their top 2 bits; the frame data is not real.
func vp8Frame(w, h uint16) []byte {
	data := []byte("not frame data")
	tag := 1<<4 | uint32(len(data))<<5 // a key frame of version 0 that is shown, and the size of the first partition
	b := []byte{byte(tag), byte(tag >> 8), byte(tag >> 16), 0x9d, 0x01, 0x2a}
	b = binary.LittleEndian.AppendUint16(b, w)
	b = binary.LittleEndian.AppendUint16(b, h)
	return append(b, data...)
}

// vp8lBits returns the 32 bits after the VP8L signature for w by h pixels.
func vp8lBits(w, h int) uint32 {
	return uint32(w-1) | uint32(h-1)<<14
}

// vp8lStream returns a VP8L bitstream that starts with bits; the image data is not real.
func vp8lStream(bits uint32) []byte {
	b := binary.LittleEndian.AppendUint32([]byte{0x2f}, bits)
	return append(b, "not image data"...)
}

func vp8xData(flags byte, w, h int) []byte {
	b := appendUint24([]byte{flags, 0, 0, 0}, w-1)
	return appendUint24(b, h-1)
}

// anmfData returns the data of ANMF for a frame of w by h pixels at (2x, 2y), followed by chunks.
func anmfData(x, y, w, h int, chunks ...[]byte) []byte {
	b := appendUint24(nil, x)
	b = appendUint24(b, y)
	b = appendUint24(b, w-1)
	b = appendUint24(b, h-1)
	b = appendUint24(b, 100) // the duration in milliseconds
	b = append(b, 0)         // alpha-blend, and do not dispose
	return append(b, slices.Concat(chunks...)...)
}

func webpProbeTests() []probeTest {
	img := func(w, h int) Info {
		return Info{MIMEType: WebP, Width: w, Height: h, Orientation: 1}
	}
	vp8 := riffChunk("VP8 ", vp8Frame(300, 200))
	vp8l := riffChunk("VP8L", vp8lStream(vp8lBits(3, 2)))
	anim := riffChunk("ANIM", []byte{0xff, 0xff, 0xff, 0xff, 0, 0}) // a white background, looping forever
	frame := riffChunk("ANMF", anmfData(0, 0, 3, 2, vp8l))
	animation := func(w, h int) []byte {
		return webpFile(riffChunk("VP8X", vp8xData(vp8xAnimation, w, h)), anim, frame)
	}
	// The largest RIFF size, 2^32-10, for a VP8 chunk cut after the header, as Probe does not read image data.
	huge := patch(webpFile(vp8), 4, le32(4+8+0xffffffea)...)
	huge = patch(huge, 16, le32(0xffffffea)...)
	return []probeTest{
		{name: "WebP VP8", data: webpFile(vp8), want: img(300, 200)},
		{name: "WebP VP8 with upscaling", data: webpFile(riffChunk("VP8 ", vp8Frame(3<<14|300, 1<<14|200))), want: img(300, 200)},
		{name: "WebP VP8 16383x16383", data: webpFile(riffChunk("VP8 ", vp8Frame(16383, 16383))), want: img(16383, 16383)},
		{
			// A key frame of version 0 that is shown, with the first partition of 0 bytes.
			name: "WebP VP8 frame header only",
			data: webpFile(riffChunk("VP8 ", []byte("\x10\x00\x00\x9d\x01\x2a\x2c\x01\xc8\x00"))),
			want: img(300, 200),
		},
		{name: "WebP VP8 chunk of 4 GiB cut", data: huge, want: img(300, 200)},
		{name: "WebP VP8L", data: webpFile(vp8l), want: img(3, 2)},
		{name: "WebP VP8L 1x1", data: webpFile(riffChunk("VP8L", vp8lStream(vp8lBits(1, 1)))), want: img(1, 1)},
		{name: "WebP VP8L 16384x16384", data: webpFile(riffChunk("VP8L", vp8lStream(vp8lBits(16384, 16384)))), want: img(16384, 16384)},
		{name: "WebP VP8L with alpha", data: webpFile(riffChunk("VP8L", vp8lStream(vp8lBits(300, 200)|1<<28))), want: img(300, 200)},
		{name: "WebP VP8L header only", data: webpFile(riffChunk("VP8L", vp8lStream(vp8lBits(3, 2))[:5])), want: img(3, 2)},
		{
			name: "WebP VP8X with ICCP, ALPH and VP8",
			data: webpFile(
				riffChunk("VP8X", vp8xData(vp8xICC|vp8xAlpha, 300, 200)),
				riffChunk("ICCP", []byte("not an ICC profile")),
				riffChunk("ALPH", []byte("\x00not alpha data")),
				vp8,
			),
			want: img(300, 200),
		},
		{
			name: "WebP VP8X with VP8L and XMP",
			data: webpFile(riffChunk("VP8X", vp8xData(vp8xAlpha|vp8xXMP, 3, 2)), vp8l, riffChunk("XMP ", []byte("<x:xmpmeta/>"))),
			want: img(3, 2),
		},
		{
			name: "WebP VP8X with chunks of odd size",
			data: webpFile(riffChunk("VP8X", vp8xData(vp8xICC, 3, 2)), riffChunk("ICCP", []byte("odd")), riffChunk("XYZW", []byte("x")), vp8l),
			want: img(3, 2),
		},
		{
			name: "WebP VP8X with unknown and metadata chunks first",
			data: webpFile(
				riffChunk("VP8X", vp8xData(vp8xEXIF|vp8xXMP, 3, 2)),
				riffChunk("XYZW", []byte("unknown")),
				riffChunk("EXIF", []byte("not Exif data")),
				riffChunk("XMP ", []byte("<x:xmpmeta/>")),
				vp8l,
			),
			want: img(3, 2),
		},
		{
			name: "WebP VP8X with reserved bits",
			data: webpFile(riffChunk("VP8X", patch(vp8xData(0, 3, 2), 0, 0xc1, 0xff, 0xff, 0xff)), vp8l),
			want: img(3, 2),
		},
		{name: "WebP VP8X of 12 bytes", data: webpFile(riffChunk("VP8X", append(vp8xData(0, 3, 2), 0xff, 0xff)), vp8l), want: img(3, 2)},
		{name: "WebP VP8X with ANIM in a still image", data: webpFile(riffChunk("VP8X", vp8xData(0, 3, 2)), anim, vp8l), want: img(3, 2)},
		{
			name: "WebP VP8X animation",
			data: webpFile(
				riffChunk("VP8X", vp8xData(vp8xAnimation|vp8xAlpha, 5, 4)),
				anim,
				frame,
				riffChunk("ANMF", anmfData(1, 1, 3, 2, riffChunk("ALPH", []byte("\x00not alpha data")), riffChunk("VP8 ", vp8Frame(3, 2)))),
			),
			want: img(5, 4),
		},
		{name: "WebP VP8X animation 16777216x255", data: animation(1<<24, 255), want: img(1<<24, 255)},
		{name: "WebP VP8X animation 255x16777216", data: animation(255, 1<<24), want: img(255, 1<<24)},
		{name: "WebP VP8X animation of 4294967295 pixels", data: animation(65537, 65535), want: img(65537, 65535)},
		{name: "WebP with data after the RIFF chunk", data: append(webpFile(vp8l), "trailing data"...), want: img(3, 2)},
	}
}

func webpUnsupportedTests() []testFile {
	vp8l := webpFile(riffChunk("VP8L", vp8lStream(vp8lBits(3, 2))))
	return []testFile{
		{name: "RIFF AVI", data: []byte("RIFF\x00\x10\x00\x00AVI LIST\x00\x01\x00\x00hdrlavih")},
		{name: "WebP signature cut", data: vp8l[:11]},
		{name: "WebP lower case", data: patch(vp8l, 8, []byte("webp")...)},
		{name: "WebP in RIFX", data: patch(vp8l, 3, 'X')},
	}
}

func webpInvalidTests() []errorTest {
	vp8 := webpFile(riffChunk("VP8 ", vp8Frame(300, 200)))
	vp8l := riffChunk("VP8L", vp8lStream(vp8lBits(3, 2)))
	vp8x := riffChunk("VP8X", vp8xData(0, 3, 2))
	animated := riffChunk("VP8X", vp8xData(vp8xAnimation, 3, 2))
	anim := riffChunk("ANIM", []byte{0xff, 0xff, 0xff, 0xff, 0, 0})
	frame := riffChunk("ANMF", anmfData(0, 0, 3, 2, vp8l))
	iccp := riffChunk("ICCP", make([]byte, 100))
	next := riffChunk("XYZW", []byte("next chunk"))
	const noImage = "invalid WebP: no image chunk"
	const cut = "invalid WebP: unexpected EOF"
	tests := []errorTest{
		{name: "WebP header only", data: []byte("RIFF\x04\x00\x00\x00WEBP"), msg: "invalid WebP: no chunks"},
		{name: "WebP RIFF size 0", data: patch(vp8, 4, 0, 0, 0, 0), msg: "invalid WebP: no chunks"},
		{name: "WebP RIFF size 11", data: patch(vp8, 4, 11, 0, 0, 0), msg: "invalid WebP: no chunks"},
		{name: "WebP RIFF size 4294967287", data: patch(vp8, 4, le32(0xfffffff7)...), msg: "invalid WebP: RIFF size too large (4294967287)"},
		{name: "WebP cut after the header", data: vp8[:12], msg: cut, is: io.ErrUnexpectedEOF},
		{name: "WebP chunk header cut", data: vp8[:16], msg: cut, is: io.ErrUnexpectedEOF},
		{
			name: "WebP RIFF size smaller than the chunk",
			data: patch(vp8, 4, 34, 0, 0, 0),
			msg:  `invalid WebP: "VP8 " chunk of 24 bytes exceeds the RIFF size`,
		},
		{
			name: "WebP chunk of 4 GiB larger than the RIFF size",
			data: patch(vp8, 16, 0xff, 0xff, 0xff, 0xff),
			msg:  `invalid WebP: "VP8 " chunk of 4294967295 bytes exceeds the RIFF size`,
		},
		{
			name: "WebP padding after the RIFF chunk",
			data: patch(webpFile(vp8l), 4, 4+8+19, 0, 0, 0),
			msg:  `invalid WebP: "VP8L" chunk of 19 bytes exceeds the RIFF size`,
		},
		{
			name: "WebP first chunk unknown",
			data: webpFile(riffChunk("XYZW", []byte("unknown")), riffChunk("VP8 ", vp8Frame(300, 200))),
			msg:  `invalid WebP: first chunk "XYZW" is not VP8, VP8L or VP8X`,
		},
		{
			name: "WebP ICCP before VP8X",
			data: webpFile(riffChunk("ICCP", []byte("not an ICC profile")), vp8x, vp8l),
			msg:  `invalid WebP: first chunk "ICCP" is not VP8, VP8L or VP8X`,
		},
		{name: "WebP cut after the VP8 chunk header", data: vp8[:20], msg: cut, is: io.ErrUnexpectedEOF},
		{name: "WebP VP8 frame header cut", data: vp8[:25], msg: cut, is: io.ErrUnexpectedEOF},
		{
			name: "WebP VP8 chunk too short",
			data: webpFile(riffChunk("VP8 ", vp8Frame(300, 200)[:9]), next),
			msg:  `invalid WebP: "VP8 " chunk of 9 bytes is too short`,
		},
		{name: "WebP VP8 interframe", data: patch(vp8, 20, vp8[20]|1), msg: "invalid WebP: VP8 frame is not a key frame"},
		{name: "WebP VP8 zero width", data: webpFile(riffChunk("VP8 ", vp8Frame(0, 200))), msg: "invalid WebP: zero width or height (0x200)"},
		{name: "WebP VP8 zero height", data: webpFile(riffChunk("VP8 ", vp8Frame(300, 0))), msg: "invalid WebP: zero width or height (300x0)"},
		{
			name: "WebP VP8 zero width with upscaling",
			data: webpFile(riffChunk("VP8 ", vp8Frame(3<<14, 200))),
			msg:  "invalid WebP: zero width or height (0x200)",
		},
		{name: "WebP VP8L header cut", data: webpFile(vp8l)[:22], msg: cut, is: io.ErrUnexpectedEOF},
		{
			name: "WebP VP8L chunk too short",
			data: webpFile(riffChunk("VP8L", vp8lStream(vp8lBits(3, 2))[:4]), next),
			msg:  `invalid WebP: "VP8L" chunk of 4 bytes is too short`,
		},
		{
			name: "WebP VP8L signature",
			data: webpFile(riffChunk("VP8L", patch(vp8lStream(vp8lBits(3, 2)), 0, 0x2e))),
			msg:  "invalid WebP: missing VP8L signature",
		},
		{
			name: "WebP VP8L version 1",
			data: webpFile(riffChunk("VP8L", vp8lStream(vp8lBits(3, 2)|1<<29))),
			msg:  "invalid WebP: unknown VP8L version 1",
		},
		{
			name: "WebP VP8L version 4",
			data: webpFile(riffChunk("VP8L", vp8lStream(vp8lBits(3, 2)|4<<29))),
			msg:  "invalid WebP: unknown VP8L version 4",
		},
		{name: "WebP VP8X header cut", data: webpFile(vp8x, vp8l)[:25], msg: cut, is: io.ErrUnexpectedEOF},
		{name: "WebP VP8X cut after the chunk", data: webpFile(vp8x, vp8l)[:30], msg: cut, is: io.ErrUnexpectedEOF},
		{
			name: "WebP VP8X chunk too short",
			data: webpFile(riffChunk("VP8X", vp8xData(0, 3, 2)[:9]), vp8l),
			msg:  `invalid WebP: "VP8X" chunk of 9 bytes is too short`,
		},
		{
			name: "WebP VP8X without an image chunk",
			data: webpFile(
				riffChunk("VP8X", vp8xData(vp8xICC|vp8xEXIF|vp8xXMP, 3, 2)),
				riffChunk("ICCP", []byte("not an ICC profile")),
				riffChunk("EXIF", []byte("not Exif data")),
				riffChunk("XMP ", []byte("<x:xmpmeta/>")),
			),
			msg: noImage,
		},
		{
			name: "WebP VP8X with ALPH only",
			data: webpFile(riffChunk("VP8X", vp8xData(vp8xAlpha, 3, 2)), riffChunk("ALPH", []byte("\x00not alpha data"))),
			msg:  noImage,
		},
		{name: "WebP VP8X with ANIM and ANMF in a still image", data: webpFile(vp8x, anim, frame), msg: noImage},
		{name: "WebP VP8X animation without ANIM", data: webpFile(animated, frame), msg: noImage},
		{name: "WebP VP8X animation with VP8L instead of ANIM", data: webpFile(animated, vp8l), msg: noImage},
		{
			name: "WebP VP8X image chunk after the RIFF chunk",
			data: append(webpFile(vp8x, riffChunk("XMP ", []byte("<x:xmpmeta/>"))), vp8l...),
			msg:  noImage,
		},
		{
			name: "WebP VP8X chunk header across the end of the RIFF chunk",
			data: patch(webpFile(vp8x, vp8l), 4, le32(4+18+4)...),
			msg:  noImage,
		},
		{name: "WebP VP8X skipped chunk cut", data: webpFile(vp8x, iccp, vp8l)[:12+18+8+10], msg: cut, is: io.ErrUnexpectedEOF},
		{
			name: "WebP VP8X skipped chunk larger than the RIFF size",
			data: patch(webpFile(vp8x, iccp, vp8l), 4, le32(4+18+8+50)...),
			msg:  `invalid WebP: "ICCP" chunk of 100 bytes exceeds the RIFF size`,
		},
		{
			name: "WebP VP8X skipped chunk of 4 GiB cut",
			data: slices.Concat([]byte("RIFF"), le32(4+18+8+0xffffff00), []byte("WEBP"), vp8x, []byte("ICCP"), le32(0xffffff00), []byte("not an ICC profile")),
			msg:  cut,
			is:   io.ErrUnexpectedEOF,
		},
		{
			name: "WebP VP8X canvas of 4294967296 pixels",
			data: webpFile(riffChunk("VP8X", vp8xData(vp8xAnimation, 65536, 65536)), anim, frame),
			msg:  "invalid WebP: canvas too large (65536x65536)",
		},
		{
			name: "WebP VP8X largest canvas",
			data: webpFile(riffChunk("VP8X", vp8xData(vp8xAnimation, 1<<24, 1<<24)), anim, frame),
			msg:  "invalid WebP: canvas too large (16777216x16777216)",
		},
	}
	for i := range 3 {
		tests = append(tests, errorTest{
			name: fmt.Sprintf("WebP VP8 start code byte %d", i),
			data: patch(vp8, 23+i, vp8[23+i]^0xff),
			msg:  "invalid WebP: missing VP8 start code",
		})
	}
	return tests
}

// allocated returns the bytes of memory that f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestProbeWebPAllocations(t *testing.T) {
	var files []testFile
	for _, tt := range webpProbeTests() {
		files = append(files, testFile{name: tt.name, data: tt.data})
	}
	for _, tt := range webpInvalidTests() {
		files = append(files, testFile{name: tt.name, data: tt.data})
	}
	for _, f := range files {
		t.Run(f.name, func(t *testing.T) {
			if n := allocated(func() { _, _ = Probe(bytes.NewReader(f.data)) }); n > 1<<20 {
				t.Errorf("Probe allocated %d bytes", n)
			}
		})
	}
}

// sizedReader returns n bytes without writing to p, for data that Probe skips.
type sizedReader struct{ n int64 }

func (r *sizedReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.n)
	r.n -= n
	return int(n), nil
}

func TestProbeWebPLargeChunk(t *testing.T) {
	const size = 3 << 30 // more than an int holds on 32-bit platforms
	vp8x := riffChunk("VP8X", vp8xData(0, 3, 2))
	vp8l := riffChunk("VP8L", vp8lStream(vp8lBits(3, 2)))
	riffSize := uint32(int64(4+len(vp8x)+8+len(vp8l)) + size)
	head := slices.Concat([]byte("RIFF"), le32(riffSize), []byte("WEBP"), vp8x, []byte("XYZW"), le32(size))
	tests := []struct {
		name string
		r    io.Reader
		want Info
		is   error
	}{
		{
			name: "whole",
			r:    io.MultiReader(bytes.NewReader(head), &sizedReader{n: size}, bytes.NewReader(vp8l)),
			want: Info{MIMEType: WebP, Width: 3, Height: 2, Orientation: 1},
		},
		{name: "cut", r: io.MultiReader(bytes.NewReader(head), &sizedReader{n: size - 1}), is: io.ErrUnexpectedEOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Info
			var err error
			if n := allocated(func() { got, err = Probe(tt.r) }); n > 1<<20 {
				t.Errorf("Probe allocated %d bytes", n)
			}
			if got != tt.want || !errors.Is(err, tt.is) {
				t.Errorf("Probe() = %+v, %v; want %+v, %v", got, err, tt.want, tt.is)
			}
		})
	}
}
