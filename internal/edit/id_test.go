package edit

import (
	"bytes"
	"io"
	"math/big"
	"slices"
	"strings"
	"sync"
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

// idNumber returns the 128 bits of id, an ID of an entity of kind, as a number.
func idNumber(t *testing.T, kind content.RefKind, id string) *big.Int {
	t.Helper()
	if err := content.CheckID(kind, id); err != nil {
		t.Fatal(err)
	}
	prefix, _, _ := strings.Cut(id, "_")
	v := new(big.Int)
	for _, c := range []byte(strings.TrimPrefix(id, prefix+"_")) {
		v.Lsh(v, 5).Or(v, big.NewInt(int64(strings.IndexByte(ulidAlphabet, c))))
	}
	return v
}

// idMillis returns the time in milliseconds that the first 48 bits of id, an ID of an entity of kind, hold.
func idMillis(t *testing.T, kind content.RefKind, id string) int64 {
	t.Helper()
	return new(big.Int).Rsh(idNumber(t, kind, id), 80).Int64()
}

func TestNewIDGrows(t *testing.T) {
	e := testEditor(t, nil)
	now := testNow
	e.now = func() time.Time { return now }

	var ids []string
	var times []int64
	add := func(kind content.RefKind) {
		id := mustID(t, e, kind)
		ids, times = append(ids, id), append(times, idMillis(t, kind, id))
	}
	for range 5 { // within a millisecond
		add(content.RefTerm)
	}
	now = now.Add(time.Millisecond)
	add(content.RefTerm)
	add(content.RefTerm)
	now = now.Add(24 * time.Hour)
	add(content.RefTerm)
	now = now.Add(-time.Hour) // the clock goes back
	add(content.RefTerm)
	add(content.RefTerm)

	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Errorf("ID %d, %s, is not larger than ID %d, %s", i, ids[i], i-1, ids[i-1])
		}
	}
	wantTimes := []int64{
		testNow.UnixMilli(), testNow.UnixMilli(), testNow.UnixMilli(), testNow.UnixMilli(), testNow.UnixMilli(),
		testNow.UnixMilli() + 1, testNow.UnixMilli() + 1,
		testNow.UnixMilli() + 1 + 24*3600*1000,
		testNow.UnixMilli() + 1 + 24*3600*1000, testNow.UnixMilli() + 1 + 24*3600*1000, // after the clock went back
	}
	if !slices.Equal(times, wantTimes) {
		t.Errorf("the times of the IDs = %v, want %v", times, wantTimes)
	}
	if !strings.HasPrefix(ids[0], "trm_") {
		t.Errorf("ID = %q, want the prefix of a term", ids[0])
	}
	// Two kinds from one source never collide, and each has its prefix.
	a, b := mustID(t, e, content.RefEntry), mustID(t, e, content.RefTranslationGroup)
	if !strings.HasPrefix(a, "ent_") || !strings.HasPrefix(b, "tg_") || strings.TrimPrefix(a, "ent_") == strings.TrimPrefix(b, "tg_") {
		t.Errorf("IDs of two kinds = %q and %q", a, b)
	}
}

func TestNewIDPredictable(t *testing.T) {
	first, second := testEditor(t, nil), testEditor(t, nil)
	for range 3 {
		a, b := mustID(t, first, content.RefTerm), mustID(t, second, content.RefTerm)
		if a != b {
			t.Errorf("two Editors with the same clock and seed made %q and %q", a, b)
		}
	}
}

// repeat reads b for ever.
type repeat byte

func (r repeat) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestNewIDCountsUp(t *testing.T) {
	e := testEditor(t, nil)
	e.ids = content.NewIDSource(io.MultiReader(bytes.NewReader([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xfe}), repeat(0)))
	var got []*big.Int
	for range 3 {
		got = append(got, idNumber(t, content.RefMedia, mustID(t, e, content.RefMedia)))
	}
	for i := 1; i < len(got); i++ {
		if d := new(big.Int).Sub(got[i], got[i-1]); d.Cmp(big.NewInt(1)) != 0 {
			t.Errorf("ID %d minus ID %d = %v, want 1", i, i-1, d)
		}
	}
	low := new(big.Int).And(got[0], new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 80), big.NewInt(1)))
	if low.Int64() != 0xfffe {
		t.Errorf("the random bits of the first ID = %#x, want 0xfffe", low)
	}
}

func TestNewIDErrors(t *testing.T) {
	e := testEditor(t, nil)
	e.ids = content.NewIDSource(repeat(0xff))
	mustID(t, e, content.RefTerm)
	_, err := e.newID(content.RefTerm)
	if want := "cannot make an ID: the 80 random bits of the last one are all ones"; err == nil || err.Error() != want {
		t.Errorf("newID() in the same millisecond as an ID of all ones = %v, want %q", err, want)
	}
	// A later millisecond has new random bits.
	e.now = func() time.Time { return testNow.Add(time.Millisecond) }
	mustID(t, e, content.RefTerm)

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
	e = testEditor(t, nil)
	e.ids = content.NewIDSource(bytes.NewReader([]byte{1, 2, 3}))
	if id, err := e.newID(content.RefTerm); err == nil {
		t.Errorf("newID() with a source of 3 bytes = %q, want an error", id)
	}
}

func TestNewIDConcurrent(t *testing.T) {
	e := testEditor(t, nil)
	const goroutines, each = 8, 200
	ids := make([][]string, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				id, err := e.newID(content.RefRedirect)
				if err != nil {
					t.Error(err)
					return
				}
				ids[g] = append(ids[g], id)
			}
		}()
	}
	wg.Wait()
	seen := make(map[string]bool)
	for g, list := range ids {
		if !slices.IsSorted(list) {
			t.Errorf("the IDs of goroutine %d are not in the order in which it made them", g)
		}
		for _, id := range list {
			if seen[id] {
				t.Fatalf("ID %q was made twice", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != goroutines*each {
		t.Errorf("%d IDs, want %d", len(seen), goroutines*each)
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
