// Command gen writes the originals of the media of the sample site in testdata/site, files/<hex>, and their documents,
// media/<ID>.json, after emptying both directories. The originals are images that it draws and a PDF that it writes,
// so that the same version of Go writes the same files; the documents get the hashes, the sizes, the formats and the
// dimensions of the originals.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

const siteDir = "testdata/site"

// uploader is the user who uploaded the media.
const uploader = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"

// Colors of the images, those of the presets of the example theme of internal/theme and others.
var (
	primary = color.NRGBA{0x1e, 0x73, 0xbe, 0xff}
	deep    = color.NRGBA{0x0b, 0x3a, 0x66, 0xff}
	muted   = color.NRGBA{0xf5, 0xf5, 0xf5, 0xff}
	white   = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	ink     = color.NRGBA{0x33, 0x33, 0x33, 0xff}
	skyTop  = color.NRGBA{0x7c, 0xb8, 0xe8, 0xff}
	skyLow  = color.NRGBA{0xe4, 0xf1, 0xfa, 0xff}
	ground  = color.NRGBA{0x7a, 0x8c, 0x6e, 0xff}
	wall    = color.NRGBA{0xd9, 0xdc, 0xe0, 0xff}
	glass   = color.NRGBA{0x3c, 0x5a, 0x82, 0xff}
)

// medium is a medium of the sample site: its document, which gen completes from its original, and its original.
type medium struct {
	doc  content.Media
	data func() ([]byte, error)
}

// media returns the media of the sample site: the image and the PDF of 6.11.5 of the concept, the avatar of the author
// of 6.11.4, and the logo, the icon and the image for Open Graph of the settings of 6.11.9.
func media() []medium {
	return []medium{
		{
			content.Media{
				ID:         "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5",
				FileName:   "会社外観.jpg",
				Alt:        content.Localized{"en": "Our office building", "ja": "会社外観"},
				Caption:    content.Localized{"ja": "本社の外観（2024年撮影）"},
				UploadedAt: "2026-10-25T10:00:00+09:00",
			},
			func() ([]byte, error) { return encodeJPEG(office(1200, 800)) },
		},
		{
			content.Media{
				ID:         "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9",
				FileName:   "yamada.png",
				Alt:        content.Localized{"en": "Taro Yamada", "ja": "山田 太郎"},
				UploadedAt: "2026-10-20T09:00:00+09:00",
			},
			func() ([]byte, error) { return encodePNG(avatar(256)) },
		},
		{
			content.Media{
				ID:         "med_01JBG3N6V3X6Z9C2E5G8J1K4M7",
				FileName:   "logo.png",
				Alt:        content.Localized{"en": "Example Inc.", "ja": "株式会社〇〇"},
				UploadedAt: "2026-10-20T09:00:00+09:00",
			},
			func() ([]byte, error) { return encodePNG(logo(400, 100)) },
		},
		{
			content.Media{
				ID:         "med_01JBG3N6W4Y7B0D3F6H9K2M5P8",
				FileName:   "icon.png",
				UploadedAt: "2026-10-20T09:00:00+09:00",
			},
			func() ([]byte, error) { return encodePNG(icon(512)) },
		},
		{
			content.Media{
				ID:         "med_01JBG3N6X5Z8C1E4G7J0K3N6Q9",
				FileName:   "ogp.jpg",
				Alt:        content.Localized{"ja": "株式会社〇〇のロゴ"},
				UploadedAt: "2026-10-20T09:00:00+09:00",
			},
			func() ([]byte, error) { return encodeJPEG(ogp(1200, 630)) },
		},
		{
			content.Media{
				ID:         "med_01JB8Z3K5TM8A1C5F9V3J6K2N7",
				FileName:   "会社案内.pdf",
				UploadedAt: "2026-10-25T10:05:00+09:00",
			},
			func() ([]byte, error) { return guide(), nil },
		},
	}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("gen: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	files, docs := filepath.Join(siteDir, "files"), filepath.Join(siteDir, "media")
	for _, dir := range []string{files, docs} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	size := 0
	for _, m := range media() {
		data, err := m.data()
		if err != nil {
			return fmt.Errorf("%s: %w", m.doc.FileName, err)
		}
		doc, err := complete(m.doc, data)
		if err != nil {
			return fmt.Errorf("%s: %w", m.doc.FileName, err)
		}
		js, err := json.Marshal(doc, jsontext.WithIndent("  "))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(files, strings.TrimPrefix(doc.Hash, "sha256:")), data, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(docs, doc.ID+".json"), append(js, '\n'), 0o644); err != nil {
			return err
		}
		size += len(data)
	}
	log.Printf("%d media: %d bytes of originals", len(media()), size)
	return nil
}

// complete returns m, the document of a medium whose original is data, with what data gives: its hash, its size, its
// MIME type and, for an image, its dimensions, as they are read at upload; and the user who uploaded it.
func complete(m content.Media, data []byte) (content.Media, error) {
	info, err := mediafile.Probe(bytes.NewReader(data))
	if err != nil {
		return m, err
	}
	sum := sha256.Sum256(data)
	m.Hash = "sha256:" + hex.EncodeToString(sum[:])
	m.MIMEType = info.MIMEType
	m.Size = len(data)
	m.Width, m.Height = info.Width, info.Height
	m.UploadedBy = uploader
	return m, nil
}

func encodeJPEG(m image.Image) ([]byte, error) {
	var b bytes.Buffer
	err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 85})
	return b.Bytes(), err
}

func encodePNG(m image.Image) ([]byte, error) {
	var b bytes.Buffer
	err := png.Encode(&b, m)
	return b.Bytes(), err
}

// office draws the building of the company under the sky, which is lighter toward the horizon: rows of windows and an
// entrance, on the ground.
func office(w, h int) image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	horizon := h * 4 / 5
	for y := range horizon {
		fill(m, image.Rect(0, y, w, y+1), blend(skyTop, skyLow, y, horizon))
	}
	fill(m, image.Rect(0, horizon, w, h), ground)
	b := image.Rect(w/4, h/5, w*3/4, horizon)
	fill(m, b, wall)
	cols, rows := 6, 5
	cw, ch := b.Dx()/cols, b.Dy()*3/4/rows
	for i := range cols {
		for j := range rows {
			x, y := b.Min.X+i*cw, b.Min.Y+j*ch
			fill(m, image.Rect(x+cw/4, y+ch/4, x+cw*3/4, y+ch*3/4), glass)
		}
	}
	fill(m, image.Rect(b.Min.X+b.Dx()*2/5, b.Max.Y-b.Dy()/6, b.Min.X+b.Dx()*3/5, b.Max.Y), glass)
	return m
}

// avatar draws a person, a head over the shoulders, on a light background, in a square of n pixels.
func avatar(n int) image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, n, n))
	fill(m, m.Bounds(), muted)
	f := float64(n)
	disc(m, f/2, f*0.4, f*0.19, primary)
	disc(m, f/2, f*1.05, f*0.42, primary)
	return m
}

// logo draws a ring and two bars where a name would be, on a transparent background.
func logo(w, h int) image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	c := float64(h) / 2
	disc(m, c, c, c*0.72, primary)
	disc(m, c, c, c*0.32, color.NRGBA{})
	fill(m, image.Rect(h*11/10, h*3/10, w-h/5, h*9/20), ink)
	fill(m, image.Rect(h*11/10, h*11/20, w*3/4, h*7/10), ink)
	return m
}

// icon draws a ring in a square with round corners, on a transparent background, in a square of n pixels.
func icon(n int) image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, n, n))
	f := float64(n)
	r := f * 0.18
	for y := range n {
		for x := range n {
			// The distance from the square without its corners, which the corners are round within.
			dx := math.Max(0, math.Abs(float64(x)+0.5-f/2)-(f/2-r))
			dy := math.Max(0, math.Abs(float64(y)+0.5-f/2)-(f/2-r))
			if dx*dx+dy*dy <= r*r {
				m.SetNRGBA(x, y, primary)
			}
		}
	}
	disc(m, f/2, f/2, f*0.28, white)
	disc(m, f/2, f/2, f*0.14, primary)
	return m
}

// ogp draws the logo in white on a gradient from the primary color to a deep blue.
func ogp(w, h int) image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		fill(m, image.Rect(x, 0, x+1, h), blend(primary, deep, x, w))
	}
	c := float64(h) / 2
	disc(m, c, c, c*0.36, white)
	disc(m, c, c, c*0.16, primary)
	fill(m, image.Rect(h*9/10, h*2/5, w-h/3, h*12/25), white)
	fill(m, image.Rect(h*9/10, h*13/25, w*2/3, h*3/5), white)
	return m
}

// fill paints r of m with c.
func fill(m draw.Image, r image.Rectangle, c color.Color) {
	draw.Draw(m, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// disc paints the pixels of m whose centers are within radius of (cx, cy) with c.
func disc(m *image.NRGBA, cx, cy, radius float64, c color.NRGBA) {
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= radius*radius {
				m.SetNRGBA(x, y, c)
			}
		}
	}
}

// blend returns the color at i of n steps from a to b.
func blend(a, b color.NRGBA, i, n int) color.NRGBA {
	mix := func(x, y uint8) uint8 { return uint8((int(x)*(n-1-i) + int(y)*i) / max(n-1, 1)) }
	return color.NRGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff}
}

// guide returns a PDF of one A4 page with a title over a line, written as the PDF format describes, with the offsets of
// its objects in the cross-reference table.
func guide() []byte {
	contents := strings.Join([]string{
		"q 0.118 0.451 0.745 rg 72 742 451 4 re f Q",
		"BT /F1 28 Tf 72 760 Td (Company Guide) Tj ET",
		"BT /F1 12 Tf 72 712 Td (Example Inc.) Tj ET",
	}, "\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> " +
			"/Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(contents), contents),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	// Each entry of the table has 20 bytes, with a space before the line break.
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}
