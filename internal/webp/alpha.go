package webp

import (
	"errors"
	"fmt"
)

// The filtering methods of ALPH (RFC 9649 section 2.7.1.2).
const (
	alphaFilterNone = iota
	alphaFilterHorizontal
	alphaFilterVertical
	alphaFilterGradient
)

// checkAlpha checks the header of the ALPH payload of a w x h frame and the size of its raw data.
func checkAlpha(data []byte, w, h int) error {
	// dwebp fails on these, though RFC 9649 says to ignore the reserved bits.
	switch {
	case len(data) < 2:
		return errors.New("webp: ALPH chunk too short")
	case data[0]>>6 != 0:
		return errors.New("webp: reserved bits of ALPH set")
	case data[0]>>4&3 > 1:
		return fmt.Errorf("webp: unknown ALPH pre-processing %d", data[0]>>4&3)
	case data[0]&3 > 1:
		return fmt.Errorf("webp: unknown ALPH compression %d", data[0]&3)
	case data[0]&3 == 0 && len(data)-1 < w*h:
		return fmt.Errorf("webp: raw ALPH data of %d bytes for %dx%d pixels", len(data)-1, w, h)
	}
	return nil
}

// decodeAlpha decodes the ALPH payload of a w x h frame (RFC 9649 section 2.7.1.2) into the A bytes of RGBA bytes of
// the frame, whose other bytes are left for the colors.
func decodeAlpha(data []byte, w, h int) ([]byte, error) {
	if err := checkAlpha(data, w, h); err != nil {
		return nil, err
	}
	var pix []byte
	if data[0]&3 == 0 {
		pix = make([]byte, 4*w*h)
		for i, a := range data[1 : 1+w*h] {
			pix[4*i+3] = a
		}
	} else {
		var err error
		if pix, err = decodeVP8LStream(data[1:], w, h, nil); err != nil {
			return nil, err
		}
		// The alpha values are the green of the image.
		for i := 1; i < len(pix); i += 4 {
			pix[i+2] = pix[i]
		}
	}
	unfilterAlpha(pix, w, h, int(data[0]>>2&3))
	return pix, nil
}

// unfilterAlpha reverses a filtering method on the A bytes of pix, RGBA bytes of a w x h frame. The top-left value is
// predicted by 0, the rest of the first row by the value to the left, and the first column by the value above.
func unfilterAlpha(pix []byte, w, h, method int) {
	if method == alphaFilterNone {
		return
	}
	stride := 4 * w
	a := pix[3:]
	for x := 4; x < stride; x += 4 {
		a[x] += a[x-4]
	}
	for y := 1; y < h; y++ {
		row, up := a[y*stride:(y+1)*stride-3], a[(y-1)*stride:y*stride-3]
		switch method {
		case alphaFilterHorizontal:
			row[0] += up[0]
			for x := 4; x < len(row); x += 4 {
				row[x] += row[x-4]
			}
		case alphaFilterVertical:
			for x := 0; x < len(row); x += 4 {
				row[x] += up[x]
			}
		case alphaFilterGradient:
			row[0] += up[0]
			for x := 4; x < len(row); x += 4 {
				row[x] += clip8(int(row[x-4]) + int(up[x]) - int(up[x-4]))
			}
		}
	}
}
