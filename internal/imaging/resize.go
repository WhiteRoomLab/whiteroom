package imaging

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

// The resampler is a separable Lanczos-3 filter in fixed point: the weights of each output pixel are computed in
// float64 once and rounded to integers that sum to exactly weightOne, so that the inner loops are integer arithmetic,
// which gives the same pixels everywhere.

const (
	weightBits = 14
	weightOne  = 1 << weightBits
)

// taps are the weights of the filter on one axis: output pixel i is the sum of the count[i] source pixels from
// start[i], weighted by w[i*n:], in units of 1/weightOne.
type taps struct {
	start, count []int
	n            int
	w            []int32
}

// lanczosTaps returns the taps from a source of srcLen pixels to dstLen pixels that show the part of span pixels from
// off. The filter is widened by the scale when downscaling; its taps are cut at the edges of the source, not of the
// part, and the rest are weighted again to sum to 1.
func lanczosTaps(srcLen, dstLen int, off, span float64) taps {
	ratio := span / float64(dstLen)
	scale := max(1, ratio)
	support := 3 * scale
	t := taps{start: make([]int, dstLen), count: make([]int, dstLen), n: int(math.Ceil(2*support)) + 1}
	t.w = make([]int32, dstLen*t.n)
	f := make([]float64, t.n)
	for i := range dstLen {
		// The explicit conversion keeps the product from being fused with the sum, which some platforms do.
		center := off + float64((float64(i)+0.5)*ratio)
		lo := max(0, int(math.Ceil(center-support-0.5)))
		hi := min(srcLen-1, int(math.Floor(center+support-0.5)))
		k := hi - lo + 1
		sum := 0.0
		for j := range k {
			f[j] = lanczos3((float64(lo+j) + 0.5 - center) / scale)
			sum += f[j]
		}
		w := t.w[i*t.n : i*t.n+k]
		total, largest := int32(0), 0
		for j := range k {
			w[j] = int32(math.Round(f[j] / sum * weightOne))
			total += w[j]
			if w[j] > w[largest] {
				largest = j
			}
		}
		w[largest] += weightOne - total
		t.start[i], t.count[i] = lo, k
	}
	return t
}

func lanczos3(x float64) float64 {
	switch {
	case x == 0:
		return 1
	case x <= -3 || x >= 3:
		return 0
	}
	px := math.Pi * x
	return 3 * math.Sin(px) * math.Sin(px/3) / (px * px)
}

// cropRect returns the largest centered part of an image of sw x sh pixels with the aspect ratio of dw x dh: its
// offset and size on each axis. On an axis where the part is not scaled, it starts at a whole pixel, so that it is
// copied as it is.
func cropRect(sw, sh, dw, dh int) (x0, w, y0, h float64) {
	if int64(sw)*int64(dh) >= int64(sh)*int64(dw) { // the height limits the part
		h, w = float64(sh), float64(int64(sh)*int64(dw))/float64(dh)
		x0 = (float64(sw) - w) / 2
		if sh == dh {
			x0 = float64((sw - dw) / 2)
		}
		return x0, w, 0, h
	}
	w, h = float64(sw), float64(int64(sw)*int64(dh))/float64(dw)
	y0 = (float64(sh) - h) / 2
	if sw == dw {
		y0 = float64((sh - dh) / 2)
	}
	return 0, w, y0, h
}

// resize returns the largest centered part of m with the aspect ratio of dw x dh, scaled to dw x dh pixels, which must
// not be larger than m: an *image.Gray for an *image.Gray, an *image.NRGBA for an image with transparency, which is
// filtered with premultiplied alpha, else an *image.RGBA. m is one of the types that decode returns; an *image.RGBA is
// taken as opaque.
func resize(m image.Image, dw, dh int) (image.Image, error) {
	b := m.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if dw < 1 || dh < 1 || dw > sw || dh > sh {
		return nil, fmt.Errorf("cannot scale an image of %dx%d pixels to %dx%d", sw, sh, dw, dh)
	}
	x0, w, y0, h := cropRect(sw, sh, dw, dh)
	r := &resampler{tx: lanczosTaps(sw, dw, x0, w), ty: lanczosTaps(sh, dh, y0, h), dw: dw, dh: dh}
	r.xlo, r.xhi = r.tx.start[0], r.tx.start[dw-1]+r.tx.count[dw-1]
	p0 := b.Min
	n := r.xhi - r.xlo
	var (
		rgb   [3][]byte   // planes of the colors of a row, for hpassRGB
		rgba  [4][]uint16 // planes of the premultiplied samples of a row, for hpassRGBA
		alpha bool        // the rows are read into rgba
	)
	switch m := m.(type) {
	case *image.Gray:
		r.ch = 1
		r.row = func(y int, dst []int32) {
			hpassGray(dst, m.Pix[m.PixOffset(p0.X+r.xlo, p0.Y+y):], &r.tx, r.xlo)
		}
		return r.run(), nil
	case *image.RGBA:
		r.row = func(y int, dst []int32) {
			deinterleave(&rgb, m.Pix[m.PixOffset(p0.X+r.xlo, p0.Y+y):][:4*n])
			hpassRGB(dst, &rgb, &r.tx, r.xlo)
		}
	case *image.NRGBA:
		if m.Opaque() {
			r.row = func(y int, dst []int32) {
				deinterleave(&rgb, m.Pix[m.PixOffset(p0.X+r.xlo, p0.Y+y):][:4*n])
				hpassRGB(dst, &rgb, &r.tx, r.xlo)
			}
			break
		}
		alpha = true
		r.row = func(y int, dst []int32) {
			src := m.Pix[m.PixOffset(p0.X+r.xlo, p0.Y+y):][:4*n]
			pr, pg, pb, pa := rgba[0][:n], rgba[1][:n], rgba[2][:n], rgba[3][:n]
			for i := range pa {
				q := src[4*i : 4*i+4]
				a := uint16(q[3])
				pr[i], pg[i], pb[i], pa[i] = uint16(q[0])*a, uint16(q[1])*a, uint16(q[2])*a, a*255
			}
			hpassRGBA(dst, &rgba, &r.tx, r.xlo)
		}
	case *image.YCbCr:
		var div int // of the horizontal chroma index
		switch m.SubsampleRatio {
		case image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio440:
			div = 1
		case image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420:
			div = 2
		case image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410:
			div = 4
		default:
			return nil, fmt.Errorf("cannot scale an image of %v", m.SubsampleRatio)
		}
		r.row = func(y int, dst []int32) {
			yi, c0 := m.YOffset(p0.X+r.xlo, p0.Y+y), m.COffset(p0.X, p0.Y+y)
			pr, pg, pb := rgb[0][:n], rgb[1][:n], rgb[2][:n]
			for i, yy := range m.Y[yi : yi+n] {
				ci := c0 + (p0.X+r.xlo+i)/div - p0.X/div
				pr[i], pg[i], pb[i] = color.YCbCrToRGB(yy, m.Cb[ci], m.Cr[ci])
			}
			hpassRGB(dst, &rgb, &r.tx, r.xlo)
		}
	case *image.Paletted:
		opaque := true
		var lut [256][4]uint16 // non-premultiplied; opaque black past the palette, as image/png has
		for i := range lut {
			lut[i] = [4]uint16{0, 0, 0, 0xff}
			if i < len(m.Palette) {
				c := color.NRGBAModel.Convert(m.Palette[i]).(color.NRGBA)
				lut[i] = [4]uint16{uint16(c.R), uint16(c.G), uint16(c.B), uint16(c.A)}
				opaque = opaque && c.A == 0xff
			}
		}
		if opaque {
			r.row = func(y int, dst []int32) {
				pr, pg, pb := rgb[0][:n], rgb[1][:n], rgb[2][:n]
				for i, c := range m.Pix[m.PixOffset(p0.X+r.xlo, p0.Y+y):][:n] {
					q := &lut[c]
					pr[i], pg[i], pb[i] = byte(q[0]), byte(q[1]), byte(q[2])
				}
				hpassRGB(dst, &rgb, &r.tx, r.xlo)
			}
			break
		}
		for i := range lut {
			a := lut[i][3]
			lut[i] = [4]uint16{lut[i][0] * a, lut[i][1] * a, lut[i][2] * a, a * 255}
		}
		alpha = true
		r.row = func(y int, dst []int32) {
			pr, pg, pb, pa := rgba[0][:n], rgba[1][:n], rgba[2][:n], rgba[3][:n]
			for i, c := range m.Pix[m.PixOffset(p0.X+r.xlo, p0.Y+y):][:n] {
				q := &lut[c]
				pr[i], pg[i], pb[i], pa[i] = q[0], q[1], q[2], q[3]
			}
			hpassRGBA(dst, &rgba, &r.tx, r.xlo)
		}
	default:
		return nil, fmt.Errorf("cannot scale an image of type %T", m)
	}
	// The readers above fill the planes allocated here.
	r.ch = 3
	if alpha {
		r.ch = 4
		for i := range rgba {
			rgba[i] = make([]uint16, n)
		}
	} else {
		for i := range rgb {
			rgb[i] = make([]byte, n)
		}
	}
	return r.run(), nil
}

// deinterleave writes the colors of RGBA samples to planes.
func deinterleave(planes *[3][]byte, src []byte) {
	pr, pg, pb := planes[0][:len(src)/4], planes[1][:len(src)/4], planes[2][:len(src)/4]
	for i := range pr {
		q := src[4*i : 4*i+3]
		pr[i], pg[i], pb[i] = q[0], q[1], q[2]
	}
}

// resampler filters the rows of the source horizontally as the vertical taps need them, keeping the last of them in a
// ring, and filters the ring vertically into each output row.
type resampler struct {
	tx, ty   taps
	dw, dh   int
	xlo, xhi int                      // the source columns that tx reads
	ch       int                      // samples of a pixel: 1 gray, 3 opaque colors, 4 premultiplied colors and alpha
	row      func(y int, dst []int32) // filters source row y horizontally into dst
}

func (r *resampler) run() image.Image {
	size := 0
	for _, c := range r.ty.count {
		size = max(size, c)
	}
	ring := make([][]int32, size)
	for i := range ring {
		ring[i] = make([]int32, r.ch*r.dw)
	}
	acc := make([]int64, r.ch*r.dw)
	var (
		gray  *image.Gray
		rgba  *image.RGBA
		nrgba *image.NRGBA
	)
	rect := image.Rect(0, 0, r.dw, r.dh)
	switch r.ch {
	case 1:
		gray = image.NewGray(rect)
	case 3:
		rgba = image.NewRGBA(rect)
	default:
		nrgba = image.NewNRGBA(rect)
	}
	next := 0 // the next source row to filter horizontally
	for y := range r.dh {
		lo, k := r.ty.start[y], r.ty.count[y]
		next = max(next, lo)
		for ; next < lo+k; next++ {
			r.row(next, ring[next%size])
		}
		clear(acc)
		for j, w := range r.ty.w[y*r.ty.n : y*r.ty.n+k] {
			src := ring[(lo+j)%size][:len(acc)]
			w := int64(w)
			for x, v := range src {
				acc[x] += w * int64(v)
			}
		}
		switch r.ch {
		case 1:
			dst := gray.Pix[y*gray.Stride:][:r.dw]
			for x, v := range acc {
				dst[x] = clamp8((v + 1<<(2*weightBits-1)) >> (2 * weightBits))
			}
		case 3:
			dst := rgba.Pix[y*rgba.Stride:][:4*r.dw]
			for x := range r.dw {
				a := acc[3*x : 3*x+3]
				d := dst[4*x : 4*x+4]
				d[0] = clamp8((a[0] + 1<<(2*weightBits-1)) >> (2 * weightBits))
				d[1] = clamp8((a[1] + 1<<(2*weightBits-1)) >> (2 * weightBits))
				d[2] = clamp8((a[2] + 1<<(2*weightBits-1)) >> (2 * weightBits))
				d[3] = 0xff
			}
		default:
			unpremultiply(nrgba.Pix[y*nrgba.Stride:][:4*r.dw], acc)
		}
	}
	switch r.ch {
	case 1:
		return gray
	case 3:
		return rgba
	}
	return nrgba
}

// hpassShift is the shift of the sums of the horizontal pass of premultiplied samples, so that they fit in int32.
const hpassShift = 7

// unpremultiply writes the NRGBA pixels of acc, the vertical sums of premultiplied samples (colors times alpha, and
// alpha times 255) in units of 2^hpassShift/weightOne^2.
func unpremultiply(dst []byte, acc []int64) {
	const unit = 255 << (2*weightBits - hpassShift) // of alpha 1
	for x := 0; x < len(dst); x += 4 {
		s := acc[x : x+4]
		a := s[3]
		alpha := int64(0)
		if a > 0 {
			alpha = min((a+unit/2)/unit, 255)
		}
		if alpha == 0 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = 0, 0, 0, 0
			continue
		}
		for c := range 3 {
			v := int64(0)
			if s[c] > 0 {
				v = min((2*255*s[c]+a)/(2*a), 255)
			}
			dst[x+c] = byte(v)
		}
		dst[x+3] = byte(alpha)
	}
}

func clamp8(v int64) byte {
	return byte(min(max(v, 0), 255))
}

// hpassGray filters a row of gray samples that starts at column xlo.
func hpassGray(dst []int32, src []byte, t *taps, xlo int) {
	for x := range dst {
		ws := t.w[x*t.n : x*t.n+t.count[x]]
		p := src[t.start[x]-xlo:][:len(ws)]
		var v int32
		for j, w := range ws {
			v += w * int32(p[j])
		}
		dst[x] = v
	}
}

// hpassRGB filters a row of the planes of red, green and blue that starts at column xlo into RGB sums.
func hpassRGB(dst []int32, planes *[3][]byte, t *taps, xlo int) {
	for x := 0; x < len(dst)/3; x++ {
		ws := t.w[x*t.n : x*t.n+t.count[x]]
		s := t.start[x] - xlo
		pr, pg, pb := planes[0][s:][:len(ws)], planes[1][s:][:len(ws)], planes[2][s:][:len(ws)]
		var r, g, b int32
		for j, w := range ws {
			r += w * int32(pr[j])
			g += w * int32(pg[j])
			b += w * int32(pb[j])
		}
		d := dst[3*x : 3*x+3]
		d[0], d[1], d[2] = r, g, b
	}
}

// hpassRGBA filters a row of the planes of premultiplied samples that starts at column xlo, shifting the sums by
// hpassShift.
func hpassRGBA(dst []int32, planes *[4][]uint16, t *taps, xlo int) {
	const half = 1 << (hpassShift - 1)
	for x := 0; x < len(dst)/4; x++ {
		ws := t.w[x*t.n : x*t.n+t.count[x]]
		s := t.start[x] - xlo
		pr, pg, pb, pa := planes[0][s:][:len(ws)], planes[1][s:][:len(ws)], planes[2][s:][:len(ws)],
			planes[3][s:][:len(ws)]
		var r, g, b, a int64
		for j, w := range ws {
			w := int64(w)
			r += w * int64(pr[j])
			g += w * int64(pg[j])
			b += w * int64(pb[j])
			a += w * int64(pa[j])
		}
		d := dst[4*x : 4*x+4]
		d[0], d[1], d[2], d[3] = int32((r+half)>>hpassShift), int32((g+half)>>hpassShift),
			int32((b+half)>>hpassShift), int32((a+half)>>hpassShift)
	}
}
