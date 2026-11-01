package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/webp"
)

// Info is what Inspect reads from the original of an image medium.
type Info struct {
	MIMEType      string // of the original: image/jpeg, image/png, image/gif or image/webp
	Width, Height int    // in pixels as displayed, with Orientation applied
	Orientation   int    // the EXIF orientation, from 1 to 8
	// Animated is set if the published file is the animation: a GIF of 2 or more frames, an APNG or an animated WebP.
	// An APNG or a WebP whose orientation is not 1, and a GIF that is truncated or has more than maxAnimationPixels in
	// its frames, are published as their first frame, with a warning.
	Animated bool
	Format   string // the MIME type of the still files made from it: image/jpeg or image/png
	// ICC tells what happens to the ICC profile of the original in the still files: "" (there is none), "keep", or it
	// is left out as "srgb" (browsers show images without a profile as sRGB), "invalid", "cmyk" (a CMYK JPEG, which is
	// converted to RGB) or "gray" (a profile of a grayscale PNG with transparency, whose still files are RGBA).
	ICC      string
	Warnings []string // in English; the same for the same original
	Size     int      // of the original, in bytes
	// Memory is about how many bytes decoding the original into the image that its still files are made from takes:
	// the image and the buffers of the decoder.
	Memory int64
}

// The values of Info.ICC other than "".
const (
	iccKeep    = "keep"
	iccSRGB    = "srgb"
	iccInvalid = "invalid"
	iccCMYK    = "cmyk"
	iccGray    = "gray"
)

// Inspect reads the original of an image medium, data, without decoding its pixels. It returns an error for a PDF, a
// file in no format of media, more than content.MaxMediaBytes of data, an image of more than content.MaxImagePixels
// or with a side of more than 65535 pixels, and headers that the decoders do not read.
func Inspect(data []byte) (Info, error) {
	if len(data) > content.MaxMediaBytes {
		return Info{}, fmt.Errorf("too large: %d bytes (max %d)", len(data), content.MaxMediaBytes)
	}
	p, err := mediafile.Probe(bytes.NewReader(data))
	if err != nil {
		return Info{}, err
	}
	if content.MediaKind(p.MIMEType) != content.MediaImage {
		return Info{}, fmt.Errorf("not an image: %s", p.MIMEType)
	}
	if p.Width > content.MaxImagePixels/p.Height {
		return Info{}, fmt.Errorf("too many pixels: %dx%d (max %d)", p.Width, p.Height, content.MaxImagePixels)
	}
	if max(p.Width, p.Height) > maxSide {
		return Info{}, fmt.Errorf("too long a side: %dx%d (max %d)", p.Width, p.Height, maxSide)
	}
	o, err := readOriginal(data, p.MIMEType)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		MIMEType:    p.MIMEType,
		Width:       p.Width,
		Height:      p.Height,
		Orientation: p.Orientation,
		Animated:    o.animated,
		Format:      o.format,
		Warnings:    o.warnings,
		Size:        len(data),
		Memory:      o.memory,
	}
	if info.Animated && info.Orientation != 1 {
		info.Animated = false
		info.Warnings = append(info.Warnings,
			fmt.Sprintf("animation with EXIF orientation %d; published as its first frame", info.Orientation))
	}
	var warning string
	info.ICC, warning = o.iccDecision()
	// An animation keeps its profile as it is.
	if warning != "" && !info.Animated {
		info.Warnings = append(info.Warnings, warning)
	}
	return info, nil
}

// original is what readOriginal reads from an image, apart from what mediafile.Probe does.
type original struct {
	format    string // of the still files
	animated  bool
	gray      bool // a grayscale image, whose profile must be GRAY
	grayFiles bool // the still files are grayscale
	cmyk      bool
	icc       []byte // the ICC profile; nil if none
	iccErr    error  // why the ICC profile cannot be read
	warnings  []string
	memory    int64 // Info.Memory
}

// readOriginal reads data, an image of the MIME type that mediafile.Probe has read.
func readOriginal(data []byte, mimeType string) (original, error) {
	switch mimeType {
	case mediafile.JPEG:
		return readJPEG(data)
	case mediafile.PNG:
		return readPNG(data)
	case mediafile.GIF:
		return readGIF(data)
	case mediafile.WebP:
		return readWebP(data)
	}
	return original{}, fmt.Errorf("not an image: %s", mimeType)
}

// iccDecision returns Info.ICC and a warning about it, or "".
func (o *original) iccDecision() (string, string) {
	switch {
	case o.cmyk:
		return iccCMYK, "CMYK JPEG; converted to RGB without color management, so its colors may change"
	case o.icc == nil && o.iccErr == nil:
		return "", ""
	}
	err := o.iccErr
	if err == nil {
		err = checkICC(o.icc, o.gray)
	}
	switch {
	case err != nil:
		return iccInvalid, fmt.Sprintf("invalid ICC profile (%v); published without it", err)
	case isSRGB(o.icc, o.gray):
		return iccSRGB, ""
	case o.gray && !o.grayFiles:
		return iccGray, "grayscale ICC profile of a PNG with transparency; published in RGBA without it"
	}
	return iccKeep, ""
}

// keptICC returns the ICC profile that the still files made from data, an original that Inspect read as info, have;
// nil if they have none.
func keptICC(data []byte, info Info) ([]byte, error) {
	if info.ICC != iccKeep {
		return nil, nil
	}
	o, err := readOriginal(data, info.MIMEType)
	if err != nil {
		return nil, err
	}
	return o.icc, nil
}

func readJPEG(data []byte) (original, error) {
	c, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return original{}, fmt.Errorf("invalid JPEG: %w", err)
	}
	h, err := readJPEGHeader(data)
	if err != nil {
		return original{}, err
	}
	o := original{format: mediafile.JPEG, memory: h.memory(c)}
	switch c.ColorModel {
	case color.GrayModel:
		o.gray, o.grayFiles = true, true
	case color.CMYKModel:
		if !h.adobe {
			// image/jpeg cannot tell CMYK from YCCK without it.
			return original{}, errors.New("unsupported JPEG: 4 components without an Adobe APP14 segment")
		}
		o.cmyk = true
	}
	if h.icc != nil {
		o.icc, o.iccErr = joinICCSegments(h.icc)
	}
	return o, nil
}

// jpegHeader is what readJPEGHeader reads.
type jpegHeader struct {
	icc         [][]byte // the data after iccSignature of the APP2 segments
	adobe       bool     // an APP14 segment that image/jpeg reads as Adobe's
	progressive bool
	sampling    [][2]int // the horizontal and vertical sampling factors of the components in the frame header
}

// readJPEGHeader reads the segments before the first SOS, where image/jpeg also stops reading the header. Like
// image/jpeg, it skips bytes that are not markers.
func readJPEGHeader(data []byte) (jpegHeader, error) {
	var h jpegHeader
	for i := 2; ; { // after SOI
		for i < len(data) && data[i] != 0xff {
			i++
		}
		for i < len(data) && data[i] == 0xff { // fill bytes before the marker
			i++
		}
		if i == len(data) {
			return jpegHeader{}, errors.New("invalid JPEG: no image data")
		}
		marker := data[i]
		i++
		switch {
		case marker == 0x00, 0xd0 <= marker && marker <= 0xd7: // not a marker, or RSTn without data
			continue
		case marker == 0xd9: // EOI
			return jpegHeader{}, errors.New("invalid JPEG: no image data")
		case len(data)-i < 2:
			return jpegHeader{}, errors.New("invalid JPEG: no image data")
		}
		n := int(binary.BigEndian.Uint16(data[i:])) // including these 2 bytes
		switch {
		case n < 2:
			return jpegHeader{}, errors.New("invalid JPEG: short segment length")
		case marker == 0xda: // SOS
			return h, nil
		case n > len(data)-i:
			return jpegHeader{}, errors.New("invalid JPEG: no image data")
		}
		seg := data[i+2 : i+n]
		switch {
		case marker == 0xe2 && bytes.HasPrefix(seg, []byte(iccSignature)):
			h.icc = append(h.icc, seg[len(iccSignature):])
		case marker == 0xee && len(seg) >= 12 && bytes.HasPrefix(seg, []byte("Adobe")):
			h.adobe = true
		case 0xc0 <= marker && marker <= 0xc2 && h.sampling == nil && len(seg) >= 6 && len(seg) >= 6+3*int(seg[5]):
			// SOF0, SOF1 or SOF2, the frame headers that image/jpeg reads
			h.progressive = marker == 0xc2
			h.sampling = make([][2]int, seg[5])
			for k := range h.sampling {
				hv := seg[7+3*k]
				h.sampling[k] = [2]int{min(max(int(hv>>4), 1), 4), min(max(int(hv&15), 1), 4)}
			}
		}
		i += n
	}
}

// memory returns about how many bytes image/jpeg takes to decode the image of c with the header h: the planes of the
// components, allocated in whole MCUs, the coefficients of a progressive image, and the image that RGB and CMYK are
// converted to.
func (h *jpegHeader) memory(c image.Config) int64 {
	s := h.sampling
	if len(s) == 1 {
		s = [][2]int{{1, 1}} // image/jpeg ignores the factors of grayscale images
	}
	maxH, maxV := 1, 1
	for _, f := range s {
		maxH, maxV = max(maxH, f[0]), max(maxV, f[1])
	}
	mcus := int64((c.Width+8*maxH-1)/(8*maxH)) * int64((c.Height+8*maxV-1)/(8*maxV))
	var samples int64
	for _, f := range s {
		samples += 64 * mcus * int64(f[0]*f[1])
	}
	planes := samples
	if len(s) >= 3 {
		// Unless the components have a standard subsampling ratio, image/jpeg decodes all three into full planes.
		r := maxH/s[1][0]<<4 | maxV/s[1][1]
		standard := s[1] == s[2] && s[0] == [2]int{maxH, maxV} && maxH%s[1][0] == 0 && maxV%s[1][1] == 0 &&
			(r == 0x11 || r == 0x12 || r == 0x21 || r == 0x22 || r == 0x41 || r == 0x42)
		if !standard {
			full := 64 * mcus * int64(maxH*maxV)
			planes += 3*full - 64*mcus*int64(s[0][0]*s[0][1]+s[1][0]*s[1][1]+s[2][0]*s[2][1])
		}
	}
	m := planes
	if h.progressive {
		m += 4 * samples // a block of 64 int32 for each 64 samples
	}
	if c.ColorModel == color.RGBAModel || c.ColorModel == color.CMYKModel {
		m += 4 * int64(c.Width) * int64(c.Height)
	}
	return m
}

const (
	pngSignature = "\x89PNG\r\n\x1a\n"
	pngIHDREnd   = len(pngSignature) + 12 + 13 // the end of the IHDR chunk, which comes first
)

// readPNG reads the chunks before the first IDAT, where iCCP, tRNS and acTL come (PNG Third Edition 5.6).
func readPNG(data []byte) (original, error) {
	// png.DecodeConfig, which mediafile.Probe calls, has read IHDR.
	ihdr := data[len(pngSignature)+8 : pngIHDREnd-4]
	colorType := ihdr[9]
	o := original{format: mediafile.PNG, gray: colorType == 0 || colorType == 4}
	var iccp []byte
	trns := false
	for i := pngIHDREnd; ; {
		if len(data)-i < 8 {
			return original{}, errors.New("invalid PNG: no image data")
		}
		n := binary.BigEndian.Uint32(data[i:])
		typ := string(data[i+4 : i+8])
		if typ == "IDAT" {
			break
		}
		if uint64(len(data)-i) < 12+uint64(n) {
			return original{}, errors.New("invalid PNG: no image data")
		}
		switch typ {
		case "iCCP":
			if iccp == nil {
				iccp = data[i+8 : i+8+int(n)]
			}
		case "tRNS":
			trns = true
		case "acTL":
			o.animated = true
		}
		i += 12 + int(n)
	}
	// image/png decodes grayscale images with transparency as RGBA.
	o.grayFiles = colorType == 0 && !trns
	if iccp != nil {
		o.icc, o.iccErr = pngICC(iccp)
	}
	// The bytes of a pixel in the image that image/png decodes, which also has an image of each pass of an interlaced
	// image.
	n := 4
	if colorType == 3 || colorType == 0 && !trns {
		n = 1
	}
	if ihdr[8] == 16 {
		n *= 2
	}
	if ihdr[12] != 0 {
		n *= 2
	}
	o.memory = int64(n) * int64(binary.BigEndian.Uint32(ihdr)) * int64(binary.BigEndian.Uint32(ihdr[4:]))
	return o, nil
}

// readGIF reads the blocks of a GIF file. It publishes an animation as its first frame if it is truncated, as it cannot
// be rebuilt, or if its frames have more than maxAnimationPixels in all.
func readGIF(data []byte) (original, error) {
	s, err := scanGIF(data)
	if err != nil {
		return original{}, err
	}
	// The indexes of the first frame and the canvas it is drawn on.
	screen := int64(binary.LittleEndian.Uint16(data[6:])) * int64(binary.LittleEndian.Uint16(data[8:]))
	o := original{format: mediafile.PNG, memory: 5 * screen}
	switch {
	case s.frames == 0:
		return original{}, errors.New("invalid GIF: no image data")
	case !s.complete:
		o.warnings = []string{"truncated GIF; published as its first frame"}
	case s.frames > 1 && s.pixels > maxAnimationPixels:
		o.warnings = []string{fmt.Sprintf("GIF animation with %d pixels in its frames (max %d); published as its first frame",
			s.pixels, maxAnimationPixels)}
	case s.frames > 1:
		o.animated = true
	}
	return o, nil
}

func readWebP(data []byte) (original, error) {
	f, err := webp.Parse(data)
	if err != nil {
		return original{}, err
	}
	o := original{format: mediafile.PNG, animated: f.Animated, icc: f.ICC}
	if !f.Animated && !f.Lossless && !f.Alpha {
		o.format = mediafile.JPEG
	}
	// The planes of a lossy frame and its RGBA, or the ARGB of a lossless one, and the canvas of an animation.
	n := 6
	switch {
	case f.Animated:
		n = 10
	case f.Lossless:
		n = 4
	}
	o.memory = int64(n) * int64(f.Width) * int64(f.Height)
	return o, nil
}
