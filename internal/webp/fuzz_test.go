package webp

import (
	"image"
	"math"
	"slices"
	"testing"
)

// fuzzMaxPixels is the largest canvas that the fuzz tests decode.
const fuzzMaxPixels = 1 << 20

func FuzzParse(f *testing.F) {
	for _, v := range loadVectors(f) {
		f.Add(readVector(f, v))
	}
	for _, c := range containerCases() {
		f.Add(c.data)
	}
	for _, s := range edgeStreams() {
		f.Add(riff(chunk("VP8L", s.data)))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := Parse(data)
		if err != nil {
			if file != nil {
				t.Errorf("Parse() = %+v with an error", file)
			}
			return
		}
		checkFile(t, file)
		if file.Width*file.Height > fuzzMaxPixels {
			return
		}
		if img, err := file.Image(); err == nil && img.Bounds() != image.Rect(0, 0, file.Width, file.Height) {
			t.Errorf("Image() of %v, want the canvas of %dx%d", img.Bounds(), file.Width, file.Height)
		}
		pixels := 0
		for i, fr := range file.Frames {
			if pixels += fr.Width * fr.Height; pixels > fuzzMaxPixels {
				break
			}
			img, err := file.FrameImage(i)
			if err != nil {
				continue
			}
			if img.Bounds() != image.Rect(0, 0, fr.Width, fr.Height) {
				t.Errorf("FrameImage(%d) of %v, want %dx%d", i, img.Bounds(), fr.Width, fr.Height)
			}
			if _, ok := img.(*image.NRGBA); ok != (fr.hasAlpha || !isOpaque(img)) {
				t.Errorf("FrameImage(%d) is %T; alpha %t", i, img, fr.hasAlpha)
			}
		}
	})
}

// checkFile checks what Parse promises.
func checkFile(t *testing.T, f *File) {
	t.Helper()
	if f.Width < 1 || f.Height < 1 || uint64(f.Width)*uint64(f.Height) > math.MaxUint32 || len(f.Frames) == 0 ||
		!f.Animated && len(f.Frames) != 1 || f.Lossless && (f.Animated || !f.Frames[0].lossless) {
		t.Fatalf("canvas %dx%d, animated %t, lossless %t, %d frames", f.Width, f.Height, f.Animated, f.Lossless,
			len(f.Frames))
	}
	alpha := false
	for i, fr := range f.Frames {
		alpha = alpha || fr.hasAlpha
		if fr.X < 0 || fr.Y < 0 || fr.X%2 != 0 || fr.Y%2 != 0 || fr.Width < 1 || fr.Height < 1 ||
			fr.Width > 1<<14 || fr.Height > 1<<14 || fr.X+fr.Width > f.Width || fr.Y+fr.Height > f.Height ||
			!f.Animated && (fr.X != 0 || fr.Y != 0 || fr.Width != f.Width || fr.Height != f.Height) {
			t.Errorf("frame %d at (%d, %d) of %dx%d on a canvas of %dx%d", i, fr.X, fr.Y, fr.Width, fr.Height,
				f.Width, f.Height)
		}
		if fr.lossless && fr.alphaData != nil || len(fr.bitstream) < 5 {
			t.Errorf("frame %d: lossless %t, ALPH of %d bytes, bitstream of %d", i, fr.lossless, len(fr.alphaData),
				len(fr.bitstream))
		}
	}
	if alpha != f.Alpha {
		t.Errorf("alpha %t, but the frames %t", f.Alpha, alpha)
	}
}

// FuzzVP8 decodes VP8 bitstreams of frames of at most fuzzMaxPixels.
func FuzzVP8(f *testing.F) {
	for _, v := range loadVectors(f) {
		file, err := Parse(readVector(f, v))
		if err != nil {
			f.Fatal(err)
		}
		for _, fr := range file.Frames {
			if !fr.lossless {
				f.Add(fr.bitstream)
			}
		}
	}
	for _, s := range slices.Concat(vp8Streams(), vp8ErrorStreams()) {
		f.Add(s.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		w, h, err := vp8Header(data)
		if err != nil || w*h > fuzzMaxPixels {
			return
		}
		img, err := decodeVP8(data)
		if err != nil {
			if img != nil {
				t.Errorf("an image with the error %v", err)
			}
			return
		}
		cw, ch := (w+1)/2, (h+1)/2
		if img.w != w || img.h != h || len(img.y) != w*h || len(img.u) != cw*ch || len(img.v) != cw*ch {
			t.Errorf("%dx%d with planes of %d, %d and %d bytes, want %dx%d", img.w, img.h, len(img.y), len(img.u),
				len(img.v), w, h)
		}
	})
}

// FuzzLosslessStream decodes image streams without a header, as in VP8L and ALPH chunks.
func FuzzLosslessStream(f *testing.F) {
	for _, v := range loadVectors(f) {
		file, err := Parse(readVector(f, v))
		if err != nil {
			f.Fatal(err)
		}
		for _, fr := range file.Frames {
			if fr.lossless && fr.Width <= 256 && fr.Height <= 256 {
				f.Add(fr.bitstream[5:], uint8(fr.Width-1), uint8(fr.Height-1))
			}
			if a := fr.alphaData; len(a) > 0 && a[0]&3 == 1 && fr.Width <= 256 && fr.Height <= 256 {
				f.Add(a[1:], uint8(fr.Width-1), uint8(fr.Height-1))
			}
		}
	}
	for _, s := range edgeStreams() {
		w, h, _, err := vp8lHeader(s.data)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(s.data[5:], uint8(w-1), uint8(h-1))
	}
	f.Fuzz(func(t *testing.T, data []byte, w, h uint8) {
		width, height := int(w)+1, int(h)+1
		pix, err := decodeVP8LStream(data, width, height, nil)
		if err == nil && len(pix) != 4*width*height {
			t.Errorf("%d bytes for %dx%d pixels", len(pix), width, height)
		}
	})
}
