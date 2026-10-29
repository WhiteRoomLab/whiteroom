package mediafile

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"slices"
	"testing"
	"testing/iotest"
)

var (
	le = binary.LittleEndian
	be = binary.BigEndian
)

// ifdEntry returns an IFD entry of count values of type typ, with value in the first 2 bytes of the value field.
func ifdEntry(order binary.AppendByteOrder, tag, typ uint16, count uint32, value uint16) []byte {
	b := order.AppendUint16(nil, tag)
	b = order.AppendUint16(b, typ)
	b = order.AppendUint32(b, count)
	b = order.AppendUint16(b, value)
	return append(b, 0, 0)
}

func orientationEntry(order binary.AppendByteOrder, o uint16) []byte {
	return ifdEntry(order, 0x0112, 3, 1, o)
}

// tiffData returns Exif data from the TIFF header in order, with IFD0 of entries at off, after zeros.
func tiffData(order binary.AppendByteOrder, off int, entries ...[]byte) []byte {
	b := []byte("II*\x00")
	if order == be {
		b = []byte("MM\x00*")
	}
	b = order.AppendUint32(b, uint32(off))
	b = append(b, make([]byte, off-len(b))...)
	b = order.AppendUint16(b, uint16(len(entries)))
	b = append(b, slices.Concat(entries...)...)
	return order.AppendUint32(b, 0) // no next IFD
}

// exifData returns Exif data with the orientation o between other entries in IFD0, as cameras write it.
func exifData(order binary.AppendByteOrder, o uint16) []byte {
	return tiffData(order, 8, ifdEntry(order, 0x0100, 3, 1, 3), orientationEntry(order, o), ifdEntry(order, 0x0128, 3, 1, 2))
}

// markerSegment returns a JPEG marker segment with data.
func markerSegment(marker byte, data []byte) []byte {
	b := binary.BigEndian.AppendUint16([]byte{0xff, marker}, uint16(2+len(data)))
	return append(b, data...)
}

// exifSegment returns an APP1 segment with Exif data.
func exifSegment(tiff []byte) []byte {
	return markerSegment(0xe1, append([]byte(exifHeader), tiff...))
}

// comSegments returns COM segments of n bytes in all, for n of 4 or more.
func comSegments(n int) []byte {
	var b []byte
	for n > 0 {
		k := min(n, 4+0xfffd) // the marker, the length and up to 65533 bytes of data
		if r := n - k; r > 0 && r < 4 {
			k -= 4
		}
		b = append(b, markerSegment(0xfe, make([]byte, k-4))...)
		n -= k
	}
	return b
}

// insert returns a copy of data with b inserted at off.
func insert(data []byte, off int, b ...[]byte) []byte {
	return slices.Concat(data[:off], slices.Concat(b...), data[off:])
}

// segmentEnd returns the end of the segment at off in data, a JPEG file.
func segmentEnd(data []byte, off int) int {
	return off + 2 + int(binary.BigEndian.Uint16(data[off+2:]))
}

const pngIHDREnd = 8 + 12 + 13 // the signature and IHDR

func exifProbeTests(t testing.TB) []probeTest {
	jpegRGBA := encodeJPEG(t, image.NewRGBA(rect))
	dqtEnd := segmentEnd(jpegRGBA, jpegSegment(t, jpegRGBA, 0xdb))
	sofEnd := segmentEnd(jpegRGBA, jpegSegment(t, jpegRGBA, 0xc0))
	jfif := markerSegment(0xe0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	xmp := markerSegment(0xe1, []byte(`http://ns.adobe.com/xap/1.0/`+"\x00"+`<x:xmpmeta xmlns:x="adobe:ns:meta/" tiff:Orientation="3"/>`))
	icc := markerSegment(0xe2, []byte("ICC_PROFILE\x00\x01\x01not an ICC profile"))
	pngRGBA := encodePNG(t, image.NewRGBA(rect))
	pngIEND := len(pngRGBA) - 12
	eXIf := func(order binary.AppendByteOrder, o uint16) []byte { return pngChunk("eXIf", exifData(order, o)) }
	gif89a := encodeGIF(t, image.NewPaletted(rect, palette))
	comment := append([]byte(exifHeader), exifData(le, 6)...)
	gifComment := slices.Concat([]byte{0x21, 0xfe, byte(len(comment))}, comment, []byte{0}) // an extension of 1 sub-block

	img := func(mimeType string, w, h, o int) Info {
		return Info{MIMEType: mimeType, Width: w, Height: h, Orientation: o}
	}
	var tests []probeTest
	for _, tt := range []struct{ o, w, h int }{{1, 3, 2}, {2, 3, 2}, {3, 3, 2}, {4, 3, 2}, {5, 2, 3}, {6, 2, 3}, {7, 2, 3}, {8, 2, 3}} {
		tests = append(tests, probeTest{
			name: fmt.Sprintf("JPEG Exif orientation %d", tt.o),
			data: insert(jpegRGBA, 2, exifSegment(exifData(le, uint16(tt.o)))),
			want: img(JPEG, tt.w, tt.h, tt.o),
		})
	}
	return append(tests, []probeTest{
		{name: "JPEG Exif big endian", data: insert(jpegRGBA, 2, exifSegment(exifData(be, 6))), want: img(JPEG, 2, 3, 6)},
		{name: "JPEG Exif after JFIF", data: insert(jpegRGBA, 2, jfif, exifSegment(exifData(le, 6))), want: img(JPEG, 2, 3, 6)},
		{name: "JPEG Exif after XMP", data: insert(jpegRGBA, 2, xmp, exifSegment(exifData(le, 8))), want: img(JPEG, 2, 3, 8)},
		{
			name: "JPEG Exif after COM and APP2",
			data: insert(jpegRGBA, 2, markerSegment(0xfe, []byte("comment")), icc, exifSegment(exifData(le, 6))),
			want: img(JPEG, 2, 3, 6),
		},
		{name: "JPEG Exif after DQT", data: insert(jpegRGBA, dqtEnd, exifSegment(exifData(le, 6))), want: img(JPEG, 2, 3, 6)},
		{
			name: "JPEG Exif after fill bytes",
			data: insert(jpegRGBA, 2, []byte{0xff, 0xff}, exifSegment(exifData(le, 6))),
			want: img(JPEG, 2, 3, 6),
		},
		{name: "JPEG Exif after SOF", data: insert(jpegRGBA, sofEnd, exifSegment(exifData(le, 6))), want: img(JPEG, 3, 2, 1)},
		{
			name: "JPEG second Exif",
			data: insert(jpegRGBA, 2, exifSegment(exifData(le, 3)), exifSegment(exifData(le, 6))),
			want: img(JPEG, 3, 2, 3),
		},
		{
			name: "JPEG second Exif after one without Orientation",
			data: insert(jpegRGBA, 2, exifSegment(tiffData(le, 8, ifdEntry(le, 0x0100, 3, 1, 3))), exifSegment(exifData(le, 6))),
			want: img(JPEG, 3, 2, 1),
		},
		{
			name: "JPEG Exif ID code with a padding byte that is not 0",
			data: insert(jpegRGBA, 2, markerSegment(0xe1, append([]byte("Exif\x00\xff"), exifData(le, 6)...))),
			want: img(JPEG, 3, 2, 1),
		},
		{
			name: "JPEG Exif in APP2",
			data: insert(jpegRGBA, 2, markerSegment(0xe2, append([]byte(exifHeader), exifData(le, 6)...))),
			want: img(JPEG, 3, 2, 1),
		},
		{name: "PNG eXIf", data: insert(pngRGBA, pngIHDREnd, eXIf(le, 6)), want: img(PNG, 2, 3, 6)},
		{
			name: "PNG eXIf big endian after gAMA and pHYs",
			data: insert(pngRGBA, pngIHDREnd,
				pngChunk("gAMA", []byte{0, 0, 0xb1, 0x8f}),
				pngChunk("pHYs", []byte{0, 0, 0x0e, 0xc4, 0, 0, 0x0e, 0xc4, 1}),
				eXIf(be, 8),
			),
			want: img(PNG, 2, 3, 8),
		},
		{
			// image/png reads the chunks up to IDAT of paletted images, which checks the CRC of eXIf.
			name: "PNG paletted with eXIf before PLTE",
			data: insert(encodePNG(t, image.NewPaletted(rect, palette)), pngIHDREnd, eXIf(le, 7)),
			want: img(PNG, 2, 3, 7),
		},
		{name: "PNG eXIf after IDAT", data: insert(pngRGBA, pngIEND, eXIf(le, 6)), want: img(PNG, 3, 2, 1)},
		{name: "PNG second eXIf", data: insert(pngRGBA, pngIHDREnd, eXIf(le, 3), eXIf(le, 6)), want: img(PNG, 3, 2, 3)},
		{
			name: "PNG eXIf with the Exif ID code",
			data: insert(pngRGBA, pngIHDREnd, pngChunk("eXIf", append([]byte(exifHeader), exifData(le, 6)...))),
			want: img(PNG, 3, 2, 1),
		},
		{name: "GIF with Exif in a comment", data: insert(gif89a, 13+3*len(palette), gifComment), want: img(GIF, 3, 2, 1)},
		{
			name: "PDF with a JPEG with Exif",
			data: slices.Concat(
				[]byte("%PDF-1.7\n1 0 obj\n<< /Type /XObject /Subtype /Image /Filter /DCTDecode >>\nstream\n"),
				insert(jpegRGBA, 2, exifSegment(exifData(le, 6))),
				[]byte("\nendstream\nendobj\n"),
			),
			want: Info{MIMEType: PDF},
		},
	}...)
}

func TestOrientation(t *testing.T) {
	type test struct {
		name string
		tiff []byte
		want int
	}
	var tests []test
	for o := range 8 {
		tests = append(tests,
			test{fmt.Sprintf("little endian %d", o+1), exifData(le, uint16(o+1)), o + 1},
			test{fmt.Sprintf("big endian %d", o+1), exifData(be, uint16(o+1)), o + 1},
		)
	}
	// IFD0 at 7 starts in the header: from there, the count is 256 and the first entry is Orientation 6.
	overlap := slices.Concat([]byte("II*\x00"), le32(7), []byte{1}, orientationEntry(le, 6))
	// IFD0 without Orientation links to IFD1 with Orientation 6.
	ifd1 := slices.Concat(patch(tiffData(le, 8, ifdEntry(le, 0x0100, 3, 1, 3)), 22, 26), []byte{1, 0}, orientationEntry(le, 6), le32(0))
	tests = append(tests, []test{
		{"little endian 0", exifData(le, 0), 1},
		{"little endian 9", exifData(le, 9), 1},
		{"little endian 65535", exifData(le, 0xffff), 1},
		{"big endian 0", exifData(be, 0), 1},
		{"big endian 9", exifData(be, 9), 1},
		{"big endian 256", exifData(be, 0x100), 1},
		{"LONG", tiffData(le, 8, ifdEntry(le, 0x0112, 4, 1, 6)), 1},
		{"SSHORT", tiffData(le, 8, ifdEntry(le, 0x0112, 8, 1, 6)), 1},
		{"2 values", tiffData(le, 8, ifdEntry(le, 0x0112, 3, 2, 6)), 1},
		{"no values", tiffData(le, 8, ifdEntry(le, 0x0112, 3, 0, 6)), 1},
		{"value in the last 2 bytes", tiffData(be, 8, slices.Concat(ifdEntry(be, 0x0112, 3, 1, 0)[:8], []byte{0, 0, 0, 6})), 1},
		{"LONG before SHORT", tiffData(le, 8, ifdEntry(le, 0x0112, 4, 1, 6), orientationEntry(le, 8)), 1},
		{"only entry", tiffData(le, 8, orientationEntry(le, 6)), 6},
		{"no Orientation", tiffData(le, 8, ifdEntry(le, 0x0100, 3, 1, 6)), 1},
		{"no entries", tiffData(le, 8), 1},
		{"Orientation in IFD1", ifd1, 1},
		{"IFD0 after other data", tiffData(be, 100, orientationEntry(be, 6)), 6},
		{"IFD0 at 7", overlap, 1},
		{"IFD0 at 0", patch(exifData(le, 6), 4, 0, 0, 0, 0), 1},
		{"IFD0 at the last byte", patch(exifData(le, 6), 4, le32(49)...), 1},
		{"IFD0 at the end", patch(exifData(le, 6), 4, le32(50)...), 1},
		{"IFD0 at 4294967295", patch(exifData(be, 6), 4, 0xff, 0xff, 0xff, 0xff), 1},
		{"more entries than fit", patch(tiffData(le, 8, orientationEntry(le, 6)), 8, 0xff, 0xff), 6},
		{"more entries than fit without Orientation", patch(tiffData(le, 8, ifdEntry(le, 0x0100, 3, 1, 6)), 8, 0xff, 0xff), 1},
		{"Orientation after the counted entries", patch(exifData(le, 6), 8, 1, 0), 1},
		{"Exif ID code before the header", append([]byte(exifHeader), exifData(le, 6)...), 1},
		{"little endian 42 after MM", patch(exifData(le, 6), 0, 'M', 'M'), 1},
		{"big endian 42 after II", patch(exifData(be, 6), 0, 'I', 'I'), 1},
		{"IM", patch(exifData(le, 6), 1, 'M'), 1},
		{"BigTIFF", patch(exifData(le, 6), 2, 43), 1},
		{"header only", exifData(le, 6)[:8], 1},
		{"empty", []byte{}, 1},
		{"nil", nil, 1},
	}...)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := orientation(tt.tiff); got != tt.want {
				t.Errorf("orientation(% x) = %d, want %d", tt.tiff, got, tt.want)
			}
		})
	}

	t.Run("cut", func(t *testing.T) {
		for _, order := range []binary.AppendByteOrder{le, be} {
			data := exifData(order, 6)
			const end = 8 + 2 + 2*12 // the end of the Orientation entry
			for n := range len(data) {
				want := 1
				if n >= end {
					want = 6
				}
				if got := orientation(data[:n]); got != want {
					t.Errorf("orientation(% x) = %d, want %d", data[:n], got, want)
				}
			}
		}
	})
}

func TestJPEGOrientation(t *testing.T) {
	exif := exifSegment(exifData(le, 6))
	soi := []byte{0xff, 0xd8}
	tests := []struct {
		name string
		data []byte
		want int
	}{
		{name: "Exif", data: slices.Concat(soi, exif), want: 6},
		{name: "Exif after DHT and DRI", data: slices.Concat(soi, markerSegment(0xc4, []byte("not a table")), markerSegment(0xdd, []byte{0, 1}), exif), want: 6},
		{name: "Exif cut after Orientation", data: slices.Concat(soi, exif)[:2+4+6+8+2+2*12], want: 6},
		{name: "Exif cut in Orientation", data: slices.Concat(soi, exif)[:2+4+6+8+2+2*12-1], want: 1},
		{name: "Exif ID code cut", data: slices.Concat(soi, exif)[:2+4+5], want: 1},
		{name: "SOI only", data: soi, want: 1},
		{name: "empty", data: nil, want: 1},
		{name: "segment header cut", data: slices.Concat(soi, exif[:3]), want: 1},
		{name: "APP1 of length 0", data: slices.Concat(soi, []byte{0xff, 0xe1, 0, 0}, exif), want: 1},
		{name: "APP1 of length 1", data: slices.Concat(soi, []byte{0xff, 0xe1, 0, 1}, exif), want: 1},
		{name: "APP1 of length 2", data: slices.Concat(soi, []byte{0xff, 0xe1, 0, 2}, exif), want: 6},
		{name: "COM longer than the data", data: slices.Concat(soi, []byte{0xff, 0xfe, 0xff, 0xff}, exif), want: 1},
		{name: "Exif after data that is not a marker", data: slices.Concat(soi, []byte{0x00}, exif), want: 1},
		{name: "Exif after FF00", data: slices.Concat(soi, []byte{0xff, 0x00}, exif), want: 1},
		{name: "Exif after RST0", data: slices.Concat(soi, []byte{0xff, 0xd0}, exif), want: 1},
		{name: "Exif after SOI", data: slices.Concat(soi, soi, exif), want: 1},
		{name: "Exif after SOS", data: slices.Concat(soi, markerSegment(0xda, []byte("scan")), exif), want: 1},
		{name: "Exif after EOI", data: slices.Concat(soi, []byte{0xff, 0xd9}, exif), want: 1},
		{name: "Exif after SOF2", data: slices.Concat(soi, markerSegment(0xc2, []byte("frame")), exif), want: 1},
		{name: "Exif after SOF9", data: slices.Concat(soi, markerSegment(0xc9, []byte("frame")), exif), want: 1},
		{name: "Exif after DAC", data: slices.Concat(soi, markerSegment(0xcc, []byte{0, 0}), exif), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jpegOrientation(tt.data); got != tt.want {
				t.Errorf("jpegOrientation(% x) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestPNGOrientation(t *testing.T) {
	head := pngHeader(3, 2)
	exif := pngChunk("eXIf", exifData(le, 6))
	huge := patch(pngChunk("zzZz", []byte("data")), 0, 0xff, 0xff, 0xff, 0xff)
	tests := []struct {
		name string
		data []byte
		want int
	}{
		{name: "eXIf", data: slices.Concat(head, exif), want: 6},
		{name: "eXIf cut after Orientation", data: slices.Concat(head, exif)[:pngIHDREnd+8+8+2+2*12], want: 6},
		{name: "eXIf cut in Orientation", data: slices.Concat(head, exif)[:pngIHDREnd+8+8+2+2*12-1], want: 1},
		{name: "eXIf longer than the data", data: slices.Concat(head, patch(exif, 0, 0x7f, 0xff, 0xff, 0xff)), want: 6},
		{
			// The CRC after the data would complete the Orientation entry.
			name: "eXIf shorter than its Orientation entry",
			data: slices.Concat(head, binary.BigEndian.AppendUint32(nil, 30), []byte("eXIf"), exifData(le, 6)[:30], []byte{6, 0, 0, 0}),
			want: 1,
		},
		{name: "eXIf after IDAT", data: slices.Concat(head, pngChunk("IDAT", nil), exif), want: 1},
		{name: "eXIf after a chunk of 4 GiB", data: slices.Concat(head, huge, exif), want: 1},
		{
			// 12 + the length is negative as an int on 32-bit platforms.
			name: "eXIf after a chunk of 4 GiB - 1012 bytes",
			data: slices.Concat(head, patch(huge, 0, binary.BigEndian.AppendUint32(nil, 1<<32-1012)...), exif),
			want: 1,
		},
		{name: "eXIf after a chunk longer than the data", data: slices.Concat(head, patch(pngChunk("zzZz", nil), 3, 100), exif), want: 1},
		{name: "eXIf after a chunk without CRC", data: slices.Concat(head, pngChunk("zzZz", nil)[:8], exif), want: 1},
		{name: "EXIF, which is not eXIf", data: slices.Concat(head, pngChunk("EXIF", exifData(le, 6))), want: 1},
		{name: "signature only", data: head[:8], want: 1},
		{name: "signature cut", data: head[:7], want: 1},
		{name: "chunk header cut", data: slices.Concat(head, exif[:7]), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pngOrientation(tt.data); got != tt.want {
				t.Errorf("pngOrientation(% x) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestProbeOrientationLimit(t *testing.T) {
	const limit = 128 << 10
	entry := tiffData(le, 8, orientationEntry(le, 6))
	jpegRGBA := encodeJPEG(t, image.NewRGBA(rect))
	pngRGBA := encodePNG(t, image.NewRGBA(rect))
	// The files with the end of the Orientation entry at byte end, after COM segments or a tEXt chunk.
	jpegAt := func(end int) []byte {
		return insert(jpegRGBA, 2, comSegments(end-2-(4+6+8+2+12)), exifSegment(entry))
	}
	pngAt := func(end int) []byte {
		text := append([]byte("Comment\x00"), bytes.Repeat([]byte("x"), end-pngIHDREnd-12-8-(8+8+2+12))...)
		return insert(pngRGBA, pngIHDREnd, pngChunk("tEXt", text), pngChunk("eXIf", entry))
	}
	// The WebP file with the end of the Orientation entry at byte end of the EXIF chunk, which is longer.
	webpAt := func(end int) []byte {
		return webpFile(
			riffChunk("VP8X", vp8xData(vp8xEXIF, 3, 2)),
			riffChunk("VP8L", vp8lStream(vp8lBits(3, 2))),
			riffChunk("EXIF", tiffData(le, end-2-12, orientationEntry(le, 6))),
		)
	}
	tests := []struct {
		name string
		data []byte
		want Info
	}{
		{name: "JPEG Orientation to 128 KiB", data: jpegAt(limit), want: Info{MIMEType: JPEG, Width: 2, Height: 3, Orientation: 6}},
		{name: "JPEG Orientation to 128 KiB + 1", data: jpegAt(limit + 1), want: Info{MIMEType: JPEG, Width: 3, Height: 2, Orientation: 1}},
		{name: "JPEG Exif after 128 KiB", data: jpegAt(limit + 4 + 6 + 8 + 2 + 12), want: Info{MIMEType: JPEG, Width: 3, Height: 2, Orientation: 1}},
		{name: "PNG Orientation to 128 KiB", data: pngAt(limit), want: Info{MIMEType: PNG, Width: 2, Height: 3, Orientation: 6}},
		{name: "PNG Orientation to 128 KiB + 1", data: pngAt(limit + 1), want: Info{MIMEType: PNG, Width: 3, Height: 2, Orientation: 1}},
		{name: "PNG eXIf after 128 KiB", data: pngAt(limit + 8 + 8 + 2 + 12), want: Info{MIMEType: PNG, Width: 3, Height: 2, Orientation: 1}},
		{name: "WebP Orientation to 64 KiB of EXIF", data: webpAt(64 << 10), want: Info{MIMEType: WebP, Width: 2, Height: 3, Orientation: 6}},
		{name: "WebP Orientation to 64 KiB + 1 of EXIF", data: webpAt(64<<10 + 1), want: Info{MIMEType: WebP, Width: 3, Height: 2, Orientation: 1}},
	}
	for _, tt := range tests {
		for _, r := range []struct {
			name string
			r    io.Reader
		}{
			{name: "Reader", r: bytes.NewReader(tt.data)},
			{name: "OneByteReader", r: iotest.OneByteReader(bytes.NewReader(tt.data))},
		} {
			t.Run(tt.name+"/"+r.name, func(t *testing.T) {
				if got, err := Probe(r.r); err != nil || got != tt.want {
					t.Errorf("Probe() = %+v, %v; want %+v", got, err, tt.want)
				}
			})
		}
	}
}
