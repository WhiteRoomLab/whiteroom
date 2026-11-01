package webp

// toRGBA converts the planes into the R, G and B bytes of pix, RGBA bytes of the frame, as libwebp does with its fancy
// upsampler: the chroma of a pixel is interpolated from the four nearest chroma samples. The A bytes are set to 255 if
// opaque, and kept otherwise.
func (m *yuvImage) toRGBA(pix []byte, opaque bool) {
	w, h, cw := m.w, m.h, (m.w+1)/2
	buf := make([]byte, 4*w)
	ut, vt, ub, vb := buf[:w], buf[w:2*w], buf[2*w:3*w], buf[3*w:]
	chroma := func(p []byte, k int) []byte { return p[k*cw : (k+1)*cw] }
	row := func(y int, u, v []byte) {
		yuvRow(pix[4*w*y:4*w*(y+1)], m.y[w*y:w*(y+1)], u, v, opaque)
	}
	// The first row, and the last row of an even height, take their nearest chroma row alone.
	upsamplePair(ut, nil, chroma(m.u, 0), chroma(m.u, 0))
	upsamplePair(vt, nil, chroma(m.v, 0), chroma(m.v, 0))
	row(0, ut, vt)
	for y := 1; y < h; y += 2 {
		k := (y + 1) / 2
		if y == h-1 {
			upsamplePair(ut, nil, chroma(m.u, k-1), chroma(m.u, k-1))
			upsamplePair(vt, nil, chroma(m.v, k-1), chroma(m.v, k-1))
			row(y, ut, vt)
			break
		}
		upsamplePair(ut, ub, chroma(m.u, k-1), chroma(m.u, k))
		upsamplePair(vt, vb, chroma(m.v, k-1), chroma(m.v, k))
		row(y, ut, vt)
		row(y+1, ub, vb)
	}
}

// upsamplePair interpolates the chroma of the two rows of pixels between the chroma rows t and c: top, the row nearer
// to t, and bottom, nearer to c, if not nil. The four nearest samples weigh 9, 3, 3 and 1, rounded as libwebp does,
// and the samples at the ends of the rows repeat.
func upsamplePair(top, bottom, t, c []byte) {
	w := len(top)
	tl, bl := int(t[0]), int(c[0])
	top[0] = byte((3*tl + bl + 2) >> 2)
	if bottom != nil {
		bottom[0] = byte((3*bl + tl + 2) >> 2)
	}
	for k := 1; 2*k < w; k++ {
		tr, br := int(t[k]), int(c[k])
		sum := tl + tr + bl + br + 8
		diag := (sum + 2*(tl+br)) >> 3
		anti := (sum + 2*(tr+bl)) >> 3
		top[2*k-1] = byte((anti + tl) >> 1)
		top[2*k] = byte((diag + tr) >> 1)
		if bottom != nil {
			bottom[2*k-1] = byte((diag + bl) >> 1)
			bottom[2*k] = byte((anti + br) >> 1)
		}
		tl, bl = tr, br
	}
	if w%2 == 0 {
		top[w-1] = byte((3*tl + bl + 2) >> 2)
		if bottom != nil {
			bottom[w-1] = byte((3*bl + tl + 2) >> 2)
		}
	}
}

// yuvRow converts a row of luma samples and their chroma to RGBA bytes with the BT.601 formulas of libwebp, in fixed
// point with 6 fractional bits.
func yuvRow(dst, y, u, v []byte, opaque bool) {
	for x := range y {
		d := dst[4*x : 4*x+4 : 4*x+4]
		l := int(y[x]) * 19077 >> 8
		cb, cr := int(u[x]), int(v[x])
		d[0] = descale(l + cr*26149>>8 - 14234)
		d[1] = descale(l - cb*6419>>8 - cr*13320>>8 + 8708)
		d[2] = descale(l + cb*33050>>8 - 17685)
		if opaque {
			d[3] = 0xff
		}
	}
}

// descale returns v without its 6 fractional bits, clamped to 0..255.
func descale(v int) byte {
	if uint(v) < 256<<6 {
		return byte(v >> 6)
	}
	if v < 0 {
		return 0
	}
	return 0xff
}
