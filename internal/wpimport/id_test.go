package wpimport

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const testSeed = "https://example.com"

var allKinds = []content.RefKind{
	content.RefEntry, content.RefMedia, content.RefForm, content.RefTerm, content.RefAuthor,
	content.RefMenu, content.RefRedirect, content.RefRevision, content.RefTranslationGroup, content.RefUser,
}

// TestNewIDGolden checks IDs against values that were worked out once, with SHA-256 and Crockford's Base32 in another
// language, so that how IDs are made does not change without notice. Changing it changes the IDs of every import.
func TestNewIDGolden(t *testing.T) {
	assign := func(seed string, o Object, kind content.RefKind) string {
		t.Helper()
		id, err := NewTable(seed).Assign(o, kind)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	post := PostObject(&wxr.Item{ID: 123})
	for _, tt := range []struct {
		name string
		got  string
		want string
	}{
		{"a post as an entry", assign(testSeed, post, content.RefEntry), "ent_0000000000QNCGZPVAWT8HNPBA"},
		{"a post as a medium", assign(testSeed, post, content.RefMedia), "med_0000000000TB4EDMXTV7FVBCZF"},
		{"a term by its ID", assign(testSeed, TermObject(&wxr.Term{ID: 5, Taxonomy: "category"}), content.RefTerm), "trm_0000000000AAFHG8EJHY38VMP6"},
		{"a term by its slug", assign(testSeed, TermObject(&wxr.Term{Taxonomy: "category", Slug: "news"}), content.RefTerm), "trm_0000000000VMV2X947VC4Z5VCT"},
		{"a user as an author", assign(testSeed, UserObject("admin"), content.RefAuthor), "aut_00000000007N74DS72X2ZMWY71"},
		{"another seed", assign("https://example.org", post, content.RefEntry), "ent_0000000000XA82DA497T60KFCS"},
		{"a revision", NewTable(testSeed).NewID(content.RefRevision, "post\x00123"), "rev_000000000000M72QAFCP2MHQV8"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s: ID = %s, want %s", tt.name, tt.got, tt.want)
		}
	}
}

func TestIDs(t *testing.T) {
	var objects []Object
	for i := int64(1); i <= 20; i++ {
		objects = append(objects, Object{Kind: ObjectPost, ID: i})
		objects = append(objects, Object{Kind: ObjectTerm, Taxonomy: "category", ID: i})
		objects = append(objects, Object{Kind: ObjectTerm, Taxonomy: "post_tag", ID: i})
		objects = append(objects, Object{Kind: ObjectTerm, Taxonomy: "category", Slug: fmt.Sprintf("slug-%d", i)})
		objects = append(objects, Object{Kind: ObjectUser, Login: fmt.Sprintf("user%d", i)})
	}
	seen := make(map[string]string)
	for _, seed := range []string{"", testSeed, "https://example.org", "https://example.com/blog"} {
		for _, kind := range allKinds {
			first, second := NewTable(seed), NewTable(seed)
			for _, o := range objects {
				id, err := first.Assign(o, kind)
				if err != nil {
					t.Fatal(err)
				}
				again, err := second.Assign(o, kind)
				if err != nil || again != id {
					t.Fatalf("Assign(%v, %s) in two tables = %s, %v and %s", o, kind, id, err, again)
				}
				if err := content.CheckID(kind, id); err != nil {
					t.Fatal(err)
				}
				prefix := id[:strings.IndexByte(id, '_')+1]
				if timePart := id[len(prefix) : len(prefix)+10]; timePart != "0000000000" {
					t.Fatalf("the time of %s is %s, want 0000000000", id, timePart)
				}
				who := fmt.Sprintf("%q %s %v", seed, kind, o)
				if other, ok := seen[id]; ok {
					t.Fatalf("%s is the ID of %s and of %s", id, other, who)
				}
				seen[id] = who
				if got := first.NewID(kind, o.key()); got != id {
					t.Fatalf("NewID(%s, the key of %v) = %s, want the ID of Assign, %s", kind, o, got, id)
				}
			}
		}
	}
	if want := 4 * len(allKinds) * len(objects); len(seen) != want {
		t.Errorf("%d different IDs, want %d", len(seen), want)
	}
}

func TestObjects(t *testing.T) {
	for _, tt := range []struct {
		name string
		got  Object
		want Object
		str  string
	}{
		{"post", PostObject(&wxr.Item{ID: 45, Type: "page"}), Object{Kind: ObjectPost, ID: 45}, "post 45"},
		{"term", TermObject(&wxr.Term{ID: 12, Taxonomy: "category", Slug: "news"}), Object{Kind: ObjectTerm, Taxonomy: "category", ID: 12}, "term 12 (category)"},
		{"term without an ID", TermObject(&wxr.Term{Taxonomy: "post_tag", Slug: "%e6%97%a5%e8%a8%98"}), Object{Kind: ObjectTerm, Taxonomy: "post_tag", Slug: "%e6%97%a5%e8%a8%98"}, `term "%e6%97%a5%e8%a8%98" (post_tag)`},
		{"user", UserObject("admin"), Object{Kind: ObjectUser, Login: "admin"}, `user "admin"`},
		{"unknown", Object{Kind: "menu", ID: 1}, Object{Kind: "menu", ID: 1}, "{Kind:menu ID:1 Taxonomy: Slug: Login:}"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s: Object = %+v, want %+v", tt.name, tt.got, tt.want)
		}
		if got := tt.got.String(); got != tt.str {
			t.Errorf("%s: String() = %q, want %q", tt.name, got, tt.str)
		}
	}
	// A term that has an ID is not the term that has its slug.
	if TermObject(&wxr.Term{ID: 1, Taxonomy: "category"}) == TermObject(&wxr.Term{Taxonomy: "category", Slug: "a"}) {
		t.Error("terms by ID and by slug are the same object")
	}
}

func TestTable(t *testing.T) {
	tb := NewTable(testSeed)
	post, media := Object{Kind: ObjectPost, ID: 12}, Object{Kind: ObjectPost, ID: 13}
	term := Object{Kind: ObjectTerm, Taxonomy: "category", ID: 3}

	if kind, id, ok := tb.Lookup(post); ok || kind != "" || id != "" {
		t.Errorf("Lookup before Assign = %q, %q, %v, want none", kind, id, ok)
	}
	id, err := tb.Assign(post, content.RefEntry)
	if err != nil {
		t.Fatal(err)
	}
	if kind, got, ok := tb.Lookup(post); !ok || kind != content.RefEntry || got != id {
		t.Errorf("Lookup = %q, %q, %v, want entry, %s, true", kind, got, ok, id)
	}
	if again, err := tb.Assign(post, content.RefEntry); err != nil || again != id {
		t.Errorf("Assign again = %q, %v, want %s", again, err, id)
	}
	if _, err := tb.Assign(post, content.RefMedia); err == nil || err.Error() != "post 12 is already imported as entry" {
		t.Errorf("Assign as another kind = %v, want post 12 is already imported as entry", err)
	}
	if kind, got, ok := tb.Lookup(post); !ok || kind != content.RefEntry || got != id {
		t.Errorf("Lookup after the error = %q, %q, %v, want entry, %s, true", kind, got, ok, id)
	}

	// The others are not touched.
	mediaID, err := tb.Assign(media, content.RefMedia)
	if err != nil {
		t.Fatal(err)
	}
	termID, err := tb.Assign(term, content.RefTerm)
	if err != nil {
		t.Fatal(err)
	}
	tb.Drop(post)
	if kind, got, ok := tb.Lookup(post); ok || kind != "" || got != "" {
		t.Errorf("Lookup after Drop = %q, %q, %v, want none", kind, got, ok)
	}
	if kind, got, ok := tb.Lookup(media); !ok || kind != content.RefMedia || got != mediaID {
		t.Errorf("Lookup of the other after Drop = %q, %q, %v", kind, got, ok)
	}
	if _, err := tb.Assign(post, content.RefMedia); err == nil || err.Error() != "post 12 is already imported as entry" {
		t.Errorf("Assign as another kind after Drop = %v, want post 12 is already imported as entry", err)
	}
	if again, err := tb.Assign(post, content.RefEntry); err != nil || again != id {
		t.Errorf("Assign after Drop = %q, %v, want %s", again, err, id)
	}
	if kind, got, ok := tb.Lookup(post); !ok || kind != content.RefEntry || got != id {
		t.Errorf("Lookup after Assign again = %q, %q, %v, want entry, %s, true", kind, got, ok, id)
	}

	// Dropping what was not assigned, or dropping twice, does nothing.
	tb.Drop(Object{Kind: ObjectPost, ID: 999})
	tb.Drop(term)
	tb.Drop(term)
	if _, _, ok := tb.Lookup(term); ok {
		t.Error("Lookup of a term after Drop is true")
	}
	if got, err := tb.Assign(term, content.RefTerm); err != nil || got != termID {
		t.Errorf("Assign of a term after Drop = %q, %v, want %s", got, err, termID)
	}

	var lookup Lookup = tb.Lookup
	if kind, got, ok := lookup(media); !ok || kind != content.RefMedia || got != mediaID {
		t.Errorf("Lookup as a Lookup = %q, %q, %v", kind, got, ok)
	}
}

func TestTableInvalidObjects(t *testing.T) {
	for _, tt := range []struct {
		name string
		o    Object
		want string
	}{
		{"a post without an ID", Object{Kind: ObjectPost}, "invalid object post 0: a post needs an ID of 1 or more"},
		{"a post with a negative ID", Object{Kind: ObjectPost, ID: -1}, "invalid object post -1: a post needs an ID of 1 or more"},
		{"a post with a login", Object{Kind: ObjectPost, ID: 1, Login: "a"}, "invalid object post 1: a post has only an ID"},
		{"a post with a taxonomy", Object{Kind: ObjectPost, ID: 1, Taxonomy: "category"}, "invalid object post 1: a post has only an ID"},
		{"a post with a slug", Object{Kind: ObjectPost, ID: 1, Slug: "a"}, "invalid object post 1: a post has only an ID"},
		{"a term without a taxonomy", Object{Kind: ObjectTerm, ID: 5}, "invalid object term 5 (): a term needs a taxonomy"},
		{"a term without an ID or a slug", Object{Kind: ObjectTerm, Taxonomy: "category"}, `invalid object term "" (category): a term needs an ID of 1 or more, or else a slug`},
		{"a term with a negative ID", Object{Kind: ObjectTerm, Taxonomy: "category", ID: -1}, `invalid object term "" (category): a term needs an ID of 1 or more, or else a slug`},
		{"a term with an ID and a slug", Object{Kind: ObjectTerm, Taxonomy: "category", ID: 5, Slug: "a"}, "invalid object term 5 (category): a term has an ID or a slug, not both"},
		{"a term with a login", Object{Kind: ObjectTerm, Taxonomy: "category", ID: 5, Login: "a"}, "invalid object term 5 (category): a term has no login"},
		{"a user without a login", Object{Kind: ObjectUser}, `invalid object user "": a user needs a login`},
		{"a user with an ID", Object{Kind: ObjectUser, Login: "a", ID: 1}, `invalid object user "a": a user has only a login`},
		{"a user with a taxonomy", Object{Kind: ObjectUser, Login: "a", Taxonomy: "category"}, `invalid object user "a": a user has only a login`},
		{"a user with a slug", Object{Kind: ObjectUser, Login: "a", Slug: "a"}, `invalid object user "a": a user has only a login`},
		{"no kind", Object{ID: 1}, "invalid object {Kind: ID:1 Taxonomy: Slug: Login:}: unknown kind"},
		{"another kind", Object{Kind: "menu", ID: 1}, "invalid object {Kind:menu ID:1 Taxonomy: Slug: Login:}: unknown kind"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tb := NewTable(testSeed)
			id, err := tb.Assign(tt.o, content.RefEntry)
			if err == nil || err.Error() != tt.want || id != "" {
				t.Errorf("Assign(%+v) = %q, %v; want the error %s", tt.o, id, err, tt.want)
			}
			if _, _, ok := tb.Lookup(tt.o); ok {
				t.Errorf("Lookup(%+v) is true after the error", tt.o)
			}
		})
	}
}

func TestTableSameID(t *testing.T) {
	tb := NewTable(testSeed)
	a := Object{Kind: ObjectPost, ID: 12}
	id, err := tb.Assign(a, content.RefEntry)
	if err != nil {
		t.Fatal(err)
	}
	// Two objects do not get the same ID in practice, but if they do, it is an error and not a shared ID.
	b := Object{Kind: ObjectUser, Login: "x"}
	tb.owners[tb.NewID(content.RefAuthor, b.key())] = a
	if got, err := tb.Assign(b, content.RefAuthor); err == nil || got != "" || !strings.HasPrefix(err.Error(), `user "x" and post 12 get the same ID aut_`) {
		t.Errorf("Assign = %q, %v; want an error about the same ID", got, err)
	}
	if _, _, ok := tb.Lookup(b); ok {
		t.Error("Lookup is true after the error")
	}
	if kind, got, ok := tb.Lookup(a); !ok || kind != content.RefEntry || got != id {
		t.Errorf("Lookup of the first = %q, %q, %v", kind, got, ok)
	}
}

func TestTableConcurrentLookup(t *testing.T) {
	tb := NewTable(testSeed)
	want := make(map[Object]string)
	for i := int64(1); i <= 200; i++ {
		o := Object{Kind: ObjectPost, ID: i}
		id, err := tb.Assign(o, content.RefEntry)
		if err != nil {
			t.Fatal(err)
		}
		want[o] = id
	}
	tb.Drop(Object{Kind: ObjectPost, ID: 7})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i := int64(1); i <= 201; i++ {
				o := Object{Kind: ObjectPost, ID: i}
				_, id, ok := tb.Lookup(o)
				if wantOK := i <= 200 && i != 7; ok != wantOK || (ok && id != want[o]) {
					t.Errorf("Lookup(%v) = %q, %v", o, id, ok)
					return
				}
				if got := tb.NewID(content.RefRevision, o.key()); !strings.HasPrefix(got, "rev_") {
					t.Errorf("NewID = %q", got)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestSeed(t *testing.T) {
	for _, tt := range []struct {
		name       string
		base, link string
		want       string
	}{
		{name: "the blog address", base: "https://example.com/blog", link: "https://example.com", want: "https://example.com/blog"},
		{name: "no blog address", link: "https://example.com", want: "https://example.com"},
		{name: "white space", base: " \n", link: "\thttps://example.com \n", want: "https://example.com"},
		{name: "white space around the blog address", base: "\n  https://example.com/blog\t", link: "https://example.com", want: "https://example.com/blog"},
		{name: "nothing"},
	} {
		e := &wxr.Export{Channel: wxr.Channel{BaseBlogURL: tt.base, Link: tt.link}}
		if got := Seed(e); got != tt.want {
			t.Errorf("%s: Seed = %q, want %q", tt.name, got, tt.want)
		}
	}
}
