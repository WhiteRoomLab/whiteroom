package imaging_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"os"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/imaging"
)

// photo returns a JPEG file of w x h pixels.
func photo(w, h int) []byte {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, nil); err != nil {
		log.Fatal(err)
	}
	return b.Bytes()
}

// A build gets the files of an image block at the default size.
func ExampleImages() {
	dir, err := os.MkdirTemp("", "imaging")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	im, err := imaging.New(imaging.Options{Dir: dir})
	if err != nil {
		log.Fatal(err)
	}

	// An image medium and its original.
	original := photo(1000, 750)
	sum := sha256.Sum256(original)
	m := content.Media{
		ID: "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", Hash: "sha256:" + hex.EncodeToString(sum[:]), FileName: "IMG 0001 (2).JPG",
	}
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(original)), nil }

	info, err := im.Info(m.Hash, open)
	if err != nil {
		log.Fatal(err)
	}
	set := imaging.ForSize(info, nil)
	vs := set.Srcset
	if vs == nil {
		vs = []imaging.Variant{set.Src}
	}
	files, err := im.Files(context.Background(), m.Hash, open, info, vs)
	if err != nil {
		log.Fatal(err)
	}
	for i, v := range vs {
		// The build writes files[i] at path in its output, and pages refer to it as path + "?v=" + Version.
		path := imaging.Path(m.ID, imaging.Name(m.FileName, info, v))
		c, err := jpeg.DecodeConfig(bytes.NewReader(files[i]))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s %dw: %dx%d\n", path, v.Width, c.Width, c.Height)
	}
	fmt.Printf("src %s, shown at %dx%d\n", imaging.Name(m.FileName, info, set.Src), set.Width, set.Height)

	// After the build succeeds, the cache keeps only what it has used.
	if err := im.Prune(); err != nil {
		log.Fatal(err)
	}
	// Output:
	// /media/med_01JB8Z3K5QH2V6T9C4R7Y1E8B5/IMG-0001-2-640x480.jpg 640w: 640x480
	// /media/med_01JB8Z3K5QH2V6T9C4R7Y1E8B5/IMG-0001-2-960x720.jpg 960w: 960x720
	// /media/med_01JB8Z3K5QH2V6T9C4R7Y1E8B5/IMG-0001-2.jpg 1000w: 1000x750
	// src IMG-0001-2.jpg, shown at 1000x750
}

// Pages refer to a file with its version, which changes with its bytes, as in
// "/media/med_01JB8Z3K5QH2V6T9C4R7Y1E8B5/IMG-0001-2-640x480.jpg?v=1a2b3c4d". The version also changes with the minor
// version of Go, so the output of this example is not checked.
func ExampleVersion() {
	const hash = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	v := imaging.Variant{Width: 640, Height: 480, Format: "image/jpeg"}
	path := imaging.Path("med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", "IMG-0001-2-640x480.jpg")
	fmt.Println(path + "?v=" + imaging.Version(hash, v))
}
