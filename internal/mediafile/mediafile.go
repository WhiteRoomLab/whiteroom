// Package mediafile reads the format, the size and the orientation of media files from their contents.
package mediafile

import (
	"bufio"
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
		return imageInfo(r, JPEG, "JPEG", jpeg.DecodeConfig)
	case strings.HasPrefix(head, "\x89PNG\r\n\x1a\n"):
		return imageInfo(r, PNG, "PNG", png.DecodeConfig)
	case isWebP(head):
		return webpInfo(r)
	case strings.HasPrefix(head, "GIF87a"), strings.HasPrefix(head, "GIF89a"):
		return imageInfo(r, GIF, "GIF", gif.DecodeConfig)
	case isPDF(head):
		return Info{MIMEType: PDF}, nil
	}
	return Info{}, ErrUnsupported
}

func imageInfo(r io.Reader, mimeType, name string, decodeConfig func(io.Reader) (image.Config, error)) (Info, error) {
	c, err := decodeConfig(r)
	if err != nil {
		return Info{}, fmt.Errorf("invalid %s: %w", name, err)
	}
	if c.Width <= 0 || c.Height <= 0 {
		return Info{}, fmt.Errorf("invalid %s: zero width or height (%dx%d)", name, c.Width, c.Height)
	}
	return Info{MIMEType: mimeType, Width: c.Width, Height: c.Height, Orientation: 1}, nil
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
