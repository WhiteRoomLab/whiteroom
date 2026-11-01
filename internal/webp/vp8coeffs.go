package webp

// readCoefficients reads the coefficient tokens of a macroblock (RFC 6386 section 13) into d.coeffs, dequantized, and
// updates the contexts top and left. With y2, the macroblock has a Y2 block, whose inverse WHT gives the DC of the Y
// blocks. It reports whether a block has a non-zero DC or a coefficient after it, as libwebp decides which
// macroblocks have their inner edges filtered.
func (d *vp8Decoder) readCoefficients(br *boolDecoder, top, left *nzFlags, y2 bool, q *quantFactors) bool {
	first := 0
	yprobs := &d.probs[3]
	if y2 {
		var dc [16]int16
		n := br.readBlock(&d.probs[1], int(top.y2+left.y2), q.y2, 0, &dc, d.stats)
		f := uint8(btoi(n > 0))
		top.y2, left.y2 = f, f
		if n > 0 {
			inverseWHT(&dc, &d.coeffs)
		}
		first = 1
		yprobs = &d.probs[0]
	}

	nonZero := false
	for j := range 4 {
		l := left.y[j]
		for i := range 4 {
			b := 4*j + i
			out := (*[16]int16)(d.coeffs[16*b:])
			n := br.readBlock(yprobs, int(top.y[i]+l), q.y1, first, out, d.stats)
			l = uint8(btoi(n > first))
			top.y[i] = l
			if n > 1 {
				d.ac |= 1 << b
			}
			nonZero = nonZero || n > 1 || out[0] != 0
		}
		left.y[j] = l
	}

	for p, b := range [2]int{16, 20} {
		tnz, lnz := &top.u, &left.u
		if p == 1 {
			tnz, lnz = &top.v, &left.v
		}
		for j := range 2 {
			l := lnz[j]
			for i := range 2 {
				out := (*[16]int16)(d.coeffs[16*(b+2*j+i):])
				n := br.readBlock(&d.probs[2], int(tnz[i]+l), q.uv, 0, out, d.stats)
				l = uint8(btoi(n > 0))
				tnz[i] = l
				if n > 1 {
					d.ac |= 1 << (b + 2*j + i)
				}
				nonZero = nonZero || n > 1 || out[0] != 0
			}
			lnz[j] = l
		}
	}
	return nonZero
}

// readBlock reads the tokens of a block from position first, with the probabilities of its type and the context ctx
// from its neighbors, and writes the coefficients times the factors q (DC and AC) into out, in raster order. It
// returns the position of dct_eob, or 16.
func (br *boolDecoder) readBlock(p *[numBands][numCtx][numProbs]uint8, ctx int, q [2]int32, first int, out *[16]int16,
	st *vp8Stats) int {
	for n := first; n < 16; n++ {
		probs := &p[coeffBands[n]][ctx]
		if !br.readBool(probs[0]) {
			return n
		}
		for !br.readBool(probs[1]) {
			if st != nil {
				st.tokens[10]++
			}
			if n++; n == 16 {
				return 16
			}
			probs = &p[coeffBands[n]][0]
		}
		var v int32
		if !br.readBool(probs[2]) {
			v, ctx = 1, 1
		} else {
			v, ctx = br.readLarge(probs), 2
		}
		if st != nil {
			st.tokens[tokenIndex(v)]++
		}
		if br.readFlag() {
			v = -v
		}
		// The product is kept in 16 bits, as in the reference decoder and libwebp.
		out[zigzag[n]] = int16(v * q[min(n, 1)])
	}
	return 16
}

// readLarge reads the rest of a token whose value is at least 2, and the extra bits of its category.
func (br *boolDecoder) readLarge(p *[numProbs]uint8) int32 {
	if !br.readBool(p[3]) {
		if !br.readBool(p[4]) {
			return 2
		}
		if !br.readBool(p[5]) {
			return 3
		}
		return 4
	}
	if !br.readBool(p[6]) {
		if !br.readBool(p[7]) {
			return 5 + int32(btoi(br.readBool(159)))
		}
		v := 7 + 2*int32(btoi(br.readBool(165)))
		return v + int32(btoi(br.readBool(145)))
	}
	var cat int
	if !br.readBool(p[8]) {
		cat = btoi(br.readBool(p[9]))
	} else {
		cat = 2 + btoi(br.readBool(p[10]))
	}
	v := int32(0)
	for _, prob := range catProbs[cat] {
		v = 2*v + int32(btoi(br.readBool(prob)))
	}
	return v + [4]int32{11, 19, 35, 67}[cat]
}

// tokenIndex returns the index in vp8Stats.tokens of the token of value v.
func tokenIndex(v int32) int {
	switch {
	case v <= 4:
		return int(v) - 1
	case v <= 6:
		return 4
	case v <= 10:
		return 5
	case v <= 18:
		return 6
	case v <= 34:
		return 7
	case v <= 66:
		return 8
	}
	return 9
}
