package webp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// decodeVP8L decodes the payload of a VP8L chunk into non-premultiplied RGBA bytes.
func decodeVP8L(data []byte) (pix []byte, w, h int, err error) {
	w, h, _, err = vp8lHeader(data)
	if err != nil {
		return nil, 0, 0, err
	}
	pix, err = decodeVP8LStream(data[5:], w, h, nil)
	return pix, w, h, err
}

// decodeVP8LStream decodes an image stream of w x h pixels without the header (RFC 9649 section 3.8.1), as ALPH also
// has, into non-premultiplied RGBA bytes. stats is nil, except in tests.
func decodeVP8LStream(data []byte, w, h int, stats *vp8lStats) ([]byte, error) {
	d := vp8lDecoder{br: bitReader{data: data}, stats: stats}
	pix, err := d.decodeImage(w, h)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+2] = pix[i+2], pix[i]
	}
	return pix, nil
}

type vp8lDecoder struct {
	br      bitReader
	lengths [maxAlphabet]uint8
	tables  tableAlloc
	stats   *vp8lStats
}

// vp8lStats counts what the decoder meets, for tests.
type vp8lStats struct {
	transforms     [4]int  // by type
	predictorModes [16]int // by the low 4 bits of green in the predictor image's pixels
	cacheBits      [12]int // of each entropy-coded image; 0 if it has no color cache
	widthBits      [4]int  // of color indexing
	simpleCodes    [2]int  // by the number of symbols
	normalCodes    int
	maxSymbol      int    // normal codes with max_symbol
	repeats        [3]int // code length codes 16, 17 and 18
	singleSymbols  int    // codes of one symbol, which take no bits
	entropyImages  int
	unusedGroups   int // prefix code groups that no block uses
	cacheHits      int
	distances      [2]int // of backward references by distance code: up to 120, and longer
	overlaps       int    // backward references whose distance is shorter than the length
}

// transform is a transform of RFC 9649 section 3.5 as read from the bitstream.
type transform struct {
	kind    int
	width   int    // of the image the transform's inverse makes
	bits    int    // block size bits, or the width bits of color indexing
	data    []byte // the predictor or color transform image
	palette *[256]uint32
}

const (
	predictorTransform = iota
	colorTransform
	subtractGreenTransform
	colorIndexingTransform
)

func divRoundUp(n, bits int) int {
	return (n + 1<<bits - 1) >> bits
}

// decodeImage decodes the transforms and the spatially coded image of the ARGB image and applies the inverse
// transforms.
func (d *vp8lDecoder) decodeImage(w, h int) ([]byte, error) {
	var (
		ts   []transform
		seen [4]bool
	)
	xsize := w
	for d.br.read(1) == 1 {
		t := transform{kind: int(d.br.read(2)), width: xsize}
		if seen[t.kind] {
			return nil, errors.New("webp: VP8L: transform used twice")
		}
		seen[t.kind] = true
		if d.stats != nil {
			d.stats.transforms[t.kind]++
		}
		var err error
		switch t.kind {
		case predictorTransform, colorTransform:
			t.bits = int(d.br.read(3)) + 2
			t.data, err = d.decodeEntropyCoded(divRoundUp(xsize, t.bits), divRoundUp(h, t.bits))
			if err == nil && t.kind == predictorTransform && d.stats != nil {
				for i := 1; i < len(t.data); i += 4 {
					d.stats.predictorModes[t.data[i]&15]++
				}
			}
		case colorIndexingTransform:
			n := int(d.br.read(8)) + 1
			t.palette, err = d.decodePalette(n)
			t.bits = widthBits(n)
			xsize = divRoundUp(xsize, t.bits)
			if d.stats != nil {
				d.stats.widthBits[t.bits]++
			}
		}
		if err != nil {
			return nil, err
		}
		ts = append(ts, t)
	}
	pix, err := d.decodeSpatiallyCoded(xsize, h, w)
	if err != nil {
		return nil, err
	}
	for i := len(ts) - 1; i >= 0; i-- {
		ts[i].inverse(pix, h)
	}
	return pix, nil
}

// widthBits returns how many pixels color indexing bundles, as a power of 2, for a color table of n colors.
func widthBits(n int) int {
	switch {
	case n <= 2:
		return 3
	case n <= 4:
		return 2
	case n <= 16:
		return 1
	}
	return 0
}

// decodePalette decodes the color table of color indexing, whose colors are coded as differences.
func (d *vp8lDecoder) decodePalette(n int) (*[256]uint32, error) {
	b, err := d.decodeEntropyCoded(n, 1)
	if err != nil {
		return nil, err
	}
	var p [256]uint32
	p[0] = pixel(b, 0)
	for i := 1; i < n; i++ {
		p[i] = addPixels(pixel(b, i), p[i-1])
	}
	return &p, nil
}

// decodeEntropyCoded decodes an image of w x h pixels in one of the roles other than the ARGB image: no transforms and
// no entropy image.
func (d *vp8lDecoder) decodeEntropyCoded(w, h int) ([]byte, error) {
	cacheBits, err := d.readCacheBits()
	if err != nil {
		return nil, err
	}
	g := make([]prefixGroup, 1)
	if err := d.readGroup(&g[0], cacheBits, true); err != nil {
		return nil, err
	}
	pix := make([]byte, 4*w*h)
	if err := d.decodePixels(pix, w, h, cacheBits, g, nil, 0); err != nil {
		return nil, err
	}
	return pix, nil
}

// decodeSpatiallyCoded decodes the ARGB image of w x h pixels into the start of a buffer for fullWidth x h pixels, the
// width before color indexing.
func (d *vp8lDecoder) decodeSpatiallyCoded(w, h, fullWidth int) ([]byte, error) {
	cacheBits, err := d.readCacheBits()
	if err != nil {
		return nil, err
	}
	var (
		blocks    []uint16 // the index in groups of the prefix code group of each block
		blockBits int
		used      = []bool{true} // by meta prefix code
		groups    []prefixGroup
	)
	if d.br.read(1) == 1 {
		blockBits = int(d.br.read(3)) + 2
		bw, bh := divRoundUp(w, blockBits), divRoundUp(h, blockBits)
		img, err := d.decodeEntropyCoded(bw, bh)
		if err != nil {
			return nil, err
		}
		if d.stats != nil {
			d.stats.entropyImages++
		}
		// Meta prefix codes go up to 65535, but only the groups of those that blocks use are kept.
		blocks = make([]uint16, bw*bh)
		n := 0
		for i := range blocks {
			n = max(n, metaCode(img, i)+1)
		}
		used = make([]bool, n)
		for i := range blocks {
			used[metaCode(img, i)] = true
		}
		index := make([]uint16, n)
		k := 0
		for m, u := range used {
			if u {
				index[m] = uint16(k)
				k++
			}
		}
		for i := range blocks {
			blocks[i] = index[metaCode(img, i)]
		}
		groups = make([]prefixGroup, 0, k)
	}
	var unused prefixGroup
	for _, u := range used {
		if !u {
			if d.stats != nil {
				d.stats.unusedGroups++
			}
			if err := d.readGroup(&unused, cacheBits, false); err != nil {
				return nil, err
			}
			continue
		}
		groups = append(groups, prefixGroup{})
		if err := d.readGroup(&groups[len(groups)-1], cacheBits, true); err != nil {
			return nil, err
		}
	}
	pix := make([]byte, 4*fullWidth*h)
	if err := d.decodePixels(pix[:4*w*h], w, h, cacheBits, groups, blocks, blockBits); err != nil {
		return nil, err
	}
	return pix, nil
}

func (d *vp8lDecoder) readCacheBits() (int, error) {
	if d.br.read(1) == 0 {
		if d.stats != nil {
			d.stats.cacheBits[0]++
		}
		return 0, nil
	}
	b := int(d.br.read(4))
	if b < 1 || b > 11 {
		return 0, fmt.Errorf("webp: VP8L: color cache of %d bits", b)
	}
	if d.stats != nil {
		d.stats.cacheBits[b]++
	}
	return b, nil
}

// prefixGroup is a prefix code group: the codes of green (with lengths and color cache indexes), red, blue, alpha and
// distance.
type prefixGroup struct {
	codes [5]prefixCode
	// If the codes of red, blue and alpha have one symbol each, literal has them, and trivial is set.
	literal uint32
	trivial bool
}

var alphabetSizes = [5]int{256 + 24, 256, 256, 256, 40}

// readGroup reads the five codes of a prefix code group. If not store, it only checks them.
func (d *vp8lDecoder) readGroup(g *prefixGroup, cacheBits int, store bool) error {
	for i := range g.codes {
		n := alphabetSizes[i]
		if i == 0 && cacheBits > 0 {
			n += 1 << cacheBits
		}
		if err := d.readCode(&g.codes[i], n, store); err != nil {
			return err
		}
	}
	if !store {
		return nil
	}
	r, b, a := g.codes[1].table, g.codes[2].table, g.codes[3].table
	g.trivial = len(r) == 1 && len(b) == 1 && len(a) == 1
	if g.trivial {
		g.literal = a[0]>>entryShift<<24 | r[0]>>entryShift<<16 | b[0]>>entryShift
	}
	return nil
}

// codeLengthOrder is the order of the code lengths of the code length code (RFC 9649 section 3.7.2.1.2).
var codeLengthOrder = [19]uint8{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// readCode reads a prefix code over an alphabet of n symbols (RFC 9649 section 3.7.2.1).
func (d *vp8lDecoder) readCode(c *prefixCode, n int, store bool) error {
	br := &d.br
	lengths := d.lengths[:n]
	clear(lengths)
	if br.read(1) == 1 {
		k := int(br.read(1)) + 1
		s := int(br.read(1 + 7*uint(br.read(1))))
		// libwebp ignores symbols that the alphabet does not have, which only distance codes can meet.
		if s < n {
			lengths[s] = 1
		}
		if k == 2 {
			if s = int(br.read(8)); s < n {
				lengths[s] = 1
			}
		}
		if d.stats != nil {
			d.stats.simpleCodes[k-1]++
		}
	} else if err := d.readCodeLengths(lengths); err != nil {
		return err
	}
	if br.eof() {
		return errVP8LEOF
	}
	if !store {
		_, _, _, err := checkLengths(lengths)
		return err
	}
	var err error
	*c, err = buildCode(lengths, &d.tables)
	if err == nil && len(c.table) == 1 && d.stats != nil {
		d.stats.singleSymbols++
	}
	return err
}

// readCodeLengths reads the code lengths of a normal code length code (RFC 9649 section 3.7.2.1.2).
func (d *vp8lDecoder) readCodeLengths(lengths []uint8) error {
	br := &d.br
	var cl [len(codeLengthOrder)]uint8
	for i := range int(br.read(4)) + 4 {
		cl[codeLengthOrder[i]] = uint8(br.read(3))
	}
	var buf [1 << 7]uint32
	clc, err := buildCode(cl[:], &tableAlloc{buf: buf[:]})
	if err != nil {
		return err
	}
	// max_symbol counts code length codes, repeats included, as libwebp reads and writes it.
	maxSymbol := len(lengths)
	if br.read(1) == 1 {
		maxSymbol = 2 + int(br.read(2+2*uint(br.read(3))))
		if maxSymbol > len(lengths) {
			return fmt.Errorf("webp: VP8L: max_symbol %d over the alphabet size %d", maxSymbol, len(lengths))
		}
		if d.stats != nil {
			d.stats.maxSymbol++
		}
	}
	if d.stats != nil {
		d.stats.normalCodes++
	}
	prev := uint8(8)
	for s := 0; s < len(lengths) && maxSymbol > 0; maxSymbol-- {
		c := br.symbol(&clc)
		if c < 16 {
			lengths[s] = uint8(c)
			s++
			if c != 0 {
				prev = uint8(c)
			}
			continue
		}
		var repeat int
		v := uint8(0)
		switch c {
		case 16:
			repeat, v = 3+int(br.read(2)), prev
		case 17:
			repeat = 3 + int(br.read(3))
		default:
			repeat = 11 + int(br.read(7))
		}
		if d.stats != nil {
			d.stats.repeats[c-16]++
		}
		if s+repeat > len(lengths) {
			return errors.New("webp: VP8L: code lengths past the alphabet")
		}
		for range repeat {
			lengths[s] = v
			s++
		}
	}
	return nil
}

var errVP8LEOF = errors.New("webp: VP8L: unexpected end of data")

// distanceMap is the offsets (x to the left, y up) of distance codes 1 to 120 (RFC 9649 section 3.6.2.2.1).
var distanceMap = [120][2]int8{
	{0, 1}, {1, 0}, {1, 1}, {-1, 1}, {0, 2}, {2, 0}, {1, 2},
	{-1, 2}, {2, 1}, {-2, 1}, {2, 2}, {-2, 2}, {0, 3}, {3, 0},
	{1, 3}, {-1, 3}, {3, 1}, {-3, 1}, {2, 3}, {-2, 3}, {3, 2},
	{-3, 2}, {0, 4}, {4, 0}, {1, 4}, {-1, 4}, {4, 1}, {-4, 1},
	{3, 3}, {-3, 3}, {2, 4}, {-2, 4}, {4, 2}, {-4, 2}, {0, 5},
	{3, 4}, {-3, 4}, {4, 3}, {-4, 3}, {5, 0}, {1, 5}, {-1, 5},
	{5, 1}, {-5, 1}, {2, 5}, {-2, 5}, {5, 2}, {-5, 2}, {4, 4},
	{-4, 4}, {3, 5}, {-3, 5}, {5, 3}, {-5, 3}, {0, 6}, {6, 0},
	{1, 6}, {-1, 6}, {6, 1}, {-6, 1}, {2, 6}, {-2, 6}, {6, 2},
	{-6, 2}, {4, 5}, {-4, 5}, {5, 4}, {-5, 4}, {3, 6}, {-3, 6},
	{6, 3}, {-6, 3}, {0, 7}, {7, 0}, {1, 7}, {-1, 7}, {5, 5},
	{-5, 5}, {7, 1}, {-7, 1}, {4, 6}, {-4, 6}, {6, 4}, {-6, 4},
	{2, 7}, {-2, 7}, {7, 2}, {-7, 2}, {3, 7}, {-3, 7}, {7, 3},
	{-7, 3}, {5, 6}, {-5, 6}, {6, 5}, {-6, 5}, {8, 0}, {4, 7},
	{-4, 7}, {7, 4}, {-7, 4}, {8, 1}, {8, 2}, {6, 6}, {-6, 6},
	{8, 3}, {5, 7}, {-5, 7}, {7, 5}, {-7, 5}, {8, 4}, {6, 7},
	{-6, 7}, {7, 6}, {-7, 6}, {8, 5}, {7, 7}, {-7, 7}, {8, 6},
	{8, 7},
}

// distance returns the distance in pixels of a distance code in an image w pixels wide.
func distance(code, w int) int {
	if code > len(distanceMap) {
		return code - len(distanceMap)
	}
	o := distanceMap[code-1]
	return max(int(o[0])+int(o[1])*w, 1)
}

// decodePixels decodes w x h pixels into pix (RFC 9649 sections 3.6.2 and 3.7.2.3). blocks gives the group of each
// block of 1<<blockBits pixels square; if it is nil, all pixels use groups[0].
func (d *vp8lDecoder) decodePixels(pix []byte, w, h, cacheBits int, groups []prefixGroup, blocks []uint16,
	blockBits int) error {
	br := &d.br
	var cache []uint32
	if cacheBits > 0 {
		cache = make([]uint32, 1<<cacheBits)
	}
	shift := uint(32 - cacheBits)
	blockMask := 1<<blockBits - 1
	blocksPerRow := divRoundUp(w, blockBits)
	g := &groups[0]
	total := w * h
	for pos, x, y := 0, 0, 0; pos < total; {
		if blocks != nil && x&blockMask == 0 {
			g = &groups[blocks[(y>>blockBits)*blocksPerRow+x>>blockBits]]
		}
		s := br.symbol(&g.codes[0])
		if s >= 256 && s < 256+24 {
			length := br.readValue(s - 256)
			code := br.readValue(br.symbol(&g.codes[4]))
			dist := distance(code, w)
			if br.eof() {
				return errVP8LEOF
			}
			if dist > pos || length > total-pos {
				return fmt.Errorf("webp: VP8L: backward reference of %d pixels at a distance of %d at pixel %d of %d",
					length, dist, pos, total)
			}
			if d.stats != nil {
				d.stats.distances[min(code-1, 120)/120]++
				if dist < length {
					d.stats.overlaps++
				}
			}
			for end := pos + length; pos < end; pos++ {
				v := pixel(pix, pos-dist)
				setPixel(pix, pos, v)
				if cache != nil {
					cache[v*0x1e35a7bd>>shift] = v
				}
			}
			x, y = pos%w, pos/w
			if blocks != nil && pos < total {
				g = &groups[blocks[(y>>blockBits)*blocksPerRow+x>>blockBits]]
			}
			continue
		}
		var v uint32
		switch {
		case s >= 256:
			v = cache[s-256-24]
			if d.stats != nil {
				d.stats.cacheHits++
			}
		case g.trivial:
			v = g.literal | uint32(s)<<8
		default:
			r := br.symbol(&g.codes[1])
			b := br.symbol(&g.codes[2])
			a := br.symbol(&g.codes[3])
			v = uint32(a)<<24 | uint32(r)<<16 | uint32(s)<<8 | uint32(b)
		}
		setPixel(pix, pos, v)
		if cache != nil {
			cache[v*0x1e35a7bd>>shift] = v
		}
		pos++
		if x++; x == w {
			x = 0
			y++
			if br.eof() {
				return errVP8LEOF
			}
		}
	}
	if br.eof() {
		return errVP8LEOF
	}
	return nil
}

// metaCode returns the meta prefix code of pixel i of an entropy image: its red and green.
func metaCode(img []byte, i int) int {
	return int(binary.LittleEndian.Uint16(img[4*i+1:]))
}

// pixel returns pixel i of pix. The decoder keeps ARGB words (RFC 9649 section 3.2) in bytes, little-endian, so that
// swapping red and blue in place gives the RGBA bytes of the result.
func pixel(pix []byte, i int) uint32 {
	return binary.LittleEndian.Uint32(pix[4*i:])
}

func setPixel(pix []byte, i int, v uint32) {
	binary.LittleEndian.PutUint32(pix[4*i:], v)
}
