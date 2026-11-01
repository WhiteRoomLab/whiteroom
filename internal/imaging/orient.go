package imaging

import (
	"image"
	"image/color"
)

// orientation maps the pixel (x, y) of a w x h image as displayed with an EXIF orientation to the pixel
// (ax*x + bx*y + cx, ay*x + by*y + cy) of the image as stored.
type orientation struct{ ax, bx, cx, ay, by, cy int }

// orientationOf returns the mapping of the EXIF orientation o, from 1 to 8, for an image of w x h pixels as displayed:
// 1 (x, y); 2 (w-1-x, y); 3 (w-1-x, h-1-y); 4 (x, h-1-y); 5 (y, x); 6 (y, w-1-x); 7 (h-1-y, w-1-x); 8 (h-1-y, x).
func orientationOf(o, w, h int) orientation {
	switch o {
	case 2:
		return orientation{ax: -1, cx: w - 1, by: 1}
	case 3:
		return orientation{ax: -1, cx: w - 1, by: -1, cy: h - 1}
	case 4:
		return orientation{ax: 1, by: -1, cy: h - 1}
	case 5:
		return orientation{bx: 1, ay: 1}
	case 6:
		return orientation{bx: 1, ay: -1, cy: w - 1}
	case 7:
		return orientation{bx: -1, cx: h - 1, ay: -1, cy: w - 1}
	case 8:
		return orientation{bx: -1, cx: h - 1, ay: 1}
	}
	return orientation{ax: 1, by: 1}
}

// orient returns m, an image as stored with the EXIF orientation o, as displayed: m itself if o is 1, else a new image
// of the same type, with the same palette, or an *image.RGBA for an *image.YCbCr. m is one of the types that decode
// returns.
func orient(m image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return m
	}
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	if o >= 5 {
		w, h = h, w
	}
	r := image.Rect(0, 0, w, h)
	t := orientationOf(o, w, h)
	switch m := m.(type) {
	case *image.Gray:
		out := image.NewGray(r)
		orientPix(out.Pix, out.Stride, m.Pix[m.PixOffset(b.Min.X, b.Min.Y):], m.Stride, 1, w, h, t)
		return out
	case *image.RGBA:
		out := image.NewRGBA(r)
		orientPix(out.Pix, out.Stride, m.Pix[m.PixOffset(b.Min.X, b.Min.Y):], m.Stride, 4, w, h, t)
		return out
	case *image.NRGBA:
		out := image.NewNRGBA(r)
		orientPix(out.Pix, out.Stride, m.Pix[m.PixOffset(b.Min.X, b.Min.Y):], m.Stride, 4, w, h, t)
		return out
	case *image.Paletted:
		out := image.NewPaletted(r, m.Palette)
		orientPix(out.Pix, out.Stride, m.Pix[m.PixOffset(b.Min.X, b.Min.Y):], m.Stride, 1, w, h, t)
		return out
	case *image.YCbCr:
		return ycbcrToRGBA(m, w, h, t)
	}
	return m
}

// orientPix writes the w x h pixels of bpp bytes of an image as displayed to dst from src, the image as stored, with t.
func orientPix(dst []byte, dstStride int, src []byte, srcStride, bpp, w, h int, t orientation) {
	step := t.ay*srcStride + t.ax*bpp
	for y := range h {
		off := (t.by*y+t.cy)*srcStride + (t.bx*y+t.cx)*bpp
		d := dst[y*dstStride : y*dstStride+w*bpp]
		if bpp == 1 {
			for x := range d {
				d[x] = src[off]
				off += step
			}
			continue
		}
		for x := 0; x < len(d); x += 4 {
			copy(d[x:x+4], src[off:off+4])
			off += step
		}
	}
}

// ycbcrToRGBA returns the w x h pixels of m, as displayed with t, in an *image.RGBA, converted as color.YCbCrToRGB
// does, with the chroma sample of each pixel.
func ycbcrToRGBA(m *image.YCbCr, w, h int, t orientation) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	b := m.Bounds()
	for y := range h {
		d := out.Pix[y*out.Stride : y*out.Stride+4*w]
		sx, sy := b.Min.X+t.bx*y+t.cx, b.Min.Y+t.by*y+t.cy
		for x := 0; x < len(d); x += 4 {
			yi, ci := m.YOffset(sx, sy), m.COffset(sx, sy)
			d[x], d[x+1], d[x+2] = color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			d[x+3] = 0xff
			sx += t.ax
			sy += t.ay
		}
	}
	return out
}
