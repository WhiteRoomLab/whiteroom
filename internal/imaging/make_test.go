package imaging

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"math"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/webp"
)

// makeOf inspects data and returns its Info and the files of vs, which checkFile checks with the profile.
func makeOf(t *testing.T, data []byte, profile []byte, vs ...Variant) (Info, [][]byte) {
	t.Helper()
	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	files, err := makeFiles(t.Context(), data, info, vs)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(vs) {
		t.Fatalf("%d files of %d variants", len(files), len(vs))
	}
	for i, f := range files {
		checkFile(t, f, vs[i], profile)
	}
	return info, files
}

// checkFile checks that file is a file of v, with only the image, its animation and its colors in it: the ICC profile
// if it is not nil, once and as it is.
func checkFile(t *testing.T, file []byte, v Variant, profile []byte) {
	t.Helper()
	var (
		c   image.Config
		err error
	)
	switch v.Format {
	case mediafile.JPEG:
		markers, app2 := jpegMarkersOf(t, file)
		want := []byte{0xd8, 0xdb, 0xc0, 0xc4, 0xda, 0xd9}
		if profile != nil {
			want = slices.Insert(want, 1, slices.Repeat([]byte{0xe2}, len(app2))...)
			if len(app2) == 0 {
				t.Errorf("%+v: no APP2 segment", v)
			}
		}
		if !bytes.Equal(markers, want) {
			t.Errorf("%+v: JPEG markers % x, want % x", v, markers, want)
		}
		checkProfile(t, v, jpegProfile(t, app2), profile)
		c, err = jpeg.DecodeConfig(bytes.NewReader(file))
		if err == nil {
			_, err = jpeg.Decode(bytes.NewReader(file))
		}
	case mediafile.PNG:
		allowed := []string{"IHDR", "PLTE", "tRNS", "IDAT", "IEND"}
		if v.Animated {
			allowed = apngChunks
		} else if profile != nil {
			allowed = append(allowed, "iCCP")
		}
		var iccp [][]byte
		for _, ch := range chunksOf(file) {
			if !slices.Contains(allowed, ch.typ) {
				t.Errorf("%+v: PNG chunk %q", v, ch.typ)
			}
			if ch.typ == "iCCP" {
				iccp = append(iccp, ch.data)
			}
		}
		if !v.Animated {
			var p []byte
			if len(iccp) > 0 {
				if p, err = pngICC(iccp[0]); err != nil {
					t.Fatal(err)
				}
			}
			if len(iccp) > 1 {
				t.Errorf("%+v: %d iCCP chunks", v, len(iccp))
			}
			checkProfile(t, v, p, profile)
		}
		c, err = png.DecodeConfig(bytes.NewReader(file))
		if err == nil {
			_, err = png.Decode(bytes.NewReader(file))
		}
	case mediafile.GIF:
		for _, name := range gifBlockNames(t, file) {
			if name != "image" && name != "control" && name[:4] != "loop" {
				t.Errorf("%+v: GIF block %s", v, name)
			}
		}
		c, err = gif.DecodeConfig(bytes.NewReader(file))
	case mediafile.WebP:
		var f *webp.File
		if f, err = webp.Parse(file); err == nil {
			c = image.Config{Width: f.Width, Height: f.Height}
			if f.EXIF != nil || f.XMP != nil {
				t.Errorf("%+v: WebP with EXIF or XMP", v)
			}
		}
		for b := file[12:]; len(b) >= 8; {
			id := string(b[:4])
			if !slices.Contains([]string{"VP8X", "ICCP", "ANIM", "ANMF", "ALPH", "VP8 ", "VP8L"}, id) {
				t.Errorf("%+v: WebP chunk %q", v, id)
			}
			n := int(le.Uint32(b[4:]))
			if id == "ANMF" {
				n = 16 // the chunks in the frame come next
			}
			b = b[min(len(b), 8+n+n%2):]
		}
	default:
		t.Fatalf("variant %+v", v)
	}
	if err != nil {
		t.Fatalf("%+v: %v", v, err)
	}
	if c.Width != v.Width || c.Height != v.Height {
		t.Errorf("%+v: file of %dx%d pixels", v, c.Width, c.Height)
	}
}

func checkProfile(t *testing.T, v Variant, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) || (got == nil) != (want == nil) {
		t.Errorf("%+v: profile of %d bytes, want %d bytes", v, len(got), len(want))
	}
}

// jpegMarkersOf returns the markers of file, a JPEG file of one scan as image/jpeg writes it, and the data of its APP2
// segments.
func jpegMarkersOf(t *testing.T, file []byte) (markers []byte, app2 [][]byte) {
	t.Helper()
	if !bytes.HasPrefix(file, []byte{0xff, 0xd8}) {
		t.Fatalf("JPEG file starting with % x", file[:min(len(file), 2)])
	}
	markers = []byte{0xd8}
	for i := 2; ; {
		if i+2 > len(file) || file[i] != 0xff {
			t.Fatalf("no marker at %d of %d bytes", i, len(file))
		}
		m := file[i+1]
		markers = append(markers, m)
		if m == 0xd9 {
			if i+2 != len(file) {
				t.Errorf("%d bytes after EOI", len(file)-i-2)
			}
			return markers, app2
		}
		n := int(be.Uint16(file[i+2:]))
		if m == 0xe2 {
			app2 = append(app2, file[i+4:i+2+n])
		}
		i += 2 + n
		if m == 0xda {
			// The entropy-coded data, up to a marker that is not RSTn.
			for i+1 < len(file) && (file[i] != 0xff || file[i+1] == 0 || 0xd0 <= file[i+1] && file[i+1] <= 0xd7) {
				i++
			}
		}
	}
}

// jpegProfile returns the ICC profile in the APP2 segments; nil if there are none.
func jpegProfile(t *testing.T, app2 [][]byte) []byte {
	t.Helper()
	var segs [][]byte
	for _, s := range app2 {
		if !bytes.HasPrefix(s, []byte(iccSignature)) {
			t.Errorf("APP2 segment %q", s[:min(len(s), 16)])
			continue
		}
		segs = append(segs, s[len(iccSignature):])
	}
	if segs == nil {
		return nil
	}
	p, err := joinICCSegments(segs)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// decodeFile decodes file, a JPEG, PNG or GIF file.
func decodeFile(t *testing.T, file []byte) image.Image {
	t.Helper()
	m, _, err := image.Decode(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func jpegQ(t testing.TB, m image.Image, quality int) []byte {
	t.Helper()
	return bytesOf(t, func(w io.Writer) error { return jpeg.Encode(w, m, &jpeg.Options{Quality: quality}) })
}

// blockColors are colors far from each other.
var blockColors = []color.RGBA{
	{230, 25, 75, 255}, {60, 180, 75, 255}, {255, 225, 25, 255}, {0, 130, 200, 255}, {245, 130, 48, 255},
	{145, 30, 180, 255}, {70, 240, 240, 255}, {240, 50, 230, 255}, {210, 245, 60, 255}, {250, 190, 212, 255},
	{0, 128, 128, 255}, {220, 190, 255, 255}, {170, 110, 40, 255}, {255, 250, 200, 255}, {128, 0, 0, 255},
	{170, 255, 195, 255},
}

// blocksImage returns an image of w x h pixels in blocks of 16 x 16 pixels of different colors, so that it differs
// from itself in each orientation, and JPEG keeps the colors of the blocks.
func blocksImage(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.SetRGBA(x, y, blockColors[(y/16*((w+15)/16)+x/16)%len(blockColors)])
		}
	}
	return m
}

// exifSides are, for each EXIF orientation, the sides of the image as displayed that the first row and the first
// column of the image as stored are, as the tag Orientation of CIPA DC-008 defines them.
var exifSides = [9][2]string{
	1: {"top", "left"}, 2: {"top", "right"}, 3: {"bottom", "right"}, 4: {"bottom", "left"},
	5: {"left", "top"}, 6: {"right", "top"}, 7: {"right", "bottom"}, 8: {"left", "bottom"},
}

// storedPoint returns the pixel of the image as stored that shows as the pixel (x, y) of the w x h image as displayed
// with the orientation o.
func storedPoint(o, x, y, w, h int) (int, int) {
	dist := func(side string) int {
		switch side {
		case "top":
			return y
		case "bottom":
			return h - 1 - y
		case "left":
			return x
		}
		return w - 1 - x
	}
	return dist(exifSides[o][1]), dist(exifSides[o][0])
}

// storedImage returns the image to store with the orientation o so that it shows as d.
func storedImage(d *image.RGBA, o int) *image.RGBA {
	w, h := d.Rect.Dx(), d.Rect.Dy()
	sw, sh := stored(w, h, o)
	s := image.NewRGBA(image.Rect(0, 0, sw, sh))
	for y := range h {
		for x := range w {
			sx, sy := storedPoint(o, x, y, w, h)
			s.SetRGBA(sx, sy, d.RGBAAt(x, y))
		}
	}
	return s
}

// displayedImage returns s, an image stored with the orientation o, as displayed.
func displayedImage(s image.Image, o int) *image.NRGBA {
	b := s.Bounds()
	w, h := stored(b.Dx(), b.Dy(), o)
	d := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			sx, sy := storedPoint(o, x, y, w, h)
			d.Set(x, y, s.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return d
}

// blockErrors returns the differences of the samples of m, the file of a variant of blocksImage(w, h) as displayed,
// from the colors of the blocks, for the pixels whose filter reads only pixels of one block.
func blockErrors(m image.Image, w, h int) []int {
	display := blocksImage(w, h)
	b := m.Bounds()
	x0, cw, y0, ch := cropRect(w, h, b.Dx(), b.Dy())
	sx, sy := cw/float64(b.Dx()), ch/float64(b.Dy())
	rx, ry := 3*max(1, sx)+0.5, 3*max(1, sy)+0.5
	var errs []int
	for y := range b.Dy() {
		for x := range b.Dx() {
			cx, cy := x0+(float64(x)+0.5)*sx, y0+(float64(y)+0.5)*sy
			bx, by := math.Floor((cx-rx)/16), math.Floor((cy-ry)/16)
			if bx != math.Floor((cx+rx)/16) || by != math.Floor((cy+ry)/16) || cx-rx < 0 || cy-ry < 0 ||
				cx+rx > float64(w) || cy+ry > float64(h) {
				continue
			}
			want := display.RGBAAt(int(cx), int(cy))
			r, g, bl, _ := m.At(b.Min.X+x, b.Min.Y+y).RGBA()
			errs = append(errs, diff(uint8(r>>8), want.R), diff(uint8(g>>8), want.G), diff(uint8(bl>>8), want.B))
		}
	}
	return errs
}

func TestMakeOrientation(t *testing.T) {
	const w, h = 64, 48
	display := blocksImage(w, h)
	for o := 1; o <= 8; o++ {
		s := storedImage(display, o)
		for _, tt := range []struct {
			format string
			data   []byte
		}{
			{mediafile.JPEG, insertJPEG(jpegQ(t, s, 95), exifSegment(o))},
			{mediafile.PNG, insertPNG(pngOf(t, s), pngChunk("eXIf", exifTIFF(o)))},
		} {
			name := fmt.Sprintf("%s with orientation %d", tt.format, o)
			info, err := Inspect(tt.data)
			if err != nil || info.Width != w || info.Height != h || info.Orientation != o {
				t.Fatalf("%s: Inspect() = %+v, %v", name, info, err)
			}
			vs := []Variant{Published(info), {32, 24, tt.format, false}, {48, 16, tt.format, false},
				{24, 24, tt.format, false}, Icon(info, 32), {64, 32, tt.format, false}}
			_, files := makeOf(t, tt.data, nil, vs...)
			for i, f := range files {
				errs := blockErrors(decodeFile(t, f), w, h)
				sum := 0
				for _, e := range errs {
					sum += e
				}
				// JPEG files, made from a JPEG file, have the artifacts of both around the edges of blocks.
				maxMean, maxErr := 0.0, 0
				if tt.format == mediafile.JPEG {
					maxMean, maxErr = 8, 48
				}
				if len(errs) < 60 || float64(sum)/float64(len(errs)) > maxMean || slices.Max(errs) > maxErr {
					t.Errorf("%s, %+v: %d samples differ from the image as displayed by %.1f on average and %d at most",
						name, vs[i], len(errs), float64(sum)/float64(len(errs)), slices.Max(errs))
				}
			}
			if tt.format == mediafile.PNG {
				if got := nrgbaOf(decodeFile(t, files[0])); !bytes.Equal(got.Pix, nrgbaOf(display).Pix) {
					t.Errorf("%s: the published file is not the image as displayed", name)
				}
			}
		}
	}
}

func TestMakeOrientationWebP(t *testing.T) {
	type vector struct {
		name  string
		o     int
		exact bool
	}
	vectors := []vector{{"meta-exif-prefix-o6-lossy", 6, false}, {"meta-exif-o6-lossless", 6, true},
		{"meta-exif-o3-alpha", 3, true}, {"meta-all-alpha", 8, true}}
	for o := 1; o <= 8; o++ {
		vectors = append(vectors, vector{fmt.Sprintf("meta-exif-o%d-lossy", o), o, false})
	}
	for _, v := range vectors {
		data := readVector(t, v.name)
		info, err := Inspect(data)
		if err != nil || info.Orientation != v.o {
			t.Fatalf("%s: Inspect() = %+v, %v", v.name, info, err)
		}
		f, err := webp.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		var profile []byte
		if info.ICC == iccKeep {
			profile = f.ICC
		}
		_, files := makeOf(t, data, profile, Published(info))
		decoded, err := webp.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		want := displayedImage(decoded, v.o)
		got := nrgbaOf(decodeFile(t, files[0]))
		if v.exact {
			if !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("%s: the file is not the image as displayed", v.name)
			}
			continue
		}
		// The colors of the file, a JPEG, are about those of the image, and far from those of the image in the other
		// orientations of the same size.
		for o := 1; o <= 8; o++ {
			other := displayedImage(decoded, o)
			if other.Rect != want.Rect {
				continue
			}
			sum := 0
			for i := range got.Pix {
				sum += diff(got.Pix[i], other.Pix[i])
			}
			if mean := float64(sum) / float64(len(got.Pix)); (o == v.o) != (mean < 8) {
				t.Errorf("%s: mean difference %.1f from the image as displayed with orientation %d", v.name, mean, o)
			}
		}
		// The white mark of the first rows and columns as stored is in the corner of those sides as displayed.
		w, h := got.Rect.Dx(), got.Rect.Dy()
		x, y := 1, 1
		if slices.Contains(exifSides[v.o][:], "right") {
			x = w - 2
		}
		if slices.Contains(exifSides[v.o][:], "bottom") {
			y = h - 2
		}
		if c := got.NRGBAAt(x, y); c.R < 200 || c.G < 200 || c.B < 200 {
			t.Errorf("%s: pixel (%d, %d) is %v, not white", v.name, x, y, c)
		}
	}
}

// metadataJPEG returns a JPEG file of m with metadata of every kind and the segments of more.
func metadataJPEG(t testing.TB, m image.Image, more ...[]byte) []byte {
	t.Helper()
	return insertJPEG(jpegOf(t, m), append([][]byte{
		jpegSegment(0xe0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00")),
		exifSegment(1),
		jpegSegment(0xe1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")),
		jpegSegment(0xed, []byte("Photoshop 3.0\x008BIM\x04\x04\x00\x00\x00\x00\x00\x00")),
		jpegSegment(0xe2, []byte("MPF\x00II*\x00")),
		jpegSegment(0xfe, []byte("comment")),
	}, more...)...)
}

// metadataPNG returns pngOf(m) with metadata chunks of every kind and more.
func metadataPNG(t testing.TB, m image.Image, more ...[]byte) []byte {
	t.Helper()
	return insertPNG(pngOf(t, m), append([][]byte{
		pngChunk("eXIf", exifTIFF(1)), pngChunk("tEXt", []byte("Comment\x00text")),
		pngChunk("zTXt", append([]byte("Comment\x00\x00"), zlibOf(t, []byte("text"))...)),
		pngChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00<x:xmpmeta/>")),
		pngChunk("tIME", []byte{7, 234, 11, 1, 12, 0, 0}), pngChunk("pHYs", make([]byte, 9)),
		pngChunk("gAMA", be.AppendUint32(nil, 45455)),
	}, more...)...)
}

// variantsOf returns the variants of the default size, of sizes and the icons of info.
func variantsOf(info Info) []Variant {
	vs := []Variant{Published(info)}
	for _, size := range []*content.ImageSize{nil, {Width: 20, Height: 20, Crop: true}, {Width: 30}} {
		set := ForSize(info, size)
		vs = append(vs, set.Src)
		vs = append(vs, set.Srcset...)
	}
	for _, n := range IconSizes() {
		vs = append(vs, Icon(info, n))
	}
	return vs
}

func TestMakeMetadata(t *testing.T) {
	m := colored(40, 30)
	gray := image.NewGray(m.Rect)
	for i := range gray.Pix {
		gray.Pix[i] = uint8(i)
	}
	frame := gifImageWith(0, 0, 40, 30, 0, nil, 2, lzwOf(t, 2, indexesOf(40, 30, 4)))
	gifMetadata := slices.Concat(gifExtension(0xfe, []byte("comment")), xmpApp, textExt,
		gifApp("NETSCAPE2.0", []byte{1, 0, 0}), gifExtension(0x99, []byte("?")))
	tests := []struct {
		name    string
		data    []byte
		profile []byte
	}{
		{"JPEG", metadataJPEG(t, m), nil},
		{"JPEG with a profile", metadataJPEG(t, m, iccSegments(p3Profile, 2)...), p3Profile},
		{"grayscale JPEG", metadataJPEG(t, gray), nil},
		{"PNG", metadataPNG(t, m), nil},
		{"PNG with a profile", metadataPNG(t, m, iccpChunk(t, p3Profile)), p3Profile},
		{"paletted PNG", metadataPNG(t, image.NewPaletted(m.Rect, color.Palette{color.Black, color.White})), nil},
		{"GIF", gifWith("GIF89a", 40, 30, rgbTable, gifMetadata, gifControlWith(1, 0, 3), frame, gifMetadata), nil},
		{"GIF animation", gifWith("GIF89a", 40, 30, rgbTable, gifMetadata, frame, gifMetadata, frame), nil},
		{"APNG", apngFile(t, m, pngChunk("tEXt", []byte("Comment\x00text")), pngChunk("eXIf", exifTIFF(1)),
			iccpChunk(t, srgbProfile)), nil},
	}
	for _, name := range []string{"meta-xmp-lossy", "meta-icc-p3-lossy", "meta-icc-srgb-lossless", "meta-all-alpha",
		"meta-anim-icc-xmp", "meta-anim-exif-o6", "meta-exif-o5-lossy"} {
		data := readVector(t, name)
		info, err := Inspect(data)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := keptICC(data, info)
		if err != nil {
			t.Fatal(err)
		}
		tests = append(tests, struct {
			name    string
			data    []byte
			profile []byte
		}{name, data, profile})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := Inspect(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			makeOf(t, tt.data, tt.profile, variantsOf(info)...)
		})
	}
}

func TestMakeAnimation(t *testing.T) {
	m := colored(4, 3)
	apng := apngFile(t, m, pngChunk("tEXt", []byte("a\x00b")), iccpChunk(t, p3Profile), pngChunk("eXIf", exifTIFF(1)))
	frame := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, indexesOf(4, 3, 4)))
	for _, tt := range []struct {
		name string
		data []byte
		want func(t *testing.T, file []byte)
	}{
		{"GIF", gifWith("GIF89a", 4, 3, rgbTable, xmpApp, frame, frame), func(t *testing.T, file []byte) {
			want, err := rebuildGIF(gifWith("GIF89a", 4, 3, rgbTable, frame, frame))
			if err != nil || !bytes.Equal(file, want) {
				t.Errorf("not the GIF rebuilt: %v", err)
			}
		}},
		{"APNG", apng, func(t *testing.T, file []byte) {
			if want := apngFile(t, m, iccpChunk(t, p3Profile)); !bytes.Equal(file, want) {
				t.Errorf("chunks %q, want %q", chunkTypes(file), chunkTypes(want))
			}
		}},
		{"WebP", readVector(t, "meta-anim-icc-xmp"), func(t *testing.T, file []byte) {
			f, err := webp.Parse(file)
			if err != nil {
				t.Fatal(err)
			}
			orig, _ := webp.Parse(readVector(t, "meta-anim-icc-xmp"))
			if !bytes.Equal(f.ICC, orig.ICC) || f.XMP != nil || len(f.Frames) != len(orig.Frames) ||
				f.LoopCount != orig.LoopCount {
				t.Errorf("WebP of %d frames, %d bytes of ICC, XMP %q", len(f.Frames), len(f.ICC), f.XMP)
			}
			for i := range f.Frames {
				a, _ := f.FrameImage(i)
				b, _ := orig.FrameImage(i)
				if !bytes.Equal(nrgbaOf(a).Pix, nrgbaOf(b).Pix) || f.Frames[i].X != orig.Frames[i].X ||
					f.Frames[i].Duration != orig.Frames[i].Duration {
					t.Errorf("frame %d differs", i)
				}
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info, err := Inspect(tt.data)
			if err != nil || !info.Animated {
				t.Fatalf("Inspect() = %+v, %v", info, err)
			}
			profile, err := keptICC(tt.data, info)
			if err != nil {
				t.Fatal(err)
			}
			_, files := makeOf(t, tt.data, profile, Published(info), Icon(info, 32))
			tt.want(t, files[0])
		})
	}
}

// TestMakeBrokenGIF checks that the files of a GIF animation that cannot be rebuilt are made from its first frame.
func TestMakeBrokenGIF(t *testing.T) {
	cover := indexesOf(4, 3, 4)
	full := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"fewer indexes", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 4, 3, 0, nil, 2,
			lzwOf(t, 2, cover[:11])))},
		{"invalid code", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 4, 3, 0, nil, 2,
			[]byte{0xff, 0xff}))},
		{"broken first frame", gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 2,
			lzwOf(t, 2, cover[:7])), full)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := rebuildGIF(tt.data); err == nil {
				t.Fatal("rebuildGIF() succeeds")
			}
			info, err := Inspect(tt.data)
			if err != nil || info.Animated || !slices.Equal(info.Warnings, []string{gifBrokenWarning}) {
				t.Fatalf("Inspect() = %+v, %v", info, err)
			}
			_, files := makeOf(t, tt.data, nil, variantsOf(info)...)
			want, err := decodeGIF(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if got := nrgbaOf(decodeFile(t, files[0])); !bytes.Equal(got.Pix, nrgbaOf(want).Pix) {
				t.Errorf("the published file is not the first frame")
			}
		})
	}
}

func TestMakeICC(t *testing.T) {
	m := colored(40, 30)
	gray := image.NewGray(m.Rect)
	for i := range gray.Pix {
		gray.Pix[i] = uint8(3 * i)
	}
	grayP := grayProfile(gamma22)
	large := rgbProfile(p3Colorant, srgbPara, profileTag{"zzzz", bytes.Repeat([]byte("large"), 30000)})
	trns := pngChunk("tRNS", []byte{0, 0})
	tests := []struct {
		name       string
		data       []byte
		profile    []byte
		grayscale  bool
		decision   string
		components int // of the JPEG files; 0 if they are PNG
	}{
		{"JPEG with Display P3", insertJPEG(jpegOf(t, m), iccSegments(p3Profile, 3)...), p3Profile, false, iccKeep, 3},
		{"JPEG with a large profile", insertJPEG(jpegOf(t, m), iccSegments(large, 3)...), large, false, iccKeep, 3},
		{"JPEG with sRGB", insertJPEG(jpegOf(t, m), iccSegment(1, 1, srgbProfile)), nil, false, iccSRGB, 3},
		{"JPEG with an invalid profile", insertJPEG(jpegOf(t, m), iccSegment(1, 1, p3Profile[:200])), nil, false,
			iccInvalid, 3},
		{"grayscale JPEG with a grayscale profile", insertJPEG(jpegOf(t, gray), iccSegment(1, 1, grayP)), grayP, true,
			iccKeep, 1},
		{"grayscale JPEG", jpegOf(t, gray), nil, true, "", 1},
		{"CMYK JPEG with a CMYK profile", insertJPEG(cmykJPEG(0), iccSegment(1, 1, cmykProfile)), nil, false, iccCMYK,
			3},
		{"PNG with Display P3", insertPNG(pngOf(t, m), iccpChunk(t, p3Profile)), p3Profile, false, iccKeep, 0},
		{"grayscale PNG with a grayscale profile", insertPNG(pngOf(t, gray), iccpChunk(t, grayP)), grayP, true,
			iccKeep, 0},
		{"grayscale PNG with tRNS and a grayscale profile", insertPNG(pngOf(t, gray), trns, iccpChunk(t, grayP)), nil,
			false, iccGray, 0},
		{"16-bit grayscale PNG with a grayscale profile", insertPNG(pngOf(t, image.NewGray16(m.Rect)),
			iccpChunk(t, grayP)), grayP, true, iccKeep, 0},
		{"WebP with Display P3", readVector(t, "meta-icc-p3-lossy"), p3ProfileOfVectors(t), false, iccKeep, 3},
		{"WebP with sRGB", readVector(t, "meta-icc-srgb-lossless"), nil, false, iccSRGB, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := Inspect(tt.data)
			if err != nil || info.ICC != tt.decision {
				t.Fatalf("Inspect() = %+v, %v", info, err)
			}
			vs := variantsOf(info)
			_, files := makeOf(t, tt.data, tt.profile, vs...)
			for i, f := range files {
				m := decodeFile(t, f)
				if _, ok := m.(*image.Gray); ok != tt.grayscale {
					t.Errorf("%+v: %T", vs[i], m)
				}
				if vs[i].Format == mediafile.JPEG {
					if c := sofComponents(t, f); c != tt.components {
						t.Errorf("%+v: %d components", vs[i], c)
					}
				}
			}
		})
	}
}

// p3ProfileOfVectors returns the Display P3 profile of the WebP vectors.
func p3ProfileOfVectors(t *testing.T) []byte {
	f, err := webp.Parse(readVector(t, "meta-icc-p3-lossy"))
	if err != nil {
		t.Fatal(err)
	}
	return f.ICC
}

// sofComponents returns the number of components in the frame header of file, a JPEG file.
func sofComponents(t *testing.T, file []byte) int {
	for i := 2; i+4 <= len(file); i += 2 + int(be.Uint16(file[i+2:])) {
		if file[i+1] == 0xc0 {
			return int(file[i+9])
		}
	}
	t.Fatal("no SOF0")
	return 0
}

// markedImage returns an image of w x h pixels, white with a red square of 2*r pixels at its center.
func markedImage(w, h, r int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{255, 255, 255, 255}
			if 2*x >= w-2*r && 2*x < w+2*r && 2*y >= h-2*r && 2*y < h+2*r {
				c = color.RGBA{255, 0, 0, 255}
			}
			m.SetRGBA(x, y, c)
		}
	}
	return m
}

func TestMakeCentered(t *testing.T) {
	for _, tt := range []struct {
		w, h int
		v    Variant
	}{
		{400, 300, pngVariant(150, 150)},
		{300, 400, pngVariant(150, 150)},
		{400, 300, pngVariant(300, 158)},
		{400, 300, pngVariant(40, 120)},
		{400, 300, pngVariant(400, 100)},
		{401, 300, pngVariant(300, 300)},
		{120, 90, pngVariant(31, 31)},
	} {
		for _, o := range []int{1, 2, 6, 7} {
			display := markedImage(tt.w, tt.h, 20)
			data := insertPNG(pngOf(t, storedImage(display, o)), pngChunk("eXIf", exifTIFF(o)))
			_, files := makeOf(t, data, nil, tt.v)
			m := decodeFile(t, files[0])
			// The bounds of the red square.
			b := m.Bounds()
			minX, minY, maxX, maxY := b.Dx(), b.Dy(), -1, -1
			for y := range b.Dy() {
				for x := range b.Dx() {
					if r, g, _, _ := m.At(x, y).RGBA(); r>>8 > 200 && g>>8 < 100 {
						minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
					}
				}
			}
			if maxX < 0 || abs(minX+maxX+1-b.Dx()) > 2 || abs(minY+maxY+1-b.Dy()) > 2 {
				t.Errorf("%dx%d to %+v with orientation %d: red from (%d, %d) to (%d, %d), not at the center", tt.w,
					tt.h, tt.v, o, minX, minY, maxX, maxY)
			}
		}
	}
}

func abs(n int) int {
	return max(n, -n)
}

// largeJPEG returns a JPEG file whose published file is smaller than it: the image as displayed is 2600 x 40 pixels.
func largeJPEG(t testing.TB, o int) []byte {
	m := image.NewRGBA(image.Rect(0, 0, 2600, 40))
	for y := range 40 {
		for x := range 2600 {
			m.SetRGBA(x, y, color.RGBA{uint8(x), uint8(x / 10), uint8(8 * y), 255})
		}
	}
	return insertJPEG(jpegOf(t, storedImage(m, o)), exifSegment(o))
}

// TestMakeGroups checks that the files of the variants are the same however they are made together: from the
// published image or from the original, each as the variant needs.
func TestMakeGroups(t *testing.T) {
	m := colored(40, 30)
	frame := gifImageWith(0, 0, 40, 30, 0, nil, 2, lzwOf(t, 2, indexesOf(40, 30, 4)))
	inputs := map[string][]byte{
		"large JPEG":          largeJPEG(t, 1),
		"large JPEG rotated":  largeJPEG(t, 6),
		"PNG with alpha":      pngOf(t, image.NewNRGBA(m.Rect)),
		"GIF":                 gifWith("GIF89a", 40, 30, rgbTable, frame),
		"GIF animation":       gifWith("GIF89a", 40, 30, rgbTable, frame, frame),
		"WebP":                readVector(t, "meta-exif-o6-lossy"),
		"WebP animation":      readVector(t, "anim-img2webp-lossy"),
		"APNG":                apngFile(t, m),
		"JPEG with a profile": insertJPEG(jpegOf(t, m), iccSegment(1, 1, p3Profile)),
	}
	for _, name := range slices.Sorted(maps.Keys(inputs)) {
		data := inputs[name]
		t.Run(name, func(t *testing.T) {
			info, err := Inspect(data)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := keptICC(data, info)
			if err != nil {
				t.Fatal(err)
			}
			vs := variantsOf(info)
			if name == "large JPEG" || name == "large JPEG rotated" {
				// A file as large as the original, made from it.
				vs = append(vs, ForSize(info, &content.ImageSize{Width: 1300}).Srcset...)
			}
			all, err := makeFiles(t.Context(), data, info, vs)
			if err != nil {
				t.Fatal(err)
			}
			again, err := makeFiles(t.Context(), data, info, vs)
			if err != nil {
				t.Fatal(err)
			}
			rev := slices.Clone(vs)
			slices.Reverse(rev)
			reversed, err := makeFiles(t.Context(), data, info, rev)
			if err != nil {
				t.Fatal(err)
			}
			for i, v := range vs {
				one, err := makeFiles(t.Context(), data, info, []Variant{v})
				if err != nil {
					t.Fatal(err)
				}
				checkFile(t, one[0], v, profile)
				if !bytes.Equal(all[i], one[0]) || !bytes.Equal(all[i], again[i]) ||
					!bytes.Equal(all[i], reversed[len(vs)-1-i]) {
					t.Errorf("%+v: other bytes when made with other variants or again", v)
				}
			}
		})
	}
}

// TestMakeFromPublished checks which image each variant of an image larger than its published file is made from.
func TestMakeFromPublished(t *testing.T) {
	data := largeJPEG(t, 6)
	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	pub := Published(info)
	src, err := decode(data, info)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := resize(src, pub.Height, pub.Width) // as stored
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		v        Variant
		original bool
	}{
		{pub, false},
		{jpegVariant(640, 10), false},
		{Icon(info, 32), false},
		{Icon(info, 180), true}, // 40 x 40 pixels, higher than the published file
		{jpegVariant(2600, 40), true},
		{jpegVariant(2580, 39), true},
	} {
		from, name := mid, "the published image"
		if tt.original {
			from, name = src, "the original"
		}
		want, err := makeStill(from, info.Orientation, tt.v, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := makeFiles(t.Context(), data, info, []Variant{tt.v})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got[0], want) {
			t.Errorf("%+v: not made from %s", tt.v, name)
		}
	}
}

func TestMakeCMYK(t *testing.T) {
	for _, transform := range []byte{0, 2} {
		data := cmykJPEG(transform)
		info, err := Inspect(data)
		if err != nil {
			t.Fatal(err)
		}
		m, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		_, files := makeOf(t, data, nil, Published(info), Icon(info, 32))
		icon := nrgbaOf(decodeFile(t, files[1]))
		for y := range 8 {
			for x := range 8 {
				c := m.(*image.CMYK).CMYKAt(x, y)
				r, g, b := color.CMYKToRGB(c.C, c.M, c.Y, c.K)
				if got, want := icon.NRGBAAt(x, y), (color.NRGBA{r, g, b, 0xff}); got != want {
					t.Errorf("transform %d: pixel (%d, %d) is %v, want %v from %v", transform, x, y, got, want, c)
				}
			}
		}
	}
}

func TestMake16Bit(t *testing.T) {
	r := image.Rect(0, 0, 3, 2)
	gray := image.NewGray16(r)
	nrgba := image.NewNRGBA64(r)
	rgba := image.NewRGBA64(r)
	for i := range gray.Pix {
		gray.Pix[i] = byte(97 * i)
	}
	for i := range nrgba.Pix {
		nrgba.Pix[i] = byte(31*i + 7)
		rgba.Pix[i] = byte(53*i + 3)
	}
	for i := 6; i < len(rgba.Pix); i += 8 {
		rgba.Pix[i], rgba.Pix[i+1] = 0xff, 0xff
	}
	round := func(v uint16) uint8 { return uint8((uint32(v) + 128) / 257) }
	for _, m := range []image.Image{gray, nrgba, rgba} {
		data := pngOf(t, m)
		info, err := Inspect(data)
		if err != nil {
			t.Fatal(err)
		}
		_, files := makeOf(t, data, nil, Published(info))
		got := decodeFile(t, files[0])
		for y := range 2 {
			for x := range 3 {
				c := color.NRGBA64Model.Convert(m.At(x, y)).(color.NRGBA64)
				want := color.NRGBA{round(c.R), round(c.G), round(c.B), round(c.A)}
				if g := color.NRGBAModel.Convert(got.At(x, y)).(color.NRGBA); g != want {
					t.Errorf("%T: pixel (%d, %d) is %v, want %v", m, x, y, g, want)
				}
			}
		}
		if _, ok := m.(*image.Gray16); ok {
			if _, ok := got.(*image.Gray); !ok {
				t.Errorf("%T: the file decodes to %T", m, got)
			}
		}
	}
}

func TestMakePaletted(t *testing.T) {
	pal := color.Palette{color.RGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 128}, color.NRGBA{0, 0, 0, 0}}
	m := image.NewPaletted(image.Rect(0, 0, 4, 3), pal)
	copy(m.Pix, indexesOf(4, 3, 3))
	for o := 1; o <= 8; o++ {
		sw, sh := stored(4, 3, o)
		s := image.NewPaletted(image.Rect(0, 0, sw, sh), pal)
		for y := range 3 {
			for x := range 4 {
				sx, sy := storedPoint(o, x, y, 4, 3)
				s.SetColorIndex(sx, sy, m.ColorIndexAt(x, y))
			}
		}
		data := insertPNG(pngOf(t, s), pngChunk("eXIf", exifTIFF(o)))
		info, err := Inspect(data)
		if err != nil {
			t.Fatal(err)
		}
		_, files := makeOf(t, data, nil, Published(info))
		got, ok := decodeFile(t, files[0]).(*image.Paletted)
		if !ok || !bytes.Equal(got.Pix, m.Pix) || !samePalette(got.Palette, pal) {
			t.Errorf("orientation %d: %v, want %v", o, got, m)
		}
	}
	// A GIF whose first frame covers its screen also keeps its palette.
	data := gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, indexesOf(4, 3, 4))))
	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	_, files := makeOf(t, data, nil, Published(info))
	if got, ok := decodeFile(t, files[0]).(*image.Paletted); !ok || !bytes.Equal(got.Pix, indexesOf(4, 3, 4)) {
		t.Errorf("GIF: %v", got)
	}
}

func TestMakeError(t *testing.T) {
	file := jpegOf(t, colored(40, 30))
	info, err := Inspect(file)
	if err != nil {
		t.Fatal(err)
	}
	sos := jpegMarker(t, file, 0xda)
	gifData := gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, indexesOf(4, 3, 4))))
	gifInfo, err := Inspect(gifData)
	if err != nil {
		t.Fatal(err)
	}
	other := gifInfo
	other.MIMEType = mediafile.PNG
	rotated := info
	rotated.Width, rotated.Height = 30, 40
	for _, tt := range []struct {
		name string
		data []byte
		info Info
		v    Variant
	}{
		{"animation of a still image", file, info, Variant{40, 30, mediafile.JPEG, true}},
		{"wider than the image", file, info, jpegVariant(41, 30)},
		{"higher than the image", file, info, jpegVariant(40, 31)},
		{"no pixels", file, info, jpegVariant(0, 30)},
		{"GIF", file, info, Variant{40, 30, mediafile.GIF, false}},
		{"WebP", file, info, Variant{40, 30, mediafile.WebP, false}},
		{"Info of another size", file, rotated, jpegVariant(30, 40)},
		{"broken image data", slices.Concat(file[:sos+20], bytes.Repeat([]byte{0xff, 0x00}, 50), file[len(file)-2:]),
			info, jpegVariant(40, 30)},
		{"JPEG of an image with transparency", pngOf(t, image.NewNRGBA(image.Rect(0, 0, 4, 3))), pngInfo(4, 3, 1),
			jpegVariant(4, 3)},
		{"Info of another format", gifData, other, pngVariant(4, 3)},
		{"PDF", []byte("%PDF-1.7\n"), Info{MIMEType: mediafile.PDF, Width: 1, Height: 1, Orientation: 1},
			pngVariant(1, 1)},
	} {
		if files, err := makeFiles(t.Context(), tt.data, tt.info, []Variant{tt.v}); err == nil {
			t.Errorf("%s: makeFiles() = %d files, want an error", tt.name, len(files))
		}
	}
}

func TestJobMemory(t *testing.T) {
	data := largeJPEG(t, 1)
	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	pub := Published(info)
	const buffers = 2 << 20
	if got := jobMemory(info, nil); got != int64(info.Size)+buffers {
		t.Errorf("jobMemory() of no variants = %d, want the size of the original %d and buffers", got, info.Size)
	}
	one := jobMemory(info, []Variant{pub})
	if want := int64(info.Size) + buffers + info.Memory + 128*(2600+40) + 16*2560*39; one != want {
		t.Errorf("jobMemory() of the published file = %d, want %d", one, want)
	}
	if all := jobMemory(info, variantsOf(info)); all <= one {
		t.Errorf("jobMemory() of all variants = %d, not more than %d of one", all, one)
	}
	frame := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, indexesOf(4, 3, 4)))
	anim := gifWith("GIF89a", 4, 3, rgbTable, frame, frame)
	ainfo, err := Inspect(anim)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jobMemory(ainfo, []Variant{Published(ainfo)}), int64(3*len(anim))+buffers; got != want {
		t.Errorf("jobMemory() of an animation = %d, want %d", got, want)
	}
	if got, want := jobMemory(ainfo, []Variant{Published(ainfo), Icon(ainfo, 32)}),
		int64(3*len(anim))+buffers+ainfo.Memory+128*7+8*9+4*9; got != want { // an icon of 3 x 3 pixels
		t.Errorf("jobMemory() of an animation and an icon = %d, want %d", got, want)
	}
}

// photoJPEG returns a JPEG file of w x h pixels of smooth colors and noise, like a photo, with the orientation o.
func photoJPEG(b testing.TB, w, h, o int) []byte {
	m := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	seed := uint32(1)
	for y := range h {
		for x := range w {
			seed = seed*1664525 + 1013904223
			m.Y[m.YOffset(x, y)] = uint8(x*255/w/2 + y*255/h/4 + int(seed>>28))
		}
	}
	for i := range m.Cb {
		m.Cb[i], m.Cr[i] = uint8(96+i%64), uint8(160-i%48)
	}
	return insertJPEG(jpegQ(b, m, 90), exifSegment(o))
}

func BenchmarkMakeFiles(b *testing.B) {
	jpegData := photoJPEG(b, 6000, 4000, 6)
	m, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		b.Fatal(err)
	}
	rgba := ycbcrToRGBA(m.(*image.YCbCr), 6000, 4000, orientationOf(1, 6000, 4000))
	for _, original := range []struct {
		name string
		data []byte
	}{
		{"JPEG", jpegData},
		{"PNG", pngOf(b, rgba)},
	} {
		info, err := Inspect(original.data)
		if err != nil {
			b.Fatal(err)
		}
		b.Logf("%s of %d bytes, %dx%d pixels as displayed; Memory %d, jobMemory %d", original.name,
			len(original.data), info.Width, info.Height, info.Memory, jobMemory(info, variantsOf(info)))
		for _, bb := range []struct {
			name string
			vs   []Variant
		}{
			{"published", []Variant{Published(info)}},
			{"default size", append([]Variant{Published(info)}, ForSize(info, nil).Srcset...)},
			{"all", variantsOf(info)},
		} {
			b.Run(original.name+" "+bb.name, func(b *testing.B) {
				for b.Loop() {
					if _, err := makeFiles(b.Context(), original.data, info, bb.vs); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
