package admin

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"net/url"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// routeFunc is a route of the admin API, as s.api takes it.
type routeFunc = func(http.ResponseWriter, *http.Request, account.Principal)

// scope says what a permission to do something to a document depends on besides the role of the account (7.3).
type scope uint8

const (
	scopeRole     scope = iota // nothing: the role decides
	scopeAuthor                // the author profile that is changed: an author account may change its own
	scopeUploader              // the account that uploaded the medium: an author account may change its own
	scopeCreator               // the account that created the entry: an author account may change its own
)

// docRule is who may do an operation to a kind of document: the action of 7.3, and what it depends on. The zero rule is
// for an operation that the kind does not have.
type docRule struct {
	action account.Action
	scope  scope
}

// docKind is a kind of document that has a collection in the API, /api/<path>, with the operations on the collection and on
// each document in it. The entries and the settings have routes of their own.
type docKind struct {
	path                 string
	kind                 content.RefKind
	create, save, remove docRule
	filters              []listFilter // the names that a list takes besides its page
}

// mediaKind is the kind of the media: docKinds serves their documents, and media.go their files.
var mediaKind = docKind{
	path: "media", kind: content.RefMedia,
	save: docRule{account.EditMedia, scopeUploader}, remove: docRule{account.DeleteMedia, scopeUploader},
	filters: []listFilter{mediaKindFilter, uploadedByFilter},
}

// docKinds are the kinds that this file serves. The media are made by the upload, which is not here.
var docKinds = []docKind{
	{
		path: "terms", kind: content.RefTerm,
		create: docRule{action: account.EditTerms}, save: docRule{action: account.EditTerms}, remove: docRule{action: account.EditTerms},
		filters: []listFilter{taxonomyFilter, termLanguageFilter},
	},
	{
		path: "authors", kind: content.RefAuthor,
		create: docRule{action: account.CreateAuthor}, save: docRule{account.EditAuthor, scopeAuthor},
		remove: docRule{action: account.DeleteAuthor},
	},
	{
		path: "menus", kind: content.RefMenu,
		create: docRule{action: account.EditMenus}, save: docRule{action: account.EditMenus}, remove: docRule{action: account.EditMenus},
		filters: []listFilter{menuLanguageFilter, locationFilter},
	},
	{
		path: "forms", kind: content.RefForm,
		create: docRule{action: account.EditForms}, save: docRule{action: account.EditForms}, remove: docRule{action: account.EditForms},
	},
	{
		path: "redirects", kind: content.RefRedirect,
		create: docRule{action: account.EditRedirects}, save: docRule{action: account.EditRedirects},
		remove: docRule{action: account.EditRedirects},
	},
	mediaKind,
}

// listFilter is a name of the query of a list that keeps the documents which have a value: valid checks the form of the
// value, and match decides whether a document has it, from its summary or, if it must, from the document, which it reads
// with tx.
type listFilter struct {
	name  string
	valid func(value string) bool
	match func(tx *store.Tx, s store.Summary, value string) (bool, error)
}

// fieldFilter is the filter of a name that keeps the documents whose summary has the value in the field that field returns.
func fieldFilter(name string, valid func(string) error, field func(s store.Summary) string) listFilter {
	return listFilter{
		name:  name,
		valid: func(v string) bool { return valid(v) == nil },
		match: func(_ *store.Tx, s store.Summary, v string) (bool, error) { return field(s) == v, nil },
	}
}

var (
	taxonomyFilter     = fieldFilter("taxonomy", content.CheckName, func(s store.Summary) string { return s.Term.Taxonomy })
	termLanguageFilter = fieldFilter("language", content.CheckLanguage, func(s store.Summary) string { return s.Term.Language })
	menuLanguageFilter = fieldFilter("language", content.CheckLanguage, func(s store.Summary) string { return s.Menu.Language })
	locationFilter     = fieldFilter("location", content.CheckName, func(s store.Summary) string { return s.Menu.Location })
	mediaKindFilter    = fieldFilter("kind", checkMediaKind, func(s store.Summary) string { return content.MediaKind(s.Media.MIMEType) })
	uploadedByFilter   = listFilter{
		name:  "uploadedBy",
		valid: func(v string) bool { return content.CheckID(content.RefUser, v) == nil },
		match: func(tx *store.Tx, s store.Summary, v string) (bool, error) {
			m, err := readMedium(tx, s.ID)
			return m != nil && m.UploadedBy == v, err
		},
	}
)

// checkMediaKind checks that v is a kind of media.
func checkMediaKind(v string) error {
	if v != content.MediaImage && v != content.MediaPDF {
		return errors.New("not a kind of media")
	}
	return nil
}

// readMedium reads the document of the medium id in tx, or returns nil for one that this program cannot decode.
func readMedium(tx *store.Tx, id string) (*content.Media, error) {
	d, err := tx.Document(content.RefMedia, id)
	if err != nil {
		return nil, err
	}
	m := new(content.Media)
	if m.UnmarshalJSON(d.Data) != nil {
		return nil, nil
	}
	return m, nil
}

// forms returns the names that a list of the kind takes besides its page, with the checks of their values.
func (d *docKind) forms() queryForms {
	forms := make(queryForms, len(d.filters)+2)
	for _, f := range d.filters {
		forms[f.name] = f.valid
	}
	return forms
}

// matching returns the IDs of the documents of the kind that have the values of the filters that q gives, in the order of the
// IDs. The filters that read only the summaries come first in each list, so that the documents are read for fewer.
func (d *docKind) matching(tx *store.Tx, q url.Values) ([]string, error) {
	sums, err := tx.Summaries(d.kind)
	if err != nil {
		return nil, err
	}
	var asked []listFilter
	for _, f := range d.filters {
		if q.Has(f.name) {
			asked = append(asked, f)
		}
	}
	ids := make([]string, 0, len(sums))
sums:
	for _, s := range sums {
		for _, f := range asked {
			switch ok, err := f.match(tx, s, q.Get(f.name)); {
			case err != nil:
				return nil, err
			case !ok:
				continue sums
			}
		}
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// documentRoutes registers the routes of the documents that are not entries: the collections of docKinds and the settings.
// Each of them reads or writes the store, so each is a route of heavy work.
func (s *Server) documentRoutes() {
	for i := range docKinds {
		d := &docKinds[i]
		collection, item := "/api/"+d.path, "/api/"+d.path+"/{id}"
		s.apiHeavy("GET "+collection, s.listDocuments(d))
		if d.create.action != "" {
			s.apiHeavy("POST "+collection, s.createDocument(d))
		}
		s.apiHeavy("GET "+item, s.showDocument(d))
		s.apiHeavy("PUT "+item, s.saveDocument(d))
		s.apiHeavy("DELETE "+item, s.deleteDocument(d))
	}
	s.apiHeavy("GET /api/settings", s.showSettings)
	s.apiHeavy("PUT /api/settings", s.saveSettings)
}

// allowRule reports whether the principal may do what rule says to the document id, before the body of the request is
// received and without reading the store: the action that depends on the document is asked as if it were the account's own,
// and Op.Check of the operation, which is rule.check, decides in the Update. If it may not, the refusal is recorded and
// answered.
func (s *Server) allowRule(w http.ResponseWriter, r *http.Request, p account.Principal, rule docRule, id string, named audit.Target) bool {
	var target account.Target
	switch rule.scope {
	case scopeRole:
	case scopeAuthor:
		target.Author = id
	case scopeUploader, scopeCreator:
		return s.allowAsOwn(w, r, p, rule.action, named)
	}
	return s.allow(w, r, p, rule.action, target, named)
}

// check returns the check of the permission that an operation of the principal to the document id calls in its Update
// (edit.Op.Check): that of the rule, and the actions of also, which the principal must have as well.
func (rule docRule) check(p account.Principal, id string, named audit.Target, also ...account.Action) func(tx *store.Tx) error {
	var target func(tx *store.Tx) (account.Target, error)
	switch rule.scope {
	case scopeRole:
	case scopeAuthor:
		target = func(*store.Tx) (account.Target, error) { return account.Target{Author: id}, nil }
	case scopeCreator:
		target = entryOwner(id)
	case scopeUploader:
		target = func(tx *store.Tx) (account.Target, error) {
			m, err := readMedium(tx, id)
			if m == nil {
				return account.Target{}, err
			}
			return account.Target{Owner: m.UploadedBy}, nil
		}
	}
	return inUpdate(p, named, target, append([]account.Action{rule.action}, also...)...)
}

// listDocuments is GET /api/<path>: the documents of the kind in the order of their IDs, a page of them, with the versions
// they are at and as the store has them.
func (s *Server) listDocuments(d *docKind) routeFunc {
	return func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		if !s.allowView(w, r, p) || !noOperationID(w, r) {
			return
		}
		pg, q, ok := readListQuery(w, r, d.forms())
		if !ok {
			return
		}
		s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
			var docs []store.Document
			var total int
			err := s.store.View(ctx, func(tx *store.Tx) error {
				ids, err := d.matching(tx, q)
				if err != nil {
					return err
				}
				total = len(ids)
				from, to := pg.bounds(total)
				docs = make([]store.Document, 0, to-from)
				for _, id := range ids[from:to] {
					doc, err := tx.Document(d.kind, id)
					if err != nil {
						return err
					}
					docs = append(docs, doc)
				}
				return nil
			})
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, nil, writeDocuments(out, docs, total, pg.offset, pg.limit)
		})
	}
}

// showDocument is GET /api/<path>/{id}.
func (s *Server) showDocument(d *docKind) routeFunc {
	return func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		if !s.allowView(w, r, p) || !noOperationID(w, r) {
			return
		}
		if _, ok := readQuery(w, r, nil); !ok {
			return
		}
		id := r.PathValue("id")
		if !validID(w, d.kind, id) {
			return
		}
		s.readOne(w, r, p, d.kind, id)
	}
}

// validID reports whether id is the ID of a document of kind, and else answers 404: the path names none.
func validID(w http.ResponseWriter, kind content.RefKind, id string) bool {
	if content.CheckID(kind, id) != nil {
		writeProblem(w, http.StatusNotFound, codeNotFound, detailNotFound)
		return false
	}
	return true
}

// readOne answers with the document of kind with id, "" for the settings, as the store has it.
func (s *Server) readOne(w http.ResponseWriter, r *http.Request, p account.Principal, kind content.RefKind, id string) {
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		var doc store.Document
		err := s.store.View(ctx, func(tx *store.Tx) (err error) {
			doc, err = tx.Document(kind, id)
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, nil, writeDocument(out, doc)
	})
}

// isObject reports whether v, a JSON value, is an object, as every document is.
func isObject(v jsontext.Value) bool { return len(v) > 0 && v[0] == '{' }

// readCreate reads the body of a request that makes a document, {"document":…}, and returns the document as it was sent: the
// bytes of the value, with the white space in them, so that the problems of the document are located in them.
func readCreate(in *spool) ([]byte, error) {
	var body struct {
		Document jsontext.Value `json:"document"`
	}
	if err := readDocument(in, &body); err != nil {
		return nil, err
	}
	if !isObject(body.Document) {
		return nil, errBadBody
	}
	return body.Document, nil
}

// readSave reads the body of a request that saves a document, {"base":…,"document":…}, as readCreate does, with the Seq of the
// version that the document is based on.
func readSave(in *spool) (base int64, document []byte, err error) {
	var body struct {
		Base     *int64         `json:"base"`
		Document jsontext.Value `json:"document"`
	}
	if err := readDocument(in, &body); err != nil {
		return 0, nil, err
	}
	if body.Base == nil || *body.Base < 0 || !isObject(body.Document) {
		return 0, nil, errBadBody
	}
	return *body.Base, body.Document, nil
}

// createDocument is POST /api/<path>: it makes a document of the kind from the document in the body, 201 with the Location of
// the new one.
func (s *Server) createDocument(d *docKind) routeFunc {
	return func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		if !s.allowRule(w, r, p, d.create, "", audit.Target{}) {
			return
		}
		opID, ok := operationID(w, r)
		if !ok {
			return
		}
		if _, ok := readQuery(w, r, nil); !ok {
			return
		}
		s.heavyWork(w, r, p, documentBodyLimit, func(ctx context.Context, in, out *spool) (int, http.Header, error) {
			document, err := readCreate(in)
			if err != nil {
				return 0, nil, err
			}
			op := edit.Op{By: p.Account.ID, ID: opID, Check: d.create.check(p, "", audit.Target{})}
			res, err := s.editor.Create(ctx, op, d.kind, document)
			if err != nil {
				return 0, nil, err
			}
			if err := writeResult(out, res); err != nil {
				return 0, nil, err
			}
			return http.StatusCreated, http.Header{"Location": {"/api/" + d.path + "/" + res.ID}}, nil
		})
	}
}

// change is what a route that changes one document has read of its request, besides the body: the document it names, how the
// refusal of the request is recorded, the operation ID, and the query.
type change struct {
	id    string
	named audit.Target
	opID  string
	query url.Values
}

// beginChange checks a request that changes the document of the path, in the order of the routes: the permission of rule, the
// header Idempotency-Key, the query (which may have only the names of forms, and must have those of required) and the ID in the
// path. If one fails it has answered, and returns false.
func (s *Server) beginChange(w http.ResponseWriter, r *http.Request, p account.Principal, d *docKind, rule docRule, forms queryForms, required ...string) (change, bool) {
	id := r.PathValue("id")
	c := change{id: id, named: audit.Target{Kind: string(d.kind), ID: id}}
	if !s.allowRule(w, r, p, rule, id, c.named) {
		return change{}, false
	}
	var ok bool
	if c.opID, ok = operationID(w, r); !ok {
		return change{}, false
	}
	if c.query, ok = readQuery(w, r, forms, required...); !ok {
		return change{}, false
	}
	if !validID(w, d.kind, id) {
		return change{}, false
	}
	return c, true
}

// saveDocument is PUT /api/<path>/{id}: it saves the document in the body as the next version of the one with the ID, which
// must be at the version that the body gives as base.
func (s *Server) saveDocument(d *docKind) routeFunc {
	return func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		c, ok := s.beginChange(w, r, p, d, d.save, nil)
		if !ok {
			return
		}
		s.heavyWork(w, r, p, documentBodyLimit, func(ctx context.Context, in, out *spool) (int, http.Header, error) {
			base, document, err := readSave(in)
			if err != nil {
				return 0, nil, err
			}
			op := edit.Op{By: p.Account.ID, ID: c.opID, Check: d.save.check(p, c.id, c.named)}
			res, err := s.editor.Save(ctx, op, d.kind, c.id, base, document)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, nil, writeResult(out, res)
		})
	}
}

// deleteDocument is DELETE /api/<path>/{id}?base=: it deletes the document, which must be at the version of base.
func (s *Server) deleteDocument(d *docKind) routeFunc {
	return func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		c, ok := s.beginChange(w, r, p, d, d.remove, queryForms{"base": isBase}, "base")
		if !ok {
			return
		}
		s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
			op := edit.Op{By: p.Account.ID, ID: c.opID, Check: d.remove.check(p, c.id, c.named)}
			res, err := s.editor.Delete(ctx, op, d.kind, c.id, baseOf(c.query))
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, nil, writeResult(out, res)
		})
	}
}

// showSettings is GET /api/settings: the settings document, 404 before the site has one.
func (s *Server) showSettings(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	s.readOne(w, r, p, store.SettingsKind, "")
}

// saveSettings is PUT /api/settings: it saves the document in the body as the next version of the settings, which must be at
// the version of base in the body, 0 if the site has none yet.
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request, p account.Principal) {
	named := audit.Target{Kind: audit.KindSettings}
	if !s.allow(w, r, p, account.EditSettings, account.Target{}, named) {
		return
	}
	opID, ok := operationID(w, r)
	if !ok {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	s.heavyWork(w, r, p, documentBodyLimit, func(ctx context.Context, in, out *spool) (int, http.Header, error) {
		base, document, err := readSave(in)
		if err != nil {
			return 0, nil, err
		}
		op := edit.Op{By: p.Account.ID, ID: opID, Check: inUpdate(p, named, nil, account.EditSettings)}
		res, err := s.editor.SaveSettings(ctx, op, base, document)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, nil, writeResult(out, res)
	})
}
