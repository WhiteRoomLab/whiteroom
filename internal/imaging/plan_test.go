package imaging

import (
	"fmt"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

func TestFit(t *testing.T) {
	tests := []struct{ w, h, maxW, maxH, wantW, wantH int }{
		{4000, 3000, 2560, 2560, 2560, 1920},
		{3000, 4000, 2560, 2560, 1920, 2560},
		{5000, 5000, 2560, 2560, 2560, 2560},
		{10000, 10000, 2560, 2560, 2560, 2560},
		{2560, 1440, 2560, 2560, 2560, 1440},
		{500, 400, 2560, 2560, 500, 400},
		{300, 200, 600, 400, 300, 200}, // not upscaled
		{300, 200, 0, 0, 300, 200},
		{1, 1, 2560, 2560, 1, 1},
		{1000, 333, 640, 0, 640, 213},
		{1000, 500, 0, 300, 600, 300},
		{5, 3, 3, 0, 3, 2},
		{3, 5, 0, 3, 2, 3},
		// Rounded half up.
		{100, 3, 50, 0, 50, 2},
		{100, 1, 50, 0, 50, 1},
		{3, 2, 0, 1, 2, 1},
		// At least 1 x 1.
		{100, 1, 10, 0, 10, 1},
		{1, 100, 0, 10, 1, 10},
		{100_000_000, 1, 2560, 2560, 2560, 1},
		{1, 100_000_000, 2560, 2560, 1, 2560},
		{0, 0, 10, 10, 1, 1},
		// Limits below 1 are no limits.
		{300, 200, -1, -1, 300, 200},
	}
	for _, tt := range tests {
		if w, h := fit(tt.w, tt.h, tt.maxW, tt.maxH); w != tt.wantW || h != tt.wantH {
			t.Errorf("fit(%d, %d, %d, %d) = %d, %d; want %d, %d", tt.w, tt.h, tt.maxW, tt.maxH, w, h, tt.wantW, tt.wantH)
		}
	}
}

func TestCrop(t *testing.T) {
	tests := []struct{ w, h, cropW, cropH, wantW, wantH int }{
		{1000, 500, 300, 300, 300, 300},
		{500, 1000, 300, 300, 300, 300},
		{300, 300, 300, 300, 300, 300},
		{4000, 3000, 1200, 630, 1200, 630},
		{2000, 100, 32, 32, 32, 32},
		// Smaller than the size in the dimension that limits: the largest part with the aspect ratio, not scaled.
		{200, 100, 300, 300, 100, 100},
		{2000, 20, 32, 32, 20, 20},
		{1000, 1000, 1200, 630, 1000, 525},
		{100, 1000, 1200, 630, 100, 53}, // 52.5
		{10, 3, 7, 4, 5, 3},             // 5.25
		{10, 3, 5, 6, 3, 3},             // 2.5
		{3, 10, 6, 5, 3, 3},             // 2.5
		{1, 1, 512, 512, 1, 1},
		{1, 1000, 512, 512, 1, 1},
		// Without both sides, as fit.
		{100, 50, 0, 30, 60, 30},
	}
	for _, tt := range tests {
		if w, h := crop(tt.w, tt.h, tt.cropW, tt.cropH); w != tt.wantW || h != tt.wantH {
			t.Errorf("crop(%d, %d, %d, %d) = %d, %d; want %d, %d", tt.w, tt.h, tt.cropW, tt.cropH, w, h, tt.wantW,
				tt.wantH)
		}
	}
}

func TestDivRound(t *testing.T) {
	tests := []struct {
		a, b int64
		want int
	}{
		{0, 1, 0}, {1, 2, 1}, {1, 3, 0}, {2, 3, 1}, {5, 2, 3}, {7, 2, 4}, {9, 4, 2}, {10, 4, 3},
		{1<<62 + 1, 1 << 62, 1}, {1<<40 - 1, 1 << 20, 1 << 20},
	}
	for _, tt := range tests {
		if got := divRound(tt.a, tt.b); got != tt.want {
			t.Errorf("divRound(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// still returns the Info of a still image of w x h pixels that makes files in format.
func still(w, h int, format string) Info {
	return Info{MIMEType: format, Width: w, Height: h, Orientation: 1, Format: format}
}

// animation returns the Info of a GIF animation of w x h pixels.
func animation(w, h int) Info {
	return Info{MIMEType: mediafile.GIF, Width: w, Height: h, Orientation: 1, Animated: true, Format: mediafile.PNG}
}

func jpegVariant(w, h int) Variant { return Variant{Width: w, Height: h, Format: mediafile.JPEG} }
func pngVariant(w, h int) Variant  { return Variant{Width: w, Height: h, Format: mediafile.PNG} }

func TestPublished(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want Variant
	}{
		{"landscape", still(4000, 3000, mediafile.JPEG), jpegVariant(2560, 1920)},
		{"portrait", still(3000, 4000, mediafile.JPEG), jpegVariant(1920, 2560)},
		{"square", still(10000, 10000, mediafile.PNG), pngVariant(2560, 2560)},
		{"small", still(500, 400, mediafile.PNG), pngVariant(500, 400)},
		{"exactly 2560", still(2560, 1440, mediafile.JPEG), jpegVariant(2560, 1440)},
		{"1x1", still(1, 1, mediafile.PNG), pngVariant(1, 1)},
		{"GIF as PNG", Info{MIMEType: mediafile.GIF, Width: 30, Height: 20, Orientation: 1, Format: mediafile.PNG},
			pngVariant(30, 20)},
		{"WebP as JPEG", Info{MIMEType: mediafile.WebP, Width: 30, Height: 20, Orientation: 1, Format: mediafile.JPEG},
			jpegVariant(30, 20)},
		{"animation", animation(400, 300), Variant{Width: 400, Height: 300, Format: mediafile.GIF, Animated: true}},
		{"large animation", animation(4000, 3000), Variant{Width: 4000, Height: 3000, Format: mediafile.GIF, Animated: true}},
		{
			"WebP animation",
			Info{MIMEType: mediafile.WebP, Width: 48, Height: 32, Orientation: 1, Animated: true, Format: mediafile.PNG},
			Variant{Width: 48, Height: 32, Format: mediafile.WebP, Animated: true},
		},
	}
	for _, tt := range tests {
		if got := Published(tt.info); got != tt.want {
			t.Errorf("%s: Published() = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestForSize(t *testing.T) {
	var (
		thumbnail = &content.ImageSize{Name: "thumbnail", Width: 150, Height: 150, Crop: true}
		medium    = &content.ImageSize{Name: "medium", Width: 300, Height: 300}
		large     = &content.ImageSize{Name: "large", Width: 1024}
		tall      = &content.ImageSize{Name: "tall", Height: 400}
		og        = &content.ImageSize{Name: "og", Width: 1200, Height: 630, Crop: true}
		wide      = &content.ImageSize{Name: "wide", Width: 2560}
	)
	landscape := still(4000, 3000, mediafile.JPEG)
	tests := []struct {
		name string
		info Info
		size *content.ImageSize
		want Set
	}{
		{
			name: "landscape at the default size",
			info: landscape,
			want: Set{Src: jpegVariant(2560, 1920), Width: 2560, Height: 1920, Srcset: []Variant{
				jpegVariant(640, 480), jpegVariant(960, 720), jpegVariant(1280, 960), jpegVariant(1920, 1440),
				jpegVariant(2560, 1920),
			}},
		},
		{
			name: "portrait at the default size",
			info: still(3000, 4000, mediafile.JPEG),
			want: Set{Src: jpegVariant(1920, 2560), Width: 1920, Height: 2560, Srcset: []Variant{
				jpegVariant(640, 853), jpegVariant(960, 1280), jpegVariant(1280, 1707), jpegVariant(1920, 2560),
			}},
		},
		{
			name: "square at the default size",
			info: still(10000, 10000, mediafile.PNG),
			want: Set{Src: pngVariant(2560, 2560), Width: 2560, Height: 2560, Srcset: []Variant{
				pngVariant(640, 640), pngVariant(960, 960), pngVariant(1280, 1280), pngVariant(1920, 1920),
				pngVariant(2560, 2560),
			}},
		},
		{
			name: "exactly 2560 at the default size",
			info: still(2560, 1440, mediafile.JPEG),
			want: Set{Src: jpegVariant(2560, 1440), Width: 2560, Height: 1440, Srcset: []Variant{
				jpegVariant(640, 360), jpegVariant(960, 540), jpegVariant(1280, 720), jpegVariant(1920, 1080),
				jpegVariant(2560, 1440),
			}},
		},
		{
			name: "641 wide at the default size",
			info: still(641, 400, mediafile.JPEG),
			want: Set{Src: jpegVariant(641, 400), Width: 641, Height: 400, Srcset: []Variant{
				jpegVariant(640, 399), jpegVariant(641, 400),
			}},
		},
		{
			name: "640 wide at the default size",
			info: still(640, 400, mediafile.JPEG),
			want: Set{Src: jpegVariant(640, 400), Width: 640, Height: 400},
		},
		{
			name: "smaller than 640 at the default size",
			info: still(500, 400, mediafile.PNG),
			want: Set{Src: pngVariant(500, 400), Width: 500, Height: 400},
		},
		{
			name: "tall and narrow at the default size",
			info: still(1000, 100_000, mediafile.PNG),
			want: Set{Src: pngVariant(26, 2560), Width: 26, Height: 2560},
		},
		{
			name: "1x1 at the default size",
			info: still(1, 1, mediafile.PNG),
			want: Set{Src: pngVariant(1, 1), Width: 1, Height: 1},
		},
		{
			name: "crop",
			info: landscape,
			size: thumbnail,
			want: Set{Src: jpegVariant(150, 150), Width: 150, Height: 150, Srcset: []Variant{
				jpegVariant(150, 150), jpegVariant(300, 300),
			}},
		},
		{
			name: "width and height",
			info: landscape,
			size: medium,
			want: Set{Src: jpegVariant(300, 225), Width: 300, Height: 225, Srcset: []Variant{
				jpegVariant(300, 225), jpegVariant(600, 450),
			}},
		},
		{
			name: "width only",
			info: landscape,
			size: large,
			want: Set{Src: jpegVariant(1024, 768), Width: 1024, Height: 768, Srcset: []Variant{
				jpegVariant(1024, 768), jpegVariant(2048, 1536),
			}},
		},
		{
			name: "height only",
			info: landscape,
			size: tall,
			want: Set{Src: jpegVariant(533, 400), Width: 533, Height: 400, Srcset: []Variant{
				jpegVariant(533, 400), jpegVariant(1067, 800),
			}},
		},
		{
			name: "crop of a size for OGP",
			info: landscape,
			size: og,
			want: Set{Src: jpegVariant(1200, 630), Width: 1200, Height: 630, Srcset: []Variant{
				jpegVariant(1200, 630), jpegVariant(2400, 1260),
			}},
		},
		{
			// 2x is larger than the published file, from the original.
			name: "1x of the published file",
			info: landscape,
			size: wide,
			want: Set{Src: jpegVariant(2560, 1920), Width: 2560, Height: 1920, Srcset: []Variant{
				jpegVariant(2560, 1920), jpegVariant(4000, 3000),
			}},
		},
		{
			name: "2x of the published file",
			info: still(500, 400, mediafile.PNG),
			size: medium,
			want: Set{Src: pngVariant(300, 240), Width: 300, Height: 240, Srcset: []Variant{
				pngVariant(300, 240), pngVariant(500, 400),
			}},
		},
		{
			name: "2x dropped: smaller than the size",
			info: still(500, 400, mediafile.PNG),
			size: large,
			want: Set{Src: pngVariant(500, 400), Width: 500, Height: 400},
		},
		{
			name: "2x dropped: crop smaller than the size",
			info: still(1000, 800, mediafile.JPEG),
			size: og,
			want: Set{Src: jpegVariant(1000, 525), Width: 1000, Height: 525},
		},
		{
			name: "2x dropped: as wide as 1x",
			info: still(150, 1000, mediafile.JPEG),
			size: thumbnail,
			want: Set{Src: jpegVariant(150, 150), Width: 150, Height: 150},
		},
		{
			name: "crop of 1x1",
			info: still(1, 1, mediafile.PNG),
			size: thumbnail,
			want: Set{Src: pngVariant(1, 1), Width: 1, Height: 1},
		},
		{
			name: "animation at the default size",
			info: animation(400, 300),
			want: Set{Src: Variant{Width: 400, Height: 300, Format: mediafile.GIF, Animated: true}, Width: 400, Height: 300},
		},
		{
			name: "large animation at the default size",
			info: animation(4000, 3000),
			want: Set{Src: Variant{Width: 4000, Height: 3000, Format: mediafile.GIF, Animated: true}, Width: 2560, Height: 1920},
		},
		{
			name: "animation at a size",
			info: animation(400, 300),
			size: medium,
			want: Set{Src: Variant{Width: 400, Height: 300, Format: mediafile.GIF, Animated: true}, Width: 300, Height: 225},
		},
		{
			name: "animation at a size that crops",
			info: animation(400, 300),
			size: thumbnail,
			want: Set{Src: Variant{Width: 400, Height: 300, Format: mediafile.GIF, Animated: true}, Width: 150, Height: 113},
		},
		{
			name: "animation at a larger size",
			info: animation(400, 300),
			size: large,
			want: Set{Src: Variant{Width: 400, Height: 300, Format: mediafile.GIF, Animated: true}, Width: 400, Height: 300},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ForSize(tt.info, tt.size)
			if got.Src != tt.want.Src || got.Width != tt.want.Width || got.Height != tt.want.Height ||
				!slices.Equal(got.Srcset, tt.want.Srcset) || (got.Srcset == nil) != (tt.want.Srcset == nil) {
				t.Errorf("ForSize() = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// TestForSizeInvariants checks the sets of images of many sizes at sizes of each kind.
func TestForSizeInvariants(t *testing.T) {
	sizes := []*content.ImageSize{
		nil,
		{Width: 150, Height: 150, Crop: true},
		{Width: 300, Height: 300},
		{Width: 1024},
		{Height: 400},
		{Width: 1200, Height: 630, Crop: true},
		{Width: 10000, Height: 10000, Crop: true},
	}
	sides := []int{1, 2, 3, 17, 150, 299, 300, 301, 639, 640, 641, 1000, 2559, 2560, 2561, 4000, 10000}
	for _, w := range sides {
		for _, h := range sides {
			for _, info := range []Info{still(w, h, mediafile.JPEG), animation(w, h)} {
				pub := Published(info)
				for _, size := range sizes {
					set := ForSize(info, size)
					name := fmt.Sprintf("%dx%d animated %t at %+v", w, h, info.Animated, size)
					checkSet(t, name, info, pub, set)
				}
			}
		}
	}
}

func checkSet(t *testing.T, name string, info Info, pub Variant, set Set) {
	t.Helper()
	if info.Animated {
		if set.Src != pub || set.Srcset != nil || set.Width > info.Width || set.Height > info.Height {
			t.Errorf("%s: %+v", name, set)
		}
		return
	}
	if set.Width != set.Src.Width || set.Height != set.Src.Height {
		t.Errorf("%s: shown at %dx%d, not at the size of Src %+v", name, set.Width, set.Height, set.Src)
	}
	if set.Srcset != nil && (len(set.Srcset) < 2 || !slices.Contains(set.Srcset, set.Src)) {
		t.Errorf("%s: Srcset %+v without Src %+v", name, set.Srcset, set.Src)
	}
	for i, v := range append([]Variant{set.Src}, set.Srcset...) {
		if v.Width < 1 || v.Height < 1 || v.Width > info.Width || v.Height > info.Height || v.Format != info.Format ||
			v.Animated {
			t.Errorf("%s: variant %+v of an image of %dx%d", name, v, info.Width, info.Height)
		}
		if i > 1 && v.Width <= set.Srcset[i-2].Width {
			t.Errorf("%s: Srcset %+v not in increasing width", name, set.Srcset)
		}
	}
}

func TestIcon(t *testing.T) {
	tests := []struct {
		name string
		info Info
		n    int
		want Variant
	}{
		{"landscape", still(4000, 3000, mediafile.JPEG), 512, pngVariant(512, 512)},
		{"portrait", still(3000, 4000, mediafile.JPEG), 32, pngVariant(32, 32)},
		{"smaller", still(100, 80, mediafile.JPEG), 180, pngVariant(80, 80)},
		{"smaller portrait", still(80, 100, mediafile.JPEG), 192, pngVariant(80, 80)},
		{"narrow", still(2000, 20, mediafile.PNG), 32, pngVariant(20, 20)},
		{"1x1", still(1, 1, mediafile.PNG), 512, pngVariant(1, 1)},
		{"animation", animation(400, 300), 192, pngVariant(192, 192)},
		{"the published file", still(512, 512, mediafile.PNG), 512, pngVariant(512, 512)},
	}
	for _, tt := range tests {
		if got := Icon(tt.info, tt.n); got != tt.want {
			t.Errorf("%s: Icon(%d) = %+v, want %+v", tt.name, tt.n, got, tt.want)
		}
	}
}

func TestIconSizes(t *testing.T) {
	want := []int{32, 180, 192, 512}
	got := IconSizes()
	if !slices.Equal(got, want) {
		t.Fatalf("IconSizes() = %d, want %d", got, want)
	}
	got[0] = 16
	if got := IconSizes(); !slices.Equal(got, want) {
		t.Errorf("after changing the result, IconSizes() = %d, want %d", got, want)
	}
}
