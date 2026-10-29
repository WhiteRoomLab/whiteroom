package mediafile_test

import (
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

func TestFormatsMediaKind(t *testing.T) {
	for _, mimeType := range mediafile.Formats() {
		want := content.MediaImage
		if mimeType == mediafile.PDF {
			want = content.MediaPDF
		}
		if got := content.MediaKind(mimeType); got != want {
			t.Errorf("content.MediaKind(%q) = %q, want %q", mimeType, got, want)
		}
	}
	// Media can have only the MIME types that Probe returns.
	mimeTypes := append(mediafile.Formats(),
		"image/svg+xml", "image/avif", "image/heic", "image/heif", "image/bmp", "image/tiff", "image/x-icon",
		"image/vnd.microsoft.icon", "image/apng", "image/jxl", "image/jpg", "image/pjpeg", "image/x-png", "IMAGE/JPEG",
		"text/html", "text/plain", "text/xml", "application/xml", "application/xhtml+xml", "application/zip",
		"application/octet-stream", "application/postscript", "video/mp4", "audio/mpeg", "",
	)
	for _, mimeType := range mimeTypes {
		if got, want := content.MediaKind(mimeType) != "", slices.Contains(mediafile.Formats(), mimeType); got != want {
			t.Errorf("content.MediaKind(%q) = %q, want it to be empty unless Probe returns the MIME type", mimeType, content.MediaKind(mimeType))
		}
	}
}
