package wpimport

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// blockIDs makes the block IDs (6.5.1) of one document: the same ones, in the same order, for the same key. idVersion is
// hashed in, so changing how IDs are made changes these too.
type blockIDs struct {
	key  string
	n    int
	used map[string]bool
}

func newBlockIDs(key string) *blockIDs {
	return &blockIDs{key: key, used: make(map[string]bool)}
}

// idSpace is 36 to the 8th power, the number of block IDs.
const idSpace = 36 * 36 * 36 * 36 * 36 * 36 * 36 * 36

func (g *blockIDs) next() string {
	for {
		g.n++
		var in []byte
		for _, s := range []string{idVersion, "block", g.key, strconv.Itoa(g.n)} {
			in = append(in, s...)
			in = append(in, 0)
		}
		sum := sha256.Sum256(in)
		digits := strconv.FormatUint(binary.BigEndian.Uint64(sum[:8])%idSpace, 36)
		id := "b_" + "00000000"[len(digits):] + digits
		if !g.used[id] {
			g.used[id] = true
			return id
		}
	}
}
