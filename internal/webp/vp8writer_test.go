package webp

import (
	"encoding/binary"
	"fmt"
)

// This file writes VP8 key frames for tests.

// boolEncoder writes bools as the encoder of RFC 6386 section 7.3 does.
type boolEncoder struct {
	buf      []byte
	rng      uint32
	bottom   uint32
	bitCount int
}

func newBoolEncoder() *boolEncoder {
	return &boolEncoder{rng: 255, bitCount: 24}
}

// carry adds 1 to the bytes written.
func (e *boolEncoder) carry() {
	i := len(e.buf) - 1
	for ; e.buf[i] == 255; i-- {
		e.buf[i] = 0
	}
	e.buf[i]++
}

func (e *boolEncoder) putBool(prob uint8, b bool) {
	split := 1 + (e.rng-1)*uint32(prob)>>8
	if b {
		e.bottom += split
		e.rng -= split
	} else {
		e.rng = split
	}
	for e.rng < 128 {
		e.rng <<= 1
		if e.bottom&(1<<31) != 0 {
			e.carry()
		}
		e.bottom <<= 1
		if e.bitCount--; e.bitCount == 0 {
			e.buf = append(e.buf, byte(e.bottom>>24))
			e.bottom &= 1<<24 - 1
			e.bitCount = 8
		}
	}
}

// bytes flushes the encoder and returns what it wrote.
func (e *boolEncoder) bytes() []byte {
	c, v := e.bitCount, e.bottom
	if v&(1<<(32-c)) != 0 {
		e.carry()
	}
	v <<= c & 7
	for c >>= 3; c > 0; c-- {
		v <<= 8
	}
	for range 4 {
		e.buf = append(e.buf, byte(v>>24))
		v <<= 8
	}
	return e.buf
}

func (e *boolEncoder) putFlag(b bool) {
	e.putBool(128, b)
}

func (e *boolEncoder) putLiteral(v, n int) {
	for i := n - 1; i >= 0; i-- {
		e.putFlag(v>>i&1 != 0)
	}
}

// putSigned writes the magnitude of v in n bits and its sign.
func (e *boolEncoder) putSigned(v, n int) {
	e.putLiteral(abs(v), n)
	e.putFlag(v < 0)
}

// putOptionalSigned writes a flag for v != 0 and then v.
func (e *boolEncoder) putOptionalSigned(v, n int) {
	e.putFlag(v != 0)
	if v != 0 {
		e.putSigned(v, n)
	}
}

// putTree writes the value v of a tree of RFC 6386 section 8.1.
func (e *boolEncoder) putTree(tree []int8, probs []uint8, v int) {
	var path func(node int) []bool
	path = func(node int) []bool {
		for b := range 2 {
			next := int(tree[2*node+b])
			if next <= 0 && -next == v {
				return []bool{b == 1}
			}
			if next > 0 {
				if p := path(next / 2); p != nil {
					return append([]bool{b == 1}, p...)
				}
			}
		}
		return nil
	}
	node := 0
	for _, b := range path(0) {
		e.putBool(probs[node], b)
		if next := int(tree[2*node+btoi(b)]); next > 0 {
			node = next / 2
		}
	}
}

// testVP8 describes a key frame to write.
type testVP8 struct {
	width, height  int
	hscale, vscale int // the scale fields above the sizes
	version        int
	colorSpace     bool
	clampType      bool
	seg            *testSegments // nil: segmentation is off
	simple         bool
	level          int
	sharpness      int
	lfDeltas       *testLFDeltas // nil: off
	log2Parts      int
	q              int
	qDeltas        [5]int                              // y1 dc, y2 dc, y2 ac, uv dc and uv ac
	probs          map[[4]int]uint8                    // token probabilities to update
	skipProb       int                                 // -1: mb_no_skip_coeff is 0
	mbs            []testMB                            // in raster order
	partSizes      func(sizes []int)                   // changes the sizes written for the token partitions
	edit           func(first *[]byte, parts [][]byte) // changes the partitions before they are put together
}

type testSegments struct {
	updateMap, updateData, absolute bool
	quant, level                    [4]int
	probs                           [3]int // -1: not written, 255
}

type testLFDeltas struct {
	update    bool
	ref, mode [4]int // written if not 0
}

// testMB is a macroblock: its modes and the quantized coefficients of each block in the order of the bitstream from
// its first position. A block's coefficients end with dct_eob unless they reach position 16, so the last must not be
// 0 then.
type testMB struct {
	segment int
	skip    bool
	ymode   int
	bmodes  [16]int
	uvmode  int
	coeffs  [25][]int // 16 Y blocks, 4 U, 4 V and Y2
}

// vp8 returns the frame as the payload of a VP8 chunk.
func (f *testVP8) vp8() []byte {
	mbw, mbh := (f.width+15)/16, (f.height+15)/16
	if len(f.mbs) != mbw*mbh {
		panic(fmt.Sprintf("%d macroblocks, want %d", len(f.mbs), mbw*mbh))
	}
	e := newBoolEncoder()
	e.putFlag(f.colorSpace)
	e.putFlag(f.clampType)
	e.putFlag(f.seg != nil)
	segProbs := [3]uint8{255, 255, 255}
	if s := f.seg; s != nil {
		e.putFlag(s.updateMap)
		e.putFlag(s.updateData)
		if s.updateData {
			e.putFlag(s.absolute)
			for _, q := range s.quant {
				e.putOptionalSigned(q, 7)
			}
			for _, l := range s.level {
				e.putOptionalSigned(l, 6)
			}
		}
		if s.updateMap {
			for i, p := range s.probs {
				e.putFlag(p >= 0)
				if p >= 0 {
					e.putLiteral(p, 8)
					segProbs[i] = uint8(p)
				}
			}
		}
	}
	e.putFlag(f.simple)
	e.putLiteral(f.level, 6)
	e.putLiteral(f.sharpness, 3)
	e.putFlag(f.lfDeltas != nil)
	if d := f.lfDeltas; d != nil {
		e.putFlag(d.update)
		if d.update {
			for _, v := range append(d.ref[:], d.mode[:]...) {
				e.putOptionalSigned(v, 6)
			}
		}
	}
	e.putLiteral(f.log2Parts, 2)
	e.putLiteral(f.q, 7)
	for _, dq := range f.qDeltas {
		e.putOptionalSigned(dq, 4)
	}
	e.putFlag(false) // refresh_entropy_probs
	probs := defaultCoeffProbs
	for i := range probs {
		for j := range probs[i] {
			for k := range probs[i][j] {
				for l := range probs[i][j][k] {
					p, ok := f.probs[[4]int{i, j, k, l}]
					e.putBool(coeffUpdateProbs[i][j][k][l], ok)
					if ok {
						e.putLiteral(int(p), 8)
						probs[i][j][k][l] = p
					}
				}
			}
		}
	}
	e.putFlag(f.skipProb >= 0)
	if f.skipProb >= 0 {
		e.putLiteral(f.skipProb, 8)
	}

	parts := make([]*boolEncoder, 1<<f.log2Parts)
	for i := range parts {
		parts[i] = newBoolEncoder()
	}
	topModes := make([]int, 4*mbw)
	topNZ := make([]nzFlags, mbw)
	for mby := range mbh {
		var leftModes [4]int
		var leftNZ nzFlags
		tokens := parts[mby%len(parts)]
		for mbx := range mbw {
			mb := &f.mbs[mby*mbw+mbx]
			if f.seg != nil && f.seg.updateMap {
				e.putTree(segmentTree[:], segProbs[:], mb.segment)
			}
			if f.skipProb >= 0 {
				e.putBool(uint8(f.skipProb), mb.skip)
			}
			e.putTree(kfYModeTree[:], kfYModeProbs[:], mb.ymode)
			top := topModes[4*mbx : 4*mbx+4]
			if mb.ymode == bPred {
				for j := range 4 {
					for i := range 4 {
						m := mb.bmodes[4*j+i]
						e.putTree(bModeTree[:], kfBModeProbs[top[i]][leftModes[j]][:], m)
						top[i], leftModes[j] = m, m
					}
				}
			} else {
				m := []int{bDCPred, bVEPred, bHEPred, bTMPred}[mb.ymode]
				for i := range 4 {
					top[i], leftModes[i] = m, m
				}
			}
			e.putTree(uvModeTree[:], kfUVModeProbs[:], mb.uvmode)
			putCoefficients(tokens, &probs, mb, &topNZ[mbx], &leftNZ, f.skipProb >= 0 && mb.skip)
		}
	}

	first := e.bytes()
	data := make([][]byte, len(parts))
	for i, p := range parts {
		data[i] = p.bytes()
	}
	if f.edit != nil {
		f.edit(&first, data)
	}
	sizes := make([]int, len(data))
	for i, p := range data {
		sizes[i] = len(p)
	}
	if f.partSizes != nil {
		f.partSizes(sizes)
	}
	tag := uint32(f.version)<<1 | 1<<4 | uint32(len(first))<<5
	out := []byte{byte(tag), byte(tag >> 8), byte(tag >> 16), 0x9d, 0x01, 0x2a, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(out[6:], uint16(f.width|f.hscale<<14))
	binary.LittleEndian.PutUint16(out[8:], uint16(f.height|f.vscale<<14))
	out = append(out, first...)
	for _, s := range sizes[:len(sizes)-1] {
		out = append(out, byte(s), byte(s>>8), byte(s>>16))
	}
	for _, p := range data {
		out = append(out, p...)
	}
	return out
}

// putCoefficients writes the tokens of a macroblock and updates the contexts as the decoder does.
func putCoefficients(e *boolEncoder, probs *[numTypes][numBands][numCtx][numProbs]uint8, mb *testMB, top,
	left *nzFlags, skip bool) {
	y2 := mb.ymode != bPred
	if skip {
		top.y, top.u, top.v = [4]uint8{}, [2]uint8{}, [2]uint8{}
		left.y, left.u, left.v = [4]uint8{}, [2]uint8{}, [2]uint8{}
		if y2 {
			top.y2, left.y2 = 0, 0
		}
		return
	}
	first, ytype := 0, 3
	if y2 {
		n := putBlock(e, &probs[1], int(top.y2+left.y2), 0, mb.coeffs[24])
		top.y2, left.y2 = uint8(btoi(n > 0)), uint8(btoi(n > 0))
		first, ytype = 1, 0
	} else if mb.coeffs[24] != nil {
		panic("coefficients of Y2 in a B_PRED macroblock")
	}
	for b := range 16 {
		i, j := b&3, b>>2
		n := putBlock(e, &probs[ytype], int(top.y[i]+left.y[j]), first, mb.coeffs[b])
		top.y[i], left.y[j] = uint8(btoi(n > first)), uint8(btoi(n > first))
	}
	for b := range 8 {
		t, l := &top.u, &left.u
		if b >= 4 {
			t, l = &top.v, &left.v
		}
		i, j := b&1, b>>1&1
		n := putBlock(e, &probs[2], int(t[i]+l[j]), 0, mb.coeffs[16+b])
		t[i], l[j] = uint8(btoi(n > 0)), uint8(btoi(n > 0))
	}
}

// putBlock writes the tokens of the coefficients from position first and returns the position after them.
func putBlock(e *boolEncoder, p *[numBands][numCtx][numProbs]uint8, ctx, first int, coeffs []int) int {
	n := first
	zero := false
	for _, v := range coeffs {
		probs := &p[coeffBands[n]][ctx]
		if !zero {
			e.putBool(probs[0], true)
		}
		a := abs(v)
		e.putBool(probs[1], a != 0)
		switch a {
		case 0:
			ctx, zero = 0, true
			n++
			continue
		case 1:
			e.putBool(probs[2], false)
			ctx = 1
		default:
			e.putBool(probs[2], true)
			putLarge(e, probs, a)
			ctx = 2
		}
		e.putFlag(v < 0)
		zero = false
		n++
	}
	if n < 16 {
		if zero {
			panic("dct_eob after a zero")
		}
		e.putBool(p[coeffBands[n]][ctx][0], false)
	}
	return n
}

// putLarge writes the rest of the token of a value of at least 2 and its extra bits.
func putLarge(e *boolEncoder, p *[numProbs]uint8, a int) {
	if a <= 4 {
		e.putBool(p[3], false)
		e.putBool(p[4], a > 2)
		if a > 2 {
			e.putBool(p[5], a == 4)
		}
		return
	}
	e.putBool(p[3], true)
	if a <= 10 {
		e.putBool(p[6], false)
		e.putBool(p[7], a >= 7)
		if a < 7 {
			e.putBool(159, a == 6)
		} else {
			e.putBool(165, (a-7)&2 != 0)
			e.putBool(145, (a-7)&1 != 0)
		}
		return
	}
	e.putBool(p[6], true)
	cat := 0
	for cat < 3 && a >= []int{19, 35, 67}[cat] {
		cat++
	}
	e.putBool(p[8], cat >= 2)
	e.putBool(p[9+cat/2], cat&1 != 0)
	x := a - []int{11, 19, 35, 67}[cat]
	extra := catProbs[cat]
	for i, prob := range extra {
		e.putBool(prob, x>>(len(extra)-1-i)&1 != 0)
	}
}
