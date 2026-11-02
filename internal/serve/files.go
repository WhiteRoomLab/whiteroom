package serve

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// fileName returns the name in the public directory of rest, the path of a request from the site URL: its segments,
// decoded, joined by "/" ("" for "/"); and whether rest ends with "/", for a directory. It is not ok if rest does not
// start with "/", or if a segment does not decode or is not a plainName once decoded, such as an empty segment ("//x"),
// a dot segment, a hidden file (".htaccess") or an encoded "/".
func fileName(rest string) (name string, dir, ok bool) {
	if !strings.HasPrefix(rest, "/") {
		return "", false, false
	}
	dir = strings.HasSuffix(rest, "/")
	segs := strings.Split(rest[1:], "/")
	if dir {
		segs = segs[:len(segs)-1]
	}
	for i, s := range segs {
		d, err := url.PathUnescape(s)
		if err != nil || !plainName(d) {
			return "", false, false
		}
		segs[i] = d
	}
	return strings.Join(segs, "/"), dir, true
}

// plainName reports whether s can be the name of a file or a directory that the server serves: not empty, not hidden
// (which "." and ".." also are), without "/" or control characters, and at most content.MaxFileNameBytes long, as the
// build makes names (the system rejects longer ones with another error than fs.ErrNotExist).
func plainName(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.Contains(s, "/") &&
		!strings.ContainsFunc(s, unicode.IsControl) && len(s) <= content.MaxFileNameBytes
}
