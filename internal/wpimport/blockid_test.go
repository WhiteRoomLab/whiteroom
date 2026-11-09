package wpimport

import (
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func nextIDs(g *blockIDs, n int) []string {
	var ids []string
	for range n {
		ids = append(ids, g.next())
	}
	return ids
}

func TestBlockIDs(t *testing.T) {
	// The IDs are made like this, in Python, and change with idVersion:
	//
	//	data = b"".join(s.encode() + b"\0" for s in (id_version, "block", key, str(n)))
	//	v = int.from_bytes(hashlib.sha256(data).digest()[:8], "big") % 36**8
	//
	// and the ID is "b_" and the 8 digits of v in base 36 (0-9a-z), with zeros in front.
	for _, tt := range []struct {
		key  string
		want []string
	}{
		{"rev_0000000000ABCDEFGHJKMNPQRS", []string{"b_podxtowb", "b_kn2kl76x", "b_64udulvr"}},
		{"post 101", []string{"b_639s7hz9", "b_s6ywpf7f", "b_74hudkmv"}},
		{"", []string{"b_f5a3ypwe", "b_kctvdmlr", "b_cn7wilf2"}},
	} {
		if got := nextIDs(newBlockIDs(tt.key), 3); !slices.Equal(got, tt.want) {
			t.Errorf("the first IDs of %q are %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestBlockIDsSameAndDifferent(t *testing.T) {
	a := nextIDs(newBlockIDs("rev_1"), 50)
	if b := nextIDs(newBlockIDs("rev_1"), 50); !slices.Equal(a, b) {
		t.Errorf("the same key gave %q and %q", a, b)
	}
	other := nextIDs(newBlockIDs("rev_2"), 50)
	for i := range a {
		if a[i] == other[i] {
			t.Errorf("the keys rev_1 and rev_2 give the same ID %s at %d", a[i], i)
		}
	}
}

func TestBlockIDsAreValidAndDifferent(t *testing.T) {
	g := newBlockIDs("rev_many")
	seen := make(map[string]int)
	for i := range 20000 {
		id := g.next()
		if err := content.CheckBlockID(id); err != nil {
			t.Fatalf("ID %d: %v", i, err)
		}
		if first, ok := seen[id]; ok {
			t.Fatalf("ID %s is made at %d and at %d", id, first, i)
		}
		seen[id] = i
	}
}

func TestBlockIDsSkipUsed(t *testing.T) {
	want := nextIDs(newBlockIDs("k"), 3)
	g := newBlockIDs("k")
	g.used[want[0]] = true
	// The first is taken, so the next one is made: as if the first had not been asked for.
	if got := nextIDs(g, 2); !slices.Equal(got, want[1:]) {
		t.Errorf("with the first ID used, the IDs are %q, want %q", got, want[1:])
	}
}
