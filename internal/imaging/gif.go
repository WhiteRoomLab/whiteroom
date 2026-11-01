package imaging

import (
	"compress/lzw"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The blocks of GIF files (GIF89a specification), which Inspect scans, decodeGIF reads up to the first image, and
// rebuildGIF writes again. All of them stop where browsers stop: at the trailer, at the end of the data, or at a byte
// that does not start a block.

const (
	gifExtensionBlock = 0x21
	gifImageBlock     = 0x2c
	gifTrailer        = 0x3b
	gifControlLabel   = 0xf9
	gifAppLabel       = 0xff
)

var errGIFTruncated = errors.New("truncated GIF")

// gifStart returns the index of the first block of a GIF file, after its header, its logical screen descriptor and
// its global color table.
func gifStart(data []byte) (int, error) {
	if len(data) < 13 || string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a" {
		return 0, errors.New("not a GIF file")
	}
	i := 13
	if data[10]&0x80 != 0 {
		i += 3 << (data[10]&7 + 1)
	}
	if i > len(data) {
		return 0, errGIFTruncated
	}
	return i, nil
}

// gifBlock is an extension or an image of a GIF file.
type gifBlock struct {
	kind byte // gifExtensionBlock or gifImageBlock
	pos  int  // of the introducer
	subs int  // of the data sub-blocks: after the label of an extension, or after the LZW minimum code size of an image
	end  int  // after the block terminator
}

// nextGIFBlock returns the block at i in data, a GIF file. It returns io.EOF at the trailer, at the end of data, or at
// a byte that does not start a block, and errGIFTruncated if data ends inside the block.
func nextGIFBlock(data []byte, i int) (gifBlock, error) {
	if i >= len(data) {
		return gifBlock{}, io.EOF
	}
	b := gifBlock{kind: data[i], pos: i}
	switch b.kind {
	case gifExtensionBlock:
		b.subs = i + 2
	case gifImageBlock:
		if len(data)-i < 10 {
			return gifBlock{}, errGIFTruncated
		}
		b.subs = i + 11 // after the descriptor and the minimum code size
		if data[i+9]&0x80 != 0 {
			b.subs += 3 << (data[i+9]&7 + 1)
		}
	default:
		return gifBlock{}, io.EOF
	}
	end, ok := skipSubBlocks(data, b.subs)
	if !ok {
		return gifBlock{}, errGIFTruncated
	}
	b.end = end
	return b, nil
}

// skipSubBlocks returns the index after the data sub-blocks at i in data, and false if they do not end in data.
func skipSubBlocks(data []byte, i int) (int, bool) {
	for i < len(data) {
		n := int(data[i])
		i += 1 + n
		if n == 0 {
			return i, true
		}
	}
	return 0, false
}

// gifFrame is the image descriptor of an image block.
type gifFrame struct {
	x, y, w, h  int
	interlaced  bool
	table       []byte // the local color table; nil if none
	minCodeSize int
}

func gifFrameOf(data []byte, b gifBlock) gifFrame {
	d := data[b.pos+1 : b.pos+10]
	le := binary.LittleEndian
	f := gifFrame{
		x: int(le.Uint16(d)), y: int(le.Uint16(d[2:])), w: int(le.Uint16(d[4:])), h: int(le.Uint16(d[6:])),
		interlaced: d[8]&0x40 != 0, minCodeSize: int(data[b.subs-1]),
	}
	if d[8]&0x80 != 0 {
		f.table = data[b.pos+10 : b.subs-1]
	}
	return f
}

// gifScan is what scanGIF reads from the blocks of a GIF file.
type gifScan struct {
	frames   int
	pixels   int64 // the sum of the areas of the frames
	complete bool  // the blocks are not truncated
}

// scanGIF reads the blocks of a GIF file that gif.DecodeConfig has read. Frames whose LZW minimum code size is not
// from 2 to 8, which neither image/gif nor compress/lzw read, and a first frame of more than content.MaxImagePixels are
// errors.
func scanGIF(data []byte) (gifScan, error) {
	i, err := gifStart(data)
	if err != nil {
		return gifScan{}, fmt.Errorf("invalid GIF: %w", err)
	}
	var s gifScan
	for {
		b, err := nextGIFBlock(data, i)
		if err != nil {
			s.complete = errors.Is(err, io.EOF)
			return s, nil
		}
		if b.kind == gifImageBlock {
			f := gifFrameOf(data, b)
			switch {
			case f.minCodeSize < 2 || f.minCodeSize > 8:
				return gifScan{}, fmt.Errorf("invalid GIF: LZW minimum code size %d", f.minCodeSize)
			case s.frames == 0 && f.h > 0 && f.w > content.MaxImagePixels/f.h:
				return gifScan{}, fmt.Errorf("too many pixels in the first GIF frame: %dx%d (max %d)", f.w, f.h,
					content.MaxImagePixels)
			}
			s.frames++
			s.pixels += int64(f.w) * int64(f.h)
		}
		i = b.end
	}
}

// subBlocks reads the bytes of the data sub-blocks that start at data[i], up to the block terminator.
type subBlocks struct {
	data []byte
	i    int // of the next byte
	n    int // bytes left in the sub-block
}

func (s *subBlocks) ReadByte() (byte, error) {
	for s.n == 0 {
		if s.i >= len(s.data) || s.data[s.i] == 0 {
			return 0, io.EOF
		}
		s.n = int(s.data[s.i])
		s.i++
	}
	if s.i >= len(s.data) {
		return 0, io.ErrUnexpectedEOF
	}
	c := s.data[s.i]
	s.i++
	s.n--
	return c, nil
}

// Read is there for lzw.NewReader, which reads with ReadByte.
func (s *subBlocks) Read(p []byte) (int, error) {
	for k := range p {
		c, err := s.ReadByte()
		if err != nil {
			if k > 0 {
				return k, nil
			}
			return 0, err
		}
		p[k] = c
	}
	return len(p), nil
}

// subBlockWriter appends what compress/lzw writes to out in data sub-blocks of at most 255 bytes.
type subBlockWriter struct {
	out []byte
	buf [255]byte
	n   int
}

func (w *subBlockWriter) WriteByte(c byte) error {
	w.buf[w.n] = c
	w.n++
	if w.n == len(w.buf) {
		return w.Flush()
	}
	return nil
}

func (w *subBlockWriter) Write(p []byte) (int, error) {
	for _, c := range p {
		if err := w.WriteByte(c); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *subBlockWriter) Flush() error {
	if w.n > 0 {
		w.out = append(append(w.out, byte(w.n)), w.buf[:w.n]...)
		w.n = 0
	}
	return nil
}

// gifPasses are the first rows and the steps of the passes of an interlaced image, and of one that is not.
var (
	gifInterlacedPasses = [][2]int{{0, 8}, {4, 8}, {2, 4}, {1, 2}}
	gifPasses           = [][2]int{{0, 1}}
)

// decodeGIF returns the first frame of a GIF file drawn on a transparent canvas of its logical screen, as browsers show
// it: an *image.Paletted if the frame covers the screen, else an *image.NRGBA. The part of the frame outside the
// screen is cut off, and pixels that the frame does not have are transparent: those whose index is past its color
// table, those after its data ends, and all of them if it has no color table. A frame of no width or height has no
// pixels.
func decodeGIF(data []byte) (image.Image, error) {
	i, err := gifStart(data)
	if err != nil {
		return nil, err
	}
	w, h := int(binary.LittleEndian.Uint16(data[6:])), int(binary.LittleEndian.Uint16(data[8:]))
	var global []byte
	if data[10]&0x80 != 0 {
		global = data[13:i]
	}
	transparent := -1
	for {
		b, err := nextGIFBlock(data, i)
		switch {
		case errors.Is(err, io.EOF):
			return nil, errors.New("invalid GIF: no image data")
		case err != nil:
			return nil, err
		case b.kind == gifImageBlock:
			return drawGIFFrame(data, b, w, h, global, transparent)
		case data[b.pos+1] == gifControlLabel && data[b.subs] >= 4 && data[b.subs+1]&1 != 0:
			// As image/gif and Chromium do, a control extension without a transparent color does not undo one before.
			transparent = int(data[b.subs+4])
		}
		i = b.end
	}
}

// drawGIFFrame draws the frame of the image block b on a transparent canvas of w x h pixels, as decodeGIF does.
func drawGIFFrame(data []byte, b gifBlock, w, h int, global []byte, transparent int) (image.Image, error) {
	f := gifFrameOf(data, b)
	if f.minCodeSize < 2 || f.minCodeSize > 8 {
		return nil, fmt.Errorf("invalid GIF: LZW minimum code size %d", f.minCodeSize)
	}
	table := f.table
	if table == nil {
		table = global
	}
	// The part of the frame on the screen.
	vw, vh := min(f.w, w-f.x), min(f.h, h-f.y)
	screen := image.Rect(0, 0, w, h)
	if table == nil || vw <= 0 || vh <= 0 {
		return image.NewNRGBA(screen), nil
	}
	pal := make(color.Palette, len(table)/3, 256)
	for k := range pal {
		pal[k] = color.RGBA{table[3*k], table[3*k+1], table[3*k+2], 0xff}
	}
	if transparent >= 0 {
		for len(pal) <= transparent {
			pal = append(pal, color.RGBA{})
		}
		pal[transparent] = color.RGBA{}
	}
	m := image.NewPaletted(screen, pal)
	done := make([]bool, vh) // the rows on the screen that the data has
	lr := lzw.NewReader(&subBlocks{data: data[:b.end], i: b.subs}, lzw.LSB, f.minCodeSize)
	row := make([]byte, f.w)
	passes := gifPasses
	if f.interlaced {
		passes = gifInterlacedPasses
	}
	maxIndex := 0
rows:
	for p, pass := range passes {
		for y := pass[0]; y < f.h; y += pass[1] {
			if y >= vh && p == len(passes)-1 {
				break rows // the rest of the frame is below the screen
			}
			if _, err := io.ReadFull(lr, row); err != nil {
				break rows
			}
			if y >= vh {
				continue
			}
			done[y] = true
			for _, c := range row[:vw] {
				maxIndex = max(maxIndex, int(c))
			}
			copy(m.Pix[m.PixOffset(f.x, f.y+y):], row[:vw])
		}
	}
	covers := f.x == 0 && f.y == 0 && vw == w && vh == h
	for _, d := range done {
		covers = covers && d
	}
	if covers {
		for len(m.Palette) <= maxIndex {
			m.Palette = append(m.Palette, color.RGBA{})
		}
		return m, nil
	}
	var lut [256][4]byte // the colors of the indexes, transparent past the palette
	for k, c := range m.Palette {
		n := color.NRGBAModel.Convert(c).(color.NRGBA)
		lut[k] = [4]byte{n.R, n.G, n.B, n.A}
	}
	out := image.NewNRGBA(screen)
	for y, d := range done {
		if !d {
			continue
		}
		src := m.Pix[m.PixOffset(f.x, f.y+y):][:vw]
		dst := out.Pix[out.PixOffset(f.x, f.y+y):][:4*vw]
		for k, c := range src {
			copy(dst[4*k:4*k+4], lut[c][:])
		}
	}
	return out, nil
}

// rebuildGIF returns the GIF animation in data written again with only what shows it, as browsers show it: "GIF89a",
// the logical screen descriptor without a pixel aspect ratio and the global color table; the graphic control
// extensions; the loop count of NETSCAPE2.0 and ANIMEXTS1.0 extensions, as NETSCAPE2.0; and the images, whose LZW data
// is decoded into exactly width x height indexes and encoded again. Comments, plain text, other application extensions
// and unknown extensions are left out, and so is what comes after the blocks. Truncated data and an image whose data
// has fewer indexes are errors, which Inspect finds first.
func rebuildGIF(data []byte) ([]byte, error) {
	i, err := gifStart(data)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(data))
	out = append(append(out, "GIF89a"...), data[6:12]...)
	out = append(append(out, 0), data[13:i]...)
	var (
		lr  lzw.Reader
		lw  lzw.Writer
		buf = make([]byte, 32<<10)
	)
	for frame := 0; ; {
		b, err := nextGIFBlock(data, i)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		i = b.end
		if b.kind == gifExtensionBlock {
			switch data[b.pos+1] {
			case gifControlLabel:
				if data[b.subs] >= 4 {
					out = append(append(out, gifExtensionBlock, gifControlLabel, 4), data[b.subs+1:b.subs+5]...)
					out = append(out, 0)
				}
			case gifAppLabel:
				if loop, ok := gifLoopCount(data, b); ok {
					out = append(append(out, gifExtensionBlock, gifAppLabel, 11), "NETSCAPE2.0"...)
					out = append(binary.LittleEndian.AppendUint16(append(out, 3, 1), loop), 0)
				}
			}
			continue
		}
		f := gifFrameOf(data, b)
		if f.minCodeSize < 2 || f.minCodeSize > 8 {
			return nil, fmt.Errorf("invalid GIF: LZW minimum code size %d", f.minCodeSize)
		}
		out = append(out, data[b.pos:b.subs]...) // the descriptor, the local color table and the minimum code size
		sw := &subBlockWriter{out: out}
		lw.Reset(sw, lzw.LSB, f.minCodeSize)
		if err := copyGIFIndexes(&lw, &lr, data, b, frame, buf); err != nil {
			return nil, err
		}
		if err := lw.Close(); err != nil {
			return nil, err
		}
		out = append(sw.out, 0)
		frame++
	}
	return append(out, gifTrailer), nil
}

// copyGIFIndexes decodes the LZW data of the image block b of data, a GIF file, which is its frame number frame, with
// lr, and copies the first width x height indexes to w with buf. Fewer of them is an error; more are not read.
func copyGIFIndexes(w io.Writer, lr *lzw.Reader, data []byte, b gifBlock, frame int, buf []byte) error {
	f := gifFrameOf(data, b)
	lr.Reset(&subBlocks{data: data[:b.end], i: b.subs}, lzw.LSB, f.minCodeSize)
	want := int64(f.w) * int64(f.h)
	n, err := io.CopyBuffer(w, io.LimitReader(lr, want), buf)
	if err == nil && n < want {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return fmt.Errorf("invalid GIF: frame %d has %d of %d pixels: %w", frame, n, want, err)
	}
	return nil
}

// checkGIFFrames returns the error that rebuildGIF would return for the LZW data of a frame of data, a GIF file that
// scanGIF reads without errors, or nil. It decodes the data of every frame, but keeps none of the indexes.
func checkGIFFrames(data []byte) error {
	i, err := gifStart(data)
	if err != nil {
		return err
	}
	var lr lzw.Reader
	for frame := 0; ; {
		b, err := nextGIFBlock(data, i)
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		case b.kind == gifImageBlock:
			if err := copyGIFIndexes(io.Discard, &lr, data, b, frame, nil); err != nil {
				return err
			}
			frame++
		}
		i = b.end
	}
}

// gifLoopCount returns the loop count in b, an application extension, if it is a NETSCAPE2.0 or ANIMEXTS1.0 extension
// with a looping sub-block, whose ID is 1 in its low 3 bits as browsers read it; of several, the last.
func gifLoopCount(data []byte, b gifBlock) (uint16, bool) {
	if id := data[b.subs:]; id[0] != 11 || string(id[1:12]) != "NETSCAPE2.0" && string(id[1:12]) != "ANIMEXTS1.0" {
		return 0, false
	}
	var loop uint16
	found := false
	for j := b.subs + 12; data[j] != 0; j += 1 + int(data[j]) {
		if data[j] >= 3 && data[j+1]&7 == 1 {
			loop, found = binary.LittleEndian.Uint16(data[j+2:]), true
		}
	}
	return loop, found
}
