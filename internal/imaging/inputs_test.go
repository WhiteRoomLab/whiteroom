package imaging

import (
	"bytes"
	"compress/lzw"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The originals that the tests read are made here: JPEG and PNG by the standard library, with segments and chunks
// added, and the rest by hand. WebP files come from ../webp/testdata/vectors.

var (
	le = binary.LittleEndian
	be = binary.BigEndian
)

func bytesOf(t testing.TB, enc func(io.Writer) error) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := enc(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t testing.TB, m image.Image) []byte {
	t.Helper()
	return bytesOf(t, func(w io.Writer) error { return jpeg.Encode(w, m, nil) })
}

func pngOf(t testing.TB, m image.Image) []byte {
	t.Helper()
	return bytesOf(t, func(w io.Writer) error { return png.Encode(w, m) })
}

// jpegSegment returns a JPEG marker segment with data.
func jpegSegment(marker byte, data []byte) []byte {
	return append(be.AppendUint16([]byte{0xff, marker}, uint16(2+len(data))), data...)
}

// insertJPEG returns a copy of file, a JPEG file, with segs after SOI.
func insertJPEG(file []byte, segs ...[]byte) []byte {
	return slices.Concat(file[:2], slices.Concat(segs...), file[2:])
}

// exifTIFF returns little-endian Exif data from the TIFF header with only the orientation o in IFD0.
func exifTIFF(o int) []byte {
	b := le.AppendUint16([]byte("II*\x00\x08\x00\x00\x00"), 1)
	b = le.AppendUint16(le.AppendUint16(b, 0x0112), 3) // Orientation, SHORT
	b = le.AppendUint16(le.AppendUint32(b, 1), uint16(o))
	return le.AppendUint32(append(b, 0, 0), 0)
}

// exifSegment returns an APP1 segment with Exif data of the orientation o.
func exifSegment(o int) []byte {
	return jpegSegment(0xe1, append([]byte("Exif\x00\x00"), exifTIFF(o)...))
}

// iccSegment returns an APP2 segment with part, the part seq of count of an ICC profile.
func iccSegment(seq, count int, part []byte) []byte {
	return jpegSegment(0xe2, slices.Concat([]byte(iccSignature), []byte{byte(seq), byte(count)}, part))
}

// iccSegments returns the APP2 segments of profile split into n parts, in order.
func iccSegments(profile []byte, n int) [][]byte {
	var segs [][]byte
	for i := range n {
		segs = append(segs, iccSegment(i+1, n, profile[len(profile)*i/n:len(profile)*(i+1)/n]))
	}
	return segs
}

// cmykJPEG returns an 8 x 8 JPEG file of 4 components, which image/jpeg cannot write, after an Adobe APP14 segment
// with transform: 0 for CMYK and 2 for YCCK. Every block has only a DC coefficient of 0.
func cmykJPEG(transform byte) []byte {
	sof := []byte{8, 0, 8, 0, 8, 4} // precision, height, width, components
	sos := []byte{4}
	for c := range byte(4) {
		sof = append(sof, c+1, 0x11, 0) // ID, no subsampling, quantization table 0
		sos = append(sos, c+1, 0x00)    // ID, Huffman tables 0
	}
	sos = append(sos, 0, 63, 0)
	// Tables of one code each, "0": the DC difference 0 and the AC end of block.
	lengths := append([]byte{1}, make([]byte, 15)...)
	dht := slices.Concat([]byte{0x00}, lengths, []byte{0}, []byte{0x10}, lengths, []byte{0})
	dqt := append([]byte{0}, bytes.Repeat([]byte{1}, 64)...)
	adobe := append([]byte("Adobe\x00\x64\x00\x00\x00\x00"), transform)
	return slices.Concat(
		[]byte{0xff, 0xd8},
		jpegSegment(0xee, adobe), jpegSegment(0xdb, dqt), jpegSegment(0xc0, sof), jpegSegment(0xc4, dht),
		jpegSegment(0xda, sos),
		[]byte{0x00}, // 2 bits for each of the 4 blocks
		[]byte{0xff, 0xd9},
	)
}

// insertPNG returns a copy of file, a PNG file, with chunks after IHDR.
func insertPNG(file []byte, chunks ...[]byte) []byte {
	return slices.Concat(file[:pngIHDREnd], slices.Concat(chunks...), file[pngIHDREnd:])
}

// iccpChunk returns an iCCP chunk with profile.
func iccpChunk(t testing.TB, profile []byte) []byte {
	t.Helper()
	return pngChunk("iCCP", append([]byte("test\x00\x00"), zlibOf(t, profile)...))
}

func zlibOf(t testing.TB, data []byte) []byte {
	t.Helper()
	return bytesOf(t, func(w io.Writer) error {
		zw := zlib.NewWriter(w)
		if _, err := zw.Write(data); err != nil {
			return err
		}
		return zw.Close()
	})
}

// pngFile returns a PNG file of chunks after IHDR of w x h pixels with bitDepth and colorType.
func pngFile(w, h int, bitDepth, colorType byte, chunks ...[]byte) []byte {
	ihdr := be.AppendUint32(be.AppendUint32(nil, uint32(w)), uint32(h))
	ihdr = append(ihdr, bitDepth, colorType, 0, 0, 0)
	return slices.Concat([]byte(pngSignature), pngChunk("IHDR", ihdr), slices.Concat(chunks...))
}

// grayAlphaPNG returns a PNG file of w x h pixels of 8-bit gray with alpha, which image/png cannot write, with chunks
// after IHDR.
func grayAlphaPNG(t testing.TB, w, h int, chunks ...[]byte) []byte {
	t.Helper()
	var raw []byte
	for y := range h {
		raw = append(raw, 0) // no filter
		for x := range w {
			raw = append(raw, byte(40*x+20*y), byte(255-30*x))
		}
	}
	return pngFile(w, h, 8, 4, slices.Concat(slices.Concat(chunks...), pngChunk("IDAT", zlibOf(t, raw)),
		pngChunk("IEND", nil)))
}

type pngChunkData struct {
	typ  string
	data []byte
}

// chunksOf returns the chunks of file, a valid PNG file.
func chunksOf(file []byte) []pngChunkData {
	var chunks []pngChunkData
	for b := file[len(pngSignature):]; len(b) >= 12; {
		n := int(be.Uint32(b))
		chunks = append(chunks, pngChunkData{string(b[4:8]), b[8 : 8+n]})
		b = b[12+n:]
	}
	return chunks
}

// apngFile returns an APNG file of 2 frames of the size of m from image/png's file of m: frame 0 is the default image
// and frame 1 has the same data in fdAT. chunks come after acTL.
func apngFile(t testing.TB, m image.Image, chunks ...[]byte) []byte {
	t.Helper()
	b := m.Bounds()
	fcTL := func(seq int) []byte {
		d := be.AppendUint32(nil, uint32(seq))
		d = be.AppendUint32(be.AppendUint32(d, uint32(b.Dx())), uint32(b.Dy()))
		d = be.AppendUint32(be.AppendUint32(d, 0), 0)
		d = be.AppendUint16(be.AppendUint16(d, 1), 10) // a delay of 1/10 s
		return pngChunk("fcTL", append(d, 0, 0))
	}
	out := []byte(pngSignature)
	var idat []byte
	for _, c := range chunksOf(pngOf(t, m)) {
		switch c.typ {
		case "IHDR":
			out = slices.Concat(out, pngChunk(c.typ, c.data), pngChunk("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0}),
				slices.Concat(chunks...))
		case "IDAT":
			if idat == nil {
				out = append(out, fcTL(0)...)
			}
			idat = append(idat, c.data...)
			out = append(out, pngChunk(c.typ, c.data)...)
		case "IEND":
			out = slices.Concat(out, fcTL(1), pngChunk("fdAT", append(be.AppendUint32(nil, 2), idat...)),
				pngChunk(c.typ, c.data))
		default:
			out = append(out, pngChunk(c.typ, c.data)...)
		}
	}
	return out
}

// gifFile returns a GIF89a file of w x h pixels with a global color table of black and white, blocks and the
// trailer.
func gifFile(w, h int, blocks ...[]byte) []byte {
	b := le.AppendUint16(le.AppendUint16([]byte("GIF89a"), uint16(w)), uint16(h))
	b = append(b, 0x80, 0, 0) // a global color table of 2 colors, the background color, no pixel aspect ratio
	b = append(b, 0, 0, 0, 0xff, 0xff, 0xff)
	return append(slices.Concat(b, slices.Concat(blocks...)), 0x3b)
}

// gifImage returns an image descriptor of w x h pixels at (x, y), with a local color table of 2 colors if local, and
// its image data, all of color 1.
func gifImage(t testing.TB, x, y, w, h int, local bool) []byte {
	t.Helper()
	b := le.AppendUint16(le.AppendUint16([]byte{0x2c}, uint16(x)), uint16(y))
	b = le.AppendUint16(le.AppendUint16(b, uint16(w)), uint16(h))
	if local {
		b = append(b, 0x80, 0xff, 0, 0, 0, 0, 0xff)
	} else {
		b = append(b, 0)
	}
	data := bytesOf(t, func(out io.Writer) error {
		lw := lzw.NewWriter(out, lzw.LSB, 2)
		if _, err := lw.Write(bytes.Repeat([]byte{1}, w*h)); err != nil {
			return err
		}
		return lw.Close()
	})
	return append(append(b, 2), gifSubBlocks(data)...)
}

// gifExtension returns an extension block with label and data in sub-blocks.
func gifExtension(label byte, data []byte) []byte {
	return append([]byte{0x21, label}, gifSubBlocks(data)...)
}

// gifControl returns a graphic control extension with a delay of 1/10 s.
func gifControl() []byte {
	return []byte{0x21, 0xf9, 4, 0, 10, 0, 0, 0}
}

// gifSubBlocks returns data in sub-blocks of at most 255 bytes, and the terminator.
func gifSubBlocks(data []byte) []byte {
	var b []byte
	for len(data) > 0 {
		n := min(len(data), 255)
		b = append(append(b, byte(n)), data[:n]...)
		data = data[n:]
	}
	return append(b, 0)
}

// readVector returns the WebP file of the vector with name in ../webp/testdata/vectors.
func readVector(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "webp", "testdata", "vectors", name+".webp"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// riffChunk returns a RIFF chunk, padded to an even size.
func riffChunk(id string, data []byte) []byte {
	b := slices.Concat([]byte(id), le.AppendUint32(nil, uint32(len(data))), data)
	if len(data)%2 != 0 {
		b = append(b, 0)
	}
	return b
}

// webpFile returns a WebP file of chunks.
func webpFile(chunks ...[]byte) []byte {
	data := slices.Concat(chunks...)
	return slices.Concat([]byte("RIFF"), le.AppendUint32(nil, uint32(4+len(data))), []byte("WEBP"), data)
}

// vp8xChunk returns a VP8X chunk with flags and a canvas of w x h pixels.
func vp8xChunk(flags byte, w, h int) []byte {
	d := []byte{flags, 0, 0, 0}
	d = append(d, byte(w-1), byte((w-1)>>8), byte((w-1)>>16))
	d = append(d, byte(h-1), byte((h-1)>>8), byte((h-1)>>16))
	return riffChunk("VP8X", d)
}

// webpChunks returns the chunks of file, a WebP file, after the RIFF header, with their padding.
func webpChunks(file []byte) map[string][]byte {
	chunks := make(map[string][]byte)
	for b := file[12:]; len(b) >= 8; {
		n := int(le.Uint32(b[4:]))
		chunks[string(b[:4])] = b[:8+n+n%2]
		b = b[8+n+n%2:]
	}
	return chunks
}

// The tags of ICC profiles made in the tests.

type profileTag struct {
	sig  string
	data []byte
}

func s15(v float64) uint32 {
	return uint32(int32(math.Round(v * 0x10000)))
}

// iccProfile returns an ICC v4.3 profile of class and space whose tags have their data after the tag table, which
// tags with the same data share.
func iccProfile(class, space string, tags ...profileTag) []byte {
	start := 132 + 12*len(tags)
	table := be.AppendUint32(nil, uint32(len(tags)))
	var data []byte
	offsets := make(map[string]int)
	for _, t := range tags {
		off, ok := offsets[string(t.data)]
		if !ok {
			off = start + len(data)
			offsets[string(t.data)] = off
			data = append(data, t.data...)
			data = append(data, make([]byte, -len(data)&3)...)
		}
		table = be.AppendUint32(be.AppendUint32(append(table, t.sig...), uint32(off)), uint32(len(t.data)))
	}
	h := be.AppendUint32(nil, uint32(start+len(data)))
	h = be.AppendUint32(append(h, "test"...), 0x04300000)
	h = append(append(append(h, class...), space...), "XYZ "...)
	h = append(h, make([]byte, 36-len(h))...)
	h = append(h, "acsp"...)
	h = append(h, make([]byte, 68-len(h))...)
	for _, v := range []float64{0.9642, 1, 0.8249} { // D50
		h = be.AppendUint32(h, s15(v))
	}
	h = append(h, make([]byte, 128-len(h))...)
	return slices.Concat(h, table, data)
}

func xyzTag(xyz [3]float64) []byte {
	b := []byte("XYZ \x00\x00\x00\x00")
	for _, v := range xyz {
		b = be.AppendUint32(b, s15(v))
	}
	return b
}

// curvTag returns a curveType tag of the entries: none for identity, a gamma in u8Fixed8 or a table.
func curvTag(entries ...uint16) []byte {
	b := be.AppendUint32([]byte("curv\x00\x00\x00\x00"), uint32(len(entries)))
	for _, e := range entries {
		b = be.AppendUint16(b, e)
	}
	return b
}

// curvTable returns a curveType tag of n entries of f.
func curvTable(n int, f func(float64) float64) []byte {
	entries := make([]uint16, n)
	for i := range entries {
		entries[i] = uint16(math.Round(f(float64(i)/float64(n-1)) * 0xffff))
	}
	return curvTag(entries...)
}

func paraTag(typ uint16, params ...float64) []byte {
	b := be.AppendUint16([]byte("para\x00\x00\x00\x00"), typ)
	b = append(b, 0, 0)
	for _, p := range params {
		b = be.AppendUint32(b, s15(p))
	}
	return b
}

var (
	srgbPara   = paraTag(3, 2.4, 1/1.055, 0.055/1.055, 1/12.92, 0.04045)
	gamma22    = curvTag(563) // 2.2 in u8Fixed8
	p3Colorant = [3][3]float64{{0.515121, 0.241196, -0.001053}, {0.291977, 0.692245, 0.041885},
		{0.157104, 0.066574, 0.784073}}
	descTag = profileTag{"desc", []byte("mluc\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x0cenUS\x00\x00\x00\x02" +
		"\x00\x00\x00\x1c\x00t")}
)

// rgbProfile returns a display profile of RGB with colorants and the curve trc for red, green and blue, and more tags.
func rgbProfile(colorants [3][3]float64, trc []byte, more ...profileTag) []byte {
	tags := []profileTag{descTag, {"wtpt", xyzTag([3]float64{0.9642, 1, 0.8249})}}
	for i, c := range []string{"r", "g", "b"} {
		tags = append(tags, profileTag{c + "XYZ", xyzTag(colorants[i])})
	}
	tags = append(tags, profileTag{"rTRC", trc}, profileTag{"gTRC", trc}, profileTag{"bTRC", trc})
	return iccProfile("mntr", "RGB ", append(tags, more...)...)
}

// grayProfile returns a display profile of GRAY with the curve trc.
func grayProfile(trc []byte) []byte {
	return iccProfile("mntr", "GRAY", descTag, profileTag{"wtpt", xyzTag([3]float64{0.9642, 1, 0.8249})},
		profileTag{"kTRC", trc})
}

// A profile of each decision.
var (
	srgbProfile = rgbProfile(srgbColorants, srgbPara)
	p3Profile   = rgbProfile(p3Colorant, srgbPara)
	// cmykProfile is an output profile of CMYK, whose transforms do not matter here.
	cmykProfile = iccProfile("prtr", "CMYK", descTag, profileTag{"A2B0", []byte("mft2\x00\x00\x00\x00")})
)

// colored returns an RGBA image of w x h pixels with colors that change across it.
func colored(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.SetRGBA(x, y, color.RGBA{uint8(255 * x / max(w-1, 1)), uint8(255 * y / max(h-1, 1)), 128, 255})
		}
	}
	return m
}
