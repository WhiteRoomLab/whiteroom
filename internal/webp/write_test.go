package webp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
)

// writeCase is a file and what WriteWithoutMetadata must write for it.
type writeCase struct {
	name      string
	data, out []byte
}

func writeCases() []writeCase {
	lossless := chunk("VP8L", vp8lSolid(2, 2, 0xff102030))
	odd := chunk("VP8L", withAlphaBit(vp8lImage(2, 1, 0x80102030, 0xff405060)))
	vp8 := chunk("VP8 ", fakeVP8(2, 2))
	alph := chunk("ALPH", []byte{0x0d, 1, 2, 3, 4})
	icc, exif, xmp := chunk("ICCP", []byte("icc")), chunk("EXIF", []byte("exif")), chunk("XMP ", []byte("xmp!"))
	unknown := chunk("XYZW", []byte("unknown"))
	bg := [4]byte{1, 2, 3, 4}
	frame := func(x, y int, method byte, chunks ...[]byte) []byte {
		return anmfChunk(x, y, 2, 2, 50, method, chunks...)
	}
	padded := func(c []byte, pad byte) []byte {
		c = slices.Clone(c)
		c[len(c)-1] = pad
		return c
	}
	longer := func(c []byte, extra ...byte) []byte {
		return chunk(string(c[:4]), append(slices.Clone(c[8:8+binary.LittleEndian.Uint32(c[4:])]), extra...))
	}
	return []writeCase{
		{"simple VP8L", riff(lossless), riff(lossless)},
		{"simple VP8", riff(vp8), riff(vp8)},
		{"simple, with chunks and data after the image", append(riff(lossless, exif, unknown), "trailing"...),
			riff(lossless)},
		{"simple, padded with a byte other than 0", riff(padded(odd, 0xff)), riff(odd)},
		{"extended still image", riff(vp8xChunk(flagEXIF|flagXMP, 2, 2), lossless, exif, xmp),
			riff(vp8xChunk(0, 2, 2), lossless)},
		{"extended without metadata", riff(vp8xChunk(flagICC|flagAlpha, 2, 2), icc, alph, vp8),
			riff(vp8xChunk(flagICC|flagAlpha, 2, 2), icc, alph, vp8)},
		{"every chunk of a still image", riff(longer(vp8xChunk(0xc1|flagICC|flagAlpha|flagEXIF|flagXMP, 2, 2), 9, 9),
			icc, unknown, xmp, alph, exif, unknown, vp8, unknown, lossless, chunk("ICCP", []byte("second"))),
			riff(vp8xChunk(flagICC|flagAlpha, 2, 2), icc, alph, vp8)},
		{"ALPH before VP8L", riff(vp8xChunk(flagAlpha, 2, 1), alph, padded(odd, 7)),
			riff(vp8xChunk(flagAlpha, 2, 1), odd)},
		{"ICCP without its flag", riff(vp8xChunk(0, 2, 2), icc, lossless), riff(vp8xChunk(0, 2, 2), lossless)},
		{"ICC flag without ICCP", riff(vp8xChunk(flagICC, 2, 2), lossless), riff(vp8xChunk(0, 2, 2), lossless)},
		{"ALPH without the Alpha flag", riff(vp8xChunk(0, 2, 2), alph, vp8),
			riff(vp8xChunk(flagAlpha, 2, 2), alph, vp8)},
		{"VP8L with alpha without the Alpha flag", riff(vp8xChunk(0, 2, 1), odd), riff(vp8xChunk(flagAlpha, 2, 1), odd)},
		{"Alpha flag without alpha", riff(vp8xChunk(flagAlpha, 2, 2), vp8), riff(vp8xChunk(flagAlpha, 2, 2), vp8)},
		{"animation without its flags", riff(vp8xChunk(flagAnimation, 6, 4), animChunk(bg, 0), frame(0, 0, 0, alph, vp8)),
			riff(vp8xChunk(flagAnimation|flagAlpha, 6, 4), animChunk(bg, 0), frame(0, 0, 0, alph, vp8))},
		{"animation", riff(vp8xChunk(flagAnimation|flagAlpha|flagEXIF|flagXMP|flagICC|0x01, 6, 4), icc, xmp,
			longer(animChunk(bg, 3), 5, 5), unknown, frame(0, 0, 0xfc, lossless, unknown), exif,
			frame(2, 2, 0xff, alph, unknown, vp8, exif), frame(4, 0, 0x02, lossless), unknown),
			riff(vp8xChunk(flagAnimation|flagAlpha|flagICC, 6, 4), icc, animChunk(bg, 3), frame(0, 0, 0, lossless),
				frame(2, 2, 3, alph, vp8), frame(4, 0, 2, lossless))},
		{"ANIM and ANMF of a still image", riff(vp8xChunk(0, 2, 2), animChunk(bg, 1), frame(0, 0, 0, lossless),
			lossless), riff(vp8xChunk(0, 2, 2), lossless)},
	}
}

func TestWriteWithoutMetadata(t *testing.T) {
	for _, tt := range writeCases() {
		f, err := Parse(tt.data)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		var buf bytes.Buffer
		if err := f.WriteWithoutMetadata(&buf); err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if !bytes.Equal(buf.Bytes(), tt.out) {
			t.Errorf("%s: wrote\n% x\nwant\n% x", tt.name, buf.Bytes(), tt.out)
		}
		checkRemux(t, f, buf.Bytes())
	}
	if err := new(File).WriteWithoutMetadata(new(bytes.Buffer)); err == nil {
		t.Error("no error for a File without frames")
	}
	f, err := Parse(riff(chunk("VP8L", vp8lSolid(2, 2, 0))))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteWithoutMetadata(errWriter{}); !errors.Is(err, errWrite) {
		t.Errorf("%v, want the error of the writer", err)
	}
}

var errWrite = errors.New("write error")

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errWrite }
