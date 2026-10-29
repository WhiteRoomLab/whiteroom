package mediafile

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

func isWebP(head string) bool {
	return len(head) >= 12 && head[:4] == "RIFF" && head[8:12] == "WEBP"
}

func webpInfo(r *bufio.Reader) (Info, error) {
	w, h, o, err := webpSize(r)
	if err != nil {
		return Info{}, fmt.Errorf("invalid WebP: %w", err)
	}
	return displayInfo(WebP, w, h, o), nil
}

// webpSize returns the width, the height and the orientation.
func webpSize(r *bufio.Reader) (int, int, int, error) {
	var h [12]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, 0, 0, noEOF(err)
	}
	size := binary.LittleEndian.Uint32(h[4:])
	if size > math.MaxUint32-9 { // the limit in RFC 9649 2.4
		return 0, 0, 0, fmt.Errorf("RIFF size too large (%d)", size)
	}
	// The RIFF size counts "WEBP" and the chunks after it.
	c := &riffReader{r: r, left: int64(size) - 4}
	err := c.next()
	if errors.Is(err, io.EOF) {
		return 0, 0, 0, errors.New("no chunks")
	}
	if err != nil {
		return 0, 0, 0, err
	}
	// Only the extended format, with VP8X, has metadata.
	switch c.fourCC {
	case "VP8 ":
		width, height, err := vp8Size(c)
		return width, height, 1, err
	case "VP8L":
		width, height, err := vp8lSize(c)
		return width, height, 1, err
	case "VP8X":
		return vp8xSize(c)
	}
	return 0, 0, 0, fmt.Errorf("first chunk %q is not VP8, VP8L or VP8X", c.fourCC)
}

// vp8Size reads the frame header of a VP8 key frame (RFC 6386 9.1).
func vp8Size(c *riffReader) (int, int, error) {
	var b [10]byte
	if err := c.read(b[:]); err != nil {
		return 0, 0, err
	}
	if b[0]&1 != 0 { // 0 for a key frame
		return 0, 0, errors.New("VP8 frame is not a key frame")
	}
	if b[3] != 0x9d || b[4] != 0x01 || b[5] != 0x2a {
		return 0, 0, errors.New("missing VP8 start code")
	}
	// The top 2 bits are for upscaling, which the size does not include.
	w := int(binary.LittleEndian.Uint16(b[6:]) & 0x3fff)
	h := int(binary.LittleEndian.Uint16(b[8:]) & 0x3fff)
	if w == 0 || h == 0 {
		return 0, 0, fmt.Errorf("zero width or height (%dx%d)", w, h)
	}
	return w, h, nil
}

// vp8lSize reads the header of a VP8L bitstream (RFC 9649 3.4).
func vp8lSize(c *riffReader) (int, int, error) {
	var b [5]byte
	if err := c.read(b[:]); err != nil {
		return 0, 0, err
	}
	if b[0] != 0x2f {
		return 0, 0, errors.New("missing VP8L signature")
	}
	// From the least significant bit: 14 bits of the width - 1, 14 of the height - 1, 1 of alpha and 3 of the version.
	bits := binary.LittleEndian.Uint32(b[1:])
	if v := bits >> 29; v != 0 {
		return 0, 0, fmt.Errorf("unknown VP8L version %d", v)
	}
	return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, nil
}

// vp8xSize reads the canvas size in VP8X and checks that the image data follows (RFC 9649 2.7). If the flags say that
// there is Exif, it also reads the orientation in the first EXIF chunk, which may come before or after the image data.
func vp8xSize(c *riffReader) (int, int, int, error) {
	var b [10]byte // the chunk may be longer, as later versions may add fields
	if err := c.read(b[:]); err != nil {
		return 0, 0, 0, err
	}
	animation := b[0]&0x02 != 0 // the flags are Rsv Rsv I L E X A R from the most significant bit
	exif := b[0]&0x08 != 0      // cleared once the EXIF chunk is read
	w := int(uint24(b[4:])) + 1
	h := int(uint24(b[7:])) + 1
	if uint64(w)*uint64(h) > math.MaxUint32 { // the limit in RFC 9649 2.7
		return 0, 0, 0, fmt.Errorf("canvas too large (%dx%d)", w, h)
	}
	found, o := false, 1
	for !found || exif {
		err := c.next()
		if errors.Is(err, io.EOF) {
			if found {
				break
			}
			return 0, 0, 0, errors.New("no image chunk")
		}
		if err != nil {
			return 0, 0, 0, err
		}
		// A still image has VP8 or VP8L, and an animation has ANIM, which readers ignore in a still image.
		switch c.fourCC {
		case "VP8 ", "VP8L":
			if !animation {
				found = true
			}
		case "ANIM":
			if animation {
				found = true
			}
		case "EXIF":
			if exif {
				exif = false
				d := make([]byte, min(c.unread, 64<<10)) // not all of it, which may be up to 4 GiB
				if err := c.read(d); err != nil {
					return 0, 0, 0, err
				}
				// Some writers keep the Exif ID code of JPEG.
				o = orientation(bytes.TrimPrefix(d, []byte(exifHeader)))
			}
		}
	}
	return w, h, o, nil
}

func uint24(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}

type riffReader struct {
	r      *bufio.Reader
	left   int64 // bytes in the RIFF chunk after the current chunk
	fourCC string
	unread int64 // bytes of the current chunk's data not read yet
	pad    int64 // 1 if the current chunk's data has an odd size
}

// next skips the rest of the current chunk and reads the header of the next one.
// It returns io.EOF at the end of the RIFF chunk.
func (c *riffReader) next() error {
	if err := discard(c.r, c.unread+c.pad); err != nil {
		return noEOF(err)
	}
	c.unread, c.pad = 0, 0
	var h [8]byte
	if c.left < int64(len(h)) {
		return io.EOF
	}
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return noEOF(err)
	}
	c.left -= int64(len(h))
	c.fourCC = string(h[:4])
	size := int64(binary.LittleEndian.Uint32(h[4:]))
	pad := size & 1
	// The RIFF size counts the padding too (RFC 9649 2.4).
	if size+pad > c.left {
		return fmt.Errorf("%q chunk of %d bytes exceeds the RIFF size", c.fourCC, size)
	}
	c.left -= size + pad
	c.unread, c.pad = size, pad
	return nil
}

// read reads the next len(p) bytes of the current chunk's data.
func (c *riffReader) read(p []byte) error {
	if int64(len(p)) > c.unread {
		return fmt.Errorf("%q chunk of %d bytes is too short", c.fourCC, c.unread)
	}
	if _, err := io.ReadFull(c.r, p); err != nil {
		return noEOF(err)
	}
	c.unread -= int64(len(p))
	return nil
}

// discard skips n bytes in steps, as n may not fit in an int on 32-bit platforms.
func discard(r *bufio.Reader, n int64) error {
	for n > 0 {
		k := int(min(n, math.MaxInt32))
		if _, err := r.Discard(k); err != nil {
			return err
		}
		n -= int64(k)
	}
	return nil
}

// noEOF returns io.ErrUnexpectedEOF for io.EOF, as the file ends inside the RIFF chunk.
func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
