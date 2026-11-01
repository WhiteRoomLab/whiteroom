package webp

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/v2"
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// vector is a file of testdata/vectors as testdata/vectors.json describes it. go generate writes both: the tools of
// libwebp and libvpx make the files from synthetic images, and dwebp decodes them.
type vector struct {
	Name        string        `json:"name"`
	Kind        string        `json:"kind"` // lossy, alpha (lossy with ALPH), lossless, vpx (by vpxenc) or animated
	Tool        string        `json:"tool"`
	Commands    []string      `json:"commands"`
	Size        int           `json:"size"`
	SHA256      string        `json:"sha256"`
	Chunks      []string      `json:"chunks"` // the FourCCs of the chunks in the RIFF chunk
	Width       int           `json:"width"`  // of the canvas
	Height      int           `json:"height"`
	Lossless    bool          `json:"lossless"` // a still image coded with VP8L
	Alpha       bool          `json:"alpha"`
	Animated    bool          `json:"animated"`
	LoopCount   int           `json:"loopCount"`
	Background  []int         `json:"background"` // B, G, R and A as in the ANIM chunk
	ICCSHA256   string        `json:"iccSHA256"`  // the payloads that webpmux set
	EXIFSHA256  string        `json:"exifSHA256"`
	XMPSHA256   string        `json:"xmpSHA256"`
	Orientation int           `json:"orientation"` // in the EXIF payload
	Frames      []vectorFrame `json:"frames"`      // one for a still image
}

// vectorFrame is a frame of a vector, with the SHA-256 of what dwebp decodes from it alone: for a lossy frame, of the
// Y, U and V planes and of the alpha plane of -yuv, and for every frame, of the RGBA pixels of -pam (not
// premultiplied).
type vectorFrame struct {
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Duration    int    `json:"duration"`
	Blend       bool   `json:"blend"`
	Dispose     bool   `json:"dispose"`
	Lossless    bool   `json:"lossless"`
	Alpha       bool   `json:"alpha"`
	YUVSHA256   string `json:"yuvSHA256"`
	AlphaSHA256 string `json:"alphaSHA256"`
	RGBASHA256  string `json:"rgbaSHA256"`
}

var (
	vectorTools = []string{"cwebp", "dwebp", "img2webp", "webpmux", "gif2webp", "vpxenc"}
	vectorName  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// loadVectors returns the vectors of testdata/vectors.json.
func loadVectors(t testing.TB) []vector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tools []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"tools"`
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(data, &m, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("vectors.json: %v", err)
	}
	var names []string
	for _, tool := range m.Tools {
		if tool.Version == "" {
			t.Errorf("vectors.json: no version of %s", tool.Name)
		}
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, vectorTools) {
		t.Fatalf("vectors.json: tools %q, want %q", names, vectorTools)
	}
	return m.Vectors
}

// readVector returns the file of v, which must have v's size and SHA-256.
func readVector(t testing.TB, v vector) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "vectors", v.Name+".webp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != v.Size || sha256Hex(data) != v.SHA256 {
		t.Fatalf("%s: %d bytes with SHA-256 %s, want %d bytes with %s", v.Name, len(data), sha256Hex(data), v.Size,
			v.SHA256)
	}
	return data
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestVectors(t *testing.T) {
	vs := loadVectors(t)
	counts := make(map[string]int)
	for i, v := range vs {
		if i > 0 && vs[i-1].Name >= v.Name {
			t.Errorf("%s after %s, want the names sorted and unique", v.Name, vs[i-1].Name)
		}
		readVector(t, v)
		counts[v.Kind]++
		if v.ICCSHA256 != "" || v.EXIFSHA256 != "" || v.XMPSHA256 != "" {
			counts["metadata"]++
		}
		for _, err := range checkVector(v) {
			t.Errorf("%s: %v", v.Name, err)
		}
	}
	files, err := filepath.Glob(filepath.Join("testdata", "vectors", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(vs) {
		t.Errorf("%d files in testdata/vectors, want one for each of the %d vectors", len(files), len(vs))
	}
	for _, k := range []struct {
		kind string
		min  int
	}{{"lossy", 40}, {"alpha", 12}, {"lossless", 25}, {"vpx", 12}, {"animated", 6}, {"metadata", 8}} {
		if counts[k.kind] < k.min {
			t.Errorf("%d vectors of %s, want at least %d", counts[k.kind], k.kind, k.min)
		}
	}
}

// checkVector returns what is inconsistent in v.
func checkVector(v vector) []error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	has := func(id string) int {
		n := 0
		for _, c := range v.Chunks {
			if c == id {
				n++
			}
		}
		return n
	}
	hash := func(s string, want bool) bool { return want && validSHA256(s) || !want && s == "" }
	still := !v.Animated
	ok := map[string]bool{
		"lossy":    still && !v.Lossless && !v.Alpha && v.Tool == "cwebp",
		"alpha":    still && !v.Lossless && v.Alpha && v.Tool == "cwebp",
		"lossless": still && v.Lossless && v.Tool == "cwebp",
		"vpx":      still && !v.Lossless && !v.Alpha && v.Tool == "vpxenc",
		"animated": v.Animated && slices.Contains([]string{"img2webp", "gif2webp", "webpmux"}, v.Tool),
	}[v.Kind]
	if !ok {
		fail("kind %q from %s, but lossless %t, alpha %t, animated %t", v.Kind, v.Tool, v.Lossless, v.Alpha, v.Animated)
	}
	if !vectorName.MatchString(v.Name) || len(v.Commands) == 0 {
		fail("not a name in kebab case, or no commands")
	}
	if !validSHA256(v.SHA256) || v.Width < 1 || v.Height < 1 {
		fail("SHA-256 %q, size %dx%d", v.SHA256, v.Width, v.Height)
	}
	extended := len(v.Chunks) > 0 && v.Chunks[0] == "VP8X"
	if !extended && len(v.Chunks) != 1 {
		fail("chunks %q", v.Chunks)
	}
	for _, m := range []struct {
		id  string
		sum string
	}{{"ICCP", v.ICCSHA256}, {"EXIF", v.EXIFSHA256}, {"XMP ", v.XMPSHA256}} {
		if n := has(m.id); n > 1 || !hash(m.sum, n == 1) {
			fail("%d %q chunks, payload SHA-256 %q", n, m.id, m.sum)
		}
	}
	if (v.EXIFSHA256 != "") != (v.Orientation != 0) || v.Orientation < 0 || v.Orientation > 8 {
		fail("orientation %d", v.Orientation)
	}
	if v.Animated {
		if !extended || has("ANIM") != 1 || has("ANMF") != len(v.Frames) || len(v.Frames) == 0 ||
			has("VP8 ")+has("VP8L")+has("ALPH") != 0 || len(v.Background) != 4 {
			fail("animation with chunks %q, %d frames, background %v", v.Chunks, len(v.Frames), v.Background)
		}
		for _, c := range v.Background {
			if c < 0 || c > 255 {
				fail("background %v", v.Background)
			}
		}
	} else {
		if has("VP8 ")+has("VP8L") != 1 || has("VP8L") != btoi(v.Lossless) ||
			has("ALPH") != btoi(!v.Lossless && v.Alpha) || has("ANIM")+has("ANMF") != 0 || v.LoopCount != 0 ||
			v.Background != nil || len(v.Frames) != 1 {
			fail("still image with chunks %q, %d frames, loop count %d, background %v", v.Chunks, len(v.Frames),
				v.LoopCount, v.Background)
		}
		if len(v.Frames) == 1 {
			f := v.Frames[0]
			if f.X != 0 || f.Y != 0 || f.Width != v.Width || f.Height != v.Height || f.Duration != 0 || f.Blend ||
				f.Dispose || f.Lossless != v.Lossless || f.Alpha != v.Alpha {
				fail("the frame of a still image is %+v", f)
			}
		}
	}
	for i, f := range v.Frames {
		if f.X < 0 || f.Y < 0 || f.X%2 != 0 || f.Y%2 != 0 || f.Width < 1 || f.Height < 1 ||
			f.X+f.Width > v.Width || f.Y+f.Height > v.Height || f.Duration < 0 {
			fail("frame %d at (%d, %d) of %dx%d, %d ms", i, f.X, f.Y, f.Width, f.Height, f.Duration)
		}
		if f.Alpha && !v.Alpha {
			fail("frame %d has alpha, the image not", i)
		}
		lossy := !f.Lossless
		if !hash(f.YUVSHA256, lossy) || !hash(f.AlphaSHA256, lossy && f.Alpha) || !validSHA256(f.RGBASHA256) {
			fail("frame %d, lossless %t, alpha %t, has SHA-256 of YUV %q, alpha %q, RGBA %q", i, f.Lossless, f.Alpha,
				f.YUVSHA256, f.AlphaSHA256, f.RGBASHA256)
		}
	}
	return errs
}

// TestParseVectors checks what Parse reads from every vector against the manifest.
func TestParseVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		f, err := Parse(readVector(t, v))
		if err != nil {
			t.Errorf("%s: %v", v.Name, err)
			continue
		}
		if f.Width != v.Width || f.Height != v.Height || f.Animated != v.Animated || f.Alpha != v.Alpha ||
			f.Lossless != v.Lossless || f.LoopCount != v.LoopCount || len(f.Frames) != len(v.Frames) {
			t.Errorf("%s: %dx%d, animated %t, alpha %t, lossless %t, loop count %d, %d frames; "+
				"want %dx%d, %t, %t, %t, %d, %d", v.Name, f.Width, f.Height, f.Animated, f.Alpha, f.Lossless,
				f.LoopCount, len(f.Frames), v.Width, v.Height, v.Animated, v.Alpha, v.Lossless, v.LoopCount,
				len(v.Frames))
			continue
		}
		bg := []int{int(f.Background[0]), int(f.Background[1]), int(f.Background[2]), int(f.Background[3])}
		if v.Animated && !slices.Equal(bg, v.Background) || !v.Animated && f.Background != [4]byte{} {
			t.Errorf("%s: background %v, want %v", v.Name, bg, v.Background)
		}
		for _, p := range []struct {
			name    string
			payload []byte
			sum     string
		}{{"ICC", f.ICC, v.ICCSHA256}, {"EXIF", f.EXIF, v.EXIFSHA256}, {"XMP", f.XMP, v.XMPSHA256}} {
			if p.sum == "" && p.payload != nil || p.sum != "" && sha256Hex(p.payload) != p.sum {
				t.Errorf("%s: %s of %d bytes, want SHA-256 %q", v.Name, p.name, len(p.payload), p.sum)
			}
		}
		for i, vf := range v.Frames {
			fr := f.Frames[i]
			got := vectorFrame{X: fr.X, Y: fr.Y, Width: fr.Width, Height: fr.Height, Duration: fr.Duration,
				Blend: fr.Blend, Dispose: fr.Dispose, Lossless: fr.lossless, Alpha: fr.hasAlpha}
			want := vf
			want.YUVSHA256, want.AlphaSHA256, want.RGBASHA256 = "", "", ""
			if got != want {
				t.Errorf("%s: frame %d is %+v, want %+v", v.Name, i, got, want)
			}
			if (fr.alphaData != nil) != (vf.Alpha && !vf.Lossless) {
				t.Errorf("%s: frame %d has ALPH %t, want %t", v.Name, i, fr.alphaData != nil, vf.Alpha && !vf.Lossless)
			}
		}
	}
}

// TestDecodeVectors decodes every vector: Decode for still images, FrameImage for frames of animations. The RGBA bytes
// must be dwebp's, and so must the alpha plane of lossy frames with alpha. Image of an animation must be its first
// frame on a transparent canvas.
func TestDecodeVectors(t *testing.T) {
	var stills, frames, alphas, want int
	for _, v := range loadVectors(t) {
		data := readVector(t, v)
		f, err := Parse(data)
		if err != nil {
			t.Errorf("%s: %v", v.Name, err)
			continue
		}
		for i, vf := range v.Frames {
			want++
			var img image.Image
			if v.Animated {
				img, err = f.FrameImage(i)
			} else {
				img, err = Decode(bytes.NewReader(data))
			}
			if err != nil {
				t.Errorf("%s: frame %d: %v", v.Name, i, err)
				continue
			}
			if img.Bounds() != image.Rect(0, 0, vf.Width, vf.Height) {
				t.Errorf("%s: frame %d of %v, want %dx%d", v.Name, i, img.Bounds(), vf.Width, vf.Height)
				continue
			}
			pix := rgbaBytes(img)
			if got := sha256Hex(pix); got != vf.RGBASHA256 {
				t.Errorf("%s: frame %d: RGBA has SHA-256 %s, want %s", v.Name, i, got, vf.RGBASHA256)
			} else if v.Animated {
				frames++
			} else {
				stills++
			}
			if vf.AlphaSHA256 != "" {
				if got := sha256Hex(alphaPlane(pix)); got != vf.AlphaSHA256 {
					t.Errorf("%s: frame %d: alpha has SHA-256 %s, want %s", v.Name, i, got, vf.AlphaSHA256)
				} else {
					alphas++
				}
			}
			if _, ok := img.(*image.NRGBA); ok != (vf.Alpha || !isOpaque(img)) {
				t.Errorf("%s: frame %d: %T, alpha %t", v.Name, i, img, vf.Alpha)
			}
		}
		if v.Animated {
			if img, err := f.Image(); err != nil {
				t.Errorf("%s: Image: %v", v.Name, err)
			} else {
				checkCanvas(t, v.Name, f, img)
			}
		}
	}
	if stills+frames != want {
		t.Errorf("%d of %d frames equal dwebp's", stills+frames, want)
	}
	t.Logf("%d still images, %d frames of animations and %d alpha planes of lossy frames equal dwebp's", stills, frames,
		alphas)
}

// checkCanvas checks that img, what Image returned for an animation, is its first frame on a transparent canvas.
func checkCanvas(t *testing.T, name string, f *File, img image.Image) {
	t.Helper()
	canvas, ok := img.(*image.NRGBA)
	if !ok || canvas.Rect != image.Rect(0, 0, f.Width, f.Height) {
		t.Errorf("%s: Image is %T of %v, want *image.NRGBA of %dx%d", name, img, img.Bounds(), f.Width, f.Height)
		return
	}
	first, err := f.FrameImage(0)
	if err != nil {
		t.Errorf("%s: FrameImage(0): %v", name, err)
		return
	}
	pix, fr := rgbaBytes(first), f.Frames[0]
	frame := image.Rect(fr.X, fr.Y, fr.X+fr.Width, fr.Y+fr.Height)
	for y := range f.Height {
		for x := range f.Width {
			want := []byte{0, 0, 0, 0}
			if image.Pt(x, y).In(frame) {
				i := 4 * ((y-fr.Y)*fr.Width + x - fr.X)
				want = pix[i : i+4]
			}
			if got := canvas.Pix[canvas.PixOffset(x, y):][:4]; !bytes.Equal(got, want) {
				t.Errorf("%s: Image at (%d, %d) is %v, want %v", name, x, y, got, want)
				return
			}
		}
	}
}

// alphaPlane returns the A bytes of RGBA bytes.
func alphaPlane(pix []byte) []byte {
	a := make([]byte, len(pix)/4)
	for i := range a {
		a[i] = pix[4*i+3]
	}
	return a
}

// TestWriteWithoutMetadataVectors re-muxes every vector. The result must be the vector without its EXIF and XMP chunks
// and their flags, and decode to the same frames.
func TestWriteWithoutMetadataVectors(t *testing.T) {
	stripped := 0
	for _, v := range loadVectors(t) {
		data := readVector(t, v)
		f, err := Parse(data)
		if err != nil {
			t.Errorf("%s: %v", v.Name, err)
			continue
		}
		var buf bytes.Buffer
		if err := f.WriteWithoutMetadata(&buf); err != nil {
			t.Errorf("%s: %v", v.Name, err)
			continue
		}
		out := buf.Bytes()
		if want := withoutMetadata(t, data); !bytes.Equal(out, want) {
			t.Errorf("%s: WriteWithoutMetadata wrote %d bytes, want the vector without EXIF and XMP, %d bytes", v.Name,
				len(out), len(want))
		}
		if len(out) < len(data) {
			stripped++
		}
		g := checkRemux(t, f, out)
		if g == nil {
			continue
		}
		for i, vf := range v.Frames {
			img, err := g.FrameImage(i)
			if err != nil {
				t.Errorf("%s: frame %d of the result: %v", v.Name, i, err)
			} else if got := sha256Hex(rgbaBytes(img)); got != vf.RGBASHA256 {
				t.Errorf("%s: frame %d of the result: RGBA has SHA-256 %s, want %s", v.Name, i, got, vf.RGBASHA256)
			}
		}
	}
	if stripped < 15 {
		t.Errorf("%d vectors lost metadata, want at least 15", stripped)
	}
}

// withoutMetadata returns a file made by the tools of libwebp without its EXIF and XMP chunks, and their flags cleared.
func withoutMetadata(t *testing.T, data []byte) []byte {
	t.Helper()
	size := binary.LittleEndian.Uint32(data[4:])
	var chunks [][]byte
	for _, c := range splitChunks(t, data[12:8+size]) {
		switch string(c[:4]) {
		case "EXIF", "XMP ":
			continue
		case "VP8X":
			c = slices.Clone(c)
			c[8] &^= flagEXIF | flagXMP
		}
		chunks = append(chunks, c)
	}
	return riff(chunks...)
}

// splitChunks returns the chunks of b with their headers and padding.
func splitChunks(t testing.TB, b []byte) [][]byte {
	t.Helper()
	var chunks [][]byte
	for len(b) > 0 {
		if len(b) < 8 {
			t.Fatalf("%d bytes after the last chunk", len(b))
		}
		n := int(binary.LittleEndian.Uint32(b[4:]))
		end := 8 + n + n&1
		if end > len(b) {
			t.Fatalf("%q chunk of %d bytes past the end", b[:4], n)
		}
		chunks = append(chunks, b[:end:end])
		b = b[end:]
	}
	return chunks
}

// checkRemux checks out, what f wrote with WriteWithoutMetadata: it must have no metadata and no unknown chunks, parse
// to f's images and ICC profile, and be written again unchanged. It returns the parsed File, or nil.
func checkRemux(t *testing.T, f *File, out []byte) *File {
	t.Helper()
	g, err := Parse(out)
	if err != nil {
		t.Errorf("Parse of the result: %v", err)
		return nil
	}
	if int(binary.LittleEndian.Uint32(out[4:])) != len(out)-8 {
		t.Errorf("RIFF size %d for %d bytes", binary.LittleEndian.Uint32(out[4:]), len(out))
	}
	allowed := []string{"VP8X", "ICCP", "ANIM", "ANMF", "ALPH", "VP8 ", "VP8L"}
	var ids []string
	for i, c := range splitChunks(t, out[12:]) {
		id := string(c[:4])
		ids = append(ids, id)
		if !slices.Contains(allowed, id) || id == "VP8X" && i > 0 {
			t.Errorf("chunk %q in the result", id)
		}
		if id == "ANMF" {
			for _, s := range splitChunks(t, c[24:]) {
				if !slices.Contains(allowed[4:], string(s[:4])) {
					t.Errorf("chunk %q in a frame of the result", s[:4])
				}
			}
		}
		if n := binary.LittleEndian.Uint32(c[4:]); n%2 == 1 && c[len(c)-1] != 0 {
			t.Errorf("chunk %q padded with %d", id, c[len(c)-1])
		}
	}
	if g.Width != f.Width || g.Height != f.Height || g.Animated != f.Animated || g.Alpha != f.Alpha ||
		g.Lossless != f.Lossless || g.LoopCount != f.LoopCount || g.Background != f.Background ||
		g.extended != f.extended {
		t.Errorf("the result is %+v, want %+v without metadata", g, f)
	}
	// The flags of VP8X must tell what the chunks have, as webpinfo checks, and an Alpha flag is kept.
	var flags byte
	if slices.Contains(ids, "ICCP") {
		flags |= flagICC
	}
	if slices.Contains(ids, "ANIM") {
		flags |= flagAnimation
	}
	if f.extended && (f.Alpha || f.flags&flagAlpha != 0) {
		flags |= flagAlpha
	}
	if g.flags != flags {
		t.Errorf("the result has flags %#x and chunks %q, want flags %#x", g.flags, ids, flags)
	}
	if !bytes.Equal(g.ICC, f.ICC) || (g.ICC == nil) != (f.ICC == nil) || g.EXIF != nil || g.XMP != nil {
		t.Errorf("the result has ICC %q, EXIF %q, XMP %q; want ICC %q", g.ICC, g.EXIF, g.XMP, f.ICC)
	}
	if !slices.EqualFunc(g.Frames, f.Frames, sameFrame) {
		t.Errorf("the result has frames %+v, want %+v", g.Frames, f.Frames)
	}
	var again bytes.Buffer
	if err := g.WriteWithoutMetadata(&again); err != nil || !bytes.Equal(again.Bytes(), out) {
		t.Errorf("writing the result again: %v, %d bytes, want the same %d bytes", err, again.Len(), len(out))
	}
	return g
}

func sameFrame(a, b Frame) bool {
	return a.X == b.X && a.Y == b.Y && a.Width == b.Width && a.Height == b.Height && a.Duration == b.Duration &&
		a.Blend == b.Blend && a.Dispose == b.Dispose && a.lossless == b.lossless && a.hasAlpha == b.hasAlpha &&
		bytes.Equal(a.bitstream, b.bitstream) && bytes.Equal(a.alphaData, b.alphaData) &&
		(a.alphaData == nil) == (b.alphaData == nil)
}

var yuvDir = flag.String("yuvdir", "", "a directory of the planes that dwebp -yuv decodes from the lossy frames, "+
	"<vector>-<frame>.yuv, to find where TestDecodeLossy differs")

// TestDecodeLossy decodes the VP8 bitstream of every lossy frame of the vectors, still images and frames of
// animations. The Y, U and V planes must be dwebp's.
func TestDecodeLossy(t *testing.T) {
	n := 0
	for _, v := range loadVectors(t) {
		f, err := Parse(readVector(t, v))
		if err != nil {
			t.Errorf("%s: %v", v.Name, err)
			continue
		}
		for i, vf := range v.Frames {
			if vf.Lossless {
				continue
			}
			img, err := decodeVP8(f.Frames[i].bitstream)
			if err != nil {
				t.Errorf("%s: frame %d: %v", v.Name, i, err)
				continue
			}
			n++
			cw, ch := (vf.Width+1)/2, (vf.Height+1)/2
			if img.w != vf.Width || img.h != vf.Height || len(img.y) != vf.Width*vf.Height || len(img.u) != cw*ch ||
				len(img.v) != cw*ch {
				t.Errorf("%s: frame %d: %dx%d with planes of %d, %d and %d bytes, want %dx%d", v.Name, i, img.w, img.h,
					len(img.y), len(img.u), len(img.v), vf.Width, vf.Height)
				continue
			}
			if got := sha256Hex(slices.Concat(img.y, img.u, img.v)); got != vf.YUVSHA256 {
				t.Errorf("%s: frame %d: YUV has SHA-256 %s, want %s%s", v.Name, i, got, vf.YUVSHA256,
					yuvDiff(fmt.Sprintf("%s-%d", v.Name, i), img))
			}
		}
	}
	t.Logf("%d lossy frames decoded", n)
}

// yuvDiff describes where img first differs from the planes in -yuvdir, if given.
func yuvDiff(name string, img *yuvImage) string {
	if *yuvDir == "" {
		return ""
	}
	file := filepath.Join(*yuvDir, name+".yuv")
	want, err := os.ReadFile(file)
	if err != nil {
		return fmt.Sprintf("; %v", err)
	}
	cw := (img.w + 1) / 2
	offset := 0
	for _, p := range []struct {
		name    string
		got     []byte
		w, size int // the width, and the macroblock size in this plane
	}{{"Y", img.y, img.w, 16}, {"U", img.u, cw, 8}, {"V", img.v, cw, 8}} {
		if len(want) < offset+len(p.got) {
			return fmt.Sprintf("; %s is %d bytes", file, len(want))
		}
		for k, g := range p.got {
			if w := want[offset+k]; g != w {
				x, y := k%p.w, k/p.w
				return fmt.Sprintf("; %s (%d, %d) in macroblock (%d, %d) is %d, want %d", p.name, x, y, x/p.size,
					y/p.size, g, w)
			}
		}
		offset += len(p.got)
	}
	return "; the planes are equal"
}

// rgbaBytes returns the non-premultiplied RGBA bytes of img, row by row. An *image.RGBA must be opaque.
func rgbaBytes(img image.Image) []byte {
	var b []byte
	switch img := img.(type) {
	case *image.NRGBA:
		for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
			b = append(b, img.Pix[img.PixOffset(img.Rect.Min.X, y):img.PixOffset(img.Rect.Max.X, y)]...)
		}
	case *image.RGBA:
		for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
			b = append(b, img.Pix[img.PixOffset(img.Rect.Min.X, y):img.PixOffset(img.Rect.Max.X, y)]...)
		}
	}
	return b
}

func isOpaque(img image.Image) bool {
	o, ok := img.(interface{ Opaque() bool })
	return ok && o.Opaque()
}

func validSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}
