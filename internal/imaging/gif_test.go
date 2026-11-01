package imaging

import (
	"bytes"
	"compress/lzw"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"io"
	"slices"
	"strings"
	"testing"
)

// lzwOf returns indexes coded with LZW of minCodeSize as GIF codes them.
func lzwOf(t testing.TB, minCodeSize int, indexes []byte) []byte {
	t.Helper()
	return bytesOf(t, func(out io.Writer) error {
		lw := lzw.NewWriter(out, lzw.LSB, minCodeSize)
		if _, err := lw.Write(indexes); err != nil {
			return err
		}
		return lw.Close()
	})
}

// gifImageWith returns an image block of w x h pixels at (x, y) with the packed fields and table, the local color table
// if packed has its flag, and its LZW data of minCodeSize, in sub-blocks.
func gifImageWith(x, y, w, h int, packed byte, table []byte, minCodeSize int, data []byte) []byte {
	b := le.AppendUint16(le.AppendUint16([]byte{gifImageBlock}, uint16(x)), uint16(y))
	b = le.AppendUint16(le.AppendUint16(b, uint16(w)), uint16(h))
	b = append(append(b, packed), table...)
	return append(append(b, byte(minCodeSize)), gifSubBlocks(data)...)
}

// gifWith returns a GIF file of version of w x h pixels with the global color table if it is not nil (padded to a
// power of 2 colors), the blocks and the trailer.
func gifWith(version string, w, h int, global []byte, blocks ...[]byte) []byte {
	b := le.AppendUint16(le.AppendUint16([]byte(version), uint16(w)), uint16(h))
	if global == nil {
		b = append(b, 0, 0, 0)
	} else {
		bits := 0
		for 3<<(bits+1) < len(global) {
			bits++
		}
		global = append(slices.Clone(global), make([]byte, 3<<(bits+1)-len(global))...)
		b = append(append(b, 0x80|byte(bits), 0, 0), global...)
	}
	return append(slices.Concat(b, slices.Concat(blocks...)), gifTrailer)
}

// gifControlWith returns a graphic control extension with the packed fields, a delay and the transparent index.
func gifControlWith(packed byte, delay int, transparent byte) []byte {
	return []byte{gifExtensionBlock, gifControlLabel, 4, packed, byte(delay), byte(delay >> 8), transparent, 0}
}

// gifApp returns an application extension with the identifier and the sub-blocks.
func gifApp(id string, subs ...[]byte) []byte {
	b := append([]byte{gifExtensionBlock, gifAppLabel, byte(len(id))}, id...)
	for _, s := range subs {
		b = append(append(b, byte(len(s))), s...)
	}
	return append(b, 0)
}

var (
	rgbTable = []byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 255} // red, green, blue, white
	xmpApp   = gifApp("XMP DataXMP", []byte("<x:xmpmeta/>"))
	textExt  = slices.Concat([]byte{gifExtensionBlock, 0x01, 12}, make([]byte, 12), gifSubBlocks([]byte("text")))
)

// indexesOf returns w x h indexes of a pattern of colors 0 to n-1.
func indexesOf(w, h, n int) []byte {
	b := make([]byte, w*h)
	for i := range b {
		b[i] = byte((i/w + i%w*3) % n)
	}
	return b
}

// rendered returns the canvas of w x h pixels with the frame of gif.Decode drawn on it, as decodeGIF draws it.
func rendered(w, h int, frame *image.Paletted) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := frame.Rect.Min.Y; y < min(frame.Rect.Max.Y, h); y++ {
		for x := frame.Rect.Min.X; x < min(frame.Rect.Max.X, w); x++ {
			out.Set(x, y, frame.At(x, y))
		}
	}
	return out
}

// nrgbaOf returns the pixels of m as NRGBA.
func nrgbaOf(m image.Image) *image.NRGBA {
	b := m.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := range b.Dy() {
		for x := range b.Dx() {
			out.Set(x, y, m.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

func TestDecodeGIF(t *testing.T) {
	cover := indexesOf(4, 3, 4)
	full := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))
	interlaced := slices.Concat(cover[0:4], cover[8:12], cover[4:8]) // rows 0, 2, 1 of 3 in pass order
	tests := []struct {
		name     string
		data     []byte
		paletted bool
		want     *image.NRGBA // nil: the frame of gif.Decode drawn on the canvas
	}{
		{name: "covering the screen", data: gifWith("GIF89a", 4, 3, rgbTable, full), paletted: true},
		{name: "GIF87a", data: gifWith("GIF87a", 4, 3, rgbTable, full), paletted: true},
		{name: "local color table and transparency", paletted: true, data: gifWith("GIF89a", 4, 3, nil,
			gifControlWith(1, 0, 2), gifImageWith(0, 0, 4, 3, 0x81, rgbTable, 2, lzwOf(t, 2, cover)))},
		{name: "transparency kept by a later control extension", paletted: true, data: gifWith("GIF89a", 4, 3, rgbTable,
			gifControlWith(1, 0, 3), gifControlWith(0, 5, 0), full)},
		{name: "transparent index past the table", paletted: true, data: gifWith("GIF89a", 4, 3, rgbTable[:6],
			gifControlWith(1, 0, 3), gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, indexesOf(4, 3, 2))))},
		{name: "offset", data: gifWith("GIF89a", 5, 4, rgbTable, gifImageWith(1, 1, 3, 2, 0, nil, 2,
			lzwOf(t, 2, indexesOf(3, 2, 4))))},
		{name: "after extensions", paletted: true, data: gifWith("GIF89a", 4, 3, rgbTable, xmpApp,
			gifExtension(0xfe, []byte("comment")), textExt, gifControlWith(4, 10, 0), full)},
		{name: "interlaced", paletted: true, data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 4, 3, 0x40, nil, 2, lzwOf(t, 2, interlaced)))},
		{name: "second frame not read", paletted: true, data: gifWith("GIF89a", 4, 3, rgbTable, full,
			gifImageWith(0, 0, 4, 3, 0, nil, 2, []byte("not LZW")))},
	}
	red, green, blue, white := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 255, 0, 255}, color.NRGBA{0, 0, 255, 255},
		color.NRGBA{255, 255, 255, 255}
	canvas := func(w, h int, rows ...[]color.NRGBA) *image.NRGBA {
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y, row := range rows {
			for x, c := range row {
				m.SetNRGBA(x, y, c)
			}
		}
		return m
	}
	var none color.NRGBA
	// The rows of the frame of cover, which gif.Decode cannot read in these.
	coverRows := [][]color.NRGBA{{red, white, blue, green}, {green, red, white, blue}, {blue, green, red, white}}
	tests = append(tests, []struct {
		name     string
		data     []byte
		paletted bool
		want     *image.NRGBA
	}{
		{name: "partly outside the screen", data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(2, 1, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))),
			want: canvas(4, 3, nil, []color.NRGBA{none, none, red, white}, []color.NRGBA{none, none, green, red})},
		{name: "larger than the screen", paletted: true, data: gifWith("GIF89a", 3, 2, rgbTable, full),
			want: canvas(3, 2, coverRows[0][:3], coverRows[1][:3])},
		{name: "outside the screen", data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(4, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))), want: canvas(4, 3)},
		{name: "unknown extension", paletted: true, data: gifWith("GIF89a", 4, 3, rgbTable,
			gifExtension(0x99, []byte("unknown")), full), want: canvas(4, 3, coverRows...)},
		{name: "no color table", data: gifWith("GIF89a", 4, 3, nil, full), want: canvas(4, 3)},
		{name: "extra data", paletted: true, data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, append(slices.Clone(cover), 1, 2, 3)))),
			want: canvas(4, 3, coverRows...)},
		{name: "index past the table", paletted: true, data: gifWith("GIF89a", 4, 3, rgbTable[:6], full),
			want: canvas(4, 3, []color.NRGBA{red, none, none, green}, []color.NRGBA{green, red, none, none},
				[]color.NRGBA{none, green, red, none})},
		{name: "short data", data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover[:7]))), want: canvas(4, 3, coverRows[0])},
		{name: "short interlaced data", data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 4, 3, 0x40, nil, 2, lzwOf(t, 2, interlaced[:8]))),
			want: canvas(4, 3, coverRows[0], nil, coverRows[2])},
		{name: "invalid LZW code", data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 4, 3, 0, nil, 2, []byte{0xff, 0xff, 0xff})), want: canvas(4, 3)},
		{name: "no width", data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 0, 3, 0, nil, 2, lzwOf(t, 2, nil))), want: canvas(4, 3)},
	}...)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := decodeGIF(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := m.(*image.Paletted); ok != tt.paletted {
				t.Errorf("decodeGIF() = %T", m)
			}
			want := tt.want
			if want == nil {
				g, err := gif.DecodeConfig(bytes.NewReader(tt.data))
				if err != nil {
					t.Fatal(err)
				}
				frame, err := gif.Decode(bytes.NewReader(tt.data))
				if err != nil {
					t.Fatal(err)
				}
				want = rendered(g.Width, g.Height, frame.(*image.Paletted))
			}
			if got := nrgbaOf(m); !bytes.Equal(got.Pix, want.Pix) || got.Rect != want.Rect {
				t.Errorf("decodeGIF() =\n%v\nwant\n%v", got.Pix, want.Pix)
			}
		})
	}
}

func TestDecodeGIFError(t *testing.T) {
	full := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, indexesOf(4, 3, 4)))
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"not GIF", []byte("GIF88a\x04\x00\x03\x00\x00\x00\x00")},
		{"no blocks", gifWith("GIF89a", 4, 3, rgbTable)},
		{"cut in the global color table", gifWith("GIF89a", 4, 3, rgbTable)[:20]},
		{"cut in the image", gifWith("GIF89a", 4, 3, rgbTable, full)[:30]},
		{"minimum code size 9", gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 9, []byte{0}))},
		{"minimum code size 1", gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 1, []byte{0}))},
	} {
		if m, err := decodeGIF(tt.data); err == nil {
			t.Errorf("%s: decodeGIF() = %v, want an error", tt.name, m.Bounds())
		}
	}
}

// gifBlockNames returns the blocks of file, a GIF file, as "image", "control", "loop <n>" for a NETSCAPE2.0 extension
// with a looping sub-block alone, or "extension <label>", and checks what is around them.
func gifBlockNames(t *testing.T, file []byte) []string {
	t.Helper()
	if !bytes.HasPrefix(file, []byte("GIF89a")) || file[12] != 0 || file[len(file)-1] != gifTrailer {
		t.Fatalf("GIF file with header %q, pixel aspect ratio %d and last byte %#x", file[:6], file[12],
			file[len(file)-1])
	}
	var names []string
	i, _ := gifStart(file)
	for {
		b, err := nextGIFBlock(file, i)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch sub := file[b.subs:b.end]; {
		case b.kind == gifImageBlock:
			names = append(names, "image")
		case file[b.pos+1] == gifControlLabel && len(sub) == 6 && sub[0] == 4:
			names = append(names, "control")
		case file[b.pos+1] == gifAppLabel && len(sub) == 17 && string(sub[:12]) == "\x0bNETSCAPE2.0" &&
			string(sub[12:14]) == "\x03\x01":
			names = append(names, "loop "+string(rune('0'+le.Uint16(sub[14:]))))
		default:
			names = append(names, "extension "+string(rune(file[b.pos+1])))
		}
		i = b.end
	}
	if i != len(file)-1 {
		t.Fatalf("blocks end at %d of %d bytes", i, len(file))
	}
	return names
}

// checkSameGIF checks that gif.DecodeAll reads in and out as the same animation, apart from the loop count.
func checkSameGIF(t *testing.T, in, out []byte) *gif.GIF {
	t.Helper()
	a, err := gif.DecodeAll(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	b, err := gif.DecodeAll(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Image) != len(b.Image) || !slices.Equal(a.Delay, b.Delay) || !slices.Equal(a.Disposal, b.Disposal) ||
		a.BackgroundIndex != b.BackgroundIndex || a.Config.Width != b.Config.Width ||
		a.Config.Height != b.Config.Height || !samePalette(a.Config.ColorModel, b.Config.ColorModel) {
		t.Fatalf("rebuilt: %d frames, delays %v, disposal %v, config %+v; want %d, %v, %v, %+v", len(b.Image), b.Delay,
			b.Disposal, b.Config, len(a.Image), a.Delay, a.Disposal, a.Config)
	}
	for i := range a.Image {
		p, q := a.Image[i], b.Image[i]
		if p.Rect != q.Rect || !bytes.Equal(p.Pix, q.Pix) || !samePalette(p.Palette, q.Palette) {
			t.Fatalf("frame %d: %v %v, want %v %v", i, q.Rect, q.Palette, p.Rect, p.Palette)
		}
	}
	return b
}

// samePalette reports whether a and b are palettes of the same colors.
func samePalette(a, b color.Model) bool {
	p, _ := a.(color.Palette)
	q, _ := b.(color.Palette)
	return slices.EqualFunc(p, q, func(c, d color.Color) bool {
		r1, g1, b1, a1 := c.RGBA()
		r2, g2, b2, a2 := d.RGBA()
		return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
	})
}

func TestRebuildGIF(t *testing.T) {
	pal := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}, color.RGBA{0, 0, 0, 255},
		color.RGBA{255, 255, 255, 255}}
	frame := func(x, y, w, h int, p color.Palette) *image.Paletted {
		m := image.NewPaletted(image.Rect(x, y, x+w, y+h), p)
		copy(m.Pix, indexesOf(w, h, len(p)))
		return m
	}
	local := color.Palette{color.RGBA{0, 255, 0, 255}, color.RGBA{255, 255, 0, 255}}
	encoded := bytesOf(t, func(w io.Writer) error {
		return gif.EncodeAll(w, &gif.GIF{
			Image: []*image.Paletted{
				frame(0, 0, 40, 30, pal), frame(5, 7, 20, 10, local), frame(39, 29, 1, 1, pal),
			},
			Delay:     []int{10, 25, 300},
			Disposal:  []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious},
			LoopCount: 3,
			Config:    image.Config{ColorModel: pal, Width: 40, Height: 30},
		})
	})
	// The blocks after the global color table, with metadata added between them.
	blocks := encoded[13+12 : len(encoded)-1]
	metadata := slices.Concat(gifExtension(0xfe, []byte("comment")), xmpApp, textExt, gifExtension(0x99, []byte("?")),
		gifApp("ICCRGBG1012", []byte("profile")))
	withMetadata := slices.Concat(encoded[:13+12], metadata, blocks, metadata, []byte{gifTrailer})
	out, err := rebuildGIF(withMetadata)
	if err != nil {
		t.Fatal(err)
	}
	g := checkSameGIF(t, encoded, out)
	if g.LoopCount != 3 {
		t.Errorf("loop count %d", g.LoopCount)
	}
	want := []string{"loop 3", "control", "image", "control", "image", "control", "image"}
	if got := gifBlockNames(t, out); !slices.Equal(got, want) {
		t.Errorf("blocks %q, want %q", got, want)
	}
	if again, err := rebuildGIF(out); err != nil || !bytes.Equal(again, out) {
		t.Errorf("rebuilt again: %d bytes, %v; want the same %d bytes", len(again), err, len(out))
	}
	// The GIF of image/gif is the same rebuilt.
	if plain, err := rebuildGIF(encoded); err != nil || !bytes.Equal(plain, out) {
		t.Errorf("rebuilt without metadata: %d bytes, %v; want the same %d bytes", len(plain), err, len(out))
	}
}

func TestRebuildGIFBlocks(t *testing.T) {
	cover := indexesOf(4, 3, 4)
	full := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))
	rows := slices.Concat(cover[0:4], cover[8:12], cover[4:8]) // in the order of the passes
	interlaced := gifImageWith(0, 0, 4, 3, 0x40, nil, 2, lzwOf(t, 2, rows))
	offset := gifImageWith(1, 1, 2, 2, 0x80, rgbTable[:6], 3, lzwOf(t, 3, []byte{1, 0, 0, 1}))
	netscape := gifApp("NETSCAPE2.0", []byte{1, 5, 0})
	tests := []struct {
		name   string
		in     []byte
		blocks []string
		// same is true if gif.DecodeAll reads in and the rebuilt file as the same animation.
		same bool
	}{
		{"two frames", gifWith("GIF89a", 4, 3, rgbTable, netscape, gifControl(), full, gifControl(), offset),
			[]string{"loop 5", "control", "image", "control", "image"}, true},
		{"GIF87a", gifWith("GIF87a", 4, 3, rgbTable, full, full), []string{"image", "image"}, true},
		{"interlaced", gifWith("GIF89a", 4, 3, rgbTable, interlaced, full, interlaced),
			[]string{"image", "image", "image"}, true},
		{"ANIMEXTS1.0", gifWith("GIF89a", 4, 3, rgbTable, gifApp("ANIMEXTS1.0", []byte{1, 7, 0}), full, full),
			[]string{"loop 7", "image", "image"}, false},
		{"loop sub-block after another", gifWith("GIF89a", 4, 3, rgbTable,
			gifApp("NETSCAPE2.0", []byte{2, 0, 0, 1, 0}, []byte{0x09, 4, 0, 0xff}), full, full),
			[]string{"loop 4", "image", "image"}, false},
		{"no looping sub-block", gifWith("GIF89a", 4, 3, rgbTable, gifApp("NETSCAPE2.0", []byte{2, 0, 0, 1, 0}), full,
			full), []string{"image", "image"}, true},
		{"control extension with more data", gifWith("GIF89a", 4, 3, rgbTable,
			gifExtension(gifControlLabel, []byte{4, 10, 0, 1, 9, 9, 9}), full, full),
			[]string{"control", "image", "image"}, false},
		{"short control extension", gifWith("GIF89a", 4, 3, rgbTable, gifExtension(gifControlLabel, []byte{4, 10}),
			full, full), []string{"image", "image"}, false},
		{"data after the trailer", append(gifWith("GIF89a", 4, 3, rgbTable, full, full), full...),
			[]string{"image", "image"}, true},
		{"a byte that is not a block", gifWith("GIF89a", 4, 3, rgbTable, full, full, []byte{0}, full),
			[]string{"image", "image"}, false},
		{"no trailer", gifWith("GIF89a", 4, 3, rgbTable, full, full)[:len(gifWith("GIF89a", 4, 3, rgbTable, full,
			full))-1], []string{"image", "image"}, false},
		{"data after the end of the LZW data", gifWith("GIF89a", 4, 3, rgbTable,
			slices.Concat(full[:len(full)-1], []byte{3, 1, 2, 3, 0}), full), []string{"image", "image"}, false},
		{"more indexes", gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, append(slices.Clone(cover), cover...))), full),
			[]string{"image", "image"}, false},
		{"frame outside the screen", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(3, 2, 4, 3, 0, nil, 2,
			lzwOf(t, 2, cover))), []string{"image", "image"}, false},
		{"frames of no pixels", gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 0, 0, 0, nil, 2, lzwOf(t, 2, nil)),
			gifImageWith(0, 0, 0, 3, 0, nil, 2, lzwOf(t, 2, nil))), []string{"image", "image"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := rebuildGIF(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := gifBlockNames(t, out); !slices.Equal(got, tt.blocks) {
				t.Errorf("blocks %q, want %q", got, tt.blocks)
			}
			if tt.same {
				checkSameGIF(t, tt.in, out)
			} else if _, err := gif.DecodeAll(bytes.NewReader(out)); err != nil &&
				!strings.Contains(tt.name, "outside") {
				t.Errorf("gif.DecodeAll() = %v", err)
			}
		})
	}
}

func TestRebuildGIFFrames(t *testing.T) {
	// What the rebuilt frames have, read by gif.DecodeAll, where it cannot read the original.
	cover := indexesOf(4, 3, 4)
	full := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))
	for _, in := range [][]byte{
		gifWith("GIF89a", 4, 3, rgbTable, slices.Concat(full[:len(full)-1], []byte{3, 1, 2, 3, 0}), full),
		gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, append(slices.Clone(cover),
			1, 2))), full),
		gifWith("GIF89a", 4, 3, rgbTable, gifExtension(gifControlLabel, []byte{4, 10, 0, 1, 9, 9, 9}), full, full),
	} {
		out, err := rebuildGIF(in)
		if err != nil {
			t.Fatal(err)
		}
		g, err := gif.DecodeAll(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range g.Image {
			if !bytes.Equal(m.Pix, cover) {
				t.Errorf("frame %d: %v, want %v", i, m.Pix, cover)
			}
		}
	}
}

func TestRebuildGIFError(t *testing.T) {
	cover := indexesOf(4, 3, 4)
	full := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))
	file := gifWith("GIF89a", 4, 3, rgbTable, full, full)
	for _, tt := range []struct {
		name string
		in   []byte
		err  string
	}{
		{"fewer indexes", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 4, 3, 0, nil, 2,
			lzwOf(t, 2, cover[:11]))), "invalid GIF: frame 1 has 11 of 12 pixels: unexpected EOF"},
		{"invalid code", gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 2, []byte{0xff, 0xff})),
			"invalid GIF: frame 0 has 0 of 12 pixels: lzw: invalid code"},
		{"truncated", file[:len(file)-3], "truncated GIF"},
		{"truncated in a descriptor", slices.Concat(file[:len(file)-1], full[:5]), "truncated GIF"},
		{"minimum code size", gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 12, []byte{0})),
			"invalid GIF: LZW minimum code size 12"},
		{"not GIF", []byte("GIF89"), "not a GIF file"},
	} {
		if out, err := rebuildGIF(tt.in); err == nil || err.Error() != tt.err {
			t.Errorf("%s: rebuildGIF() = %d bytes, %v; want %q", tt.name, len(out), err, tt.err)
		}
	}
}

// TestCheckGIFFrames checks that checkGIFFrames finds the frames that rebuildGIF cannot rebuild, with its error.
func TestCheckGIFFrames(t *testing.T) {
	cover := indexesOf(4, 3, 4)
	full := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))
	for _, tt := range []struct {
		name string
		data []byte
		err  string
	}{
		{"whole", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(1, 1, 2, 2, 0x80, rgbTable[:6], 3,
			lzwOf(t, 3, []byte{1, 0, 0, 1}))), ""},
		{"interlaced", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 4, 3, 0x40, nil, 2,
			lzwOf(t, 2, cover))), ""},
		{"more indexes", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 4, 3, 0, nil, 2,
			lzwOf(t, 2, append(slices.Clone(cover), 1, 2)))), ""},
		{"invalid code after the indexes", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 1, 1, 0, nil, 2,
			lzwInvalidAfter1)), ""},
		{"no pixels", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 0, 3, 0, nil, 2, nil)), ""},
		{"no trailer", gifWith("GIF89a", 4, 3, rgbTable, full, full)[:len(gifWith("GIF89a", 4, 3, rgbTable, full,
			full))-1], ""},
		{"fewer indexes", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 4, 3, 0, nil, 2,
			lzwOf(t, 2, cover[:11]))), "invalid GIF: frame 1 has 11 of 12 pixels: unexpected EOF"},
		{"no data", gifWith("GIF89a", 4, 3, rgbTable, full, full, gifImageWith(1, 1, 2, 2, 0, nil, 2, nil)),
			"invalid GIF: frame 2 has 0 of 4 pixels: unexpected EOF"},
		{"invalid code", gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 2, []byte{0xff, 0xff}),
			full), "invalid GIF: frame 0 has 0 of 12 pixels: lzw: invalid code"},
		{"invalid code after the first index", gifWith("GIF89a", 4, 3, rgbTable, full, gifImageWith(0, 0, 2, 1, 0,
			nil, 2, lzwInvalidAfter1)), "invalid GIF: frame 1 has 1 of 2 pixels: lzw: invalid code"},
	} {
		err := checkGIFFrames(tt.data)
		_, rerr := rebuildGIF(tt.data)
		if fmtErr(err) != tt.err || fmtErr(rerr) != tt.err {
			t.Errorf("%s: checkGIFFrames() = %v, rebuildGIF() = %v; want %q", tt.name, err, rerr, tt.err)
		}
	}
}

func TestScanGIF(t *testing.T) {
	full := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, indexesOf(4, 3, 4)))
	one := gifWith("GIF89a", 4, 3, rgbTable, full)
	for _, tt := range []struct {
		name string
		data []byte
		want gifScan
		err  string
	}{
		{name: "two frames", data: gifWith("GIF89a", 4, 3, rgbTable, full, full),
			want: gifScan{frames: 2, pixels: 24, complete: true}},
		{name: "frame larger than the screen", data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 200, 100, 0, nil, 2, nil)), want: gifScan{frames: 1, pixels: 20000, complete: true}},
		{name: "truncated", data: slices.Concat(one[:len(one)-1], full[:15]),
			want: gifScan{frames: 1, pixels: 12}},
		{name: "minimum code size 1", data: gifWith("GIF89a", 4, 3, rgbTable, gifImageWith(0, 0, 4, 3, 0, nil, 1, nil)),
			err: "invalid GIF: LZW minimum code size 1"},
		{name: "minimum code size 9 in the second frame", data: gifWith("GIF89a", 4, 3, rgbTable, full,
			gifImageWith(0, 0, 4, 3, 0, nil, 9, nil)), err: "invalid GIF: LZW minimum code size 9"},
		{name: "first frame of too many pixels", data: gifWith("GIF89a", 4, 3, rgbTable,
			gifImageWith(0, 0, 10001, 10000, 0, nil, 2, nil)),
			err: "too many pixels in the first GIF frame: 10001x10000 (max 100000000)"},
		{name: "second frame of many pixels", data: gifWith("GIF89a", 4, 3, rgbTable, full,
			gifImageWith(0, 0, 10001, 10000, 0, nil, 2, nil)),
			want: gifScan{frames: 2, pixels: 12 + 10001*10000, complete: true}},
	} {
		got, err := scanGIF(tt.data)
		if got != tt.want || fmtErr(err) != tt.err {
			t.Errorf("%s: scanGIF() = %+v, %v; want %+v, %q", tt.name, got, err, tt.want, tt.err)
		}
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
