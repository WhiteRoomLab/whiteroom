package webp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
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

func validSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
