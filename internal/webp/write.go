package webp

import (
	"encoding/binary"
	"errors"
	"io"
)

// WriteWithoutMetadata writes the file without its EXIF and XMP metadata and unknown chunks: VP8X, ICCP, and ANIM and
// the frames or the still image, with their payloads unchanged. The flags of VP8X tell which of these chunks and alpha
// the file has (an Alpha flag is kept), and reserved bits are 0. A file of the simple format is written as its image
// chunk alone.
func (f *File) WriteWithoutMetadata(w io.Writer) error {
	if len(f.Frames) == 0 {
		return errors.New("webp: no frames")
	}
	n := 64 + len(f.ICC)
	for i := range f.Frames {
		n += 48 + len(f.Frames[i].bitstream) + len(f.Frames[i].alphaData)
	}
	b := append(make([]byte, 0, n), "RIFF\x00\x00\x00\x00WEBP"...)
	if f.extended {
		b = f.appendExtended(b)
	} else {
		b = appendImage(b, &f.Frames[0])
	}
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	_, err := w.Write(b)
	return err
}

// appendExtended appends the chunks of the extended format without metadata.
func (f *File) appendExtended(b []byte) []byte {
	var vp8x [10]byte
	vp8x[0] = f.vp8xFlags()
	put24(vp8x[4:], f.Width-1)
	put24(vp8x[7:], f.Height-1)
	b = appendChunk(b, "VP8X", vp8x[:])
	if f.ICC != nil {
		b = appendChunk(b, "ICCP", f.ICC)
	}
	if !f.Animated {
		return appendImage(b, &f.Frames[0])
	}
	var anim [6]byte
	copy(anim[:], f.Background[:])
	binary.LittleEndian.PutUint16(anim[4:], uint16(f.LoopCount))
	b = appendChunk(b, "ANIM", anim[:])
	for i := range f.Frames {
		b = appendFrame(b, &f.Frames[i])
	}
	return b
}

// vp8xFlags returns the flags of VP8X for WriteWithoutMetadata. libwebp's webpinfo and webpmux reject files whose
// flags and chunks disagree, except an Alpha flag without alpha.
func (f *File) vp8xFlags() byte {
	flags := f.flags & flagAlpha
	if f.Alpha {
		flags |= flagAlpha
	}
	if f.ICC != nil {
		flags |= flagICC
	}
	if f.Animated {
		flags |= flagAnimation
	}
	return flags
}

// appendFrame appends an ANMF chunk with the frame's image.
func appendFrame(b []byte, fr *Frame) []byte {
	start := len(b)
	var h [16]byte
	put24(h[:], fr.X/2)
	put24(h[3:], fr.Y/2)
	put24(h[6:], fr.Width-1)
	put24(h[9:], fr.Height-1)
	put24(h[12:], fr.Duration)
	if !fr.Blend {
		h[15] |= 2
	}
	if fr.Dispose {
		h[15] |= 1
	}
	b = appendChunk(b, "ANMF", h[:])
	b = appendImage(b, fr)
	binary.LittleEndian.PutUint32(b[start+4:], uint32(len(b)-start-8))
	return b
}

// appendImage appends the chunks of a frame's image: ALPH and VP8, or VP8L.
func appendImage(b []byte, fr *Frame) []byte {
	if fr.lossless {
		return appendChunk(b, "VP8L", fr.bitstream)
	}
	if fr.alphaData != nil {
		b = appendChunk(b, "ALPH", fr.alphaData)
	}
	return appendChunk(b, "VP8 ", fr.bitstream)
}

// appendChunk appends a chunk, padded with a zero to an even size.
func appendChunk(b []byte, id string, payload []byte) []byte {
	b = append(b, id...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(payload)))
	b = append(b, payload...)
	if len(payload)%2 != 0 {
		b = append(b, 0)
	}
	return b
}

func put24(b []byte, v int) {
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16)
}
