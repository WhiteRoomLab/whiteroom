package theme

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// asset is a file in assets/, kept in memory so that WriteAssets writes what Load has read.
type asset struct {
	data []byte
	sum  [sha256.Size]byte
}

// readAssets reads the files in assets/ and reports whether every one has loaded.
func (l *loader) readAssets() bool {
	loaded := true
	complete := l.walk("assets", checkAssetName, func(p, rel string, d fs.DirEntry) {
		if err := checkAssetName(d.Name()); err != nil {
			l.add(p, err)
			loaded = false
			return
		}
		data, ok := l.readFile(p, d, MaxAssetBytes)
		if !ok {
			loaded = false
			return
		}
		l.t.assets[rel] = asset{data: data, sum: sha256.Sum256(data)}
	})
	return complete && loaded
}

// assetNameChars are the characters of the names in the paths of assets, which URLs have as they are.
const assetNameChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~@+"

func checkAssetName(name string) error {
	if len(name) > content.MaxFileNameBytes {
		return fmt.Errorf("name too long: %d bytes (max %d)", len(name), content.MaxFileNameBytes)
	}
	if strings.Trim(name, assetNameChars) != "" {
		return fmt.Errorf(`invalid name %q: use ASCII letters, digits, "-", "_", ".", "~", "@" and "+"`, name)
	}
	return nil
}

// Assets returns the paths of the files in assets/, relative to it and sorted, such as "css/style.css".
func (t *Theme) Assets() []string {
	return slices.Sorted(maps.Keys(t.assets))
}

// WriteAssets writes the files of Assets, as Load has read them, to assets/ under root, making the directories they
// need. It fails on a file that exists, and stops at the first error.
func (t *Theme) WriteAssets(root *os.Root) error {
	for _, p := range t.Assets() {
		name := "assets/" + p
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = f.Write(t.assets[p].data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
