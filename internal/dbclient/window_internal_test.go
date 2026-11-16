package dbclient

import (
	"bytes"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// TestWindowReadSize: the bytes that a request of a window is counted as are those that it is sent as, and the hold of a
// window is the bytes of a window and the largest request.
func TestWindowReadSize(t *testing.T) {
	largest := 0
	for _, n := range []int{0, 1, 7, 200, 4095, dbwire.MaxKey} {
		key := bytes.Repeat([]byte("k"), n)
		for _, r := range []windowRead{
			{kind: readGet, table: 2, key: key},
			{kind: readScan, table: 2, key: key},
			{kind: readObject, hash: [32]byte{1}},
		} {
			var buf bytes.Buffer
			w := dbwire.NewWriter(&buf, 1<<20)
			if e := r.encode(w); e.Err != nil {
				t.Fatal(e.Err)
			}
			if err := w.End(); err != nil {
				t.Fatal(err)
			}
			if got, want := r.size(), buf.Len(); got != want {
				t.Errorf("a request of kind %d with a key of %d bytes is counted as %d bytes, and is %d bytes", r.kind, n, got, want)
			}
			largest = max(largest, buf.Len())
		}
	}
	if want := windowHold - dbwire.WindowBytes; largest != want {
		t.Errorf("the largest request has %d bytes, and the hold is %d bytes more than a window", largest, want)
	}
}
