// Command gen writes testdata/vectors/*.webp, WebP files that the tools of libwebp and libvpx make from synthetic
// images, and testdata/vectors.json, which describes each file with the SHA-256 of what dwebp decodes from it. It needs
// cwebp, dwebp, img2webp, webpmux, gif2webp and vpxenc in PATH, as in the dev shell.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var tools = []string{"cwebp", "dwebp", "img2webp", "webpmux", "gif2webp", "vpxenc"}

type manifest struct {
	Tools   []tool   `json:"tools"`
	Vectors []vector `json:"vectors"`
}

type tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type vector struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Tool        string   `json:"tool"`
	Commands    []string `json:"commands"` // run in a directory with the inputs
	Size        int      `json:"size"`
	SHA256      string   `json:"sha256"`
	Chunks      []string `json:"chunks"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	Lossless    bool     `json:"lossless"`
	Alpha       bool     `json:"alpha"`
	Animated    bool     `json:"animated"`
	LoopCount   int      `json:"loopCount,omitzero"`
	Background  []int    `json:"background,omitempty"` // B, G, R, A as in the ANIM chunk
	ICCSHA256   string   `json:"iccSHA256,omitempty"`
	EXIFSHA256  string   `json:"exifSHA256,omitempty"`
	XMPSHA256   string   `json:"xmpSHA256,omitempty"`
	Orientation int      `json:"orientation,omitzero"`
	Frames      []frame  `json:"frames"`
}

type frame struct {
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Duration    int    `json:"duration"`
	Blend       bool   `json:"blend"`
	Dispose     bool   `json:"dispose"`
	Lossless    bool   `json:"lossless"`
	Alpha       bool   `json:"alpha"`
	YUVSHA256   string `json:"yuvSHA256,omitempty"`   // the Y, U and V planes of dwebp -yuv
	AlphaSHA256 string `json:"alphaSHA256,omitempty"` // the alpha plane of dwebp -yuv
	RGBASHA256  string `json:"rgbaSHA256"`            // the pixels of dwebp -pam
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("gen: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	versions, err := toolVersions()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "webp-gen-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	g := &gen{dir: dir, written: make(map[string]bool)}
	m := manifest{Tools: versions}
	files := make(map[string][]byte)
	names := make(map[string]string) // by SHA-256
	for _, sp := range specs() {
		v, data, err := g.vector(sp)
		if err != nil {
			return fmt.Errorf("%s: %w", sp.name, err)
		}
		if _, ok := files[v.Name]; ok {
			return fmt.Errorf("%s: two vectors have the name", v.Name)
		}
		if other, ok := names[v.SHA256]; ok {
			return fmt.Errorf("%s: the same file as %s", v.Name, other)
		}
		names[v.SHA256] = v.Name
		files[v.Name] = data
		m.Vectors = append(m.Vectors, v)
	}
	return write(m, files)
}

// toolVersions checks that the tools are in PATH and returns their versions.
func toolVersions() ([]tool, error) {
	var ts []tool
	for _, name := range tools {
		if _, err := exec.LookPath(name); err != nil {
			return nil, fmt.Errorf("%w; run go generate in the dev shell (nix develop)", err)
		}
		arg := "-version"
		if name == "vpxenc" {
			arg = "--help"
		}
		out, err := exec.Command(name, arg).Output()
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", name, arg, err)
		}
		v := version(name, string(out))
		if v == "" {
			return nil, fmt.Errorf("%s %s: no version", name, arg)
		}
		ts = append(ts, tool{name, v})
	}
	return ts, nil
}

// version returns the lines of the output of "name -version" joined, or the VP8 encoder in "vpxenc --help".
func version(name, out string) string {
	var lines []string
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if name != "vpxenc" {
			if line != "" {
				lines = append(lines, line)
			}
		} else if rest, ok := strings.CutPrefix(line, "vp8 "); ok {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "-"))
		}
	}
	return strings.Join(lines, ", ")
}

// gen runs the tools in dir.
type gen struct {
	dir     string
	written map[string]bool
	cmds    []string // the commands that made the current vector
}

func (g *gen) path(name string) string { return filepath.Join(g.dir, name) }

func (g *gen) run(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = g.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

// record runs a command that makes the current vector, and records it.
func (g *gen) record(name string, args ...string) error {
	g.cmds = append(g.cmds, strings.Join(append([]string{name}, args...), " "))
	_, err := g.run(name, args...)
	return err
}

// file writes what data returns to name, once, and returns name.
func (g *gen) file(name string, data func() ([]byte, error)) (string, error) {
	if g.written[name] {
		return name, nil
	}
	b, err := data()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if err := os.WriteFile(g.path(name), b, 0o644); err != nil {
		return "", err
	}
	g.written[name] = true
	return name, nil
}

func (g *gen) png(p picture) (string, error) {
	return g.file(p.String()+".png", func() ([]byte, error) {
		img, err := p.image()
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		err = png.Encode(&b, img)
		return b.Bytes(), err
	})
}

func (g *gen) y4m(p picture) (string, error) {
	return g.file(p.String()+".y4m", func() ([]byte, error) {
		img, err := p.image()
		if err != nil {
			return nil, err
		}
		return y4m(img), nil
	})
}

func (g *gen) gif(name string, a *gif.GIF) (string, error) {
	return g.file(name+".gif", func() ([]byte, error) {
		var b bytes.Buffer
		err := gif.EncodeAll(&b, a)
		return b.Bytes(), err
	})
}

func (g *gen) cwebp(p picture, out string, args ...string) error {
	in, err := g.png(p)
	if err != nil {
		return err
	}
	return g.record("cwebp", slices.Concat(args, []string{"-quiet", in, "-o", out})...)
}

// spec is a vector to make: make writes the file out, then webpmux sets the metadata meta.
type spec struct {
	name string
	kind string
	tool string
	make func(g *gen, out string) error
	meta metadata
}

type payload struct {
	file string
	data []byte
}

type metadata struct {
	icc, exif, xmp payload
	orientation    int // in exif
}

// muxSet is a payload set by webpmux -set what.
type muxSet struct {
	what string
	id   string // of the chunk
	p    payload
}

func (m metadata) sets() []muxSet {
	return []muxSet{{"icc", "ICCP", m.icc}, {"exif", "EXIF", m.exif}, {"xmp", "XMP ", m.xmp}}
}

// vector makes the vector of sp and returns it with the file.
func (g *gen) vector(sp spec) (vector, []byte, error) {
	g.cmds = nil
	out := sp.name + ".webp"
	sets := slices.DeleteFunc(sp.meta.sets(), func(s muxSet) bool { return s.p.data == nil })
	cur := out
	if len(sets) > 0 {
		cur = sp.name + "-0.webp"
	}
	if err := sp.make(g, cur); err != nil {
		return vector{}, nil, err
	}
	for i, s := range sets {
		in, err := g.file(s.p.file, func() ([]byte, error) { return s.p.data, nil })
		if err != nil {
			return vector{}, nil, err
		}
		next := out
		if i < len(sets)-1 {
			next = fmt.Sprintf("%s-%d.webp", sp.name, i+1)
		}
		if err := g.record("webpmux", "-set", s.what, in, cur, "-o", next); err != nil {
			return vector{}, nil, err
		}
		cur = next
	}

	data, err := os.ReadFile(g.path(out))
	if err != nil {
		return vector{}, nil, err
	}
	cs, err := chunks(data)
	if err != nil {
		return vector{}, nil, err
	}
	info, err := g.info(out)
	if err != nil {
		return vector{}, nil, err
	}
	v := vector{
		Name:        sp.name,
		Kind:        sp.kind,
		Tool:        sp.tool,
		Commands:    g.cmds,
		Size:        len(data),
		SHA256:      sum(data),
		Width:       info.width,
		Height:      info.height,
		Alpha:       info.alpha,
		Animated:    info.animated,
		Orientation: sp.meta.orientation,
	}
	var images []string
	for _, c := range cs {
		v.Chunks = append(v.Chunks, c.id)
		if c.id == "VP8 " || c.id == "VP8L" {
			images = append(images, c.id)
		}
	}
	hashes := []*string{&v.ICCSHA256, &v.EXIFSHA256, &v.XMPSHA256}
	flags := []bool{info.icc, info.exif, info.xmp}
	for i, s := range sp.meta.sets() {
		var got [][]byte
		for _, c := range cs {
			if c.id == s.id {
				got = append(got, c.data)
			}
		}
		if s.p.data == nil {
			if flags[i] || len(got) != 0 {
				return vector{}, nil, fmt.Errorf("a %q chunk that was not set", s.id)
			}
			continue
		}
		if !flags[i] || len(got) != 1 || !bytes.Equal(got[0], s.p.data) {
			return vector{}, nil, fmt.Errorf("the %q chunks are not the payload set", s.id)
		}
		*hashes[i] = sum(s.p.data)
	}

	if info.animated {
		bg := info.background
		v.LoopCount = info.loopCount
		v.Background = []int{int(bg & 0xff), int(bg >> 8 & 0xff), int(bg >> 16 & 0xff), int(bg >> 24)}
		for i, f := range info.frames {
			name := fmt.Sprintf("%s-frame%d.webp", sp.name, i+1)
			if _, err := g.run("webpmux", "-get", "frame", strconv.Itoa(i+1), out, "-o", name); err != nil {
				return vector{}, nil, err
			}
			d, err := g.decode(name, f)
			if err != nil {
				return vector{}, nil, err
			}
			v.Frames = append(v.Frames, d)
		}
	} else {
		if len(images) != 1 {
			return vector{}, nil, fmt.Errorf("image chunks %q", images)
		}
		v.Lossless = images[0] == "VP8L"
		f, err := g.decode(out, frame{Width: v.Width, Height: v.Height, Lossless: v.Lossless, Alpha: v.Alpha})
		if err != nil {
			return vector{}, nil, err
		}
		v.Frames = []frame{f}
	}
	if err := checkKind(v); err != nil {
		return vector{}, nil, err
	}
	return v, data, nil
}

func checkKind(v vector) error {
	lossy := !v.Animated && !v.Lossless
	var ok bool
	switch v.Kind {
	case "lossy", "vpx":
		ok = lossy && !v.Alpha
	case "alpha":
		ok = lossy && v.Alpha
	case "lossless":
		ok = !v.Animated && v.Lossless
	case "animated":
		ok = v.Animated
	}
	if !ok {
		return fmt.Errorf("kind %q, but lossless %t, alpha %t, animated %t", v.Kind, v.Lossless, v.Alpha, v.Animated)
	}
	return nil
}

// decode returns f with the hashes of what dwebp decodes from file, a still image of f's size.
func (g *gen) decode(file string, f frame) (frame, error) {
	w, h := f.Width, f.Height
	if !f.Lossless {
		yuv, err := g.dwebp(file, "yuv")
		if err != nil {
			return f, err
		}
		n := w*h + 2*((w+1)/2)*((h+1)/2)
		switch {
		case len(yuv) == n && !f.Alpha:
		case len(yuv) == n+w*h && f.Alpha:
			f.AlphaSHA256 = sum(yuv[n:])
		default:
			return f, fmt.Errorf("%s: %d bytes of YUV for %dx%d, alpha %t", file, len(yuv), w, h, f.Alpha)
		}
		f.YUVSHA256 = sum(yuv[:n])
	}
	pam, err := g.dwebp(file, "pam")
	if err != nil {
		return f, err
	}
	header := fmt.Sprintf("P7\nWIDTH %d\nHEIGHT %d\nDEPTH 4\nMAXVAL 255\nTUPLTYPE RGB_ALPHA\nENDHDR\n", w, h)
	pix, ok := bytes.CutPrefix(pam, []byte(header))
	if !ok || len(pix) != w*h*4 {
		return f, fmt.Errorf("%s: PAM of %d bytes is not %dx%d", file, len(pam), w, h)
	}
	for i := 3; i < len(pix) && !f.Alpha; i += 4 {
		if pix[i] != 255 {
			return f, fmt.Errorf("%s: transparent pixels without alpha", file)
		}
	}
	f.RGBASHA256 = sum(pix)
	return f, nil
}

// dwebp returns what dwebp writes for file in format, yuv or pam.
func (g *gen) dwebp(file, format string) ([]byte, error) {
	out := file + "." + format
	if _, err := g.run("dwebp", "-quiet", "-nodither", "-"+format, file, "-o", out); err != nil {
		return nil, err
	}
	return os.ReadFile(g.path(out))
}

// muxInfo is what webpmux -info prints.
type muxInfo struct {
	width, height   int
	animated, alpha bool
	icc, exif, xmp  bool
	background      uint32 // 0xAARRGGBB
	loopCount       int
	frames          []frame
}

func (g *gen) info(file string) (muxInfo, error) {
	out, err := g.run("webpmux", "-info", file)
	if err != nil {
		return muxInfo{}, err
	}
	return parseInfo(string(out))
}

func parseInfo(out string) (muxInfo, error) {
	var m muxInfo
	n := 0
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		var err error
		switch {
		case line == "", line == "No features present.", strings.HasPrefix(line, "No.:"),
			strings.HasPrefix(line, "Size of"):
		case strings.HasPrefix(line, "Canvas size:"):
			_, err = fmt.Sscanf(line, "Canvas size: %d x %d", &m.width, &m.height)
		case strings.HasPrefix(line, "Features present:"):
			m.animated = strings.Contains(line, " animation")
			m.icc = strings.Contains(line, " ICC profile")
			m.exif = strings.Contains(line, " EXIF metadata")
			m.xmp = strings.Contains(line, " XMP metadata")
			m.alpha = strings.Contains(line, " transparency")
		case strings.HasPrefix(line, "Background color"):
			_, err = fmt.Sscanf(line, "Background color : 0x%X  Loop Count : %d", &m.background, &m.loopCount)
		case strings.HasPrefix(line, "Number of frames:"):
			_, err = fmt.Sscanf(line, "Number of frames: %d", &n)
		default:
			var f frame
			f, err = parseFrame(line, len(m.frames)+1)
			m.frames = append(m.frames, f)
		}
		if err != nil {
			return m, fmt.Errorf("webpmux -info: %q: %w", line, err)
		}
	}
	if m.width <= 0 || m.height <= 0 || len(m.frames) != n || m.animated != (n > 0) {
		return m, fmt.Errorf("webpmux -info: canvas %dx%d, %d frames of %d, animation %t",
			m.width, m.height, len(m.frames), n, m.animated)
	}
	return m, nil
}

// parseFrame parses row n of the frame table of webpmux -info, whose columns are: width height alpha x_offset y_offset
// duration dispose blend image_size compression.
func parseFrame(line string, n int) (frame, error) {
	var f frame
	cols := strings.Fields(line)
	if len(cols) != 11 || cols[0] != strconv.Itoa(n)+":" {
		return f, errors.New("not a frame")
	}
	for i, p := range []*int{&f.Width, &f.Height, nil, &f.X, &f.Y, &f.Duration} {
		if p == nil {
			continue
		}
		v, err := strconv.Atoi(cols[i+1])
		if err != nil {
			return f, err
		}
		*p = v
	}
	for _, b := range []struct {
		p       *bool
		col     int
		no, yes string
	}{{&f.Alpha, 3, "no", "yes"}, {&f.Dispose, 7, "none", "background"}, {&f.Blend, 8, "no", "yes"},
		{&f.Lossless, 10, "lossy", "lossless"}} {
		switch cols[b.col] {
		case b.no:
		case b.yes:
			*b.p = true
		default:
			return f, fmt.Errorf("column %d is %q", b.col, cols[b.col])
		}
	}
	return f, nil
}

type chunk struct {
	id   string
	data []byte
}

// chunks returns the chunks of a WebP file whose RIFF size is exact, as the tools write it.
func chunks(data []byte) ([]chunk, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, errors.New("not a WebP file")
	}
	if n := binary.LittleEndian.Uint32(data[4:]); int(n) != len(data)-8 {
		return nil, fmt.Errorf("RIFF size %d in a file of %d bytes", n, len(data))
	}
	var cs []chunk
	for p := 12; p < len(data); {
		if len(data)-p < 8 {
			return nil, errors.New("truncated chunk header")
		}
		n := int(binary.LittleEndian.Uint32(data[p+4:]))
		if n > len(data)-p-8-n%2 {
			return nil, fmt.Errorf("chunk %q of %d bytes runs past the end", data[p:p+4], n)
		}
		cs = append(cs, chunk{string(data[p : p+4]), data[p+8 : p+8+n]})
		p += 8 + n + n%2
	}
	return cs, nil
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func write(m manifest, files map[string][]byte) error {
	dir := filepath.Join("testdata", "vectors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	old, err := filepath.Glob(filepath.Join(dir, "*.webp"))
	if err != nil {
		return err
	}
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	slices.SortFunc(m.Vectors, func(a, b vector) int { return strings.Compare(a.Name, b.Name) })
	size := 0
	counts := make(map[string]int)
	for _, v := range m.Vectors {
		if err := os.WriteFile(filepath.Join(dir, v.Name+".webp"), files[v.Name], 0o644); err != nil {
			return err
		}
		size += v.Size
		counts[v.Kind]++
		if v.ICCSHA256 != "" || v.EXIFSHA256 != "" || v.XMPSHA256 != "" {
			counts["metadata"]++
		}
	}
	js, err := json.Marshal(m, jsontext.WithIndent("\t"))
	if err != nil {
		return err
	}
	js = append(js, '\n')
	if err := os.WriteFile(filepath.Join("testdata", "vectors.json"), js, 0o644); err != nil {
		return err
	}
	var parts []string
	for _, k := range []string{"lossy", "alpha", "lossless", "vpx", "animated", "metadata"} {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	log.Printf("%d vectors (%s): %d bytes of files and %d bytes of vectors.json, %d bytes in all",
		len(m.Vectors), strings.Join(parts, ", "), size, len(js), size+len(js))
	return nil
}

// ivfFrame returns the first frame of an IVF file of VP8, which must be a key frame that is shown.
func ivfFrame(data []byte) ([]byte, error) {
	if len(data) < 32 || string(data[:4]) != "DKIF" || string(data[8:12]) != "VP80" {
		return nil, errors.New("not an IVF file of VP8")
	}
	p := int(binary.LittleEndian.Uint16(data[6:]))
	if p < 32 || len(data)-p < 12 {
		return nil, errors.New("no frame")
	}
	n := int(binary.LittleEndian.Uint32(data[p:]))
	f := data[p+12:]
	if n > len(f) {
		return nil, errors.New("truncated frame")
	}
	f = f[:n]
	if len(f) < 10 || f[0]&1 != 0 || f[0]&0x10 == 0 || !bytes.Equal(f[3:6], []byte{0x9d, 0x01, 0x2a}) {
		return nil, errors.New("the first frame is not a key frame that is shown")
	}
	return f, nil
}

// simpleLossy returns a WebP file of the simple lossy format with the VP8 frame f.
func simpleLossy(f []byte) []byte {
	pad := len(f) % 2
	b := binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(4+8+len(f)+pad))
	b = binary.LittleEndian.AppendUint32(append(b, "WEBPVP8 "...), uint32(len(f)))
	return append(append(b, f...), make([]byte, pad)...)
}

// y4m returns img as a YUV4MPEG2 stream of one frame with 4:2:0 chroma, converted with the equations of JFIF.
func y4m(img *image.NRGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	ys := make([]byte, 0, w*h)
	var cb, cr []int
	for y := range h {
		for x := range w {
			i := img.PixOffset(x, y)
			yy, u, v := color.RGBToYCbCr(img.Pix[i], img.Pix[i+1], img.Pix[i+2])
			ys = append(ys, yy)
			cb, cr = append(cb, int(u)), append(cr, int(v))
		}
	}
	b := fmt.Appendf(nil, "YUV4MPEG2 W%d H%d F30:1 Ip A1:1 C420jpeg\nFRAME\n", w, h)
	b = append(b, ys...)
	for _, c := range [][]int{cb, cr} {
		for y := 0; y < h; y += 2 {
			for x := 0; x < w; x += 2 {
				x1, y1 := min(x+1, w-1), min(y+1, h-1)
				b = append(b, byte((c[y*w+x]+c[y*w+x1]+c[y1*w+x]+c[y1*w+x1]+2)/4))
			}
		}
	}
	return b
}

// rng gives the same numbers on every platform: it uses only the output of the PCG generator.
type rng struct{ p *rand.PCG }

func newRNG(name string) rng {
	s := sha256.Sum256([]byte(name))
	return rng{rand.NewPCG(binary.LittleEndian.Uint64(s[:8]), binary.LittleEndian.Uint64(s[8:16]))}
}

func (r rng) intn(n int) int { return int(r.p.Uint64() % uint64(n)) }

// picture is a synthetic image, written to the file String() + ".png" or ".y4m".
type picture interface {
	String() string
	image() (*image.NRGBA, error)
}

// source is a pattern of w x h pixels, with the alpha of mask if it is not empty.
type source struct {
	pattern string
	w, h    int
	mask    string
}

func src(pattern string, w, h int) source { return source{pattern, w, h, ""} }

func masked(pattern string, w, h int, mask string) source { return source{pattern, w, h, mask} }

func (s source) String() string {
	name := fmt.Sprintf("%s-%dx%d", s.pattern, s.w, s.h)
	if s.mask != "" {
		name += "-" + s.mask
	}
	return name
}

func (s source) image() (*image.NRGBA, error) {
	img := image.NewNRGBA(image.Rect(0, 0, s.w, s.h))
	if err := paint(img, s.pattern, newRNG(source{s.pattern, s.w, s.h, ""}.String())); err != nil {
		return nil, err
	}
	if s.mask != "" {
		if err := applyMask(img, s.mask, newRNG(s.String())); err != nil {
			return nil, err
		}
	}
	return img, nil
}

func clamp(v int) uint8 { return uint8(min(max(v, 0), 255)) }

// ramp returns i in [0, n) scaled to [0, 255].
func ramp(i, n int) int {
	if n < 2 {
		return 128
	}
	return i * 255 / (n - 1)
}

func set(img *image.NRGBA, x, y, r, g, b, a int) {
	i := img.PixOffset(x, y)
	img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = clamp(r), clamp(g), clamp(b), clamp(a)
}

// fill sets each pixel to what f returns, in raster order.
func fill(img *image.NRGBA, f func(x, y int) (r, g, b, a int)) {
	for y := range img.Rect.Dy() {
		for x := range img.Rect.Dx() {
			r, g, b, a := f(x, y)
			set(img, x, y, r, g, b, a)
		}
	}
}

func paint(img *image.NRGBA, pattern string, rg rng) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	switch pattern {
	case "flat":
		fill(img, func(x, y int) (int, int, int, int) { return 90, 140, 200, 255 })
	case "gradient":
		fill(img, func(x, y int) (int, int, int, int) {
			r, g := ramp(x, w), ramp(y, h)
			return r, g, 255 - (r+g)/2, 255
		})
	case "noise":
		fill(img, func(x, y int) (int, int, int, int) { return rg.intn(256), rg.intn(256), rg.intn(256), 255 })
	case "edges":
		edges(img)
	case "glyphs":
		glyphs(img, rg)
	case "photo":
		photo(img, rg)
	case "asym":
		asym(img)
	case "palette2":
		palette(img, 2, false, rg)
	case "palette4":
		palette(img, 4, false, rg)
	case "palette16":
		palette(img, 16, false, rg)
	case "palette256":
		palette(img, 256, false, rg)
	case "palette4a":
		palette(img, 4, true, rg)
	case "mix":
		for i, p := range []string{"noise", "glyphs", "gradient", "palette16"} {
			part := image.NewNRGBA(img.Rect)
			if err := paint(part, p, rg); err != nil {
				return err
			}
			r := image.Rect(i%2*w/2, i/2*h/2, (i%2+1)*w/2, (i/2+1)*h/2)
			draw.Draw(img, r, part, r.Min, draw.Src)
		}
	default:
		return fmt.Errorf("unknown pattern %q", pattern)
	}
	return nil
}

// edges fills img with regions bounded by lines of several directions, crossed by thin lines.
func edges(img *image.NRGBA) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	lines := [][3]int{{1, 0, w / 3}, {0, 1, h / 2}, {1, 1, (w + h) / 2}, {1, -1, w / 5}, {2, 1, w}, {1, 3, 2 * h}}
	fill(img, func(x, y int) (int, int, int, int) {
		if (x+2*y)%17 == 0 || (3*x-y+1000)%29 == 0 {
			return 250, 250, 250, 255
		}
		k := 0
		for i, l := range lines {
			if l[0]*x+l[1]*y > l[2] {
				k |= 1 << i
			}
		}
		return 30 + 160*(k&1) + 60*(k>>3&1), 30 + 160*(k>>1&1) + 60*(k>>4&1), 30 + 160*(k>>2&1) + 60*(k>>5&1), 255
	})
}

// glyphs fills img with lines of text-like glyphs, from an alphabet of 16, on paper.
func glyphs(img *image.NRGBA, rg rng) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	var shapes [16][7]int
	for i := range shapes {
		stem := rg.intn(2) * 4
		for row := range shapes[i] {
			bits := 1 << stem
			if (row == 0 || row == 3 || row == 6) && rg.intn(2) == 0 {
				bits |= 0x1f
			}
			if rg.intn(4) == 0 {
				bits |= 1 << rg.intn(5)
			}
			shapes[i][row] = bits
		}
	}
	cols := (w + 5) / 6
	cells := make([]int, cols*((h+8)/9))
	for i := range cells {
		cells[i] = rg.intn(20) // 16 and above are spaces
	}
	fill(img, func(x, y int) (int, int, int, int) {
		gx, gy := x%6, y%9
		if c := cells[y/9*cols+x/6]; c < 16 && gx < 5 && gy < 7 && shapes[c][gy]>>gx&1 != 0 {
			if y/9%3 == 2 {
				return 170, 30, 30, 255
			}
			return 20, 30, 70, 255
		}
		return 250, 248, 240, 255
	})
}

// photo fills img with a landscape of sky and textured ground, soft discs and noise.
func photo(img *image.NRGBA, rg rng) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	type disc struct{ x, y, r int }
	discs := make([]disc, 5)
	colors := make([][3]int, len(discs))
	for i := range discs {
		discs[i] = disc{rg.intn(w), rg.intn(h), 2 + rg.intn(max(w, h)/3+1)}
		colors[i] = [3]int{rg.intn(256), rg.intn(256), rg.intn(256)}
	}
	horizon := h * 3 / 5
	fill(img, func(x, y int) (int, int, int, int) {
		var c [3]int
		if y < horizon {
			t := ramp(y, horizon)
			c = [3]int{70 + t*100/255, 130 + t*90/255, 220 + t*30/255}
		} else {
			t, d := ramp(y-horizon, h-horizon), (x*5+y*3)%16-8
			c = [3]int{90 + t*60/255 + d, 120 - t*40/255 + d, 40 + t*20/255 + d}
		}
		for i, s := range discs {
			dx, dy := x-s.x, y-s.y
			if d2, r2 := dx*dx+dy*dy, s.r*s.r; d2 < r2 {
				wt := (r2 - d2) * 256 / r2
				for j := range c {
					c[j] = (c[j]*(256-wt) + colors[i][j]*wt) >> 8
				}
			}
		}
		return c[0] + rg.intn(13) - 6, c[1] + rg.intn(13) - 6, c[2] + rg.intn(13) - 6, 255
	})
}

// asym fills img with four quadrants of different colors and a white mark in the top left corner, so that each of the
// eight orientations gives a different image.
func asym(img *image.NRGBA) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	fill(img, func(x, y int) (int, int, int, int) {
		switch {
		case x < w/4 && y < h/4:
			return 250, 250, 250, 255
		case x < w/2 && y < h/2:
			return 220, 40, 40, 255
		case y < h/2:
			return 40, 180, 60, 255
		case x < w/2:
			return 40, 70, 220, 255
		}
		return 230, 210, 40, 255
	})
}

// palette fills img with n colors: blocks of 3x2 pixels of random colors, or, for 256 colors, each color in turn.
// Some of the colors are transparent if transparent is true.
func palette(img *image.NRGBA, n int, transparent bool, rg rng) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	var colors [][4]int
	for len(colors) < n {
		c := [4]int{rg.intn(256), rg.intn(256), rg.intn(256), 255}
		if transparent {
			c[3] = [4]int{0, 96, 255, 255}[len(colors)%4]
		}
		if !slices.Contains(colors, c) {
			colors = append(colors, c)
		}
	}
	bw := (w + 2) / 3
	blocks := make([]int, bw*((h+1)/2))
	for i := range blocks {
		blocks[i] = rg.intn(n)
	}
	fill(img, func(x, y int) (int, int, int, int) {
		c := colors[blocks[y/2*bw+x/3]]
		if n == 256 {
			c = colors[(x+y*w)%256]
		}
		return c[0], c[1], c[2], c[3]
	})
}

func applyMask(img *image.NRGBA, mask string, rg rng) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	// Distances from the center are in half pixels.
	dist := func(x, y int) int {
		dx, dy := 2*x+1-w, 2*y+1-h
		return dx*dx + dy*dy
	}
	var f func(x, y int) int
	switch mask {
	case "half":
		f = func(x, y int) int { return 128 }
	case "rampx":
		f = func(x, y int) int { return ramp(x, w) }
	case "rampy":
		f = func(x, y int) int { return ramp(y, h) }
	case "plane":
		f = func(x, y int) int { return 3*x + 2*y }
	case "radial":
		f = func(x, y int) int { return 255 - 255*dist(x, y)/(w*w+h*h) }
	case "binary":
		r := max(min(w, h)*4/5, 1)
		f = func(x, y int) int {
			if dist(x, y) < r*r {
				return 255
			}
			return 0
		}
	case "holes":
		r := min(w, h)
		f = func(x, y int) int {
			switch d := dist(x, y); {
			case x/3%3 == 1 && y/3%3 == 1:
				return 0
			case d >= (r-3)*(r-3) && d < r*r:
				return 128
			}
			return 255
		}
	case "noise":
		f = func(x, y int) int { return rg.intn(256) }
	case "steps":
		f = func(x, y int) int { return (x/4 + y/3) % 4 * 85 }
	default:
		return fmt.Errorf("unknown mask %q", mask)
	}
	for y := range h {
		for x := range w {
			img.Pix[img.PixOffset(x, y)+3] = clamp(f(x, y))
		}
	}
	return nil
}

// sprite is frame i of n of an animation: a square that moves over the background bg (flat, photo or clear), and a
// disc that stays.
type sprite struct {
	w, h, i, n int
	bg         string
}

func (s sprite) String() string { return fmt.Sprintf("sprite-%s-%dx%d-%d", s.bg, s.w, s.h, s.i+1) }

func (s sprite) image() (*image.NRGBA, error) {
	img := image.NewNRGBA(image.Rect(0, 0, s.w, s.h))
	switch s.bg {
	case "flat":
		fill(img, func(x, y int) (int, int, int, int) { return 200, 210, 220, 255 })
	case "photo":
		photo(img, newRNG("sprite"))
	case "clear":
	default:
		return nil, fmt.Errorf("unknown background %q", s.bg)
	}
	side := s.h / 3
	x0, y0 := s.i*(s.w-side)/max(s.n-1, 1), s.h/3+s.i%2*2
	a := 255
	if s.bg == "clear" {
		a = 150 + 30*s.i
	}
	for y := y0; y < y0+side; y++ {
		for x := x0; x < x0+side; x++ {
			set(img, x, y, 220, 60+40*s.i, 40, a)
		}
	}
	cx, cy, r := s.w*3/4, s.h/4, s.h/6
	for y := range s.h {
		for x := range s.w {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
				set(img, x, y, 40, 90, 200, 255)
			}
		}
	}
	return img, nil
}

type gifFrame struct {
	r        image.Rectangle
	p        color.Palette
	index    func(x, y int) int
	delay    int
	disposal byte
}

func newGIF(w, h, loop int, global color.Palette, frames ...gifFrame) *gif.GIF {
	a := &gif.GIF{Config: image.Config{ColorModel: global, Width: w, Height: h}, LoopCount: loop}
	for _, f := range frames {
		p := image.NewPaletted(f.r, f.p)
		for y := f.r.Min.Y; y < f.r.Max.Y; y++ {
			for x := f.r.Min.X; x < f.r.Max.X; x++ {
				p.SetColorIndex(x, y, uint8(f.index(x, y)))
			}
		}
		a.Image = append(a.Image, p)
		a.Delay = append(a.Delay, f.delay)
		a.Disposal = append(a.Disposal, f.disposal)
	}
	return a
}

// spritesGIF returns an animation whose frames have odd offsets, transparent pixels, a local color table and the three
// disposal methods.
func spritesGIF() *gif.GIF {
	pal := color.Palette{color.RGBA{}}
	for i := 1; i < 16; i++ {
		pal = append(pal, color.RGBA{uint8(i * 16), uint8(255 - i*12), uint8(i * i), 255})
	}
	local := color.Palette{color.RGBA{}, color.RGBA{250, 250, 250, 255}, color.RGBA{10, 10, 10, 255},
		color.RGBA{200, 40, 40, 255}}
	return newGIF(40, 30, 2, pal,
		gifFrame{image.Rect(0, 0, 40, 30), pal, func(x, y int) int { return 1 + (x/4+y/3)%7 }, 10, gif.DisposalNone},
		gifFrame{image.Rect(5, 3, 25, 18), pal, func(x, y int) int {
			if dx, dy := 2*x-30, 2*y-21; dx*dx+dy*dy < 300 {
				return 9 + (x+y)%3
			}
			return 0
		}, 20, gif.DisposalBackground},
		gifFrame{image.Rect(15, 9, 37, 29), pal, func(x, y int) int {
			if (x+y)%5 == 0 {
				return 0
			}
			return 12 + y%4
		}, 30, gif.DisposalPrevious},
		gifFrame{image.Rect(1, 1, 11, 11), local, func(x, y int) int { return 1 + (x+y)%3 }, 40, gif.DisposalNone},
	)
}

// smoothGIF returns an animation of an odd size with smooth colors and no transparency.
func smoothGIF() *gif.GIF {
	pal := make(color.Palette, 64)
	for i := range pal {
		pal[i] = color.RGBA{uint8(i * 4), uint8(128 + i), uint8(255 - i*4), 255}
	}
	return newGIF(33, 21, 0, pal,
		gifFrame{image.Rect(0, 0, 33, 21), pal, func(x, y int) int { return (x*63/32 + y) % 64 }, 7, gif.DisposalNone},
		gifFrame{image.Rect(3, 5, 30, 20), pal, func(x, y int) int { return (x + 2*y) % 64 }, 13, gif.DisposalNone},
		gifFrame{image.Rect(10, 2, 21, 13), pal, func(x, y int) int { return x * y % 64 }, 25, gif.DisposalBackground},
	)
}

// exifData returns Exif data from the TIFF header, little-endian, whose IFD0 has only the orientation o.
func exifData(o int) []byte {
	le := binary.LittleEndian
	b := le.AppendUint16([]byte("II*\x00\x08\x00\x00\x00"), 1)
	b = le.AppendUint16(le.AppendUint16(b, 0x0112), 3) // Orientation, SHORT
	b = le.AppendUint16(le.AppendUint32(b, 1), uint16(o))
	return le.AppendUint32(append(b, 0, 0), 0)
}

// iccProfile returns an ICC v4.3 display profile named desc whose colorants are rgb (the X, Y and Z of red, green and
// blue, adapted to D50) and whose tone curves are that of sRGB.
func iccProfile(desc string, rgb [3][3]float64) []byte {
	be := binary.BigEndian
	s15Fixed16 := func(v float64) uint32 { return uint32(int32(math.Round(v * 65536))) }
	xyz := func(v [3]float64) []byte {
		b := []byte("XYZ \x00\x00\x00\x00")
		for _, c := range v {
			b = be.AppendUint32(b, s15Fixed16(c))
		}
		return b
	}
	mluc := func(s string) []byte {
		b := be.AppendUint32(be.AppendUint32([]byte("mluc\x00\x00\x00\x00"), 1), 12)
		b = be.AppendUint32(be.AppendUint32(append(b, "enUS"...), uint32(2*len(s))), 28)
		for _, c := range []byte(s) {
			b = be.AppendUint16(b, uint16(c))
		}
		return b
	}
	trc := []byte("para\x00\x00\x00\x00\x00\x03\x00\x00")
	for _, v := range []float64{2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045} {
		trc = be.AppendUint32(trc, s15Fixed16(v))
	}
	d50 := [3]float64{0.9642, 1, 0.8249}
	tags := []struct {
		sig  string
		data []byte
	}{
		{"desc", mluc(desc)}, {"cprt", mluc("No copyright, use freely")}, {"wtpt", xyz(d50)},
		{"rXYZ", xyz(rgb[0])}, {"gXYZ", xyz(rgb[1])}, {"bXYZ", xyz(rgb[2])},
		{"rTRC", trc}, {"gTRC", trc}, {"bTRC", trc},
	}
	start := 128 + 4 + 12*len(tags)
	table := be.AppendUint32(nil, uint32(len(tags)))
	var data []byte
	offsets := make(map[string]int) // tags with the same data share it
	for _, t := range tags {
		off, ok := offsets[string(t.data)]
		if !ok {
			off = start + len(data)
			offsets[string(t.data)] = off
			data = append(data, t.data...)
			data = append(data, make([]byte, -len(data)&3)...)
		}
		table = be.AppendUint32(be.AppendUint32(append(table, t.sig...), uint32(off)), uint32(len(t.data)))
	}
	h := be.AppendUint32(nil, uint32(start+len(data)))
	h = be.AppendUint32(append(h, 0, 0, 0, 0), 0x04300000)
	h = append(h, "mntrRGB XYZ "...)
	for _, v := range []uint16{2026, 11, 1, 0, 0, 0} {
		h = be.AppendUint16(h, v)
	}
	h = append(h, "acsp"...)
	h = append(h, make([]byte, 68-len(h))...)
	for _, c := range d50 {
		h = be.AppendUint32(h, s15Fixed16(c))
	}
	h = append(h, make([]byte, 128-len(h))...)
	return slices.Concat(h, table, data)
}

const xmpPacket = `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">
   <dc:title><rdf:Alt><rdf:li xml:lang="x-default">whiteroom WebP test</rdf:li></rdf:Alt></dc:title>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

// cwebp returns a vector that cwebp makes from p with args.
func cwebp(kind string, p picture, suffix string, args ...string) spec {
	name := kind + "-" + p.String() + suffix
	return spec{name: name, kind: kind, tool: "cwebp", make: func(g *gen, out string) error {
		return g.cwebp(p, out, args...)
	}}
}

var vpxOptions = []string{"--codec=vp8", "--ivf", "--limit=1", "--passes=1", "--lag-in-frames=0", "--threads=1",
	"--quiet", "--disable-warning-prompt"}

// vpxenc returns a vector of the simple lossy format with the VP8 key frame that vpxenc makes from p with args.
func vpxenc(p picture, suffix string, args ...string) spec {
	return spec{name: "vpx-" + p.String() + suffix, kind: "vpx", tool: "vpxenc", make: func(g *gen, out string) error {
		in, err := g.y4m(p)
		if err != nil {
			return err
		}
		ivf := strings.TrimSuffix(out, ".webp") + ".ivf"
		if err := g.record("vpxenc", slices.Concat(vpxOptions, args, []string{"-o", ivf, in})...); err != nil {
			return err
		}
		data, err := os.ReadFile(g.path(ivf))
		if err != nil {
			return err
		}
		f, err := ivfFrame(data)
		if err != nil {
			return fmt.Errorf("%s: %w", ivf, err)
		}
		return os.WriteFile(g.path(out), simpleLossy(f), 0o644)
	}}
}

// step is a frame of img2webp: its options and its image.
type step struct {
	opts []string
	p    picture
}

func img2webp(name string, opts []string, steps ...step) spec {
	return spec{name: name, kind: "animated", tool: "img2webp", make: func(g *gen, out string) error {
		args := slices.Clone(opts)
		for _, s := range steps {
			in, err := g.png(s.p)
			if err != nil {
				return err
			}
			args = append(append(args, s.opts...), in)
		}
		return g.record("img2webp", append(args, "-o", out)...)
	}}
}

func gif2webp(name, gifName string, a *gif.GIF, args ...string) spec {
	return spec{name: name, kind: "animated", tool: "gif2webp", make: func(g *gen, out string) error {
		in, err := g.gif(gifName, a)
		if err != nil {
			return err
		}
		return g.record("gif2webp", slices.Concat(args, []string{"-quiet", in, "-o", out})...)
	}}
}

// muxFrame is a frame of webpmux -frame: cwebp makes it from p with args, and opts are its duration, offsets,
// disposal and blending.
type muxFrame struct {
	p    picture
	args []string
	opts string
}

func webpmux(name string, opts []string, frames ...muxFrame) spec {
	return spec{name: name, kind: "animated", tool: "webpmux", make: func(g *gen, out string) error {
		var args []string
		for i, f := range frames {
			file := fmt.Sprintf("%s-f%d.webp", strings.TrimSuffix(out, ".webp"), i+1)
			if err := g.cwebp(f.p, file, f.args...); err != nil {
				return err
			}
			args = append(args, "-frame", file, f.opts)
		}
		return g.record("webpmux", slices.Concat(args, opts, []string{"-o", out})...)
	}}
}

// withMeta returns base renamed to name, with metadata m.
func withMeta(name string, base spec, m metadata) spec {
	base.name, base.meta = name, m
	return base
}

func specs() []spec {
	var s []spec
	add := func(sp ...spec) { s = append(s, sp...) }

	photo64, photo100, edges64, edges32 := src("photo", 64, 48), src("photo", 100, 75), src("edges", 64, 48),
		src("edges", 32, 32)
	for _, q := range []string{"0", "10", "50", "75", "90", "100"} {
		add(cwebp("lossy", photo64, "-q"+q, "-q", q))
	}
	for m := range 7 {
		add(cwebp("lossy", photo100, fmt.Sprintf("-m%d", m), "-m", strconv.Itoa(m)))
	}
	for n := 1; n <= 4; n++ {
		add(cwebp("lossy", src("photo", 128, 96), fmt.Sprintf("-segments%d", n), "-segments", strconv.Itoa(n)))
	}
	for _, v := range []string{"0", "50", "100"} {
		add(cwebp("lossy", edges64, "-sns"+v, "-sns", v))
	}
	for _, v := range []string{"0", "30", "100"} {
		add(cwebp("lossy", photo64, "-q50-f"+v, "-q", "50", "-f", v))
	}
	for sh := 1; sh <= 7; sh++ {
		suffix := fmt.Sprintf("-sharpness%d", sh)
		args := []string{"-q", "40", "-f", "100", "-sharpness", strconv.Itoa(sh)}
		if sh%2 == 0 {
			suffix, args = suffix+"-nostrong", append(args, "-nostrong")
		}
		add(cwebp("lossy", edges32, suffix, args...))
	}
	add(
		cwebp("lossy", photo64, "-nostrong", "-nostrong"),
		cwebp("lossy", photo64, "-af", "-af"),
		cwebp("lossy", photo64, "-sharp-yuv", "-sharp_yuv"),
		cwebp("lossy", photo100, "-preset-photo", "-preset", "photo"),
		cwebp("lossy", src("glyphs", 64, 48), "-preset-picture", "-preset", "picture"),
		cwebp("lossy", edges64, "-preset-drawing", "-preset", "drawing"),
		cwebp("lossy", src("palette16", 32, 32), "-preset-icon", "-preset", "icon"),
		cwebp("lossy", src("glyphs", 64, 48), "-preset-text", "-preset", "text"),
		cwebp("lossy", src("noise", 64, 48), "-q90-partition-limit100", "-q", "90", "-partition_limit", "100"),
		cwebp("lossy", src("noise", 64, 48), "-q75-m6", "-q", "75", "-m", "6"),
		cwebp("lossy", src("noise", 31, 13), "-q100", "-q", "100"),
		cwebp("lossy", src("gradient", 128, 96), "-q90", "-q", "90"),
		cwebp("lossy", edges64, "-q90-m6", "-q", "90", "-m", "6"),
		cwebp("lossy", src("flat", 64, 48), ""),
		cwebp("lossy", src("photo", 256, 192), "-q80", "-q", "80"),
		cwebp("lossy", src("asym", 32, 24), "-q90", "-q", "90"),
	)
	tiny := []source{src("flat", 1, 1), src("gradient", 2, 1), src("gradient", 1, 2), src("noise", 13, 1),
		src("noise", 1, 13), src("edges", 15, 15), src("photo", 16, 16), src("photo", 17, 31), src("edges", 31, 13)}
	for _, t := range tiny {
		add(cwebp("lossy", t, ""))
	}

	// These masks and options make cwebp use each ALPH filter; it never filters raw alpha.
	alpha := func(mask string) source { return masked("photo", 64, 48, mask) }
	add(
		cwebp("alpha", alpha("rampx"), ""),
		cwebp("alpha", alpha("rampx"), "-m6-best", "-m", "6", "-alpha_filter", "best"),
		cwebp("alpha", alpha("radial"), ""),
		cwebp("alpha", alpha("radial"), "-best", "-alpha_filter", "best"),
		cwebp("alpha", alpha("plane"), "-best", "-alpha_filter", "best"),
		cwebp("alpha", alpha("plane"), "-m0-best", "-m", "0", "-alpha_filter", "best"),
		cwebp("alpha", alpha("noise"), "-best", "-alpha_filter", "best"),
		cwebp("alpha", alpha("holes"), "-none", "-alpha_filter", "none"),
		cwebp("alpha", alpha("binary"), "-method0", "-alpha_method", "0"),
		cwebp("alpha", alpha("radial"), "-alpha-q0", "-alpha_q", "0"),
		cwebp("alpha", alpha("radial"), "-alpha-q50", "-alpha_q", "50"),
		cwebp("alpha", alpha("holes"), "-exact", "-exact"),
		cwebp("alpha", alpha("steps"), "-q90-m6", "-q", "90", "-m", "6"),
		cwebp("alpha", masked("photo", 128, 96, "radial"), "-q90", "-q", "90"),
		cwebp("alpha", masked("photo", 1, 1, "half"), ""),
		cwebp("alpha", masked("photo", 2, 1, "rampx"), ""),
		cwebp("alpha", masked("photo", 1, 2, "rampy"), ""),
		cwebp("alpha", masked("noise", 13, 1, "noise"), ""),
		cwebp("alpha", masked("photo", 1, 13, "rampy"), ""),
		cwebp("alpha", masked("photo", 17, 31, "holes"), ""),
	)

	// Smaller images give the same file for -z 6 and 7, and for 8 and 9.
	photo48, mix128 := src("photo", 48, 36), src("mix", 128, 96)
	for z := range 10 {
		add(cwebp("lossless", mix128, fmt.Sprintf("-z%d", z), "-z", strconv.Itoa(z)))
	}
	for _, v := range []string{"0", "40", "80"} {
		add(cwebp("lossless", photo64, "-near-lossless"+v, "-lossless", "-near_lossless", v))
	}
	add(
		cwebp("lossless", alpha("holes"), "", "-lossless"),
		cwebp("lossless", alpha("holes"), "-exact", "-lossless", "-exact"),
		cwebp("lossless", alpha("steps"), "", "-lossless"),
		cwebp("lossless", src("palette2", 64, 48), "", "-lossless"),
		cwebp("lossless", src("palette4", 64, 48), "", "-lossless"),
		cwebp("lossless", src("palette16", 64, 48), "", "-lossless"),
		cwebp("lossless", src("palette256", 64, 48), "", "-lossless"),
		cwebp("lossless", src("palette4a", 32, 32), "", "-lossless"),
		cwebp("lossless", src("palette2", 13, 7), "", "-lossless"),
		cwebp("lossless", src("palette4", 15, 15), "", "-lossless"),
		cwebp("lossless", src("palette16", 17, 31), "", "-lossless"),
		cwebp("lossless", src("glyphs", 64, 48), "", "-lossless"),
		cwebp("lossless", src("glyphs", 128, 96), "-m6-q100", "-lossless", "-m", "6", "-q", "100"),
		cwebp("lossless", src("gradient", 64, 48), "", "-lossless"),
		cwebp("lossless", src("noise", 31, 13), "", "-lossless"),
		cwebp("lossless", photo64, "-hint-photo", "-lossless", "-hint", "photo"),
		cwebp("lossless", edges64, "-hint-graph", "-lossless", "-hint", "graph"),
		cwebp("lossless", edges64, "-m6-q100", "-lossless", "-m", "6", "-q", "100"),
		cwebp("lossless", src("asym", 32, 24), "", "-lossless"),
	)
	for _, t := range tiny[:7] {
		add(cwebp("lossless", t, "", "-lossless"))
	}

	// In key frames libvpx ignores --sharpness, and without --min-q and --max-q uses quantizer 4 and no loop filter.
	quant := func(n int) []string { return []string{fmt.Sprintf("--min-q=%d", n), fmt.Sprintf("--max-q=%d", n)} }
	photo128 := src("photo", 128, 128)
	for n := range 4 {
		parts := fmt.Sprintf("--token-parts=%d", n)
		add(vpxenc(photo128, fmt.Sprintf("-token-parts%d", n), append(quant(30), parts)...))
	}
	add(vpxenc(photo64, "-q10-token-parts3", append(quant(10), "--token-parts=3")...))
	for n := range 4 {
		add(vpxenc(photo64, fmt.Sprintf("-q45-profile%d", n), append(quant(45), fmt.Sprintf("--profile=%d", n))...))
	}
	for _, n := range []int{0, 30, 63} {
		add(vpxenc(photo64, fmt.Sprintf("-q%d", n), quant(n)...))
	}
	add(
		vpxenc(src("noise", 32, 32), "-q0", quant(0)...),
		vpxenc(src("gradient", 1, 1), ""),
		vpxenc(src("noise", 13, 1), "-q24", quant(24)...),
		vpxenc(src("photo", 17, 31), "-q24", quant(24)...),
		vpxenc(src("edges", 31, 13), "-q24", quant(24)...),
		vpxenc(photo64, "-q40-error-resilient", append(quant(40), "--error-resilient=1")...),
		vpxenc(edges64, "-q63-error-resilient", append(quant(63), "--error-resilient=1")...),
		vpxenc(src("glyphs", 64, 48), "-q50-screen-content", append(quant(50), "--screen-content-mode=1")...),
		vpxenc(photo100, "-q20-tune-ssim", append(quant(20), "--tune=ssim")...),
		vpxenc(photo100, ""),
	)

	flat := func(i int) sprite { return sprite{48, 32, i, 4, "flat"} }
	clearBG := func(i int) sprite { return sprite{48, 32, i, 3, "clear"} }
	photoBG := func(i int) sprite { return sprite{48, 32, i, 4, "photo"} }
	lossless := img2webp("anim-img2webp-lossless", []string{"-loop", "0"},
		step{[]string{"-d", "50"}, flat(0)}, step{[]string{"-d", "100"}, flat(1)}, step{nil, flat(2)},
		step{[]string{"-d", "150"}, flat(3)})
	mux := webpmux("anim-webpmux", []string{"-loop", "4", "-bgcolor", "255,40,80,120"},
		muxFrame{src("photo", 48, 32), []string{"-q", "70"}, "+80+0+0+0-b"},
		muxFrame{masked("photo", 20, 16, "radial"), []string{"-q", "70"}, "+60+10+8+1+b"},
		muxFrame{src("glyphs", 16, 16), []string{"-lossless"}, "+100+30+14+0-b"},
		muxFrame{masked("photo", 24, 12, "binary"), []string{"-lossless"}, "+40+2+18+1+b"})
	add(
		lossless,
		img2webp("anim-img2webp-lossy", []string{"-loop", "2"},
			step{[]string{"-lossy", "-q", "60"}, clearBG(0)}, step{nil, clearBG(1)}, step{nil, clearBG(2)}),
		img2webp("anim-img2webp-mixed", []string{"-mixed", "-loop", "3", "-kmin", "1", "-kmax", "2"},
			step{[]string{"-d", "70"}, photoBG(0)}, step{nil, photoBG(1)}, step{nil, photoBG(2)},
			step{nil, photoBG(3)}),
		// -kmax 1 makes every frame a key frame (-kmax 0 disables key frames, unlike what -h says).
		img2webp("anim-img2webp-keyframes", []string{"-kmax", "1"},
			step{[]string{"-lossy"}, flat(0)}, step{nil, flat(1)}, step{nil, flat(2)}),
		img2webp("anim-img2webp-min-size", []string{"-min_size"}, step{nil, clearBG(0)}, step{nil, clearBG(1)},
			step{nil, clearBG(2)}),
		gif2webp("anim-gif2webp", "sprites-40x30", spritesGIF()),
		gif2webp("anim-gif2webp-lossy", "smooth-33x21", smoothGIF(), "-lossy", "-q", "70"),
		gif2webp("anim-gif2webp-mixed", "smooth-33x21", smoothGIF(), "-mixed"),
		mux,
	)

	exif := make([]payload, 9)
	for o := 1; o <= 8; o++ {
		exif[o] = payload{fmt.Sprintf("o%d.exif", o), exifData(o)}
	}
	p3 := payload{"p3.icc", iccProfile("whiteroom test P3", [3][3]float64{
		{0.515121, 0.241196, -0.001053}, {0.291977, 0.692245, 0.041885}, {0.157104, 0.066574, 0.784073}})}
	srgb := payload{"srgb.icc", iccProfile("whiteroom test sRGB", [3][3]float64{
		{0.436066, 0.222488, 0.013916}, {0.385147, 0.716873, 0.097076}, {0.143066, 0.060608, 0.714096}})}
	xmp := payload{"packet.xmp", []byte(xmpPacket)}
	asymLossy := cwebp("lossy", src("asym", 32, 24), "-q90", "-q", "90")
	for o := 1; o <= 8; o++ {
		add(withMeta(fmt.Sprintf("meta-exif-o%d-lossy", o), asymLossy, metadata{exif: exif[o], orientation: o}))
	}
	add(
		withMeta("meta-exif-o6-lossless", cwebp("lossless", src("asym", 32, 24), "", "-lossless"),
			metadata{exif: exif[6], orientation: 6}),
		withMeta("meta-exif-o3-alpha", cwebp("alpha", masked("asym", 32, 24, "holes"), ""),
			metadata{exif: exif[3], orientation: 3}),
		withMeta("meta-exif-prefix-o6-lossy", asymLossy, metadata{
			exif: payload{"o6-prefix.exif", append([]byte("Exif\x00\x00"), exifData(6)...)}, orientation: 6}),
		withMeta("meta-icc-p3-lossy", cwebp("lossy", photo64, "-q75", "-q", "75"), metadata{icc: p3}),
		withMeta("meta-icc-srgb-lossless", cwebp("lossless", photo48, "-z6", "-z", "6"), metadata{icc: srgb}),
		withMeta("meta-xmp-lossy", cwebp("lossy", src("glyphs", 64, 48), "-preset-text", "-preset", "text"),
			metadata{xmp: xmp}),
		withMeta("meta-all-alpha", cwebp("alpha", alpha("radial"), "-best", "-alpha_filter", "best"),
			metadata{icc: p3, exif: exif[8], xmp: xmp, orientation: 8}),
		withMeta("meta-anim-icc-xmp", mux, metadata{icc: p3, xmp: xmp}),
		withMeta("meta-anim-exif-o6", lossless, metadata{exif: exif[6], orientation: 6}),
	)
	return s
}
