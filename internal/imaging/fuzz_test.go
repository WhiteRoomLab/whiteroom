package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/webp"
)

// makeSeeds returns small originals of every kind that makeFiles makes files from.
func makeSeeds(t testing.TB) [][]byte {
	cover := indexesOf(4, 3, 4)
	frame := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, cover))
	pal := image.NewPaletted(image.Rect(0, 0, 5, 4), color.Palette{color.Black, color.NRGBA{0, 0, 255, 128}})
	seeds := append(inspectSeeds(t),
		insertJPEG(jpegOf(t, blocksImage(20, 12)), exifSegment(7)),
		insertPNG(pngOf(t, pal), pngChunk("eXIf", exifTIFF(5))),
		pngOf(t, image.NewNRGBA64(image.Rect(0, 0, 3, 5))),
		gifWith("GIF89a", 5, 4, rgbTable, gifControlWith(1, 0, 2), gifImageWith(1, 1, 4, 3, 0x40, nil, 2,
			lzwOf(t, 2, cover))),
		gifWith("GIF89a", 4, 3, rgbTable, gifApp("NETSCAPE2.0", []byte{1, 0, 0}), gifControlWith(9, 5, 1), frame,
			gifExtension(0xfe, []byte("comment")), frame),
		readVector(t, "lossless-photo-16x16"),
		readVector(t, "meta-exif-o3-alpha"),
		readVector(t, "anim-img2webp-lossy"),
	)
	return seeds
}

// TestMakeSeeds checks that makeFiles makes the files of every seed of FuzzMake.
func TestMakeSeeds(t *testing.T) {
	for i, data := range makeSeeds(t) {
		info, err := Inspect(data)
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		if _, err := makeFiles(data, info, variantsOf(info)); err != nil {
			t.Errorf("seed %d: %v", i, err)
		}
	}
}

func FuzzMake(f *testing.F) {
	for _, data := range makeSeeds(f) {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := Inspect(data)
		if err != nil || int64(info.Width)*int64(info.Height) > 1<<20 {
			return
		}
		vs := append([]Variant{Published(info)}, ForSize(info, nil).Srcset...)
		vs = append(vs, Icon(info, 32))
		files, err := makeFiles(data, info, vs)
		if err != nil {
			return
		}
		if len(files) != len(vs) {
			t.Fatalf("%d files of %d variants", len(files), len(vs))
		}
		for i, file := range files {
			v := vs[i]
			var (
				c   image.Config
				err error
			)
			switch v.Format {
			case mediafile.JPEG:
				if _, err = jpeg.Decode(bytes.NewReader(file)); err == nil {
					c, err = jpeg.DecodeConfig(bytes.NewReader(file))
				}
			case mediafile.PNG:
				if _, err = png.Decode(bytes.NewReader(file)); err == nil {
					c, err = png.DecodeConfig(bytes.NewReader(file))
				}
			case mediafile.GIF:
				// image/gif reads the file if it reads the original: frames outside the screen are kept.
				if _, e := gif.DecodeAll(bytes.NewReader(data)); e == nil {
					_, err = gif.DecodeAll(bytes.NewReader(file))
				}
				if err == nil {
					c, err = gif.DecodeConfig(bytes.NewReader(file))
				}
			case mediafile.WebP:
				var w *webp.File
				if w, err = webp.Parse(file); err == nil {
					c = image.Config{Width: w.Width, Height: w.Height}
				}
			}
			if err != nil {
				t.Fatalf("%+v: %v", v, err)
			}
			if c.Width != v.Width || c.Height != v.Height {
				t.Fatalf("%+v: file of %dx%d pixels", v, c.Width, c.Height)
			}
		}
		if info.MIMEType == mediafile.GIF {
			checkGIFFiles(t, data, info, files[0])
		}
	})
}

// checkGIFFiles checks the published file of a GIF against what image/gif reads from data, if it reads it: the first
// frame on a canvas of the screen, or the frames, their delays and disposal.
func checkGIFFiles(t *testing.T, data []byte, info Info, published []byte) {
	if info.Animated {
		want, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return
		}
		got, err := gif.DecodeAll(bytes.NewReader(published))
		if err != nil || len(got.Image) != len(want.Image) || !slices.Equal(got.Delay, want.Delay) ||
			!slices.Equal(got.Disposal, want.Disposal) {
			t.Fatalf("rebuilt GIF: %v; want the frames, delays and disposal of the original", err)
		}
		for i, m := range want.Image {
			if got.Image[i].Rect != m.Rect || !bytes.Equal(got.Image[i].Pix, m.Pix) ||
				!samePalette(got.Image[i].Palette, m.Palette) {
				t.Fatalf("frame %d of the rebuilt GIF differs", i)
			}
		}
		return
	}
	frame, err := gif.Decode(bytes.NewReader(data))
	if pub := Published(info); err != nil || pub.Width != info.Width || pub.Height != info.Height {
		return
	}
	got, err := png.Decode(bytes.NewReader(published))
	if err != nil {
		t.Fatal(err)
	}
	if want := rendered(info.Width, info.Height, frame.(*image.Paletted)); !bytes.Equal(nrgbaOf(got).Pix, want.Pix) {
		t.Fatalf("the published file is not the first frame that image/gif reads")
	}
}
