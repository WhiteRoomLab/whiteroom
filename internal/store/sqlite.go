package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/db"
)

// sqliteBackend is the backend of the SQLite database whiteroom.db (internal/db).
type sqliteBackend struct {
	db *db.DB
}

func (b *sqliteBackend) view(ctx context.Context, f func(backendTx) error) error {
	return b.db.Read(ctx, func(tx *sql.Tx) error { return f(&sqliteTx{tx: tx, ctx: ctx}) })
}

func (b *sqliteBackend) update(ctx context.Context, f func(backendTx) error) error {
	return b.db.Write(ctx, func(tx *sql.Tx) error { return f(&sqliteTx{tx: tx, ctx: ctx}) })
}

func (b *sqliteBackend) close() error {
	return b.db.Close()
}

type sqliteTx struct {
	tx  *sql.Tx
	ctx context.Context
}

// versionColumns are the columns of a version, v, that scanVersion reads.
const versionColumns = "v.id, v.seq, v.object, v.saved_at, v.saved_by"

// documentQuery selects the current versions of the documents, d, with their JSON, for scanDocument.
const documentQuery = "SELECT " + versionColumns + ", o.data FROM documents AS d " +
	"JOIN versions AS v ON v.seq = d.version JOIN objects AS o ON o.hash = v.object"

// revisionColumns are the columns of a revision, r, that scanRevision reads.
const revisionColumns = "r.seq, r.id, r.entry, r.object, r.saved_at, r.saved_by"

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
		return Version{}, versionErr(v.Seq, err)
	}
	v.Hash = object.String
	return v, nil
}

// scanDocument reads a document of kind from a row of documentQuery.
func scanDocument(row scanner, kind content.RefKind) (Document, error) {
	var d Document
	var err error
	if d.Version, err = scanVersion(row, kind, &d.Data); err != nil {
		return Document{}, err
	}
	return d, nil
}

// scanRevision reads a revision from the columns of revisionColumns of row, and the columns after them into more.
func scanRevision(row scanner, more ...any) (Revision, error) {
	var r Revision
	var savedAt string
	if err := row.Scan(append([]any{&r.Seq, &r.ID, &r.Entry, &r.Hash, &savedAt, &r.SavedBy}, more...)...); err != nil {
		return Revision{}, err
	}
	var err error
	if r.SavedAt, err = parseSavedAt(savedAt); err != nil {
		return Revision{}, revisionErr(r.ID, err)
	}
	return r, nil
}

func (t *sqliteTx) current(kind content.RefKind, id string) (Version, bool, error) {
	const q = "SELECT " + versionColumns + " FROM documents AS d JOIN versions AS v ON v.seq = d.version " +
		"WHERE d.kind = ? AND d.id = ?"
	v, err := scanVersion(t.tx.QueryRowContext(t.ctx, q, kind, id), kind)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Version{}, false, nil
	case err != nil:
		return Version{}, false, err
	}
	return v, true, nil
}

func (t *sqliteTx) addObject(hash string, data []byte) error {
	_, err := t.tx.ExecContext(t.ctx, "INSERT OR IGNORE INTO objects (hash, data) VALUES (?, ?)", hash, data)
	return err
}

func (t *sqliteTx) addVersion(kind content.RefKind, id, hash, savedAt, by string) (int64, error) {
	const q = "INSERT INTO versions (kind, id, object, saved_at, saved_by) VALUES (?, ?, ?, ?, ?)"
	res, err := t.tx.ExecContext(t.ctx, q, kind, id, sql.NullString{String: hash, Valid: hash != ""}, savedAt, by)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (t *sqliteTx) setCurrent(kind content.RefKind, id string, seq int64) error {
	const q = "INSERT INTO documents (kind, id, version) VALUES (?, ?, ?) " +
		"ON CONFLICT (kind, id) DO UPDATE SET version = excluded.version"
	_, err := t.tx.ExecContext(t.ctx, q, kind, id, seq)
	return err
}

func (t *sqliteTx) deleteCurrent(kind content.RefKind, id string) error {
	_, err := t.tx.ExecContext(t.ctx, "DELETE FROM documents WHERE kind = ? AND id = ?", kind, id)
	return err
}

func (t *sqliteTx) document(kind content.RefKind, id string) (Document, bool, error) {
	row := t.tx.QueryRowContext(t.ctx, documentQuery+" WHERE d.kind = ? AND d.id = ?", kind, id)
	d, err := scanDocument(row, kind)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Document{}, false, nil
	case err != nil:
		return Document{}, false, err
	}
	return d, true, nil
}

func (t *sqliteTx) documents(kind content.RefKind) ([]Document, error) {
	rows, err := t.tx.QueryContext(t.ctx, documentQuery+" WHERE d.kind = ? ORDER BY d.id", kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var docs []Document
	for rows.Next() {
		d, err := scanDocument(rows, kind)
		if err != nil {
			return docs, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

func (t *sqliteTx) history(kind content.RefKind, id string) ([]Version, error) {
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

func (t *sqliteTx) object(hash string) ([]byte, bool, error) {
	var data []byte
	err := t.tx.QueryRowContext(t.ctx, "SELECT data FROM objects WHERE hash = ?", hash).Scan(&data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return data, true, nil
}

func (t *sqliteTx) revisionExists(id string) (bool, error) {
	var exists bool
	const q = "SELECT EXISTS (SELECT 1 FROM revisions WHERE id = ?)"
	if err := t.tx.QueryRowContext(t.ctx, q, id).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (t *sqliteTx) addRevision(id, entry, hash, savedAt, by string) (int64, error) {
	const insert = "INSERT INTO revisions (id, entry, object, saved_at, saved_by) VALUES (?, ?, ?, ?, ?)"
	res, err := t.tx.ExecContext(t.ctx, insert, id, entry, hash, savedAt, by)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (t *sqliteTx) revision(id string) (Revision, bool, error) {
	const q = "SELECT " + revisionColumns + ", o.data FROM revisions AS r JOIN objects AS o ON o.hash = r.object " +
		"WHERE r.id = ?"
	var data []byte
	r, err := scanRevision(t.tx.QueryRowContext(t.ctx, q, id), &data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Revision{}, false, nil
	case err != nil:
		return Revision{}, false, err
	}
	r.Data = data
	return r, true, nil
}

func (t *sqliteTx) revisions(entry string) ([]Revision, error) {
	const q = "SELECT " + revisionColumns + " FROM revisions AS r WHERE r.entry = ? ORDER BY r.seq"
	rows, err := t.tx.QueryContext(t.ctx, q, entry)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var revs []Revision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return revs, nil
}

func (t *sqliteTx) addEvent([]byte) error {
	return errNoEvents
}
