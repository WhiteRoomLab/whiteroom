package content

import (
	"encoding/binary"
	"math/rand/v2"
	"strings"
	"testing"
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
	r := rand.New(rand.NewPCG(1, 2))
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
	r := rand.New(rand.NewPCG(3, 4))
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
