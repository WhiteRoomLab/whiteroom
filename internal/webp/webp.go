// Package webp decodes WebP images as RFC 9649 (the container and the lossless format) and RFC 6386 (the lossy format)
// specify.
package webp

//go:generate go run ./gen

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
)

// Decode reads a WebP image as File.Image does.
func Decode(r io.Reader) (image.Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	f, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return f.Image()
}

// DecodeConfig returns the size of the canvas and, from the headers, the color model of Decode's image.
func DecodeConfig(r io.Reader) (image.Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return image.Config{}, err
	}
	f, err := Parse(data)
	if err != nil {
		return image.Config{}, err
	}
	m := color.RGBAModel
	if f.Alpha || f.Animated {
		m = color.NRGBAModel
	}
	return image.Config{ColorModel: m, Width: f.Width, Height: f.Height}, nil
}

// Image decodes the still image as FrameImage does, or the first frame of an animation drawn on a transparent canvas,
// an *image.NRGBA.
func (f *File) Image() (image.Image, error) {
	if !f.Animated {
		return f.FrameImage(0)
	}
	if len(f.Frames) == 0 {
		return nil, errors.New("webp: no frames")
	}
	// A canvas can have up to 2^32-1 pixels, more than an int counts in bytes on 32-bit platforms.
	if int64(f.Width)*int64(f.Height) > math.MaxInt/4 {
		return nil, fmt.Errorf("webp: canvas of %dx%d pixels is too large", f.Width, f.Height)
	}
	fr := &f.Frames[0]
	pix, err := fr.decode()
	if err != nil {
		return nil, err
	}
	img := image.NewNRGBA(image.Rect(0, 0, f.Width, f.Height))
	for y := range fr.Height {
		copy(img.Pix[img.PixOffset(fr.X, fr.Y+y):], pix[4*fr.Width*y:4*fr.Width*(y+1)])
	}
	return img, nil
}

// FrameImage decodes frame i alone, at its own size: an *image.NRGBA if the frame has alpha (an ALPH chunk, or the
// alpha_is_used bit of VP8L) or a pixel that is not opaque, otherwise an *image.RGBA.
func (f *File) FrameImage(i int) (image.Image, error) {
	if i < 0 || i >= len(f.Frames) {
		return nil, fmt.Errorf("webp: no frame %d", i)
	}
	fr := &f.Frames[i]
	pix, err := fr.decode()
	if err != nil {
		return nil, err
	}
	r := image.Rect(0, 0, fr.Width, fr.Height)
	if fr.hasAlpha || fr.lossless && !opaque(pix) {
		return &image.NRGBA{Pix: pix, Stride: 4 * fr.Width, Rect: r}, nil
	}
	return &image.RGBA{Pix: pix, Stride: 4 * fr.Width, Rect: r}, nil
}

// decode returns the non-premultiplied RGBA bytes of the frame.
func (fr *Frame) decode() ([]byte, error) {
	if fr.lossless {
		pix, _, _, err := decodeVP8L(fr.bitstream)
		return pix, err
	}
	m, err := decodeVP8(fr.bitstream)
	if err != nil {
		return nil, err
	}
	var pix []byte
	if fr.alphaData != nil {
		if pix, err = decodeAlpha(fr.alphaData, m.w, m.h); err != nil {
			return nil, err
		}
	} else {
		pix = make([]byte, 4*m.w*m.h)
	}
	m.toRGBA(pix, fr.alphaData == nil)
	return pix, nil
}

func opaque(pix []byte) bool {
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0xff {
			return false
		}
	}
	return true
}
