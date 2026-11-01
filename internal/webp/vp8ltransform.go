package webp

// inverse applies the inverse of t (RFC 9649 section 3.5) to the image at the start of pix, h rows of t.width pixels,
// or for color indexing of the width it reduced t.width to.
func (t *transform) inverse(pix []byte, h int) {
	switch t.kind {
	case predictorTransform:
		inversePredictor(pix, t.width, h, t.bits, t.data)
	case colorTransform:
		inverseColor(pix, t.width, h, t.bits, t.data)
	case subtractGreenTransform:
		addGreen(pix[:4*t.width*h])
	case colorIndexingTransform:
		inverseIndexing(pix, t.width, h, t.bits, t.palette)
	}
}

// inversePredictor adds the predictions of the modes in the green of the predictor image (RFC 9649 section 3.5.1).
func inversePredictor(pix []byte, w, h, bits int, modes []byte) {
	setPixel(pix, 0, addPixels(pixel(pix, 0), 0xff000000))
	for x := 1; x < w; x++ {
		setPixel(pix, x, addPixels(pixel(pix, x), pixel(pix, x-1)))
	}
	blocksPerRow := divRoundUp(w, bits)
	for y := 1; y < h; y++ {
		row := y * w
		setPixel(pix, row, addPixels(pixel(pix, row), pixel(pix, row-w)))
		modeRow := modes[4*(y>>bits)*blocksPerRow:]
		for x := 1; x < w; x++ {
			i := row + x
			setPixel(pix, i, addPixels(pixel(pix, i), predict(modeRow[4*(x>>bits)+1], pix, i, w)))
		}
	}
}

// predict returns the prediction of a mode for pixel i, not on the first row or column, of an image w pixels wide. On
// the last column, the pixel after the top one, which predictions take as the top-right, is the first of the row.
func predict(mode byte, pix []byte, i, w int) uint32 {
	// Like libwebp, the mode is the low 4 bits of green, and 14 and 15 predict as 0.
	switch mode & 15 {
	case 1:
		return pixel(pix, i-1)
	case 2:
		return pixel(pix, i-w)
	case 3:
		return pixel(pix, i-w+1)
	case 4:
		return pixel(pix, i-w-1)
	case 5:
		return average2(average2(pixel(pix, i-1), pixel(pix, i-w+1)), pixel(pix, i-w))
	case 6:
		return average2(pixel(pix, i-1), pixel(pix, i-w-1))
	case 7:
		return average2(pixel(pix, i-1), pixel(pix, i-w))
	case 8:
		return average2(pixel(pix, i-w-1), pixel(pix, i-w))
	case 9:
		return average2(pixel(pix, i-w), pixel(pix, i-w+1))
	case 10:
		return average2(average2(pixel(pix, i-1), pixel(pix, i-w-1)), average2(pixel(pix, i-w), pixel(pix, i-w+1)))
	case 11:
		return selectPixel(pixel(pix, i-1), pixel(pix, i-w), pixel(pix, i-w-1))
	case 12:
		return clampAddSubtractFull(pixel(pix, i-1), pixel(pix, i-w), pixel(pix, i-w-1))
	case 13:
		return clampAddSubtractHalf(average2(pixel(pix, i-1), pixel(pix, i-w)), pixel(pix, i-w-1))
	}
	return 0xff000000
}

// addPixels adds each channel modulo 256.
func addPixels(a, b uint32) uint32 {
	return (a&0xff00ff00+b&0xff00ff00)&0xff00ff00 | (a&0x00ff00ff+b&0x00ff00ff)&0x00ff00ff
}

// average2 returns the average of each channel, rounded down.
func average2(a, b uint32) uint32 {
	return ((a^b)&0xfefefefe)>>1 + a&b
}

// selectPixel returns l or t, the one nearer to the estimate l + t - tl.
func selectPixel(l, t, tl uint32) uint32 {
	dl, dt := 0, 0
	for s := 0; s < 32; s += 8 {
		c := int(tl >> s & 0xff)
		dl += abs(int(t>>s&0xff) - c)
		dt += abs(int(l>>s&0xff) - c)
	}
	if dl < dt {
		return l
	}
	return t
}

func clampAddSubtractFull(a, b, c uint32) uint32 {
	var v uint32
	for s := 0; s < 32; s += 8 {
		v |= clamp255(int(a>>s&0xff)+int(b>>s&0xff)-int(c>>s&0xff)) << s
	}
	return v
}

func clampAddSubtractHalf(a, b uint32) uint32 {
	var v uint32
	for s := 0; s < 32; s += 8 {
		x := int(a >> s & 0xff)
		v |= clamp255(x+(x-int(b>>s&0xff))/2) << s
	}
	return v
}

func clamp255(v int) uint32 {
	return uint32(min(max(v, 0), 255))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// inverseColor adds the color transform deltas of the color transform image, whose blue, green and red are
// green_to_red, green_to_blue and red_to_blue (RFC 9649 section 3.5.2).
func inverseColor(pix []byte, w, h, bits int, elems []byte) {
	blocksPerRow := divRoundUp(w, bits)
	for y := range h {
		row := elems[4*(y>>bits)*blocksPerRow:]
		for x := range w {
			e := row[4*(x>>bits):]
			p := pix[4*(y*w+x):]
			g := int8(p[1])
			r := p[2] + uint8(colorDelta(int8(e[0]), g))
			p[0] += uint8(colorDelta(int8(e[1]), g)) + uint8(colorDelta(int8(e[2]), int8(r)))
			p[2] = r
		}
	}
}

func colorDelta(t, c int8) int {
	return int(t) * int(c) >> 5
}

// addGreen adds green to red and blue (RFC 9649 section 3.5.3).
func addGreen(pix []byte) {
	for i := 0; i < len(pix); i += 4 {
		g := pix[i+1]
		pix[i] += g
		pix[i+2] += g
	}
}

// inverseIndexing replaces the indexes in green by the colors of the palette (RFC 9649 section 3.5.4), unbundling the
// pixels that bits packs into each pixel.
func inverseIndexing(pix []byte, w, h, bits int, palette *[256]uint32) {
	if bits == 0 {
		for i := range w * h {
			setPixel(pix, i, palette[pix[4*i+1]])
		}
		return
	}
	// Going backwards, each packed pixel is read before a pixel overwrites it.
	packed := divRoundUp(w, bits)
	size := 8 >> bits
	mask := 1<<size - 1
	for y := h - 1; y >= 0; y-- {
		for x := w - 1; x >= 0; x-- {
			g := int(pix[4*(y*packed+x>>bits)+1])
			setPixel(pix, y*w+x, palette[g>>(x&(1<<bits-1)*size)&mask])
		}
	}
}
