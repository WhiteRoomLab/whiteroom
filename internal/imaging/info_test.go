package imaging

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/webp"
)

type inspectTest struct {
	name string
	data []byte
	want Info
}

func equalInfo(a, b Info) bool {
	return a.MIMEType == b.MIMEType && a.Width == b.Width && a.Height == b.Height && a.Orientation == b.Orientation &&
		a.Animated == b.Animated && a.Format == b.Format && a.ICC == b.ICC && slices.Equal(a.Warnings, b.Warnings)
}

// with returns info with icc and warnings.
func (info Info) with(icc string, warnings ...string) Info {
	info.ICC, info.Warnings = icc, warnings
	return info
}

func jpegInfo(w, h, o int) Info {
	return Info{MIMEType: mediafile.JPEG, Width: w, Height: h, Orientation: o, Format: mediafile.JPEG}
}

func pngInfo(w, h, o int) Info {
	return Info{MIMEType: mediafile.PNG, Width: w, Height: h, Orientation: o, Format: mediafile.PNG}
}

func gifInfo(w, h int, animated bool) Info {
	return Info{MIMEType: mediafile.GIF, Width: w, Height: h, Orientation: 1, Animated: animated, Format: mediafile.PNG}
}

const (
	cmykWarning        = "CMYK JPEG; converted to RGB without color management, so its colors may change"
	grayWarning        = "grayscale ICC profile of a PNG with transparency; published in RGBA without it"
	gifCutWarning      = "truncated GIF; published as its first frame"
	o6AnimationWarning = "animation with EXIF orientation 6; published as its first frame"
)

func invalidWarning(reason string) string {
	return "invalid ICC profile (" + reason + "); published without it"
}

func checkInspect(t *testing.T, tests []inspectTest) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Inspect(tt.data)
			if err != nil || !equalInfo(got, tt.want) {
				t.Fatalf("Inspect() = %+v, %v\nwant %+v", got, err, tt.want)
			}
			if got.Warnings != nil && len(got.Warnings) == 0 {
				t.Errorf("Warnings is empty, not nil")
			}
			icc, err := keptICC(tt.data, got)
			if err != nil || (icc != nil) != (got.ICC == iccKeep) {
				t.Errorf("keptICC() = %d bytes, %v; ICC %q", len(icc), err, got.ICC)
			}
			if gray := isGrayProfile(icc); icc != nil && (checkICC(icc, gray) != nil || isSRGB(icc, gray)) {
				t.Errorf("keptICC() = a profile that is invalid or sRGB")
			}
		})
	}
}

func isGrayProfile(p []byte) bool {
	return len(p) >= 20 && string(p[16:20]) == "GRAY"
}

func TestInspectJPEG(t *testing.T) {
	file := jpegOf(t, colored(3, 2))
	gray := jpegOf(t, image.NewGray(image.Rect(0, 0, 3, 2)))
	p3 := iccSegments(p3Profile, 3)
	tests := []inspectTest{
		{"color", file, jpegInfo(3, 2, 1)},
		{"grayscale", gray, jpegInfo(3, 2, 1)},
		{"sRGB profile", insertJPEG(file, iccSegment(1, 1, srgbProfile)), jpegInfo(3, 2, 1).with(iccSRGB)},
		{"Display P3 profile in 3 segments", insertJPEG(file, p3...), jpegInfo(3, 2, 1).with(iccKeep)},
		{"Display P3 profile in 3 segments out of order", insertJPEG(file, p3[2], p3[0], p3[1]),
			jpegInfo(3, 2, 1).with(iccKeep)},
		{"Display P3 profile after Exif", insertJPEG(file, exifSegment(6), iccSegment(1, 1, p3Profile)),
			jpegInfo(2, 3, 6).with(iccKeep)},
		{"missing segment", insertJPEG(file, p3[0], p3[2]),
			jpegInfo(3, 2, 1).with(iccInvalid, invalidWarning("no ICC_PROFILE segment 2 of 3"))},
		{"duplicate segment", insertJPEG(file, p3[0], p3[1], p3[1], p3[2]),
			jpegInfo(3, 2, 1).with(iccInvalid, invalidWarning("two ICC_PROFILE segments 2 of 3"))},
		{"segments of different counts", insertJPEG(file, iccSegment(1, 2, p3Profile[:10]),
			iccSegment(2, 3, p3Profile[10:])),
			jpegInfo(3, 2, 1).with(iccInvalid, invalidWarning("ICC_PROFILE segments of 2 and of 3 segments"))},
		{"profile after the image data", append(slices.Clone(file), iccSegment(1, 1, []byte("not a profile"))...),
			jpegInfo(3, 2, 1)},
		{"profile of a device link", insertJPEG(file, iccSegment(1, 1, patchString(p3Profile, 12, "link"))),
			jpegInfo(3, 2, 1).with(iccInvalid, invalidWarning(`device class "link"`))},
		{"grayscale profile", insertJPEG(file, iccSegment(1, 1, grayProfile(gamma22))),
			jpegInfo(3, 2, 1).with(iccInvalid, invalidWarning(`color space "GRAY" on a color image`))},
		{"grayscale with a grayscale sRGB profile", insertJPEG(gray, iccSegment(1, 1, grayProfile(srgbPara))),
			jpegInfo(3, 2, 1).with(iccSRGB)},
		{"grayscale with a grayscale profile", insertJPEG(gray, iccSegment(1, 1, grayProfile(gamma22))),
			jpegInfo(3, 2, 1).with(iccKeep)},
		{"grayscale with an RGB profile", insertJPEG(gray, iccSegment(1, 1, p3Profile)),
			jpegInfo(3, 2, 1).with(iccInvalid, invalidWarning(`color space "RGB " on a grayscale image`))},
		{"CMYK", cmykJPEG(0), jpegInfo(8, 8, 1).with(iccCMYK, cmykWarning)},
		{"YCCK", cmykJPEG(2), jpegInfo(8, 8, 1).with(iccCMYK, cmykWarning)},
		{"CMYK with a CMYK profile", insertJPEG(cmykJPEG(0), iccSegment(1, 1, cmykProfile)),
			jpegInfo(8, 8, 1).with(iccCMYK, cmykWarning)},
		{"CMYK with a missing segment", insertJPEG(cmykJPEG(0), iccSegment(2, 2, cmykProfile)),
			jpegInfo(8, 8, 1).with(iccCMYK, cmykWarning)},
	}
	for o := 1; o <= 8; o++ {
		w, h := 3, 2
		if o >= 5 {
			w, h = h, w
		}
		tests = append(tests, inspectTest{fmt.Sprintf("orientation %d", o), insertJPEG(file, exifSegment(o)),
			jpegInfo(w, h, o)})
	}
	checkInspect(t, tests)

	// The test files decode as their Info says.
	m, err := jpeg.Decode(bytes.NewReader(cmykJPEG(0)))
	if _, ok := m.(*image.CMYK); !ok || err != nil {
		t.Errorf("cmykJPEG(0) decodes to %T, %v", m, err)
	}
}

func TestInspectPNG(t *testing.T) {
	file := pngOf(t, colored(3, 2))
	gray := pngOf(t, image.NewGray(image.Rect(0, 0, 3, 2)))
	paletted := pngOf(t, image.NewPaletted(image.Rect(0, 0, 3, 2), color.Palette{color.Black, color.White}))
	nrgba := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	trns := pngChunk("tRNS", []byte{0, 0})
	tests := []inspectTest{
		{"RGB", file, pngInfo(3, 2, 1)},
		{"RGBA", pngOf(t, nrgba), pngInfo(3, 2, 1)},
		{"16-bit", pngOf(t, image.NewNRGBA64(image.Rect(0, 0, 3, 2))), pngInfo(3, 2, 1)},
		{"paletted", paletted, pngInfo(3, 2, 1)},
		{"grayscale", gray, pngInfo(3, 2, 1)},
		{"grayscale with alpha", grayAlphaPNG(t, 3, 2), pngInfo(3, 2, 1)},
		{"eXIf", insertPNG(file, pngChunk("eXIf", exifTIFF(6))), pngInfo(2, 3, 6)},
		{"sRGB profile", insertPNG(file, iccpChunk(t, srgbProfile)), pngInfo(3, 2, 1).with(iccSRGB)},
		{"Display P3 profile", insertPNG(file, iccpChunk(t, p3Profile)), pngInfo(3, 2, 1).with(iccKeep)},
		{"Display P3 profile and eXIf", insertPNG(file, pngChunk("eXIf", exifTIFF(8)), iccpChunk(t, p3Profile)),
			pngInfo(2, 3, 8).with(iccKeep)},
		{"two profiles", insertPNG(file, iccpChunk(t, p3Profile), iccpChunk(t, srgbProfile)),
			pngInfo(3, 2, 1).with(iccKeep)},
		{"profile after the image data", slices.Concat(file[:len(file)-12], iccpChunk(t, p3Profile),
			file[len(file)-12:]), pngInfo(3, 2, 1)},
		{"paletted with Display P3", insertPNG(paletted, iccpChunk(t, p3Profile)), pngInfo(3, 2, 1).with(iccKeep)},
		{"paletted with a grayscale profile", insertPNG(paletted, iccpChunk(t, grayProfile(gamma22))),
			pngInfo(3, 2, 1).with(iccInvalid, invalidWarning(`color space "GRAY" on a color image`))},
		{"grayscale with a grayscale profile", insertPNG(gray, iccpChunk(t, grayProfile(gamma22))),
			pngInfo(3, 2, 1).with(iccKeep)},
		{"grayscale with a grayscale sRGB profile", insertPNG(gray, iccpChunk(t, grayProfile(srgbPara))),
			pngInfo(3, 2, 1).with(iccSRGB)},
		{"grayscale with an RGB profile", insertPNG(gray, iccpChunk(t, p3Profile)),
			pngInfo(3, 2, 1).with(iccInvalid, invalidWarning(`color space "RGB " on a grayscale image`))},
		{"grayscale with tRNS and a grayscale profile", insertPNG(gray, trns, iccpChunk(t, grayProfile(gamma22))),
			pngInfo(3, 2, 1).with(iccGray, grayWarning)},
		{"grayscale with tRNS and a grayscale sRGB profile", insertPNG(gray, iccpChunk(t, grayProfile(srgbPara)), trns),
			pngInfo(3, 2, 1).with(iccSRGB)},
		{"grayscale with alpha and a grayscale profile", grayAlphaPNG(t, 3, 2, iccpChunk(t, grayProfile(gamma22))),
			pngInfo(3, 2, 1).with(iccGray, grayWarning)},
		{"grayscale with alpha and a grayscale sRGB profile", grayAlphaPNG(t, 3, 2, iccpChunk(t, grayProfile(srgbPara))),
			pngInfo(3, 2, 1).with(iccSRGB)},
		{"grayscale with alpha and an RGB profile", grayAlphaPNG(t, 3, 2, iccpChunk(t, p3Profile)),
			pngInfo(3, 2, 1).with(iccInvalid, invalidWarning(`color space "RGB " on a grayscale image`))},
		{"profile larger than 4 MiB", insertPNG(file, iccpChunk(t, make([]byte, maxICCBytes+1))),
			pngInfo(3, 2, 1).with(iccInvalid, invalidWarning("larger than 4194304 bytes"))},
		{"profile not compressed", insertPNG(file, pngChunk("iCCP", append([]byte("test\x00\x01"), p3Profile...))),
			pngInfo(3, 2, 1).with(iccInvalid, invalidWarning("iCCP compression method 1"))},
		{"empty profile", insertPNG(file, iccpChunk(t, nil)),
			pngInfo(3, 2, 1).with(iccInvalid, invalidWarning("only 0 bytes"))},
		{"APNG", apngFile(t, colored(3, 2)), animatedPNG(3, 2)},
		{"APNG with Display P3", apngFile(t, colored(3, 2), iccpChunk(t, p3Profile)), animatedPNG(3, 2).with(iccKeep)},
		// The animation keeps its profile as it is, so there is no warning.
		{"APNG with an invalid profile", apngFile(t, colored(3, 2), iccpChunk(t, p3Profile[:200])),
			animatedPNG(3, 2).with(iccInvalid)},
		{"APNG with eXIf", apngFile(t, colored(3, 2), pngChunk("eXIf", exifTIFF(6))),
			pngInfo(2, 3, 6).with("", o6AnimationWarning)},
		{"APNG with eXIf and an invalid profile", apngFile(t, colored(3, 2), pngChunk("eXIf", exifTIFF(6)),
			iccpChunk(t, p3Profile[:200])),
			pngInfo(2, 3, 6).with(iccInvalid, o6AnimationWarning,
				invalidWarning(fmt.Sprintf("size field %d, but 200 bytes", len(p3Profile))))},
		{"acTL after the image data", slices.Concat(file[:len(file)-12], pngChunk("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0}),
			file[len(file)-12:]), pngInfo(3, 2, 1)},
	}
	checkInspect(t, tests)

	// The test files decode as their Info says.
	for _, tt := range tests[:6] {
		if _, err := png.Decode(bytes.NewReader(tt.data)); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}

func animatedPNG(w, h int) Info {
	return Info{MIMEType: mediafile.PNG, Width: w, Height: h, Orientation: 1, Animated: true, Format: mediafile.PNG}
}

func TestInspectGIF(t *testing.T) {
	frame := gifImage(t, 0, 0, 4, 3, false)
	frame2 := gifImage(t, 1, 1, 2, 2, true)
	netscape := slices.Concat([]byte{0x21, 0xff, 11}, []byte("NETSCAPE2.0"), []byte{3, 1, 0, 0, 0})
	xmp := slices.Concat([]byte{0x21, 0xff, 11}, []byte("XMP DataXMP"), gifSubBlocks([]byte("<x:xmpmeta/>")))
	text := slices.Concat([]byte{0x21, 0x01, 12}, make([]byte, 12), gifSubBlocks([]byte("text")))
	animation := gifFile(4, 3, frame, frame2)
	noTrailer := animation[:len(animation)-1]
	tests := []inspectTest{
		{"still", gifFile(4, 3, frame), gifInfo(4, 3, false)},
		{"still with a local color table", gifFile(4, 3, gifImage(t, 0, 0, 4, 3, true)), gifInfo(4, 3, false)},
		{"animation", animation, gifInfo(4, 3, true)},
		{"animation with extensions", gifFile(4, 3, netscape, gifExtension(0xfe, []byte("comment")), gifControl(),
			frame, xmp, text, gifControl(), frame2, gifExtension(0xfe, nil)), gifInfo(4, 3, true)},
		{"animation without the trailer", noTrailer, gifInfo(4, 3, true)},
		{"animation with data after the trailer", append(slices.Clone(animation), "data"...), gifInfo(4, 3, true)},
		{"animation that ends at a byte that is not a block", append(slices.Clone(noTrailer), 0x00, 0x2c),
			gifInfo(4, 3, true)},
		{"frame after a byte that is not a block", gifFile(4, 3, frame, []byte{0x00}, frame2), gifInfo(4, 3, false)},
		{"frames of no pixels", gifFile(4, 3, gifImage(t, 0, 0, 0, 0, false), gifImage(t, 0, 0, 0, 0, false)),
			gifInfo(4, 3, true)},
		{"truncated in the image data of frame 2", animation[:len(animation)-3], gifInfo(4, 3, false).with("",
			gifCutWarning)},
		{"truncated in the local color table of frame 2", slices.Concat(gifFile(4, 3, frame)[:len(gifFile(4, 3,
			frame))-1], frame2[:12]), gifInfo(4, 3, false).with("", gifCutWarning)},
		{"truncated in the image descriptor of frame 2", slices.Concat(noTrailer[:len(noTrailer)-len(frame2)],
			frame2[:9]), gifInfo(4, 3, false).with("", gifCutWarning)},
		{"truncated in an extension", slices.Concat(gifFile(4, 3, frame)[:len(gifFile(4, 3, frame))-1],
			netscape[:10]), gifInfo(4, 3, false).with("", gifCutWarning)},
		{"truncated after the label of an extension", slices.Concat(gifFile(4, 3, frame)[:len(gifFile(4, 3,
			frame))-1], []byte{0x21, 0xfe}), gifInfo(4, 3, false).with("", gifCutWarning)},
		{"truncated after the introducer of an extension", slices.Concat(gifFile(4, 3, frame)[:len(gifFile(4, 3,
			frame))-1], []byte{0x21}), gifInfo(4, 3, false).with("", gifCutWarning)},
	}
	checkInspect(t, tests)

	// The test files decode as their Info says.
	for _, tt := range tests[:4] {
		if _, err := gif.DecodeAll(bytes.NewReader(tt.data)); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}

func TestInspectGIFFramePixels(t *testing.T) {
	defer func(n int64) { maxAnimationPixels = n }(maxAnimationPixels)
	data := gifFile(4, 3, gifImage(t, 0, 0, 4, 3, false), gifImage(t, 0, 0, 4, 3, false))
	maxAnimationPixels = 24
	checkInspect(t, []inspectTest{{"at most maxAnimationPixels", data, gifInfo(4, 3, true)}})
	maxAnimationPixels = 23
	checkInspect(t, []inspectTest{{"more than maxAnimationPixels", data, gifInfo(4, 3, false).with("",
		"GIF animation with 24 pixels in its frames (max 23); published as its first frame")}})
}

// webpVector is a vector of ../webp/testdata/vectors.json.
type webpVector struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Animated    bool   `json:"animated"`
	Orientation int    `json:"orientation"`
	ICCSHA256   string `json:"iccSHA256"`
}

func TestInspectWebPVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "webp", "testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Vectors []webpVector `json:"vectors"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// The profiles of the vectors, which go generate makes in ../webp/gen.
	decisions := map[string]string{
		"317689309add8dc5ba39df2aa301c1c9b1fbfa11edbe104270be56c192cbb136": iccKeep, // Display P3
		"647432aee60754b3a04a34bf0a11cfdc20315de6b6d378a37a75bbb14ca39dd2": iccSRGB,
	}
	var tests []inspectTest
	kinds := make(map[string]int)
	for _, v := range m.Vectors {
		o := max(v.Orientation, 1)
		want := Info{MIMEType: mediafile.WebP, Width: v.Width, Height: v.Height, Orientation: o, Format: mediafile.PNG}
		if o >= 5 {
			want.Width, want.Height = v.Height, v.Width
		}
		switch {
		case v.Animated && o == 1:
			want.Animated = true
		case v.Animated:
			want.Warnings = []string{fmt.Sprintf("animation with EXIF orientation %d; published as its first frame", o)}
		case v.Kind == "lossy" || v.Kind == "vpx":
			want.Format = mediafile.JPEG
		}
		if v.ICCSHA256 != "" {
			want.ICC = decisions[v.ICCSHA256]
			if want.ICC == "" {
				t.Fatalf("%s: unknown profile %s", v.Name, v.ICCSHA256)
			}
		}
		tests = append(tests, inspectTest{v.Name, readVector(t, v.Name), want})
		kinds[fmt.Sprintf("%s %s %s %t", v.Kind, want.Format, want.ICC, want.Animated)]++
	}
	if len(tests) < 100 {
		t.Fatalf("%d vectors", len(tests))
	}
	checkInspect(t, tests)
	t.Logf("vectors of each kind, format, ICC and animation: %v", kinds)
}

func TestInspectWebP(t *testing.T) {
	lossy := webpChunks(readVector(t, "lossy-photo-16x16"))["VP8 "]
	lossless := webpChunks(readVector(t, "lossless-photo-16x16"))["VP8L"]
	const flagICC = 0x20
	webpInfo := func(format string) Info {
		return Info{MIMEType: mediafile.WebP, Width: 16, Height: 16, Orientation: 1, Format: format}
	}
	tests := []inspectTest{
		{"lossy", webpFile(lossy), webpInfo(mediafile.JPEG)},
		{"lossless", webpFile(lossless), webpInfo(mediafile.PNG)},
		{"lossy with Display P3", webpFile(vp8xChunk(flagICC, 16, 16), riffChunk("ICCP", p3Profile), lossy),
			webpInfo(mediafile.JPEG).with(iccKeep)},
		{"lossless with sRGB", webpFile(vp8xChunk(flagICC, 16, 16), riffChunk("ICCP", srgbProfile), lossless),
			webpInfo(mediafile.PNG).with(iccSRGB)},
		{"grayscale profile", webpFile(vp8xChunk(flagICC, 16, 16), riffChunk("ICCP", grayProfile(srgbPara)), lossy),
			webpInfo(mediafile.JPEG).with(iccInvalid, invalidWarning(`color space "GRAY" on a color image`))},
		{"empty profile", webpFile(vp8xChunk(flagICC, 16, 16), riffChunk("ICCP", nil), lossy),
			webpInfo(mediafile.JPEG).with(iccInvalid, invalidWarning("only 0 bytes"))},
		{"profile without the flag", webpFile(vp8xChunk(0, 16, 16), riffChunk("ICCP", p3Profile), lossy),
			webpInfo(mediafile.JPEG)},
		{"flag without a profile", webpFile(vp8xChunk(flagICC, 16, 16), lossy), webpInfo(mediafile.JPEG)},
		{"lossy with alpha", webpFile(vp8xChunk(0x10, 16, 16),
			riffChunk("ALPH", append([]byte{0}, bytes.Repeat([]byte{0x80}, 16*16)...)), lossy), webpInfo(mediafile.PNG)},
		{"alpha flag without ALPH", webpFile(vp8xChunk(0x10, 16, 16), lossy), webpInfo(mediafile.JPEG)},
	}
	checkInspect(t, tests)
}

func TestInspectError(t *testing.T) {
	file := jpegOf(t, colored(3, 2))
	jfif := jpegSegment(0xe0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	sos := jpegMarker(t, file, 0xda)
	pngData := pngOf(t, colored(3, 2))
	idat := len(pngData) - 12 - len(chunksOf(pngData)[1].data) - 12
	frame := gifImage(t, 0, 0, 4, 3, false)
	lossy := webpChunks(readVector(t, "lossy-photo-16x16"))["VP8 "]
	tests := []struct {
		name string
		data []byte
		err  string
		is   error
	}{
		{name: "empty", data: nil, is: mediafile.ErrUnsupported},
		{name: "SVG", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), is: mediafile.ErrUnsupported},
		{name: "PDF", data: []byte("%PDF-1.7\n"), err: "not an image: application/pdf"},
		{name: "too large", data: make([]byte, content.MaxMediaBytes+1), err: "too large: 67108865 bytes (max 67108864)"},
		{name: "PNG of too many pixels", data: pngFile(10001, 10000, 8, 2, pngChunk("IDAT", nil), pngChunk("IEND", nil)),
			err: "too many pixels: 10001x10000 (max 100000000)"},
		{name: "GIF of too many pixels", data: gifFile(20000, 5001, frame),
			err: "too many pixels: 20000x5001 (max 100000000)"},
		{name: "WebP of too many pixels", data: webpFile(vp8xChunk(0, 16384, 16384), lossy),
			err: "too many pixels: 16384x16384 (max 100000000)"},
		{name: "JPEG of 2 bytes", data: []byte{0xff, 0xd8}, is: mediafile.ErrUnsupported},
		{name: "JPEG cut before SOS", data: insertJPEG(file[:sos], jfif), err: "invalid JPEG: no image data"},
		{name: "JPEG with EOI before SOS", data: slices.Concat(insertJPEG(file[:sos], jfif), []byte{0xff, 0xd9},
			file[sos:]), err: "invalid JPEG: no image data"},
		{name: "PNG cut before IDAT", data: pngData[:idat], err: "invalid PNG: no image data"},
		{name: "PNG cut in IDAT's header", data: pngData[:idat+7], err: "invalid PNG: no image data"},
		{name: "PNG with a chunk past the end", data: insertPNG(pngData, patch32(pngChunk("tEXt", nil), 0, 1<<31)),
			err: "invalid PNG: no image data"},
		{name: "PNG with a chunk cut before IDAT", data: insertPNG(pngData[:idat], pngChunk("tEXt", []byte("a\x00b"))[:12]),
			err: "invalid PNG: no image data"},
		{name: "GIF without images", data: gifFile(4, 3), err: "invalid GIF: no image data"},
		{name: "GIF without blocks", data: gifFile(4, 3)[:19], err: "invalid GIF: no image data"},
		{name: "GIF cut in its first image", data: gifFile(4, 3, frame)[:25], err: "invalid GIF: no image data"},
		{name: "GIF cut in an extension before its first image",
			data: gifFile(4, 3, gifExtension(0xfe, []byte("comment")))[:22], err: "invalid GIF: no image data"},
		{name: "GIF with a byte that is not a block before its first image", data: gifFile(4, 3, []byte{0x00}, frame),
			err: "invalid GIF: no image data"},
		{name: "WebP of an image smaller than its canvas", data: webpFile(vp8xChunk(0, 17, 16), lossy),
			err: "webp: image of 16x16 pixels on a canvas of 17x16"},
	}
	for _, tt := range tests {
		got, err := Inspect(tt.data)
		switch {
		case err == nil:
			t.Errorf("%s: Inspect() = %+v, want an error", tt.name, got)
		case tt.is != nil && !errors.Is(err, tt.is), tt.is == nil && err.Error() != tt.err:
			t.Errorf("%s: Inspect() error = %v, want %q (%v)", tt.name, err, tt.err, tt.is)
		}
	}
}

func TestInspectLimit(t *testing.T) {
	data := pngFile(10000, 10000, 8, 2, pngChunk("IDAT", nil), pngChunk("IEND", nil))
	if got, err := Inspect(data); err != nil || !equalInfo(got, pngInfo(10000, 10000, 1)) {
		t.Errorf("Inspect() = %+v, %v; want an Info of 10000x10000 pixels", got, err)
	}
	data = gifFile(10000, 10000, gifImage(t, 0, 0, 1, 1, false))
	if got, err := Inspect(data); err != nil || !equalInfo(got, gifInfo(10000, 10000, false)) {
		t.Errorf("Inspect() = %+v, %v; want an Info of 10000x10000 pixels", got, err)
	}
}

// inspectSeeds returns originals of every format for fuzzing.
func inspectSeeds(t testing.TB) [][]byte {
	file := jpegOf(t, colored(3, 2))
	pngData := pngOf(t, colored(3, 2))
	frame := gifImage(t, 0, 0, 4, 3, false)
	return [][]byte{
		insertJPEG(file, exifSegment(6), iccSegment(1, 2, p3Profile[:100]), iccSegment(2, 2, p3Profile[100:])),
		insertJPEG(jpegOf(t, image.NewGray(image.Rect(0, 0, 3, 2))), iccSegment(1, 1, grayProfile(gamma22))),
		cmykJPEG(2),
		insertPNG(pngData, pngChunk("eXIf", exifTIFF(6)), iccpChunk(t, p3Profile)),
		grayAlphaPNG(t, 3, 2, iccpChunk(t, grayProfile(curvTable(26, srgbTransfer)))),
		apngFile(t, colored(3, 2)),
		gifFile(4, 3, frame, gifControl(), gifImage(t, 1, 1, 2, 2, true)),
		readVector(t, "meta-all-alpha"),
		readVector(t, "meta-anim-exif-o6"),
		readVector(t, "lossy-photo-16x16"),
	}
}

func FuzzInspect(f *testing.F) {
	for _, data := range inspectSeeds(f) {
		f.Add(data)
	}
	formats := []string{mediafile.JPEG, mediafile.PNG}
	iccs := []string{"", iccKeep, iccSRGB, iccInvalid, iccCMYK, iccGray}
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := Inspect(data)
		if err != nil {
			if !equalInfo(info, Info{}) {
				t.Fatalf("Inspect() = %+v with error %v", info, err)
			}
			return
		}
		switch {
		case content.MediaKind(info.MIMEType) != content.MediaImage,
			info.Width < 1, info.Height < 1, info.Width > content.MaxImagePixels/info.Height,
			info.Orientation < 1, info.Orientation > 8,
			!slices.Contains(formats, info.Format), !slices.Contains(iccs, info.ICC),
			info.Animated && (info.MIMEType == mediafile.JPEG || info.Orientation != 1),
			info.ICC == iccCMYK && info.MIMEType != mediafile.JPEG:
			t.Fatalf("Inspect() = %+v", info)
		}
		for _, w := range info.Warnings {
			if w == "" || strings.ContainsAny(w, "\n\x00") {
				t.Fatalf("warning %q", w)
			}
		}
		icc, err := keptICC(data, info)
		if err != nil || (icc != nil) != (info.ICC == iccKeep) {
			t.Fatalf("keptICC() = %d bytes, %v; ICC %q", len(icc), err, info.ICC)
		}
		if again, err := Inspect(data); err != nil || !equalInfo(again, info) {
			t.Fatalf("a second Inspect() = %+v, %v; first %+v", again, err, info)
		}
		pub := Published(info)
		for _, size := range []*content.ImageSize{nil, {Width: 150, Height: 150, Crop: true}, {Width: 1024}} {
			checkSet(t, fmt.Sprint(size), info, pub, ForSize(info, size))
		}
		for _, n := range IconSizes() {
			if v := Icon(info, n); v.Width != v.Height || v.Width > n || v.Width < 1 {
				t.Fatalf("Icon(%d) = %+v", n, v)
			}
		}
		if name := Name("photo.jpg", info, pub); len(name) > content.MaxFileNameBytes {
			t.Fatalf("Name() = %q", name)
		}
	})
}

// TestVectorProfiles checks that the profiles of the vectors are those that TestInspectWebPVectors knows, and that the
// still files keep the one that is not sRGB as it is.
func TestVectorProfiles(t *testing.T) {
	tests := []struct {
		name, sha256 string
		kept         bool
	}{
		{"meta-icc-p3-lossy", "317689309add8dc5ba39df2aa301c1c9b1fbfa11edbe104270be56c192cbb136", true},
		{"meta-icc-srgb-lossless", "647432aee60754b3a04a34bf0a11cfdc20315de6b6d378a37a75bbb14ca39dd2", false},
	}
	for _, tt := range tests {
		data := readVector(t, tt.name)
		f, err := webp.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if s := sha256.Sum256(f.ICC); hex.EncodeToString(s[:]) != tt.sha256 {
			t.Errorf("%s: profile with SHA-256 %x, want %s", tt.name, s, tt.sha256)
		}
		info, err := Inspect(data)
		if err != nil {
			t.Fatal(err)
		}
		var want []byte
		if tt.kept {
			want = f.ICC
		}
		if p, err := keptICC(data, info); err != nil || !bytes.Equal(p, want) || (p == nil) != (want == nil) {
			t.Errorf("%s: keptICC() = %d bytes, %v; want %d bytes", tt.name, len(p), err, len(want))
		}
	}
}
