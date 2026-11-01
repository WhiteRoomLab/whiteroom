package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

// encode returns m in a file of format, JPEG of quality jpegQuality or PNG of the best compression, with profile in it
// unless it is nil. The standard library writes nothing but the image: no metadata.
func encode(m image.Image, format string, profile []byte) ([]byte, error) {
	var b bytes.Buffer
	switch format {
	case mediafile.JPEG:
		m, err := jpegImage(m)
		if err != nil {
			return nil, err
		}
		if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return nil, err
		}
	case mediafile.PNG:
		if ycc, ok := m.(*image.YCbCr); ok {
			// image/png would write 16 bits per sample.
			r := ycc.Bounds()
			m = ycbcrToRGBA(ycc, r.Dx(), r.Dy(), orientationOf(1, r.Dx(), r.Dy()))
		}
		if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&b, m); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("cannot write %s files", format)
	}
	switch {
	case profile == nil:
		return b.Bytes(), nil
	case format == mediafile.JPEG:
		return embedJPEGICC(b.Bytes(), profile)
	}
	return embedPNGICC(b.Bytes(), profile)
}

// jpegImage returns m as an image that image/jpeg encodes without converting each pixel through color.Color: an
// *image.YCbCr, an *image.Gray or an *image.RGBA. An image with transparency is an error.
func jpegImage(m image.Image) (image.Image, error) {
	switch m := m.(type) {
	case *image.YCbCr, *image.Gray, *image.RGBA:
		return m, nil
	case *image.NRGBA:
		if m.Opaque() {
			return &image.RGBA{Pix: m.Pix, Stride: m.Stride, Rect: m.Rect}, nil
		}
	case *image.Paletted:
		if m.Opaque() {
			out := image.NewRGBA(m.Rect)
			for y := m.Rect.Min.Y; y < m.Rect.Max.Y; y++ {
				for x := m.Rect.Min.X; x < m.Rect.Max.X; x++ {
					out.Set(x, y, m.At(x, y))
				}
			}
			return out, nil
		}
	default:
		return nil, fmt.Errorf("cannot write an image of type %T as JPEG", m)
	}
	return nil, errors.New("cannot write an image with transparency as JPEG")
}
