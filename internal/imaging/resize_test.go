package imaging

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"
)

func TestLanczosTaps(t *testing.T) {
	for _, tt := range []struct{ srcLen, dstLen int }{
		{1, 1}, {2, 1}, {3, 2}, {7, 7}, {10, 3}, {100, 99}, {100, 64}, {640, 33}, {4000, 2560}, {10000, 32}, {2600, 39},
	} {
		for _, crop := range []float64{0, 0.25, 0.5, 1} {
			span := float64(tt.srcLen) - crop*float64(tt.srcLen-tt.dstLen)
			off := (float64(tt.srcLen) - span) / 2
			name := fmt.Sprintf("%d to %d from %g of %g", tt.srcLen, tt.dstLen, off, span)
			tp := lanczosTaps(tt.srcLen, tt.dstLen, off, span)
			end := 0
			for i := range tt.dstLen {
				start, n := tp.start[i], tp.count[i]
				if start < 0 || n < 1 || n > tp.n || start+n > tt.srcLen ||
					i > 0 && (start < tp.start[i-1] || start+n < end) {
					t.Fatalf("%s: output %d takes %d pixels from %d (%d at most); the one before ends at %d", name, i,
						n, start, tp.n, end)
				}
				end = start + n
				sum := int32(0)
				for _, w := range tp.w[i*tp.n : i*tp.n+n] {
					sum += w
				}
				if sum != weightOne {
					t.Fatalf("%s: weights of output %d sum to %d", name, i, sum)
				}
			}
		}
	}
	// Not scaled and at whole pixels: each output pixel is a source pixel.
	tp := lanczosTaps(10, 6, 2, 6)
	for i := range 6 {
		for j, w := range tp.w[i*tp.n : i*tp.n+tp.count[i]] {
			if want := int32(weightOne * btoi(tp.start[i]+j == i+2)); w != want {
				t.Errorf("output %d: weight %d of source %d, want %d", i, w, tp.start[i]+j, want)
			}
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestCropRect(t *testing.T) {
	tests := []struct {
		sw, sh, dw, dh int
		x0, w, y0, h   float64
	}{
		{400, 300, 200, 150, 0, 400, 0, 300},
		{400, 300, 100, 100, 50, 300, 0, 300},
		{300, 400, 100, 100, 0, 300, 50, 300},
		{1000, 333, 640, 213, 0, 1000, (333 - 1000*213.0/640) / 2, 1000 * 213.0 / 640},
		// Not scaled: at whole pixels, the half pixel left out on the right or the bottom.
		{11, 6, 6, 6, 2, 6, 0, 6},
		{6, 11, 6, 6, 0, 6, 2, 6},
		{10, 6, 6, 6, 2, 6, 0, 6},
		{7, 7, 7, 7, 0, 7, 0, 7},
	}
	for _, tt := range tests {
		x0, w, y0, h := cropRect(tt.sw, tt.sh, tt.dw, tt.dh)
		if x0 != tt.x0 || w != tt.w || y0 != tt.y0 || h != tt.h {
			t.Errorf("cropRect(%d, %d, %d, %d) = %g, %g, %g, %g; want %g, %g, %g, %g", tt.sw, tt.sh, tt.dw, tt.dh,
				x0, w, y0, h, tt.x0, tt.w, tt.y0, tt.h)
		}
	}
}

// sources returns an image of w x h pixels of each type that resize reads, all of the color c.
func constantSources(w, h int, c color.NRGBA) []image.Image {
	r := image.Rect(0, 0, w, h)
	gray := image.NewGray(r)
	rgba := image.NewRGBA(r)
	nrgba := image.NewNRGBA(r)
	opaque := image.NewNRGBA(r)
	ycc := image.NewYCbCr(r, image.YCbCrSubsampleRatio420)
	paletted := image.NewPaletted(r, color.Palette{color.Black, c})
	opaquePaletted := image.NewPaletted(r, color.Palette{color.Black, color.NRGBA{c.R, c.G, c.B, 0xff}})
	yy, cb, cr := color.RGBToYCbCr(c.R, c.G, c.B)
	for y := range h {
		for x := range w {
			gray.SetGray(x, y, color.Gray{c.G})
			rgba.SetRGBA(x, y, color.RGBA{c.R, c.G, c.B, 0xff})
			nrgba.SetNRGBA(x, y, c)
			opaque.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, 0xff})
			paletted.SetColorIndex(x, y, 1)
			opaquePaletted.SetColorIndex(x, y, 1)
			ycc.Y[ycc.YOffset(x, y)], ycc.Cb[ycc.COffset(x, y)], ycc.Cr[ycc.COffset(x, y)] = yy, cb, cr
		}
	}
	return []image.Image{gray, rgba, nrgba, opaque, ycc, paletted, opaquePaletted}
}

func TestResizeConstant(t *testing.T) {
	c := color.NRGBA{200, 120, 40, 0x80}
	for _, size := range [][4]int{{1, 1, 1, 1}, {7, 5, 3, 2}, {64, 48, 64, 48}, {64, 48, 13, 31}, {300, 7, 25, 1}} {
		for _, m := range constantSources(size[0], size[1], c) {
			out, err := resize(m, size[2], size[3])
			if err != nil {
				t.Fatal(err)
			}
			want := m.At(0, 0)
			if ycc, ok := m.(*image.YCbCr); ok {
				r, g, b := color.YCbCrToRGB(ycc.Y[0], ycc.Cb[0], ycc.Cr[0])
				want = color.RGBA{r, g, b, 0xff}
			}
			wr, wg, wb, wa := want.RGBA()
			b := out.Bounds()
			if b != image.Rect(0, 0, size[2], size[3]) {
				t.Fatalf("%T to %dx%d: bounds %v", m, size[2], size[3], b)
			}
			for y := range b.Dy() {
				for x := range b.Dx() {
					if r, g, b, a := out.At(x, y).RGBA(); r != wr || g != wg || b != wb || a != wa {
						t.Fatalf("%T %dx%d to %dx%d: pixel (%d, %d) is %v, want %v", m, size[0], size[1], size[2],
							size[3], x, y, out.At(x, y), want)
					}
				}
			}
		}
	}
}

func TestResizeTypes(t *testing.T) {
	c := color.NRGBA{200, 120, 40, 0x80}
	want := []string{"*image.Gray", "*image.RGBA", "*image.NRGBA", "*image.RGBA", "*image.RGBA", "*image.NRGBA",
		"*image.RGBA"}
	for i, m := range constantSources(8, 8, c) {
		out, err := resize(m, 4, 4)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%T", out); got != want[i] {
			t.Errorf("resize(%T) = %s, want %s", m, got, want[i])
		}
	}
}

// TestResizeTransparentBorder checks that transparent pixels, whose color is black, do not darken the colors next to
// them: the colors are filtered premultiplied by alpha.
func TestResizeTransparentBorder(t *testing.T) {
	c := color.NRGBA{200, 100, 50, 0xff}
	m := image.NewNRGBA(image.Rect(0, 0, 40, 30))
	for y := 5; y < 25; y++ {
		for x := 5; x < 35; x++ {
			m.SetNRGBA(x, y, c)
		}
	}
	p := image.NewPaletted(m.Rect, color.Palette{color.Transparent, c})
	for y := 5; y < 25; y++ {
		for x := 5; x < 35; x++ {
			p.SetColorIndex(x, y, 1)
		}
	}
	for _, src := range []image.Image{m, p} {
		for _, size := range [][2]int{{13, 10}, {20, 15}, {8, 6}, {39, 29}} {
			out, err := resize(src, size[0], size[1])
			if err != nil {
				t.Fatal(err)
			}
			n := out.(*image.NRGBA)
			partial := 0
			for y := range size[1] {
				for x := range size[0] {
					got := n.NRGBAAt(x, y)
					if got.A == 0 {
						continue
					}
					if got.A < 0xff {
						partial++
					}
					if got.A >= 16 && (diff(got.R, c.R) > 2 || diff(got.G, c.G) > 2 || diff(got.B, c.B) > 2) {
						t.Errorf("%T to %v: pixel (%d, %d) is %v, want the color of %v", src, size, x, y, got, c)
					}
				}
			}
			if partial == 0 {
				t.Errorf("%T to %v: no pixel at the edges", src, size)
			}
		}
	}
}

func diff(a, b uint8) int {
	return int(max(a, b) - min(a, b))
}

func TestResizeCheckerboard(t *testing.T) {
	for _, n := range []int{2, 16, 64} {
		m := image.NewGray(image.Rect(0, 0, n, n))
		for y := range n {
			for x := range n {
				m.SetGray(x, y, color.Gray{uint8(255 * ((x + y) % 2))})
			}
		}
		out, err := resize(m, n/2, n/2)
		if err != nil {
			t.Fatal(err)
		}
		// The taps that the edges cut off leave the pixels next to them lighter or darker.
		margin := min(3, n/4)
		g := out.(*image.Gray)
		for y := margin; y < n/2-margin; y++ {
			for x := margin; x < n/2-margin; x++ {
				if v := g.GrayAt(x, y).Y; v != 127 && v != 128 {
					t.Errorf("%dx%d: pixel (%d, %d) of %d, want mid-gray", n, n, x, y, v)
				}
			}
		}
	}
}

func TestResizeNoUpscaling(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 10, 8))
	for _, size := range [][2]int{{11, 8}, {10, 9}, {11, 9}, {0, 8}, {10, 0}, {20, 1}} {
		if out, err := resize(m, size[0], size[1]); err == nil {
			t.Errorf("resize(10x8, %v) = %v, want an error", size, out.Bounds())
		}
	}
}

// TestResizeCrop checks that a part that is not scaled is copied as it is.
func TestResizeCrop(t *testing.T) {
	m := colored(11, 6)
	for _, tt := range []struct {
		w, h int
		at   image.Point
	}{
		{6, 6, image.Pt(2, 0)},
		{11, 6, image.Pt(0, 0)},
		{9, 6, image.Pt(1, 0)},
	} {
		out, err := resize(m, tt.w, tt.h)
		if err != nil {
			t.Fatal(err)
		}
		for y := range tt.h {
			for x := range tt.w {
				if got, want := out.(*image.RGBA).RGBAAt(x, y), m.RGBAAt(tt.at.X+x, tt.at.Y+y); got != want {
					t.Fatalf("%dx%d: pixel (%d, %d) is %v, want %v", tt.w, tt.h, x, y, got, want)
				}
			}
		}
	}
}

// TestResizeFloat checks the fixed point against the same filter in float64.
func TestResizeFloat(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		sw, sh := 1+r.IntN(80), 1+r.IntN(80)
		dw, dh := 1+r.IntN(sw), 1+r.IntN(sh)
		m := image.NewNRGBA(image.Rect(0, 0, sw, sh))
		for i := range m.Pix {
			m.Pix[i] = uint8(r.IntN(256))
		}
		gray := image.NewGray(m.Rect)
		for i := range gray.Pix {
			gray.Pix[i] = m.Pix[4*i]
		}
		opaque := image.NewRGBA(m.Rect)
		copy(opaque.Pix, m.Pix)
		for i := 3; i < len(opaque.Pix); i += 4 {
			opaque.Pix[i] = 0xff
		}
		for _, src := range []image.Image{m, gray, opaque} {
			out, err := resize(src, dw, dh)
			if err != nil {
				t.Fatal(err)
			}
			want := floatResize(src, dw, dh)
			for y := range dh {
				for x := range dw {
					g := color.NRGBAModel.Convert(out.At(x, y)).(color.NRGBA)
					w := want[y*dw+x]
					tolerance := 1.0
					if w[3] < 255 {
						tolerance = 1 + 255/max(w[3], 1) // the colors of pixels with less alpha are less exact
					}
					if math.Abs(float64(g.A)-w[3]) > 1 || g.A > 0 && (math.Abs(float64(g.R)-w[0]) > tolerance ||
						math.Abs(float64(g.G)-w[1]) > tolerance || math.Abs(float64(g.B)-w[2]) > tolerance) {
						t.Fatalf("%T %dx%d to %dx%d: pixel (%d, %d) is %v, want %.2f", src, sw, sh, dw, dh, x, y, g, w)
					}
				}
			}
		}
	}
}

// floatResize returns the NRGBA pixels of resize(m, dw, dh) computed in float64, without rounding the weights.
func floatResize(m image.Image, dw, dh int) [][4]float64 {
	b := m.Bounds()
	sw, sh := b.Dx(), b.Dy()
	x0, w, y0, h := cropRect(sw, sh, dw, dh)
	weights := func(srcLen, dstLen int, off, span float64) [][]float64 {
		out := make([][]float64, dstLen)
		scale := max(1, span/float64(dstLen))
		for i := range dstLen {
			center := off + (float64(i)+0.5)*span/float64(dstLen)
			ws := make([]float64, srcLen)
			sum := 0.0
			for j := range srcLen {
				if d := float64(j) + 0.5 - center; math.Abs(d) < 3*scale {
					ws[j] = lanczos3(d / scale)
					sum += ws[j]
				}
			}
			for j := range ws {
				ws[j] /= sum
			}
			out[i] = ws
		}
		return out
	}
	wx, wy := weights(sw, dw, x0, w), weights(sh, dh, y0, h)
	out := make([][4]float64, dw*dh)
	for y := range dh {
		for x := range dw {
			var s [4]float64
			for j := range sh {
				for i := range sw {
					k := wx[x][i] * wy[y][j]
					if k == 0 {
						continue
					}
					c := color.NRGBAModel.Convert(m.At(i, j)).(color.NRGBA)
					a := float64(c.A)
					s[0] += k * float64(c.R) * a
					s[1] += k * float64(c.G) * a
					s[2] += k * float64(c.B) * a
					s[3] += k * a
				}
			}
			p := [4]float64{0, 0, 0, min(max(s[3], 0), 255)}
			if s[3] > 0 {
				for c := range 3 {
					p[c] = min(max(s[c]/s[3], 0), 255)
				}
			}
			out[y*dw+x] = p
		}
	}
	return out
}
