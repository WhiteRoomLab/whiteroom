package admin

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// entryKind is the kind for the routes that change an entry: who may edit one (save a revision of it) and delete one depends on
// who created it.
var entryKind = docKind{
	path: "entries", kind: content.RefEntry,
	create: docRule{action: account.CreateEntry},
	save:   docRule{account.EditEntry, scopeCreator},
	remove: docRule{account.DeleteEntry, scopeCreator},
}

func (s *Server) entryRoutes() {
	s.apiHeavy("GET /api/entries", s.listEntries)
	s.apiHeavy("POST /api/entries", s.createEntry)
	s.apiHeavy("GET /api/entries/{id}", s.showEntry)
	s.apiHeavy("DELETE /api/entries/{id}", s.deleteEntry)
	s.apiHeavy("GET /api/entries/{id}/revisions", s.listRevisions)
	s.apiHeavy("POST /api/entries/{id}/revisions", s.saveRevision)
	s.apiHeavy("POST /api/entries/{id}/translations", s.createTranslation)
	s.apiHeavy("GET /api/revisions/{id}", s.showRevision)
}

// entryOwner is the target of inUpdate for the entry id: the account that created it owns it.
func entryOwner(id string) func(tx *store.Tx) (account.Target, error) {
	return func(tx *store.Tx) (account.Target, error) {
		sum, err := tx.Summary(content.RefEntry, id)
		if err != nil {
			return account.Target{}, err
		}
		return account.Target{Owner: sum.Entry.CreatedBy}, nil
	}
}

// htmlRight returns Op.EditHTML for the principal, and, if it is true, the action that the check in the Update must hold to
// besides its own, so that an account that has lost the right since the request came saves no html block.
func htmlRight(p account.Principal) (editHTML bool, also []account.Action) {
	if !account.Allowed(p, account.EditHTML, account.Target{}) {
		return false, nil
	}
	return true, []account.Action{account.EditHTML}
}

// The sorts of a list of entries.
const (
	sortNumber      = "number"
	sortCreatedAt   = "createdAt"
	sortDate        = "date"
	sortScheduledAt = "scheduledAt"
)

var entrySorts = []string{sortNumber, sortCreatedAt, sortDate, sortScheduledAt}

// entryForms are the names that a list of entries takes besides its page.
var entryForms = queryForms{
	"type":             func(v string) bool { return content.CheckName(v) == nil },
	"language":         func(v string) bool { return content.CheckLanguage(v) == nil },
	"status":           func(v string) bool { _, ok := parseStatuses(v); return ok },
	"term":             isID(content.RefTerm),
	"createdBy":        isID(content.RefUser),
	"translationGroup": isID(content.RefTranslationGroup),
	"sort":             func(v string) bool { return slices.Contains(entrySorts, v) },
	"order":            func(v string) bool { return v == "asc" || v == "desc" },
}

func isID(kind content.RefKind) func(v string) bool {
	return func(v string) bool { return content.CheckID(kind, v) == nil }
}

// parseStatuses reads statuses of entries separated by commas.
func parseStatuses(v string) ([]content.Status, bool) {
	var statuses []content.Status
	for _, name := range strings.Split(v, ",") {
		switch s := content.Status(name); s {
		case content.StatusDraft, content.StatusPublished, content.StatusScheduled, content.StatusArchived:
			statuses = append(statuses, s)
		default:
			return nil, false
		}
	}
	return statuses, true
}

// entryQuery is what a list of entries asks for. A filter that is "" or empty keeps every entry.
type entryQuery struct {
	page            page
	typ, language   string
	statuses        []content.Status
	term, createdBy string
	group           string
	sortBy          string
	desc            bool
}

// readEntryQuery reads the query of a list of entries. If it fails it has answered 400.
func readEntryQuery(w http.ResponseWriter, r *http.Request) (entryQuery, bool) {
	pg, v, ok := readListQuery(w, r, entryForms)
	if !ok {
		return entryQuery{}, false
	}
	q := entryQuery{
		page: pg, typ: v.Get("type"), language: v.Get("language"), term: v.Get("term"), createdBy: v.Get("createdBy"),
		group: v.Get("translationGroup"), sortBy: sortNumber, desc: v.Get("order") != "asc",
	}
	if by := v.Get("sort"); by != "" {
		q.sortBy = by
	}
	if list := v.Get("status"); list != "" {
		q.statuses, _ = parseStatuses(list)
	}
	return q, true
}

func termsHave(terms content.Terms, id string) bool {
	for _, ids := range terms {
		if slices.Contains(ids, id) {
			return true
		}
	}
	return false
}

// latestReferrers returns the entries whose last revision holds the ID of the term, in its terms or anywhere else.
func latestReferrers(tx *store.Tx, term string) (map[string]bool, error) {
	refs, err := tx.Referrers(content.RefTerm, term)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]bool)
	for _, ref := range refs {
		if ref.Kind == content.RefEntry && ref.Role == store.RoleLatest {
			latest[ref.ID] = true
		}
	}
	return latest, nil
}

// hasTerm reports whether the entry of s has the term in the terms of its published revision, or of its last one: a link to
// the term elsewhere in the last revision does not count. latest is latestReferrers of the term.
func hasTerm(tx *store.Tx, s *store.Summary, term string, latest map[string]bool) (bool, error) {
	if s.Published != nil && termsHave(s.Published.Terms, term) {
		return true, nil
	}
	if !latest[s.ID] || s.Latest == "" || s.Published != nil && s.Latest == s.Entry.PublishedRevision {
		return false, nil
	}
	r, err := tx.Revision(s.Latest)
	if err != nil {
		return false, err
	}
	var rev content.Revision
	if rev.UnmarshalJSON(r.Data) != nil {
		return false, nil
	}
	return termsHave(rev.Terms, term), nil
}

func (q *entryQuery) keeps(tx *store.Tx, s *store.Summary, latest map[string]bool) (bool, error) {
	e := s.Entry
	switch {
	case q.typ != "" && e.Type != q.typ,
		q.language != "" && e.Language != q.language,
		len(q.statuses) > 0 && !slices.Contains(q.statuses, e.Status),
		q.createdBy != "" && e.CreatedBy != q.createdBy,
		q.group != "" && e.TranslationGroup != q.group:
		return false, nil
	}
	if q.term == "" {
		return true, nil
	}
	return hasTerm(tx, s, q.term, latest)
}

// entryItem is an entry that a list keeps, with the time that it is sorted by, if it has one.
type entryItem struct {
	sum  *store.Summary
	when time.Time
	has  bool
}

func (q *entryQuery) item(s *store.Summary) entryItem {
	it := entryItem{sum: s}
	var text string
	switch q.sortBy {
	case sortCreatedAt:
		text = s.Entry.CreatedAt
	case sortDate:
		if s.Published != nil {
			text = s.Published.Date
		}
	case sortScheduledAt:
		text = s.Entry.ScheduledAt
	default:
		return it
	}
	var err error
	it.when, err = time.Parse(time.RFC3339, text)
	it.has = err == nil
	return it
}

// sort puts the items in the order that q asks for: an item that has no time to sort by is after the others whatever the
// direction, and items of the same value stay in the order of their IDs, which they come in.
func (q *entryQuery) sort(items []entryItem) {
	direction := func(c int) int {
		if q.desc {
			return -c
		}
		return c
	}
	slices.SortStableFunc(items, func(a, b entryItem) int {
		if q.sortBy == sortNumber {
			return direction(cmp.Compare(a.sum.Entry.Number, b.sum.Entry.Number))
		}
		switch {
		case !a.has && !b.has:
			return 0
		case !a.has:
			return 1
		case !b.has:
			return -1
		}
		return direction(a.when.Compare(b.when))
	})
}

// entryRow is a row of a list of entries: the state document, the last revision without its JSON, and the published revision.
type entryRow struct {
	doc       store.Document
	latest    *store.Revision
	title     string
	hasTitle  bool
	published *content.Revision
}

// read returns the page of the entries that q asks for, and how many entries q keeps in all.
func (q *entryQuery) read(tx *store.Tx) (rows []entryRow, total int, err error) {
	sums, err := tx.Summaries(content.RefEntry)
	if err != nil {
		return nil, 0, err
	}
	var latest map[string]bool
	if q.term != "" {
		if latest, err = latestReferrers(tx, q.term); err != nil {
			return nil, 0, err
		}
	}
	items := make([]entryItem, 0, len(sums))
	for i := range sums {
		ok, err := q.keeps(tx, &sums[i], latest)
		if err != nil {
			return nil, 0, err
		}
		if ok {
			items = append(items, q.item(&sums[i]))
		}
	}
	q.sort(items)
	from, to := q.page.bounds(len(items))
	rows = make([]entryRow, 0, to-from)
	for _, it := range items[from:to] {
		row, err := readEntryRow(tx, it.sum)
		if err != nil {
			return nil, 0, err
		}
		rows = append(rows, row)
	}
	return rows, len(items), nil
}

func readEntryRow(tx *store.Tx, s *store.Summary) (entryRow, error) {
	doc, err := tx.Document(content.RefEntry, s.ID)
	if err != nil {
		return entryRow{}, err
	}
	row := entryRow{doc: doc, published: s.Published}
	if s.Latest != "" {
		rev, err := tx.Revision(s.Latest)
		if err != nil {
			return entryRow{}, err
		}
		row.title, row.hasTitle = revisionTitle(rev.Data)
		rev.Data = nil
		row.latest = &rev
	}
	return row, nil
}

// revisionTitle returns the title of the revision whose JSON is data: the member title of its top level, which it reads no
// further than. It reports false if there is none, if it is not a string, or if the JSON cannot be read.
func revisionTitle(data []byte) (string, bool) {
	d := jsontext.NewDecoder(bytes.NewReader(data))
	if tok, err := d.ReadToken(); err != nil || tok.Kind() != '{' {
		return "", false
	}
	for d.PeekKind() == '"' {
		name, err := d.ReadToken()
		if err != nil {
			return "", false
		}
		if name.String() != "title" {
			if d.SkipValue() != nil {
				return "", false
			}
			continue
		}
		if d.PeekKind() != '"' {
			return "", false
		}
		title, err := d.ReadToken()
		if err != nil {
			return "", false
		}
		return title.String(), true
	}
	return "", false
}

func (row *entryRow) write(j *jsonStream) {
	j.object()
	j.versioned(row.doc)
	if r := row.latest; r != nil {
		j.key("latestRevision")
		j.object()
		j.key("id")
		j.value(r.ID)
		if row.hasTitle {
			j.key("title")
			j.value(row.title)
		}
		j.key("savedAt")
		j.value(r.SavedAt.UTC())
		j.key("savedBy")
		j.value(r.SavedBy)
		j.end()
	}
	if row.published != nil {
		j.key("published")
		j.object()
		j.key("slug")
		j.value(row.published.Slug)
		if row.published.Date != "" {
			j.key("date")
			j.value(row.published.Date)
		}
		j.end()
	}
	j.end()
}

// listEntries is GET /api/entries. It reads the summaries of the index, and only the entries of the page (and, for a term, the
// last revisions that mention it) have their documents and revisions read.
func (s *Server) listEntries(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	q, ok := readEntryQuery(w, r)
	if !ok {
		return
	}
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		var rows []entryRow
		var total int
		err := s.store.View(ctx, func(tx *store.Tx) (err error) {
			rows, total, err = q.read(tx)
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, nil, writeList(out, total, q.page, func(j *jsonStream) error {
			for i := range rows {
				rows[i].write(j)
			}
			return nil
		})
	})
}

// writeRevision writes the revision r as an object, with its JSON as the store has it. The entry is named if entry is true.
func writeRevision(j *jsonStream, r store.Revision, entry bool) {
	j.object()
	j.key("id")
	j.value(r.ID)
	if entry {
		j.key("entry")
		j.value(r.Entry)
	}
	j.key("seq")
	j.value(r.Seq)
	j.key("hash")
	j.value(r.Hash)
	j.key("savedAt")
	j.value(r.SavedAt.UTC())
	j.key("savedBy")
	j.value(r.SavedBy)
	j.key("document")
	j.raw(r.Data)
	j.end()
}

// showEntry is GET /api/entries/{id}: the state document of the entry and its last revision.
func (s *Server) showEntry(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(w, content.RefEntry, id) {
		return
	}
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		var doc store.Document
		var latest *store.Revision
		err := s.store.View(ctx, func(tx *store.Tx) (err error) {
			if doc, err = tx.Document(content.RefEntry, id); err != nil {
				return err
			}
			sum, err := tx.Summary(content.RefEntry, id)
			if err != nil || sum.Latest == "" {
				return err
			}
			rev, err := tx.Revision(sum.Latest)
			latest = &rev
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		j := newJSONStream(out)
		j.object()
		j.versioned(doc)
		if latest != nil {
			j.key("latestRevision")
			writeRevision(j, *latest, false)
		}
		j.end()
		return http.StatusOK, nil, j.done()
	})
}

// createEntry is POST /api/entries: it makes a draft entry of the type and the language in the body.
func (s *Server) createEntry(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowRule(w, r, p, entryKind.create, "", audit.Target{}) {
		return
	}
	opID, ok := operationID(w, r)
	if !ok {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	var in struct {
		Type     *string `json:"type"`
		Language *string `json:"language"`
	}
	if !readJSON(w, r, smallBodyLimit, &in) {
		return
	}
	if in.Type == nil || in.Language == nil {
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		return
	}
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		op := edit.Op{By: p.Account.ID, ID: opID, Check: entryKind.create.check(p, "", audit.Target{})}
		res, err := s.editor.CreateEntry(ctx, op, *in.Type, *in.Language)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, http.Header{"Location": {"/api/entries/" + res.ID}}, writeResult(out, res)
	})
}

// deleteEntry is DELETE /api/entries/{id}?base=: the entry must be at the version of base and neither published nor scheduled.
func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request, p account.Principal) {
	c, ok := s.beginChange(w, r, p, &entryKind, entryKind.remove, queryForms{"base": isBase}, "base")
	if !ok {
		return
	}
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		op := edit.Op{By: p.Account.ID, ID: c.opID, Check: entryKind.remove.check(p, c.id, c.named)}
		res, err := s.editor.Delete(ctx, op, content.RefEntry, c.id, baseOf(c.query))
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, nil, writeResult(out, res)
	})
}

type revisionAnswer struct {
	ID      string    `json:"id"`
	Seq     int64     `json:"seq"`
	Hash    string    `json:"hash"`
	SavedAt time.Time `json:"savedAt"`
	SavedBy string    `json:"savedBy"`
}

// listRevisions is GET /api/entries/{id}/revisions: the newest first, without their JSON. They stay after the entry is deleted;
// an entry that has neither itself nor revisions is not found.
func (s *Server) listRevisions(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	pg, _, ok := readListQuery(w, r, nil)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(w, content.RefEntry, id) {
		return
	}
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		var revs []store.Revision
		err := s.store.View(ctx, func(tx *store.Tx) (err error) {
			if revs, err = tx.Revisions(id); err != nil || len(revs) > 0 {
				return err
			}
			_, err = tx.Document(content.RefEntry, id)
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		slices.Reverse(revs)
		from, to := pg.bounds(len(revs))
		return http.StatusOK, nil, writeList(out, len(revs), pg, func(j *jsonStream) error {
			for _, rev := range revs[from:to] {
				j.value(revisionAnswer{ID: rev.ID, Seq: rev.Seq, Hash: rev.Hash, SavedAt: rev.SavedAt.UTC(), SavedBy: rev.SavedBy})
			}
			return nil
		})
	})
}

// readRevisionSave reads {"base":…,"revision":…}: the ID of the last revision that the caller has read, "" if there is none,
// and the revision as it was sent, with its white space.
func readRevisionSave(in *spool) (base string, revision []byte, err error) {
	var body struct {
		Base     *string        `json:"base"`
		Revision jsontext.Value `json:"revision"`
	}
	if err := readDocument(in, &body); err != nil {
		return "", nil, err
	}
	if body.Base == nil || !isObject(body.Revision) || *body.Base != "" && content.CheckID(content.RefRevision, *body.Base) != nil {
		return "", nil, errBadBody
	}
	return *body.Base, body.Revision, nil
}

// saveRevision is POST /api/entries/{id}/revisions: the last revision of the entry must be the one that base names.
func (s *Server) saveRevision(w http.ResponseWriter, r *http.Request, p account.Principal) {
	c, ok := s.beginChange(w, r, p, &entryKind, entryKind.save, nil)
	if !ok {
		return
	}
	editHTML, also := htmlRight(p)
	s.heavyWork(w, r, p, documentBodyLimit, func(ctx context.Context, in, out *spool) (int, http.Header, error) {
		base, revision, err := readRevisionSave(in)
		if err != nil {
			return 0, nil, err
		}
		op := edit.Op{By: p.Account.ID, ID: c.opID, Check: entryKind.save.check(p, c.id, c.named, also...), EditHTML: editHTML}
		res, err := s.editor.SaveRevision(ctx, op, c.id, base, revision)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, http.Header{"Location": {"/api/revisions/" + res.Revision}}, writeResult(out, res)
	})
}

// createTranslation is POST /api/entries/{id}/translations: it makes a draft entry in the language of the body.
func (s *Server) createTranslation(w http.ResponseWriter, r *http.Request, p account.Principal) {
	c, ok := s.beginChange(w, r, p, &entryKind, entryKind.create, nil)
	if !ok {
		return
	}
	var in struct {
		Language *string `json:"language"`
	}
	if !readJSON(w, r, smallBodyLimit, &in) {
		return
	}
	if in.Language == nil {
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		return
	}
	editHTML, also := htmlRight(p)
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		op := edit.Op{By: p.Account.ID, ID: c.opID, Check: entryKind.create.check(p, c.id, c.named, also...), EditHTML: editHTML}
		res, err := s.editor.CreateTranslation(ctx, op, c.id, *in.Language)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, http.Header{"Location": {"/api/entries/" + res.ID}}, writeResult(out, res)
	})
}

// showRevision is GET /api/revisions/{id}.
func (s *Server) showRevision(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(w, content.RefRevision, id) {
		return
	}
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		var rev store.Revision
		err := s.store.View(ctx, func(tx *store.Tx) (err error) {
			rev, err = tx.Revision(id)
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		j := newJSONStream(out)
		writeRevision(j, rev, true)
		return http.StatusOK, nil, j.done()
	})
}
