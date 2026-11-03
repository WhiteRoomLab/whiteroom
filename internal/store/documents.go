package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Version is a saved version of a document.
type Version struct {
	Seq     int64           // the order of saving in the store
	Kind    content.RefKind // a kind of build.Source.Documents, or SettingsKind
	ID      string          // "" for the settings document
	Hash    string          // of the JSON, as Object takes it; "" for a deletion
	SavedAt time.Time       // in UTC
	SavedBy string          // the ID of the user (usr_)
}

// Document is a version of a document with its JSON.
type Document struct {
	Version
	Data []byte
}

// decoders decode the documents of each kind as the build does, and return the id in a document; "" if it has none.
var decoders = map[content.RefKind]func(data []byte) (id string, err error){
	content.RefEntry:    decoder(func(e *content.Entry) string { return e.ID }),
	content.RefTerm:     decoder(func(t *content.Term) string { return t.ID }),
	content.RefMedia:    decoder(func(m *content.Media) string { return m.ID }),
	content.RefAuthor:   decoder(func(a *content.AuthorProfile) string { return a.ID }),
	content.RefMenu:     decoder(func(m *content.Menu) string { return m.ID }),
	content.RefForm:     decoder(func(f *content.Form) string { return f.ID }),
	content.RefRedirect: decoder(func(r *content.Redirect) string { return r.ID }),
	SettingsKind:        func(data []byte) (string, error) { return "", new(content.Site).UnmarshalSettings(data) },
}

// decoder returns the decoder of the documents of type T, whose id returns the id in a document.
func decoder[T any, P interface {
	*T
	UnmarshalJSON(data []byte) error
}](id func(*T) string) func([]byte) (string, error) {
	return func(data []byte) (string, error) {
		var v T
		if err := P(&v).UnmarshalJSON(data); err != nil {
			return "", err
		}
		return id(&v), nil
	}
}

func checkKind(kind content.RefKind) error {
	if _, ok := decoders[kind]; !ok {
		return fmt.Errorf("unsupported kind %q", kind)
	}
	return nil
}

// checkKey checks kind and id, which name a document.
func checkKey(kind content.RefKind, id string) error {
	if err := checkKind(kind); err != nil {
		return err
	}
	if kind != SettingsKind {
		return content.CheckID(kind, id)
	}
	if id != "" {
		return errors.New("the settings document has no ID")
	}
	return nil
}

// name returns the name of the document of kind with id in errors, that of its file in a site directory (4.3).
func name(kind content.RefKind, id string) string {
	if kind == SettingsKind {
		return "site/settings.json"
	}
	return content.DocumentDir(kind) + "/" + id + ".json"
}

// checkData checks that data, the JSON of the document of kind with id, decodes as the build decodes it, without
// validating it against the site settings. The error is content.Errors.
func checkData(kind content.RefKind, id string, data []byte) error {
	f := content.SiteFile{Name: name(kind, id), Data: data}
	if len(data) > content.MaxDocumentBytes {
		return content.Errors{{
			File: f.Name,
			Msg:  fmt.Sprintf("too large: %d bytes (max %d)", len(data), content.MaxDocumentBytes),
		}}
	}
	var got string
	if err := content.DecodeFile(f, func(b []byte) (err error) {
		got, err = decoders[kind](b)
		return err
	}); err != nil {
		return err
	}
	if got != "" && got != id {
		msg := fmt.Sprintf("id %q differs from the ID of the document, %q", got, id)
		return content.LocateErrors(&content.Error{Path: "/id", Msg: msg}, f)
	}
	return nil
}

// Put saves data, the JSON of the document of kind with id, as its current version by the user by, and returns the
// version. data must decode as a document of kind, but is not validated against the site settings. If data is that of
// the current version, Put returns that version, and saves nothing.
func (t *Tx) Put(kind content.RefKind, id string, data []byte, by string) (Version, error) {
	if err := t.check(true); err != nil {
		return Version{}, err
	}
	if err := checkKey(kind, id); err != nil {
		return Version{}, err
	}
	if err := content.CheckID(content.RefUser, by); err != nil {
		return Version{}, err
	}
	if err := checkData(kind, id, data); err != nil {
		return Version{}, err
	}
	hash := hashOf(data)
	cur, err := t.current(kind, id)
	switch {
	case err == nil && cur.Hash == hash:
		return cur, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return Version{}, err
	}
	if err := t.addObject(hash, data); err != nil {
		return Version{}, err
	}
	v, err := t.addVersion(kind, id, hash, by)
	if err != nil {
		return Version{}, err
	}
	const q = "INSERT INTO documents (kind, id, version) VALUES (?, ?, ?) " +
		"ON CONFLICT (kind, id) DO UPDATE SET version = excluded.version"
	if _, err := t.tx.ExecContext(t.ctx, q, kind, id, v.Seq); err != nil {
		return Version{}, err
	}
	return v, nil
}

// Delete deletes the document of kind with id by the user by: it saves a version that marks the deletion, and returns
// it. History keeps the versions before it. If the document does not exist, the error wraps fs.ErrNotExist. The
// settings document cannot be deleted.
func (t *Tx) Delete(kind content.RefKind, id, by string) (Version, error) {
	if err := t.check(true); err != nil {
		return Version{}, err
	}
	if err := checkKey(kind, id); err != nil {
		return Version{}, err
	}
	if kind == SettingsKind {
		return Version{}, errors.New("the settings document cannot be deleted")
	}
	if err := content.CheckID(content.RefUser, by); err != nil {
		return Version{}, err
	}
	if _, err := t.current(kind, id); err != nil {
		return Version{}, err
	}
	v, err := t.addVersion(kind, id, "", by)
	if err != nil {
		return Version{}, err
	}
	if _, err := t.tx.ExecContext(t.ctx, "DELETE FROM documents WHERE kind = ? AND id = ?", kind, id); err != nil {
		return Version{}, err
	}
	return v, nil
}

// addObject saves data, whose hash is hash, unless the store has it.
func (t *Tx) addObject(hash string, data []byte) error {
	_, err := t.tx.ExecContext(t.ctx, "INSERT OR IGNORE INTO objects (hash, data) VALUES (?, ?)", hash, data)
	return err
}

// addVersion saves a version of the document of kind with id by the user by: that of the object hash, or a deletion if
// hash is "".
func (t *Tx) addVersion(kind content.RefKind, id, hash, by string) (Version, error) {
	const q = "INSERT INTO versions (kind, id, object, saved_at, saved_by) VALUES (?, ?, ?, ?, ?)"
	res, err := t.tx.ExecContext(t.ctx, q, kind, id, sql.NullString{String: hash, Valid: hash != ""}, t.savedAt, by)
	if err != nil {
		return Version{}, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return Version{}, err
	}
	savedAt, err := parseSavedAt(t.savedAt)
	if err != nil {
		return Version{}, err
	}
	return Version{Seq: seq, Kind: kind, ID: id, Hash: hash, SavedAt: savedAt, SavedBy: by}, nil
}

// versionColumns are the columns of a version, v, that scanVersion reads.
const versionColumns = "v.id, v.seq, v.object, v.saved_at, v.saved_by"

// documentQuery selects the current versions of the documents, d, with their JSON, for scanDocument.
const documentQuery = "SELECT " + versionColumns + ", o.data FROM documents AS d " +
	"JOIN versions AS v ON v.seq = d.version JOIN objects AS o ON o.hash = v.object"

// scanner is a *sql.Row or *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanVersion reads a version of kind from the columns of versionColumns of row, and the columns after them into more.
func scanVersion(row scanner, kind content.RefKind, more ...any) (Version, error) {
	v := Version{Kind: kind}
	var object sql.NullString
	var savedAt string
	if err := row.Scan(append([]any{&v.ID, &v.Seq, &object, &savedAt, &v.SavedBy}, more...)...); err != nil {
		return Version{}, err
	}
	var err error
	if v.SavedAt, err = parseSavedAt(savedAt); err != nil {
		return Version{}, fmt.Errorf("version %d: %w", v.Seq, err)
	}
	v.Hash = object.String
	return v, nil
}

// scanDocument reads a document of kind from a row of documentQuery, and checks its JSON against its hash.
func scanDocument(row scanner, kind content.RefKind) (Document, error) {
	var d Document
	var err error
	if d.Version, err = scanVersion(row, kind, &d.Data); err != nil {
		return Document{}, err
	}
	if err := verify(d.Hash, d.Data); err != nil {
		return Document{}, err
	}
	return d, nil
}

func parseSavedAt(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t.UTC(), err
}

// current returns the current version of the document of kind with id. If there is none, the error wraps
// fs.ErrNotExist.
func (t *Tx) current(kind content.RefKind, id string) (Version, error) {
	const q = "SELECT " + versionColumns + " FROM documents AS d JOIN versions AS v ON v.seq = d.version " +
		"WHERE d.kind = ? AND d.id = ?"
	v, err := scanVersion(t.tx.QueryRowContext(t.ctx, q, kind, id), kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, notFoundError(name(kind, id))
	}
	return v, err
}

// Document returns the current version of the document of kind with id. If the document does not exist, the error
// wraps fs.ErrNotExist.
func (t *Tx) Document(kind content.RefKind, id string) (Document, error) {
	if err := t.check(false); err != nil {
		return Document{}, err
	}
	if err := checkKey(kind, id); err != nil {
		return Document{}, err
	}
	row := t.tx.QueryRowContext(t.ctx, documentQuery+" WHERE d.kind = ? AND d.id = ?", kind, id)
	d, err := scanDocument(row, kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, notFoundError(name(kind, id))
	}
	return d, err
}

// Documents returns the current versions of the documents of kind, in the order of their IDs.
func (t *Tx) Documents(kind content.RefKind) ([]Document, error) {
	if err := t.check(false); err != nil {
		return nil, err
	}
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(t.ctx, documentQuery+" WHERE d.kind = ? ORDER BY d.id", kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var docs []Document
	for rows.Next() {
		d, err := scanDocument(rows, kind)
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

// History returns every version of the document of kind with id, deletions included, in the order of saving; none if
// it has never been saved.
func (t *Tx) History(kind content.RefKind, id string) ([]Version, error) {
	if err := t.check(false); err != nil {
		return nil, err
	}
	if err := checkKey(kind, id); err != nil {
		return nil, err
	}
	const q = "SELECT " + versionColumns + " FROM versions AS v WHERE v.kind = ? AND v.id = ? ORDER BY v.seq"
	rows, err := t.tx.QueryContext(t.ctx, q, kind, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var versions []Version
	for rows.Next() {
		v, err := scanVersion(rows, kind)
		if err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return versions, nil
}

// Object returns the JSON of a version or a revision, whose hash is hash. If there is none, the error wraps
// fs.ErrNotExist.
func (t *Tx) Object(hash string) ([]byte, error) {
	if err := t.check(false); err != nil {
		return nil, err
	}
	if err := checkHash(hash); err != nil {
		return nil, err
	}
	var data []byte
	err := t.tx.QueryRowContext(t.ctx, "SELECT data FROM objects WHERE hash = ?", hash).Scan(&data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, notFoundError("object " + hash)
	case err != nil:
		return nil, err
	}
	if err := verify(hash, data); err != nil {
		return nil, err
	}
	return data, nil
}

// hashOf returns the hash of data: "sha256:" and the 64 lower case hex digits of its SHA-256.
func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func checkHash(hash string) error {
	digits, ok := strings.CutPrefix(hash, "sha256:")
	if !ok || len(digits) != 64 || strings.Trim(digits, "0123456789abcdef") != "" {
		return fmt.Errorf(`invalid hash %q: use "sha256:" and 64 lower case hex digits`, hash)
	}
	return nil
}

// verify checks that data, that of the object hash, has the hash.
func verify(hash string, data []byte) error {
	if got := hashOf(data); got != hash {
		return fmt.Errorf("object %s: the data has the hash %s", hash, got)
	}
	return nil
}

// notFoundError is the error for a document, a revision or an object that does not exist, by its name in errors, such
// as "entries/<ID>.json".
type notFoundError string

func (e notFoundError) Error() string { return string(e) + ": not found" }

func (notFoundError) Unwrap() error { return fs.ErrNotExist }
