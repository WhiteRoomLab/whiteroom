package store

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The index is derived from the documents, the revisions of the entries and nothing else: the summary of each current
// document, and a reference from each document, and from the published, the scheduled and the last revision of each
// entry, to every ID that its JSON holds; an entry also refers to the revisions that it publishes and schedules. Put,
// Delete and AddRevision update it in their own transaction, and RebuildIndex makes it again with the same result. The
// shared code makes every decision of it; a backend only keeps the rows (backend.go).

// ErrNoIndex is the error of the reads of the index in a store of the table schema 1, which has none: opening it as
// its writer once builds it.
var ErrNoIndex = errors.New(
	"the store has the table schema 1, which has no index; open it as its writer once to build it")

// Summary is what the index keeps of a current document, which does not depend on the schema file or the theme: what
// the checks across the documents, the paths of the pages and the lookups of references read. Kind and ID name the
// document ("" for the settings document), and only the pointer of its kind is set, with the fields that follow of the
// JSON of the document; the other fields are zero:
//   - Entry has every field, ScheduledRevision and ScheduledAt too. Published has Slug, Parent, Date, Author, Sticky
//     and Terms of the published revision, and is set only if the store has the revision that Entry.PublishedRevision
//     names, of any entry as the build reads any revision by its ID, and it decodes. Latest is the ID of the last
//     revision of the entry, "" if it has none;
//   - Term has ID, Taxonomy, Language, TranslationGroup, Slug and Parent;
//   - Media has ID, Hash and MIMEType;
//   - Author has ID and Slug;
//   - Menu has ID, Language and Location;
//   - Form has ID;
//   - Redirect has ID, From, To and Status.
//
// The settings document has a summary without any of the pointers.
type Summary struct {
	Kind content.RefKind
	ID   string

	Entry     *content.Entry
	Published *content.Revision
	Latest    string
	Term      *content.Term
	Media     *content.Media
	Author    *content.AuthorProfile
	Menu      *content.Menu
	Form      *content.Form
	Redirect  *content.Redirect
}

// Role is how a document refers to an ID.
type Role string

const (
	RoleDocument  Role = "document"  // the JSON of the document holds it
	RolePublished Role = "published" // the JSON of the published revision of an entry holds it
	RoleLatest    Role = "latest"    // the JSON of the last revision of an entry holds it
	RoleScheduled Role = "scheduled" // the JSON of the scheduled revision of an entry holds it

	// rolePublishes is that of the revisions that an entry names as its published and its scheduled one, whether the
	// store has them or not, so that AddRevision finds the entries to which the revision gives a published or a scheduled
	// revision. Referrers does not take the ID of a revision.
	rolePublishes Role = "publishes"
)

// Referrer is a current document that refers to an ID, with how.
type Referrer struct {
	Kind content.RefKind
	ID   string
	Role Role
}

// summaryRow is the value of a row of summaries: the JSON of the partial content values of Summary, and the IDs that
// the document refers to, by role, each sorted and without duplicates, which say what rows of references it has.
type summaryRow struct {
	Entry     *content.Entry         `json:"entry,omitzero"`
	Published *content.Revision      `json:"published,omitzero"`
	Latest    string                 `json:"latest,omitempty"`
	Term      *content.Term          `json:"term,omitzero"`
	Media     *content.Media         `json:"media,omitzero"`
	Author    *content.AuthorProfile `json:"author,omitzero"`
	Menu      *content.Menu          `json:"menu,omitzero"`
	Form      *content.Form          `json:"form,omitzero"`
	Redirect  *content.Redirect      `json:"redirect,omitzero"`
	Refs      map[Role][]string      `json:"refs,omitempty"`
}

// indexKinds are the kinds of the documents that have summaries.
var indexKinds = []content.RefKind{
	content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
	content.RefRedirect, SettingsKind,
}

var indexTableNames = [numIndexTables]string{
	indexSummaries: "summaries", indexReferences: "references", indexOperations: "operations", indexAccounts: "accounts",
	indexSessions: "sessions",
}

// readRow decodes value, that of key in summaries, the row of a document of kind.
func readRow(kind content.RefKind, key, value []byte) (summaryRow, error) {
	var r summaryRow
	if err := json.Unmarshal(value, &r, json.RejectUnknownMembers(true)); err != nil || !r.fits(kind) {
		return summaryRow{}, fmt.Errorf("%s %q: invalid value", indexTableNames[indexSummaries], key)
	}
	return r, nil
}

func invalidKey(table indexTable, key []byte) error {
	return fmt.Errorf("%s %q: invalid key", indexTableNames[table], key)
}

// fits reports whether r can be the row of a document of kind: it has the pointer of kind and no other pointer, and
// only an entry has a published revision, a last revision, and the roles published, latest, scheduled and publishes.
func (r *summaryRow) fits(kind content.RefKind) bool {
	for _, p := range []struct {
		kind content.RefKind
		set  bool
	}{
		{content.RefEntry, r.Entry != nil}, {content.RefTerm, r.Term != nil}, {content.RefMedia, r.Media != nil},
		{content.RefAuthor, r.Author != nil}, {content.RefMenu, r.Menu != nil}, {content.RefForm, r.Form != nil},
		{content.RefRedirect, r.Redirect != nil},
	} {
		if p.set != (p.kind == kind) {
			return false
		}
	}
	if kind != content.RefEntry && (r.Published != nil || r.Latest != "") {
		return false
	}
	for role := range r.Refs {
		switch role {
		case RoleDocument:
		case RolePublished, RoleLatest, RoleScheduled, rolePublishes:
			if kind != content.RefEntry {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (r *summaryRow) marshal() ([]byte, error) {
	return json.Marshal(r, json.Deterministic(true))
}

func (r *summaryRow) summary(kind content.RefKind, id string) Summary {
	return Summary{
		Kind: kind, ID: id, Entry: r.Entry, Published: r.Published, Latest: r.Latest, Term: r.Term, Media: r.Media,
		Author: r.Author, Menu: r.Menu, Form: r.Form, Redirect: r.Redirect,
	}
}

// setRefs sets the IDs that the row refers to in role; none removes the role.
func (r *summaryRow) setRefs(role Role, ids []string) {
	switch {
	case len(ids) > 0:
		if r.Refs == nil {
			r.Refs = make(map[Role][]string)
		}
		r.Refs[role] = ids
	case r.Refs != nil:
		delete(r.Refs, role)
		if len(r.Refs) == 0 {
			r.Refs = nil
		}
	}
}

// refKey is the key in references of the reference of the document of kind with id to target, in role.
func refKey(target string, kind content.RefKind, id string, role Role) []byte {
	b := append([]byte(target), 0)
	b = append(append(b, kind...), 0)
	b = append(append(b, id...), 0)
	return append(b, role...)
}

// refKeys returns the keys in references of the references of the row of the document of kind with id, in order.
func (r *summaryRow) refKeys(kind content.RefKind, id string) [][]byte {
	var keys [][]byte
	for _, role := range slices.Sorted(maps.Keys(r.Refs)) {
		for _, target := range r.Refs[role] {
			keys = append(keys, refKey(target, kind, id, role))
		}
	}
	return keys
}

// refsOf returns the sorted IDs, without duplicates, of the entries, terms, media, authors and forms other than self
// that the strings in data, JSON, are: strings at any depth, also in custom fields and texts, but not the names of
// objects. For a revision, self is its entry.
func refsOf(data []byte, self string) []string {
	dec := jsontext.NewDecoder(bytes.NewBuffer(data))
	var ids []string
	for {
		kind, length := dec.StackIndex(dec.StackDepth())
		tok, err := dec.ReadToken()
		if err != nil {
			break
		}
		// In an object, the tokens that come first, third and so on are names.
		if tok.Kind() != '"' || kind == '{' && length%2 == 0 {
			continue
		}
		if s := tok.String(); s != self && isRefID(s) {
			ids = append(ids, s)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// isRefID reports whether s is the ID of an entry, a term, a medium, an author or a form.
func isRefID(s string) bool {
	var kind content.RefKind
	switch {
	case len(s) < 4:
		return false
	case s[:4] == "ent_":
		kind = content.RefEntry
	case s[:4] == "trm_":
		kind = content.RefTerm
	case s[:4] == "med_":
		kind = content.RefMedia
	case s[:4] == "aut_":
		kind = content.RefAuthor
	case s[:4] == "frm_":
		kind = content.RefForm
	default:
		return false
	}
	return content.CheckID(kind, s) == nil
}

// publishes returns the IDs of the revisions that e names as its published and its scheduled one, sorted and without
// duplicates. Only an ID can name a revision of the store, and a key must not be made of any other string.
func publishes(e *content.Entry) []string {
	var ids []string
	for _, id := range []string{e.PublishedRevision, e.ScheduledRevision} {
		if content.CheckID(content.RefRevision, id) == nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// docRow makes the row of the document id, whose JSON is data and which decoders have decoded as doc. The row of an
// entry still lacks what its revisions give it.
func docRow(id string, doc any, data []byte) summaryRow {
	var r summaryRow
	switch x := doc.(type) {
	case *content.Entry:
		e := *x
		r.Entry = &e
		r.setRefs(rolePublishes, publishes(&e))
	case *content.Term:
		r.Term = &content.Term{
			ID: x.ID, Taxonomy: x.Taxonomy, Language: x.Language, TranslationGroup: x.TranslationGroup, Slug: x.Slug,
			Parent: x.Parent,
		}
	case *content.Media:
		r.Media = &content.Media{ID: x.ID, Hash: x.Hash, MIMEType: x.MIMEType}
	case *content.AuthorProfile:
		r.Author = &content.AuthorProfile{ID: x.ID, Slug: x.Slug}
	case *content.Menu:
		r.Menu = &content.Menu{ID: x.ID, Language: x.Language, Location: x.Location}
	case *content.Form:
		r.Form = &content.Form{ID: x.ID}
	case *content.Redirect:
		r.Redirect = &content.Redirect{ID: x.ID, From: x.From, To: x.To, Status: x.Status}
	}
	r.setRefs(RoleDocument, refsOf(data, id))
	return r
}

// projection returns what the summary of an entry keeps of its published revision r.
func projection(r *content.Revision) *content.Revision {
	return &content.Revision{
		Slug: r.Slug, Parent: r.Parent, Date: r.Date, Author: r.Author, Sticky: r.Sticky, Terms: r.Terms,
	}
}

// decodeRevisionData decodes data, the JSON of the revision id, as AddRevision does.
func decodeRevisionData(id string, data []byte) (*content.Revision, error) {
	rev := new(content.Revision)
	if err := decodeFile(content.SiteFile{Name: revisionName(id), Data: data}, rev.UnmarshalJSON); err != nil {
		return nil, err
	}
	return rev, nil
}

// readRevision returns the revision id with its data, checked against its hash.
func (t *Tx) readRevision(id string) (Revision, bool, error) {
	r, ok, err := t.b.revision(id)
	if err != nil || !ok {
		return Revision{}, false, err
	}
	if err := verify(r.Hash, r.Data); err != nil {
		return Revision{}, false, err
	}
	return r, true, nil
}

// readDecoded returns the revision id and its data if the store has it, of any entry, and it decodes; else nil.
func (t *Tx) readDecoded(id string) (*content.Revision, []byte, error) {
	r, ok, err := t.readRevision(id)
	if err != nil || !ok {
		return nil, nil, err
	}
	rev, err := decodeRevisionData(id, r.Data)
	if err != nil {
		return nil, nil, nil
	}
	return rev, r.Data, nil
}

// revisionData is the data of the revision id, which publish or schedule has read.
type revisionData struct {
	id   string
	data []byte
}

// publish sets in row, that of the entry entry, what the revision id, which the entry publishes, gives it, if the store
// has it, of any entry, and it decodes. It returns the data of the revision if it has set them.
func (t *Tx) publish(row *summaryRow, entry, id string) ([]byte, error) {
	rev, data, err := t.readDecoded(id)
	if rev == nil {
		return nil, err
	}
	row.Published = projection(rev)
	row.setRefs(RolePublished, refsOf(data, entry))
	return data, nil
}

// schedule sets in row, that of the entry entry, the references of the revision id, which the entry schedules, if the
// store has it, of any entry, and it decodes. It returns the data of the revision if it has set them.
func (t *Tx) schedule(row *summaryRow, entry, id string) ([]byte, error) {
	rev, data, err := t.readDecoded(id)
	if rev == nil {
		return nil, err
	}
	row.setRefs(RoleScheduled, refsOf(data, entry))
	return data, nil
}

// latest sets in row, that of the entry entry, the last revision of the entry. If it is one of the revisions in read,
// whose data are set in the row, it does not read it again.
func (t *Tx) latest(row *summaryRow, entry string, read ...revisionData) error {
	revs, err := t.b.revisions(entry)
	if err != nil || len(revs) == 0 {
		return err
	}
	last := revs[len(revs)-1].ID
	var data []byte
	for _, r := range read {
		if r.id == last && r.data != nil {
			data = r.data
		}
	}
	if data == nil {
		r, ok, err := t.readRevision(last)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("entryRevisions of %s: the revision %s is missing", entry, last)
		}
		data = r.Data
	}
	row.Latest = last
	row.setRefs(RoleLatest, refsOf(data, entry))
	return nil
}

// loadRow returns the row of the document of kind with id in summaries, and its value.
func (t *Tx) loadRow(kind content.RefKind, id string) (row summaryRow, value []byte, ok bool, err error) {
	key := docKey(kind, id)
	value, ok, err = t.b.indexGet(indexSummaries, key)
	if err != nil || !ok {
		return summaryRow{}, nil, false, err
	}
	if row, err = readRow(kind, key, value); err != nil {
		return summaryRow{}, nil, false, err
	}
	return row, value, true, nil
}

// saveRow writes row, the new row of the document of kind with id, in place of old, its row in summaries with the
// value oldValue, if it has one: the row of summaries, and the rows of references that differ.
func (t *Tx) saveRow(kind content.RefKind, id string, row, old summaryRow, oldValue []byte) error {
	value, err := row.marshal()
	if err != nil {
		return err
	}
	if bytes.Equal(value, oldValue) {
		return nil
	}
	if err := t.updateRefs(kind, id, old.Refs, row.Refs); err != nil {
		return err
	}
	return t.b.indexPut(indexSummaries, docKey(kind, id), value)
}

// updateRefs makes the rows of references of the document of kind with id those of now, from those of old: it deletes
// those that only old has, and puts those that only now has.
func (t *Tx) updateRefs(kind content.RefKind, id string, old, now map[Role][]string) error {
	roles := slices.Sorted(maps.Keys(old))
	for role := range now {
		if _, ok := old[role]; !ok {
			roles = append(roles, role)
		}
	}
	slices.Sort(roles)
	for _, role := range roles {
		a, b := old[role], now[role]
		for i, j := 0, 0; i < len(a) || j < len(b); {
			var err error
			switch {
			case j == len(b) || i < len(a) && a[i] < b[j]:
				err = t.b.indexDelete(indexReferences, refKey(a[i], kind, id, role))
				i++
			case i == len(a) || b[j] < a[i]:
				err = t.b.indexPut(indexReferences, refKey(b[j], kind, id, role), nil)
				j++
			default:
				i++
				j++
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// indexDocument updates the index after Put has saved data, the JSON of the document of kind with id, which has
// decoded as doc; or, with data nil, after Delete has deleted the document.
func (t *Tx) indexDocument(kind content.RefKind, id string, doc any, data []byte) error {
	old, oldValue, hadOld, err := t.loadRow(kind, id)
	if err != nil {
		return err
	}
	if data == nil {
		if !hadOld {
			return nil
		}
		if err := t.updateRefs(kind, id, old.Refs, nil); err != nil {
			return err
		}
		return t.b.indexDelete(indexSummaries, docKey(kind, id))
	}
	row := docRow(id, doc, data)
	if e, ok := doc.(*content.Entry); ok {
		if err := t.entryRow(&row, id, e, old, hadOld); err != nil {
			return err
		}
		if err := t.raiseNumber(int64(e.Number)); err != nil {
			return err
		}
	}
	return t.saveRow(kind, id, row, old, oldValue)
}

// entryRow adds to row, that of the entry id whose document is e, what its revisions give it, as the row old, which it
// had if hadOld, has them if they are still those: the published revision, the scheduled one, and the last one.
func (t *Tx) entryRow(row *summaryRow, id string, e *content.Entry, old summaryRow, hadOld bool) error {
	var read []revisionData
	if e.PublishedRevision != "" {
		if hadOld && old.Entry != nil && old.Entry.PublishedRevision == e.PublishedRevision && old.Published != nil {
			row.Published = old.Published
			row.setRefs(RolePublished, old.Refs[RolePublished])
		} else {
			data, err := t.publish(row, id, e.PublishedRevision)
			if err != nil {
				return err
			}
			read = append(read, revisionData{e.PublishedRevision, data})
		}
	}
	if e.ScheduledRevision != "" {
		if hadOld && old.Entry != nil && old.Entry.ScheduledRevision == e.ScheduledRevision && len(old.Refs[RoleScheduled]) > 0 {
			row.setRefs(RoleScheduled, old.Refs[RoleScheduled])
		} else {
			data, err := t.schedule(row, id, e.ScheduledRevision)
			if err != nil {
				return err
			}
			read = append(read, revisionData{e.ScheduledRevision, data})
		}
	}
	if hadOld {
		row.Latest = old.Latest
		row.setRefs(RoleLatest, old.Refs[RoleLatest])
		return nil
	}
	return t.latest(row, id, read...)
}

// indexRevision updates the index after AddRevision has added the revision id of the entry entry, whose JSON is data,
// which has decoded as rev: it is the last revision of entry, and the published or the scheduled revision of the
// entries that name it, of which the references to it say.
func (t *Tx) indexRevision(entry, id string, rev *content.Revision, data []byte) error {
	entries := []string{entry}
	prefix := append([]byte(id), 0)
	var rerr error
	if err := t.b.indexScan(indexReferences, prefix, func(key, _ []byte) bool {
		r, ok := splitRefKey(key)
		if !ok {
			rerr = invalidKey(indexReferences, key)
			return false
		}
		// Under the ID of a revision, a key is that of the entry that publishes or schedules it.
		if r.ID != entry {
			entries = append(entries, r.ID)
		}
		return true
	}); err != nil {
		return err
	}
	if rerr != nil {
		return rerr
	}
	for _, e := range entries {
		old, oldValue, ok, err := t.loadRow(content.RefEntry, e)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		row := old
		row.Refs = maps.Clone(old.Refs)
		refs := refsOf(data, e)
		if e == entry {
			row.Latest = id
			row.setRefs(RoleLatest, refs)
		}
		if row.Entry != nil && row.Entry.PublishedRevision == id {
			row.Published = projection(rev)
			row.setRefs(RolePublished, refs)
		}
		if row.Entry != nil && row.Entry.ScheduledRevision == id {
			row.setRefs(RoleScheduled, refs)
		}
		if err := t.saveRow(content.RefEntry, e, row, old, oldValue); err != nil {
			return err
		}
	}
	return nil
}

// splitRefKey splits key, a key of references, into the document that refers and the role. It reports false for a key
// that the index cannot make: a document that has no summary, a role that its kind does not have (only an entry has
// published, latest, scheduled and publishes), or a target that the role does not take (only publishes takes a
// revision).
func splitRefKey(key []byte) (Referrer, bool) {
	target, rest, ok1 := bytes.Cut(key, []byte{0})
	kind, rest, ok2 := bytes.Cut(rest, []byte{0})
	id, role, ok3 := bytes.Cut(rest, []byte{0})
	r := Referrer{Kind: content.RefKind(kind), ID: string(id), Role: Role(role)}
	if !ok1 || !ok2 || !ok3 || checkKey(r.Kind, r.ID) != nil {
		return Referrer{}, false
	}
	var ok bool
	switch r.Role {
	case RoleDocument:
		ok = isRefID(string(target))
	case RolePublished, RoleLatest, RoleScheduled:
		ok = r.Kind == content.RefEntry && isRefID(string(target))
	case rolePublishes:
		ok = r.Kind == content.RefEntry && content.CheckID(content.RefRevision, string(target)) == nil
	}
	if !ok {
		return Referrer{}, false
	}
	return r, true
}

// raiseNumber makes n the largest number that an entry has had, if it is larger than that.
func (t *Tx) raiseNumber(n int64) error {
	last, err := t.b.lastNumber()
	if err != nil || n <= last {
		return err
	}
	return t.b.setLastNumber(n)
}

// checkIndex returns ErrNoIndex if the store has no index.
func (t *Tx) checkIndex() error {
	ready, err := t.b.indexReady()
	switch {
	case err != nil:
		return err
	case !ready:
		return ErrNoIndex
	}
	return nil
}

// Summary returns the summary of the current document of kind with id. If the document does not exist, the error wraps
// fs.ErrNotExist. A store of the table schema 1, which has no index, fails with ErrNoIndex.
func (t *Tx) Summary(kind content.RefKind, id string) (Summary, error) {
	if err := t.check(false); err != nil {
		return Summary{}, err
	}
	if err := checkKey(kind, id); err != nil {
		return Summary{}, err
	}
	if err := t.checkIndex(); err != nil {
		return Summary{}, err
	}
	row, _, ok, err := t.loadRow(kind, id)
	switch {
	case err != nil:
		return Summary{}, err
	case !ok:
		return Summary{}, notFoundError(name(kind, id))
	}
	return row.summary(kind, id), nil
}

// Summaries returns the summaries of the current documents of kind, in the order of their IDs. A key or a row that the
// index cannot have fails it.
func (t *Tx) Summaries(kind content.RefKind) ([]Summary, error) {
	if err := t.check(false); err != nil {
		return nil, err
	}
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	if err := t.checkIndex(); err != nil {
		return nil, err
	}
	prefix := docKey(kind, "")
	var sums []Summary
	var rerr error
	if err := t.b.indexScan(indexSummaries, prefix, func(key, value []byte) bool {
		id := string(key[len(prefix):])
		if checkKey(kind, id) != nil {
			rerr = invalidKey(indexSummaries, key)
			return false
		}
		var row summaryRow
		if row, rerr = readRow(kind, key, value); rerr != nil {
			return false
		}
		sums = append(sums, row.summary(kind, id))
		return true
	}); err != nil {
		return nil, err
	}
	if rerr != nil {
		return nil, rerr
	}
	return sums, nil
}

// Referrers returns the current documents that refer to the document of kind with id, with how, in the order of their
// kinds, IDs and roles: the IDs that the JSON of a document holds, and, of an entry, those that the JSON of its
// published, of its scheduled and of its last revision hold. Only entries, terms, media, authors and forms are referred
// to; nothing refers to the settings document. A key that the index cannot have fails it.
func (t *Tx) Referrers(kind content.RefKind, id string) ([]Referrer, error) {
	if err := t.check(false); err != nil {
		return nil, err
	}
	if err := checkKey(kind, id); err != nil {
		return nil, err
	}
	if err := t.checkIndex(); err != nil {
		return nil, err
	}
	if kind == SettingsKind {
		return nil, nil
	}
	prefix := append([]byte(id), 0)
	var refs []Referrer
	var rerr error
	if err := t.b.indexScan(indexReferences, prefix, func(key, _ []byte) bool {
		r, ok := splitRefKey(key)
		if !ok {
			rerr = invalidKey(indexReferences, key)
			return false
		}
		refs = append(refs, r)
		return true
	}); err != nil {
		return nil, err
	}
	if rerr != nil {
		return nil, rerr
	}
	return refs, nil
}

// LastNumber returns the largest number that an entry has had, also if it has been deleted; 0 if none. A store that
// had no index when it was opened knows only the entries that it had then.
func (t *Tx) LastNumber() (int, error) {
	if err := t.check(false); err != nil {
		return 0, err
	}
	if err := t.checkIndex(); err != nil {
		return 0, err
	}
	n, err := t.b.lastNumber()
	return int(n), err
}

// computeIndex calls emit with every row that the index must have, in the order of the documents, from the current
// documents and the revisions alone, and returns the largest number of the entries.
func (t *Tx) computeIndex(emit func(table indexTable, key, value []byte) error) (int64, error) {
	var largest int64
	for _, kind := range indexKinds {
		docs, err := t.b.documents(kind)
		if err != nil {
			return 0, err
		}
		for _, d := range docs {
			if err := verify(d.Hash, d.Data); err != nil {
				return 0, err
			}
			// A document that does not decode has no summary.
			doc, err := checkData(kind, d.ID, d.Data)
			if err != nil {
				continue
			}
			row := docRow(d.ID, doc, d.Data)
			if e, ok := doc.(*content.Entry); ok {
				largest = max(largest, int64(e.Number))
				if err := t.entryRow(&row, d.ID, e, summaryRow{}, false); err != nil {
					return 0, err
				}
			}
			value, err := row.marshal()
			if err != nil {
				return 0, err
			}
			if err := emit(indexSummaries, docKey(kind, d.ID), value); err != nil {
				return 0, err
			}
			for _, key := range row.refKeys(kind, d.ID) {
				if err := emit(indexReferences, key, nil); err != nil {
					return 0, err
				}
			}
		}
	}
	return largest, nil
}

// RebuildIndex makes the index again from the current documents and the revisions: the rows of summaries and
// references become those that Put, Delete and AddRevision would have made, and the last number is raised to the
// largest of the current entries. It writes only what differs: the rows that are missing or have another value, and
// the deletion of the rows that nothing gives, so that an index that is right gets no write. A document that does not
// decode has no summary. It leaves the operations, the accounts and the sessions, which are records, and not derived
// from anything.
func (t *Tx) RebuildIndex() error {
	if err := t.check(true); err != nil {
		return err
	}
	tables := []indexTable{indexSummaries, indexReferences}
	// Of each table that has rows, the keys of the rows that the index must have, to find those that it has too much.
	// A table without rows needs neither: its rows are all to put.
	want := map[indexTable]map[string]struct{}{}
	for _, table := range tables {
		empty := true
		if err := t.b.indexScan(table, nil, func(_, _ []byte) bool {
			empty = false
			return false
		}); err != nil {
			return err
		}
		if !empty {
			want[table] = map[string]struct{}{}
		}
	}
	largest, err := t.computeIndex(func(table indexTable, key, value []byte) error {
		if keys, ok := want[table]; ok {
			keys[string(key)] = struct{}{}
			if old, found, err := t.b.indexGet(table, key); err != nil || found && bytes.Equal(old, value) {
				return err
			}
		}
		return t.b.indexPut(table, key, value)
	})
	if err != nil {
		return err
	}
	for _, table := range tables {
		keys, ok := want[table]
		if !ok {
			continue
		}
		var derr error
		if err := t.b.indexScan(table, nil, func(key, _ []byte) bool {
			if _, ok := keys[string(key)]; !ok {
				derr = t.b.indexDelete(table, key)
			}
			return derr == nil
		}); err != nil {
			return err
		}
		if derr != nil {
			return derr
		}
	}
	return t.raiseNumber(largest)
}

// maxOperationID is the largest ID of an operation, in bytes.
const maxOperationID = 256

func checkOperationID(id string) error {
	switch {
	case id == "":
		return errors.New("invalid operation ID: it is empty")
	case len(id) > maxOperationID:
		return fmt.Errorf("invalid operation ID: it has %d bytes (max %d)", len(id), maxOperationID)
	}
	return nil
}

// Operation returns the data that PutOperation has saved for the operation id, and whether there are any. The store
// keeps them for the save operations, which record what they have done to answer a retry; their ID and data are theirs.
func (t *Tx) Operation(id string) ([]byte, bool, error) {
	if err := t.check(false); err != nil {
		return nil, false, err
	}
	if err := checkOperationID(id); err != nil {
		return nil, false, err
	}
	data, ok, err := t.b.indexGet(indexOperations, []byte(id))
	if err != nil || !ok {
		return nil, false, err
	}
	return append([]byte{}, data...), true, nil
}

// PutOperation saves data for the operation id, in place of what it had.
func (t *Tx) PutOperation(id string, data []byte) error {
	if err := t.check(true); err != nil {
		return err
	}
	if err := checkOperationID(id); err != nil {
		return err
	}
	return t.b.indexPut(indexOperations, []byte(id), data)
}
