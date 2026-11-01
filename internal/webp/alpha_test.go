package webp

import (
	"bytes"
	"encoding/hex"
	"math/rand/v2"
	"strings"
	"testing"
)

// refUnfilter transcribes RFC 9649 section 2.7.1.2: the predictor of each filtering method, 0 at the top left, the
// value to the left in the first row and the value above in the first column, and alpha = (predictor + X) % 256.
func refUnfilter(filtered []byte, w, h, method int) []byte {
	out := make([]byte, w*h)
	for y := range h {
		for x := range w {
			var pred int
			switch {
			case method == alphaFilterNone || x == 0 && y == 0:
				pred = 0
			case y == 0:
				pred = int(out[x-1])
			case x == 0:
				pred = int(out[(y-1)*w])
			default:
				a, b, c := int(out[y*w+x-1]), int(out[(y-1)*w+x]), int(out[(y-1)*w+x-1])
				switch method {
				case alphaFilterHorizontal:
					pred = a
				case alphaFilterVertical:
					pred = b
				case alphaFilterGradient:
					pred = min(max(a+b-c, 0), 255)
				}
			}
			out[y*w+x] = byte((pred + int(filtered[y*w+x])) % 256)
		}
	}
	return out
}

// rawAlpha returns an ALPH payload of raw data with a filtering method.
func rawAlpha(method int, data []byte) []byte {
	return append([]byte{byte(method << 2)}, data...)
}

func TestUnfilterAlpha(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 29))
	for w := 1; w <= 9; w++ {
		for h := 1; h <= 9; h++ {
			filtered := make([]byte, w*h)
			for i := range filtered {
				filtered[i] = byte(rng.IntN(256))
			}
			for method := range 4 {
				pix, err := decodeAlpha(rawAlpha(method, filtered), w, h)
				if err != nil {
					t.Fatalf("%dx%d, method %d: %v", w, h, method, err)
				}
				if got, want := alphaPlane(pix), refUnfilter(filtered, w, h, method); !bytes.Equal(got, want) {
					t.Errorf("%dx%d, method %d: % x, want % x", w, h, method, got, want)
				}
			}
		}
	}
}

// TestUnfilterAlphaDwebp checks planes against what dwebp decodes from them in ALPH chunks.
func TestUnfilterAlphaDwebp(t *testing.T) {
	ramp := make([]byte, 20)
	for i := range ramp {
		ramp[i] = byte(i*73 + 29)
	}
	clipping := []byte{
		250, 3, 250, 3, 250,
		3, 250, 3, 250, 3,
		250, 250, 0, 0, 128,
		0, 255, 1, 254, 2,
	}
	for _, tt := range []struct {
		w, h     int
		filtered []byte
		want     [4]string // by method
	}{
		{5, 4, ramp, [4]string{
			"1d66aff8418ad31c65aef74089d21b64adf63f88",
			"1d83322a6ba77a96fba99ede67395402afa5e46c",
			"1d83322a6ba7564e8f199e96d761340243cda0bc",
			"1d83322a6ba7d29dfaad9e0989b88602adf53e94",
		}},
		{4, 5, ramp, [4]string{
			"1d66aff8418ad31c65aef74089d21b64adf63f88",
			"1d83322a5ee8bbd7c37168a84c1e399df9ef2eb6",
			"1d83322a5e0d0546c3bbfc864c8d17eaf9835672",
			"1d83322a5e4ed3e7c361dd314cd21a64f9f57c4e",
		}},
		{1, 3, ramp[:3], [4]string{"1d66af", "1d8332", "1d8332", "1d8332"}},
		{3, 1, ramp[:3], [4]string{"1d66af", "1d8332", "1d8332", "1d8332"}},
		{1, 1, ramp[:1], [4]string{"1d", "1d", "1d", "1d"}},
		{5, 4, clipping, [4]string{
			"fa03fa03fa03fa03fa03fafa00008000ff01fe02",
			"fafdf7faf4fdf7faf4f7f7f1f1f171f7f6f7f5f7",
			"fafdf7faf4fdf7faf4f7f7f1faf477f7f0fbf279",
			"fafdf7faf4fdf9f6f3f0f7edeae764f7eceae564",
		}},
	} {
		for method, want := range tt.want {
			pix, err := decodeAlpha(rawAlpha(method, tt.filtered), tt.w, tt.h)
			if err != nil {
				t.Fatalf("%dx%d, method %d: %v", tt.w, tt.h, method, err)
			}
			if got := hex.EncodeToString(alphaPlane(pix)); got != want {
				t.Errorf("%dx%d, method %d: %s, want %s", tt.w, tt.h, method, got, want)
			}
		}
	}
}

// TestLosslessAlpha decodes ALPH chunks compressed with an image stream, whose green values are the alpha values.
func TestLosslessAlpha(t *testing.T) {
	const w, h = 4, 3
	filtered := []byte{9, 200, 3, 255, 0, 17, 128, 64, 1, 2, 250, 6}
	pix := make([]uint32, w*h)
	for i, g := range filtered {
		// Alpha, red and blue vary, and must not matter.
		pix[i] = uint32(20*i)<<24 | uint32(255-i)<<16 | uint32(g)<<8 | uint32(3*i)
	}
	var bw bitWriter
	bw.write(0, 1)
	bw.imageData(0, true, lits(pix...))
	stream := bw.bytes()
	for method := range 4 {
		got, err := decodeAlpha(append([]byte{byte(method<<2 | 1)}, stream...), w, h)
		if err != nil {
			t.Fatalf("method %d: %v", method, err)
		}
		if want := refUnfilter(filtered, w, h, method); !bytes.Equal(alphaPlane(got), want) {
			t.Errorf("method %d: % x, want % x", method, alphaPlane(got), want)
		}
	}
	// Bytes after the image are ignored; an image stream cut short is an error.
	if _, err := decodeAlpha(append(append([]byte{1}, stream...), 1, 2, 3), w, h); err != nil {
		t.Errorf("data after the image: %v", err)
	}
	if _, err := decodeAlpha(append([]byte{1}, stream[:len(stream)/2]...), w, h); err == nil {
		t.Error("no error for a truncated image stream")
	}
}

// TestAlphaHeader follows dwebp, which fails on every error here (with the status OUT_OF_MEMORY, as libwebp reports
// errors of ALPH headers) and decodes the rest.
func TestAlphaHeader(t *testing.T) {
	raw := bytes.Repeat([]byte{7}, 20)
	for _, tt := range []struct {
		name string
		data []byte
		err  string
	}{
		{"empty", nil, "ALPH chunk too short"},
		{"header of raw data alone", []byte{0}, "ALPH chunk too short"},
		{"header of an image stream alone", []byte{1}, "ALPH chunk too short"},
		{"reserved bits 1", append([]byte{0x40}, raw...), "reserved bits"},
		{"reserved bits 2", append([]byte{0x80}, raw...), "reserved bits"},
		{"reserved bits 3", append([]byte{0xc0}, raw...), "reserved bits"},
		{"pre-processing 2", append([]byte{0x20}, raw...), "unknown ALPH pre-processing 2"},
		{"pre-processing 3", append([]byte{0x30}, raw...), "unknown ALPH pre-processing 3"},
		{"compression 2", append([]byte{0x02}, raw...), "unknown ALPH compression 2"},
		{"compression 3", append([]byte{0x03}, raw...), "unknown ALPH compression 3"},
		{"raw data too short", append([]byte{0}, raw[:19]...), "raw ALPH data of 19 bytes for 5x4 pixels"},
		{"pre-processing 1", append([]byte{0x10}, raw...), ""},
		{"raw data too long", append([]byte{0}, append(raw, 1, 2, 3)...), ""},
	} {
		pix, err := decodeAlpha(tt.data, 5, 4)
		switch {
		case tt.err == "":
			if err != nil || !bytes.Equal(alphaPlane(pix), raw) {
				t.Errorf("%s: %v", tt.name, err)
			}
		case err == nil || !strings.Contains(err.Error(), tt.err):
			t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.err)
		}
		if err := checkAlpha(tt.data, 5, 4); (err == nil) != (tt.err == "") {
			t.Errorf("%s: checkAlpha: %v", tt.name, err)
		}
	}
}
