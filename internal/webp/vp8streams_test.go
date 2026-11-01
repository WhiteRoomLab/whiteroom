package webp

import (
	"bytes"
	"encoding/binary"
	"flag"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var vp8Dir = flag.String("vp8dir", "", "a directory to write the hand-made VP8 frames of TestVP8Streams and "+
	"TestVP8Errors to, in WebP files, to check them with dwebp")

// randomMBs fills f.mbs with macroblocks of random modes, segments, skip flags and coefficients. The dequantized
// coefficients stay within 1024 in magnitude, as in real streams, where libwebp's SIMD transforms equal its C ones.
func (f *testVP8) randomMBs(rng *rand.Rand, bpred, skip, density float64) *testVP8 {
	mbw, mbh := (f.width+15)/16, (f.height+15)/16
	var d vp8Decoder
	if s := f.seg; s != nil {
		d.seg = segmentHeader{enabled: true, absolute: !s.updateData || s.absolute, quant: s.quant}
	}
	d.initQuant(f.q, f.qDeltas)
	f.mbs = make([]testMB, mbw*mbh)
	for i := range f.mbs {
		mb := &f.mbs[i]
		if f.seg != nil && f.seg.updateMap {
			mb.segment = rng.IntN(4)
		}
		q := &d.quant[mb.segment]
		mb.uvmode = rng.IntN(4)
		mb.ymode = rng.IntN(4)
		if rng.Float64() < bpred {
			mb.ymode = bPred
			for b := range mb.bmodes {
				mb.bmodes[b] = rng.IntN(numBModes)
			}
		}
		if f.skipProb >= 0 && rng.Float64() < skip {
			mb.skip = true
			continue
		}
		first := 0
		if mb.ymode != bPred {
			mb.coeffs[24] = randomTokens(rng, 0, q.y2, density)
			first = 1
		}
		for b := range 16 {
			mb.coeffs[b] = randomTokens(rng, first, q.y1, density)
		}
		for b := 16; b < 24; b++ {
			mb.coeffs[b] = randomTokens(rng, 0, q.uv, density)
		}
	}
	return f
}

// randomTokens returns random coefficients of a block from position first, or nil, as testMB has them.
func randomTokens(rng *rand.Rand, first int, q [2]int32, density float64) []int {
	if rng.Float64() >= density {
		return nil
	}
	n := 1 + rng.IntN(min(4, 16-first))
	if rng.IntN(4) == 0 {
		n = 16 - first
	}
	c := make([]int, n)
	for i := range c {
		if i < n-1 && rng.IntN(3) == 0 {
			continue
		}
		limit := max(1024/int(q[min(first+i, 1)]), 1)
		v := []int{1, 1, 1, 2, 3 + rng.IntN(8), 11 + rng.IntN(56), limit}[rng.IntN(7)]
		c[i] = min(v, limit) * (1 - 2*rng.IntN(2))
	}
	return c
}

// vp8Stream is a hand-made VP8 frame.
type vp8Stream struct {
	name string
	data []byte
	same string // a stream that must decode to the same planes
}

// newRNG returns random numbers for the frame of a name, which do not change when other frames are added.
func newRNG(name string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(name))
	return rand.New(rand.NewPCG(31, h.Sum64()))
}

func testFrame(w, h, q, level int) *testVP8 {
	return &testVP8{width: w, height: h, q: q, level: level, skipProb: 200}
}

// vp8Streams returns hand-made frames of what the vectors lack, and of libwebp's choices where it differs from the
// reference decoder of RFC 6386.
func vp8Streams() []vp8Stream {
	var s []vp8Stream
	add := func(name, same string, make func(rng *rand.Rand) *testVP8) {
		s = append(s, vp8Stream{name: name, data: make(newRNG(name)).vp8(), same: same})
	}
	random := func(f *testVP8, bpred, skip, density float64) func(rng *rand.Rand) *testVP8 {
		return func(rng *rand.Rand) *testVP8 { return f.randomMBs(rng, bpred, skip, density) }
	}

	add("random", "", random(testFrame(45, 37, 40, 20), 0.4, 0.2, 0.5))
	f := testFrame(45, 37, 30, 30)
	f.simple, f.sharpness = true, 3
	add("random simple filter", "", random(f, 0.4, 0.2, 0.5))
	f = testFrame(64, 48, 50, 25)
	f.seg = &testSegments{updateMap: true, updateData: true, quant: [4]int{-10, 0, 15, 40},
		level: [4]int{-20, 0, 10, 30}, probs: [3]int{100, -1, 30}}
	add("segments by deltas", "", random(f, 0.4, 0.2, 0.6))
	f = testFrame(64, 48, 50, 25)
	f.seg = &testSegments{updateMap: true, updateData: true, absolute: true, quant: [4]int{5, 127, 60, 0},
		level: [4]int{0, 63, 12, 40}, probs: [3]int{-1, 200, 0}}
	add("segments by absolute values", "", random(f, 0.4, 0.2, 0.6))
	f = testFrame(45, 37, 40, 30)
	f.lfDeltas = &testLFDeltas{update: true, ref: [4]int{-7, 3, 0, 63}, mode: [4]int{12, -1, 0, 5}}
	add("loop filter deltas", "", random(f, 0.5, 0.2, 0.5))
	f = testFrame(45, 37, 40, 30)
	f.lfDeltas = &testLFDeltas{}
	add("loop filter deltas without update", "", random(f, 0.5, 0.2, 0.5))
	f = testFrame(40, 160, 35, 20)
	f.log2Parts = 3
	add("8 partitions for 10 rows", "", random(f, 0.3, 0.3, 0.4))
	add("quantizer 0", "", random(testFrame(33, 33, 0, 10), 0.2, 0.1, 0.8))
	add("quantizer 127", "", random(testFrame(33, 33, 127, 63), 0.2, 0.1, 0.8))
	f = testFrame(33, 33, 20, 20)
	f.qDeltas = [5]int{5, -3, 7, -8, 15}
	add("quantizer deltas", "", random(f, 0.3, 0.1, 0.8))
	f = testFrame(33, 33, 2, 20)
	f.qDeltas = [5]int{-15, -15, -15, -15, -15}
	add("quantizer deltas below 0", "", random(f, 0.3, 0.1, 0.8))
	f = testFrame(33, 33, 125, 20)
	f.qDeltas = [5]int{15, 15, 15, 15, 15}
	add("quantizer deltas above 127", "", random(f, 0.3, 0.1, 0.8))
	add("token probability updates", "", func(rng *rand.Rand) *testVP8 {
		f := testFrame(33, 33, 40, 20)
		f.probs = map[[4]int]uint8{}
		for range 150 {
			i := [4]int{rng.IntN(numTypes), rng.IntN(numBands), rng.IntN(numCtx), rng.IntN(numProbs)}
			f.probs[i] = uint8([]int{0, 1, 255, rng.IntN(256)}[rng.IntN(4)])
		}
		return f.randomMBs(rng, 0.3, 0.1, 0.8)
	})
	f = testFrame(33, 33, 40, 20)
	f.skipProb = -1
	add("without skip", "", random(f, 0.3, 0, 0.5))
	f = testFrame(33, 33, 40, 20)
	f.skipProb = 0
	add("skip probability 0", "", random(f, 0.3, 0.5, 0.5))

	// The color space, clamping and scale fields change nothing; nor does the version.
	base := func() *testVP8 { return testFrame(33, 33, 40, 20).randomMBs(newRNG("plain"), 0.3, 0.2, 0.5) }
	add("plain", "", func(*rand.Rand) *testVP8 { return base() })
	add("color space and clamping bits", "plain", func(*rand.Rand) *testVP8 {
		f := base()
		f.colorSpace, f.clampType = true, true
		return f
	})
	add("scale and version 3", "plain", func(*rand.Rand) *testVP8 {
		f := base()
		f.hscale, f.vscale, f.version = 3, 1, 3
		return f
	})

	// Without a map update, every macroblock is in segment 0.
	base = func() *testVP8 {
		return testFrame(33, 33, 40, 20).randomMBs(newRNG("no segments"), 0.3, 0.2, 0.5)
	}
	add("no segments", "", func(*rand.Rand) *testVP8 { return base() })
	add("segments without map update", "no segments", func(*rand.Rand) *testVP8 {
		f := base()
		f.q, f.level = 7, 5
		f.seg = &testSegments{updateData: true, absolute: true, quant: [4]int{40, 1, 2, 3}, level: [4]int{20, 1, 2, 3}}
		return f
	})
	add("segments by deltas without map update", "no segments", func(*rand.Rand) *testVP8 {
		f := base()
		f.q, f.level = 30, 15
		f.seg = &testSegments{updateData: true, quant: [4]int{10, 1, 2, 3}, level: [4]int{5, 1, 2, 3}}
		return f
	})

	// libwebp's choices.
	f = testFrame(48, 48, 40, 20)
	f.seg = &testSegments{updateMap: true, probs: [3]int{128, 128, 128}}
	add("segments without data", "", random(f, 0.3, 0.2, 0.6))
	f = testFrame(48, 48, 30, 60)
	f.seg = &testSegments{updateMap: true, updateData: true, quant: [4]int{0, 0, 0, 0}, level: [4]int{10, -63, 63, 3},
		probs: [3]int{128, 128, 128}}
	f.lfDeltas = &testLFDeltas{update: true, ref: [4]int{-10}, mode: [4]int{-30}}
	add("segment levels out of range before the deltas", "", random(f, 0.5, 0.2, 0.3))
	f = testFrame(48, 48, 30, 20)
	f.seg = &testSegments{updateMap: true, updateData: true, absolute: true, quant: [4]int{-20, 127, 0, 120},
		probs: [3]int{128, 128, 128}}
	f.qDeltas = [5]int{12, 15, -13, 14, 9}
	add("segment quantizers out of range before the deltas", "", random(f, 0.3, 0.2, 0.6))
	// libwebp filters the inner edges of a macroblock by its coefficients after the inverse WHT, so not those of
	// macroblocks whose only coefficients are the zeros of a Y2 block.
	add("Y2 blocks of zeros", "", func(rng *rand.Rand) *testVP8 {
		f := testFrame(64, 64, 40, 30).randomMBs(rng, 0.3, 0, 0.8)
		for i := range f.mbs {
			if mb := &f.mbs[i]; (i+i/4)%2 == 1 {
				*mb = testMB{ymode: []int{vPred, hPred, tmPred}[i%3], uvmode: vPred}
				mb.coeffs[24] = make([]int, 16)
			}
		}
		return f
	})
	add("skipped B_PRED", "", random(testFrame(48, 48, 40, 30), 1, 0.5, 0.5))
	add("Y2 contexts across B_PRED", "", func(rng *rand.Rand) *testVP8 {
		f := testFrame(80, 16, 40, 30).randomMBs(rng, 0, 0, 0.5)
		for i := range f.mbs {
			f.mbs[i].coeffs[24] = []int{3, -1}
		}
		f.mbs[1] = testMB{skip: true, ymode: bPred}
		f.mbs[2].coeffs[24] = []int{0, 0, 2}
		f.mbs[3] = testMB{ymode: bPred}
		return f
	})

	f = testFrame(64, 64, 40, 20)
	f.seg = &testSegments{updateMap: true, updateData: true, absolute: true, level: [4]int{14, 15, 39, 40},
		quant: [4]int{40, 40, 40, 40}, probs: [3]int{128, 128, 128}}
	add("high edge variance thresholds", "", random(f, 0.4, 0.1, 0.3))
	for _, simple := range []bool{false, true} {
		f = testFrame(64, 64, 40, 20)
		f.simple, f.sharpness = simple, 7
		f.seg = &testSegments{updateMap: true, updateData: true, absolute: true, level: [4]int{1, 2, 3, 4},
			quant: [4]int{40, 40, 40, 40}, probs: [3]int{128, 128, 128}}
		name := "low levels with sharpness 7"
		if simple {
			name += ", simple"
		}
		add(name, "", random(f, 0.4, 0.1, 0.3))
	}
	add("level 63 at strong edges", "", func(rng *rand.Rand) *testVP8 {
		f := testFrame(64, 64, 4, 63).randomMBs(rng, 0.3, 0, 0.3)
		for i := range f.mbs {
			// Strong edges between flat blocks.
			mb := &f.mbs[i]
			mb.coeffs = [25][]int{}
			if mb.ymode != bPred {
				mb.coeffs[24] = []int{[]int{-200, 200}[i%2]}
			} else {
				mb.coeffs[i%16] = []int{150}
			}
		}
		return f
	})
	f = testFrame(32, 32, 30, 0)
	f.seg = &testSegments{updateMap: true, updateData: true, absolute: true, level: [4]int{30, 40, 50, 63},
		quant: [4]int{30, 30, 30, 30}, probs: [3]int{128, 128, 128}}
	add("frame level 0 with segment levels", "", random(f, 0.4, 0.1, 0.5))

	add("extreme tokens", "", func(*rand.Rand) *testVP8 {
		f := testFrame(16, 16, 0, 0)
		f.mbs = []testMB{{ymode: bPred, uvmode: tmPred}}
		c := &f.mbs[0].coeffs
		c[0] = []int{2114, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3}
		c[1] = []int{-2114}
		c[2] = make([]int, 16)
		c[3] = []int{1, 2, 3, 4, 5, 6, 7, 10, 11, 18, 19, 34, 35, 66, 67, 68}
		c[4] = []int{-1, -2, -3, -4, -5, -6, -7, -10, -11, -18, -19, -34, -35, -66, -67, -1000}
		c[16] = []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7}
		return f
	})
	return s
}

// vp8StreamHashes are the SHA-256 of the Y, U and V planes that dwebp -nodither -yuv decodes from the streams of
// vp8Streams in WebP files (see -vp8dir).
var vp8StreamHashes = map[string]string{
	"8 partitions for 10 rows":                          "5b89cd89ae50393d9ebbbd62a772bb68fd9b659ae5b3ccfe342f8178d456e16e",
	"Y2 blocks of zeros":                                "73169bd6356614470ccbac6c1ca7f9c251351a726cac12f581f9061a8a7767d3",
	"Y2 contexts across B_PRED":                         "d223824585a940465e0964b2f363e5ac516a8b8ee629592972da9b2c6d8cce57",
	"color space and clamping bits":                     "ef52107c3602c798b16e969817a89c24b366d83ade41e143a99bef1b0f00da71",
	"extreme tokens":                                    "0a3f53fbd906a3a992fa3ed828ec06c4c1868ed29de62d83b57d8a8dff669ac9",
	"frame level 0 with segment levels":                 "6544fe42de0a580fa37a52efadd659bc4ae698ee3ec9988349715289963e3d54",
	"high edge variance thresholds":                     "b81cb2b029ddacfbd659087d3a778a9fcd06931ed91896a07310cb5bcba48a2b",
	"level 63 at strong edges":                          "f3b69ccffaf8b50fdb21c7c1c6aa92453e7137898b73c2ce1fb456c6d44a0f8d",
	"loop filter deltas without update":                 "4f7a1169f619a1e298d8310975d245369355f150e3d3acf6e9a6f230f2bbb58c",
	"loop filter deltas":                                "4b61993b4552da66209c8d83e14d9245c6ef21ca1edbd3d04083ea2d9ddb47d2",
	"low levels with sharpness 7, simple":               "e86d2490013c6b515a659b3c4b39d1141ef861dc68529397e13429427751b6a7",
	"low levels with sharpness 7":                       "a13c0e002d7c8a4bb0290035f5ae077e525a04d83b397cce2197602bd7628ec7",
	"no segments":                                       "82429f2cd9e21e72d75b4a059a419d9e411e5306a6f981224574e1938e1ad04e",
	"plain":                                             "ef52107c3602c798b16e969817a89c24b366d83ade41e143a99bef1b0f00da71",
	"quantizer 0":                                       "ff2ad85431f5aa64555333ee65cec192c9e8caacda6f71d8fd84614f8ae43b05",
	"quantizer 127":                                     "d15ff869157d9a44440208c1430f23bb93d7a9aba9f4e09e662207019ffb6525",
	"quantizer deltas above 127":                        "e9a08fce8ad7b977dc9d4c83e1ccf17652e780d5e750c1e338d0338d8d15666d",
	"quantizer deltas below 0":                          "a74e81cd771351979b7b8c703a7c94f241627080cd8f35820bf1a05ea76a1269",
	"quantizer deltas":                                  "365a7c7a67de9de06e3195f08f0b47b0596e396237a46d13eaa47322b98c9652",
	"random simple filter":                              "7b5a58f631ea9f4424d16326fd8e21d92290e03e489c8e562097c15f3240f4d4",
	"random":                                            "1ba820b4f0af64fa258a88c208f1e207f3f05469cce22d4c2fe2905b094f93a6",
	"scale and version 3":                               "ef52107c3602c798b16e969817a89c24b366d83ade41e143a99bef1b0f00da71",
	"segment levels out of range before the deltas":     "a548c8b2c66be1743b1cd25492cdefed0e163b99664db703035b921c7a8d0399",
	"segment quantizers out of range before the deltas": "fabd71c5dfcff2aee517106d08f14cf6351baafec533685de7d92c0fbb7e7fff",
	"segments by absolute values":                       "1c56d59c4ae1242432345a9d5ec10db54ede316c3a4bbb1ce42bb70ff39febcd",
	"segments by deltas without map update":             "82429f2cd9e21e72d75b4a059a419d9e411e5306a6f981224574e1938e1ad04e",
	"segments by deltas":                                "4af3b3c4064cdb2230dfbc765a7a440ef216953aa94ae5052666d916aa79076c",
	"segments without data":                             "cd41d281efb0516df709012e0aeb713952f3a1ae02da8bd28f33b9ddfff08250",
	"segments without map update":                       "82429f2cd9e21e72d75b4a059a419d9e411e5306a6f981224574e1938e1ad04e",
	"skip probability 0":                                "cf177d79e637f18cb8024ff8067a40edc49026a07042e48cac9135d152aaffb5",
	"skipped B_PRED":                                    "3439a49f80132b61764d22a7c186a40fb9eab3c3295e457f8ddb2f3a74010919",
	"token probability updates":                         "d87d0a228e47817b08c5aa0812d091226c7afbe6db234fb917eea36640b1879f",
	"without skip":                                      "b5f7714a8f8b825265b242fccdb1b96b0847a0a1ba23fcb2f4a14c20988792e7",
}

func TestVP8Streams(t *testing.T) {
	planes := make(map[string][]byte)
	for _, s := range vp8Streams() {
		writeStream(t, s.name, s.data)
		img, err := decodeVP8(s.data)
		if err != nil {
			t.Errorf("%s: %v", s.name, err)
			continue
		}
		yuv := slices.Concat(img.y, img.u, img.v)
		planes[s.name] = yuv
		if want := vp8StreamHashes[s.name]; sha256Hex(yuv) != want {
			t.Errorf("%s: SHA-256 %s, want %s", s.name, sha256Hex(yuv), want)
		}
		if s.same != "" && !bytes.Equal(yuv, planes[s.same]) {
			t.Errorf("%s: the planes differ from those of %s", s.name, s.same)
		}
	}
}

// writeStream writes a frame to -vp8dir, if given, in a WebP file.
func writeStream(t *testing.T, name string, data []byte) {
	t.Helper()
	if *vp8Dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(*vp8Dir, name+".webp"), riff(chunk("VP8 ", data)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// vp8ErrorStreams returns hand-made frames that dwebp rejects.
func vp8ErrorStreams() []vp8Stream {
	var s []vp8Stream
	add := func(name string, make func(f *testVP8)) {
		f := &testVP8{width: 33, height: 40, q: 40, level: 20, skipProb: 128}
		make(f)
		if f.mbs == nil {
			f.randomMBs(newRNG(name), 0.3, 0.2, 0.6)
		}
		s = append(s, vp8Stream{name: name, data: f.vp8()})
	}
	add("empty last partition", func(f *testVP8) {
		f.log2Parts = 1
		f.edit = func(_ *[]byte, parts [][]byte) { parts[1] = nil }
	})
	add("empty last partition that no row uses", func(f *testVP8) {
		f.log2Parts = 3
		f.edit = func(first *[]byte, parts [][]byte) {
			parts[7] = nil
			// An even size, or dwebp would give the last partition the padding byte of the chunk.
			n := 10 + len(*first) + 3*7
			for _, p := range parts {
				n += len(p)
			}
			if n%2 == 1 {
				parts[6] = append(parts[6], 0)
			}
		}
	})
	add("partition size past the end", func(f *testVP8) {
		f.log2Parts = 2
		f.partSizes = func(sizes []int) { sizes[1] += 1000 }
	})
	add("first partition too short", func(f *testVP8) {
		f.edit = func(first *[]byte, _ [][]byte) { *first = (*first)[:len(*first)/2] }
	})
	add("token partition too short", func(f *testVP8) {
		f.edit = func(_ *[]byte, parts [][]byte) { parts[0] = parts[0][:len(parts[0])/2] }
	})
	// libwebp's bit reader is at its end as soon as it starts on an empty partition, even if no macroblock reads it.
	add("empty partition of skipped macroblocks", func(f *testVP8) {
		f.height, f.log2Parts = 32, 1
		f.randomMBs(newRNG("empty partition of skipped macroblocks"), 0.3, 0.2, 0.6)
		for i := range 3 {
			f.mbs[i] = testMB{skip: true}
		}
		f.edit = func(_ *[]byte, parts [][]byte) { parts[0] = nil }
	})
	f := testFrame(33, 40, 40, 20)
	f.log2Parts = 3
	data := f.randomMBs(newRNG("partition sizes"), 0.3, 0.2, 0.6).vp8()
	first := int(binary.LittleEndian.Uint32(data)&0xffffff) >> 5
	s = append(s, vp8Stream{name: "partition sizes past the end", data: data[:10+first+20]},
		vp8Stream{name: "first partition past the end", data: data[:10+first-1]})
	return s
}

func TestVP8Errors(t *testing.T) {
	for _, s := range vp8ErrorStreams() {
		writeStream(t, "error "+s.name, s.data)
		if _, err := decodeVP8(s.data); err == nil {
			t.Errorf("%s: no error", s.name)
		}
	}

	// A lossy frame cut short decodes as before if the decoder does not need the missing data, as libwebp decides,
	// and fails otherwise.
	for _, v := range loadVectors(t) {
		f, err := Parse(readVector(t, v))
		if err != nil {
			t.Fatal(err)
		}
		for i, fr := range f.Frames {
			if fr.lossless {
				continue
			}
			full, err := decodeVP8(fr.bitstream)
			if err != nil {
				t.Fatal(err)
			}
			want := slices.Concat(full.y, full.u, full.v)
			n := len(fr.bitstream)
			for _, cut := range []int{n / 2, n - 8, n - 3, n - 1} {
				img, err := decodeVP8(fr.bitstream[:cut])
				switch {
				case err == nil && cut == n/2:
					t.Errorf("%s: frame %d cut to half: no error", v.Name, i)
				case err == nil && !bytes.Equal(slices.Concat(img.y, img.u, img.v), want):
					t.Errorf("%s: frame %d cut to %d bytes of %d decodes to other planes", v.Name, i, cut, n)
				}
			}
		}
	}
}
