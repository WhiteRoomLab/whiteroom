package imaging

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"slices"
)

// iccSignature starts the APP2 segments of JPEG that hold an ICC profile (ICC.1:2022 annex B.4).
const iccSignature = "ICC_PROFILE\x00"

// maxICCSegment is the most bytes of a profile in an APP2 segment, after the length, iccSignature, the sequence
// number and the number of segments.
const maxICCSegment = 0xffff - 2 - len(iccSignature) - 2

// srgbColorants are the XYZ of the red, green and blue of sRGB, adapted to D50 as ICC profiles have them.
var srgbColorants = [3][3]float64{{0.4361, 0.2225, 0.0139}, {0.3851, 0.7169, 0.0971}, {0.1431, 0.0606, 0.7141}}

const (
	colorantTolerance = 0.003
	curveTolerance    = 0.001
)

// joinICCSegments returns the profile in segs, the data after iccSignature of the APP2 segments of a JPEG file: each
// has its sequence number from 1, the number of segments and its part of the profile.
func joinICCSegments(segs [][]byte) ([]byte, error) {
	var parts [][]byte
	for _, s := range segs {
		if len(s) < 2 {
			return nil, errors.New("ICC_PROFILE segment too short")
		}
		seq, n := int(s[0]), int(s[1])
		switch {
		case parts == nil && n == 0:
			return nil, errors.New("ICC_PROFILE segment of 0 segments")
		case parts == nil:
			parts = make([][]byte, n)
		case n != len(parts):
			return nil, fmt.Errorf("ICC_PROFILE segments of %d and of %d segments", len(parts), n)
		}
		switch {
		case seq < 1 || seq > n:
			return nil, fmt.Errorf("ICC_PROFILE segment %d of %d", seq, n)
		case parts[seq-1] != nil:
			return nil, fmt.Errorf("two ICC_PROFILE segments %d of %d", seq, n)
		}
		parts[seq-1] = s[2:]
	}
	for i, p := range parts {
		if p == nil {
			return nil, fmt.Errorf("no ICC_PROFILE segment %d of %d", i+1, len(parts))
		}
	}
	return bytes.Join(parts, nil), nil
}

// pngICC returns the profile in the data of an iCCP chunk: a name of 1 to 79 bytes, a zero byte, the compression
// method 0 and the profile compressed with zlib.
func pngICC(chunk []byte) ([]byte, error) {
	k := bytes.IndexByte(chunk[:min(len(chunk), 80)], 0)
	switch {
	case k < 1:
		return nil, errors.New("iCCP chunk without a name of 1 to 79 bytes")
	case k+1 == len(chunk):
		return nil, errors.New("iCCP chunk without a compression method")
	case chunk[k+1] != 0:
		return nil, fmt.Errorf("iCCP compression method %d", chunk[k+1])
	}
	zr, err := zlib.NewReader(bytes.NewReader(chunk[k+2:]))
	if err != nil {
		return nil, fmt.Errorf("iCCP data: %w", err)
	}
	p, err := io.ReadAll(io.LimitReader(zr, maxICCBytes+1))
	if err != nil {
		return nil, fmt.Errorf("iCCP data: %w", err)
	}
	if len(p) > maxICCBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxICCBytes)
	}
	return p, nil
}

// checkICC returns an error unless p is an ICC profile (ICC.1:2022 section 7) of an input, display, output or color
// space device in the color space of the image: GRAY if gray, else RGB.
func checkICC(p []byte, gray bool) error {
	be := binary.BigEndian
	switch {
	case len(p) < 132: // the header and the number of tags
		return fmt.Errorf("only %d bytes", len(p))
	case len(p) > maxICCBytes:
		return fmt.Errorf("larger than %d bytes", maxICCBytes)
	case uint64(be.Uint32(p)) != uint64(len(p)):
		return fmt.Errorf("size field %d, but %d bytes", be.Uint32(p), len(p))
	case string(p[36:40]) != "acsp":
		return errors.New(`no "acsp" signature`)
	}
	switch class := string(p[12:16]); class {
	case "scnr", "mntr", "prtr", "spac":
	default:
		return fmt.Errorf("device class %q", class)
	}
	want, image := "RGB ", "color"
	if gray {
		want, image = "GRAY", "grayscale"
	}
	if space := string(p[16:20]); space != want {
		return fmt.Errorf("color space %q on a %s image", space, image)
	}
	n := be.Uint32(p[128:])
	if uint64(n) > uint64(len(p)-132)/12 {
		return errors.New("tag table past the end")
	}
	for i := range int(n) {
		t := p[132+12*i:]
		if uint64(be.Uint32(t[4:]))+uint64(be.Uint32(t[8:])) > uint64(len(p)) {
			return fmt.Errorf("tag %q past the end", t[:4])
		}
	}
	return nil
}

// iccTag returns the data of the first tag with sig in p, a profile that checkICC accepts; nil if there is none.
func iccTag(p []byte, sig string) []byte {
	be := binary.BigEndian
	for i := range int(be.Uint32(p[128:])) {
		if t := p[132+12*i:]; string(t[:4]) == sig {
			off, size := int(be.Uint32(t[4:])), int(be.Uint32(t[8:]))
			return p[off : off+size : off+size]
		}
	}
	return nil
}

// isSRGB reports whether p, a profile that checkICC accepts, shows the colors of sRGB: the colorants of sRGB and its
// transfer function, or for a GRAY profile, the transfer function. Images without a profile have those colors.
func isSRGB(p []byte, gray bool) bool {
	if string(p[20:24]) != "XYZ " { // the profile connection space, of the colorants
		return false
	}
	// These transforms, which may be anything, come before the colorants and the curves.
	for _, sig := range []string{"A2B0", "A2B1", "A2B2", "D2B0", "D2B1", "D2B2", "D2B3"} {
		if iccTag(p, sig) != nil {
			return false
		}
	}
	if gray {
		return isSRGBCurve(iccTag(p, "kTRC"))
	}
	for i, sigs := range [3][2]string{{"rXYZ", "rTRC"}, {"gXYZ", "gTRC"}, {"bXYZ", "bTRC"}} {
		if !isSRGBColorant(iccTag(p, sigs[0]), srgbColorants[i]) || !isSRGBCurve(iccTag(p, sigs[1])) {
			return false
		}
	}
	return true
}

// isSRGBColorant reports whether b, an XYZType tag, is within colorantTolerance of xyz.
func isSRGBColorant(b []byte, xyz [3]float64) bool {
	if len(b) < 20 || string(b[:4]) != "XYZ " {
		return false
	}
	for i, v := range xyz {
		if math.Abs(s15Fixed16(b[8+4*i:])-v) > colorantTolerance {
			return false
		}
	}
	return true
}

// isSRGBCurve reports whether b, a curveType or parametricCurveType tag, is within curveTolerance of the transfer
// function of sRGB at the 256 values of 8-bit samples.
func isSRGBCurve(b []byte) bool {
	f := toneCurve(b)
	if f == nil {
		return false
	}
	for i := range 256 {
		x := float64(i) / 255
		if !(math.Abs(min(max(f(x), 0), 1)-srgbTransfer(x)) <= curveTolerance) { // false for NaN
			return false
		}
	}
	return true
}

// srgbTransfer returns the linear light of x, a value of sRGB from 0 to 1 (IEC 61966-2-1).
func srgbTransfer(x float64) float64 {
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

// toneCurve returns the function from 0 to 1 of tag, a curveType or parametricCurveType tag (ICC.1:2022 sections 10.6
// and 10.18); nil if it is neither.
func toneCurve(tag []byte) func(float64) float64 {
	be := binary.BigEndian
	if len(tag) < 12 {
		return nil
	}
	switch string(tag[:4]) {
	case "curv":
		n := be.Uint32(tag[8:])
		if uint64(n) > uint64(len(tag)-12)/2 {
			return nil
		}
		t := tag[12 : 12+2*int(n)]
		switch n {
		case 0:
			return func(x float64) float64 { return x }
		case 1:
			g := float64(be.Uint16(t)) / 256
			return func(x float64) float64 { return math.Pow(x, g) }
		}
		return func(x float64) float64 {
			p := x * float64(n-1)
			i := min(int(p), int(n)-2)
			f := p - float64(i)
			return (float64(be.Uint16(t[2*i:]))*(1-f) + float64(be.Uint16(t[2*i+2:]))*f) / 0xffff
		}
	case "para":
		typ := int(be.Uint16(tag[8:]))
		counts := []int{1, 3, 4, 5, 7} // of the parameters of each function type
		if typ >= len(counts) || len(tag) < 12+4*counts[typ] {
			return nil
		}
		var p [7]float64
		for i := range counts[typ] {
			p[i] = s15Fixed16(tag[12+4*i:])
		}
		g, a, b, c, d, e, f := p[0], p[1], p[2], p[3], p[4], p[5], p[6]
		if typ == 0 {
			a = 1
		}
		// pow returns (a*x + b)^g, or 0 where a*x + b is not positive.
		pow := func(x float64) float64 {
			if y := a*x + b; y > 0 {
				return math.Pow(y, g)
			}
			return 0
		}
		switch typ {
		case 0:
			return pow
		case 1:
			return func(x float64) float64 {
				if x >= -b/a {
					return pow(x)
				}
				return 0
			}
		case 2:
			return func(x float64) float64 {
				if x >= -b/a {
					return pow(x) + c
				}
				return c
			}
		case 3:
			return func(x float64) float64 {
				if x >= d {
					return pow(x)
				}
				return c * x
			}
		}
		return func(x float64) float64 {
			if x >= d {
				return pow(x) + e
			}
			return c*x + f
		}
	}
	return nil
}

func s15Fixed16(b []byte) float64 {
	return float64(int32(binary.BigEndian.Uint32(b))) / 0x10000
}

// embedJPEGICC returns file, a JPEG file that image/jpeg wrote, with profile in APP2 segments after SOI.
func embedJPEGICC(file, profile []byte) ([]byte, error) {
	n := (len(profile) + maxICCSegment - 1) / maxICCSegment
	switch {
	case len(file) < 2 || file[0] != 0xff || file[1] != 0xd8:
		return nil, errors.New("not a JPEG file")
	case n < 1 || n > 0xff:
		return nil, fmt.Errorf("ICC profile of %d bytes", len(profile))
	}
	out := make([]byte, 0, len(file)+len(profile)+n*(4+len(iccSignature)+2))
	out = append(out, file[:2]...)
	for i := range n {
		part := profile[i*maxICCSegment : min(len(profile), (i+1)*maxICCSegment)]
		out = append(out, 0xff, 0xe2)
		out = binary.BigEndian.AppendUint16(out, uint16(2+len(iccSignature)+2+len(part)))
		out = append(out, iccSignature...)
		out = append(out, byte(i+1), byte(n))
		out = append(out, part...)
	}
	return append(out, file[2:]...), nil
}

// embedPNGICC returns file, a PNG file that image/png wrote, with profile in an iCCP chunk after IHDR.
func embedPNGICC(file, profile []byte) ([]byte, error) {
	if len(file) < pngIHDREnd || string(file[:16]) != pngSignature+"\x00\x00\x00\x0dIHDR" {
		return nil, errors.New("not a PNG file")
	}
	var data bytes.Buffer
	data.WriteString("ICC profile\x00\x00") // the name, and the compression method 0
	zw, err := zlib.NewWriterLevel(&data, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(profile); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return slices.Concat(file[:pngIHDREnd], pngChunk("iCCP", data.Bytes()), file[pngIHDREnd:]), nil
}

// pngChunk returns a PNG chunk of type typ with data.
func pngChunk(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(b, typ...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}
