package edit

import (
	"fmt"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// newID returns a new ID for an entity of kind.
func (e *Editor) newID(kind content.RefKind) (string, error) {
	return e.ids.NewID(kind, e.now())
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
