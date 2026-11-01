package webp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
)

// containerCase is a file for Parse: an error that contains err, or if err is "", a File that check accepts.
type containerCase struct {
	name  string
	data  []byte
	err   string
	check func(f *File) bool
}

func containerCases() []containerCase {
	solid := func(w, h int) []byte { return chunk("VP8L", vp8lSolid(w, h, 0xff102030)) }
	alpha := chunk("VP8L", withAlphaBit(vp8lImage(2, 1, 0x80102030, 0xff405060)))
	still := riff(solid(2, 2))
	stillChunks := func(flags byte, chunks ...[]byte) []byte {
		return riff(append([][]byte{vp8xChunk(flags, 2, 2)}, chunks...)...)
	}
	frame := func(x, y int, method byte, extra ...[]byte) []byte {
		return anmfChunk(x, y, 2, 2, 70, method, append([][]byte{solid(2, 2)}, extra...)...)
	}
	anim := func(chunks ...[]byte) []byte {
		return riff(append([][]byte{vp8xChunk(flagAnimation, 6, 4)}, chunks...)...)
	}
	bg := [4]byte{1, 2, 3, 4}
	setSize := func(b []byte, size uint32) []byte {
		b = slices.Clone(b)
		binary.LittleEndian.PutUint32(b[4:], size)
		return b
	}
	payload := func(f *File) []byte { return f.Frames[0].bitstream }
	vp8 := func(tag0 byte, w, h int, startCode ...byte) []byte {
		b := fakeVP8(w, h)
		b[0] = tag0
		copy(b[3:], startCode)
		return riff(chunk("VP8 ", b))
	}
	return []containerCase{
		{name: "empty", data: nil, err: "not a WebP file"},
		{name: "RIFF header only", data: []byte("RIFF\x04\x00\x00\x00WEB"), err: "not a WebP file"},
		{name: "not WEBP", data: []byte("RIFF\x04\x00\x00\x00WAVE"), err: "not a WebP file"},
		{name: "RIFF size under 4", data: setSize(still, 3), err: "invalid RIFF size"},
		{name: "RIFF size over the limit", data: setSize(still, 0xfffffff7), err: "invalid RIFF size"},
		{name: "truncated", data: still[:len(still)-1], err: "past the end of the data"},
		{name: "no chunks", data: []byte("RIFF\x04\x00\x00\x00WEBP"), err: "no chunks"},
		{name: "data after RIFF", data: append(slices.Clone(still), "trailing data"...), check: func(f *File) bool {
			return f.Width == 2 && f.Height == 2 && f.Lossless && f.flags == 0
		}},
		{name: "chunks after a simple image", data: riff(solid(2, 2), chunk("EXIF", []byte("II*\x00")), []byte("junk")),
			check: func(f *File) bool { return f.EXIF == nil && len(f.Frames) == 1 }},
		{name: "odd chunk without its padding", data: setSize(riff(alpha), uint32(len(riff(alpha))-9)),
			err: "runs past its parent"},
		{name: "odd chunk with its padding", data: riff(alpha), check: func(f *File) bool {
			return len(payload(f))%2 == 1 && f.Alpha
		}},
		{name: "chunk past the RIFF", data: setSize(still, uint32(len(still)-10)), err: "runs past its parent"},
		{name: "data after RIFF, extended", data: append(stillChunks(0, solid(2, 2)), "XYZW"...),
			check: func(f *File) bool { return f.Lossless }},
		{name: "truncated chunk header", data: riff(vp8xChunk(0, 2, 2), solid(2, 2), []byte("XYZW")),
			err: "truncated chunk header"},
		{name: "first chunk unknown", data: riff(chunk("ICCP", []byte("x")), solid(2, 2)), err: "first chunk"},
		{name: "first chunk ALPH", data: riff(chunk("ALPH", []byte{0}), solid(2, 2)), err: "first chunk"},

		{name: "VP8X too short", data: riff(chunk("VP8X", make([]byte, 9)), solid(2, 2)), err: "VP8X chunk too short"},
		{name: "VP8X longer", data: riff(chunk("VP8X", append(vp8xChunk(0, 2, 2)[8:], 1, 2)), solid(2, 2)),
			check: func(f *File) bool { return f.Width == 2 && !f.Animated }},
		{name: "VP8X reserved bits", data: stillChunks(0xc1, solid(2, 2)), check: func(f *File) bool {
			return !f.Animated && f.Lossless && f.flags == 0xc1
		}},
		{name: "canvas of 2^32 pixels", data: riff(vp8xChunk(flagAnimation, 1<<16, 1<<16), animChunk(bg, 0),
			frame(0, 0, 0)), err: "canvas of 65536x65536 pixels too large"},
		{name: "canvas of 2^24 x 2^24", data: riff(vp8xChunk(flagAnimation, 1<<24, 1<<24), animChunk(bg, 0),
			frame(0, 0, 0)), err: "too large"},
		{name: "canvas of 2^32-1 pixels", data: riff(vp8xChunk(flagAnimation, 65537, 65535), animChunk(bg, 0),
			frame(0, 0, 0)), check: func(f *File) bool { return f.Width == 65537 && f.Height == 65535 }},
		{name: "second VP8X", data: stillChunks(0, vp8xChunk(0, 2, 2), solid(2, 2)), err: "second VP8X"},

		{name: "image size other than the canvas", data: stillChunks(0, solid(2, 3)), err: "on a canvas of 2x2"},
		{name: "no image", data: stillChunks(flagEXIF, chunk("EXIF", []byte("II"))), err: "no image data"},
		{name: "ALPH only", data: stillChunks(0, chunk("ALPH", []byte{0})), err: "no image data"},
		{name: "two ALPH chunks", data: stillChunks(0, chunk("ALPH", []byte{0}), chunk("ALPH", []byte{0}),
			chunk("VP8 ", fakeVP8(2, 2))), err: "two ALPH chunks"},
		{name: "ALPH and VP8", data: stillChunks(0, chunk("ALPH", []byte{0, 1, 2, 3, 4}), chunk("VP8 ", fakeVP8(2, 2))),
			check: func(f *File) bool {
				return f.Alpha && !f.Lossless && bytes.Equal(f.Frames[0].alphaData, []byte{0, 1, 2, 3, 4})
			}},
		{name: "ALPH, unknown chunk and VP8", data: stillChunks(0, chunk("ALPH", []byte{0, 1, 2, 3, 4}),
			chunk("XYZW", nil), chunk("EXIF", nil), chunk("VP8 ", fakeVP8(2, 2))), check: func(f *File) bool {
			return f.Alpha && f.Frames[0].alphaData != nil
		}},
		{name: "ALPH with reserved bits", data: stillChunks(0, chunk("ALPH", []byte{0x40, 1, 2, 3, 4}),
			chunk("VP8 ", fakeVP8(2, 2))), err: "reserved bits of ALPH set"},
		{name: "ALPH of pre-processing 2", data: stillChunks(0, chunk("ALPH", []byte{0x20, 1, 2, 3, 4}),
			chunk("VP8 ", fakeVP8(2, 2))), err: "unknown ALPH pre-processing 2"},
		{name: "ALPH of compression 2", data: stillChunks(0, chunk("ALPH", []byte{2, 1, 2, 3, 4}),
			chunk("VP8 ", fakeVP8(2, 2))), err: "unknown ALPH compression 2"},
		{name: "ALPH of the header alone", data: stillChunks(0, chunk("ALPH", []byte{1}), chunk("VP8 ", fakeVP8(2, 2))),
			err: "ALPH chunk too short"},
		{name: "raw ALPH data too short", data: stillChunks(0, chunk("ALPH", []byte{0, 1, 2, 3}),
			chunk("VP8 ", fakeVP8(2, 2))), err: "raw ALPH data of 3 bytes for 2x2 pixels"},
		{name: "ALPH of pre-processing 1 and every filter", data: stillChunks(0, chunk("ALPH", []byte{0x1c, 1, 2, 3, 4}),
			chunk("VP8 ", fakeVP8(2, 2))), check: func(f *File) bool { return f.Alpha }},
		{name: "ALPH in a frame with reserved bits", data: anim(animChunk(bg, 0), anmfChunk(0, 0, 2, 2, 0, 0,
			chunk("ALPH", []byte{0x80, 1, 2, 3, 4}), chunk("VP8 ", fakeVP8(2, 2)))), err: "reserved bits of ALPH set"},
		{name: "invalid ALPH and VP8L", data: stillChunks(0, chunk("ALPH", []byte{0xff}), solid(2, 2)),
			check: func(f *File) bool { return f.Lossless && f.Frames[0].alphaData == nil }},
		{name: "ALPH and VP8L", data: stillChunks(0, chunk("ALPH", []byte{0}), solid(2, 2)), check: func(f *File) bool {
			return !f.Alpha && f.Lossless && f.Frames[0].alphaData == nil
		}},
		{name: "ALPH after the image", data: stillChunks(0, chunk("VP8 ", fakeVP8(2, 2)), chunk("ALPH", []byte{0})),
			check: func(f *File) bool { return !f.Alpha && f.Frames[0].alphaData == nil }},
		{name: "second image", data: stillChunks(0, solid(2, 2), chunk("VP8 ", fakeVP8(3, 3))),
			check: func(f *File) bool { return f.Lossless && len(f.Frames) == 1 }},
		{name: "ICCP, EXIF and XMP", data: stillChunks(flagICC|flagEXIF|flagXMP, chunk("ICCP", []byte("icc")),
			chunk("EXIF", []byte("exif")), solid(2, 2), chunk("XMP ", []byte("xmp")), chunk("EXIF", []byte("2")),
			chunk("XMP ", []byte("2")), chunk("ICCP", []byte("2"))), check: func(f *File) bool {
			return string(f.ICC) == "icc" && string(f.EXIF) == "exif" && string(f.XMP) == "xmp"
		}},
		{name: "empty metadata", data: stillChunks(flagEXIF|flagXMP, chunk("EXIF", nil), chunk("XMP ", nil),
			solid(2, 2)), check: func(f *File) bool { return f.EXIF != nil && len(f.EXIF) == 0 && f.XMP != nil }},
		{name: "metadata without flags", data: stillChunks(0, chunk("ICCP", []byte("icc")), chunk("EXIF", []byte("e")),
			solid(2, 2), chunk("XMP ", []byte("x")), chunk("ICCP", []byte("late"))), check: func(f *File) bool {
			return f.ICC == nil && f.EXIF == nil && f.XMP == nil
		}},
		{name: "flags without metadata", data: stillChunks(flagICC|flagEXIF|flagXMP, solid(2, 2)),
			check: func(f *File) bool { return f.ICC == nil && f.EXIF == nil && f.XMP == nil }},
		{name: "ICCP after the image", data: stillChunks(flagICC, solid(2, 2), chunk("ICCP", []byte("icc"))),
			err: "ICCP chunk after the image data"},
		{name: "ICCP after ALPH", data: stillChunks(flagICC, chunk("ALPH", []byte{0}), chunk("ICCP", []byte("icc")),
			chunk("VP8 ", fakeVP8(2, 2))), err: "ICCP chunk after the image data"},
		{name: "ANIM and ANMF in a still image", data: stillChunks(0, animChunk(bg, 3), frame(0, 0, 0), solid(2, 2),
			chunk("ANMF", nil)), check: func(f *File) bool {
			return !f.Animated && f.LoopCount == 0 && f.Background == [4]byte{} && len(f.Frames) == 1
		}},
		{name: "unknown chunks", data: stillChunks(0, chunk("XYZW", []byte("123")), solid(2, 2), chunk("abcd", nil)),
			check: func(f *File) bool { return f.Lossless }},

		{name: "animation", data: anim(chunk("XYZW", nil), animChunk(bg, 7), frame(2, 0, 0), chunk("EXIF", nil),
			frame(4, 2, 1, chunk("XYZW", []byte("x"))), frame(0, 2, 2), frame(2, 2, 3)), check: func(f *File) bool {
			want := []Frame{{X: 2, Y: 0, Blend: true}, {X: 4, Y: 2, Blend: true, Dispose: true}, {X: 0, Y: 2},
				{X: 2, Y: 2, Dispose: true}}
			for i := range want {
				want[i].Width, want[i].Height, want[i].Duration = 2, 2, 70
			}
			return f.Animated && !f.Lossless && !f.Alpha && f.LoopCount == 7 && f.Background == bg &&
				slices.EqualFunc(f.Frames, want, func(a, b Frame) bool {
					return a.X == b.X && a.Y == b.Y && a.Width == b.Width && a.Height == b.Height &&
						a.Duration == b.Duration && a.Blend == b.Blend && a.Dispose == b.Dispose && a.lossless
				})
		}},
		{name: "one frame", data: anim(animChunk(bg, 0), frame(0, 0, 0)), check: func(f *File) bool {
			return f.Animated && len(f.Frames) == 1
		}},
		{name: "frames with alpha", data: anim(animChunk(bg, 0), frame(0, 0, 0),
			anmfChunk(0, 0, 2, 1, 0, 0, alpha), anmfChunk(0, 0, 2, 2, 0, 0, chunk("ALPH", []byte{1, 0}),
				chunk("VP8 ", fakeVP8(2, 2)))), check: func(f *File) bool {
			return f.Alpha && !f.Frames[0].hasAlpha && f.Frames[1].hasAlpha && f.Frames[2].hasAlpha &&
				f.Frames[2].alphaData != nil
		}},
		{name: "ICCP of an animation", data: riff(vp8xChunk(flagAnimation|flagICC, 6, 4), chunk("ICCP", []byte("i")),
			animChunk(bg, 0), frame(0, 0, 0)), check: func(f *File) bool { return string(f.ICC) == "i" }},
		{name: "ICCP after ANIM", data: riff(vp8xChunk(flagAnimation|flagICC, 6, 4), animChunk(bg, 0),
			chunk("ICCP", []byte("i")), frame(0, 0, 0)), err: "ICCP chunk after the image data"},
		{name: "no ANIM", data: anim(), err: "without an ANIM chunk"},
		{name: "ANMF before ANIM", data: anim(frame(0, 0, 0), animChunk(bg, 0)), err: "ANMF chunk before ANIM"},
		{name: "ANIM after ANMF", data: anim(animChunk(bg, 0), frame(0, 0, 0), animChunk(bg, 0)),
			err: "ANIM chunk out of order"},
		{name: "two ANIM chunks", data: anim(animChunk(bg, 0), animChunk(bg, 0), frame(0, 0, 0)),
			err: "ANIM chunk out of order"},
		{name: "ANIM too short", data: anim(chunk("ANIM", make([]byte, 5)), frame(0, 0, 0)),
			err: "ANIM chunk too short"},
		{name: "no frames", data: anim(animChunk(bg, 0)), err: "no image data"},
		{name: "image outside ANMF", data: anim(animChunk(bg, 0), frame(0, 0, 0), solid(6, 4)),
			err: `"VP8L" chunk outside ANMF`},
		{name: "ALPH outside ANMF", data: anim(animChunk(bg, 0), chunk("ALPH", nil), frame(0, 0, 0)),
			err: `"ALPH" chunk outside ANMF`},
		{name: "ANMF too short", data: anim(animChunk(bg, 0), chunk("ANMF", make([]byte, 15))),
			err: "ANMF chunk too short"},
		{name: "frame past the right", data: anim(animChunk(bg, 0), frame(6, 0, 0)), err: "outside the canvas"},
		{name: "frame past the bottom", data: anim(animChunk(bg, 0), frame(0, 4, 0)), err: "outside the canvas"},
		{name: "frame without a bitstream", data: anim(animChunk(bg, 0), anmfChunk(0, 0, 2, 2, 0, 0,
			chunk("ALPH", nil), chunk("XYZW", nil))), err: "frame without a bitstream"},
		{name: "empty frame", data: anim(animChunk(bg, 0), anmfChunk(0, 0, 2, 2, 0, 0)),
			err: "frame without a bitstream"},
		{name: "bitstream of another size", data: anim(animChunk(bg, 0), anmfChunk(0, 0, 2, 3, 0, 0, solid(2, 2))),
			err: "bitstream of 2x2 pixels in a frame of 2x3"},
		{name: "two bitstreams in a frame", data: anim(animChunk(bg, 0), frame(0, 0, 0, solid(2, 2))),
			err: "two bitstreams in a frame"},
		{name: "two ALPH chunks in a frame", data: anim(animChunk(bg, 0), anmfChunk(0, 0, 2, 2, 0, 0,
			chunk("ALPH", nil), chunk("ALPH", nil), chunk("VP8 ", fakeVP8(2, 2)))), err: "two ALPH chunks in a frame"},
		{name: "ALPH after the bitstream of a frame", data: anim(animChunk(bg, 0), frame(0, 0, 0, chunk("ALPH", nil))),
			err: "ALPH chunk after the bitstream"},
		{name: "ALPH with VP8L in a frame", data: anim(animChunk(bg, 0), anmfChunk(0, 0, 2, 2, 0, 0, chunk("ALPH", nil),
			solid(2, 2))), check: func(f *File) bool { return !f.Alpha && f.Frames[0].alphaData == nil }},
		{name: "subchunk past the ANMF", data: anim(animChunk(bg, 0), setFrameSize(frame(0, 0, 0), -2)),
			err: "runs past its parent"},

		{name: "VP8", data: riff(chunk("VP8 ", fakeVP8(5, 3))), check: func(f *File) bool {
			return f.Width == 5 && f.Height == 3 && !f.Lossless && !f.Alpha && f.Frames[0].Width == 5
		}},
		{name: "VP8 with scaling bits", data: riff(chunk("VP8 ", append(fakeVP8(5, 3)[:6], 5, 0xc0, 3, 0x40))),
			check: func(f *File) bool { return f.Width == 5 && f.Height == 3 }},
		{name: "VP8 too short", data: riff(chunk("VP8 ", fakeVP8(5, 3)[:9])), err: "VP8 bitstream too short"},
		{name: "VP8 interframe", data: vp8(0x11, 5, 3), err: "not a key frame"},
		{name: "VP8 version 4", data: vp8(0x18, 5, 3), err: "unknown VP8 version 4"},
		{name: "VP8 version 3", data: vp8(0x16, 5, 3), check: func(f *File) bool { return f.Width == 5 }},
		{name: "VP8 not shown", data: vp8(0x00, 5, 3), err: "not shown"},
		{name: "VP8 first partition past the end", data: vp8(0x30, 5, 3), err: "first VP8 partition"},
		{name: "VP8 start code", data: vp8(0x10, 5, 3, 0x9d, 0x01, 0x2b), err: "missing VP8 start code"},
		{name: "VP8 of no width", data: vp8(0x10, 0, 3), err: "VP8 frame of 0x3 pixels"},
		{name: "VP8 of no height", data: vp8(0x10, 3, 0x4000), err: "VP8 frame of 3x0 pixels"},
		{name: "VP8L too short", data: riff(chunk("VP8L", []byte{0x2f, 0, 0, 0})), err: "VP8L bitstream too short"},
		{name: "VP8L signature", data: riff(chunk("VP8L", append([]byte{0x2e}, vp8lSolid(2, 2, 0)[1:]...))),
			err: "missing VP8L signature"},
		{name: "VP8L version", data: riff(chunk("VP8L", vp8lVersion(vp8lSolid(2, 2, 0), 1))),
			err: "unknown VP8L version 1"},
	}
}

// setFrameSize changes the size of the payload of an ANMF chunk by d, keeping its data.
func setFrameSize(anmf []byte, d int) []byte {
	b := slices.Clone(anmf)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8+d))
	return b
}

func vp8lVersion(b []byte, v byte) []byte {
	b = slices.Clone(b)
	b[4] = b[4]&0x1f | v<<5
	return b
}

func TestParse(t *testing.T) {
	for _, tt := range containerCases() {
		f, err := Parse(tt.data)
		switch {
		case tt.err != "":
			if err == nil || !strings.Contains(err.Error(), tt.err) || !strings.HasPrefix(err.Error(), "webp: ") {
				t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.err)
			}
			if f != nil {
				t.Errorf("%s: a File with the error", tt.name)
			}
		case err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case !tt.check(f):
			t.Errorf("%s: %+v", tt.name, f)
		}
	}
}

func TestDecodeConfig(t *testing.T) {
	for _, tt := range []struct {
		name  string
		data  []byte
		model color.Model
		w, h  int
	}{
		{"opaque", riff(chunk("VP8L", vp8lSolid(3, 2, 0xff102030))), color.RGBAModel, 3, 2},
		{"lossy", riff(chunk("VP8 ", fakeVP8(5, 4))), color.RGBAModel, 5, 4},
		{"alpha_is_used", riff(chunk("VP8L", withAlphaBit(vp8lSolid(2, 1, 0xff102030)))), color.NRGBAModel, 2, 1},
		{"animation", riff(vp8xChunk(flagAnimation, 6, 4), animChunk([4]byte{}, 0),
			anmfChunk(0, 0, 2, 2, 0, 0, chunk("VP8L", vp8lSolid(2, 2, 0xff102030)))), color.NRGBAModel, 6, 4},
	} {
		c, err := DecodeConfig(bytes.NewReader(tt.data))
		if err != nil || c.ColorModel != tt.model || c.Width != tt.w || c.Height != tt.h {
			t.Errorf("%s: %v, %v", tt.name, c, err)
		}
	}
	if _, err := DecodeConfig(bytes.NewReader([]byte("RIFF"))); err == nil {
		t.Error("no error for a short file")
	}
	if _, err := DecodeConfig(errReader{}); err == nil {
		t.Error("no error for a failing reader")
	}
	if _, err := Decode(errReader{}); err == nil {
		t.Error("Decode: no error for a failing reader")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read error") }

func TestImage(t *testing.T) {
	// An animation's first frame on its canvas.
	f, err := Parse(riff(vp8xChunk(flagAnimation, 5, 4), animChunk([4]byte{9, 9, 9, 9}, 0),
		anmfChunk(2, 2, 2, 1, 0, 0, chunk("VP8L", vp8lImage(2, 1, 0xff102030, 0x00405060))),
		anmfChunk(0, 0, 1, 1, 0, 0, chunk("VP8 ", fakeVP8(1, 1)))))
	if err != nil {
		t.Fatal(err)
	}
	img, err := f.Image()
	if err != nil {
		t.Fatal(err)
	}
	canvas, ok := img.(*image.NRGBA)
	if !ok || canvas.Rect != image.Rect(0, 0, 5, 4) {
		t.Fatalf("%T of %v", img, img.Bounds())
	}
	for y := range 4 {
		for x := range 5 {
			want := color.NRGBA{}
			switch {
			case x == 2 && y == 2:
				want = color.NRGBA{0x10, 0x20, 0x30, 0xff}
			case x == 3 && y == 2:
				want = color.NRGBA{0x40, 0x50, 0x60, 0}
			}
			if got := canvas.NRGBAAt(x, y); got != want {
				t.Errorf("(%d, %d) is %v, want %v", x, y, got, want)
			}
		}
	}
	for _, i := range []int{-1, 2} {
		if _, err := f.FrameImage(i); err == nil {
			t.Errorf("FrameImage(%d): no error", i)
		}
	}
	if _, err := f.FrameImage(1); err == nil {
		t.Error("FrameImage of a lossy frame without data: no error")
	}

	// A lossless still image of opaque pixels whose alpha_is_used is set is *image.NRGBA.
	var w bitWriter
	w.header(1, 1, true)
	w.write(0, 1)
	w.imageData(0, true, lits(0xff102030))
	img, err = Decode(bytes.NewReader(riff(chunk("VP8L", w.bytes()))))
	if _, ok := img.(*image.NRGBA); !ok || err != nil {
		t.Errorf("%T, %v", img, err)
	}

	// Errors of the bitstream.
	for _, data := range [][]byte{
		riff(chunk("VP8L", vp8lSolid(2, 2, 0)[:6])),
		riff(vp8xChunk(flagAnimation, 5, 4), animChunk([4]byte{}, 0), anmfChunk(0, 0, 2, 2, 0, 0,
			chunk("VP8L", vp8lSolid(2, 2, 0)[:6]))),
		riff(chunk("VP8 ", fakeVP8(2, 2))),
	} {
		f, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Image(); err == nil {
			t.Errorf("Image of % x: no error", data)
		}
	}
}
