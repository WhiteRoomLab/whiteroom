package webp

// filterRow applies the loop filter (RFC 6386 section 15) to the macroblocks of row mby, after their reconstruction
// and before that of the row below. Each macroblock filters the edge to its left, the edges between its subblocks
// across, the edge above, and the edges between its subblocks down, in this order.
func (d *vp8Decoder) filterRow(mby int) {
	ys, cs := 16*d.mbw, 8*d.mbw
	for mbx, m := range d.mbs {
		p := &d.fparams[m.segment][m.bpred]
		if p.mbLimit == 0 {
			continue
		}
		yo := 16*mby*ys + 16*mbx
		if d.lf.simple {
			if mbx > 0 {
				simpleEdge(d.y, yo, 1, ys, p.mbLimit)
			}
			if m.inner {
				for x := 4; x < 16; x += 4 {
					simpleEdge(d.y, yo+x, 1, ys, p.subLimit)
				}
			}
			if mby > 0 {
				simpleEdge(d.y, yo, ys, 1, p.mbLimit)
			}
			if m.inner {
				for y := 4; y < 16; y += 4 {
					simpleEdge(d.y, yo+y*ys, ys, 1, p.subLimit)
				}
			}
			continue
		}
		co := 8*mby*cs + 8*mbx
		if mbx > 0 {
			normalEdge(d.y, yo, 1, ys, 16, p, true)
			normalEdge(d.u, co, 1, cs, 8, p, true)
			normalEdge(d.v, co, 1, cs, 8, p, true)
		}
		if m.inner {
			for x := 4; x < 16; x += 4 {
				normalEdge(d.y, yo+x, 1, ys, 16, p, false)
			}
			normalEdge(d.u, co+4, 1, cs, 8, p, false)
			normalEdge(d.v, co+4, 1, cs, 8, p, false)
		}
		if mby > 0 {
			normalEdge(d.y, yo, ys, 1, 16, p, true)
			normalEdge(d.u, co, cs, 1, 8, p, true)
			normalEdge(d.v, co, cs, 1, 8, p, true)
		}
		if m.inner {
			for y := 4; y < 16; y += 4 {
				normalEdge(d.y, yo+y*ys, ys, 1, 16, p, false)
			}
			normalEdge(d.u, co+4*cs, cs, 1, 8, p, false)
			normalEdge(d.v, co+4*cs, cs, 1, 8, p, false)
		}
	}
}

// The filters below work on the segments across an edge: b[i] is the first pixel after the edge in the first segment,
// step is the distance between the pixels of a segment, and next the distance between segments.

// simpleEdge applies the simple filter (RFC 6386 section 15.2) to the 16 segments across a luma edge.
func simpleEdge(b []byte, i, step, next, limit int) {
	for range 16 {
		p1, p0, q0, q1 := int(b[i-2*step]), int(b[i-step]), int(b[i]), int(b[i+step])
		if abs(p0-q0)*2+abs(p1-q1)>>1 <= limit {
			adjust(b, i, step, p1, p0, q0, q1, true)
		}
		i += next
	}
}

// normalEdge applies the normal filter (RFC 6386 section 15.3) to the n segments across an edge between macroblocks,
// or between subblocks.
func normalEdge(b []byte, i, step, next, n int, p *filterParams, mbEdge bool) {
	limit, in, hev := p.subLimit, p.interior, p.hev
	if mbEdge {
		limit = p.mbLimit
	}
	for range n {
		p3, p2, p1, p0 := int(b[i-4*step]), int(b[i-3*step]), int(b[i-2*step]), int(b[i-step])
		q0, q1, q2, q3 := int(b[i]), int(b[i+step]), int(b[i+2*step]), int(b[i+3*step])
		if abs(p0-q0)*2+abs(p1-q1)>>1 <= limit && abs(p3-p2) <= in && abs(p2-p1) <= in && abs(p1-p0) <= in &&
			abs(q3-q2) <= in && abs(q2-q1) <= in && abs(q1-q0) <= in {
			switch {
			case abs(p1-p0) > hev || abs(q1-q0) > hev:
				adjust(b, i, step, p1, p0, q0, q1, true)
			case mbEdge:
				w := clampS8(clampS8(p1-q1) + 3*(q0-p0))
				a := (27*w + 63) >> 7
				b[i-step], b[i] = clip8(p0+a), clip8(q0-a)
				a = (18*w + 63) >> 7
				b[i-2*step], b[i+step] = clip8(p1+a), clip8(q1-a)
				a = (9*w + 63) >> 7
				b[i-3*step], b[i+2*step] = clip8(p2+a), clip8(q2-a)
			default:
				a := (adjust(b, i, step, p1, p0, q0, q1, false) + 1) >> 1
				b[i-2*step], b[i+step] = clip8(p1+a), clip8(q1-a)
			}
		}
		i += next
	}
}

// adjust brings p0 and q0 closer, using p1 and q1 if outer (common_adjust in RFC 6386 section 15.2), and returns what
// it subtracts from q0.
func adjust(b []byte, i, step, p1, p0, q0, q1 int, outer bool) int {
	a := 3 * (q0 - p0)
	if outer {
		a += clampS8(p1 - q1)
	}
	a = clampS8(a)
	f1 := clampS8(a+4) >> 3
	f2 := clampS8(a+3) >> 3
	b[i-step], b[i] = clip8(p0+f2), clip8(q0-f1)
	return f1
}

func clampS8(v int) int {
	return min(max(v, -128), 127)
}
