// Package imaging makes the published files and the derived files of image media from their originals, as the concept
// 6.6.2 describes.
package imaging

import "github.com/WhiteRoomLab/whiteroom/internal/content"

const (
	maxPublishedSide = 2560 // the long side of the published file of a still image
	jpegQuality      = 85
	// processingVersion is in every cache key: bump it whenever the files made from the same original change.
	processingVersion = 1
	maxICCBytes       = 4 << 20 // of the ICC profile of an original; larger ones are invalid
)

// srcsetWidths are the widths of the smaller files of an image shown at the default size.
var srcsetWidths = [...]int{640, 960, 1280, 1920, 2560}

// maxAnimationPixels is the most pixels that the frames of a GIF animation can have in all for it to be rebuilt; a
// variable for tests.
var maxAnimationPixels int64 = 10 * content.MaxImagePixels

// IconSizes returns the sides of the square icons that Icon makes for a site, in pixels, in a new slice.
func IconSizes() []int {
	return []int{32, 180, 192, 512}
}
