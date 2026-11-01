package webp

import (
	"encoding/binary"
	"math/bits"
	"slices"
)

// This file writes VP8L bitstreams and WebP files for tests.

// bitWriter writes bits least significant first, as VP8L reads them.
type bitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

func (w *bitWriter) write(v int, n uint) {
	w.acc |= uint64(v) & (1<<n - 1) << w.n
	w.n += n
	for w.n >= 8 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc >>= 8
		w.n -= 8
	}
}

// bytes returns the bits written, padded with zeros to a whole byte.
func (w *bitWriter) bytes() []byte {
	b := slices.Clone(w.buf)
	if w.n > 0 {
		b = append(b, byte(w.acc))
	}
	return b
}

// testCode is a canonical prefix code to write symbols with.
type testCode struct {
	lengths []uint8
	codes   []int // reversed, as written
	single  bool  // one symbol, written with no bits
}

func newTestCode(lengths []uint8) testCode {
	c := testCode{lengths: lengths, codes: make([]int, len(lengths))}
	var count [16]int
	n := 0
	for _, l := range lengths {
		if l != 0 {
			count[l]++
			n++
		}
	}
	c.single = n == 1
	var next [16]int
	code := 0
	for l := 1; l < 16; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	for s, l := range lengths {
		if l != 0 {
			c.codes[s] = int(bits.Reverse16(uint16(next[l]))) >> (16 - l)
			next[l]++
		}
	}
	return c
}

func (w *bitWriter) symbol(c *testCode, s int) {
	if !c.single {
		w.write(c.codes[s], uint(c.lengths[s]))
	}
}

// completeLengths returns the lengths of a complete code over the symbols of an alphabet of n.
func completeLengths(n int, symbols []int) []uint8 {
	lengths := make([]uint8, n)
	if len(symbols) == 1 {
		lengths[symbols[0]] = 1
		return lengths
	}
	d := bits.Len(uint(len(symbols) - 1))
	short := 1<<d - len(symbols)
	for i, s := range symbols {
		lengths[s] = uint8(d)
		if i < short {
			lengths[s] = uint8(d - 1)
		}
	}
	return lengths
}

// code writes a prefix code over an alphabet of n with the symbols (none means symbol 0): a simple code if it can, else
// a normal one with the code lengths of completeLengths.
func (w *bitWriter) code(n int, symbols ...int) testCode {
	symbols = slices.Compact(slices.Sorted(slices.Values(symbols)))
	if len(symbols) == 0 {
		symbols = []int{0}
	}
	if len(symbols) <= 2 && symbols[len(symbols)-1] < 256 {
		w.write(1, 1)
		w.write(len(symbols)-1, 1)
		if symbols[0] < 2 {
			w.write(0, 1)
			w.write(symbols[0], 1)
		} else {
			w.write(1, 1)
			w.write(symbols[0], 8)
		}
		if len(symbols) == 2 {
			w.write(symbols[1], 8)
		}
		return newTestCode(completeLengths(n, symbols))
	}
	lengths := completeLengths(n, symbols)
	w.normalCode(lengths)
	return newTestCode(lengths)
}

// normalCode writes code lengths with a code length code that gives 4 bits to each of 0 to 15, without max_symbol.
func (w *bitWriter) normalCode(lengths []uint8) {
	w.write(0, 1)
	w.write(19-4, 4)
	cl := make([]uint8, 19)
	for _, s := range codeLengthOrder {
		if s < 16 {
			cl[s] = 4
		}
		w.write(int(cl[s]), 3)
	}
	w.write(0, 1)
	c := newTestCode(cl)
	for _, l := range lengths {
		w.symbol(&c, int(l))
	}
}

// vp8lToken is a coded pixel: a literal, a backward reference, or a color cache index.
type vp8lToken struct {
	kind   byte // 'l', 'r' or 'c'
	argb   uint32
	length int
	code   int // the distance code of a reference, or the color cache index
}

func lit(argb uint32) vp8lToken      { return vp8lToken{kind: 'l', argb: argb} }
func ref(length, code int) vp8lToken { return vp8lToken{kind: 'r', length: length, code: code} }
func hit(index int) vp8lToken        { return vp8lToken{kind: 'c', code: index} }
func lits(pix ...uint32) []vp8lToken { return mapSlice(pix, lit) }

func mapSlice[T, U any](s []T, f func(T) U) []U {
	r := make([]U, len(s))
	for i, v := range s {
		r[i] = f(v)
	}
	return r
}

// prefixValue returns the prefix code, the number of extra bits and the extra bits of a length or distance value.
func prefixValue(v int) (code int, n uint, extra int) {
	if v <= 4 {
		return v - 1, 0, 0
	}
	x := v - 1
	hb := bits.Len(uint(x)) - 1
	n = uint(hb - 1)
	return 2*hb + x>>n&1, n, x & (1<<n - 1)
}

// imageData writes an image's color cache info, a 0 bit for no meta prefix codes if spatial, one prefix code group,
// and the tokens.
func (w *bitWriter) imageData(cacheBits int, spatial bool, tokens []vp8lToken) {
	if cacheBits == 0 {
		w.write(0, 1)
	} else {
		w.write(1, 1)
		w.write(cacheBits, 4)
	}
	if spatial {
		w.write(0, 1)
	}
	g := w.group(cacheBits, tokens)
	w.tokens(&g, tokens)
}

// group writes the five codes of a prefix code group for the tokens.
func (w *bitWriter) group(cacheBits int, tokens []vp8lToken) [5]testCode {
	var syms [5][]int
	for _, t := range tokens {
		switch t.kind {
		case 'l':
			syms[0] = append(syms[0], int(t.argb>>8&0xff))
			syms[1] = append(syms[1], int(t.argb>>16&0xff))
			syms[2] = append(syms[2], int(t.argb&0xff))
			syms[3] = append(syms[3], int(t.argb>>24))
		case 'r':
			lc, _, _ := prefixValue(t.length)
			dc, _, _ := prefixValue(t.code)
			syms[0] = append(syms[0], 256+lc)
			syms[4] = append(syms[4], dc)
		case 'c':
			syms[0] = append(syms[0], 280+t.code)
		}
	}
	var g [5]testCode
	for i := range syms {
		n := alphabetSizes[i]
		if i == 0 && cacheBits > 0 {
			n += 1 << cacheBits
		}
		g[i] = w.code(n, syms[i]...)
	}
	return g
}

func (w *bitWriter) tokens(g *[5]testCode, tokens []vp8lToken) {
	for _, t := range tokens {
		switch t.kind {
		case 'l':
			w.symbol(&g[0], int(t.argb>>8&0xff))
			w.symbol(&g[1], int(t.argb>>16&0xff))
			w.symbol(&g[2], int(t.argb&0xff))
			w.symbol(&g[3], int(t.argb>>24))
		case 'r':
			lc, ln, le := prefixValue(t.length)
			dc, dn, de := prefixValue(t.code)
			w.symbol(&g[0], 256+lc)
			w.write(le, ln)
			w.symbol(&g[4], dc)
			w.write(de, dn)
		case 'c':
			w.symbol(&g[0], 280+t.code)
		}
	}
}

// subimage writes an entropy-coded image of the pixels, as literals.
func (w *bitWriter) subimage(pix ...uint32) {
	w.imageData(0, false, lits(pix...))
}

// header writes the header of a VP8L bitstream.
func (w *bitWriter) header(width, height int, alpha bool) {
	w.write(0x2f, 8)
	w.write(width-1, 14)
	w.write(height-1, 14)
	w.write(btoi(alpha), 1)
	w.write(0, 3)
}

// vp8lImage returns a VP8L bitstream of w x h pixels, without transforms or color cache, coded as literals.
func vp8lImage(width, height int, pix ...uint32) []byte {
	var w bitWriter
	w.header(width, height, false)
	w.write(0, 1)
	w.imageData(0, true, lits(pix...))
	return w.bytes()
}

// withAlphaBit returns the VP8L bitstream b with alpha_is_used set.
func withAlphaBit(b []byte) []byte {
	b = slices.Clone(b)
	b[4] |= 0x10
	return b
}

// vp8lSolid returns a VP8L bitstream of w x h pixels of one color, which take no bits.
func vp8lSolid(width, height int, argb uint32) []byte {
	pix := make([]uint32, width*height)
	for i := range pix {
		pix[i] = argb
	}
	return vp8lImage(width, height, pix...)
}

// fakeVP8 returns the start of a VP8 key frame of w x h pixels, which Parse accepts.
func fakeVP8(width, height int) []byte {
	b := []byte{0x10, 0, 0, 0x9d, 0x01, 0x2a, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(b[6:], uint16(width))
	binary.LittleEndian.PutUint16(b[8:], uint16(height))
	return b
}

// chunk returns a RIFF chunk, padded.
func chunk(id string, payload []byte) []byte {
	b := append([]byte(id), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(payload)))
	b = append(b, payload...)
	if len(payload)%2 != 0 {
		b = append(b, 0)
	}
	return b
}

// riff returns a WebP file of the chunks.
func riff(chunks ...[]byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WEBP")
	for _, c := range chunks {
		b = append(b, c...)
	}
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

// vp8xChunk returns a VP8X chunk.
func vp8xChunk(flags byte, width, height int) []byte {
	p := make([]byte, 10)
	p[0] = flags
	put24(p[4:], width-1)
	put24(p[7:], height-1)
	return chunk("VP8X", p)
}

// animChunk returns an ANIM chunk.
func animChunk(bg [4]byte, loops int) []byte {
	p := append(bg[:], 0, 0)
	binary.LittleEndian.PutUint16(p[4:], uint16(loops))
	return chunk("ANIM", p)
}

// anmfChunk returns an ANMF chunk with the subchunks; method has the B and D bits.
func anmfChunk(x, y, width, height, duration int, method byte, subchunks ...[]byte) []byte {
	p := make([]byte, 16)
	put24(p, x/2)
	put24(p[3:], y/2)
	put24(p[6:], width-1)
	put24(p[9:], height-1)
	put24(p[12:], duration)
	p[15] = method
	for _, c := range subchunks {
		p = append(p, c...)
	}
	return chunk("ANMF", p)
}

func put24(b []byte, v int) {
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16)
}
