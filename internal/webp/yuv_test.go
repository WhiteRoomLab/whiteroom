package webp

import (
	"math"
	"math/rand/v2"
	"testing"
)

// refRGB transcribes the conversion that the comment of libwebp's src/dsp/yuv.h gives:
// R = (19077 . y + 26149 . v - 14234) >> 6, G = (19077 . y - 6419 . u - 13320 . v + 8708) >> 6 and
// B = (19077 . y + 33050 . u - 17685) >> 6, where a . b = ((a << 8) * b) >> 16, clamped to 0..255.
func refRGB(y, u, v int) [3]byte {
	dot := func(a, b int) int { return (a << 8) * b >> 16 }
	clamp := func(x int) byte { return byte(min(max(x>>6, 0), 255)) }
	return [3]byte{
		clamp(dot(y, 19077) + dot(v, 26149) - 14234),
		clamp(dot(y, 19077) - dot(u, 6419) - dot(v, 13320) + 8708),
		clamp(dot(y, 19077) + dot(u, 33050) - 17685),
	}
}

// bt601 returns the color of BT.601 (Y in 16..235, U and V in 16..240) as the same comment writes it.
func bt601(y, u, v int) [3]float64 {
	l, cb, cr := 1.164*float64(y-16), float64(u-128), float64(v-128)
	return [3]float64{l + 1.596*cr, l - 0.813*cr - 0.392*cb, l + 2.017*cb}
}

func TestYUVToRGB(t *testing.T) {
	extremes := []int{0, 16, 128, 235, 255}
	all := make([]int, 256)
	for i := range all {
		all[i] = i
	}
	// One row per pair of values of two of Y, U and V, with every value of the third.
	check := func(ys, us, vs []int) {
		t.Helper()
		n := max(len(ys), len(us), len(vs))
		y, u, v, dst := make([]byte, n), make([]byte, n), make([]byte, n), make([]byte, 4*n)
		at := func(s []int, i int) int { return s[i%len(s)] }
		for i := range n {
			y[i], u[i], v[i] = byte(at(ys, i)), byte(at(us, i)), byte(at(vs, i))
		}
		yuvRow(dst, y, u, v, true)
		for i := range n {
			yy, uu, vv := at(ys, i), at(us, i), at(vs, i)
			got := [3]byte(dst[4*i:])
			if want := refRGB(yy, uu, vv); got != want || dst[4*i+3] != 255 {
				t.Fatalf("Y %d, U %d, V %d: %v and A %d, want %v and 255", yy, uu, vv, got, dst[4*i+3], want)
			}
			for c, f := range bt601(yy, uu, vv) {
				if d := float64(got[c]) - min(max(math.Round(f), 0), 255); d < -1 || d > 1 {
					t.Fatalf("Y %d, U %d, V %d: %v, far from BT.601 %v", yy, uu, vv, got, bt601(yy, uu, vv))
				}
			}
		}
	}
	for _, a := range extremes {
		for _, b := range extremes {
			check(all, []int{a}, []int{b})
			check([]int{a}, all, []int{b})
			check([]int{a}, []int{b}, all)
		}
	}
	for _, y := range extremes {
		for u := range 256 {
			check([]int{y}, []int{u}, all)
		}
	}
	for _, tt := range []struct {
		y, u, v int
		want    [3]byte
	}{
		{16, 128, 128, [3]byte{0, 0, 0}},
		{235, 128, 128, [3]byte{255, 255, 255}},
		{0, 0, 0, [3]byte{0, 136, 0}},
		{255, 255, 255, [3]byte{255, 125, 255}},
		{81, 90, 240, [3]byte{254, 0, 0}},
	} {
		var dst [4]byte
		yuvRow(dst[:], []byte{byte(tt.y)}, []byte{byte(tt.u)}, []byte{byte(tt.v)}, false)
		if got := [3]byte(dst[:]); got != tt.want || dst[3] != 0 {
			t.Errorf("Y %d, U %d, V %d: %v, want %v and A kept", tt.y, tt.u, tt.v, dst, tt.want)
		}
	}
}

// refChroma transcribes the fancy upsampler of libwebp (UPSAMPLE_FUNC in src/dsp/upsampling.c, EmitFancyRGB in
// src/dec/io_dec.c) for the chroma of pixel (x, y) of a w x h image from its plane p. Output row 0 is a top row on chroma
// row 0 alone; rows 2k-1 (top) and 2k (bottom) lie between chroma rows k-1 and k; if h is even, row h-1 is a top row on
// chroma row h/2-1 alone. In a row, x = 0 and, if w is even, x = w-1 take one chroma column; x = 2k-1 and 2k lie between
// chroma columns k-1 and k.
func refChroma(p []byte, w, h, x, y int) int {
	cw := (w + 1) / 2
	at := func(row, col int) int { return int(p[row*cw+col]) }
	t, c, bottom := 0, 0, false
	switch {
	case y == 0:
	case y%2 == 1 && y == h-1:
		t, c = (y-1)/2, (y-1)/2
	case y%2 == 1:
		t, c = (y-1)/2, (y+1)/2
	default:
		t, c, bottom = y/2-1, y/2, true
	}
	if x == 0 || x == w-1 && w%2 == 0 {
		tl, l := at(t, x/2), at(c, x/2)
		if bottom {
			return (3*l + tl + 2) >> 2
		}
		return (3*tl + l + 2) >> 2
	}
	k := (x + 1) / 2
	tl, tr, l, r := at(t, k-1), at(t, k), at(c, k-1), at(c, k)
	avg := tl + tr + l + r + 8
	diag12 := (avg + 2*(tr+l)) >> 3
	diag03 := (avg + 2*(tl+r)) >> 3
	switch {
	case !bottom && x%2 == 1:
		return (diag12 + tl) >> 1
	case !bottom:
		return (diag03 + tr) >> 1
	case x%2 == 1:
		return (diag03 + l) >> 1
	}
	return (diag12 + r) >> 1
}

func randomYUV(rng *rand.Rand, w, h int) *yuvImage {
	cw, ch := (w+1)/2, (h+1)/2
	m := &yuvImage{y: make([]byte, w*h), u: make([]byte, cw*ch), v: make([]byte, cw*ch), w: w, h: h}
	for _, p := range [][]byte{m.y, m.u, m.v} {
		for i := range p {
			p[i] = byte(rng.IntN(256))
		}
	}
	return m
}

func TestFancyUpsampling(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 29))
	sizes := [][2]int{{1, 1}, {2, 1}, {1, 2}, {3, 3}, {4, 4}, {5, 3}, {33, 17}, {32, 18}}
	for w := 1; w <= 8; w++ {
		for h := 1; h <= 8; h++ {
			sizes = append(sizes, [2]int{w, h})
		}
	}
	for _, s := range sizes {
		w, h := s[0], s[1]
		m := randomYUV(rng, w, h)
		pix := make([]byte, 4*w*h)
		for i := range pix {
			pix[i] = byte(i)
		}
		m.toRGBA(pix, false)
		opaque := make([]byte, 4*w*h)
		m.toRGBA(opaque, true)
	Pixels:
		for y := range h {
			for x := range w {
				i := 4 * (y*w + x)
				want := refRGB(int(m.y[y*w+x]), refChroma(m.u, w, h, x, y), refChroma(m.v, w, h, x, y))
				if [3]byte(pix[i:]) != want || pix[i+3] != byte(i+3) || [3]byte(opaque[i:]) != want ||
					opaque[i+3] != 255 {
					t.Errorf("%dx%d: (%d, %d) is %v, or %v opaque, want %v", w, h, x, y, pix[i:i+4], opaque[i:i+4],
						want)
					break Pixels
				}
			}
		}
	}
}

func BenchmarkToRGBA(b *testing.B) {
	m := randomYUV(rand.New(rand.NewPCG(1, 1)), 1920, 1080)
	pix := make([]byte, 4*m.w*m.h)
	b.ReportAllocs()
	for b.Loop() {
		m.toRGBA(pix, true)
	}
	b.ReportMetric(float64(m.w*m.h)*float64(b.N)/b.Elapsed().Seconds()/1e6, "Mpixel/s")
}

// TestUpsamplingConstant checks that chroma planes of one value give that value at every pixel.
func TestUpsamplingConstant(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 29))
	for _, c := range []byte{0, 1, 127, 128, 254, 255} {
		m := randomYUV(rng, 7, 5)
		for i := range m.u {
			m.u[i], m.v[i] = c, 255-c
		}
		pix := make([]byte, 4*7*5)
		m.toRGBA(pix, true)
		for i, y := range m.y {
			if want := refRGB(int(y), int(c), int(255-c)); [3]byte(pix[4*i:]) != want {
				t.Errorf("chroma %d: pixel %d is %v, want %v", c, i, pix[4*i:4*i+3], want)
			}
		}
	}
}
