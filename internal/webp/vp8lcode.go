package webp

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// bitReader reads a VP8L bitstream, least significant bit first (RFC 9649 section 3.2). Past the end of the data, it
// reads zeros, and n goes below 0.
type bitReader struct {
	data []byte
	pos  int    // of the next byte to load
	val  uint64 // the loaded bits from bit 0; above n, zeros or the bits that follow
	n    int    // the number of loaded bits not read yet
}

// fill loads bytes until n is over 55 or the data ends; n < 0 only once the data has ended.
func (br *bitReader) fill() {
	if br.pos+8 <= len(br.data) {
		br.val |= binary.LittleEndian.Uint64(br.data[br.pos:]) << uint(br.n)
		k := (63 - br.n) >> 3
		br.pos += k
		br.n += k << 3
		return
	}
	for br.n <= 56 && br.pos < len(br.data) {
		br.val |= uint64(br.data[br.pos]) << uint(br.n)
		br.pos++
		br.n += 8
	}
}

func (br *bitReader) eof() bool {
	return br.n < 0
}

// read returns the next n bits, n <= 32.
func (br *bitReader) read(n uint) uint32 {
	if br.n < int(n) {
		br.fill()
	}
	v := uint32(br.val & (1<<n - 1))
	br.val >>= n
	br.n -= int(n)
	return v
}

// readValue reads the extra bits of a length or distance prefix code (RFC 9649 section 3.6.2.2).
func (br *bitReader) readValue(code int) int {
	if code < 4 {
		return code + 1
	}
	extra := uint(code-2) >> 1
	return (2+code&1)<<extra + int(br.read(extra)) + 1
}

// A prefixCode decodes a canonical prefix code with a table indexed by the next rootBits bits. An entry is a symbol
// and its length, or for longer codes a link to a second table indexed by the bits after the first rootBits.
type prefixCode struct {
	table    []uint32
	rootBits uint
	mask     uint64 // of rootBits
}

const (
	maxCodeLength = 15
	maxRootBits   = 8
	entryLink     = 1 << 8      // the entry links to a second table
	entryShift    = 16          // of the symbol or the link's offset; the low 8 bits are the length or the link's bits
	maxAlphabet   = 280 + 1<<11 // green, length and color cache codes with the largest color cache
)

// symbol reads a symbol of c.
func (br *bitReader) symbol(c *prefixCode) int {
	if br.n < maxCodeLength {
		br.fill()
	}
	e := c.table[br.val&c.mask]
	if e&entryLink != 0 {
		br.val >>= c.rootBits
		br.n -= int(c.rootBits)
		e = c.table[int(e>>entryShift)+int(br.val&(1<<(e&0xff)-1))]
	}
	n := e & 0xff
	br.val >>= n
	br.n -= int(n)
	return int(e >> entryShift)
}

var (
	errEmptyCode      = errors.New("webp: VP8L: prefix code without symbols")
	errOversubscribed = errors.New("webp: VP8L: over-subscribed prefix code")
	errIncomplete     = errors.New("webp: VP8L: incomplete prefix code")
)

// checkLengths checks that code lengths describe a complete prefix code, or a single symbol (RFC 9649 section 3.7.2.1).
// It returns the number of symbols, the count of each length, and the last symbol.
func checkLengths(lengths []uint8) (n int, count [maxCodeLength + 1]int, last int, err error) {
	for s, l := range lengths {
		if l != 0 {
			count[l]++
			n++
			last = s
		}
	}
	switch n {
	case 0:
		return 0, count, 0, errEmptyCode
	case 1:
		return 1, count, last, nil
	}
	left := 1
	for l := 1; l <= maxCodeLength; l++ {
		left = left<<1 - count[l]
		if left < 0 {
			return 0, count, 0, errOversubscribed
		}
	}
	if left > 0 {
		return 0, count, 0, errIncomplete
	}
	return n, count, last, nil
}

// buildCode returns the prefix code with the code lengths, whose table it takes from alloc. A single symbol takes no
// bits.
func buildCode(lengths []uint8, alloc *tableAlloc) (prefixCode, error) {
	n, count, last, err := checkLengths(lengths)
	if err != nil {
		return prefixCode{}, err
	}
	if n == 1 {
		t := alloc.get(1)
		t[0] = uint32(last) << entryShift
		return prefixCode{table: t}, nil
	}
	maxLen := maxCodeLength
	for count[maxLen] == 0 {
		maxLen--
	}
	root := uint(min(maxLen, maxRootBits))

	// The first code of each length, as RFC 1951 section 3.2.2 assigns them, which VP8L also does.
	var next [maxCodeLength + 1]int
	code := 0
	for l := 1; l <= maxCodeLength; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	// The second tables: one for each first rootBits bits that longer codes share, as large as the longest of them.
	var subBits [1 << maxRootBits]uint8
	codes := next
	for _, l := range lengths {
		if uint(l) > root {
			p := codes[l] >> (uint(l) - root)
			subBits[p] = max(subBits[p], l-uint8(root))
		}
		codes[l]++
	}
	size := 1 << root
	var offsets [1 << maxRootBits]int
	for p, b := range subBits {
		if b != 0 {
			offsets[p] = size
			size += 1 << b
		}
	}
	t := alloc.get(size)
	codes = next
	for s, l := range lengths {
		if l == 0 {
			continue
		}
		c := codes[l]
		codes[l]++
		e := uint32(s)<<entryShift | uint32(l)
		if uint(l) <= root {
			for i := reverse(c, uint(l)); i < 1<<root; i += 1 << l {
				t[i] = e
			}
			continue
		}
		rest := uint(l) - root
		p := c >> rest
		b := subBits[p]
		t[reverse(p, root)] = uint32(offsets[p])<<entryShift | entryLink | uint32(b)
		sub := t[offsets[p] : offsets[p]+1<<b]
		e = uint32(s)<<entryShift | uint32(rest)
		for i := reverse(c&(1<<rest-1), rest); i < len(sub); i += 1 << rest {
			sub[i] = e
		}
	}
	return prefixCode{table: t, rootBits: root, mask: 1<<root - 1}, nil
}

// reverse returns the n low bits of c in reverse order: the stream has the first bit of a code at its least
// significant bit.
func reverse(c int, n uint) int {
	return int(bits.Reverse16(uint16(c)) >> (16 - n))
}

// tableAlloc hands out tables from larger blocks.
type tableAlloc struct {
	buf []uint32
}

func (a *tableAlloc) get(n int) []uint32 {
	if len(a.buf) < n {
		a.buf = make([]uint32, max(n, 1<<14))
	}
	t := a.buf[:n:n]
	a.buf = a.buf[n:]
	return t
}
