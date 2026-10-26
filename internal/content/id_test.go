package content

import "testing"

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
			err := checkID(tt.kind, tt.in)
			if tt.ok {
				if err != nil {
					t.Errorf("checkID(%q, %q) = %v, want nil", tt.kind, tt.in, err)
				}
				return
			}
			want := `invalid ` + string(tt.kind) + ` ID "` + tt.in + `": use "` + idPrefixes[tt.kind] + `" and a ULID (26 characters in upper case)`
			if err == nil || err.Error() != want {
				t.Errorf("checkID(%q, %q) = %v, want %s", tt.kind, tt.in, err, want)
			}
		})
	}
}

func TestIDPrefixes(t *testing.T) {
	kinds := []RefKind{RefEntry, RefMedia, RefForm, RefTerm, RefAuthor, RefRevision, RefTranslationGroup, RefUser}
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
