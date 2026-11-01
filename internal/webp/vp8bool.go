package webp

import (
	"encoding/binary"
	"math/bits"
)

// boolDecoder reads the bools of a partition of a VP8 frame (RFC 6386 section 7). Past the end of the partition it
// reads zeros and sets eof, which libwebp treats as an error: eof is set when the 8 bits that a read compares need data
// after the partition, or when the partition is empty.
type boolDecoder struct {
	data  []byte // the bytes not loaded yet
	value uint64 // the n loaded bits not read yet, in the low bits; below rng << (n-8)
	n     int
	rng   uint32 // 128 to 255 between reads
	eof   bool
}

func (d *boolDecoder) init(data []byte) {
	*d = boolDecoder{data: data, rng: 255, eof: len(data) == 0}
}

// load loads at least 8 bits; n is below 8.
func (d *boolDecoder) load() {
	switch {
	case len(d.data) >= 8:
		d.value = d.value<<56 | binary.BigEndian.Uint64(d.data)>>8
		d.n += 56
		d.data = d.data[7:]
		return
	case len(d.data) > 0:
		d.value = d.value<<8 | uint64(d.data[0])
		d.data = d.data[1:]
	default:
		d.value <<= 8
		d.eof = true
	}
	d.n += 8
}

// readBool reads a bool whose probability of being false is prob/256.
func (d *boolDecoder) readBool(prob uint8) bool {
	if d.n < 8 {
		d.load()
	}
	split := 1 + (d.rng-1)*uint32(prob)>>8
	s := uint64(split) << uint(d.n-8)
	bit := d.value >= s
	if bit {
		d.rng -= split
		d.value -= s
	} else {
		d.rng = split
	}
	shift := bits.LeadingZeros32(d.rng) - 24
	d.rng <<= uint(shift)
	d.n -= shift
	return bit
}

// readFlag reads a bool of probability 1/2, L(1) in RFC 6386.
func (d *boolDecoder) readFlag() bool {
	return d.readBool(128)
}

// readLiteral reads an unsigned number of n bits, most significant first, L(n) in RFC 6386.
func (d *boolDecoder) readLiteral(n int) int {
	v := 0
	for range n {
		v <<= 1
		if d.readFlag() {
			v |= 1
		}
	}
	return v
}

// readSigned reads a magnitude of n bits and a sign bit.
func (d *boolDecoder) readSigned(n int) int {
	v := d.readLiteral(n)
	if d.readFlag() {
		return -v
	}
	return v
}

// readOptionalSigned reads a flag and, if it is set, a signed number of n bits; it returns 0 if the flag is not set.
func (d *boolDecoder) readOptionalSigned(n int) int {
	if !d.readFlag() {
		return 0
	}
	return d.readSigned(n)
}

// readTree reads a value coded with a tree of RFC 6386 section 8.1 and its probabilities.
func (d *boolDecoder) readTree(tree []int8, probs []uint8) int {
	i := 0
	for {
		next := 2 * i
		if d.readBool(probs[i]) {
			next++
		}
		v := int(tree[next])
		if v <= 0 {
			return -v
		}
		i = v / 2
	}
}
