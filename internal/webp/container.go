package webp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// File is a parsed WebP file. Its methods decode on demand.
type File struct {
	Width, Height int // of the canvas
	Animated      bool
	// Alpha is set if a frame has an alpha channel: an ALPH chunk with VP8, or a VP8L bitstream whose alpha_is_used bit
	// is set. The Alpha flag of VP8X is not used.
	Alpha      bool
	Lossless   bool    // a still image coded with VP8L
	ICC        []byte  // the payload of the first ICCP chunk if VP8X has the ICC flag; nil if none
	EXIF, XMP  []byte  // the same for EXIF and XMP
	LoopCount  int     // of an animation; 0 means forever
	Background [4]byte // of an animation, as B, G, R and A
	Frames     []Frame // one for a still image, covering the canvas

	flags byte // of VP8X; 0 in the simple formats
}

// Frame is a frame of an animation, or the still image.
type Frame struct {
	X, Y, Width, Height int
	Duration            int  // in milliseconds
	Blend               bool // alpha-blend onto the canvas (the B bit is 0)
	Dispose             bool // dispose to the background color (the D bit is 1)

	bitstream []byte // the payload of the VP8 or VP8L chunk
	alphaData []byte // the payload of the ALPH chunk of VP8; nil if none
	lossless  bool
	hasAlpha  bool
}

// The flags of VP8X (RFC 9649 section 2.7).
const (
	flagAnimation = 0x02
	flagXMP       = 0x04
	flagEXIF      = 0x08
	flagICC       = 0x20
)

// Parse reads the chunks of a WebP file (RFC 9649 section 2) and the headers of its bitstreams, without decoding them.
// Data after the RIFF chunk is ignored. The File refers to data, which must not change while the File is in use.
func Parse(data []byte) (*File, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, errors.New("webp: not a WebP file")
	}
	size := binary.LittleEndian.Uint32(data[4:])
	switch {
	case size < 4 || size > math.MaxUint32-9: // the limit of RFC 9649 section 2.4
		return nil, fmt.Errorf("webp: invalid RIFF size %d", size)
	case uint64(size) > uint64(len(data)-8):
		return nil, fmt.Errorf("webp: RIFF size %d past the end of the data", size)
	}
	r := chunkReader(data[12 : 8+uint64(size)])
	if len(r) == 0 {
		return nil, errors.New("webp: no chunks")
	}
	id, payload, err := r.next()
	if err != nil {
		return nil, err
	}
	f := new(File)
	switch id {
	case "VP8 ", "VP8L":
		// The simple formats: the chunks after the image are ignored.
		fr, err := newFrame(id, payload, nil)
		if err != nil {
			return nil, err
		}
		f.Width, f.Height, f.Lossless, f.Alpha = fr.Width, fr.Height, fr.lossless, fr.hasAlpha
		f.Frames = []Frame{fr}
		return f, nil
	case "VP8X":
		if err := f.parseExtended(payload, r); err != nil {
			return nil, err
		}
		return f, nil
	}
	return nil, fmt.Errorf("webp: first chunk %q is not VP8, VP8L or VP8X", id)
}

// chunkReader splits RIFF chunks, which are padded to an even size.
type chunkReader []byte

// next returns the FourCC and the payload of the next chunk.
func (r *chunkReader) next() (id string, payload []byte, err error) {
	b := *r
	if len(b) < 8 {
		return "", nil, errors.New("webp: truncated chunk header")
	}
	n := uint64(binary.LittleEndian.Uint32(b[4:]))
	end := 8 + n + n&1
	if end > uint64(len(b)) {
		return "", nil, fmt.Errorf("webp: %q chunk of %d bytes runs past its parent", b[:4], n)
	}
	*r = b[end:]
	return string(b[:4]), b[8 : 8+n : 8+n], nil
}

// parseExtended reads the extended format (RFC 9649 section 2.7) from the payload of VP8X and the chunks after it.
func (f *File) parseExtended(vp8x []byte, r chunkReader) error {
	if len(vp8x) < 10 {
		return errors.New("webp: VP8X chunk too short")
	}
	// Reserved bits and fields after the canvas size are ignored.
	f.flags = vp8x[0]
	p := extendedParser{f: f}
	f.Width, f.Height = 1+uint24(vp8x[4:]), 1+uint24(vp8x[7:])
	if uint64(f.Width)*uint64(f.Height) > math.MaxUint32 {
		return fmt.Errorf("webp: canvas of %dx%d pixels too large", f.Width, f.Height)
	}
	f.Animated = f.flags&flagAnimation != 0
	for len(r) > 0 {
		id, payload, err := r.next()
		if err == nil {
			err = p.chunk(id, payload)
		}
		if err != nil {
			return err
		}
	}
	switch {
	case f.Animated && !p.anim:
		return errors.New("webp: animation without an ANIM chunk")
	case len(f.Frames) == 0:
		return errors.New("webp: no image data")
	}
	return nil
}

type extendedParser struct {
	f              *File
	started        bool   // ANIM or the image data has been read
	anim           bool   // ANIM has been read
	alph           []byte // the ALPH payload of a still image
	icc, exif, xmp bool
}

// chunk reads a chunk after VP8X. Chunks for reconstruction must be in the order of RFC 9649 section 2.7, while
// metadata and unknown chunks may be anywhere.
func (p *extendedParser) chunk(id string, payload []byte) error {
	f := p.f
	switch id {
	case "VP8X":
		return errors.New("webp: second VP8X chunk")
	case "ICCP":
		if f.flags&flagICC == 0 || p.icc {
			return nil
		}
		if p.started {
			return errors.New("webp: ICCP chunk after the image data")
		}
		f.ICC, p.icc = payload, true
	case "EXIF":
		if f.flags&flagEXIF != 0 && !p.exif {
			f.EXIF, p.exif = payload, true
		}
	case "XMP ":
		if f.flags&flagXMP != 0 && !p.xmp {
			f.XMP, p.xmp = payload, true
		}
	case "ANIM":
		switch {
		case !f.Animated:
			return nil
		case p.started:
			return errors.New("webp: ANIM chunk out of order")
		case len(payload) < 6:
			return errors.New("webp: ANIM chunk too short")
		}
		copy(f.Background[:], payload)
		f.LoopCount = int(binary.LittleEndian.Uint16(payload[4:]))
		p.started, p.anim = true, true
	case "ANMF":
		if !f.Animated {
			return nil
		}
		if !p.anim {
			return errors.New("webp: ANMF chunk before ANIM")
		}
		fr, err := parseANMF(payload, f.Width, f.Height)
		if err != nil {
			return err
		}
		f.Frames = append(f.Frames, fr)
		f.Alpha = f.Alpha || fr.hasAlpha
	case "ALPH", "VP8 ", "VP8L":
		if f.Animated {
			return fmt.Errorf("webp: %q chunk outside ANMF in an animation", id)
		}
		if len(f.Frames) > 0 {
			return nil
		}
		p.started = true
		if id == "ALPH" {
			if p.alph != nil {
				return errors.New("webp: two ALPH chunks")
			}
			p.alph = payload
			return nil
		}
		fr, err := newFrame(id, payload, p.alph)
		if err != nil {
			return err
		}
		if fr.Width != f.Width || fr.Height != f.Height {
			return fmt.Errorf("webp: image of %dx%d pixels on a canvas of %dx%d", fr.Width, fr.Height, f.Width,
				f.Height)
		}
		f.Frames = []Frame{fr}
		f.Lossless, f.Alpha = fr.lossless, fr.hasAlpha
	}
	return nil
}

// parseANMF reads the payload of an ANMF chunk: the frame's position, size, duration and methods (RFC 9649 section
// 2.7.1.1), then ALPH, VP8 or VP8L, and unknown chunks.
func parseANMF(b []byte, canvasWidth, canvasHeight int) (Frame, error) {
	if len(b) < 16 {
		return Frame{}, errors.New("webp: ANMF chunk too short")
	}
	x, y := 2*uint24(b), 2*uint24(b[3:])
	w, h := 1+uint24(b[6:]), 1+uint24(b[9:])
	if x+w > canvasWidth || y+h > canvasHeight {
		return Frame{}, fmt.Errorf("webp: frame of %dx%d pixels at (%d, %d) outside the canvas of %dx%d", w, h, x, y,
			canvasWidth, canvasHeight)
	}
	var (
		fr    Frame
		alph  []byte
		found bool
	)
	r := chunkReader(b[16:])
	for len(r) > 0 {
		id, payload, err := r.next()
		if err != nil {
			return Frame{}, err
		}
		switch id {
		case "ALPH":
			switch {
			case found:
				return Frame{}, errors.New("webp: ALPH chunk after the bitstream of a frame")
			case alph != nil:
				return Frame{}, errors.New("webp: two ALPH chunks in a frame")
			}
			alph = payload
		case "VP8 ", "VP8L":
			if found {
				return Frame{}, errors.New("webp: two bitstreams in a frame")
			}
			if fr, err = newFrame(id, payload, alph); err != nil {
				return Frame{}, err
			}
			found = true
		}
	}
	switch {
	case !found:
		return Frame{}, errors.New("webp: frame without a bitstream")
	case fr.Width != w || fr.Height != h:
		return Frame{}, fmt.Errorf("webp: bitstream of %dx%d pixels in a frame of %dx%d", fr.Width, fr.Height, w, h)
	}
	fr.X, fr.Y, fr.Duration = x, y, uint24(b[12:])
	fr.Blend, fr.Dispose = b[15]&2 == 0, b[15]&1 != 0
	return fr, nil
}

// newFrame returns a frame of the size in the header of the VP8 or VP8L bitstream data. alph is the payload of the ALPH
// chunk before it, which VP8L ignores.
func newFrame(id string, data, alph []byte) (Frame, error) {
	fr := Frame{bitstream: data}
	var err error
	if id == "VP8L" {
		fr.Width, fr.Height, fr.hasAlpha, err = vp8lHeader(data)
		fr.lossless = true
	} else {
		fr.Width, fr.Height, err = vp8Header(data)
		fr.alphaData, fr.hasAlpha = alph, alph != nil
	}
	return fr, err
}

// vp8Header reads the frame tag and the start of the key frame header of a VP8 bitstream (RFC 6386 section 9.1).
func vp8Header(b []byte) (w, h int, err error) {
	if len(b) < 10 {
		return 0, 0, errors.New("webp: VP8 bitstream too short")
	}
	tag := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	switch {
	case tag&1 != 0:
		return 0, 0, errors.New("webp: VP8 frame is not a key frame")
	case tag>>1&7 > 3:
		return 0, 0, fmt.Errorf("webp: unknown VP8 version %d", tag>>1&7)
	case tag>>4&1 == 0:
		return 0, 0, errors.New("webp: VP8 frame is not shown")
	case int(tag>>5) > len(b)-10:
		return 0, 0, errors.New("webp: first VP8 partition past the end of the data")
	case b[3] != 0x9d || b[4] != 0x01 || b[5] != 0x2a:
		return 0, 0, errors.New("webp: missing VP8 start code")
	}
	// The top 2 bits of each are scaling factors, which do not change the size.
	w, h = int(binary.LittleEndian.Uint16(b[6:])&0x3fff), int(binary.LittleEndian.Uint16(b[8:])&0x3fff)
	if w == 0 || h == 0 {
		return 0, 0, fmt.Errorf("webp: VP8 frame of %dx%d pixels", w, h)
	}
	return w, h, nil
}

// vp8lHeader reads the header of a VP8L bitstream (RFC 9649 section 3.4).
func vp8lHeader(b []byte) (w, h int, alpha bool, err error) {
	if len(b) < 5 {
		return 0, 0, false, errors.New("webp: VP8L bitstream too short")
	}
	if b[0] != 0x2f {
		return 0, 0, false, errors.New("webp: missing VP8L signature")
	}
	// From the least significant bit: 14 bits of the width - 1, 14 of the height - 1, alpha_is_used and 3 of the
	// version.
	bits := binary.LittleEndian.Uint32(b[1:])
	if v := bits >> 29; v != 0 {
		return 0, 0, false, fmt.Errorf("webp: unknown VP8L version %d", v)
	}
	return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, bits>>28&1 != 0, nil
}

func uint24(b []byte) int {
	return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
}
