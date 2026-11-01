package webp

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func TestBitReader(t *testing.T) {
	br := bitReader{data: []byte{0b10110100, 0xff, 0x01}}
	for _, r := range []struct {
		n    uint
		want uint32
	}{{1, 0}, {2, 0b10}, {0, 0}, {5, 0b10110}, {8, 0xff}, {8, 1}} {
		if got := br.read(r.n); got != r.want || br.eof() {
			t.Fatalf("read(%d) = %#b, eof %t; want %#b", r.n, got, br.eof(), r.want)
		}
	}
	if got := br.read(3); got != 0 || !br.eof() {
		t.Errorf("read past the end = %d, eof %t; want 0 and eof", got, br.eof())
	}

	// Words loaded at once, and then bytes near the end.
	var w bitWriter
	rng := rand.New(rand.NewPCG(1, 2))
	var ns []uint
	var vs []int
	for range 1000 {
		n := uint(rng.IntN(33))
		v := int(rng.Uint32()) & (1<<n - 1)
		w.write(v, n)
		ns, vs = append(ns, n), append(vs, v)
	}
	br = bitReader{data: w.bytes()}
	for i, n := range ns {
		if got := br.read(n); int(got) != vs[i] {
			t.Fatalf("read %d: %#x, want %#x", i, got, vs[i])
		}
	}
	if br.eof() {
		t.Error("eof before the end")
	}
}

func TestBuildCode(t *testing.T) {
	for _, tt := range []struct {
		lengths []uint8
		err     error
		codes   map[int]string // symbol: its bits in the order of the stream
	}{
		{lengths: []uint8{0, 0, 0}, err: errEmptyCode},
		{lengths: []uint8{0, 1, 0}, codes: map[int]string{1: ""}},
		{lengths: []uint8{0, 0, 7}, codes: map[int]string{2: ""}},
		{lengths: []uint8{0, 1, 0, 1}, codes: map[int]string{1: "0", 3: "1"}},
		{lengths: []uint8{2, 1, 2}, codes: map[int]string{1: "0", 0: "10", 2: "11"}},
		{lengths: []uint8{3, 3, 2, 2, 2}, codes: map[int]string{2: "00", 3: "01", 4: "10", 0: "110", 1: "111"}},
		{lengths: []uint8{1, 1, 1}, err: errOversubscribed},
		{lengths: []uint8{1, 2}, err: errIncomplete},
		{lengths: []uint8{2, 2, 2}, err: errIncomplete},
		{lengths: []uint8{15, 15}, err: errIncomplete},
		{lengths: []uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, err: errIncomplete},
		{lengths: []uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 15, 15}, err: errOversubscribed},
	} {
		c, err := buildCode(tt.lengths, &tableAlloc{})
		if !errors.Is(err, tt.err) {
			t.Errorf("%v: %v, want %v", tt.lengths, err, tt.err)
			continue
		}
		for s, code := range tt.codes {
			// The code, then ones: a code must read only its own bits.
			var w bitWriter
			for _, b := range code + "111111111111111" {
				w.write(int(b-'0'), 1)
			}
			br := bitReader{data: w.bytes()}
			got := br.symbol(&c)
			if n := 8*br.pos - br.n; got != s || n != len(code) || br.eof() {
				t.Errorf("%v: %q gives symbol %d after %d bits; want %d after %d", tt.lengths, code, got, n, s,
					len(code))
			}
		}
	}
}

// randomLengths returns the code lengths of a random complete prefix code with k symbols out of n, at most 15 bits
// long: leaves of a binary tree that is split at random.
func randomLengths(rng *rand.Rand, n, k int) []uint8 {
	depths := []uint8{0}
	for len(depths) < k {
		i := rng.IntN(len(depths))
		if depths[i] == maxCodeLength {
			continue
		}
		depths[i]++
		depths = append(depths, depths[i])
	}
	lengths := make([]uint8, n)
	for i, s := range rng.Perm(n)[:k] {
		lengths[s] = depths[i]
	}
	return lengths
}

// TestPrefixCodeRoundTrip writes random symbols with random codes, long ones too, and reads them back.
func TestPrefixCodeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for i := range 300 {
		n := []int{2, 19, 40, 256, 280, maxAlphabet}[i%6]
		lengths := randomLengths(rng, n, 2+rng.IntN(n-1))
		if i%3 == 0 {
			// Deep codes: split the deepest leaves.
			lengths = make([]uint8, n)
			k := min(n, 16)
			for j := range k - 1 {
				lengths[j] = uint8(j + 1)
			}
			lengths[k-1] = uint8(k - 1)
		}
		c, err := buildCode(lengths, &tableAlloc{})
		if err != nil {
			t.Fatalf("%v: %v", lengths, err)
		}
		tc := newTestCode(lengths)
		var used []int
		for s, l := range lengths {
			if l != 0 {
				used = append(used, s)
			}
		}
		var w bitWriter
		var want []int
		for range 500 {
			s := used[rng.IntN(len(used))]
			w.symbol(&tc, s)
			want = append(want, s)
		}
		br := bitReader{data: w.bytes()}
		for j, s := range want {
			if got := br.symbol(&c); got != s {
				t.Fatalf("code %d, symbol %d: %d, want %d", i, j, got, s)
			}
		}
		if br.eof() {
			t.Fatalf("code %d: read past the end", i)
		}
	}
}

// refPixel is a pixel as its alpha, red, green and blue.
type refPixel [4]int

func channels(v uint32) refPixel {
	return refPixel{int(v >> 24), int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)}
}

func (p refPixel) argb() uint32 {
	return uint32(p[0]&0xff)<<24 | uint32(p[1]&0xff)<<16 | uint32(p[2]&0xff)<<8 | uint32(p[3]&0xff)
}

func eachChannel(f func(c int) int) refPixel {
	return refPixel{f(0), f(1), f(2), f(3)}
}

func refAverage2(a, b refPixel) refPixel {
	return eachChannel(func(c int) int { return (a[c] + b[c]) / 2 })
}

func refClamp(v int) int { return min(max(v, 0), 255) }

// refPredict is Table 2 of RFC 9649 section 3.5.1, written out channel by channel, with libwebp's modes 14 and 15.
func refPredict(mode int, l, t, tl, tr refPixel) refPixel {
	switch mode {
	case 1:
		return l
	case 2:
		return t
	case 3:
		return tr
	case 4:
		return tl
	case 5:
		return refAverage2(refAverage2(l, tr), t)
	case 6:
		return refAverage2(l, tl)
	case 7:
		return refAverage2(l, t)
	case 8:
		return refAverage2(tl, t)
	case 9:
		return refAverage2(t, tr)
	case 10:
		return refAverage2(refAverage2(l, tl), refAverage2(t, tr))
	case 11:
		p := eachChannel(func(c int) int { return l[c] + t[c] - tl[c] })
		pl, pt := 0, 0
		for c := range 4 {
			pl += abs(p[c] - l[c])
			pt += abs(p[c] - t[c])
		}
		if pl < pt {
			return l
		}
		return t
	case 12:
		return eachChannel(func(c int) int { return refClamp(l[c] + t[c] - tl[c]) })
	case 13:
		a := refAverage2(l, t)
		return eachChannel(func(c int) int { return refClamp(a[c] + (a[c]-tl[c])/2) })
	}
	return channels(0xff000000)
}

func randomPixels(rng *rand.Rand, n int) []byte {
	b := make([]byte, 4*n)
	for i := range b {
		// Extremes often, to reach the clamping.
		switch rng.IntN(4) {
		case 0:
			b[i] = 0
		case 1:
			b[i] = 255
		default:
			b[i] = byte(rng.Uint32())
		}
	}
	return b
}

func TestInversePredictor(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for _, sz := range [][3]int{{1, 1, 2}, {1, 5, 2}, {5, 1, 2}, {5, 4, 2}, {9, 7, 2}, {17, 9, 3}, {3, 3, 9}} {
		w, h, bits := sz[0], sz[1], sz[2]
		modes := randomPixels(rng, divRoundUp(w, bits)*divRoundUp(h, bits))
		for i := 1; i < len(modes); i += 4 {
			// Every mode, 14 and 15 and green above 15 too.
			modes[i] = byte(rng.IntN(16) + 16*rng.IntN(2))
		}
		res := randomPixels(rng, w*h)
		want := make([]uint32, w*h)
		for y := range h {
			for x := range w {
				i := y*w + x
				var pred refPixel
				switch {
				case x == 0 && y == 0:
					pred = channels(0xff000000)
				case y == 0:
					pred = channels(want[i-1])
				case x == 0:
					pred = channels(want[i-w])
				default:
					m := int(modes[4*((y>>bits)*divRoundUp(w, bits)+x>>bits)+1]) & 15
					tr := (y-1)*w + x + 1
					if x == w-1 {
						tr = y * w
					}
					pred = refPredict(m, channels(want[i-1]), channels(want[i-w]), channels(want[i-w-1]),
						channels(want[tr]))
				}
				r := channels(pixel(res, i))
				want[i] = eachChannel(func(c int) int { return r[c] + pred[c] }).argb()
			}
		}
		pix := slices.Clone(res)
		inversePredictor(pix, w, h, bits, modes)
		for i, v := range want {
			if got := pixel(pix, i); got != v {
				t.Fatalf("%dx%d, %d bits: pixel %d is %#08x, want %#08x", w, h, bits, i, got, v)
			}
		}
	}
}

func TestInverseColor(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	w, h, bits := 11, 6, 2
	elems := randomPixels(rng, divRoundUp(w, bits)*divRoundUp(h, bits))
	pix := randomPixels(rng, w*h)
	want := make([]uint32, w*h)
	delta := func(t, c int) int { return int(int8(t)) * int(int8(c)) >> 5 }
	for y := range h {
		for x := range w {
			e := channels(pixel(elems, (y>>bits)*divRoundUp(w, bits)+x>>bits))
			greenToRed, greenToBlue, redToBlue := e[3], e[2], e[1]
			p := channels(pixel(pix, y*w+x))
			red := p[1] + delta(greenToRed, p[2])
			blue := p[3] + delta(greenToBlue, p[2]) + delta(redToBlue, red&0xff)
			want[y*w+x] = refPixel{p[0], red, p[2], blue}.argb()
		}
	}
	inverseColor(pix, w, h, bits, elems)
	for i, v := range want {
		if got := pixel(pix, i); got != v {
			t.Fatalf("pixel %d is %#08x, want %#08x", i, got, v)
		}
	}
}

func TestAddGreen(t *testing.T) {
	pix := []byte{0x10, 0xf0, 0x20, 0x33, 0xff, 0x01, 0x00, 0x44}
	addGreen(pix)
	if want := []byte{0x00, 0xf0, 0x10, 0x33, 0x00, 0x01, 0x01, 0x44}; !bytes.Equal(pix, want) {
		t.Errorf("% x, want % x", pix, want)
	}
}

func TestInverseIndexing(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	for _, n := range []int{1, 2, 3, 4, 5, 16, 17, 256} {
		for _, sz := range [][2]int{{1, 1}, {7, 3}, {8, 2}, {9, 3}, {33, 4}} {
			w, h := sz[0], sz[1]
			var palette [256]uint32
			for i := range n {
				palette[i] = rng.Uint32()
			}
			bits := widthBits(n)
			packed := randomPixels(rng, divRoundUp(w, bits)*h)
			want := make([]uint32, w*h)
			for y := range h {
				for x := range w {
					g := int(packed[4*(y*divRoundUp(w, bits)+x>>bits)+1])
					size := 8 >> bits
					idx := g >> (x % (1 << bits) * size) % (1 << size)
					if idx < n {
						want[y*w+x] = palette[idx]
					}
				}
			}
			pix := make([]byte, 4*w*h)
			copy(pix, packed)
			inverseIndexing(pix, w, h, bits, &palette)
			for i, v := range want {
				if got := pixel(pix, i); got != v {
					t.Fatalf("%d colors, %dx%d: pixel %d is %#08x, want %#08x", n, w, h, i, got, v)
				}
			}
		}
	}
}

func TestDistance(t *testing.T) {
	for _, tt := range []struct{ code, w, want int }{
		{1, 10, 10}, {2, 10, 1}, {3, 10, 11}, {4, 10, 9}, {120, 10, 78}, {121, 10, 1}, {1000, 10, 880},
		{4, 1, 1}, {10, 1, 1}, {10, 2, 1}, {10, 3, 1}, {120, 8, 64},
	} {
		if got := distance(tt.code, tt.w); got != tt.want {
			t.Errorf("distance(%d, %d) = %d, want %d", tt.code, tt.w, got, tt.want)
		}
	}
	seen := make(map[[2]int8]bool)
	for i, o := range distanceMap {
		x, y := o[0], o[1]
		if seen[o] || x < -7 || x > 8 || y < 0 || y > 7 || y == 0 && x < 1 {
			t.Errorf("code %d: (%d, %d)", i+1, x, y)
		}
		seen[o] = true
	}
	if len(seen) != 120 {
		t.Errorf("%d distinct offsets", len(seen))
	}
}

// TestColorCache checks the color cache index of RFC 9649 section 3.6.2.3: cache hits must give the colors inserted
// at their hashes.
func TestColorCache(t *testing.T) {
	for _, bits := range []int{1, 4, 5, 6, 10, 11} {
		rng := rand.New(rand.NewPCG(11, uint64(bits)))
		var tokens []vp8lToken
		var want []uint32
		cache := make(map[int]uint32)
		for range 40 {
			if len(cache) > 0 && rng.IntN(2) == 0 {
				keys := slices.Sorted(func(yield func(int) bool) {
					for k := range cache {
						if !yield(k) {
							return
						}
					}
				})
				k := keys[rng.IntN(len(keys))]
				tokens = append(tokens, hit(k))
				want = append(want, cache[k])
				continue
			}
			v := rng.Uint32()
			tokens = append(tokens, lit(v))
			want = append(want, v)
			cache[cacheIndex(v, bits)] = v
		}
		var w bitWriter
		w.header(len(want), 1, true)
		w.write(0, 1)
		w.imageData(bits, true, tokens)
		checkVP8L(t, w.bytes(), want)
	}
}

// cacheIndex is the index of a color in a color cache of 1<<bits colors, as RFC 9649 section 3.6.2.3 writes it.
func cacheIndex(argb uint32, bits int) int {
	return int(uint64(0x1e35a7bd) * uint64(argb) % (1 << 32) >> (32 - bits))
}

// paletteDeltas returns the color table of color indexing for the colors, as differences.
func paletteDeltas(colors ...uint32) []uint32 {
	d := slices.Clone(colors)
	for i := len(d) - 1; i > 0; i-- {
		a, b := channels(d[i]), channels(d[i-1])
		d[i] = eachChannel(func(c int) int { return a[c] - b[c] }).argb()
	}
	return d
}

// checkVP8L decodes a VP8L bitstream and checks its pixels.
func checkVP8L(t *testing.T, data []byte, want []uint32) {
	t.Helper()
	pix, _, _, err := decodeVP8L(data)
	if err != nil {
		t.Fatal(err)
	}
	var b []byte
	for _, v := range want {
		b = append(b, byte(v>>16), byte(v>>8), byte(v), byte(v>>24))
	}
	if !bytes.Equal(pix, b) {
		t.Errorf("RGBA % x, want % x", pix, b)
	}
}

// edgeStreams returns VP8L bitstreams of edge cases with the RGBA bytes that dwebp decodes from them, in hex, or
// "error".
func edgeStreams() []struct {
	name string
	data []byte
	want string
} {
	type stream = struct {
		name string
		data []byte
		want string
	}
	var s []stream
	add := func(name, want string, f func(w *bitWriter)) {
		var w bitWriter
		f(&w)
		s = append(s, stream{name, w.bytes(), want})
	}
	residuals := lits(0x10203040, 0x01020304, 0xfffefdfc, 0x05060708, 0x80808080, 0x7f7f7f7f, 0x11111111, 0x00ff00ff,
		0x12345678)
	for _, m := range []struct {
		green int
		want  string
	}{
		{0, "2030400f223344102030400f263748148080807f7f7f7f7e37485925ff00ffff34567811"},
		{11, "2030400f223344102030400f26374814a6b7c8942536471337485925a5b7c794598cbf25"},
		{13, "2030400f223344102030400f26374814a6b7c993021223f03748592591a3b4804f82b4dc"},
		{14, "2030400f223344102030400f263748148080807f7f7f7f7e37485925ff00ffff34567811"},
		{15, "2030400f223344102030400f263748148080807f7f7f7f7e37485925ff00ffff34567811"},
		{17, "2030400f223344102030400f26374814a6b7c8942536471337485925364858256a9ed037"},
		{29, "2030400f223344102030400f26374814a6b7c993021223f03748592591a3b4804f82b4dc"},
		{255, "2030400f223344102030400f263748148080807f7f7f7f7e37485925ff00ffff34567811"},
	} {
		add(fmt.Sprintf("predictor of green %d", m.green), m.want, func(w *bitWriter) {
			w.header(3, 3, true)
			w.write(1, 1)
			w.write(predictorTransform, 2)
			w.write(0, 3)
			w.subimage(uint32(m.green) << 8)
			w.write(0, 1)
			w.imageData(0, true, residuals)
		})
	}
	add("simple code symbol past the alphabet", "414042ff414042ff414042ff414042ff415042ff", func(w *bitWriter) {
		w.header(5, 1, false)
		w.write(0, 3)
		g := w.code(280, 0x40, 0x50, 257)
		w.code(256, 0x41)
		w.code(256, 0x42)
		w.code(256, 0xff)
		// Distance symbols 1 and 200, of which libwebp keeps 1, which then takes no bits.
		w.write(0b1011, 4)
		w.write(200, 8)
		w.symbol(&g, 0x40)
		w.symbol(&g, 0x40)
		w.symbol(&g, 257)
		w.symbol(&g, 0x50)
	})
	add("only symbol past the alphabet", "error", func(w *bitWriter) {
		w.header(1, 1, true)
		w.write(0, 3)
		w.code(280, 0x20)
		w.code(256, 0x30)
		w.code(256, 0x40)
		w.code(256, 0xff)
		w.write(0b101, 3)
		w.write(100, 8)
	})
	add("transparent without alpha_is_used", "1020308040506000", func(w *bitWriter) {
		w.header(2, 1, false)
		w.write(0, 1)
		w.imageData(0, true, lits(0x80102030, 0x00405060))
	})
	add("indexes past 3 colors, bundled", "112233ff12233400142536020000000000000000", func(w *bitWriter) {
		w.header(5, 1, true)
		w.write(0b111, 3)
		w.write(3-1, 8)
		w.subimage(0xff112233, 0x01010101, 0x02020202)
		w.write(0, 1)
		w.imageData(0, true, lits(0b11100100<<8, 0b11<<8))
	})
	add("indexes past 20 colors", "0c121806283c501400000000", func(w *bitWriter) {
		w.header(3, 1, true)
		w.write(0b111, 3)
		w.write(20-1, 8)
		pal := make([]uint32, 20)
		for i := range pal {
			pal[i] = 0x01020304
		}
		w.subimage(pal...)
		w.write(0, 1)
		w.imageData(0, true, lits(5<<8, 19<<8, 200<<8))
	})
	add("code 16 first repeats 8", "a51030ff5a1030ff", func(w *bitWriter) {
		w.header(2, 1, true)
		w.write(0, 3)
		g := w.code(280, 0x10)
		// Red: a code length code of only 16, then 42 repeats of 6 and one of 4: 256 lengths of 8.
		w.write(0, 1)
		w.write(9-4, 4)
		for _, s := range codeLengthOrder[:9] {
			w.write(btoi(s == 16), 3)
		}
		w.write(0, 1)
		for range 42 {
			w.write(3, 2)
		}
		w.write(1, 2)
		red := newTestCode(bytes.Repeat([]byte{8}, 256))
		w.code(256, 0x30)
		w.code(256, 0xff)
		w.code(40)
		w.symbol(&g, 0x10)
		w.symbol(&red, 0xa5)
		w.symbol(&g, 0x10)
		w.symbol(&red, 0x5a)
	})
	add("max_symbol counts repeats once", "200330ff200430ff", func(w *bitWriter) {
		w.header(2, 1, true)
		w.write(0, 3)
		// Green: a code length code of 17 and 1, max_symbol 3, then 17 (3 zeros), 1, 1.
		w.write(0, 1)
		w.write(0, 4)
		w.write(1, 3)
		w.write(0, 3)
		w.write(0, 3)
		w.write(1, 3)
		w.write(1, 1)
		w.write(0, 3)
		w.write(1, 2)
		cl := make([]uint8, 19)
		cl[17], cl[1] = 1, 1
		c := newTestCode(cl)
		w.symbol(&c, 17)
		w.write(0, 3)
		w.symbol(&c, 1)
		w.symbol(&c, 1)
		gl := make([]uint8, 280)
		gl[3], gl[4] = 1, 1
		g := newTestCode(gl)
		w.code(256, 0x20)
		w.code(256, 0x30)
		w.code(256, 0xff)
		w.code(40)
		w.symbol(&g, 3)
		w.symbol(&g, 4)
	})
	add("cache hit of an empty entry inserts 0", "000000ff0000000000000000", func(w *bitWriter) {
		w.header(3, 1, true)
		w.write(0, 1)
		// 0xff000000 is at 0 in a cache of 1 bit.
		w.imageData(1, true, []vp8lToken{lit(0xff000000), hit(1), hit(0)})
	})
	add("simple code with the same symbol twice", "200730ff200730ff", func(w *bitWriter) {
		w.header(2, 1, true)
		w.write(0, 3)
		w.write(0b111, 3)
		w.write(7, 8)
		w.write(7, 8)
		w.code(256, 0x20)
		w.code(256, 0x30)
		w.code(256, 0xff)
		w.code(40)
	})
	add("normal code of one symbol of length 5", "200930ff200930ff", func(w *bitWriter) {
		w.header(2, 1, true)
		w.write(0, 3)
		l := make([]uint8, 280)
		l[9] = 5
		w.normalCode(l)
		w.code(256, 0x20)
		w.code(256, 0x30)
		w.code(256, 0xff)
		w.code(40)
	})
	add("distances under 1 and over 120", "102030ff102030ff102030ff405060ff405060ff405060ff", func(w *bitWriter) {
		w.header(1, 6, true)
		w.write(0, 1)
		w.imageData(0, true, []vp8lToken{lit(0xff102030), ref(2, 4), lit(0xff405060), ref(2, 121)})
	})
	add("overlapping copy with a color cache", "102030ff405060804050608040506080405060804050608040506080",
		func(w *bitWriter) {
			w.header(7, 1, true)
			w.write(0, 1)
			w.imageData(2, true, []vp8lToken{lit(0xff102030), lit(0x80405060), ref(5, 2)})
		})
	add("subtract green, color transform and predictor of modes 11 and 13",
		"3b5afe599d5b589e9947d802c28f850973c6822fa3ed3a1fe26b4a00fc21818247a21a259333257d34b5e681d169488e12342faef0a1"+
			"70f428a888ee74bb980f44a8eeb8b4f1aa00b2d52f5c3c5cd289", func(w *bitWriter) {
			w.header(5, 4, true)
			w.write(0b101, 3)
			w.write(0b011, 3)
			w.write(0, 3)
			w.subimage(0xff102030, 0xff80c0f0)
			w.write(0b001, 3)
			w.write(0, 3)
			w.subimage(11<<8, 13<<8)
			w.write(0, 1)
			pix := make([]uint32, 20)
			for i := range pix {
				pix[i] = uint32(i*0x1f3a5b7d) ^ 0x5a5a5a5a
			}
			w.imageData(0, true, lits(pix...))
		})
	add("8 pixels bundled, 11 wide",
		"10203080112233ff1020308010203080112233ff112233ff10203080112233ff112233ff10203080112233ff10203080102030801020"+
			"308010203080112233ff112233ff112233ff112233ff112233ff112233ff10203080", func(w *bitWriter) {
			w.header(11, 2, true)
			w.write(0b111, 3)
			w.write(2-1, 8)
			w.subimage(0x80102030, 0x7f010203)
			w.write(0, 1)
			w.imageData(0, true, lits(0b10110010<<8, 0b101<<8, 0b11110000<<8, 0b011<<8))
		})
	add("color indexing, then a predictor on the packed image",
		"000000ff000000ff000000ff000000ff102030ff102030ff305070ff000000ff315171003151710031517100305070ff102030ff3151"+
			"7100000000ff000000ff000000ff000000ff102030ff31517100000000ff102030ff000000ff000000ff102030ff102030ff"+
			"000000ff",
		func(w *bitWriter) {
			w.header(9, 3, true)
			w.write(0b111, 3)
			w.write(4-1, 8)
			w.subimage(0xff000000, 0x00102030, 0x00203040, 0x01010101)
			w.write(0b001, 3)
			w.write(0, 3)
			w.subimage(1 << 8)
			w.write(0, 1)
			pix := make([]uint32, 3*3)
			for i := range pix {
				pix[i] = uint32(i*37) << 8
			}
			w.imageData(0, true, lits(pix...))
		})
	add("transform used twice", "error", func(w *bitWriter) {
		w.header(1, 1, true)
		w.write(0b101, 3)
		w.write(0b101, 3)
		w.write(0, 1)
		w.imageData(0, true, lits(0xff102030))
	})
	return s
}

func TestVP8LEdgeCases(t *testing.T) {
	for _, s := range edgeStreams() {
		pix, _, _, err := decodeVP8L(s.data)
		got := hex.EncodeToString(pix)
		if err != nil {
			got = "error"
		}
		if got != s.want {
			t.Errorf("%s: %s (%v), want %s", s.name, got, err, s.want)
		}
	}
}

// TestVP8LStreams decodes hand-made bitstreams of features that the vectors lack, and longer chains of transforms.
func TestVP8LStreams(t *testing.T) {
	// A color cache in a subimage, the color table, of each size; 4 pixels bundled in one.
	for bits := 1; bits <= 11; bits++ {
		var w bitWriter
		w.header(4, 1, true)
		w.write(0b111, 3)
		w.write(2-1, 8)
		w.imageData(bits, false, []vp8lToken{lit(0xff102030), hit(cacheIndex(0xff102030, bits))})
		w.write(0, 1)
		w.imageData(bits, true, lits(0b0110<<8))
		checkVP8L(t, w.bytes(), []uint32{0xff102030, 0xfe204060, 0xfe204060, 0xff102030})
	}

	// An entropy image whose meta prefix codes 0, 1 and 3 are not used.
	{
		var w bitWriter
		w.header(8, 1, true)
		w.write(0, 2)
		w.write(1, 1)
		w.write(0, 3)
		w.subimage(0x0400, 0x0200) // the blocks of 4 pixels use meta codes 4 and 2
		groups := [][]vp8lToken{
			lits(1), lits(2, 3), lits(4), lits(5, 6), lits(0xff000004, 0xff000005),
		}
		var codes [][5]testCode
		for _, g := range groups {
			codes = append(codes, w.group(0, g))
		}
		for _, v := range []uint32{0xff000004, 0xff000005, 0xff000004, 0xff000004} {
			w.tokens(&codes[4], lits(v))
		}
		w.tokens(&codes[2], lits(4, 4, 4, 4))
		checkVP8L(t, w.bytes(), []uint32{0xff000004, 0xff000005, 0xff000004, 0xff000004, 4, 4, 4, 4})
	}

	// A backward reference to the last pixel of an image with an entropy image whose blocks end with the image.
	{
		var w bitWriter
		w.header(4, 4, true)
		w.write(0, 2)
		w.write(1, 1)
		w.write(0, 3)
		w.subimage(0)
		tokens := []vp8lToken{lit(0xff102030), ref(15, 2)}
		g := w.group(0, tokens)
		w.tokens(&g, tokens)
		want := make([]uint32, 16)
		for i := range want {
			want[i] = 0xff102030
		}
		checkVP8L(t, w.bytes(), want)
	}

	// Every transform: color indexing, subtract green, color transform and predictor. All but the first work on the
	// packed image of 3x2 pixels, each 0x0000e400 (indexes 0, 1, 2 and 3) before the first. Subtract green makes it
	// 0x001ce41c, and the color transform with green_to_red 1, green_to_blue 2 and red_to_blue 3 0x001de41c. The
	// predictor of L leaves one residual.
	{
		colors := []uint32{0xff000000, 0xff102030, 0xff304070, 0xff4060a0}
		var w bitWriter
		w.header(9, 2, true)
		w.write(0b111, 3)
		w.write(len(colors)-1, 8)
		w.subimage(paletteDeltas(colors...)...)
		w.write(0b101, 3)
		w.write(0b011, 3)
		w.write(0, 3)
		w.subimage(0x00030201)
		w.write(0b001, 3)
		w.write(0, 3)
		w.subimage(1 << 8)
		w.write(0, 1)
		w.imageData(0, true, lits(0x011de41c, 0, 0, 0, 0, 0))
		row := slices.Concat(colors, colors, colors[:1])
		checkVP8L(t, w.bytes(), slices.Concat(row, row))
	}
}

func TestVP8LErrors(t *testing.T) {
	pix := make([]uint32, 30)
	rng := rand.New(rand.NewPCG(13, 14))
	for i := range pix {
		pix[i] = rng.Uint32()
	}
	valid := vp8lImage(6, 5, pix...)
	if _, _, _, err := decodeVP8L(valid); err != nil {
		t.Fatal(err)
	}
	for n := range len(valid) - 1 {
		if _, _, _, err := decodeVP8L(valid[:n]); err == nil {
			t.Errorf("no error with %d bytes of %d", n, len(valid))
		}
	}

	streams := map[string]func(w *bitWriter){
		"color cache of 0 bits": func(w *bitWriter) {
			w.write(0, 1)
			w.write(1, 1)
			w.write(0, 4)
		},
		"color cache of 12 bits": func(w *bitWriter) {
			w.write(0, 1)
			w.write(1, 1)
			w.write(12, 4)
		},
		"reference before the start": func(w *bitWriter) {
			w.write(0, 1)
			w.imageData(0, true, []vp8lToken{ref(1, 2)})
		},
		"reference past the end": func(w *bitWriter) {
			w.write(0, 1)
			w.imageData(0, true, []vp8lToken{lit(0), lit(0), ref(5, 2)})
		},
		"max_symbol over the alphabet": func(w *bitWriter) {
			w.write(0, 3)
			w.write(0, 1)
			w.write(0, 4)
			w.write(0, 3)
			w.write(0, 3)
			w.write(1, 3)
			w.write(1, 3)
			w.write(1, 1)
			w.write(7, 3)
			w.write(0xffff, 16)
		},
		"repeat past the alphabet": func(w *bitWriter) {
			w.write(0, 3)
			for range 4 {
				w.code(256)
			}
			// Distance: code length codes 18 and 1, then 18 (bit 1) with 11 + 127 zeros.
			w.write(0, 1)
			w.write(0, 4)
			w.write(0, 3)
			w.write(1, 3)
			w.write(0, 3)
			w.write(1, 3)
			w.write(0, 1)
			w.write(1, 1)
			w.write(127, 7)
		},
		"incomplete code length code": func(w *bitWriter) {
			w.write(0, 3)
			w.write(0, 1)
			w.write(0, 4)
			w.write(1, 3)
			w.write(2, 3)
			w.write(0, 3)
			w.write(0, 3)
		},
		"over-subscribed code": func(w *bitWriter) {
			w.write(0, 3)
			w.normalCode(append([]uint8{1, 1, 1}, make([]uint8, 277)...))
		},
	}
	for name, f := range streams {
		var w bitWriter
		w.header(3, 1, true)
		f(&w)
		w.write(0, 64)
		if _, _, _, err := decodeVP8L(w.bytes()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	for _, h := range [][]byte{{}, {0x2f, 0, 0, 0}, {0x2e, 0, 0, 0, 0}, {0x2f, 0, 0, 0, 0x20}, {0x2f, 0, 0, 0, 0xe0}} {
		if _, _, _, err := vp8lHeader(h); err == nil {
			t.Errorf("vp8lHeader(% x): no error", h)
		}
		if _, _, _, err := decodeVP8L(h); err == nil {
			t.Errorf("decodeVP8L(% x): no error", h)
		}
	}
}

// TestVP8LCoverage decodes the lossless bitstreams of the vectors, counting what the decoder meets, and requires that
// they have every feature of the format that encoders write.
func TestVP8LCoverage(t *testing.T) {
	var st vp8lStats
	for _, v := range loadVectors(t) {
		f, err := Parse(readVector(t, v))
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		for i, fr := range f.Frames {
			if !fr.lossless {
				continue
			}
			if _, err := decodeVP8LStream(fr.bitstream[5:], fr.Width, fr.Height, &st); err != nil {
				t.Errorf("%s: frame %d: %v", v.Name, i, err)
			}
		}
	}
	t.Logf("%+v", st)
	var missing []string
	need := func(ok bool, what string) {
		if !ok {
			missing = append(missing, what)
		}
	}
	for i, n := range st.transforms {
		need(n > 0, fmt.Sprintf("transform %d", i))
	}
	for m, n := range st.predictorModes[:14] {
		need(n > 0, fmt.Sprintf("predictor mode %d", m))
	}
	sizes := 0
	for _, n := range st.cacheBits[1:] {
		if n > 0 {
			sizes++
		}
	}
	need(st.cacheBits[0] > 0, "images without a color cache")
	need(sizes >= 3, "3 sizes of color cache")
	for b, n := range st.widthBits {
		need(n > 0, fmt.Sprintf("color indexing with width bits %d", b))
	}
	need(st.simpleCodes[0] > 0 && st.simpleCodes[1] > 0, "simple codes of 1 and 2 symbols")
	need(st.normalCodes > 0 && st.maxSymbol > 0, "normal codes, with max_symbol")
	need(st.repeats[0] > 0 && st.repeats[1] > 0 && st.repeats[2] > 0, "code length codes 16, 17 and 18")
	need(st.entropyImages > 0, "entropy images")
	need(st.cacheHits > 0, "color cache hits")
	need(st.distances[0] > 0 && st.distances[1] > 0, "distance codes up to 120 and above")
	need(st.overlaps > 0, "backward references longer than their distance")
	if len(missing) > 0 {
		t.Errorf("the vectors lack: %s", strings.Join(missing, ", "))
	}
}

// TestALPHStreams decodes the ALPH chunks of the vectors that are lossless and not filtered: an image stream without
// a header whose green is the alpha plane (RFC 9649 section 2.7.1.2).
func TestALPHStreams(t *testing.T) {
	n := 0
	for _, v := range loadVectors(t) {
		f, err := Parse(readVector(t, v))
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		for i, fr := range f.Frames {
			a := fr.alphaData
			if len(a) == 0 || a[0]&3 != 1 || a[0]>>2&3 != 0 {
				continue
			}
			pix, err := decodeVP8LStream(a[1:], fr.Width, fr.Height, nil)
			if err != nil {
				t.Errorf("%s: frame %d: %v", v.Name, i, err)
				continue
			}
			plane := make([]byte, len(pix)/4)
			for j := range plane {
				plane[j] = pix[4*j+1]
			}
			if got := sha256Hex(plane); got != v.Frames[i].AlphaSHA256 {
				t.Errorf("%s: frame %d: alpha has SHA-256 %s, want %s", v.Name, i, got, v.Frames[i].AlphaSHA256)
			}
			n++
		}
	}
	if n < 5 {
		t.Errorf("%d ALPH chunks decoded, want at least 5", n)
	}
	t.Logf("%d ALPH chunks decoded", n)
}
