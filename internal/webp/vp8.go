package webp

import (
	"errors"
	"fmt"
)

// yuvImage is a decoded VP8 frame: the Y, U and V planes cropped to the frame, without padding. U and V have
// (w+1)/2 x (h+1)/2 samples.
type yuvImage struct {
	y, u, v []byte
	w, h    int
}

// decodeVP8 decodes the payload of a VP8 chunk, a key frame (RFC 6386). The planes are what libwebp decodes.
func decodeVP8(data []byte) (*yuvImage, error) {
	var d vp8Decoder
	return d.decode(data)
}

var errVP8EOF = errors.New("webp: VP8: unexpected end of data")

// vp8Decoder decodes a key frame. Where libwebp differs from the reference decoder of RFC 6386, in streams that
// encoders do not write, it follows libwebp, as the comments say.
type vp8Decoder struct {
	stats *vp8Stats // nil, except in tests

	first  boolDecoder // the frame header and the modes
	parts  [8]boolDecoder
	nparts int

	seg      segmentHeader
	lf       filterHeader
	probs    [numTypes][numBands][numCtx][numProbs]uint8
	useSkip  bool
	skipProb uint8
	quant    [4]quantFactors    // by segment
	fparams  [4][2]filterParams // by segment, and 1 for B_PRED
	filter   bool               // the frame is loop filtered

	mbw, mbh  int
	topModes  []uint8   // the subblock modes at the bottom of the macroblocks above, 4 per column
	leftModes [4]uint8  // and at the right of the macroblock to the left
	topNZ     []nzFlags // which blocks at the bottom of the macroblocks above have coefficients
	leftNZ    nzFlags
	mbs       []mbInfo // of the current row

	y, u, v          []byte // 16*mbw x 16*mbh and 8*mbw x 8*mbh
	topY, topU, topV []byte // the bottom rows of the macroblocks above before loop filtering

	bmodes [16]uint8
	coeffs [25 * 16]int16 // 16 Y blocks, 4 U, 4 V and Y2, dequantized, in raster order
	ac     uint32         // bit i: block i has a coefficient after the first
	ybuf   [17 * ybps]byte
	ubuf   [9 * cbps]byte
	vbuf   [9 * cbps]byte
}

type segmentHeader struct {
	enabled, updateMap, absolute bool
	quant, level                 [4]int
	probs                        [3]uint8
}

type filterHeader struct {
	simple           bool
	level, sharpness int
	deltas           bool
	refDelta         [4]int
	modeDelta        [4]int
}

// quantFactors are the dequantization factors of a segment, for DC and AC.
type quantFactors struct {
	y1, y2, uv [2]int32
}

// filterParams are the limits of the loop filter of a segment and mode; mbLimit is 0 if the level is 0.
type filterParams struct {
	mbLimit, subLimit, interior, hev int
}

// nzFlags tells which blocks along an edge of a macroblock have coefficients, as contexts of the tokens.
type nzFlags struct {
	y    [4]uint8
	u, v [2]uint8
	y2   uint8
}

type mbInfo struct {
	segment uint8
	bpred   uint8 // 1 for B_PRED
	inner   bool  // filter the edges between subblocks
}

// The layout of the macroblock buffers: row -1 and column -1 hold the pixels above and to the left, and luma has 4
// more columns for the pixels above and to the right.
const (
	ybps = 32
	yorg = ybps + 1
	cbps = 16
	corg = cbps + 1
)

// vp8Stats counts what the decoder meets, for tests.
type vp8Stats struct {
	frames        int
	profiles      [4]int
	partitions    [4]int // by log2 of the number
	filterTypes   [2]int // normal and simple, of frames with a filter level
	levels        [2]int // frames with filter level 0 and above
	sharpness     int    // filtered frames with sharpness above 0
	segmentModes  [3]int // frames without segmentation, with delta values and with absolute values
	segmentMaps   int    // frames that update the segment map
	segmentData   int    // frames that update the segment data
	segments      [4]int // macroblocks by segment
	lfDeltas      int    // frames with loop filter deltas
	lfDeltaUpdate int    // frames that update them
	quantDeltas   [5]int // frames with a non-zero delta of y1 dc, y2 dc, y2 ac, uv dc and uv ac
	probUpdates   int    // token probabilities updated
	skipEnabled   [2]int // frames without and with mb_no_skip_coeff
	skipped       int    // macroblocks with the skip flag
	yModes        [5]int
	bModes        [numBModes]int
	uvModes       [4]int
	tokens        [11]int // non-zero tokens: dct_1 to dct_4, then categories 1 to 6, and dct_0
	innerSkipped  int     // filtered macroblocks whose inner edges are not filtered
	segmentLevels [2]int  // segments with filter levels 0 and above, in filtered frames
}

func (d *vp8Decoder) decode(data []byte) (*yuvImage, error) {
	w, h, err := vp8Header(data)
	if err != nil {
		return nil, err
	}
	tag := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	firstSize := int(tag >> 5)
	data = data[10:]
	d.first.init(data[:firstSize])
	if err := d.readHeader(data[firstSize:]); err != nil {
		return nil, err
	}
	if st := d.stats; st != nil {
		st.frames++
		st.profiles[tag>>1&7]++
	}
	d.mbw, d.mbh = (w+15)/16, (h+15)/16
	d.init()
	for mby := range d.mbh {
		tokens := &d.parts[mby&(d.nparts-1)]
		d.leftModes = [4]uint8{}
		d.leftNZ = nzFlags{}
		for mbx := range d.mbw {
			d.decodeMB(mbx, mby, tokens)
		}
		if d.first.eof || tokens.eof {
			return nil, errVP8EOF
		}
		d.saveTop(mby)
		if d.filter {
			d.filterRow(mby)
		}
	}
	return &yuvImage{
		y: crop(d.y, 16*d.mbw, w, h),
		u: crop(d.u, 8*d.mbw, (w+1)/2, (h+1)/2),
		v: crop(d.v, 8*d.mbw, (w+1)/2, (h+1)/2),
		w: w,
		h: h,
	}, nil
}

// readHeader reads the frame header from the first partition (RFC 6386 sections 9.2 to 9.11 and 19.2) and finds the
// token partitions in rest, the data after the first partition.
func (d *vp8Decoder) readHeader(rest []byte) error {
	br := &d.first
	// The color space and the clamping type are ignored, as libwebp does; clamping changes nothing in a frame that
	// needs none.
	br.readFlag()
	br.readFlag()

	s := &d.seg
	// Without segment data, libwebp uses the absolute values 0.
	s.absolute = true
	if s.enabled = br.readFlag(); s.enabled {
		s.updateMap = br.readFlag()
		if br.readFlag() {
			s.absolute = br.readFlag()
			for i := range s.quant {
				s.quant[i] = br.readOptionalSigned(7)
			}
			for i := range s.level {
				s.level[i] = br.readOptionalSigned(6)
			}
			if d.stats != nil {
				d.stats.segmentData++
			}
		}
		if s.updateMap {
			for i := range s.probs {
				s.probs[i] = 255
				if br.readFlag() {
					s.probs[i] = uint8(br.readLiteral(8))
				}
			}
		}
	}

	f := &d.lf
	f.simple = br.readFlag()
	f.level = br.readLiteral(6)
	f.sharpness = br.readLiteral(3)
	update := false
	if f.deltas = br.readFlag(); f.deltas {
		if update = br.readFlag(); update {
			for i := range f.refDelta {
				if br.readFlag() {
					f.refDelta[i] = br.readSigned(6)
				}
			}
			for i := range f.modeDelta {
				if br.readFlag() {
					f.modeDelta[i] = br.readSigned(6)
				}
			}
		}
	}

	log2 := br.readLiteral(2)
	if err := d.initPartitions(1<<log2, rest); err != nil {
		return err
	}

	q := br.readLiteral(7)
	var dq [5]int // y1 dc, y2 dc, y2 ac, uv dc and uv ac
	for i := range dq {
		dq[i] = br.readOptionalSigned(4)
	}
	br.readFlag() // refresh_entropy_probs, which only matters for later frames

	d.probs = defaultCoeffProbs
	updates := 0
	for i := range d.probs {
		for j := range d.probs[i] {
			for k := range d.probs[i][j] {
				for l := range d.probs[i][j][k] {
					if br.readBool(coeffUpdateProbs[i][j][k][l]) {
						d.probs[i][j][k][l] = uint8(br.readLiteral(8))
						updates++
					}
				}
			}
		}
	}
	if d.useSkip = br.readFlag(); d.useSkip {
		d.skipProb = uint8(br.readLiteral(8))
	}

	d.initQuant(q, dq)
	d.initFilter()
	if st := d.stats; st != nil {
		st.partitions[log2]++
		st.probUpdates += updates
		st.skipEnabled[btoi(d.useSkip)]++
		for i, v := range dq {
			if v != 0 {
				st.quantDeltas[i]++
			}
		}
		mode := 0
		if s.enabled {
			mode = 1 + btoi(s.absolute)
			if s.updateMap {
				st.segmentMaps++
			}
		}
		st.segmentModes[mode]++
		if f.deltas {
			st.lfDeltas++
			if update {
				st.lfDeltaUpdate++
			}
		}
		st.levels[btoi(f.level > 0)]++
		if f.level > 0 {
			st.filterTypes[btoi(f.simple)]++
			if f.sharpness > 0 {
				st.sharpness++
			}
		}
	}
	return nil
}

// initPartitions finds the n token partitions in data: the sizes of all but the last in 3 bytes each, then the
// partitions (RFC 6386 section 9.5).
func (d *vp8Decoder) initPartitions(n int, data []byte) error {
	if len(data) < 3*(n-1) {
		return errors.New("webp: VP8 partition sizes past the end of the data")
	}
	sizes, data := data[:3*(n-1)], data[3*(n-1):]
	for i := range n - 1 {
		size := uint24(sizes[3*i:])
		if size > len(data) {
			return fmt.Errorf("webp: VP8 partition %d of %d bytes past the end of the data", i+1, size)
		}
		d.parts[i].init(data[:size])
		data = data[size:]
	}
	if len(data) == 0 {
		// libwebp rejects it even if no row uses it.
		return errors.New("webp: VP8: the last partition is empty")
	}
	d.parts[n-1].init(data)
	d.nparts = n
	return nil
}

// initQuant computes the dequantization factors of each segment from the base quantizer index q and the deltas
// (RFC 6386 sections 9.6 and 14.1).
func (d *vp8Decoder) initQuant(q int, dq [5]int) {
	dc := func(i int) int32 { return int32(dcTable[min(max(i, 0), 127)]) }
	ac := func(i int) int32 { return int32(acTable[min(max(i, 0), 127)]) }
	for i := range d.quant {
		sq := q
		if d.seg.enabled {
			if sq = d.seg.quant[i]; !d.seg.absolute {
				sq += q
			}
		}
		d.quant[i] = quantFactors{
			y1: [2]int32{dc(sq + dq[0]), ac(sq)},
			y2: [2]int32{2 * dc(sq+dq[1]), max(ac(sq+dq[2])*155/100, 8)},
			uv: [2]int32{min(dc(sq+dq[3]), 132), ac(sq + dq[4])},
		}
	}
}

// initFilter computes the loop filter limits of each segment and mode (RFC 6386 sections 9.3, 9.4 and 15.4).
func (d *vp8Decoder) initFilter() {
	f := &d.lf
	d.filter = f.level > 0
	if !d.filter {
		return
	}
	for i := range d.fparams {
		base := f.level
		if d.seg.enabled {
			if base = d.seg.level[i]; !d.seg.absolute {
				base += f.level
			}
		}
		for bpred := range d.fparams[i] {
			// The deltas of intra frames and of B_PRED, added to the level of the segment before it is clamped, as
			// libwebp does.
			level := base
			if f.deltas {
				level += f.refDelta[0]
				if bpred == 1 {
					level += f.modeDelta[0]
				}
			}
			level = min(max(level, 0), 63)
			p := &d.fparams[i][bpred]
			*p = filterParams{}
			if level == 0 {
				continue
			}
			interior := level
			if f.sharpness > 0 {
				if f.sharpness > 4 {
					interior >>= 2
				} else {
					interior >>= 1
				}
				interior = min(interior, 9-f.sharpness)
			}
			interior = max(interior, 1)
			p.mbLimit = (level+2)*2 + interior
			p.subLimit = level*2 + interior
			p.interior = interior
			if level >= 40 {
				p.hev = 2
			} else if level >= 15 {
				p.hev = 1
			}
		}
	}
	if st := d.stats; st != nil {
		for i := range d.fparams {
			for _, p := range d.fparams[i] {
				st.segmentLevels[btoi(p.mbLimit > 0)]++
			}
		}
	}
}

// init allocates the planes and the contexts.
func (d *vp8Decoder) init() {
	d.y = make([]byte, 256*d.mbw*d.mbh)
	d.u = make([]byte, 64*d.mbw*d.mbh)
	d.v = make([]byte, 64*d.mbw*d.mbh)
	d.topY = make([]byte, 16*d.mbw+4)
	d.topU = make([]byte, 8*d.mbw)
	d.topV = make([]byte, 8*d.mbw)
	d.topModes = make([]uint8, 4*d.mbw)
	d.topNZ = make([]nzFlags, d.mbw)
	d.mbs = make([]mbInfo, d.mbw)
}

// decodeMB reads the modes of a macroblock from the first partition and its coefficients from tokens, and reconstructs
// it.
func (d *vp8Decoder) decodeMB(mbx, mby int, tokens *boolDecoder) {
	br := &d.first
	m := &d.mbs[mbx]
	m.segment = 0
	if d.seg.updateMap {
		m.segment = uint8(br.readTree(segmentTree[:], d.seg.probs[:]))
	}
	skip := d.useSkip && br.readBool(d.skipProb)

	ymode := br.readTree(kfYModeTree[:], kfYModeProbs[:])
	top := (*[4]uint8)(d.topModes[4*mbx:])
	if ymode == bPred {
		for j := range 4 {
			left := d.leftModes[j]
			for i := range 4 {
				mode := uint8(br.readTree(bModeTree[:], kfBModeProbs[top[i]][left][:]))
				d.bmodes[4*j+i] = mode
				top[i], left = mode, mode
			}
			d.leftModes[j] = left
		}
	} else {
		// For the contexts of later subblock modes, the subblock mode that matches the macroblock mode.
		mode := [4]uint8{bDCPred, bVEPred, bHEPred, bTMPred}[ymode]
		*top = [4]uint8{mode, mode, mode, mode}
		d.leftModes = *top
	}
	uvmode := br.readTree(uvModeTree[:], kfUVModeProbs[:])
	m.bpred = uint8(btoi(ymode == bPred))

	clear(d.coeffs[:])
	d.ac = 0
	tnz, lnz := &d.topNZ[mbx], &d.leftNZ
	if skip {
		tnz.y, tnz.u, tnz.v = [4]uint8{}, [2]uint8{}, [2]uint8{}
		lnz.y, lnz.u, lnz.v = [4]uint8{}, [2]uint8{}, [2]uint8{}
		if ymode != bPred {
			tnz.y2, lnz.y2 = 0, 0
		}
		m.inner = ymode == bPred
	} else {
		m.inner = d.readCoefficients(tokens, tnz, lnz, ymode != bPred, &d.quant[m.segment]) || ymode == bPred
	}

	if st := d.stats; st != nil {
		st.segments[m.segment]++
		st.yModes[ymode]++
		st.uvModes[uvmode]++
		if ymode == bPred {
			for _, b := range d.bmodes {
				st.bModes[b]++
			}
		}
		if skip {
			st.skipped++
		}
		if d.filter && !m.inner && d.fparams[m.segment][m.bpred].mbLimit > 0 {
			st.innerSkipped++
		}
	}

	d.reconstruct(mbx, mby, ymode, uvmode)
}

// saveTop keeps the bottom rows of the macroblocks of row mby, before loop filtering, for the predictions of the row
// below.
func (d *vp8Decoder) saveTop(mby int) {
	ys, cs := 16*d.mbw, 8*d.mbw
	copy(d.topY, d.y[(16*mby+15)*ys:][:ys])
	// The pixels above and to the right of the last macroblock repeat the last pixel (RFC 6386 section 12.3).
	fill(d.topY[ys:], d.topY[ys-1])
	copy(d.topU, d.u[(8*mby+7)*cs:][:cs])
	copy(d.topV, d.v[(8*mby+7)*cs:][:cs])
}

// crop returns the w x h pixels at the top left of a plane whose rows are stride apart, moving them in place.
func crop(p []byte, stride, w, h int) []byte {
	for y := 1; y < h; y++ {
		copy(p[y*w:], p[y*stride:y*stride+w])
	}
	return p[:w*h]
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
