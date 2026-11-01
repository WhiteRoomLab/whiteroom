package webp

import (
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBoolDecoder(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	for round := range 60 {
		n := []int{0, 1, 2, 7, 8, 9, 100, 1000, 20000}[round%9]
		e := newBoolEncoder()
		probs := make([]uint8, n)
		bits := make([]bool, n)
		for i := range n {
			// The extremes often, and bits mostly as likely as the probabilities say, so that the range takes all its
			// values.
			switch rng.IntN(4) {
			case 0:
				probs[i] = uint8(rng.IntN(3))
			case 1:
				probs[i] = uint8(253 + rng.IntN(3))
			default:
				probs[i] = uint8(rng.Uint32())
			}
			bits[i] = rng.IntN(256) >= int(probs[i])
			if rng.IntN(8) == 0 {
				bits[i] = !bits[i]
			}
			e.putBool(probs[i], bits[i])
		}
		var d boolDecoder
		d.init(e.bytes())
		for i := range n {
			if got := d.readBool(probs[i]); got != bits[i] {
				t.Fatalf("round %d: bool %d of %d at probability %d is %t, want %t", round, i, n, probs[i], got, bits[i])
			}
		}
		if d.eof {
			t.Errorf("round %d: end of data before the end of %d bools", round, n)
		}
		for range 64 {
			d.readBool(128)
		}
		if !d.eof {
			t.Errorf("round %d: no end of data after reading past it", round)
		}
	}

	var d boolDecoder
	if d.init(nil); !d.eof {
		t.Error("an empty partition is not at its end")
	}
}

func TestBoolDecoderValues(t *testing.T) {
	rng := rand.New(rand.NewPCG(23, 24))
	type value struct{ kind, n, v int }
	var vs []value
	e := newBoolEncoder()
	trees := []struct {
		tree  []int8
		probs []uint8
	}{
		{segmentTree[:], []uint8{1, 128, 255}},
		{kfYModeTree[:], kfYModeProbs[:]},
		{bModeTree[:], kfBModeProbs[3][7][:]},
		{uvModeTree[:], kfUVModeProbs[:]},
	}
	for range 2000 {
		v := value{kind: rng.IntN(4), n: 1 + rng.IntN(16)}
		switch v.kind {
		case 0:
			v.v = rng.IntN(1 << v.n)
			e.putLiteral(v.v, v.n)
		case 1:
			v.v = rng.IntN(1<<v.n) * (1 - 2*rng.IntN(2))
			e.putSigned(v.v, v.n)
		case 2:
			v.v = rng.IntN(1<<v.n) * rng.IntN(2) * (1 - 2*rng.IntN(2))
			e.putOptionalSigned(v.v, v.n)
		case 3:
			v.n = rng.IntN(len(trees))
			v.v = rng.IntN(len(trees[v.n].tree)/2 + 1)
			e.putTree(trees[v.n].tree, trees[v.n].probs, v.v)
		}
		vs = append(vs, v)
	}
	var d boolDecoder
	d.init(e.bytes())
	for i, v := range vs {
		var got int
		switch v.kind {
		case 0:
			got = d.readLiteral(v.n)
		case 1:
			got = d.readSigned(v.n)
		case 2:
			got = d.readOptionalSigned(v.n)
		case 3:
			got = d.readTree(trees[v.n].tree, trees[v.n].probs)
		}
		if got != v.v {
			t.Fatalf("value %d, %+v: got %d", i, v, got)
		}
	}
}

// refIDCT is the inverse DCT of RFC 6386 section 14.4, with its intermediate values in 16 bits, added to the
// prediction and clamped as in section 14.5.
func refIDCT(input [16]int16, predict [16]int) [16]int {
	const cospi8sqrt2minus1, sinpi8sqrt2 = 20091, 35468
	var output [16]int16
	for i := range 4 {
		ip := input[i:]
		a1 := int(ip[0]) + int(ip[8])
		b1 := int(ip[0]) - int(ip[8])
		temp1 := (int(ip[4]) * sinpi8sqrt2) >> 16
		temp2 := int(ip[12]) + ((int(ip[12]) * cospi8sqrt2minus1) >> 16)
		c1 := temp1 - temp2
		temp1 = int(ip[4]) + ((int(ip[4]) * cospi8sqrt2minus1) >> 16)
		temp2 = (int(ip[12]) * sinpi8sqrt2) >> 16
		d1 := temp1 + temp2
		output[i] = int16(a1 + d1)
		output[12+i] = int16(a1 - d1)
		output[4+i] = int16(b1 + c1)
		output[8+i] = int16(b1 - c1)
	}
	var out [16]int
	for i := range 4 {
		ip := output[4*i:]
		a1 := int(ip[0]) + int(ip[2])
		b1 := int(ip[0]) - int(ip[2])
		temp1 := (int(ip[1]) * sinpi8sqrt2) >> 16
		temp2 := int(ip[3]) + ((int(ip[3]) * cospi8sqrt2minus1) >> 16)
		c1 := temp1 - temp2
		temp1 = int(ip[1]) + ((int(ip[1]) * cospi8sqrt2minus1) >> 16)
		temp2 = (int(ip[3]) * sinpi8sqrt2) >> 16
		d1 := temp1 + temp2
		for j, v := range [4]int{(a1 + d1 + 4) >> 3, (b1 + c1 + 4) >> 3, (b1 - c1 + 4) >> 3, (a1 - d1 + 4) >> 3} {
			out[4*i+j] = refClamp(predict[4*i+j] + v)
		}
	}
	return out
}

// refWHT is the inverse WHT of RFC 6386 section 14.3.
func refWHT(input [16]int16) [16]int16 {
	var output [16]int16
	for i := range 4 {
		ip := input[i:]
		a1 := int(ip[0]) + int(ip[12])
		b1 := int(ip[4]) + int(ip[8])
		c1 := int(ip[4]) - int(ip[8])
		d1 := int(ip[0]) - int(ip[12])
		output[i] = int16(a1 + b1)
		output[4+i] = int16(c1 + d1)
		output[8+i] = int16(a1 - b1)
		output[12+i] = int16(d1 - c1)
	}
	for i := range 4 {
		ip := output[4*i : 4*i+4]
		a1 := int(ip[0]) + int(ip[3])
		b1 := int(ip[1]) + int(ip[2])
		c1 := int(ip[1]) - int(ip[2])
		d1 := int(ip[0]) - int(ip[3])
		a2, b2, c2, d2 := a1+b1, c1+d1, a1-b1, d1-c1
		ip[0], ip[1], ip[2], ip[3] = int16((a2+3)>>3), int16((b2+3)>>3), int16((c2+3)>>3), int16((d2+3)>>3)
	}
	return output
}

// randomCoeffs returns a block of dequantized coefficients in the range of real streams, often sparse.
func randomCoeffs(rng *rand.Rand) [16]int16 {
	var c [16]int16
	n := []int{1, 2, 3, 16}[rng.IntN(4)]
	for i := range n {
		limit := []int{8, 100, 2048}[rng.IntN(3)]
		c[zigzag[i]] = int16(rng.IntN(2*limit+1) - limit)
	}
	return c
}

func TestInverseTransforms(t *testing.T) {
	rng := rand.New(rand.NewPCG(25, 26))
	for i := range 20000 {
		in := randomCoeffs(rng)
		if i%4 == 0 {
			in = [16]int16{in[0]}
		}
		// The prediction, 4x4 pixels in rows 4 apart.
		var pred [16]int
		got := make([]byte, 16)
		for k := range pred {
			pred[k] = []int{0, 255, rng.IntN(256)}[rng.IntN(3)]
			got[k] = byte(pred[k])
		}
		dc := slices.Clone(got)
		want := refIDCT(in, pred)
		idctAdd(&in, got, 0, 4)
		dcAdd(in[0], dc, 0, 4)
		for k := range 16 {
			if int(got[k]) != want[k] {
				t.Fatalf("idctAdd(%v) on %v: pixel %d is %d, want %d", in, pred, k, got[k], want[k])
			}
			if in == [16]int16{in[0]} && int(dc[k]) != want[k] {
				t.Fatalf("dcAdd(%d) on %v: pixel %d is %d, want %d", in[0], pred, k, dc[k], want[k])
			}
		}

		var coeffs [25 * 16]int16
		inverseWHT(&in, &coeffs)
		for k, w := range refWHT(in) {
			if coeffs[16*k] != w {
				t.Fatalf("inverseWHT(%v): DC %d is %d, want %d", in, k, coeffs[16*k], w)
			}
		}
	}
}

// refPredict4 is subblock_intra_predict of RFC 6386 section 12.3, with A[-1] and L[-1] P, A the 8 pixels above and L
// the 4 to the left.
func refPredict4(mode int, p int, a [8]int, l [4]int) [4][4]int {
	avg3 := func(x, y, z int) int { return (x + y + y + z + 2) >> 2 }
	avg2 := func(x, y int) int { return (x + y + 1) >> 1 }
	A := func(i int) int {
		if i < 0 {
			return p
		}
		return a[i]
	}
	L := func(i int) int {
		if i < 0 {
			return p
		}
		return l[i]
	}
	E := [9]int{l[3], l[2], l[1], l[0], p, a[0], a[1], a[2], a[3]}
	avg3p := func(f func(int) int, i int) int { return avg3(f(i-1), f(i), f(i+1)) }
	e := func(i int) int { return E[i] }
	var B [4][4]int
	switch mode {
	case bDCPred:
		v := 4
		for i := range 4 {
			v += a[i] + l[i]
		}
		for i := range 4 {
			for j := range 4 {
				B[i][j] = v >> 3
			}
		}
	case bTMPred:
		for r := range 4 {
			for c := range 4 {
				B[r][c] = refClamp(l[r] + a[c] - p)
			}
		}
	case bVEPred:
		for c := range 4 {
			v := avg3p(A, c)
			B[0][c], B[1][c], B[2][c], B[3][c] = v, v, v, v
		}
	case bHEPred:
		v := avg3(l[2], l[3], l[3])
		for r := 3; ; {
			B[r][0], B[r][1], B[r][2], B[r][3] = v, v, v, v
			if r--; r < 0 {
				break
			}
			v = avg3p(L, r)
		}
	case bLDPred:
		B[0][0] = avg3p(A, 1)
		B[0][1], B[1][0] = avg3p(A, 2), avg3p(A, 2)
		B[0][2], B[1][1], B[2][0] = avg3p(A, 3), avg3p(A, 3), avg3p(A, 3)
		B[0][3], B[1][2], B[2][1], B[3][0] = avg3p(A, 4), avg3p(A, 4), avg3p(A, 4), avg3p(A, 4)
		B[1][3], B[2][2], B[3][1] = avg3p(A, 5), avg3p(A, 5), avg3p(A, 5)
		B[2][3], B[3][2] = avg3p(A, 6), avg3p(A, 6)
		B[3][3] = avg3(a[6], a[7], a[7])
	case bRDPred:
		B[3][0] = avg3p(e, 1)
		B[3][1], B[2][0] = avg3p(e, 2), avg3p(e, 2)
		B[3][2], B[2][1], B[1][0] = avg3p(e, 3), avg3p(e, 3), avg3p(e, 3)
		B[3][3], B[2][2], B[1][1], B[0][0] = avg3p(e, 4), avg3p(e, 4), avg3p(e, 4), avg3p(e, 4)
		B[2][3], B[1][2], B[0][1] = avg3p(e, 5), avg3p(e, 5), avg3p(e, 5)
		B[1][3], B[0][2] = avg3p(e, 6), avg3p(e, 6)
		B[0][3] = avg3p(e, 7)
	case bVRPred:
		B[3][0] = avg3p(e, 2)
		B[2][0] = avg3p(e, 3)
		B[3][1], B[1][0] = avg3p(e, 4), avg3p(e, 4)
		B[2][1], B[0][0] = avg2(E[4], E[5]), avg2(E[4], E[5])
		B[3][2], B[1][1] = avg3p(e, 5), avg3p(e, 5)
		B[2][2], B[0][1] = avg2(E[5], E[6]), avg2(E[5], E[6])
		B[3][3], B[1][2] = avg3p(e, 6), avg3p(e, 6)
		B[2][3], B[0][2] = avg2(E[6], E[7]), avg2(E[6], E[7])
		B[1][3] = avg3p(e, 7)
		B[0][3] = avg2(E[7], E[8])
	case bVLPred:
		B[0][0] = avg2(a[0], a[1])
		B[1][0] = avg3p(A, 1)
		B[2][0], B[0][1] = avg2(a[1], a[2]), avg2(a[1], a[2])
		B[1][1], B[3][0] = avg3p(A, 2), avg3p(A, 2)
		B[2][1], B[0][2] = avg2(a[2], a[3]), avg2(a[2], a[3])
		B[3][1], B[1][2] = avg3p(A, 3), avg3p(A, 3)
		B[2][2], B[0][3] = avg2(a[3], a[4]), avg2(a[3], a[4])
		B[3][2], B[1][3] = avg3p(A, 4), avg3p(A, 4)
		B[2][3] = avg3p(A, 5)
		B[3][3] = avg3p(A, 6)
	case bHDPred:
		B[3][0] = avg2(E[0], E[1])
		B[3][1] = avg3p(e, 1)
		B[2][0], B[3][2] = avg2(E[1], E[2]), avg2(E[1], E[2])
		B[2][1], B[3][3] = avg3p(e, 2), avg3p(e, 2)
		B[2][2], B[1][0] = avg2(E[2], E[3]), avg2(E[2], E[3])
		B[2][3], B[1][1] = avg3p(e, 3), avg3p(e, 3)
		B[1][2], B[0][0] = avg2(E[3], E[4]), avg2(E[3], E[4])
		B[1][3], B[0][1] = avg3p(e, 4), avg3p(e, 4)
		B[0][2] = avg3p(e, 5)
		B[0][3] = avg3p(e, 6)
	case bHUPred:
		B[0][0] = avg2(l[0], l[1])
		B[0][1] = avg3p(L, 1)
		B[0][2], B[1][0] = avg2(l[1], l[2]), avg2(l[1], l[2])
		B[0][3], B[1][1] = avg3p(L, 2), avg3p(L, 2)
		B[1][2], B[2][0] = avg2(l[2], l[3]), avg2(l[2], l[3])
		B[1][3], B[2][1] = avg3(l[2], l[3], l[3]), avg3(l[2], l[3], l[3])
		B[2][2], B[2][3], B[3][0], B[3][1], B[3][2], B[3][3] = l[3], l[3], l[3], l[3], l[3], l[3]
	}
	return B
}

func TestPredict4(t *testing.T) {
	rng := rand.New(rand.NewPCG(27, 28))
	for range 3000 {
		pix := func() int { return []int{0, 255, rng.IntN(256)}[rng.IntN(3)] }
		var a [8]int
		var l [4]int
		p := pix()
		for i := range a {
			a[i] = pix()
		}
		for i := range l {
			l[i] = pix()
		}
		o := yorg + 4*ybps + 8 // subblock 6
		for mode := range numBModes {
			var buf [17 * ybps]byte
			buf[o-ybps-1] = byte(p)
			for i, v := range a {
				buf[o-ybps+i] = byte(v)
			}
			for i, v := range l {
				buf[o+i*ybps-1] = byte(v)
			}
			predict4(buf[:], o, uint8(mode))
			want := refPredict4(mode, p, a, l)
			for r := range 4 {
				for c := range 4 {
					if got := int(buf[o+r*ybps+c]); got != want[r][c] {
						t.Fatalf("mode %d, P %d, A %v, L %v: (%d, %d) is %d, want %d", mode, p, a, l, r, c, got,
							want[r][c])
					}
				}
			}
		}
	}
}

// The loop filters of RFC 6386 sections 15.2 and 15.3, on a segment p3 p2 p1 p0 | q0 q1 q2 q3, in 8-bit signed
// arithmetic.
func refC(v int) int   { return min(max(v, -128), 127) }
func refU2S(v int) int { return v - 128 }
func refS2U(v int) int { return refC(v) + 128 }

func refCommonAdjust(useOuterTaps bool, s *[8]int) int {
	p1, p0, q0, q1 := refU2S(s[2]), refU2S(s[3]), refU2S(s[4]), refU2S(s[5])
	a := 3 * (q0 - p0)
	if useOuterTaps {
		a += refC(p1 - q1)
	}
	a = refC(a)
	b := refC(a+3) >> 3
	a = refC(a+4) >> 3
	s[4] = refS2U(q0 - a)
	s[3] = refS2U(p0 + b)
	return a
}

func refSimpleSegment(edgeLimit int, s *[8]int) {
	if abs(s[3]-s[4])*2+abs(s[2]-s[5])/2 <= edgeLimit {
		refCommonAdjust(true, s)
	}
}

func refFilterYes(I, E int, s *[8]int) bool {
	p3, p2, p1, p0 := refU2S(s[0]), refU2S(s[1]), refU2S(s[2]), refU2S(s[3])
	q0, q1, q2, q3 := refU2S(s[4]), refU2S(s[5]), refU2S(s[6]), refU2S(s[7])
	return abs(p0-q0)*2+abs(p1-q1)/2 <= E && abs(p3-p2) <= I && abs(p2-p1) <= I && abs(p1-p0) <= I &&
		abs(q3-q2) <= I && abs(q2-q1) <= I && abs(q1-q0) <= I
}

func refHEV(threshold int, s *[8]int) bool {
	return abs(s[2]-s[3]) > threshold || abs(s[5]-s[4]) > threshold
}

func refSubblockFilter(hevThreshold, interiorLimit, edgeLimit int, s *[8]int) {
	if refFilterYes(interiorLimit, edgeLimit, s) {
		p1, q1 := refU2S(s[2]), refU2S(s[5])
		hv := refHEV(hevThreshold, s)
		a := (refCommonAdjust(hv, s) + 1) >> 1
		if !hv {
			s[5] = refS2U(q1 - a)
			s[2] = refS2U(p1 + a)
		}
	}
}

func refMBFilter(hevThreshold, interiorLimit, edgeLimit int, s *[8]int) {
	if refFilterYes(interiorLimit, edgeLimit, s) {
		if !refHEV(hevThreshold, s) {
			p2, p1, p0 := refU2S(s[1]), refU2S(s[2]), refU2S(s[3])
			q0, q1, q2 := refU2S(s[4]), refU2S(s[5]), refU2S(s[6])
			w := refC(refC(p1-q1) + 3*(q0-p0))
			a := refC((27*w + 63) >> 7)
			s[4], s[3] = refS2U(q0-a), refS2U(p0+a)
			a = refC((18*w + 63) >> 7)
			s[5], s[2] = refS2U(q1-a), refS2U(p1+a)
			a = refC((9*w + 63) >> 7)
			s[6], s[1] = refS2U(q2-a), refS2U(p2+a)
		} else {
			refCommonAdjust(true, s)
		}
	}
}

func TestLoopFilterSegments(t *testing.T) {
	rng := rand.New(rand.NewPCG(29, 30))
	for i := range 200000 {
		var s [8]int
		base := rng.IntN(256)
		spread := []int{2, 8, 40, 256}[rng.IntN(4)]
		for k := range s {
			s[k] = min(max(base+rng.IntN(2*spread+1)-spread, 0), 255)
			if rng.IntN(16) == 0 {
				s[k] = 255 * rng.IntN(2)
			}
		}
		if i%3 == 0 {
			// A step at the edge.
			for k := 4; k < 8; k++ {
				s[k] = min(max(255-s[k]+rng.IntN(5)-2, 0), 255)
			}
		}
		level, sharpness := 1+rng.IntN(63), rng.IntN(8)
		d := vp8Decoder{lf: filterHeader{level: level, sharpness: sharpness}}
		d.initFilter()
		p := &d.fparams[0][0]
		// One segment across a vertical edge: step 1, at 4 in b.
		for kind := range 3 {
			b := make([]byte, 8)
			for k, v := range s {
				b[k] = byte(v)
			}
			want := s
			switch kind {
			case 0:
				simpleEdgeN(b, 4, p.mbLimit)
				refSimpleSegment(p.mbLimit, &want)
			case 1:
				normalEdge(b, 4, 1, 0, 1, p, false)
				refSubblockFilter(p.hev, p.interior, p.subLimit, &want)
			case 2:
				normalEdge(b, 4, 1, 0, 1, p, true)
				refMBFilter(p.hev, p.interior, p.mbLimit, &want)
			}
			for k := range b {
				if int(b[k]) != want[k] {
					t.Fatalf("filter %d, level %d, sharpness %d, segment %v: %v, want %v", kind, level, sharpness, s,
						b, want)
				}
			}
		}
	}
}

// simpleEdgeN applies simpleEdge to one segment.
func simpleEdgeN(b []byte, i, limit int) {
	// simpleEdge filters 16 segments: give it 16 copies, 8 apart, and keep the first.
	big := make([]byte, 16*8)
	for k := range 16 {
		copy(big[8*k:], b)
	}
	simpleEdge(big, i, 1, 8, limit)
	copy(b, big[:8])
}

// TestFilterParams checks the limits of the loop filter (RFC 6386 section 15.4) at every level and sharpness.
func TestFilterParams(t *testing.T) {
	for level := 1; level < 64; level++ {
		for sharpness := range 8 {
			d := vp8Decoder{lf: filterHeader{level: level, sharpness: sharpness}}
			d.initFilter()
			interior := level
			if sharpness > 0 {
				if sharpness > 4 {
					interior >>= 2
				} else {
					interior >>= 1
				}
				if interior > 9-sharpness {
					interior = 9 - sharpness
				}
			}
			if interior == 0 {
				interior = 1
			}
			hev := 0
			if level >= 40 {
				hev = 2
			} else if level >= 15 {
				hev = 1
			}
			want := filterParams{mbLimit: (level+2)*2 + interior, subLimit: level*2 + interior, interior: interior,
				hev: hev}
			for i := range d.fparams {
				for _, p := range d.fparams[i] {
					if p != want {
						t.Fatalf("level %d, sharpness %d: %+v, want %+v", level, sharpness, p, want)
					}
				}
			}
		}
	}
}

// TestVP8Header decodes hand-made frame headers and checks what the decoder makes of them. The factors are those of
// the tables of RFC 6386 section 14.1 at the indices of section 9.6, clamped to 0 to 127; as in libwebp and the
// reference decoder, the index of a segment is not clamped before the deltas are added. libwebp does not clamp the
// filter level of a segment before the loop filter deltas either.
func TestVP8Header(t *testing.T) {
	type factors = [3][2]int32 // y1, y2 and uv
	for _, tt := range []struct {
		name   string
		f      testVP8
		quant  [4]factors
		levels [4][2]int // by segment, for other modes and B_PRED
	}{
		{
			name:  "quantizer deltas",
			f:     testVP8{q: 60, qDeltas: [5]int{5, -3, 7, -8, 15}, level: 20},
			quant: [4]factors{{{60, 70}, {104, 130}, {47, 100}}},
		},
		{
			name:  "indices clamped to 0",
			f:     testVP8{q: 3, qDeltas: [5]int{-15, -15, -15, -15, -15}, level: 20},
			quant: [4]factors{{{4, 7}, {8, 8}, {4, 4}}},
		},
		{
			name:  "indices clamped to 127",
			f:     testVP8{q: 125, qDeltas: [5]int{15, 15, 15, 15, 15}, level: 20},
			quant: [4]factors{{{157, 274}, {314, 440}, {132, 284}}},
		},
		{
			name: "segments by absolute values",
			f: testVP8{q: 30, qDeltas: [5]int{12, 15, -13, 14, 9}, level: 20, seg: &testSegments{updateMap: true,
				updateData: true, absolute: true, quant: [4]int{-20, 127, 0, 120}, level: [4]int{0, -5, 63, 33}}},
			quant: [4]factors{{{4, 4}, {8, 8}, {4, 4}}, {{157, 284}, {314, 342}, {132, 284}},
				{{15, 4}, {34, 8}, {17, 13}}, {{157, 249}, {314, 299}, {132, 284}}},
			levels: [4][2]int{{0, 0}, {0, 0}, {63, 63}, {33, 33}},
		},
		{
			name: "segments by deltas",
			f: testVP8{q: 50, level: 60, seg: &testSegments{updateData: true, quant: [4]int{-10, 0, 15, 40},
				level: [4]int{10, -63, 63, 3}}, lfDeltas: &testLFDeltas{update: true, ref: [4]int{-10, 1, 2, 3},
				mode: [4]int{-30, 4, 5, 6}}},
			quant: [4]factors{{{37, 44}, {74, 68}, {37, 44}}, {{46, 54}, {92, 83}, {46, 54}},
				{{60, 80}, {120, 124}, {60, 80}}, {{84, 137}, {168, 212}, {84, 137}}},
			levels: [4][2]int{{60, 30}, {0, 0}, {63, 63}, {53, 23}},
		},
		{
			name: "segments without data",
			f: testVP8{q: 50, level: 60, seg: &testSegments{updateMap: true},
				lfDeltas: &testLFDeltas{update: true, ref: [4]int{5}, mode: [4]int{7}}},
			quant:  [4]factors{{{4, 4}, {8, 8}, {4, 4}}, {{4, 4}, {8, 8}, {4, 4}}, {{4, 4}, {8, 8}, {4, 4}}, {{4, 4}, {8, 8}, {4, 4}}},
			levels: [4][2]int{{5, 12}, {5, 12}, {5, 12}, {5, 12}},
		},
	} {
		f := tt.f
		f.width, f.height, f.skipProb = 16, 16, -1
		f.mbs = []testMB{{}}
		var d vp8Decoder
		if _, err := d.decode(f.vp8()); err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		segments := 1
		if f.seg != nil {
			segments = 4
		}
		for i := range segments {
			q := d.quant[i]
			if got := (factors{q.y1, q.y2, q.uv}); got != tt.quant[i] {
				t.Errorf("%s: segment %d has factors %v, want %v", tt.name, i, got, tt.quant[i])
			}
			want := tt.levels[i]
			if f.seg == nil {
				want = [2]int{f.level, f.level}
			}
			for bpred, level := range want {
				// With sharpness 0, the interior limit is the level.
				p := d.fparams[i][bpred]
				if level == 0 && p != (filterParams{}) || level > 0 && (p.mbLimit != 3*level+4 || p.subLimit != 3*level) {
					t.Errorf("%s: segment %d, B_PRED %d: %+v, want level %d", tt.name, i, bpred, p, level)
				}
			}
		}
	}

	// The other fields.
	f := testVP8{width: 16, height: 16, q: 10, level: 33, sharpness: 5, simple: true, log2Parts: 2, skipProb: 77,
		seg: &testSegments{updateMap: true, probs: [3]int{-1, 0, 200}}, lfDeltas: &testLFDeltas{update: true,
			ref: [4]int{-1, 2, -3, 63}, mode: [4]int{-63, 0, 5, -6}},
		probs: map[[4]int]uint8{{0, 1, 2, 3}: 0, {3, 7, 2, 10}: 255, {1, 0, 0, 0}: 17}}
	f.mbs = []testMB{{segment: 2}}
	var d vp8Decoder
	if _, err := d.decode(f.vp8()); err != nil {
		t.Fatal(err)
	}
	if want := (segmentHeader{enabled: true, updateMap: true, absolute: true, probs: [3]uint8{255, 0, 200}}); d.seg != want {
		t.Errorf("segments %+v, want %+v", d.seg, want)
	}
	if want := (filterHeader{simple: true, level: 33, sharpness: 5, deltas: true, refDelta: [4]int{-1, 2, -3, 63},
		modeDelta: [4]int{-63, 0, 5, -6}}); d.lf != want {
		t.Errorf("loop filter %+v, want %+v", d.lf, want)
	}
	if d.nparts != 4 || !d.useSkip || d.skipProb != 77 {
		t.Errorf("%d partitions, skip %t with probability %d", d.nparts, d.useSkip, d.skipProb)
	}
	probs := defaultCoeffProbs
	probs[0][1][2][3], probs[3][7][2][10], probs[1][0][0][0] = 0, 255, 17
	if d.probs != probs {
		t.Error("token probabilities not updated as written")
	}
}

// TestVP8ScaleBits sets the scale fields of the lossy frames of the vectors, which change nothing in the decoding
// (RFC 6386 section 9.1).
func TestVP8ScaleBits(t *testing.T) {
	for _, v := range loadVectors(t) {
		f, err := Parse(readVector(t, v))
		if err != nil {
			t.Fatal(err)
		}
		for i, fr := range f.Frames {
			if fr.lossless {
				continue
			}
			want, err := decodeVP8(fr.bitstream)
			if err != nil {
				t.Fatal(err)
			}
			b := slices.Clone(fr.bitstream)
			b[7] |= byte(1+i%3) << 6
			b[9] |= byte(3-i%3) << 6
			got, err := decodeVP8(b)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s: frame %d with scale bits: %v, or other planes", v.Name, i, err)
			}
		}
	}
}

// TestCoefficientStorage checks that dequantized coefficients are kept in 16 bits, as RFC 6386 section 14.1 says.
func TestCoefficientStorage(t *testing.T) {
	e := newBoolEncoder()
	putBlock(e, &defaultCoeffProbs[3], 0, 0, []int{2000, -2000, 0, 1})
	var d boolDecoder
	d.init(e.bytes())
	var out [16]int16
	dc, ac := int32(157), int32(284)
	n := d.readBlock(&defaultCoeffProbs[3], 0, [2]int32{dc, ac}, 0, &out, nil)
	want := [16]int16{0: int16(2000 * dc), 1: int16(-2000 * ac), 8: int16(ac)} // positions 0, 1 and 3 in raster order
	if n != 4 || out != want {
		t.Errorf("%d tokens, coefficients %v, want 4, %v", n, out, want)
	}
}

// BenchmarkDecodeVP8 decodes the largest lossy vector, or the VP8 frame of the WebP file in $VP8_BENCH.
func BenchmarkDecodeVP8(b *testing.B) {
	var data []byte
	if name := os.Getenv("VP8_BENCH"); name != "" {
		file, err := os.ReadFile(name)
		if err != nil {
			b.Fatal(err)
		}
		f, err := Parse(file)
		if err != nil {
			b.Fatal(err)
		}
		data = f.Frames[0].bitstream
	} else {
		for _, v := range loadVectors(b) {
			if v.Kind == "lossy" && (data == nil || v.Width*v.Height > 256*192-1) {
				f, err := Parse(readVector(b, v))
				if err != nil {
					b.Fatal(err)
				}
				data = f.Frames[0].bitstream
			}
		}
	}
	img, err := decodeVP8(data)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := decodeVP8(data); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(img.w*img.h)*float64(b.N)/b.Elapsed().Seconds()/1e6, "Mpixel/s")
}

// TestVP8Coverage decodes the lossy frames of the vectors, counting what the decoder meets, and requires that they
// have the features of the format that the encoders write. The others are covered by hand-made streams.
func TestVP8Coverage(t *testing.T) {
	var st vp8Stats
	for _, v := range loadVectors(t) {
		f, err := Parse(readVector(t, v))
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		for i, fr := range f.Frames {
			if fr.lossless {
				continue
			}
			d := vp8Decoder{stats: &st}
			if _, err := d.decode(fr.bitstream); err != nil {
				t.Errorf("%s: frame %d: %v", v.Name, i, err)
			}
		}
	}
	t.Logf("%+v", st)
	var missing []string
	need := func(ok bool, format string, args ...any) {
		if !ok {
			missing = append(missing, fmt.Sprintf(format, args...))
		}
	}
	for i, n := range st.profiles {
		need(n > 0, "profile %d", i)
	}
	for i, n := range st.partitions {
		need(n > 0, "%d token partitions", 1<<i)
	}
	need(st.filterTypes[0] > 0 && st.filterTypes[1] > 0, "the normal and the simple loop filter")
	need(st.levels[0] > 0 && st.levels[1] > 0, "filter level 0 and above")
	need(st.sharpness > 0, "sharpness above 0")
	need(st.segmentModes[0] > 0 && st.segmentModes[1] > 0 && st.segmentModes[2] > 0,
		"frames without segments, and with segments by delta and absolute values")
	need(st.segmentMaps > 0 && st.segmentData > 0, "segment map and data updates")
	for i, n := range st.segments {
		need(n > 0, "segment %d", i)
	}
	need(st.lfDeltas > 0 && st.lfDeltaUpdate > 0, "loop filter deltas")
	need(st.probUpdates > 0, "token probability updates")
	need(st.skipEnabled[0] > 0 && st.skipEnabled[1] > 0 && st.skipped > 0, "frames with and without skipped macroblocks")
	for i, n := range st.yModes {
		need(n > 0, "luma mode %d", i)
	}
	for i, n := range st.bModes {
		need(n > 0, "subblock mode %d", i)
	}
	for i, n := range st.uvModes {
		need(n > 0, "chroma mode %d", i)
	}
	for i, n := range st.tokens {
		need(n > 0, "token %d", i)
	}
	need(st.innerSkipped > 0, "macroblocks without filtering of inner edges")
	need(st.segmentLevels[0] > 0 && st.segmentLevels[1] > 0, "segments with filter levels 0 and above")
	need(st.quantDeltas[3] > 0 && st.quantDeltas[4] > 0 && st.quantDeltas[1] > 0, "deltas of y2 dc, uv dc and uv ac")
	if len(missing) > 0 {
		t.Errorf("the vectors lack: %s", strings.Join(missing, ", "))
	}
}
