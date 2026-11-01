package imaging

import (
	"bytes"
	"fmt"
	"image"

	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/webp"
)

// makeFiles returns the files of the variants vs, in their order, made from data, an original that Inspect read as
// info. Each file depends only on data and its variant: a still variant that fits in the published file is made from
// the published image (the original scaled to the size of the published file) when that is smaller than the original,
// and others from the original, whichever variants are asked together.
func makeFiles(data []byte, info Info, vs []Variant) ([][]byte, error) {
	files := make([][]byte, len(vs))
	pub := Published(info)
	var stills []int // the indexes in vs of the still variants
	for i, v := range vs {
		switch {
		case v.Animated && (!info.Animated || v != pub):
			return nil, fmt.Errorf("no animation of %+v", v)
		case v.Animated:
			f, err := makeAnimation(data, info)
			if err != nil {
				return nil, err
			}
			files[i] = f
		case v.Format != mediafile.JPEG && v.Format != mediafile.PNG:
			return nil, fmt.Errorf("cannot make a still file of %s", v.Format)
		case v.Width < 1 || v.Height < 1 || v.Width > info.Width || v.Height > info.Height:
			return nil, fmt.Errorf("cannot make a file of %dx%d pixels from an image of %dx%d", v.Width, v.Height,
				info.Width, info.Height)
		default:
			stills = append(stills, i)
		}
	}
	if stills == nil {
		return files, nil
	}
	profile, err := keptICC(data, info)
	if err != nil {
		return nil, err
	}
	src, err := decode(data, info)
	if err != nil {
		return nil, err
	}
	// The variants made from the original come first, so that it can be freed before the others are made.
	var fromMid []int
	for _, i := range stills {
		if fromPublished(info, vs[i]) {
			fromMid = append(fromMid, i)
		} else if files[i], err = makeStill(src, info.Orientation, vs[i], profile); err != nil {
			return nil, err
		}
	}
	if fromMid == nil {
		return files, nil
	}
	w, h := stored(pub.Width, pub.Height, info.Orientation)
	mid, err := resize(src, w, h)
	if err != nil {
		return nil, err
	}
	for _, i := range fromMid {
		if files[i], err = makeStill(mid, info.Orientation, vs[i], profile); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// fromPublished reports whether the still variant v of an original that Inspect read as info is made from the published
// image: whether it fits in the published file, which is smaller than the original.
func fromPublished(info Info, v Variant) bool {
	pub := Published(info)
	return !pub.Animated && (pub.Width < info.Width || pub.Height < info.Height) && v.Width <= pub.Width &&
		v.Height <= pub.Height
}

// stored returns the size as stored of an image of w x h pixels as displayed with the EXIF orientation o.
func stored(w, h, o int) (int, int) {
	if o >= 5 {
		return h, w
	}
	return w, h
}

// makeStill returns the file of v made from m, an image as stored with the EXIF orientation o: the largest centered
// part of m with the aspect ratio of v, scaled to its size, as displayed, with profile.
func makeStill(m image.Image, o int, v Variant, profile []byte) ([]byte, error) {
	w, h := stored(v.Width, v.Height, o)
	if b := m.Bounds(); b.Dx() != w || b.Dy() != h {
		var err error
		if m, err = resize(m, w, h); err != nil {
			return nil, err
		}
	}
	return encode(orient(m, o), v.Format, profile)
}

// makeAnimation returns the published file of the animation in data, an original that Inspect read as info, without
// metadata: a GIF rebuilt, an APNG with only the chunks that show it, or a WebP without its metadata chunks.
func makeAnimation(data []byte, info Info) ([]byte, error) {
	switch info.MIMEType {
	case mediafile.GIF:
		return rebuildGIF(data)
	case mediafile.PNG:
		return filterAPNG(data)
	case mediafile.WebP:
		f, err := webp.Parse(data)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := f.WriteWithoutMetadata(&b); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	return nil, fmt.Errorf("no animation in %s", info.MIMEType)
}

// jobMemory returns about how many bytes makeFiles takes at most to make the files of vs from an original that Inspect
// read as info, the original included: the decoded image (Info.Memory), the published image that other variants are
// made from, the buffers of the resampler, the largest variant scaled and oriented, the files, and the buffers of the
// decoders and encoders.
func jobMemory(info Info, vs []Variant) int64 {
	m := int64(info.Size) + 2<<20 // the compressor of a PNG file alone takes about 1 MiB
	var stills, mid bool
	var largest, files int64
	for _, v := range vs {
		if v.Animated {
			m += 2 * int64(info.Size) // the file made again, while it grows
			continue
		}
		n := int64(v.Width) * int64(v.Height)
		stills = true
		largest = max(largest, n)
		files += 4 * n // the uncompressed pixels, and what the buffer of a file grows by
		mid = mid || fromPublished(info, v)
	}
	if !stills {
		return m
	}
	// The weights and the rows of the resampler take about 100 bytes for each pixel of a side of the image.
	m += info.Memory + 128*int64(info.Width+info.Height) + 8*largest + files
	if mid {
		pub := Published(info)
		m += 4 * int64(pub.Width) * int64(pub.Height)
	}
	return m
}
