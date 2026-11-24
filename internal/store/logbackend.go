package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// The log backend keeps a store in the engine of internal/logdb (3.5), which the DB daemon opens in logdb/ of its data
// directory, beside files/, and which the backend reaches through the client of internal/dbclient. The tables of its
// schema, logSchemaVersion:
//
//	meta (1)            "schema", "versions", "revisions", "numbers" -> uvarint: the schema; the last seq of a
//	                    version, of a revision; the largest number that an entry has had (none: 0)
//	                    "loginLog", "activityLog" -> the head of the chain of that log (audit.go): the number of its last
//	                    record in a uvarint, then the SHA-256 of the record as the store keeps it (32 bytes); none while
//	                    the log has no record
//	documents (2)       kind 0x00 id -> the current version, as in history
//	history (3)         kind 0x00 id 0x00 seq (8 bytes, big-endian) -> version
//	revisions (4)       revision ID -> revision
//	entryRevisions (5)  entry ID 0x00 seq (8 bytes, big-endian) -> revision ID
//	summaries (6)       kind 0x00 id -> the JSON of the summary of the current document (index.go)
//	references (7)      target ID 0x00 kind 0x00 id 0x00 role -> nothing: the document of kind with id refers to
//	                    the target, as role says
//	operations (8)      operation ID -> the data that the caller of PutOperation keeps
//	accounts (9)        user ID -> the row of a login account, which internal/account writes and the store does not read
//	sessions (10)       session key -> the row of a session, which internal/account writes and the store does not read
//	heads (11)          the name of an audit log, "login" or "activity" -> the line of the head of its chain
//	                    (audit.Head.String), which the daemon writes out without reading it; none while the log has no
//	                    record
//	chains (12)         the name of the log -> its chain start, the first record that remains after a deletion of the
//	                    oldest ones and the hash of the one before it (dbwire.ChainStart); the name, 0x00 and the number
//	                    of a record (8 bytes, big-endian) -> the break at that record that is accepted (dbwire.ChainBreak),
//	                    for as long as the record has its hash. Only the maintenance of the daemon writes them, and a store
//	                    of a schema before 8 has none
//
// A version is its seq in a uvarint, then the SHA-256 of its JSON (no bytes for a deletion), saved_at and the ID of the
// user, each after its length in a uvarint. A revision is its seq in a uvarint, then the entry ID after its length, the
// SHA-256 of its JSON (32 bytes), and saved_at and the ID of the user after their lengths.
//
// The schema 1, which the tables 1 to 5 make, has no index: Open builds it, and sets the schema logSchemaVersion
// (upgradeLog). The schema 2 has the index without the role scheduled in references and without the scheduled items in
// the entries of summaries, which the schema 3 adds; no program of the schema 2 could save an entry with them, so its
// index is that of the schema 3. The schema 4 adds the table accounts and the schema 5 the table sessions, which a
// store of an older schema has no rows of. The schema 6 adds the audit log: the heads in meta, and the audit events that
// hold its records. The schema 7 adds the table heads, and lets the daemon add its marks to the events, which the reads
// of the audit log skip (audit.go). The schema 8 adds the table chains, and the events of the daemon for the deletion
// of records and the acceptance of a break (dbwire.DeletedKind, dbwire.AcceptedKind), which the reads skip too. Open
// sets the schema 8 in a store of the schema 1 to 7 together with the record store.upgrade (the first of the activity
// log, with its head, for one of the schema 1 to 5) and the rows of heads of the logs that have a head; one of the
// schema 2 to 7 gets nothing else and has no document read, and the index of one of the schema 1 is built as before.
const logSchemaVersion = dbwire.TableVersion

// indexSchema is the first schema that has the index tables, auditSchema the first that has the audit log, and
// exportSchema the first that has the table heads.
const (
	indexSchema  = 2
	auditSchema  = 6
	exportSchema = 7
)

const (
	tableMeta uint32 = iota + 1
	tableDocuments
	tableHistory
	tableRevisions
	tableEntryRevisions
	tableSummaries
	tableReferences
	tableOperations
	tableAccounts
	tableSessions
)

const (
	tableHeads  = dbwire.TableHeads
	tableChains = dbwire.TableChains
)

var tableNames = map[uint32]string{
	tableMeta: "meta", tableDocuments: "documents", tableHistory: "history", tableRevisions: "revisions",
	tableEntryRevisions: "entryRevisions", tableSummaries: "summaries", tableReferences: "references",
	tableOperations: "operations", tableAccounts: "accounts", tableSessions: "sessions", tableHeads: "heads",
	tableChains: "chains",
}

// indexTableIDs are the tables of the engine that keep the index tables.
var indexTableIDs = [numIndexTables]uint32{
	indexSummaries: tableSummaries, indexReferences: tableReferences, indexOperations: tableOperations,
	indexAccounts: tableAccounts, indexSessions: tableSessions,
}

// The keys of meta.
const (
	metaSchema    = "schema"
	metaVersions  = "versions"
	metaRevisions = "revisions"
	metaNumbers   = "numbers"

	metaLoginLog    = "loginLog"
	metaActivityLog = "activityLog"
)

// logBackend is the backend of the daemon's engine, which the client of internal/dbclient reaches.
type logBackend struct {
	c *dbclient.Client
}

var schemaKey = []byte(metaSchema)

// createLog puts the schema in meta, with the first record of the activity log, store.create, at the time of now, in
// one commit of the store that c is connected to, which must have no commit. name is that of the daemon in errors.
func createLog(ctx context.Context, c *dbclient.Client, name string, now func() time.Time) error {
	return c.Update(ctx, func(tx *dbclient.Tx) error {
		_, ok, err := tx.Get(tableMeta, schemaKey)
		if err != nil {
			return err
		}
		if ok || tx.Seq() != 0 {
			return fmt.Errorf("%s: the store is not new; a new store needs a data directory that whiteroom-db init has just made",
				name)
		}
		if err := tx.Put(tableMeta, schemaKey, binary.AppendUvarint(nil, logSchemaVersion)); err != nil {
			return err
		}
		return recordIn(tx, now, audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.StoreCreate})
	})
}

// recordIn adds r to the audit log in tx, a transaction of the client, as Tx.Record does in an Update that begins at
// the time of now.
func recordIn(tx *dbclient.Tx, now func() time.Time, r audit.Record) error {
	t := &Tx{b: &logTx{tx: tx}, savedAt: now().UTC().Format(savedAtLayout)}
	return t.run(func(t *Tx) error { return t.Record(r) })
}

// checkSchema checks that the store of c has a schema that this program knows, and refuses one without the schema, or
// of a newer one. name is that of the daemon in errors.
func checkSchema(ctx context.Context, c *dbclient.Client, name string) error {
	return c.View(ctx, func(tx *dbclient.Tx) error {
		v, ok, err := tx.Get(tableMeta, schemaKey)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s: %w", name, ErrNoStore)
		}
		switch version, ok := decodeUint(v); {
		case !ok || version == 0:
			return fmt.Errorf("%s: %w", name, invalidValue(tableMeta, schemaKey))
		case version > logSchemaVersion:
			return fmt.Errorf("%s: the store schema is of version %d, newer than this program knows (%d)", name,
				version, logSchemaVersion)
		}
		return nil
	})
}

// upgradeLog brings the store of c, which its writer has just opened, to the schema logSchemaVersion in one Update, so
// that a failure leaves the store as it was. A store of the schema 1 gets its index, the schema 8 and the record
// store.upgrade. One of the schema 2 to 7 has the index of the schema 8 already: it gets only the schema 8, the record
// and the rows of heads, and no document, revision or object is read, so a damaged one does not stop Open, and only the
// reads that use it fail. name is that of the daemon in errors, and the record has the time of now.
func upgradeLog(ctx context.Context, c *dbclient.Client, name string, now func() time.Time) error {
	var version uint64
	upgrade := false
	if err := c.View(ctx, func(tx *dbclient.Tx) error {
		v, _, err := tx.Get(tableMeta, schemaKey)
		if err != nil {
			return err
		}
		var ok bool
		version, ok = decodeUint(v)
		upgrade = ok && version < logSchemaVersion
		return nil
	}); err != nil || !upgrade {
		return err
	}
	if err := c.Update(ctx, func(tx *dbclient.Tx) error {
		// The schema first, so that the index methods find the schema that they need.
		if err := tx.Put(tableMeta, schemaKey, binary.AppendUvarint(nil, logSchemaVersion)); err != nil {
			return err
		}
		// The index of the schema 2 to 7 is that of the schema 8, and making it again would read every document.
		if version < indexSchema {
			if err := (&Tx{b: &logTx{tx: tx}}).run(func(t *Tx) error { return t.RebuildIndex() }); err != nil {
				return err
			}
		}
		upgrade := audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.StoreUpgrade,
			Detail: map[string]string{"from": strconv.FormatUint(version, 10), "to": strconv.Itoa(logSchemaVersion)}}
		if err := recordIn(tx, now, upgrade); err != nil {
			return err
		}
		// The heads of a store of the schema 6 have no rows yet; the record has put that of its log.
		t := &logTx{tx: tx}
		for _, log := range auditLogs {
			h, ok, err := t.auditHead(log)
			if err != nil {
				return err
			}
			if ok {
				if err := t.putHeadLine(h); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("%s: cannot upgrade the table schema to %d: %w", name, logSchemaVersion, err)
	}
	return nil
}

func (b *logBackend) view(ctx context.Context, f func(backendTx) error) error {
	return b.c.View(ctx, func(tx *dbclient.Tx) error { return f(&logTx{tx: tx}) })
}

func (b *logBackend) update(ctx context.Context, f func(backendTx) error) error {
	return b.c.Update(ctx, func(tx *dbclient.Tx) error { return f(&logTx{tx: tx}) })
}

func (b *logBackend) close() error {
	return b.c.Close()
}

func (b *logBackend) verify(ctx context.Context) error {
	return b.c.Verify(ctx)
}

type logTx struct {
	tx    *dbclient.Tx
	wrote bool // a write other than those of the audit log has been made
}

func (t *logTx) current(kind content.RefKind, id string) (Version, bool, error) {
	key := docKey(kind, id)
	v, ok, err := t.tx.Get(tableDocuments, key)
	if err != nil || !ok {
		return Version{}, false, err
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
	t.wrote = true
	if got := sha256.Sum256(data); got != want {
		return fmt.Errorf("object %s: the data has the hash %s", hash, hashText(got[:]))
	}
	return t.tx.PutObject(want, data)
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
	t.wrote = true
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
	t.wrote = true
	key := historyKey(kind, id, seq)
	v, ok, err := t.tx.Get(tableHistory, key)
	switch {
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("%s %q: missing", tableNames[tableHistory], key)
	}
	return t.tx.Put(tableDocuments, docKey(kind, id), v)
}

func (t *logTx) deleteCurrent(kind content.RefKind, id string) error {
	t.wrote = true
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
	type current struct {
		key []byte
		v   Version
	}
	var rows []current
	var err error
	if serr := t.tx.Scan(tableDocuments, prefix, func(key, value []byte) bool {
		var v Version
		if v, err = decodeCurrent(key, value, kind, string(key[len(prefix):])); err != nil {
			return false
		}
		rows = append(rows, current{key, v})
		return true
	}); err == nil {
		err = serr
	}
	// The data of the rows that the scan gave, the objects of a chunk with one window. The error is the first problem in
	// the order of the IDs, as when each row was read with its data: the error of the scan comes after the rows.
	var docs []Document
	for len(rows) > 0 {
		chunk := rows[:min(chunkSize, len(rows))]
		rows = rows[len(chunk):]
		hashes := make([][sha256.Size]byte, 0, len(chunk))
		for _, r := range chunk {
			if d, derr := digest(r.v.Hash); derr == nil {
				hashes = append(hashes, d)
			}
		}
		// A hint: an object that it could not read is read, and its failure reported, by data.
		_ = t.tx.PrefetchObjects(hashes)
		for _, r := range chunk {
			data, derr := t.data(tableDocuments, r.key, r.v.Hash)
			if derr != nil {
				t.tx.DropPrefetched()
				return docs, derr
			}
			docs = append(docs, Document{Version: r.v, Data: data})
		}
	}
	return docs, err
}

// getMany reads the rows of keys in table into the transaction, chunkSize keys at a time, so that a call holds the
// requests of one window at most.
func (t *logTx) getMany(table uint32, keys [][]byte) error {
	for len(keys) > 0 {
		n := min(chunkSize, len(keys))
		if err := t.tx.GetMany(table, keys[:n]); err != nil {
			return err
		}
		keys = keys[n:]
	}
	return nil
}

// prefetchObjects reads the objects with the hashes into the transaction, chunkSize at a time, as getMany does.
func (t *logTx) prefetchObjects(hashes [][sha256.Size]byte) error {
	for len(hashes) > 0 {
		n := min(chunkSize, len(hashes))
		if err := t.tx.PrefetchObjects(hashes[:n]); err != nil {
			return err
		}
		hashes = hashes[n:]
	}
	return nil
}

func (t *logTx) preloadEntries(entries []entryRevisions) error {
	var keys, prefixes [][]byte
	for _, e := range entries {
		for _, id := range e.named {
			keys = append(keys, []byte(id))
		}
		prefixes = append(prefixes, append([]byte(e.entry), 0))
	}
	if err := t.getMany(tableRevisions, keys); err != nil {
		return err
	}
	if err := t.tx.PrefetchScans(tableEntryRevisions, prefixes); err != nil {
		return err
	}
	// The rows of the revisions that the pages name, and then the data of those that entryRow reads: what cannot be read
	// here is left to the read that needs it.
	last := make([]string, len(entries))
	keys = keys[:0]
	for i, p := range prefixes {
		_ = t.tx.Scan(tableEntryRevisions, p, func(_, id []byte) bool {
			keys = append(keys, id)
			last[i] = string(id)
			return true
		})
	}
	if err := t.getMany(tableRevisions, keys); err != nil {
		return err
	}
	var hashes [][sha256.Size]byte
	for i, e := range entries {
		for _, id := range append(e.named[:len(e.named):len(e.named)], last[i]) {
			if id == "" {
				continue
			}
			if r, ok, err := t.revisionInfo(id); err == nil && ok {
				if d, err := digest(r.Hash); err == nil {
					hashes = append(hashes, d)
				}
			}
		}
	}
	return t.prefetchObjects(hashes)
}

func (t *logTx) preloadIndex(rows []indexRow) error {
	var keys [numIndexTables][][]byte
	for _, r := range rows {
		keys[r.table] = append(keys[r.table], r.key)
	}
	for table, k := range keys {
		if err := t.getMany(indexTableIDs[table], k); err != nil {
			return err
		}
	}
	return nil
}

func (t *logTx) preloadObjects(hashes []string) error {
	sums := make([][sha256.Size]byte, 0, len(hashes))
	for _, h := range hashes {
		if d, err := digest(h); err == nil {
			sums = append(sums, d)
		}
	}
	return t.prefetchObjects(sums)
}

func (t *logTx) forget() {
	t.tx.DropPrefetched()
}

func (t *logTx) readAhead(sums, docs []DocumentID, revs []string) error {
	t.tx.DropPrefetched()
	keysOf := func(ids []DocumentID) [][]byte {
		keys := make([][]byte, len(ids))
		for i, id := range ids {
			keys[i] = docKey(id.Kind, id.ID)
		}
		return keys
	}
	if len(sums) > 0 {
		// Summary reads the schema before the row.
		if err := t.getMany(tableMeta, [][]byte{schemaKey}); err != nil {
			return err
		}
	}
	if err := t.getMany(tableSummaries, keysOf(sums)); err != nil {
		return err
	}
	docKeys := keysOf(docs)
	if err := t.getMany(tableDocuments, docKeys); err != nil {
		return err
	}
	revKeys := make([][]byte, len(revs))
	for i, id := range revs {
		revKeys[i] = []byte(id)
	}
	if err := t.getMany(tableRevisions, revKeys); err != nil {
		return err
	}
	// The data of the versions and the revisions that are there and can be read; the others are left to their reads.
	var hashes [][sha256.Size]byte
	for i, id := range docs {
		if v, ok, err := t.tx.Get(tableDocuments, docKeys[i]); err == nil && ok {
			if ver, err := decodeCurrent(docKeys[i], v, id.Kind, id.ID); err == nil {
				if d, err := digest(ver.Hash); err == nil {
					hashes = append(hashes, d)
				}
			}
		}
	}
	for _, id := range revs {
		if r, ok, err := t.revisionInfo(id); err == nil && ok {
			if d, err := digest(r.Hash); err == nil {
				hashes = append(hashes, d)
			}
		}
	}
	return t.prefetchObjects(hashes)
}

func (t *logTx) history(kind content.RefKind, id string) ([]Version, error) {
	var versions []Version
	var err error
	if serr := t.tx.Scan(tableHistory, append(docKey(kind, id), 0), func(key, value []byte) bool {
		var v Version
		if v, err = decodeVersion(tableHistory, key, value, kind, id); err != nil {
			return false
		}
		versions = append(versions, v)
		return true
	}); err == nil {
		err = serr
	}
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
	return t.tx.Object(d)
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
	_, ok, err := t.tx.Get(tableRevisions, []byte(id))
	return ok, err
}

func (t *logTx) addRevision(id, entry, hash, savedAt, by string) (int64, error) {
	sum, err := digest(hash)
	if err != nil {
		return 0, err
	}
	t.wrote = true
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
	v, ok, err := t.tx.Get(tableRevisions, []byte(id))
	if err != nil || !ok {
		return Revision{}, false, err
	}
	r, err := decodeRevision(id, v)
	if err != nil {
		return Revision{}, false, err
	}
	return r, true, nil
}

func (t *logTx) revisions(entry string) ([]Revision, error) {
	type named struct{ key, id []byte }
	var rows []named
	serr := t.tx.Scan(tableEntryRevisions, append([]byte(entry), 0), func(key, id []byte) bool {
		rows = append(rows, named{key, id})
		return true
	})
	ids := make([][]byte, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	// A hint: a row that it could not read is read, and its failure reported, by revisionInfo.
	_ = t.getMany(tableRevisions, ids)
	var revs []Revision
	for _, row := range rows {
		r, ok, err := t.revisionInfo(string(row.id))
		if err == nil && !ok {
			err = fmt.Errorf("%s %q: the revision %s is missing", tableNames[tableEntryRevisions], row.key, row.id)
		}
		if err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	if serr != nil {
		return nil, serr
	}
	return revs, nil
}

func (t *logTx) allVersions(fn func(Version, error) bool) error {
	return t.tx.Scan(tableHistory, nil, func(key, value []byte) bool {
		kind, id, seq, ok := splitHistoryKey(key)
		if !ok {
			return fn(Version{}, invalidRowKey(tableHistory, key))
		}
		v, err := decodeVersion(tableHistory, key, value, kind, id)
		if err != nil {
			// Not err itself: the text of a time that does not parse has the row's value.
			return fn(Version{Seq: seq, Kind: kind, ID: id}, invalidValue(tableHistory, key))
		}
		return fn(v, nil)
	})
}

func (t *logTx) allCurrent(fn func(Version, error) bool) error {
	return t.tx.Scan(tableDocuments, nil, func(key, value []byte) bool {
		name, idName, ok := bytes.Cut(key, []byte{0})
		if !ok || len(name) == 0 {
			return fn(Version{}, invalidRowKey(tableDocuments, key))
		}
		kind, id := content.RefKind(name), string(idName)
		v, err := decodeVersion(tableDocuments, key, value, kind, id)
		if err != nil {
			return fn(Version{Kind: kind, ID: id}, invalidValue(tableDocuments, key))
		}
		return fn(v, nil)
	})
}

func (t *logTx) allRevisions(fn func(Revision, error) bool) error {
	return t.tx.Scan(tableRevisions, nil, func(key, value []byte) bool {
		id := string(key)
		r, err := decodeRevision(id, value)
		if err != nil {
			return fn(Revision{ID: id}, invalidValue(tableRevisions, key))
		}
		return fn(r, nil)
	})
}

func (t *logTx) allEntryRevisions(fn func(entry string, seq int64, id string, err error) bool) error {
	return t.tx.Scan(tableEntryRevisions, nil, func(key, value []byte) bool {
		if len(key) < 9 || key[len(key)-9] != 0 || binary.BigEndian.Uint64(key[len(key)-8:]) > math.MaxInt64 {
			return fn("", 0, "", invalidRowKey(tableEntryRevisions, key))
		}
		return fn(string(key[:len(key)-9]), int64(binary.BigEndian.Uint64(key[len(key)-8:])), string(value), nil)
	})
}

func (t *logTx) addEvent(data []byte) error {
	return t.tx.AppendEvent(data)
}

// events reads the events of the commits up to that of the snapshot of t, and not those that a later commit has added, so
// that the heads that t reads are those of the events. The marks of the daemon are among them.
func (t *logTx) events(ctx context.Context, fn func(number uint64, data []byte) bool) error {
	return t.tx.Events(ctx, fn)
}

var metaHeads = map[audit.Log]string{audit.LoginLog: metaLoginLog, audit.ActivityLog: metaActivityLog}

func (t *logTx) auditHead(log audit.Log) (audit.Head, bool, error) {
	key := metaHeads[log]
	v, ok, err := t.tx.Get(tableMeta, []byte(key))
	if err != nil || !ok {
		return audit.Head{}, false, err
	}
	r := valueReader{b: v}
	h := audit.Head{Log: log, N: r.uvarint()}
	copy(h.Hash[:], r.bytes(sha256.Size))
	if !r.done() || h.N == 0 {
		return audit.Head{}, false, invalidValue(tableMeta, []byte(key))
	}
	return h, true, nil
}

func (t *logTx) setAuditHead(h audit.Head) error {
	value := append(binary.AppendUvarint(nil, h.N), h.Hash[:]...)
	if err := t.tx.Put(tableMeta, []byte(metaHeads[h.Log]), value); err != nil {
		return err
	}
	return t.putHeadLine(h)
}

// putHeadLine puts the line of h in heads, where the daemon finds it to write it out.
func (t *logTx) putHeadLine(h audit.Head) error {
	return t.tx.Put(tableHeads, []byte(h.Log), []byte(h.String()))
}

func (t *logTx) headLine(log audit.Log) (string, bool, error) {
	v, ok, err := t.tx.Get(tableHeads, []byte(log))
	return string(v), ok, err
}

func (t *logTx) auditReady() (bool, error) {
	v, ok, err := t.tx.Get(tableMeta, schemaKey)
	if err != nil {
		return false, err
	}
	version, valid := decodeUint(v)
	if !ok || !valid {
		return false, invalidValue(tableMeta, schemaKey)
	}
	return version >= auditSchema, nil
}

func (t *logTx) exportReady() (bool, error) {
	v, ok, err := t.tx.Get(tableMeta, schemaKey)
	if err != nil {
		return false, err
	}
	version, valid := decodeUint(v)
	if !ok || !valid {
		return false, invalidValue(tableMeta, schemaKey)
	}
	return version >= exportSchema, nil
}

func (t *logTx) chainRow(key []byte) ([]byte, bool, error) {
	return t.tx.Get(tableChains, key)
}

func (t *logTx) chainRows(prefix []byte, fn func(key, value []byte) bool) error {
	return t.tx.Scan(tableChains, prefix, fn)
}

func (t *logTx) changed() bool {
	return t.wrote
}

func (t *logTx) indexGet(table indexTable, key []byte) ([]byte, bool, error) {
	return t.tx.Get(indexTableIDs[table], key)
}

func (t *logTx) indexPut(table indexTable, key, value []byte) error {
	t.wrote = true
	return t.tx.Put(indexTableIDs[table], key, value)
}

func (t *logTx) indexDelete(table indexTable, key []byte) error {
	t.wrote = true
	return t.tx.Delete(indexTableIDs[table], key)
}

func (t *logTx) indexScan(table indexTable, prefix []byte, fn func(key, value []byte) bool) error {
	return t.tx.Scan(indexTableIDs[table], prefix, fn)
}

func (t *logTx) indexReady() (bool, error) {
	v, ok, err := t.tx.Get(tableMeta, schemaKey)
	if err != nil {
		return false, err
	}
	version, valid := decodeUint(v)
	if !ok || !valid {
		return false, invalidValue(tableMeta, schemaKey)
	}
	return version >= indexSchema, nil
}

func (t *logTx) lastNumber() (int64, error) {
	v, ok, err := t.tx.Get(tableMeta, []byte(metaNumbers))
	if err != nil || !ok {
		return 0, err
	}
	n, valid := decodeUint(v)
	if !valid || n > math.MaxInt64 {
		return 0, invalidValue(tableMeta, []byte(metaNumbers))
	}
	return int64(n), nil
}

func (t *logTx) setLastNumber(n int64) error {
	t.wrote = true
	return t.tx.Put(tableMeta, []byte(metaNumbers), binary.AppendUvarint(nil, uint64(n)))
}

// next returns the seq of the next version or revision, one more than the last one, whose seq meta has at key, and
// puts it there.
func (t *logTx) next(key string) (int64, error) {
	var last uint64
	v, ok, err := t.tx.Get(tableMeta, []byte(key))
	if err != nil {
		return 0, err
	}
	if ok {
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

// splitHistoryKey splits key, a key of history, into the kind, the ID and the seq of the version.
func splitHistoryKey(key []byte) (kind content.RefKind, id string, seq int64, ok bool) {
	name, rest, found := bytes.Cut(key, []byte{0})
	if !found || len(name) == 0 || len(rest) < 9 || rest[len(rest)-9] != 0 {
		return "", "", 0, false
	}
	n := binary.BigEndian.Uint64(rest[len(rest)-8:])
	if n > math.MaxInt64 {
		return "", "", 0, false
	}
	return content.RefKind(name), string(rest[:len(rest)-9]), int64(n), true
}

// invalidRowKey is the error of key in table, which the backend cannot read.
func invalidRowKey(table uint32, key []byte) error {
	return fmt.Errorf("%s %q: invalid key", tableNames[table], key)
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
