// Package mediafile reads the format, the size and the orientation of media files from their contents.
package mediafile

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
)

const (
	JPEG = "image/jpeg"
	PNG  = "image/png"
	WebP = "image/webp"
	GIF  = "image/gif"
	PDF  = "application/pdf"
)

// Formats returns the MIME types that Probe returns, in a new slice.
func Formats() []string {
	return []string{JPEG, PNG, WebP, GIF, PDF}
}

type Info struct {
	MIMEType      string
	Width, Height int // images: in pixels as displayed, with Orientation applied; 0 for PDF
	Orientation   int // images: the EXIF orientation from 1 to 8, 1 if there is none; 0 for PDF
}

// ErrUnsupported means that a file is in none of Formats, such as SVG, HEIC, AVIF, BMP, TIFF, HTML or an empty file.
var ErrUnsupported = errors.New("unsupported file format")

// Probe reads the Info of a file from the start of r without decoding image data, and does not close r.
// Errors from r are returned as they are.
//
// The orientation is read only from the first 128 KiB of JPEG and PNG files and from the first 64 KiB of the EXIF
// chunk of WebP files; Orientation is 1 if it lies beyond them.
func Probe(r io.Reader) (Info, error) {
	er := &errReader{r: r}
	info, err := probe(bufio.NewReaderSize(er, 128<<10))
	if er.err != nil {
		return Info{}, er.err
	}
	return info, err
}

func probe(r *bufio.Reader) (Info, error) {
	b, err := r.Peek(12) // the longest signature below
	if err != nil && !errors.Is(err, io.EOF) {
		return Info{}, err
	}
	head := string(b)
	switch {
	case strings.HasPrefix(head, "\xff\xd8\xff"):
		return imageInfo(r, JPEG, "JPEG", jpeg.DecodeConfig, jpegOrientation)
	case strings.HasPrefix(head, "\x89PNG\r\n\x1a\n"):
		return imageInfo(r, PNG, "PNG", png.DecodeConfig, pngOrientation)
	case isWebP(head):
		return webpInfo(r)
	case strings.HasPrefix(head, "GIF87a"), strings.HasPrefix(head, "GIF89a"):
		return imageInfo(r, GIF, "GIF", gif.DecodeConfig, nil) // GIF has no Exif
	case isPDF(head):
		return Info{MIMEType: PDF}, nil
	}
	return Info{}, ErrUnsupported
}

// imageInfo reads the size with decodeConfig and, unless readOrientation is nil, the orientation from the start of
// the file in the buffer of r.
func imageInfo(
	r *bufio.Reader, mimeType, name string, decodeConfig func(io.Reader) (image.Config, error), readOrientation func([]byte) int,
) (Info, error) {
	o := 1
	if readOrientation != nil {
		// Peek does not advance r, so decodeConfig still reads from the start.
		b, err := r.Peek(r.Size())
		if err != nil && !errors.Is(err, io.EOF) {
			return Info{}, err
		}
		o = readOrientation(b)
	}
	c, err := decodeConfig(r)
	if err != nil {
		return Info{}, fmt.Errorf("invalid %s: %w", name, err)
	}
	if c.Width <= 0 || c.Height <= 0 {
		return Info{}, fmt.Errorf("invalid %s: zero width or height (%dx%d)", name, c.Width, c.Height)
	}
	return displayInfo(mimeType, c.Width, c.Height, o), nil
}

// displayInfo returns the Info of an image of w by h pixels in orientation o; orientations 5 to 8 turn it by 90 degrees.
func displayInfo(mimeType string, w, h, o int) Info {
	if o >= 5 {
		w, h = h, w
	}
	return Info{MIMEType: mimeType, Width: w, Height: h, Orientation: o}
}

// jpegOrientation reads the first APP1 segment with Exif in b, the start of a JPEG file. It stops at the frame header
// (SOF), before which Exif comes, and at any marker other than APPn, COM, DQT, DHT and DRI, which image/jpeg accepts
// before it.
func jpegOrientation(b []byte) int {
	for i := 2; i+4 <= len(b) && b[i] == 0xff; {
		marker := b[i+1]
		switch {
		case marker == 0xff: // a fill byte (ITU T.81 B.1.1.2)
			i++
			continue
		case 0xe0 <= marker && marker <= 0xef, marker == 0xfe, marker == 0xdb, marker == 0xc4, marker == 0xdd:
			// APPn, COM, DQT, DHT or DRI
		default:
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2:])) // including these 2 bytes
		if n < 2 {
			return 1
		}
		if seg := b[i+4 : min(i+2+n, len(b))]; marker == 0xe1 && bytes.HasPrefix(seg, []byte(exifHeader)) {
			return orientation(seg[len(exifHeader):])
		}
		i += 2 + n
	}
	return 1
}

// pngOrientation reads the eXIf chunk in b, the start of a PNG file. It stops at the first IDAT, as eXIf comes before it
// (PNG Third Edition 5.6).
func pngOrientation(b []byte) int {
	for i := 8; i+8 <= len(b); { // after the signature
		n := binary.BigEndian.Uint32(b[i:])
		data := b[i+8:]
		if uint64(n) < uint64(len(data)) {
			data = data[:n]
		}
		switch string(b[i+4 : i+8]) {
		case "IDAT":
			return 1
		case "eXIf":
			return orientation(data)
		}
		if uint64(len(b)-i) < 12+uint64(n) {
			return 1
		}
		i += 12 + int(n)
	}
	return 1
}

// isPDF reports whether head starts with a PDF header such as "%PDF-1.7".
func isPDF(head string) bool {
	return len(head) >= 8 && head[:5] == "%PDF-" && isDigit(head[5]) && head[6] == '.' && isDigit(head[7])
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

// errReader keeps the first error from r other than io.EOF, as the decoders may wrap it with %v.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}
