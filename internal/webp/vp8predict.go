package webp

// reconstruct predicts the macroblock at (mbx, mby) (RFC 6386 section 12), adds the residue of d.coeffs (section 14)
// and stores the pixels in the planes.
func (d *vp8Decoder) reconstruct(mbx, mby, ymode, uvmode int) {
	d.loadEdges(mbx, mby)
	yb := d.ybuf[:]
	if ymode == bPred {
		// The pixels above and to the right of subblocks 7, 11 and 15 are those of subblock 3.
		tr := yb[yorg-ybps+16 : yorg-ybps+20]
		for k := 1; k < 4; k++ {
			copy(yb[yorg+(4*k-1)*ybps+16:], tr)
		}
		for b := range 16 {
			o := yorg + 4*(b>>2)*ybps + 4*(b&3)
			predict4(yb, o, d.bmodes[b])
			d.addResidue(b, yb, o, ybps)
		}
	} else {
		predictBlock(yb, yorg, ybps, 16, ymode, mbx, mby)
		for b := range 16 {
			d.addResidue(b, yb, yorg+4*(b>>2)*ybps+4*(b&3), ybps)
		}
	}
	for p, cb := range [2][]byte{d.ubuf[:], d.vbuf[:]} {
		predictBlock(cb, corg, cbps, 8, uvmode, mbx, mby)
		for b := range 4 {
			d.addResidue(16+4*p+b, cb, corg+4*(b>>1)*cbps+4*(b&1), cbps)
		}
	}

	ys, cs := 16*d.mbw, 8*d.mbw
	for r := range 16 {
		copy(d.y[(16*mby+r)*ys+16*mbx:][:16], yb[yorg+r*ybps:])
	}
	for r := range 8 {
		copy(d.u[(8*mby+r)*cs+8*mbx:][:8], d.ubuf[corg+r*cbps:])
		copy(d.v[(8*mby+r)*cs+8*mbx:][:8], d.vbuf[corg+r*cbps:])
	}
}

// loadEdges puts the pixels above and to the left of the macroblock at (mbx, mby) into the edges of the buffers: from
// the frame before loop filtering, or 127 above it and 129 to the left of it (RFC 6386 section 12.2). The pixel above
// and to the left of the frame is 127.
func (d *vp8Decoder) loadEdges(mbx, mby int) {
	yb, ub, vb := d.ybuf[:], d.ubuf[:], d.vbuf[:]
	// The column to the left is the right column of the macroblock to the left, still in the buffers.
	for r := range 16 {
		o := yorg + r*ybps
		yb[o-1] = 129
		if mbx > 0 {
			yb[o-1] = yb[o+15]
		}
	}
	for r := range 8 {
		o := corg + r*cbps
		ub[o-1], vb[o-1] = 129, 129
		if mbx > 0 {
			ub[o-1], vb[o-1] = ub[o+7], vb[o+7]
		}
	}
	ytop := yb[yorg-ybps-1 : yorg-ybps+20]
	utop := ub[corg-cbps-1 : corg-cbps+8]
	vtop := vb[corg-cbps-1 : corg-cbps+8]
	if mby == 0 {
		fill(ytop, 127)
		fill(utop, 127)
		fill(vtop, 127)
		return
	}
	if mbx == 0 {
		ytop[0], utop[0], vtop[0] = 129, 129, 129
	} else {
		ytop[0], utop[0], vtop[0] = d.topY[16*mbx-1], d.topU[8*mbx-1], d.topV[8*mbx-1]
	}
	copy(ytop[1:], d.topY[16*mbx:])
	copy(utop[1:], d.topU[8*mbx:])
	copy(vtop[1:], d.topV[8*mbx:])
}

func fill(b []byte, v byte) {
	for i := range b {
		b[i] = v
	}
}

// addResidue adds the inverse transform of block blk of d.coeffs to the 4x4 pixels at b[o:].
func (d *vp8Decoder) addResidue(blk int, b []byte, o, stride int) {
	c := (*[16]int16)(d.coeffs[16*blk:])
	if d.ac>>blk&1 != 0 {
		idctAdd(c, b, o, stride)
	} else if c[0] != 0 {
		dcAdd(c[0], b, o, stride)
	}
}

// predictBlock predicts the n x n block at b[o:] with a macroblock mode (RFC 6386 sections 12.2 and 12.3). DC_PRED
// averages only the pixels inside the frame, or is 128 for the macroblock at the top left.
func predictBlock(b []byte, o, stride, n, mode, mbx, mby int) {
	above := b[o-stride : o-stride+n]
	switch mode {
	case dcPred:
		sum, count := 0, 0
		if mby > 0 {
			for _, v := range above {
				sum += int(v)
			}
			count += n
		}
		if mbx > 0 {
			for r := range n {
				sum += int(b[o+r*stride-1])
			}
			count += n
		}
		v := byte(128)
		if count > 0 {
			v = byte((sum + count/2) / count)
		}
		for r := range n {
			fill(b[o+r*stride:o+r*stride+n], v)
		}
	case vPred:
		for r := range n {
			copy(b[o+r*stride:o+r*stride+n], above)
		}
	case hPred:
		for r := range n {
			row := b[o+r*stride-1 : o+r*stride+n]
			fill(row[1:], row[0])
		}
	case tmPred:
		p := int(b[o-stride-1])
		for r := range n {
			row := b[o+r*stride-1 : o+r*stride+n]
			l := int(row[0]) - p
			for c, a := range above {
				row[1+c] = clip8(l + int(a))
			}
		}
	}
}

// predict4 predicts the 4x4 subblock at b[o:], in the luma buffer, with a subblock mode (RFC 6386 section 12.3).
func predict4(b []byte, o int, mode uint8) {
	t := b[o-ybps-1 : o-ybps+8 : o-ybps+8] // P, then A[0] to A[7]
	p, a0, a1, a2, a3 := int(t[0]), int(t[1]), int(t[2]), int(t[3]), int(t[4])
	a4, a5, a6, a7 := int(t[5]), int(t[6]), int(t[7]), int(t[8])
	l0, l1, l2, l3 := int(b[o-1]), int(b[o+ybps-1]), int(b[o+2*ybps-1]), int(b[o+3*ybps-1])
	r0 := b[o : o+4 : o+4]
	r1 := b[o+ybps : o+ybps+4 : o+ybps+4]
	r2 := b[o+2*ybps : o+2*ybps+4 : o+2*ybps+4]
	r3 := b[o+3*ybps : o+3*ybps+4 : o+3*ybps+4]
	set := func(r []byte, v0, v1, v2, v3 int) {
		r[0], r[1], r[2], r[3] = byte(v0), byte(v1), byte(v2), byte(v3)
	}
	switch mode {
	case bDCPred:
		v := (a0 + a1 + a2 + a3 + l0 + l1 + l2 + l3 + 4) >> 3
		for _, r := range [4][]byte{r0, r1, r2, r3} {
			set(r, v, v, v, v)
		}
	case bTMPred:
		for i, r := range [4][]byte{r0, r1, r2, r3} {
			l := [4]int{l0, l1, l2, l3}[i] - p
			r[0], r[1], r[2], r[3] = clip8(l+a0), clip8(l+a1), clip8(l+a2), clip8(l+a3)
		}
	case bVEPred:
		v0, v1, v2, v3 := avg3(p, a0, a1), avg3(a0, a1, a2), avg3(a1, a2, a3), avg3(a2, a3, a4)
		for _, r := range [4][]byte{r0, r1, r2, r3} {
			set(r, v0, v1, v2, v3)
		}
	case bHEPred:
		for i, v := range [4]int{avg3(p, l0, l1), avg3(l0, l1, l2), avg3(l1, l2, l3), avg3(l2, l3, l3)} {
			set([4][]byte{r0, r1, r2, r3}[i], v, v, v, v)
		}
	case bLDPred:
		d0, d1, d2, d3 := avg3(a0, a1, a2), avg3(a1, a2, a3), avg3(a2, a3, a4), avg3(a3, a4, a5)
		d4, d5, d6 := avg3(a4, a5, a6), avg3(a5, a6, a7), avg3(a6, a7, a7)
		set(r0, d0, d1, d2, d3)
		set(r1, d1, d2, d3, d4)
		set(r2, d2, d3, d4, d5)
		set(r3, d3, d4, d5, d6)
	case bRDPred:
		// From the bottom left to the top right of the edge: L[3], L[2], L[1], L[0], P, A[0], A[1], A[2], A[3].
		e0, e1, e2 := avg3(l3, l2, l1), avg3(l2, l1, l0), avg3(l1, l0, p)
		e3, e4, e5, e6 := avg3(l0, p, a0), avg3(p, a0, a1), avg3(a0, a1, a2), avg3(a1, a2, a3)
		set(r0, e3, e4, e5, e6)
		set(r1, e2, e3, e4, e5)
		set(r2, e1, e2, e3, e4)
		set(r3, e0, e1, e2, e3)
	case bVRPred:
		set(r0, avg2(p, a0), avg2(a0, a1), avg2(a1, a2), avg2(a2, a3))
		set(r1, avg3(l0, p, a0), avg3(p, a0, a1), avg3(a0, a1, a2), avg3(a1, a2, a3))
		set(r2, avg3(l1, l0, p), avg2(p, a0), avg2(a0, a1), avg2(a1, a2))
		set(r3, avg3(l2, l1, l0), avg3(l0, p, a0), avg3(p, a0, a1), avg3(a0, a1, a2))
	case bVLPred:
		set(r0, avg2(a0, a1), avg2(a1, a2), avg2(a2, a3), avg2(a3, a4))
		set(r1, avg3(a0, a1, a2), avg3(a1, a2, a3), avg3(a2, a3, a4), avg3(a3, a4, a5))
		set(r2, avg2(a1, a2), avg2(a2, a3), avg2(a3, a4), avg3(a4, a5, a6))
		set(r3, avg3(a1, a2, a3), avg3(a2, a3, a4), avg3(a3, a4, a5), avg3(a5, a6, a7))
	case bHDPred:
		set(r0, avg2(l0, p), avg3(l0, p, a0), avg3(p, a0, a1), avg3(a0, a1, a2))
		set(r1, avg2(l1, l0), avg3(l1, l0, p), avg2(l0, p), avg3(l0, p, a0))
		set(r2, avg2(l2, l1), avg3(l2, l1, l0), avg2(l1, l0), avg3(l1, l0, p))
		set(r3, avg2(l3, l2), avg3(l3, l2, l1), avg2(l2, l1), avg3(l2, l1, l0))
	case bHUPred:
		set(r0, avg2(l0, l1), avg3(l0, l1, l2), avg2(l1, l2), avg3(l1, l2, l3))
		set(r1, avg2(l1, l2), avg3(l1, l2, l3), avg2(l2, l3), avg3(l2, l3, l3))
		set(r2, avg2(l2, l3), avg3(l2, l3, l3), l3, l3)
		set(r3, l3, l3, l3, l3)
	}
}

func avg2(x, y int) int    { return (x + y + 1) >> 1 }
func avg3(x, y, z int) int { return (x + 2*y + z + 2) >> 2 }
