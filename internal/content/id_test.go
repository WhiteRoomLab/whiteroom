package content

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	mathrand "math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCheckID(t *testing.T) {
	const ulid = "01JB8Y6T2RG7M4XK9P2D5W8NQ3"
	tests := []struct {
		name string
		kind RefKind
		in   string
		ok   bool
	}{
		{name: "entry", kind: RefEntry, in: "ent_" + ulid, ok: true},
		{name: "media", kind: RefMedia, in: "med_" + ulid, ok: true},
		{name: "form", kind: RefForm, in: "frm_" + ulid, ok: true},
		{name: "term", kind: RefTerm, in: "trm_" + ulid, ok: true},
		{name: "author", kind: RefAuthor, in: "aut_" + ulid, ok: true},
		{name: "menu", kind: RefMenu, in: "mnu_" + ulid, ok: true},
		{name: "redirect", kind: RefRedirect, in: "rdr_" + ulid, ok: true},
		{name: "revision", kind: RefRevision, in: "rev_" + ulid, ok: true},
		{name: "translation group", kind: RefTranslationGroup, in: "tg_" + ulid, ok: true},
		{name: "user", kind: RefUser, in: "usr_" + ulid, ok: true},
		{name: "every character", kind: RefEntry, in: "ent_0123456789ABCDEFGHJKMNPQRS", ok: true},
		{name: "every character after the first", kind: RefEntry, in: "ent_7TVWXYZ0000000000000000000", ok: true},
		{name: "largest", kind: RefEntry, in: "ent_7ZZZZZZZZZZZZZZZZZZZZZZZZZ", ok: true},
		{name: "first character 8", kind: RefEntry, in: "ent_8ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		{name: "prefix of another kind", kind: RefEntry, in: "med_" + ulid},
		{name: "no prefix", kind: RefEntry, in: ulid},
		{name: "prefix only", kind: RefEntry, in: "ent_"},
		{name: "empty", kind: RefEntry, in: ""},
		{name: "upper case prefix", kind: RefEntry, in: "ENT_" + ulid},
		{name: "lower case", kind: RefEntry, in: "ent_01jb8y6t2rg7m4xk9p2d5w8nq3"},
		{name: "25 characters", kind: RefEntry, in: "ent_" + ulid[:25]},
		{name: "27 characters", kind: RefEntry, in: "ent_" + ulid + "0"},
		{name: "I", kind: RefEntry, in: "ent_01JB8Y6T2RG7M4XK9P2D5W8NQI"},
		{name: "L", kind: RefEntry, in: "ent_01JB8Y6T2RG7M4XK9P2D5W8NQL"},
		{name: "O", kind: RefEntry, in: "ent_01JB8Y6T2RG7M4XK9P2D5W8NQO"},
		{name: "U", kind: RefEntry, in: "ent_01JB8Y6T2RG7M4XK9P2D5W8NQU"},
		{name: "hyphen", kind: RefEntry, in: "ent_01JB8Y6T2RG7M4XK9P2D5W8N-3"},
		{name: "space in the middle", kind: RefEntry, in: "ent_01JB8Y6T2RG7M 4XK9P2D5W8NQ"},
		{name: "full-width digit", kind: RefEntry, in: "ent_01JB8Y6T2RG7M4XK9P2D5W8\uff11"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckID(tt.kind, tt.in)
			if tt.ok {
				if err != nil {
					t.Errorf("CheckID(%q, %q) = %v, want nil", tt.kind, tt.in, err)
				}
				return
			}
			want := `invalid ` + string(tt.kind) + ` ID "` + tt.in + `": use "` + idPrefixes[tt.kind] + `" and a ULID (26 characters in upper case)`
			if err == nil || err.Error() != want {
				t.Errorf("CheckID(%q, %q) = %v, want %s", tt.kind, tt.in, err, want)
			}
		})
	}
}

func TestIDPrefixes(t *testing.T) {
	kinds := []RefKind{RefEntry, RefMedia, RefForm, RefTerm, RefAuthor, RefMenu, RefRedirect, RefRevision, RefTranslationGroup, RefUser}
	if len(idPrefixes) != len(kinds) {
		t.Errorf("idPrefixes has %d kinds, want %d", len(idPrefixes), len(kinds))
	}
	seen := make(map[string]bool)
	for _, k := range kinds {
		p := idPrefixes[k]
		if p == "" || seen[p] {
			t.Errorf("prefix of %s = %q, want one of its own", k, p)
		}
		seen[p] = true
	}
}

// parseULID returns the 128 bits that the 26 characters of a ULID write.
func parseULID(t *testing.T, s string) [16]byte {
	t.Helper()
	if len(s) != 26 {
		t.Fatalf("%q has %d characters, want 26", s, len(s))
	}
	var hi, lo uint64
	for i := range len(s) {
		v := strings.IndexByte(crockford, s[i])
		if v < 0 {
			t.Fatalf("%q has %q, which is not in Crockford's Base32", s, s[i])
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(v)
	}
	var u [16]byte
	binary.BigEndian.PutUint64(u[:8], hi)
	binary.BigEndian.PutUint64(u[8:], lo)
	return u
}

func TestFormatID(t *testing.T) {
	// bit returns the bits with only the nth set, counted from the least significant.
	bit := func(n int) [16]byte {
		var u [16]byte
		u[15-n/8] = 1 << (n % 8)
		return u
	}
	var ones, time, random [16]byte
	for i := range ones {
		ones[i] = 0xff
		if i < 6 {
			time[i] = 0xff
		} else {
			random[i] = 0xff
		}
	}
	tests := []struct {
		name string
		u    [16]byte
		want string
	}{
		{"zero", [16]byte{}, "00000000000000000000000000"},
		{"all ones", ones, "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		{"lowest bit", bit(0), "00000000000000000000000001"},
		{"highest bit of the lowest character", bit(4), "0000000000000000000000000G"},
		{"lowest bit of the second character", bit(5), "00000000000000000000000010"},
		{"highest bit of the random part", bit(79), "0000000000G000000000000000"},
		{"lowest bit of the time", bit(80), "00000000010000000000000000"},
		{"highest bit", bit(127), "40000000000000000000000000"},
		{"second highest bit", bit(126), "20000000000000000000000000"},
		{"third highest bit", bit(125), "10000000000000000000000000"},
		{"time of ones", time, "7ZZZZZZZZZ0000000000000000"},
		{"random part of ones", random, "0000000000ZZZZZZZZZZZZZZZZ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatID(RefEntry, tt.u)
			if got != "ent_"+tt.want {
				t.Errorf("FormatID(entry, %x) = %q, want %q", tt.u, got, "ent_"+tt.want)
			}
			if err := CheckID(RefEntry, got); err != nil {
				t.Error(err)
			}
			if back := parseULID(t, tt.want); back != tt.u {
				t.Errorf("parseULID(%q) = %x, want %x", tt.want, back, tt.u)
			}
		})
	}
}

func TestFormatIDKinds(t *testing.T) {
	var ones [16]byte
	for i := range ones {
		ones[i] = 0xff
	}
	kinds := []RefKind{RefEntry, RefMedia, RefForm, RefTerm, RefAuthor, RefMenu, RefRedirect, RefRevision, RefTranslationGroup, RefUser}
	for _, kind := range kinds {
		for _, u := range [][16]byte{{}, ones, {0, 0, 0, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6}} {
			id := FormatID(kind, u)
			if err := CheckID(kind, id); err != nil {
				t.Errorf("FormatID(%s, %x) = %q: %v", kind, u, id, err)
			}
			if !strings.HasPrefix(id, idPrefixes[kind]) || len(id) != len(idPrefixes[kind])+26 {
				t.Errorf("FormatID(%s, %x) = %q, want the prefix %q and 26 characters", kind, u, id, idPrefixes[kind])
			}
		}
	}
}

func TestFormatIDRoundTrip(t *testing.T) {
	const ulid = "01JB8Y6T2RG7M4XK9P2D5W8NQ3"
	if got := FormatID(RefEntry, parseULID(t, ulid)); got != "ent_"+ulid {
		t.Errorf("FormatID(entry, the bits of %s) = %q, want %q", ulid, got, "ent_"+ulid)
	}
	r := mathrand.New(mathrand.NewPCG(1, 2))
	for range 2000 {
		var u [16]byte
		binary.BigEndian.PutUint64(u[:8], r.Uint64())
		binary.BigEndian.PutUint64(u[8:], r.Uint64())
		id := FormatID(RefMedia, u)
		if err := CheckID(RefMedia, id); err != nil {
			t.Fatal(err)
		}
		if back := parseULID(t, strings.TrimPrefix(id, "med_")); back != u {
			t.Fatalf("FormatID(media, %x) = %q, which is the bits %x", u, id, back)
		}
	}
}

func TestFormatIDUnknownKind(t *testing.T) {
	for _, kind := range []RefKind{"", "unknown", "Entry"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("FormatID(%q) did not panic", kind)
				}
			}()
			FormatID(kind, [16]byte{})
		}()
	}
}

// TestFormatIDOrder checks that the IDs of larger ULIDs are larger texts, as the ULID specification has it.
func TestFormatIDOrder(t *testing.T) {
	r := mathrand.New(mathrand.NewPCG(3, 4))
	for range 1000 {
		var a, b [16]byte
		for i := range a {
			a[i], b[i] = byte(r.Uint32()), byte(r.Uint32())
		}
		x, y := FormatID(RefEntry, a), FormatID(RefEntry, b)
		if (string(a[:]) < string(b[:])) != (x < y) {
			t.Fatalf("FormatID(%x) = %q and FormatID(%x) = %q are not in the order of the bits", a, x, b, y)
		}
	}
}

var idSourceNow = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

// idSourceSeed gives a new IDSource the same random bits each time.
var idSourceSeed = [32]byte{'i', 'd', 's'}

// ulidMillis returns the time in Unix milliseconds that the first 48 bits of u hold.
func ulidMillis(u [16]byte) int64 {
	return int64(binary.BigEndian.Uint64(u[:8]) >> 16)
}

// repeatByte reads its byte for ever.
type repeatByte byte

func (r repeatByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestIDSourceGrows(t *testing.T) {
	g := NewIDSource(mathrand.NewChaCha8(idSourceSeed))
	now := idSourceNow

	var ids []string
	var times []int64
	add := func() {
		id, err := g.NewID(RefTerm, now)
		if err != nil {
			t.Fatalf("NewID() = %v", err)
		}
		ids, times = append(ids, id), append(times, ulidMillis(parseULID(t, strings.TrimPrefix(id, "trm_"))))
	}
	for range 5 { // within a millisecond
		add()
	}
	now = now.Add(time.Millisecond)
	add()
	add()
	now = now.Add(24 * time.Hour)
	add()
	now = now.Add(-time.Hour) // the clock goes back
	add()
	add()

	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Errorf("ID %d, %s, is not larger than ID %d, %s", i, ids[i], i-1, ids[i-1])
		}
		if err := CheckID(RefTerm, ids[i]); err != nil {
			t.Error(err)
		}
	}
	ms := idSourceNow.UnixMilli()
	wantTimes := []int64{
		ms, ms, ms, ms, ms,
		ms + 1, ms + 1,
		ms + 1 + 24*3600*1000,
		ms + 1 + 24*3600*1000, ms + 1 + 24*3600*1000, // after the clock went back
	}
	if !slices.Equal(times, wantTimes) {
		t.Errorf("the times of the IDs = %v, want %v", times, wantTimes)
	}
}

func TestIDSourceNext(t *testing.T) {
	g := NewIDSource(mathrand.NewChaCha8(idSourceSeed))
	var ulids [][16]byte
	for range 3 {
		u, err := g.Next(idSourceNow)
		if err != nil {
			t.Fatal(err)
		}
		ulids = append(ulids, u)
	}
	for i, u := range ulids {
		if ulidMillis(u) != idSourceNow.UnixMilli() {
			t.Errorf("the time of ULID %d = %d, want %d", i, ulidMillis(u), idSourceNow.UnixMilli())
		}
		if i > 0 && bytes.Compare(ulids[i-1][:], u[:]) >= 0 {
			t.Errorf("ULID %d, %x, is not larger than ULID %d, %x", i, u, i-1, ulids[i-1])
		}
	}
	// NewID is the ID of the next ULID.
	h := NewIDSource(mathrand.NewChaCha8(idSourceSeed))
	for i, u := range ulids {
		id, err := h.NewID(RefMedia, idSourceNow)
		if err != nil {
			t.Fatal(err)
		}
		if want := FormatID(RefMedia, u); id != want {
			t.Errorf("NewID(media) %d = %q, want %q", i, id, want)
		}
	}
}

func TestIDSourcePredictable(t *testing.T) {
	first, second := NewIDSource(mathrand.NewChaCha8(idSourceSeed)), NewIDSource(mathrand.NewChaCha8(idSourceSeed))
	for range 3 {
		a, err := first.NewID(RefTerm, idSourceNow)
		if err != nil {
			t.Fatal(err)
		}
		b, err := second.NewID(RefTerm, idSourceNow)
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Errorf("two sources with the same clock and random bits made %q and %q", a, b)
		}
	}
}

func TestIDSourceKinds(t *testing.T) {
	g := NewIDSource(mathrand.NewChaCha8(idSourceSeed))
	kinds := []RefKind{RefEntry, RefMedia, RefForm, RefTerm, RefAuthor, RefMenu, RefRedirect, RefRevision, RefTranslationGroup, RefUser}
	seen := make(map[string]bool)
	for _, kind := range kinds {
		id, err := g.NewID(kind, idSourceNow)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckID(kind, id); err != nil {
			t.Error(err)
		}
		if !strings.HasPrefix(id, idPrefixes[kind]) {
			t.Errorf("NewID(%s) = %q, want the prefix %q", kind, id, idPrefixes[kind])
		}
		// Two kinds from one source never collide.
		ulid := strings.TrimPrefix(id, idPrefixes[kind])
		if seen[ulid] {
			t.Errorf("NewID(%s) = %q, whose ULID was made before", kind, id)
		}
		seen[ulid] = true
	}
}

func TestIDSourceCountsUp(t *testing.T) {
	g := NewIDSource(io.MultiReader(bytes.NewReader([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xfe}), repeatByte(0)))
	var got []*big.Int
	for i := range 3 {
		u, err := g.Next(idSourceNow)
		if err != nil {
			t.Fatal(err)
		}
		if want := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xfe}; i == 0 && !bytes.Equal(u[6:], want) {
			t.Errorf("the random bits of the first ULID = %x, want %x", u[6:], want)
		}
		got = append(got, new(big.Int).SetBytes(u[:]))
	}
	for i := 1; i < len(got); i++ {
		if d := new(big.Int).Sub(got[i], got[i-1]); d.Cmp(big.NewInt(1)) != 0 {
			t.Errorf("ULID %d minus ULID %d = %v, want 1", i, i-1, d)
		}
	}

	// A carry goes through the bytes of the random part and stops there: the time does not change.
	g = NewIDSource(io.MultiReader(bytes.NewReader([]byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}), repeatByte(0)))
	first, err := g.Next(idSourceNow)
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.Next(idSourceNow)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0}; !bytes.Equal(second[6:], want) {
		t.Errorf("the random bits after 00ffffffffffffffffff = %x, want %x", second[6:], want)
	}
	if ulidMillis(first) != idSourceNow.UnixMilli() || ulidMillis(second) != idSourceNow.UnixMilli() {
		t.Errorf("the times after a carry = %d and %d, want %d", ulidMillis(first), ulidMillis(second), idSourceNow.UnixMilli())
	}
}

func TestIDSourceErrors(t *testing.T) {
	const allOnes = "cannot make an ID: the 80 random bits of the last one are all ones"
	g := NewIDSource(repeatByte(0xff))
	if _, err := g.Next(idSourceNow); err != nil {
		t.Fatal(err)
	}
	for range 2 { // the failure leaves the last ULID as it was
		if _, err := g.NewID(RefTerm, idSourceNow); err == nil || err.Error() != allOnes {
			t.Errorf("NewID() in the same millisecond as a ULID of all ones = %v, want %q", err, allOnes)
		}
	}
	// The clock going back does not help; a later millisecond has new random bits.
	if _, err := g.Next(idSourceNow.Add(-time.Hour)); err == nil || err.Error() != allOnes {
		t.Errorf("Next() with the clock back = %v, want %q", err, allOnes)
	}
	u, err := g.Next(idSourceNow.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if ulidMillis(u) != idSourceNow.UnixMilli()+1 {
		t.Errorf("the time of the ULID in the next millisecond = %d, want %d", ulidMillis(u), idSourceNow.UnixMilli()+1)
	}

	for _, tt := range []struct {
		name string
		now  time.Time
		want string
	}{
		{"before 1970", time.UnixMilli(-1).UTC(), "the time 1969-12-31T23:59:59Z is out of range for an ID (1970 to the year 10889)"},
		{"after the year 10889", time.UnixMilli(1 << 48).UTC(), "the time 10889-08-02T05:31:50Z is out of range for an ID (1970 to the year 10889)"},
	} {
		g := NewIDSource(mathrand.NewChaCha8(idSourceSeed))
		if id, err := g.NewID(RefTerm, tt.now); err == nil || err.Error() != tt.want {
			t.Errorf("NewID() %s = %q, %v, want the error %q", tt.name, id, err, tt.want)
		}
		if _, err := g.Next(tt.now); err == nil {
			t.Errorf("Next() %s did not fail", tt.name)
		}
		// The failure took no random bits and left the source usable.
		if _, err := g.Next(idSourceNow); err != nil {
			t.Errorf("Next() after a time %s = %v", tt.name, err)
		}
	}
	// The limits of the range are valid.
	for _, now := range []time.Time{time.UnixMilli(0), time.UnixMilli(1<<48 - 1)} {
		g := NewIDSource(mathrand.NewChaCha8(idSourceSeed))
		u, err := g.Next(now)
		if err != nil {
			t.Errorf("Next(%s) = %v", now.UTC().Format(time.RFC3339Nano), err)
			continue
		}
		if ulidMillis(u) != now.UnixMilli() {
			t.Errorf("the time of the ULID for %d = %d", now.UnixMilli(), ulidMillis(u))
		}
		if err := CheckID(RefTerm, FormatID(RefTerm, u)); err != nil {
			t.Error(err)
		}
	}

	g = NewIDSource(bytes.NewReader([]byte{1, 2, 3}))
	if id, err := g.NewID(RefTerm, idSourceNow); err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("NewID() with a source of 3 bytes = %q, %v, want an error that wraps io.ErrUnexpectedEOF", id, err)
	}
	// A failure to read leaves nothing behind: the next ULID is made as the first one of the source.
	g = NewIDSource(&failFirstRead{})
	if _, err := g.Next(idSourceNow); err == nil || err.Error() != "no random bits" {
		t.Fatalf("Next() with a source that fails = %v, want its error", err)
	}
	u, err = g.Next(idSourceNow)
	if err != nil {
		t.Fatalf("Next() after a failed read = %v", err)
	}
	if want := bytes.Repeat([]byte{7}, 10); !bytes.Equal(u[6:], want) || ulidMillis(u) != idSourceNow.UnixMilli() {
		t.Errorf("the ULID after a failed read = %x, want the time %d and the random bits %x", u, idSourceNow.UnixMilli(), want)
	}
}

// failFirstRead fails its first read and then reads the byte 7 for ever.
type failFirstRead struct{ failed bool }

func (r *failFirstRead) Read(p []byte) (int, error) {
	if !r.failed {
		r.failed = true
		return 0, errors.New("no random bits")
	}
	return repeatByte(7).Read(p)
}

func TestIDSourceConcurrent(t *testing.T) {
	g := NewIDSource(rand.Reader)
	const goroutines, each = 8, 200
	ids := make([][]string, goroutines)
	var wg sync.WaitGroup
	for n := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				id, err := g.NewID(RefRedirect, idSourceNow) // one millisecond for all of them
				if err != nil {
					t.Error(err)
					return
				}
				ids[n] = append(ids[n], id)
			}
		}()
	}
	wg.Wait()
	seen := make(map[string]bool)
	for n, list := range ids {
		if !slices.IsSorted(list) {
			t.Errorf("the IDs of goroutine %d are not in the order in which it made them", n)
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
