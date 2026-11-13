package wpimport

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// testPNG is a PNG of w by h pixels. The same arguments give the same bytes in every version of Go: the data is not
// compressed, which the encoder does not change.
func testPNG(w, h int, seed byte) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{R: byte(x) + seed, G: byte(y), B: seed, A: 255})
		}
	}
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func testJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: byte(x * 3), G: byte(y * 3), B: byte(x + y), A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// testGIF is a GIF of w by h pixels with that many frames.
func testGIF(w, h, frames int) []byte {
	pal := color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}}
	g := &gif.GIF{}
	for i := range frames {
		img := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for y := range h {
			for x := range w {
				img.SetColorIndex(x, y, uint8((x+y+i)%len(pal)))
			}
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 10)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func testPDF() []byte {
	return []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
}

// pngHeader is a PNG that has only the signature, an IHDR for an image of w by h pixels, an empty IDAT and an IEND, so
// that it tells a size, such as one of more than 100 million pixels, without being large. It cannot be decoded.
func pngHeader(w, h int) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(data)))
		b.Write(n[:])
		b.WriteString(typ)
		b.Write(data)
		sum := crc32.NewIEEE()
		sum.Write([]byte(typ))
		sum.Write(data)
		binary.BigEndian.PutUint32(n[:], sum.Sum32())
		b.Write(n[:])
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = 8, 6 // 8 bits, RGBA
	chunk("IHDR", ihdr)
	chunk("IDAT", nil)
	chunk("IEND", nil)
	return b.Bytes()
}
