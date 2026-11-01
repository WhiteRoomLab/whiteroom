package imaging

import (
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

// Variant is a file made from an original. A still variant shows the largest centered part of the image as displayed
// with the aspect ratio of its size, scaled to its size, so that its size and format tell what it shows.
type Variant struct {
	Width, Height int
	Format        string // the MIME type of the file
	Animated      bool   // the animation, which only the published file of an animated image is
}

// label returns v for messages, as in "640x480 image/jpeg" and "400x300 image/gif animation".
func (v Variant) label() string {
	s := fmt.Sprintf("%dx%d %s", v.Width, v.Height, v.Format)
	if v.Animated {
		s += " animation"
	}
	return s
}

// Published returns the published file of the image of info: the animation as it is, or a still image of at most
// 2560 pixels on each side.
func Published(info Info) Variant {
	if info.Animated {
		return Variant{Width: info.Width, Height: info.Height, Format: info.MIMEType, Animated: true}
	}
	w, h := fit(info.Width, info.Height, maxPublishedSide, maxPublishedSide)
	return Variant{Width: w, Height: h, Format: info.Format}
}

// Set is the files that show an image at a size.
type Set struct {
	Src Variant
	// Width and Height are the size to show Src at: that of Src, or for an animation, its size scaled down to fit.
	Width, Height int
	Srcset        []Variant // in increasing width, including Src; nil if Src is the only one
}

// ForSize returns the files that show the image of info at size, an image size of the theme, or at the default size
// if size is nil: the published file and smaller ones of srcsetWidths at the default size, and at a size, a file that
// fits in it (or fills it if it crops) and one twice as large if the image is larger. An animation is shown by its
// published file at every size, scaled to fit in it (or in 2560 x 2560 pixels at the default size) and not cropped.
func ForSize(info Info, size *content.ImageSize) Set {
	pub := Published(info)
	switch {
	case info.Animated:
		maxW, maxH := maxPublishedSide, maxPublishedSide
		if size != nil {
			maxW, maxH = size.Width, size.Height
		}
		w, h := fit(info.Width, info.Height, maxW, maxH)
		return Set{Src: pub, Width: w, Height: h}
	case size == nil:
		set := Set{Src: pub, Width: pub.Width, Height: pub.Height}
		for _, l := range srcsetWidths {
			if l < pub.Width {
				w, h := fit(info.Width, info.Height, l, 0)
				set.Srcset = append(set.Srcset, Variant{Width: w, Height: h, Format: info.Format})
			}
		}
		if set.Srcset != nil {
			set.Srcset = append(set.Srcset, pub)
		}
		return set
	}
	// scaled returns the file for a screen of n device pixels per CSS pixel.
	scaled := func(n int) Variant {
		resize := fit
		if size.Crop {
			resize = crop
		}
		w, h := resize(info.Width, info.Height, n*size.Width, n*size.Height)
		return Variant{Width: w, Height: h, Format: info.Format}
	}
	x1, x2 := scaled(1), scaled(2)
	set := Set{Src: x1, Width: x1.Width, Height: x1.Height}
	if x2.Width > x1.Width {
		set.Srcset = []Variant{x1, x2}
	}
	return set
}

// Icon returns a square PNG of n pixels on each side, cropped at the center of the image of info, or of its first frame
// if it is animated; it is smaller if the image is.
func Icon(info Info, n int) Variant {
	w, h := crop(info.Width, info.Height, n, n)
	return Variant{Width: w, Height: h, Format: mediafile.PNG}
}

// fit returns the largest size within maxW x maxH pixels (0: no limit) with the aspect ratio of w x h, rounded half
// up, but not larger than w x h nor smaller than 1 x 1.
func fit(w, h, maxW, maxH int) (int, int) {
	w, h = max(w, 1), max(h, 1)
	if maxW <= 0 || maxW > w {
		maxW = w
	}
	if maxH <= 0 || maxH > h {
		maxH = h
	}
	if int64(maxW)*int64(h) <= int64(maxH)*int64(w) {
		return maxW, max(1, divRound(int64(h)*int64(maxW), int64(w)))
	}
	return max(1, divRound(int64(w)*int64(maxH), int64(h))), maxH
}

// crop returns cropW x cropH pixels if the image of w x h is at least as large in the dimension that limits its
// largest centered part with that aspect ratio; otherwise the size of that part, rounded half up, which is not scaled.
func crop(w, h, cropW, cropH int) (int, int) {
	w, h = max(w, 1), max(h, 1)
	switch {
	case cropW <= 0 || cropH <= 0:
		return fit(w, h, cropW, cropH)
	case int64(w)*int64(cropH) >= int64(cropW)*int64(h): // the height limits the part
		if h >= cropH {
			return cropW, cropH
		}
		return min(w, max(1, divRound(int64(h)*int64(cropW), int64(cropH)))), h
	case w >= cropW:
		return cropW, cropH
	}
	return w, min(h, max(1, divRound(int64(w)*int64(cropH), int64(cropW))))
}

// divRound returns a / b rounded half up, for a >= 0 and b > 0.
func divRound(a, b int64) int {
	q, r := a/b, a%b
	if r >= b-r {
		q++
	}
	return int(q)
}
