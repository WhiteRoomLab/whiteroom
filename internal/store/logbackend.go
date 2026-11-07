package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The log backend keeps a store in the engine of internal/logdb (3.5), in logdb/ of its data directory, beside files/.
// The tables of its schema, logSchemaVersion:
//
//	meta (1)            "schema", "versions", "revisions", "numbers" -> uvarint: the schema; the last seq of a
//	                    version, of a revision; the largest number that an entry has had (none: 0)
//	documents (2)       kind 0x00 id -> the current version, as in history
//	history (3)         kind 0x00 id 0x00 seq (8 bytes, big-endian) -> version
//	revisions (4)       revision ID -> revision
//	entryRevisions (5)  entry ID 0x00 seq (8 bytes, big-endian) -> revision ID
//	summaries (6)       kind 0x00 id -> the JSON of the summary of the current document (index.go)
//	references (7)      target ID 0x00 kind 0x00 id 0x00 role -> nothing: the document of kind with id refers to
//	                    the target, as role says
//	operations (8)      operation ID -> the data that the caller of PutOperation keeps
//
// A version is its seq in a uvarint, then the SHA-256 of its JSON (no bytes for a deletion), saved_at and the ID of the
// user, each after its length in a uvarint. A revision is its seq in a uvarint, then the entry ID after its length, the
// SHA-256 of its JSON (32 bytes), and saved_at and the ID of the user after their lengths.
//
// The schema 1, which the tables 1 to 5 make, has no index: Open builds it, and sets the schema 2 (upgradeLog).
const logSchemaVersion = 2

// indexSchema is the first schema that has the index tables.
const indexSchema = 2

const (
	tableMeta uint32 = iota + 1
	tableDocuments
	tableHistory
	tableRevisions
	tableEntryRevisions
	tableSummaries
	tableReferences
	tableOperations
)

var tableNames = map[uint32]string{
	tableMeta: "meta", tableDocuments: "documents", tableHistory: "history", tableRevisions: "revisions",
	tableEntryRevisions: "entryRevisions", tableSummaries: "summaries", tableReferences: "references",
	tableOperations: "operations",
}

// indexTableIDs are the tables of the engine that keep the index tables.
var indexTableIDs = [numIndexTables]uint32{
	indexSummaries: tableSummaries, indexReferences: tableReferences, indexOperations: tableOperations,
}

// The keys of meta.
const (
	metaSchema    = "schema"
	metaVersions  = "versions"
	metaRevisions = "revisions"
	metaNumbers   = "numbers"
)

// logBackend is the backend of the engine in logdb/.
type logBackend struct {
	db   *logdb.DB
	fsys vfs.FS // logdb/, which db does not close
}

// createLog makes the engine with o in logdb/ of root, which is empty, and puts the schema in meta.
func createLog(root vfs.FS, o logdb.Options) (*logBackend, error) {
	fsys, err := root.Sub("logdb")
	if err != nil {
		return nil, err
	}
	d, err := logdb.Create(fsys, o)
	if err == nil {
		if err = d.Update(context.Background(), func(tx *logdb.Tx) error {
			return tx.Put(tableMeta, []byte(metaSchema), binary.AppendUvarint(nil, logSchemaVersion))
		}); err != nil {
			_ = d.Close()
		}
	}
	if err != nil {
		_ = fsys.Close()
		return nil, err
	}
	return &logBackend{db: d, fsys: fsys}, nil
}

// openLog opens the engine in logdb/ of root with o: as its writer, or as a reader in another process if reader is
// true.
func openLog(root vfs.FS, o logdb.Options, reader bool) (*logBackend, error) {
	fsys, err := root.Sub("logdb")
	if err != nil {
		return nil, err
	}
	openFS := logdb.Open
	if reader {
		openFS = logdb.OpenReader
	}
	d, err := openFS(fsys, o)
	if err != nil {
		_ = fsys.Close()
		return nil, err
	}
	return &logBackend{db: d, fsys: fsys}, nil
}

// checkSchema returns the Accept of openLog for the store whose logdb/ is at name: it refuses a store without the
// schema, or of a newer one, before the engine changes a file.
func checkSchema(name string) func(*logdb.Tx) error {
	return func(tx *logdb.Tx) error {
		v, ok := tx.Get(tableMeta, []byte(metaSchema))
		if !ok {
			return fmt.Errorf("%s: not a store of whiteroom (no schema version)", name)
		}
		switch version, ok := decodeUint(v); {
		case !ok || version == 0:
			return fmt.Errorf("%s: %w", name, invalidValue(tableMeta, []byte(metaSchema)))
		case version > logSchemaVersion:
			return fmt.Errorf("%s: the store schema is of version %d, newer than this program knows (%d)", name,
				version, logSchemaVersion)
		}
		return nil
	}
}

// upgradeLog brings the store of b, which its writer has just opened, to the schema logSchemaVersion: a store of the
// schema 1 gets its index, and the schema 2, in one Update, so that a failure leaves the store as it was. name is that
// of its logdb/ in errors.
func upgradeLog(b *logBackend, name string) error {
	upgrade := false
	if err := b.db.View(context.Background(), func(tx *logdb.Tx) error {
		v, _ := tx.Get(tableMeta, []byte(metaSchema))
		version, ok := decodeUint(v)
		upgrade = ok && version < indexSchema
		return nil
	}); err != nil || !upgrade {
		return err
	}
	if err := b.db.Update(context.Background(), func(tx *logdb.Tx) error {
		// The schema first, so that the index methods find the schema that they need.
		if err := tx.Put(tableMeta, []byte(metaSchema), binary.AppendUvarint(nil, indexSchema)); err != nil {
			return err
		}
		return (&Tx{b: &logTx{tx: tx}}).run(func(t *Tx) error { return t.RebuildIndex() })
	}); err != nil {
		return fmt.Errorf("%s: cannot upgrade the table schema to %d, so the store is unchanged: %w", name, indexSchema,
			err)
	}
	return nil
}

func (b *logBackend) view(ctx context.Context, f func(backendTx) error) error {
	return b.db.View(ctx, func(tx *logdb.Tx) error { return f(&logTx{tx: tx}) })
}

func (b *logBackend) update(ctx context.Context, f func(backendTx) error) error {
	return b.db.Update(ctx, func(tx *logdb.Tx) error { return f(&logTx{tx: tx}) })
}

func (b *logBackend) close() error {
	return errors.Join(b.db.Close(), b.fsys.Close())
}

type logTx struct {
	tx *logdb.Tx
}

func (t *logTx) current(kind content.RefKind, id string) (Version, bool, error) {
	key := docKey(kind, id)
	v, ok := t.tx.Get(tableDocuments, key)
	if !ok {
		return Version{}, false, nil
	}
	ver, err := decodeCurrent(key, v, kind, id)
	if err != nil {
		return Version{}, false, err
	}
	return ver, true, nil
}

func (t *logTx) addObject(hash string, data []byte) error {
	want, err := digest(hash)
	if err != nil {
		return err
	}
	got, err := t.tx.PutObject(data)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("object %s: the data has the hash %s", hash, hashText(got[:]))
	}
	return nil
}

func (t *logTx) addVersion(kind content.RefKind, id, hash, savedAt, by string) (int64, error) {
	var sum []byte // none for a deletion
	if hash != "" {
		d, err := digest(hash)
		if err != nil {
			return 0, err
		}
		sum = d[:]
	}
	seq, err := t.next(metaVersions)
	if err != nil {
		return 0, err
	}
	if err := t.tx.Put(tableHistory, historyKey(kind, id, seq), appendVersion(seq, sum, savedAt, by)); err != nil {
		return 0, err
	}
	return seq, nil
}

func (t *logTx) setCurrent(kind content.RefKind, id string, seq int64) error {
	key := historyKey(kind, id, seq)
	v, ok := t.tx.Get(tableHistory, key)
	if !ok {
		return fmt.Errorf("%s %q: missing", tableNames[tableHistory], key)
	}
	return t.tx.Put(tableDocuments, docKey(kind, id), v)
}

func (t *logTx) deleteCurrent(kind content.RefKind, id string) error {
	return t.tx.Delete(tableDocuments, docKey(kind, id))
}

func (t *logTx) document(kind content.RefKind, id string) (Document, bool, error) {
	v, ok, err := t.current(kind, id)
	if err != nil || !ok {
		return Document{}, false, err
	}
	data, err := t.data(tableDocuments, docKey(kind, id), v.Hash)
	if err != nil {
		return Document{}, false, err
	}
	return Document{Version: v, Data: data}, true, nil
}

func (t *logTx) documents(kind content.RefKind) ([]Document, error) {
	prefix := docKey(kind, "")
	var docs []Document
	var err error
	t.tx.Scan(tableDocuments, prefix, func(key, value []byte) bool {
		var v Version
		if v, err = decodeCurrent(key, value, kind, string(key[len(prefix):])); err != nil {
			return false
		}
		var data []byte
		if data, err = t.data(tableDocuments, key, v.Hash); err != nil {
			return false
		}
		docs = append(docs, Document{Version: v, Data: data})
		return true
	})
	return docs, err
}

func (t *logTx) history(kind content.RefKind, id string) ([]Version, error) {
	var versions []Version
	var err error
	t.tx.Scan(tableHistory, append(docKey(kind, id), 0), func(key, value []byte) bool {
		var v Version
		if v, err = decodeVersion(tableHistory, key, value, kind, id); err != nil {
			return false
		}
		versions = append(versions, v)
		return true
	})
	if err != nil {
		return nil, err
	}
	return versions, nil
}

// object returns false only if the store does not have the object: an object whose data cannot be read is an error.
func (t *logTx) object(hash string) ([]byte, bool, error) {
	d, err := digest(hash)
	if err != nil {
		return nil, false, err
	}
	if !t.tx.HasObject(d) {
		return nil, false, nil
	}
	data, err := t.tx.Object(d)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// data returns the data of the object hash, which the value of key in table names, and which the store must have.
func (t *logTx) data(table uint32, key []byte, hash string) ([]byte, error) {
	data, ok, err := t.object(hash)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return nil, fmt.Errorf("%s %q: the object %s is missing", tableNames[table], key, hash)
	}
	return data, nil
}

func (t *logTx) revisionExists(id string) (bool, error) {
	_, ok := t.tx.Get(tableRevisions, []byte(id))
	return ok, nil
}

func (t *logTx) addRevision(id, entry, hash, savedAt, by string) (int64, error) {
	sum, err := digest(hash)
	if err != nil {
		return 0, err
	}
	seq, err := t.next(metaRevisions)
	if err != nil {
		return 0, err
	}
	if err := t.tx.Put(tableRevisions, []byte(id), appendRevision(seq, entry, sum, savedAt, by)); err != nil {
		return 0, err
	}
	if err := t.tx.Put(tableEntryRevisions, entryKey(entry, seq), []byte(id)); err != nil {
		return 0, err
	}
	return seq, nil
}

func (t *logTx) revision(id string) (Revision, bool, error) {
	r, ok, err := t.revisionInfo(id)
	if err != nil || !ok {
		return Revision{}, false, err
	}
	if r.Data, err = t.data(tableRevisions, []byte(id), r.Hash); err != nil {
		return Revision{}, false, err
	}
	return r, true, nil
}

// revisionInfo returns the revision with id without Data.
func (t *logTx) revisionInfo(id string) (Revision, bool, error) {
	v, ok := t.tx.Get(tableRevisions, []byte(id))
	if !ok {
		return Revision{}, false, nil
	}
	r, err := decodeRevision(id, v)
	if err != nil {
		return Revision{}, false, err
	}
	return r, true, nil
}

func (t *logTx) revisions(entry string) ([]Revision, error) {
	var revs []Revision
	var err error
	t.tx.Scan(tableEntryRevisions, append([]byte(entry), 0), func(key, id []byte) bool {
		var r Revision
		var ok bool
		if r, ok, err = t.revisionInfo(string(id)); err == nil && !ok {
			err = fmt.Errorf("%s %q: the revision %s is missing", tableNames[tableEntryRevisions], key, id)
		}
		if err != nil {
			return false
		}
		revs = append(revs, r)
		return true
	})
	if err != nil {
		return nil, err
	}
	return revs, nil
}

func (t *logTx) addEvent(data []byte) error {
	return t.tx.AppendEvent(data)
}

func (t *logTx) indexGet(table indexTable, key []byte) ([]byte, bool, error) {
	v, ok := t.tx.Get(indexTableIDs[table], key)
	return v, ok, nil
}

func (t *logTx) indexPut(table indexTable, key, value []byte) error {
	return t.tx.Put(indexTableIDs[table], key, value)
}

func (t *logTx) indexDelete(table indexTable, key []byte) error {
	return t.tx.Delete(indexTableIDs[table], key)
}

func (t *logTx) indexScan(table indexTable, prefix []byte, fn func(key, value []byte) bool) error {
	t.tx.Scan(indexTableIDs[table], prefix, fn)
	return nil
}

func (t *logTx) indexReady() (bool, error) {
	v, ok := t.tx.Get(tableMeta, []byte(metaSchema))
	version, valid := decodeUint(v)
	if !ok || !valid {
		return false, invalidValue(tableMeta, []byte(metaSchema))
	}
	return version >= indexSchema, nil
}

func (t *logTx) lastNumber() (int64, error) {
	v, ok := t.tx.Get(tableMeta, []byte(metaNumbers))
	if !ok {
		return 0, nil
	}
	n, valid := decodeUint(v)
	if !valid || n > math.MaxInt64 {
		return 0, invalidValue(tableMeta, []byte(metaNumbers))
	}
	return int64(n), nil
}

func (t *logTx) setLastNumber(n int64) error {
	return t.tx.Put(tableMeta, []byte(metaNumbers), binary.AppendUvarint(nil, uint64(n)))
}

// next returns the seq of the next version or revision, one more than the last one, whose seq meta has at key, and
// puts it there.
func (t *logTx) next(key string) (int64, error) {
	var last uint64
	if v, ok := t.tx.Get(tableMeta, []byte(key)); ok {
		if last, ok = decodeUint(v); !ok || last == math.MaxInt64 {
			return 0, invalidValue(tableMeta, []byte(key))
		}
	}
	if err := t.tx.Put(tableMeta, []byte(key), binary.AppendUvarint(nil, last+1)); err != nil {
		return 0, err
	}
	return int64(last + 1), nil
}

// docKey is the key of the document of kind with id in documents; with 0x00 after it, that of its versions in history.
func docKey(kind content.RefKind, id string) []byte {
	return append(append([]byte(kind), 0), id...)
}

func historyKey(kind content.RefKind, id string, seq int64) []byte {
	return binary.BigEndian.AppendUint64(append(docKey(kind, id), 0), uint64(seq))
}

// entryKey is the key of the revision seq of the entry entry in entryRevisions.
func entryKey(entry string, seq int64) []byte {
	return binary.BigEndian.AppendUint64(append([]byte(entry), 0), uint64(seq))
}

// appendField appends b after its length in a uvarint.
func appendField(dst, b []byte) []byte {
	return append(binary.AppendUvarint(dst, uint64(len(b))), b...)
}

// appendVersion returns the value of a version, whose JSON has the SHA-256 sum, or which is a deletion if sum is nil.
func appendVersion(seq int64, sum []byte, savedAt, by string) []byte {
	b := binary.AppendUvarint(nil, uint64(seq))
	b = appendField(b, sum)
	b = appendField(b, []byte(savedAt))
	return appendField(b, []byte(by))
}

// appendRevision returns the value of a revision, whose JSON has the SHA-256 sum.
func appendRevision(seq int64, entry string, sum [sha256.Size]byte, savedAt, by string) []byte {
	b := binary.AppendUvarint(nil, uint64(seq))
	b = appendField(b, []byte(entry))
	b = append(b, sum[:]...)
	b = appendField(b, []byte(savedAt))
	return appendField(b, []byte(by))
}

// valueReader reads a value of the tables. What it cannot read makes it bad.
type valueReader struct {
	b   []byte
	bad bool
}

func (r *valueReader) uvarint() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.bad = true
		return 0
	}
	r.b = r.b[n:]
	return v
}

// seq reads a seq, a uvarint that an int64 holds.
func (r *valueReader) seq() int64 {
	v := r.uvarint()
	if v > math.MaxInt64 {
		r.bad = true
	}
	return int64(v)
}

func (r *valueReader) bytes(n uint64) []byte {
	if n > uint64(len(r.b)) {
		r.bad = true
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

// field reads the bytes of appendField.
func (r *valueReader) field() []byte {
	return r.bytes(r.uvarint())
}

// done reports whether r has read the value to its end without a problem.
func (r *valueReader) done() bool {
	return !r.bad && len(r.b) == 0
}

// decodeUint decodes v, a uvarint, which a value of meta is.
func decodeUint(v []byte) (uint64, bool) {
	r := valueReader{b: v}
	n := r.uvarint()
	return n, r.done()
}

// decodeVersion decodes v, the value of key in table, a version of the document of kind with id.
func decodeVersion(table uint32, key, v []byte, kind content.RefKind, id string) (Version, error) {
	r := valueReader{b: v}
	seq := r.seq()
	sum := r.field()
	savedAt := r.field()
	by := r.field()
	if !r.done() || len(sum) != 0 && len(sum) != sha256.Size {
		return Version{}, invalidValue(table, key)
	}
	at, err := parseSavedAt(string(savedAt))
	if err != nil {
		return Version{}, versionErr(seq, err)
	}
	ver := Version{Seq: seq, Kind: kind, ID: id, SavedAt: at, SavedBy: string(by)}
	if len(sum) > 0 {
		ver.Hash = hashText(sum)
	}
	return ver, nil
}

// decodeCurrent decodes v, the value of key in documents, the current version of the document of kind with id, which
// cannot be a deletion.
func decodeCurrent(key, v []byte, kind content.RefKind, id string) (Version, error) {
	ver, err := decodeVersion(tableDocuments, key, v, kind, id)
	if err == nil && ver.Hash == "" {
		return Version{}, invalidValue(tableDocuments, key)
	}
	return ver, err
}

// decodeRevision decodes v, the value of the revision with id in revisions.
func decodeRevision(id string, v []byte) (Revision, error) {
	r := valueReader{b: v}
	seq := r.seq()
	entry := r.field()
	sum := r.bytes(sha256.Size)
	savedAt := r.field()
	by := r.field()
	if !r.done() {
		return Revision{}, invalidValue(tableRevisions, []byte(id))
	}
	at, err := parseSavedAt(string(savedAt))
	if err != nil {
		return Revision{}, revisionErr(id, err)
	}
	return Revision{Seq: seq, ID: id, Entry: string(entry), Hash: hashText(sum), SavedAt: at, SavedBy: string(by)}, nil
}

// invalidValue is the error of the value of key in table, which the backend cannot read.
func invalidValue(table uint32, key []byte) error {
	return fmt.Errorf("%s %q: invalid value", tableNames[table], key)
}

// digest returns the SHA-256 of hash, which Version.Hash and Revision.Hash have.
func digest(hash string) ([sha256.Size]byte, error) {
	var d [sha256.Size]byte
	if err := checkHash(hash); err != nil {
		return d, err
	}
	_, err := hex.Decode(d[:], []byte(strings.TrimPrefix(hash, "sha256:")))
	return d, err
}

// hashText returns the hash of Version.Hash and Revision.Hash of the SHA-256 sum.
func hashText(sum []byte) string {
	return "sha256:" + hex.EncodeToString(sum)
}

// backupLog copies the data directory srcDir of a store of the log backend, whose writer may go on writing, to dstDir,
// which must be missing or an empty directory: logdb/ as it is at the last commit that is durable when backupLog
// begins (logdb.Backup), and then files/, which has the originals of that commit by then, while the engine keeps
// maintenance away. If it fails, dstDir must be emptied before it can try again.
func backupLog(ctx context.Context, srcDir, dstDir string) error {
	return backup(ctx, srcDir, nil, dstDir, nil)
}

// backup is backupLog of the data directories src and dst, or of srcDir and dstDir on the OS if they are nil; srcDir
// and dstDir name them in errors.
func backup(ctx context.Context, srcDir string, src vfs.FS, dstDir string, dst vfs.FS) error {
	if err := newDataDir(dstDir, dst); err != nil {
		return err
	}
	src, doneSrc, err := dataRoot(srcDir, src)
	if err != nil {
		return err
	}
	defer doneSrc()
	if err := checkDir(src, srcDir, "logdb"); err != nil {
		return err
	}
	if err := checkDir(src, srcDir, "files"); err != nil {
		return err
	}
	srcLog, err := src.Sub("logdb")
	if err != nil {
		return err
	}
	defer func() { _ = srcLog.Close() }()
	dst, doneDst, err := dataRoot(dstDir, dst)
	if err != nil {
		return err
	}
	defer doneDst()
	for _, name := range []string{"files", "logdb"} {
		if err := dst.Mkdir(name, 0o700); err != nil {
			return err
		}
	}
	dstLog, err := dst.Sub("logdb")
	if err != nil {
		return err
	}
	defer func() { _ = dstLog.Close() }()
	if _, err := logdb.Backup(ctx, srcLog, dstLog, func(logdb.Position) error {
		return copyFiles(ctx, src, dst, "files")
	}); err != nil {
		return err
	}
	return dst.SyncDir(".")
}

// copyFiles copies the directory dir of src, with every directory and regular file in it but the temporary files of
// PutOriginal, to dst, which has dir. It syncs each file, and each directory after what it has copied in it.
func copyFiles(ctx context.Context, src, dst vfs.FS, dir string) error {
	entries, err := src.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := path.Join(dir, e.Name())
		switch {
		case strings.HasPrefix(e.Name(), ".tmp-"):
		case e.IsDir():
			if err := dst.Mkdir(name, 0o700); err != nil {
				return err
			}
			if err := copyFiles(ctx, src, dst, name); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := copyFile(src, dst, name); err != nil {
				return err
			}
		}
	}
	return dst.SyncDir(dir)
}

// copyFile copies the file name of src to dst, and syncs it.
func copyFile(src, dst vfs.FS, name string) error {
	r, err := src.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	w, err := dst.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if err == nil {
		err = w.Sync()
	}
	return errors.Join(err, w.Close())
}
