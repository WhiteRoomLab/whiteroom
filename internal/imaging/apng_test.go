package imaging

import (
	"bytes"
	"image/png"
	"slices"
	"testing"
)

func TestFilterAPNG(t *testing.T) {
	kept := [][]byte{
		pngChunk("sRGB", []byte{0}), pngChunk("gAMA", be.AppendUint32(nil, 45455)), pngChunk("cHRM", make([]byte, 32)),
		pngChunk("cICP", []byte{1, 13, 0, 1}), pngChunk("sBIT", []byte{8, 8, 8}), iccpChunk(t, p3Profile),
		pngChunk("mDCV", make([]byte, 24)), pngChunk("cLLI", make([]byte, 8)),
	}
	dropped := [][]byte{
		pngChunk("eXIf", exifTIFF(6)), pngChunk("tEXt", []byte("Comment\x00text")),
		pngChunk("zTXt", append([]byte("Comment\x00\x00"), zlibOf(t, []byte("text"))...)),
		pngChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00<x:xmpmeta/>")),
		pngChunk("tIME", []byte{7, 234, 11, 1, 12, 0, 0}), pngChunk("pHYs", make([]byte, 9)),
		pngChunk("bKGD", make([]byte, 6)), pngChunk("abCD", []byte("unknown ancillary")),
	}
	clean := apngFile(t, colored(3, 2), kept...)
	// A dropped chunk with a wrong CRC is not an error.
	broken := flip(pngChunk("tEXt", []byte("a\x00b")), 14)
	in := apngFile(t, colored(3, 2), slices.Concat(dropped, kept, [][]byte{broken})...)
	in = append(in, "data after IEND"...)
	out, err := filterAPNG(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, clean) {
		t.Errorf("filterAPNG() = chunks %q, want %q", chunkTypes(out), chunkTypes(clean))
	}
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("png.Decode() = %v", err)
	}
	if again, err := filterAPNG(out); err != nil || !bytes.Equal(again, out) {
		t.Errorf("filterAPNG() again = %d bytes, %v; want the same %d bytes", len(again), err, len(out))
	}
	want := []string{"IHDR", "acTL", "sRGB", "gAMA", "cHRM", "cICP", "sBIT", "iCCP", "mDCV", "cLLI", "fcTL", "IDAT",
		"fcTL", "fdAT", "IEND"}
	if got := chunkTypes(out); !slices.Equal(got, want) {
		t.Errorf("chunks %q, want %q", got, want)
	}
}

// flip returns a copy of data with the bits of the byte at off flipped.
func flip(data []byte, off int) []byte {
	c := slices.Clone(data)
	c[off] ^= 0xff
	return c
}

func chunkTypes(file []byte) []string {
	var types []string
	for _, c := range chunksOf(file) {
		types = append(types, c.typ)
	}
	return types
}

func TestFilterAPNGError(t *testing.T) {
	file := apngFile(t, colored(3, 2))
	iend := len(file) - 12
	for _, tt := range []struct {
		name string
		data []byte
		err  string
	}{
		{"not PNG", file[1:], "not a PNG file"},
		{"no IEND", file[:iend], "truncated PNG"},
		{"cut in IEND", file[:len(file)-1], "truncated PNG"},
		{"chunk past the end", insertPNG(file, patch32(pngChunk("tEXt", nil), 0, 1<<31)), "truncated PNG"},
		{"wrong CRC", flip(file, len(file)-1), `PNG chunk "IEND" with a wrong CRC`},
		{"wrong CRC of acTL", flip(file, pngIHDREnd+19), `PNG chunk "acTL" with a wrong CRC`},
		{"wrong data of IDAT", flip(file, pngIHDREnd+20+38+8), `PNG chunk "IDAT" with a wrong CRC`},
		{"unknown critical chunk", insertPNG(file, pngChunk("ABCD", nil)), `unknown critical PNG chunk "ABCD"`},
		{"invalid chunk type", insertPNG(file, pngChunk("ab1d", nil)), `invalid PNG chunk type "ab1d"`},
	} {
		if out, err := filterAPNG(tt.data); fmtErr(err) != tt.err {
			t.Errorf("%s: filterAPNG() = %d bytes, %v; want %q", tt.name, len(out), err, tt.err)
		}
	}
}
