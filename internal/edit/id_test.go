package edit

import (
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func mustID(t *testing.T, e *Editor, kind content.RefKind) string {
	t.Helper()
	id, err := e.newID(kind)
	if err != nil {
		t.Fatalf("newID(%q) = %v", kind, err)
	}
	return id
}

// TestNewIDUsesTheClock checks that newID takes the time of an ID from the clock of the Editor.
func TestNewIDUsesTheClock(t *testing.T) {
	for _, tt := range []struct {
		name string
		now  time.Time
	}{
		{"before 1970", time.UnixMilli(-1)},
		{"after the year 10889", time.UnixMilli(1 << 48)},
	} {
		e := testEditor(t, nil)
		e.now = func() time.Time { return tt.now }
		if id, err := e.newID(content.RefTerm); err == nil || !strings.Contains(err.Error(), "out of range for an ID") {
			t.Errorf("newID() %s = %q, %v, want an error that the time is out of range", tt.name, id, err)
		}
	}
}

func TestCheckOpID(t *testing.T) {
	const ulid = "01JB8Y6T2RG7M4XK9P2D5W8NQ3"
	for _, tt := range []struct {
		in string
		ok bool
	}{
		{"op_" + ulid, true},
		{"op_7ZZZZZZZZZZZZZZZZZZZZZZZZZ", true},
		{"op_00000000000000000000000000", true},
		{"op_8ZZZZZZZZZZZZZZZZZZZZZZZZZ", false},
		{"usr_" + ulid, false},
		{"op" + ulid, false},
		{ulid, false},
		{"op_", false},
		{"", false},
		{"OP_" + ulid, false},
		{"op_" + strings.ToLower(ulid), false},
		{"op_" + ulid[:25], false},
		{"op_" + ulid + "0", false},
		{"op_01JB8Y6T2RG7M4XK9P2D5W8NQI", false},
	} {
		err := checkOpID(tt.in)
		want := `invalid operation ID "` + tt.in + `": use "op_" and a ULID (26 characters in upper case)`
		switch {
		case tt.ok && err != nil:
			t.Errorf("checkOpID(%q) = %v, want nil", tt.in, err)
		case !tt.ok && (err == nil || err.Error() != want):
			t.Errorf("checkOpID(%q) = %v, want %q", tt.in, err, want)
		}
	}
}
