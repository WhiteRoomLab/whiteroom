package imaging

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"slices"
	"strings"
	"testing"
)

// patch returns a copy of data with b written at off.
func patch(data []byte, off int, b ...byte) []byte {
	c := slices.Clone(data)
	copy(c[off:], b)
	return c
}

func patchString(data []byte, off int, s string) []byte {
	return patch(data, off, []byte(s)...)
}

func patch32(data []byte, off int, v uint32) []byte {
	return patch(data, off, be.AppendUint32(nil, v)...)
}

// tagEntry returns the offset of the entry of the tag with sig in the tag table of p.
func tagEntry(t *testing.T, p []byte, sig string) int {
	t.Helper()
	for i := range int(be.Uint32(p[128:])) {
		if string(p[132+12*i:136+12*i]) == sig {
			return 132 + 12*i
		}
	}
	t.Fatalf("no tag %q", sig)
	return 0
}

func TestCheckICC(t *testing.T) {
	n := len(srgbProfile)
	rXYZ := tagEntry(t, srgbProfile, "rXYZ")
	huge := iccProfile("mntr", "RGB ", profileTag{"zzzz", make([]byte, maxICCBytes)})
	tests := []struct {
		name string
		p    []byte
		gray bool
		err  string // "" if valid
	}{
		{name: "sRGB", p: srgbProfile},
		{name: "Display P3", p: p3Profile},
		{name: "gray", p: grayProfile(srgbPara), gray: true},
		{name: "input device", p: patchString(srgbProfile, 12, "scnr")},
		{name: "output device", p: patchString(srgbProfile, 12, "prtr")},
		{name: "color space", p: patchString(srgbProfile, 12, "spac")},
		{name: "no tags", p: iccProfile("mntr", "RGB ")},
		{name: "tag at the end", p: patch32(srgbProfile, rXYZ+4, uint32(n-20))},
		{name: "empty", p: nil, err: "only 0 bytes"},
		{name: "short", p: srgbProfile[:131], err: "only 131 bytes"},
		{name: "size field larger", p: patch32(srgbProfile, 0, uint32(n+1)),
			err: fmt.Sprintf("size field %d, but %d bytes", n+1, n)},
		{name: "size field smaller", p: patch32(srgbProfile, 0, uint32(n-1)),
			err: fmt.Sprintf("size field %d, but %d bytes", n-1, n)},
		{name: "data after the profile", p: append(slices.Clone(srgbProfile), 0, 0, 0, 0),
			err: fmt.Sprintf("size field %d, but %d bytes", n, n+4)},
		{name: "no signature", p: patchString(srgbProfile, 36, "ACSP"), err: `no "acsp" signature`},
		{name: "device link", p: patchString(srgbProfile, 12, "link"), err: `device class "link"`},
		{name: "abstract", p: patchString(srgbProfile, 12, "abst"), err: `device class "abst"`},
		{name: "named colors", p: patchString(srgbProfile, 12, "nmcl"), err: `device class "nmcl"`},
		{name: "RGB on a grayscale image", p: srgbProfile, gray: true, err: `color space "RGB " on a grayscale image`},
		{name: "GRAY on a color image", p: grayProfile(srgbPara), err: `color space "GRAY" on a color image`},
		{name: "CMYK", p: cmykProfile, err: `color space "CMYK" on a color image`},
		{name: "Lab", p: patchString(srgbProfile, 16, "Lab "), err: `color space "Lab " on a color image`},
		{name: "tag table past the end", p: patch32(srgbProfile, 128, uint32(n-132)/12+1), err: "tag table past the end"},
		{name: "tag count of 4 GiB", p: patch32(srgbProfile, 128, math.MaxUint32), err: "tag table past the end"},
		{name: "tag past the end", p: patch32(srgbProfile, rXYZ+4, uint32(n-19)), err: `tag "rXYZ" past the end`},
		{name: "tag of 4 GiB", p: patch32(srgbProfile, rXYZ+8, math.MaxUint32), err: `tag "rXYZ" past the end`},
		{name: "tag at 4 GiB", p: patch32(srgbProfile, rXYZ+4, math.MaxUint32), err: `tag "rXYZ" past the end`},
		{name: "larger than 4 MiB", p: huge, err: "larger than 4194304 bytes"},
	}
	for _, tt := range tests {
		err := checkICC(tt.p, tt.gray)
		if fmt.Sprint(err) != tt.err && (err != nil || tt.err != "") {
			t.Errorf("%s: checkICC() = %v, want %q", tt.name, err, tt.err)
		}
	}
}

func TestIsSRGB(t *testing.T) {
	// near returns a curve of 256 entries, at the values of 8-bit samples, of sRGB but for entry 128, which is off by
	// delta / 65535.
	near := func(delta int) []byte {
		entries := make([]uint16, 256)
		for i := range entries {
			entries[i] = uint16(math.Round(srgbTransfer(float64(i)/255) * 0xffff))
		}
		entries[128] += uint16(delta)
		return curvTag(entries...)
	}
	shifted := func(d float64) [3][3]float64 {
		var c [3][3]float64
		for i := range c {
			for j := range c[i] {
				c[i][j] = srgbColorants[i][j] + d
			}
		}
		return c
	}
	// matrix returns a profile with the tags of the colorants and the curves of red, green and blue.
	matrix := func(rXYZ, gXYZ, bXYZ, rTRC, gTRC, bTRC []byte) []byte {
		return iccProfile("mntr", "RGB ", profileTag{"rXYZ", rXYZ}, profileTag{"gXYZ", gXYZ}, profileTag{"bXYZ", bXYZ},
			profileTag{"rTRC", rTRC}, profileTag{"gTRC", gTRC}, profileTag{"bTRC", bTRC})
	}
	red, green, blue := xyzTag(srgbColorants[0]), xyzTag(srgbColorants[1]), xyzTag(srgbColorants[2])
	tests := []struct {
		name string
		p    []byte
		gray bool
		want bool
	}{
		{name: "parametric curve of type 3", p: srgbProfile, want: true},
		{name: "parametric curve of type 4", p: rgbProfile(srgbColorants,
			paraTag(4, 2.4, 1/1.055, 0.055/1.055, 1/12.92, 0.04045, 0, 0)), want: true},
		{name: "table of 1024", p: rgbProfile(srgbColorants, curvTable(1024, srgbTransfer)), want: true},
		{name: "table of 4096", p: rgbProfile(srgbColorants, curvTable(4096, srgbTransfer)), want: true},
		{name: "table of 26", p: rgbProfile(srgbColorants, curvTable(26, srgbTransfer)), want: true},
		{name: "colorants of the WebP vectors", p: rgbProfile([3][3]float64{{0.436066, 0.222488, 0.013916},
			{0.385147, 0.716873, 0.097076}, {0.143066, 0.060608, 0.714096}}, srgbPara), want: true},
		{name: "colorants 0.0029 off", p: rgbProfile(shifted(0.0029), srgbPara), want: true},
		{name: "colorants 0.0029 off the other way", p: rgbProfile(shifted(-0.0029), srgbPara), want: true},
		{name: "colorants 0.0031 off", p: rgbProfile(shifted(0.0031), srgbPara)},
		{name: "colorants 0.0031 off the other way", p: rgbProfile(shifted(-0.0031), srgbPara)},
		{name: "curve 0.0009 off", p: rgbProfile(srgbColorants, near(59)), want: true},
		{name: "curve 0.0011 off", p: rgbProfile(srgbColorants, near(73))},
		{name: "curve 0.0011 off the other way", p: rgbProfile(srgbColorants, near(-73))},
		{name: "Display P3", p: p3Profile},
		{name: "gamma 2.2", p: rgbProfile(srgbColorants, gamma22)},
		{name: "parametric gamma 2.2", p: rgbProfile(srgbColorants, paraTag(0, 2.2))},
		{name: "parametric gamma 2.4", p: rgbProfile(srgbColorants, paraTag(0, 2.4))},
		{name: "linear", p: rgbProfile(srgbColorants, curvTag())},
		{name: "table of 2", p: rgbProfile(srgbColorants, curvTag(0, 0xffff))},
		{name: "sRGB in its own tags", p: matrix(red, green, blue, srgbPara, srgbPara, srgbPara), want: true},
		{name: "green of gamma 2.2", p: matrix(red, green, blue, srgbPara, gamma22, srgbPara)},
		{name: "blue of gamma 2.2", p: matrix(red, green, blue, srgbPara, srgbPara, gamma22)},
		{name: "green of Display P3", p: matrix(red, xyzTag(p3Colorant[1]), blue, srgbPara, srgbPara, srgbPara)},
		{name: "red and green swapped", p: matrix(green, red, blue, srgbPara, srgbPara, srgbPara)},
		{name: "with A2B0", p: rgbProfile(srgbColorants, srgbPara, profileTag{"A2B0", []byte("mAB \x00\x00\x00\x00")})},
		{name: "with D2B0", p: rgbProfile(srgbColorants, srgbPara, profileTag{"D2B0", []byte("mpet\x00\x00\x00\x00")})},
		{name: "with B2A0", p: rgbProfile(srgbColorants, srgbPara, profileTag{"B2A0", []byte("mBA \x00\x00\x00\x00")}),
			want: true},
		{name: "PCS of Lab", p: patchString(srgbProfile, 20, "Lab ")},
		{name: "no colorants", p: iccProfile("mntr", "RGB ", profileTag{"rTRC", srgbPara}, profileTag{"gTRC", srgbPara},
			profileTag{"bTRC", srgbPara})},
		{name: "no curves", p: iccProfile("mntr", "RGB ", profileTag{"rXYZ", xyzTag(srgbColorants[0])},
			profileTag{"gXYZ", xyzTag(srgbColorants[1])}, profileTag{"bXYZ", xyzTag(srgbColorants[2])})},
		{name: "colorant of another type", p: matrix(patchString(red, 0, "sf32"), green, blue, srgbPara, srgbPara,
			srgbPara)},
		{name: "short colorant", p: matrix(red, green, blue[:19], srgbPara, srgbPara, srgbPara)},
		{name: "empty colorant", p: matrix(red, nil, blue, srgbPara, srgbPara, srgbPara)},
		{name: "curve of another type", p: rgbProfile(srgbColorants, patchString(srgbPara, 0, "sf32"))},
		{name: "parametric curve of type 5", p: rgbProfile(srgbColorants, paraTag(5, 2.4, 1, 0, 0, 0, 0, 0, 0))},
		{name: "parametric curve too short", p: rgbProfile(srgbColorants, srgbPara[:len(srgbPara)-4])},
		{name: "table past its tag", p: rgbProfile(srgbColorants, patch32(curvTag(0, 0xffff), 8, 3))},
		{name: "short curve", p: rgbProfile(srgbColorants, srgbPara[:11])},
		{name: "parametric curve of type 1 with a of 0", p: rgbProfile(srgbColorants, paraTag(1, 2.4, 0, 0))},
		{name: "parametric curve of type 2 with a of 0", p: rgbProfile(srgbColorants, paraTag(2, 2.4, 0, 0, 0))},
		{name: "gray of sRGB", p: grayProfile(srgbPara), gray: true, want: true},
		{name: "gray of an sRGB table", p: grayProfile(curvTable(1024, srgbTransfer)), gray: true, want: true},
		{name: "gray of gamma 2.2", p: grayProfile(gamma22), gray: true},
		{name: "gray without a curve", p: iccProfile("mntr", "GRAY", descTag), gray: true},
		{name: "gray with A2B0", p: iccProfile("mntr", "GRAY", profileTag{"kTRC", srgbPara},
			profileTag{"A2B0", []byte("mAB \x00\x00\x00\x00")}), gray: true},
	}
	for _, tt := range tests {
		if err := checkICC(tt.p, tt.gray); err != nil {
			t.Fatalf("%s: checkICC() = %v", tt.name, err)
		}
		if got := isSRGB(tt.p, tt.gray); got != tt.want {
			t.Errorf("%s: isSRGB() = %t, want %t", tt.name, got, tt.want)
		}
	}
}

func TestToneCurve(t *testing.T) {
	tests := []struct {
		name string
		tag  []byte
		x    []float64
		want []float64
	}{
		{"identity", curvTag(), []float64{0, 0.5, 1}, []float64{0, 0.5, 1}},
		{"gamma 2", curvTag(512), []float64{0, 0.5, 1}, []float64{0, 0.25, 1}},
		{"table", curvTag(0, 0x8000, 0xffff), []float64{0, 0.25, 0.5, 0.75, 1},
			[]float64{0, 0.2500038147554742, 0.5000076295109483, 0.7500038147554742, 1}},
		{"type 0", paraTag(0, 2), []float64{0, 0.5, 1}, []float64{0, 0.25, 1}},
		{"type 1", paraTag(1, 2, 2, -1), []float64{0, 0.5, 0.75, 1}, []float64{0, 0, 0.25, 1}},
		{"type 2", paraTag(2, 2, 2, -1, 0.25), []float64{0, 0.5, 0.75, 1}, []float64{0.25, 0.25, 0.5, 1.25}},
		{"type 3", paraTag(3, 2, 1, 0, 0.5, 0.5), []float64{0, 0.25, 0.5, 1}, []float64{0, 0.125, 0.25, 1}},
		{"type 4", paraTag(4, 2, 1, 0, 0.5, 0.5, 0.25, 0.125), []float64{0, 0.25, 0.5, 1},
			[]float64{0.125, 0.25, 0.5, 1.25}},
		{"negative base", paraTag(4, 2, 1, -1, 0, 0, 0.5, 0), []float64{0, 0.5, 1}, []float64{0.5, 0.5, 0.5}},
	}
	for _, tt := range tests {
		f := toneCurve(tt.tag)
		if f == nil {
			t.Errorf("%s: toneCurve() = nil", tt.name)
			continue
		}
		for i, x := range tt.x {
			if got := f(x); math.Abs(got-tt.want[i]) > 1e-9 {
				t.Errorf("%s: f(%g) = %g, want %g", tt.name, x, got, tt.want[i])
			}
		}
	}
	for _, tag := range [][]byte{nil, []byte("curv\x00\x00\x00\x00\x00\x00\x00"), []byte("sf32\x00\x00\x00\x00\x00\x00\x00\x00"),
		paraTag(5, 1, 1, 1, 1, 1, 1, 1), paraTag(1, 1, 1), patch32(curvTag(1), 8, math.MaxUint32)} {
		if toneCurve(tag) != nil {
			t.Errorf("toneCurve(% x) is not nil", tag)
		}
	}
}

func FuzzICC(f *testing.F) {
	for _, p := range [][]byte{srgbProfile, p3Profile, grayProfile(srgbPara), rgbProfile(srgbColorants,
		curvTable(26, srgbTransfer)), rgbProfile(srgbColorants, paraTag(4, 1, 2, 3, 4, 5, 6, 7))} {
		f.Add(p, string(p[16:20]) == "GRAY")
	}
	f.Fuzz(func(t *testing.T, p []byte, gray bool) {
		if checkICC(p, gray) == nil {
			isSRGB(p, gray)
		}
		if f := toneCurve(p); f != nil {
			for _, x := range []float64{0, 0.25, 0.5, 1} {
				f(x)
			}
		}
	})
}

func TestJoinICCSegments(t *testing.T) {
	p := p3Profile
	seg := func(seq, n int, part []byte) []byte { return slices.Concat([]byte{byte(seq), byte(n)}, part) }
	a, b, c := p[:100], p[100:200], p[200:]
	tests := []struct {
		name string
		segs [][]byte
		err  string // "" if they make p
	}{
		{name: "one", segs: [][]byte{seg(1, 1, p)}},
		{name: "three", segs: [][]byte{seg(1, 3, a), seg(2, 3, b), seg(3, 3, c)}},
		{name: "out of order", segs: [][]byte{seg(3, 3, c), seg(1, 3, a), seg(2, 3, b)}},
		{name: "empty parts", segs: [][]byte{seg(1, 3, nil), seg(2, 3, p), seg(3, 3, nil)}},
		{name: "255", segs: append([][]byte{seg(1, 255, p)}, slices.Repeat([][]byte{nil}, 254)...)},
		{name: "missing", segs: [][]byte{seg(1, 3, a), seg(3, 3, c)}, err: "no ICC_PROFILE segment 2 of 3"},
		{name: "missing first", segs: [][]byte{seg(2, 2, a)}, err: "no ICC_PROFILE segment 1 of 2"},
		{name: "duplicate", segs: [][]byte{seg(1, 2, a), seg(1, 2, a), seg(2, 2, b)},
			err: "two ICC_PROFILE segments 1 of 2"},
		{name: "different counts", segs: [][]byte{seg(1, 2, a), seg(2, 3, b)},
			err: "ICC_PROFILE segments of 2 and of 3 segments"},
		{name: "sequence number 0", segs: [][]byte{seg(0, 1, p)}, err: "ICC_PROFILE segment 0 of 1"},
		{name: "sequence number past the count", segs: [][]byte{seg(2, 1, p)}, err: "ICC_PROFILE segment 2 of 1"},
		{name: "count 0", segs: [][]byte{seg(1, 0, p)}, err: "ICC_PROFILE segment of 0 segments"},
		{name: "no count", segs: [][]byte{{1}}, err: "ICC_PROFILE segment too short"},
		{name: "empty", segs: [][]byte{{}}, err: "ICC_PROFILE segment too short"},
	}
	for _, tt := range tests {
		if tt.name == "255" {
			for i := 1; i < 255; i++ {
				tt.segs[i] = seg(i+1, 255, nil)
			}
		}
		got, err := joinICCSegments(tt.segs)
		switch {
		case tt.err == "" && (err != nil || !bytes.Equal(got, p)):
			t.Errorf("%s: joinICCSegments() = %d bytes, %v; want the profile", tt.name, len(got), err)
		case tt.err != "" && fmt.Sprint(err) != tt.err:
			t.Errorf("%s: joinICCSegments() error = %v, want %q", tt.name, err, tt.err)
		}
	}
}

func TestJPEGICCSegments(t *testing.T) {
	file := jpegOf(t, colored(16, 8))
	soi := []byte{0xff, 0xd8}
	sos := jpegSegment(0xda, []byte{1, 1, 0, 0, 63, 0})
	icc := iccSegment(1, 1, p3Profile)
	tests := []struct {
		name string
		data []byte
		want int // segments
		err  string
	}{
		{name: "none", data: file},
		{name: "after SOI", data: insertJPEG(file, icc), want: 1},
		{name: "three", data: insertJPEG(file, iccSegments(p3Profile, 3)...), want: 3},
		{name: "between other segments", data: insertJPEG(file, jpegSegment(0xe0, []byte("JFIF\x00\x01\x02")),
			iccSegment(1, 2, p3Profile[:10]), exifSegment(6), jpegSegment(0xfe, []byte("comment")),
			iccSegment(2, 2, p3Profile[10:])), want: 2},
		{name: "after DQT", data: slices.Concat(file[:jpegMarker(t, file, 0xc0)], icc, file[jpegMarker(t, file, 0xc0):]),
			want: 1},
		{name: "APP2 of another kind", data: insertJPEG(file, jpegSegment(0xe2, []byte("MPF\x00data"))), want: 0},
		{name: "ICC_PROFILE in APP1", data: insertJPEG(file, jpegSegment(0xe1, []byte(iccSignature+"\x01\x01"))),
			want: 0},
		{name: "after fill bytes", data: insertJPEG(file, []byte{0xff, 0xff}, icc), want: 1},
		{name: "after bytes that are not markers", data: insertJPEG(file, []byte{0x00, 0x12, 0xff, 0x00}, icc),
			want: 1},
		{name: "after RST0", data: insertJPEG(file, []byte{0xff, 0xd0}, icc), want: 1},
		{name: "after SOS", data: slices.Concat(soi, sos, icc, []byte{0xff, 0xd9}), want: 0},
		{name: "SOI only", data: soi, err: "invalid JPEG: no image data"},
		{name: "EOI before SOS", data: slices.Concat(soi, icc, []byte{0xff, 0xd9}, sos), err: "invalid JPEG: no image data"},
		{name: "cut in a segment", data: slices.Concat(soi, icc[:20]), err: "invalid JPEG: no image data"},
		{name: "cut in a length", data: slices.Concat(soi, icc[:3]), err: "invalid JPEG: no image data"},
		{name: "cut after a marker", data: slices.Concat(soi, icc[:2]), err: "invalid JPEG: no image data"},
		{name: "length 1", data: slices.Concat(soi, []byte{0xff, 0xe2, 0, 1}, sos), err: "invalid JPEG: short segment length"},
		{name: "SOS of length 0", data: slices.Concat(soi, []byte{0xff, 0xda, 0, 0}), err: "invalid JPEG: short segment length"},
	}
	for _, tt := range tests {
		segs, err := jpegICCSegments(tt.data)
		switch {
		case tt.err == "" && (err != nil || len(segs) != tt.want):
			t.Errorf("%s: jpegICCSegments() = %d segments, %v; want %d", tt.name, len(segs), err, tt.want)
		case tt.err != "" && fmt.Sprint(err) != tt.err:
			t.Errorf("%s: jpegICCSegments() error = %v, want %q", tt.name, err, tt.err)
		}
		if tt.want > 0 {
			if p, err := joinICCSegments(segs); err != nil || !bytes.Equal(p, p3Profile) {
				t.Errorf("%s: joinICCSegments() = %d bytes, %v; want the profile", tt.name, len(p), err)
			}
		}
	}
}

// jpegMarker returns the offset of the first segment with marker in data, a file from image/jpeg.
func jpegMarker(t *testing.T, data []byte, marker byte) int {
	t.Helper()
	for i := 2; i+4 <= len(data); i += 2 + int(be.Uint16(data[i+2:])) {
		if data[i+1] == marker {
			return i
		}
	}
	t.Fatalf("no segment with marker %#x", marker)
	return 0
}

func TestPNGICC(t *testing.T) {
	zp := zlibOf(t, p3Profile)
	exact := make([]byte, maxICCBytes)
	tests := []struct {
		name  string
		chunk []byte
		want  []byte
		err   string
	}{
		{name: "profile", chunk: slices.Concat([]byte("ICC profile\x00\x00"), zp), want: p3Profile},
		{name: "name of 79 bytes", chunk: slices.Concat([]byte(strings.Repeat("n", 79)+"\x00\x00"), zp), want: p3Profile},
		{name: "data after the stream", chunk: slices.Concat([]byte("n\x00\x00"), zp, []byte("more")), want: p3Profile},
		{name: "4 MiB", chunk: slices.Concat([]byte("n\x00\x00"), zlibOf(t, exact)), want: exact},
		{name: "name of 80 bytes", chunk: slices.Concat([]byte(strings.Repeat("n", 80)+"\x00\x00"), zp),
			err: "iCCP chunk without a name of 1 to 79 bytes"},
		{name: "no name", chunk: slices.Concat([]byte("\x00\x00"), zp), err: "iCCP chunk without a name of 1 to 79 bytes"},
		{name: "no end of the name", chunk: []byte("name"), err: "iCCP chunk without a name of 1 to 79 bytes"},
		{name: "empty", chunk: []byte{}, err: "iCCP chunk without a name of 1 to 79 bytes"},
		{name: "no compression method", chunk: []byte("name\x00"), err: "iCCP chunk without a compression method"},
		{name: "compression method 1", chunk: slices.Concat([]byte("n\x00\x01"), zp), err: "iCCP compression method 1"},
		{name: "not zlib", chunk: []byte("n\x00\x00not zlib"), err: "iCCP data: zlib: invalid header"},
		{name: "no data", chunk: []byte("n\x00\x00"), err: "iCCP data: unexpected EOF"},
		{name: "cut", chunk: slices.Concat([]byte("n\x00\x00"), zp[:len(zp)-10]), err: "iCCP data: unexpected EOF"},
		{name: "checksum", chunk: slices.Concat([]byte("n\x00\x00"), patch(zp, len(zp)-1, ^zp[len(zp)-1])),
			err: "iCCP data: zlib: invalid checksum"},
		{name: "larger than 4 MiB", chunk: slices.Concat([]byte("n\x00\x00"), zlibOf(t, make([]byte, maxICCBytes+1))),
			err: "larger than 4194304 bytes"},
	}
	for _, tt := range tests {
		got, err := pngICC(tt.chunk)
		switch {
		case tt.err == "" && (err != nil || !bytes.Equal(got, tt.want)):
			t.Errorf("%s: pngICC() = %d bytes, %v; want %d bytes", tt.name, len(got), err, len(tt.want))
		case tt.err != "" && fmt.Sprint(err) != tt.err:
			t.Errorf("%s: pngICC() error = %v, want %q", tt.name, err, tt.err)
		}
	}
}

func TestEmbedJPEGICC(t *testing.T) {
	file := jpegOf(t, colored(16, 8))
	sizes := []int{len(p3Profile), maxICCSegment, maxICCSegment + 1, 2*maxICCSegment + 10, 255 * maxICCSegment}
	for _, n := range sizes {
		profile := bytes.Repeat(p3Profile, n/len(p3Profile)+1)[:n]
		out, err := embedJPEGICC(file, profile)
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		if !bytes.Equal(out[:2], file[:2]) || !bytes.Equal(out[len(out)-len(file)+2:], file[2:]) {
			t.Errorf("%d bytes: not the file with segments after SOI", n)
		}
		segs, err := jpegICCSegments(out)
		if want := (n + maxICCSegment - 1) / maxICCSegment; err != nil || len(segs) != want {
			t.Fatalf("%d bytes: %d segments, %v; want %d", n, len(segs), err, want)
		}
		for i, s := range segs {
			if len(s) != 2+min(maxICCSegment, n-i*maxICCSegment) {
				t.Errorf("%d bytes: segment %d of %d bytes", n, i+1, len(s))
			}
		}
		if got, err := joinICCSegments(segs); err != nil || !bytes.Equal(got, profile) {
			t.Errorf("%d bytes: the segments make %d bytes, %v", n, len(got), err)
		}
		if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
			t.Errorf("%d bytes: jpeg.Decode() = %v", n, err)
		}
	}
	for _, tt := range []struct {
		name          string
		file, profile []byte
		err           string
	}{
		{"empty profile", file, nil, "ICC profile of 0 bytes"},
		{"more than 255 segments", file, make([]byte, 255*maxICCSegment+1), "ICC profile of 16707346 bytes"},
		{"not JPEG", pngOf(t, colored(2, 2)), p3Profile, "not a JPEG file"},
		{"empty file", nil, p3Profile, "not a JPEG file"},
	} {
		if _, err := embedJPEGICC(tt.file, tt.profile); fmt.Sprint(err) != tt.err {
			t.Errorf("%s: embedJPEGICC() error = %v, want %q", tt.name, err, tt.err)
		}
	}
}

func TestEmbedPNGICC(t *testing.T) {
	for _, m := range []image.Image{colored(16, 8), image.NewGray(image.Rect(0, 0, 3, 2)),
		image.NewPaletted(image.Rect(0, 0, 3, 2), []color.Color{color.Black, color.White})} {
		file := pngOf(t, m)
		profile := p3Profile
		if _, ok := m.(*image.Gray); ok {
			profile = grayProfile(gamma22)
		}
		out, err := embedPNGICC(file, profile)
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		for _, c := range chunksOf(out) {
			types = append(types, c.typ)
		}
		var want []string
		for _, c := range chunksOf(file) {
			want = append(want, c.typ)
		}
		want = slices.Insert(want, 1, "iCCP")
		if !slices.Equal(types, want) {
			t.Errorf("%T: chunks %q, want %q", m, types, want)
		}
		if iccp := chunksOf(out)[1].data; !bytes.HasPrefix(iccp, []byte("ICC profile\x00\x00")) {
			t.Errorf("%T: iCCP starts with %q", m, iccp[:min(len(iccp), 14)])
		}
		o, err := readPNG(out)
		if err != nil || o.iccErr != nil || !bytes.Equal(o.icc, profile) {
			t.Errorf("%T: readPNG() = %d bytes of profile, %v, %v", m, len(o.icc), o.iccErr, err)
		}
		if _, err := png.Decode(bytes.NewReader(out)); err != nil {
			t.Errorf("%T: png.Decode() = %v", m, err)
		}
		if again, _ := embedPNGICC(file, profile); !bytes.Equal(again, out) {
			t.Errorf("%T: a second embedPNGICC() gave other bytes", m)
		}
	}
	for _, file := range [][]byte{nil, jpegOf(t, colored(2, 2)), pngOf(t, colored(2, 2))[:pngIHDREnd-1],
		patchString(pngOf(t, colored(2, 2)), 12, "IHDX")} {
		if _, err := embedPNGICC(file, p3Profile); fmt.Sprint(err) != "not a PNG file" {
			t.Errorf("embedPNGICC(% x) error = %v, want %q", file[:min(len(file), 16)], err, "not a PNG file")
		}
	}
}
