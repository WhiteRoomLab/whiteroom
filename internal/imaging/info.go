package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
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
// file in no format of media, more than content.MaxMediaBytes of data, and an image of more than
// content.MaxImagePixels.
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
	o := original{format: mediafile.JPEG}
	switch c.ColorModel {
	case color.GrayModel:
		o.gray, o.grayFiles = true, true
	case color.CMYKModel:
		o.cmyk = true
	}
	segs, err := jpegICCSegments(data)
	if err != nil {
		return original{}, err
	}
	if segs != nil {
		o.icc, o.iccErr = joinICCSegments(segs)
	}
	return o, nil
}

// jpegICCSegments returns the data after iccSignature of the APP2 segments before the first SOS, where image/jpeg also
// stops reading the header. Like image/jpeg, it skips bytes that are not markers.
func jpegICCSegments(data []byte) ([][]byte, error) {
	var segs [][]byte
	for i := 2; ; { // after SOI
		for i < len(data) && data[i] != 0xff {
			i++
		}
		for i < len(data) && data[i] == 0xff { // fill bytes before the marker
			i++
		}
		if i == len(data) {
			return nil, errors.New("invalid JPEG: no image data")
		}
		marker := data[i]
		i++
		switch {
		case marker == 0x00, 0xd0 <= marker && marker <= 0xd7: // not a marker, or RSTn without data
			continue
		case marker == 0xd9: // EOI
			return nil, errors.New("invalid JPEG: no image data")
		case len(data)-i < 2:
			return nil, errors.New("invalid JPEG: no image data")
		}
		n := int(binary.BigEndian.Uint16(data[i:])) // including these 2 bytes
		switch {
		case n < 2:
			return nil, errors.New("invalid JPEG: short segment length")
		case marker == 0xda: // SOS
			return segs, nil
		case n > len(data)-i:
			return nil, errors.New("invalid JPEG: no image data")
		}
		if seg := data[i+2 : i+n]; marker == 0xe2 && bytes.HasPrefix(seg, []byte(iccSignature)) {
			segs = append(segs, seg[len(iccSignature):])
		}
		i += n
	}
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
	return o, nil
}

// readGIF reads the blocks of a GIF file. It publishes an animation as its first frame if it is truncated, as it cannot
// be rebuilt, or if its frames have more than maxAnimationPixels in all.
func readGIF(data []byte) (original, error) {
	o := original{format: mediafile.PNG}
	frames, pixels, complete := gifFrames(data)
	switch {
	case frames == 0:
		return original{}, errors.New("invalid GIF: no image data")
	case !complete:
		o.warnings = []string{"truncated GIF; published as its first frame"}
	case frames > 1 && pixels > maxAnimationPixels:
		o.warnings = []string{fmt.Sprintf("GIF animation with %d pixels in its frames (max %d); published as its first frame",
			pixels, maxAnimationPixels)}
	case frames > 1:
		o.animated = true
	}
	return o, nil
}

// gifFrames returns the number of the images in a GIF file, the sum of their areas, and whether the blocks end without
// being truncated: at the trailer, at the end of data, or at a byte that does not start a block, where browsers also
// stop.
func gifFrames(data []byte) (frames int, pixels int64, complete bool) {
	// gif.DecodeConfig, which mediafile.Probe calls, has read the logical screen descriptor and the global color table.
	i := 13
	if data[10]&0x80 != 0 {
		i += 3 << (data[10]&7 + 1)
	}
	for i < len(data) {
		var ok bool
		switch data[i] {
		case 0x21: // an extension: its label and data sub-blocks
			i, ok = skipSubBlocks(data, i+2)
		case 0x2c: // an image descriptor, a local color table, the minimum code size of LZW and data sub-blocks
			if len(data)-i < 10 {
				return frames, pixels, false
			}
			w, h := binary.LittleEndian.Uint16(data[i+5:]), binary.LittleEndian.Uint16(data[i+7:])
			j := i + 10
			if data[i+9]&0x80 != 0 {
				j += 3 << (data[i+9]&7 + 1)
			}
			if i, ok = skipSubBlocks(data, j+1); ok {
				frames++
				pixels += int64(w) * int64(h)
			}
		default: // the trailer, or not a block
			return frames, pixels, true
		}
		if !ok {
			return frames, pixels, false
		}
	}
	return frames, pixels, true
}

// skipSubBlocks returns the index after the data sub-blocks at i in data, and false if they do not end in data.
func skipSubBlocks(data []byte, i int) (int, bool) {
	for i < len(data) {
		n := int(data[i])
		i += 1 + n
		if n == 0 {
			return i, true
		}
	}
	return 0, false
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
	return o, nil
}
