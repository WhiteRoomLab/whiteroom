package edit

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// idSource makes ULIDs: 48 bits of Unix milliseconds and 80 random bits. They grow within a process, also within a
// millisecond and when the clock goes back: then the random bits of the last one count up.
type idSource struct {
	mu   sync.Mutex
	rand io.Reader // crypto/rand unless a test replaces it
	last [16]byte
	used bool
}

func (g *idSource) next(now time.Time) ([16]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := now.UnixMilli()
	if ms < 0 || ms >= 1<<48 {
		return [16]byte{}, fmt.Errorf("the time %s is out of range for an ID (1970 to the year 10889)", now.Format(time.RFC3339))
	}
	var id [16]byte
	if lastMs := binary.BigEndian.Uint64(g.last[:8]) >> 16; g.used && uint64(ms) <= lastMs {
		id = g.last
		if countUp(id[6:]) {
			return [16]byte{}, errors.New("cannot make an ID: the 80 random bits of the last one are all ones")
		}
	} else {
		binary.BigEndian.PutUint64(id[:8], uint64(ms)<<16)
		if _, err := io.ReadFull(g.rand, id[6:]); err != nil {
			return [16]byte{}, err
		}
	}
	g.last, g.used = id, true
	return id, nil
}

// countUp adds 1 to b, a big-endian number, and reports whether it overflowed.
func countUp(b []byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return false
		}
	}
	return true
}

// newID returns a new ID for an entity of kind.
func (e *Editor) newID(kind content.RefKind) (string, error) {
	ulid, err := e.ids.next(e.now())
	if err != nil {
		return "", err
	}
	return content.FormatID(kind, ulid), nil
}

const opPrefix = "op_"

// ulidAlphabet is the alphabet of the characters of a ULID.
const ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// checkOpID checks that id is an operation ID: "op_" and a ULID, as content.CheckID checks the IDs of entities.
func checkOpID(id string) error {
	rest, ok := strings.CutPrefix(id, opPrefix)
	if !ok || len(rest) != 26 || rest[0] > '7' || strings.Trim(rest, ulidAlphabet) != "" {
		return fmt.Errorf("invalid operation ID %q: use %q and a ULID (26 characters in upper case)", id, opPrefix)
	}
	return nil
}
