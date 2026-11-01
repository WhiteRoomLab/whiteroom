package imaging

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/webp"
)

// decode returns the image of data, an original that Inspect read as info, as stored, before its orientation: an
// *image.YCbCr or *image.Gray, an *image.RGBA of an opaque image, an *image.NRGBA, or an *image.Paletted. A JPEG in
// CMYK becomes RGB as color.CMYK converts it, and 16 bits per sample become 8, rounded. A GIF and an animated WebP give
// their first frame on a transparent canvas, and an APNG its default image.
func decode(data []byte, info Info) (image.Image, error) {
	w, h := stored(info.Width, info.Height, info.Orientation)
	if w < 1 || h < 1 || w > content.MaxImagePixels/h {
		return nil, fmt.Errorf("image of %dx%d pixels", w, h)
	}
	var (
		m   image.Image
		err error
	)
	switch info.MIMEType {
	case mediafile.JPEG:
		if err = checkConfig(data, jpeg.DecodeConfig, w, h); err == nil {
			m, err = jpeg.Decode(bytes.NewReader(data))
		}
	case mediafile.PNG:
		if err = checkConfig(data, png.DecodeConfig, w, h); err == nil {
			m, err = png.Decode(bytes.NewReader(data))
		}
	case mediafile.GIF:
		if err = checkConfig(data, gif.DecodeConfig, w, h); err == nil {
			m, err = decodeGIF(data)
		}
	case mediafile.WebP:
		var f *webp.File
		if f, err = webp.Parse(data); err == nil {
			if f.Width != w || f.Height != h {
				return nil, fmt.Errorf("image of %dx%d pixels, not %dx%d", f.Width, f.Height, w, h)
			}
			m, err = f.Image()
		}
	default:
		return nil, fmt.Errorf("cannot decode %s", info.MIMEType)
	}
	if err != nil {
		return nil, err
	}
	if m.Bounds() != image.Rect(0, 0, w, h) {
		return nil, fmt.Errorf("image of %v, not %dx%d pixels", m.Bounds(), w, h)
	}
	switch m := m.(type) {
	case *image.YCbCr, *image.Gray, *image.RGBA, *image.NRGBA, *image.Paletted:
		return m, nil
	case *image.CMYK:
		// In place: CMYK and RGBA both have 4 bytes per pixel.
		for y := range h {
			p := m.Pix[y*m.Stride : y*m.Stride+4*w]
			for i := 0; i < len(p); i += 4 {
				p[i], p[i+1], p[i+2] = color.CMYKToRGB(p[i], p[i+1], p[i+2], p[i+3])
				p[i+3] = 0xff
			}
		}
		return &image.RGBA{Pix: m.Pix, Stride: m.Stride, Rect: m.Rect}, nil
	case *image.Gray16:
		return &image.Gray{Pix: to8Bits(m.Pix, m.Stride, w, h, 1), Stride: w, Rect: m.Rect}, nil
	case *image.RGBA64:
		return &image.RGBA{Pix: to8Bits(m.Pix, m.Stride, w, h, 4), Stride: 4 * w, Rect: m.Rect}, nil
	case *image.NRGBA64:
		return &image.NRGBA{Pix: to8Bits(m.Pix, m.Stride, w, h, 4), Stride: 4 * w, Rect: m.Rect}, nil
	}
	return nil, fmt.Errorf("cannot use an image of type %T", m)
}

// checkConfig returns an error unless decodeConfig reads data as an image of w x h pixels.
func checkConfig(data []byte, decodeConfig func(io.Reader) (image.Config, error), w, h int) error {
	c, err := decodeConfig(bytes.NewReader(data))
	switch {
	case err != nil:
		return err
	case c.Width != w || c.Height != h:
		return fmt.Errorf("image of %dx%d pixels, not %dx%d", c.Width, c.Height, w, h)
	}
	return nil
}

// to8Bits rounds the big-endian 16-bit samples of a w x h image with n samples a pixel, at the start of the rows of
// stride bytes of pix, to 8 bits, in place, and returns them in rows of n*w bytes.
func to8Bits(pix []byte, stride, w, h, n int) []byte {
	for y := range h {
		src, dst := pix[y*stride:y*stride+2*n*w], pix[y*n*w:y*n*w+n*w]
		for i := range dst {
			// Each 8-bit sample is at or before the 16-bit sample it comes from, so it never overwrites one to come.
			v := int(src[2*i])<<8 | int(src[2*i+1])
			dst[i] = byte((v + 128) / 257)
		}
	}
	return pix[:n*w*h]
}
