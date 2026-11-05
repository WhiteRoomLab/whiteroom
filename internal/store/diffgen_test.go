package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The generator of the differential tests (differential_test.go) makes seeded random Updates of valid and invalid
// operations on small pools of names, and the reads of everything in the pools. What it makes depends on its seed
// alone, never on what the operations return, so that every backend runs the same operations, and their results can be
// compared line by line.

// diffKind is a kind of documents of the generator, with the IDs of its documents and the key of a string that they
// may have. An unsupported kind has no IDs.
type diffKind struct {
	kind  content.RefKind
	ids   []string
	field string
}

var (
	diffKinds = []diffKind{
		{content.RefEntry, []string{entryID, pageID, "ent_01JE0000000000000000000000"}, "type"},
		{content.RefTerm, []string{termID, "trm_01JE0000000000000000000000"}, "name"},
		{content.RefMedia, []string{mediaID, "med_01JE0000000000000000000000"}, "fileName"},
		{content.RefAuthor, []string{authorID, "aut_01JE0000000000000000000000"}, "name"},
		{content.RefMenu, []string{menuID, "mnu_01JE0000000000000000000000"}, "title"},
		{content.RefForm, []string{formID, "frm_01JE0000000000000000000000"}, "title"},
		{content.RefRedirect, []string{redirectID, "rdr_01JE0000000000000000000000"}, "from"},
		{SettingsKind, []string{""}, "url"},
	}
	diffUnsupported = []diffKind{
		{kind: "page", field: "type"}, {kind: content.RefRevision, field: "type"},
		{kind: content.RefUser, field: "type"}, {kind: "", field: "type"}, {kind: "entries", field: "type"},
	}

	diffUsers            = []string{userID, otherUser}
	diffInvalidUsers     = []string{"usr_x", entryID, ""}
	diffRevisions        = revisionIDs(32)
	diffInvalidRevisions = []string{"rev_x", entryID, "rev_/../../site/settings", ""}
	diffInvalidEntries   = []string{termID, "ent_x", ""}

	diffValidDocs, diffInvalidDocs = diffDocs()
)

// revisionIDs returns n IDs of revisions, which AddRevision can use once each.
func revisionIDs(n int) []string {
	ids := []string{revisionID}
	for i := 1; i < n; i++ {
		ids = append(ids, fmt.Sprintf("rev_01JE%022d", i))
	}
	return ids
}

// diffDoc names a document by a kind and an ID, either of which may be invalid.
type diffDoc struct {
	k  diffKind
	id string
}

// diffDocs returns every document of diffKinds, and invalid names: invalid IDs of each kind, and unsupported kinds.
func diffDocs() (valid, invalid []diffDoc) {
	for i, k := range diffKinds {
		for _, id := range k.ids {
			valid = append(valid, diffDoc{k, id})
		}
		other := diffKinds[(i+1)%(len(diffKinds)-1)].ids[0]
		for _, id := range invalidIDs(k, other) {
			invalid = append(invalid, diffDoc{k, id})
		}
	}
	for _, k := range diffUnsupported {
		invalid = append(invalid, diffDoc{k, entryID})
	}
	return valid, invalid
}

// invalidIDs returns IDs that no document of k has: other, the ID of a document of another kind, and IDs with the
// prefix of k that are short, a path, empty, in lower case, or above the largest ULID.
func invalidIDs(k diffKind, other string) []string {
	if k.kind == SettingsKind {
		return []string{other, "x"}
	}
	prefix, ulid := k.ids[0][:4], k.ids[0][4:]
	return []string{other, prefix + "x", prefix + "/../../site/settings", "", prefix + strings.ToLower(ulid),
		prefix + "8" + ulid[1:]}
}

// longText makes the data of the long variants larger than the small segments of the log backend in some runs.
var longText = strings.Repeat("x", 3000)

// diffLarge returns a buffer of content.MaxDocumentBytes+1 zero bytes, which every run shares: the data of the variant
// "too large", and without its last byte that of "largest".
var diffLarge = sync.OnceValue(func() []byte { return make([]byte, content.MaxDocumentBytes+1) })

// diffLargeHashes returns the hashes of the data of "largest" and "too large".
var diffLargeHashes = sync.OnceValues(func() (largest, tooLarge string) {
	b := diffLarge()
	return hashOf(b[:content.MaxDocumentBytes]), hashOf(b)
})

// withField returns the JSON of the document of k with id that has the string v at the key k.field.
func withField(k diffKind, id, v string) []byte {
	if k.kind == SettingsKind {
		return []byte(`{"schemaVersion": 1, "` + k.field + `": "` + v + `"}`)
	}
	return []byte(`{"schemaVersion": 1, "id": "` + id + `", "` + k.field + `": "` + v + `"}`)
}

// diffDocData are the data that Put gets for a document, by name, with their weights, and whether they are valid.
// "without id" is valid for every kind, and the same data for all.
var diffDocData = []struct {
	name   string
	weight int
	valid  bool
	data   func(k diffKind, id string) []byte
}{
	{"minimal", 24, true, func(k diffKind, id string) []byte { return docJSON(k.kind, id) }},
	{"with a newline", 10, true, func(k diffKind, id string) []byte { return append(docJSON(k.kind, id), '\n') }},
	{"field A", 12, true, func(k diffKind, id string) []byte { return withField(k, id, "A") }},
	{"field B", 10, true, func(k diffKind, id string) []byte { return withField(k, id, "B") }},
	{"without id", 8, true, func(diffKind, string) []byte { return []byte(`{"schemaVersion": 1}`) }},
	{"long", 4, true, func(k diffKind, id string) []byte { return withField(k, id, longText) }},
	{"invalid JSON", 5, false, func(diffKind, string) []byte { return []byte(`{"schemaVersion": 1,}`) }},
	{"wrong id", 5, false, func(k diffKind, id string) []byte {
		if len(k.ids) < 2 {
			return docJSON(content.RefEntry, entryID)
		}
		other := k.ids[0]
		if other == id {
			other = k.ids[1]
		}
		return docJSON(k.kind, other)
	}},
	{"key of another kind", 5, false, func(k diffKind, id string) []byte {
		if k.kind == SettingsKind {
			return []byte(`{"schemaVersion": 1, "types": {}}`)
		}
		key := `"taxonomy": "category"`
		if k.kind == content.RefTerm {
			key = `"status": "draft"`
		}
		return []byte(`{"schemaVersion": 1, "id": "` + id + `", ` + key + `}`)
	}},
	{"document of another kind", 3, false, func(k diffKind, _ string) []byte {
		other := diffKinds[0]
		if k.kind == content.RefEntry {
			other = diffKinds[1]
		}
		return withField(other, other.ids[0], "A")
	}},
	{"unknown key", 4, false, func(diffKind, string) []byte { return []byte(`{"schemaVersion": 1, "foo": 2}`) }},
	{"empty", 3, false, func(diffKind, string) []byte { return nil }},
	{"too large", 1, false, func(diffKind, string) []byte { return diffLarge() }},
	{"largest", 1, false, func(diffKind, string) []byte { return diffLarge()[:content.MaxDocumentBytes] }},
}

// diffRevisionData are the data that AddRevision gets for the revision id, as diffDocData.
var diffRevisionData = []struct {
	name   string
	weight int
	valid  bool
	data   func(id string) []byte
}{
	{"First", 20, true, func(string) []byte { return revJSON("First") }},
	{"Second", 14, true, func(string) []byte { return revJSON("Second") }},
	{"Third", 10, true, func(string) []byte { return revJSON("Third") }},
	{"without title", 8, true, func(string) []byte { return []byte(`{"schemaVersion": 1}`) }},
	{"long", 4, true, func(string) []byte { return revJSON(longText) }},
	{"invalid JSON", 5, false, func(string) []byte { return []byte(`{"schemaVersion": 1,}`) }},
	{"id in the revision", 5, false, func(id string) []byte { return []byte(`{"schemaVersion": 1, "id": "` + id + `"}`) }},
	{"empty", 3, false, func(string) []byte { return nil }},
	{"too large", 1, false, func(string) []byte { return diffLarge() }},
	{"largest", 1, false, func(string) []byte { return diffLarge()[:content.MaxDocumentBytes] }},
}

// diffOp is an operation of an Update: desc names the call in the lines, and run makes it.
type diffOp struct {
	desc string
	run  func(tx *Tx) (any, error)
}

// diffEnd is how the function of an Update ends.
type diffEnd int

const (
	endCommit     diffEnd = iota // it returns nil
	endAbort                     // it returns errAbort
	endFirstError                // it stops at the first operation that fails, and returns its error
)

// diffUpdate is an Update of the generator.
type diffUpdate struct {
	ops []diffOp
	end diffEnd
}

// diffGen makes the operations of a differential run from its seed.
type diffGen struct {
	rng     *rand.Rand
	seen    []string        // the hashes of the valid data that the operations have got, in the order of their first use
	invalid []string        // and of the invalid data, which may be valid elsewhere: in seen too then
	known   map[string]bool // whether a hash of seen or invalid is in seen
}

func newDiffGen(seed uint64) *diffGen {
	return &diffGen{rng: rand.New(rand.NewPCG(seed, 0x5745_4c4f_475f_4446)), known: map[string]bool{}}
}

// chance returns true with the probability p.
func (g *diffGen) chance(p float64) bool {
	return g.rng.Float64() < p
}

// pick returns an element of s at random.
func pick[T any](g *diffGen, s []T) T {
	return s[g.rng.IntN(len(s))]
}

// weighted returns the index of an element of n with the weights of weight.
func weighted(g *diffGen, n int, weight func(i int) int) int {
	total := 0
	for i := range n {
		total += weight(i)
	}
	r := g.rng.IntN(total)
	for i := range n {
		if r -= weight(i); r < 0 {
			return i
		}
	}
	panic("unreachable")
}

// see adds the hash of data, which an operation gets and which is valid there if valid is true, to the hashes that
// the reads of Object take.
func (g *diffGen) see(data []byte, valid bool) {
	h := dataHash(data)
	switch inSeen, ok := g.known[h]; {
	case !ok && !valid:
		g.invalid = append(g.invalid, h)
	case valid && !inSeen:
		g.seen = append(g.seen, h)
	default:
		return
	}
	g.known[h] = valid
}

// dataHash is hashOf(data), which it makes once for the large data of the generator.
func dataHash(data []byte) string {
	if len(data) < content.MaxDocumentBytes {
		return hashOf(data)
	}
	largest, tooLarge := diffLargeHashes()
	if len(data) == content.MaxDocumentBytes {
		return largest
	}
	return tooLarge
}

// doc returns the name of a document: mostly a valid one, else an invalid ID of a kind or an unsupported kind.
func (g *diffGen) doc() diffDoc {
	if g.chance(0.9) {
		return pick(g, diffValidDocs)
	}
	return pick(g, diffInvalidDocs)
}

func (g *diffGen) kind() content.RefKind {
	if g.chance(0.9) {
		return pick(g, diffKinds).kind
	}
	return pick(g, diffUnsupported).kind
}

func (g *diffGen) user() string {
	if g.chance(0.92) {
		return pick(g, diffUsers)
	}
	return pick(g, diffInvalidUsers)
}

func (g *diffGen) revision() string {
	if g.chance(0.9) {
		return pick(g, diffRevisions)
	}
	return pick(g, diffInvalidRevisions)
}

func (g *diffGen) entry() string {
	if g.chance(0.9) {
		return pick(g, diffKinds[0].ids)
	}
	return pick(g, diffInvalidEntries)
}

// hash returns a hash for Object: mostly one of data that the operations have got, else that of no data, or an invalid
// one.
func (g *diffGen) hash() string {
	switch r := g.rng.IntN(100); {
	case r < 70 && len(g.seen)+len(g.invalid) > 0:
		i := g.rng.IntN(len(g.seen) + len(g.invalid))
		if i < len(g.seen) {
			return g.seen[i]
		}
		return g.invalid[i-len(g.seen)]
	case r < 85:
		return diffMissingHash
	}
	return pick(g, diffInvalidHashes)
}

// diffMissingHash is the hash of data that no operation gets; diffInvalidHashes are not hashes that Object takes.
var (
	diffMissingHash   = hashOf([]byte("missing"))
	diffInvalidHashes = []string{
		"sha256:" + strings.ToUpper(diffMissingHash[7:]), diffMissingHash[7:], diffMissingHash[:70],
		"sha512:" + diffMissingHash[7:], "",
	}
)

// describe names data in the lines.
func describe(name string, data []byte) string {
	return fmt.Sprintf("%s (%d bytes)", name, len(data))
}

// diffOps are the operations of the Updates, with their weights.
var diffOps = []struct {
	weight int
	make   func(g *diffGen) diffOp
}{
	{28, (*diffGen).put},
	{12, (*diffGen).delete},
	{16, (*diffGen).addRevision},
	{9, (*diffGen).document},
	{7, (*diffGen).documents},
	{7, (*diffGen).history},
	{9, (*diffGen).object},
	{6, (*diffGen).getRevision},
	{6, (*diffGen).revisions},
}

// update returns an Update of 1 to 6 operations, a quarter of which end with errAbort, or rarely a bulk one.
func (g *diffGen) update() diffUpdate {
	if g.chance(0.02) {
		return g.bulk()
	}
	var u diffUpdate
	for range 1 + g.rng.IntN(6) {
		u.ops = append(u.ops, diffOps[weighted(g, len(diffOps), func(i int) int { return diffOps[i].weight })].make(g))
	}
	switch r := g.rng.IntN(100); {
	case r < 25:
		u.end = endAbort
	case r < 35:
		u.end = endFirstError
	}
	return u
}

// bulk returns an Update of 64 to 256 Puts of valid data on an entry and another document, most of which save a
// version, so that the seqs go past 255 and the record is larger than the small segments of the log backend, with a
// revision of the entry after every eighth.
func (g *diffGen) bulk() diffUpdate {
	docs := []diffDoc{{diffKinds[0], pick(g, diffKinds[0].ids)}, pick(g, diffValidDocs)}
	var u diffUpdate
	for i := range 64 + g.rng.IntN(193) {
		d := docs[i%2]
		// minimal, with a newline, field A and field B in turn: each differs from the one before.
		v := diffDocData[i/2%4]
		u.ops = append(u.ops, g.putOp(d, v.name, v.valid, v.data(d.k, d.id), pick(g, diffUsers)))
		if i%8 == 7 {
			r := diffRevisionData[g.rng.IntN(5)] // valid
			id := pick(g, diffRevisions)
			u.ops = append(u.ops, g.addRevisionOp(docs[0].id, id, r.name, r.valid, r.data(id), pick(g, diffUsers)))
		}
	}
	if g.chance(0.25) {
		u.end = endAbort
	}
	return u
}

func (g *diffGen) put() diffOp {
	d, by := g.doc(), g.user()
	v := diffDocData[weighted(g, len(diffDocData), func(i int) int { return diffDocData[i].weight })]
	return g.putOp(d, v.name, v.valid, v.data(d.k, d.id), by)
}

func (g *diffGen) putOp(d diffDoc, name string, valid bool, data []byte, by string) diffOp {
	g.see(data, valid)
	return diffOp{
		desc: fmt.Sprintf("Put(%q, %q, %s, %q)", d.k.kind, d.id, describe(name, data), by),
		run:  func(tx *Tx) (any, error) { return tx.Put(d.k.kind, d.id, data, by) },
	}
}

func (g *diffGen) delete() diffOp {
	d, by := g.doc(), g.user()
	return diffOp{
		desc: fmt.Sprintf("Delete(%q, %q, %q)", d.k.kind, d.id, by),
		run:  func(tx *Tx) (any, error) { return tx.Delete(d.k.kind, d.id, by) },
	}
}

func (g *diffGen) addRevision() diffOp {
	entry, id, by := g.entry(), g.revision(), g.user()
	v := diffRevisionData[weighted(g, len(diffRevisionData), func(i int) int { return diffRevisionData[i].weight })]
	return g.addRevisionOp(entry, id, v.name, v.valid, v.data(id), by)
}

func (g *diffGen) addRevisionOp(entry, id, name string, valid bool, data []byte, by string) diffOp {
	g.see(data, valid)
	return diffOp{
		desc: fmt.Sprintf("AddRevision(%q, %q, %s, %q)", entry, id, describe(name, data), by),
		run:  func(tx *Tx) (any, error) { return tx.AddRevision(entry, id, data, by) },
	}
}

func (g *diffGen) document() diffOp {
	d := g.doc()
	return diffOp{
		desc: fmt.Sprintf("Document(%q, %q)", d.k.kind, d.id),
		run:  func(tx *Tx) (any, error) { return tx.Document(d.k.kind, d.id) },
	}
}

func (g *diffGen) documents() diffOp {
	kind := g.kind()
	return diffOp{
		desc: fmt.Sprintf("Documents(%q)", kind),
		run:  func(tx *Tx) (any, error) { return tx.Documents(kind) },
	}
}

func (g *diffGen) history() diffOp {
	d := g.doc()
	return diffOp{
		desc: fmt.Sprintf("History(%q, %q)", d.k.kind, d.id),
		run:  func(tx *Tx) (any, error) { return tx.History(d.k.kind, d.id) },
	}
}

func (g *diffGen) object() diffOp {
	h := g.hash()
	return diffOp{
		desc: fmt.Sprintf("Object(%q)", h),
		run:  func(tx *Tx) (any, error) { return tx.Object(h) },
	}
}

func (g *diffGen) getRevision() diffOp {
	id := g.revision()
	return diffOp{
		desc: fmt.Sprintf("Revision(%q)", id),
		run:  func(tx *Tx) (any, error) { return tx.Revision(id) },
	}
}

func (g *diffGen) revisions() diffOp {
	entry := g.entry()
	return diffOp{
		desc: fmt.Sprintf("Revisions(%q)", entry),
		run:  func(tx *Tx) (any, error) { return tx.Revisions(entry) },
	}
}

// diffReads are the reads of a dump of a store: Documents of each kind, Document and History of each document, Revision
// of each revision, Revisions of each entry and Object of each hash.
type diffReads struct {
	kinds   []content.RefKind
	docs    []diffDoc
	revs    []string
	entries []string
	hashes  []string
}

// reads returns the reads of everything that a store can have of what the operations of g name, and if invalid is
// true, of the invalid names and data too: the store refuses the names before it reads anything, and has none of
// the data.
func (g *diffGen) reads(invalid bool) diffReads {
	r := diffReads{
		docs:    diffValidDocs,
		revs:    diffRevisions,
		entries: diffKinds[0].ids,
		hashes:  append(slices.Clone(g.seen), diffMissingHash),
	}
	kinds := diffKinds
	if invalid {
		r.docs = slices.Concat(diffValidDocs, diffInvalidDocs)
		r.revs = slices.Concat(diffRevisions, diffInvalidRevisions)
		r.entries = slices.Concat(diffKinds[0].ids, diffInvalidEntries)
		for _, h := range g.invalid {
			if !g.known[h] {
				r.hashes = append(r.hashes, h)
			}
		}
		r.hashes = append(r.hashes, diffInvalidHashes...)
		kinds = slices.Concat(diffKinds, diffUnsupported)
	}
	for _, k := range kinds {
		r.kinds = append(r.kinds, k.kind)
	}
	return r
}
