package store

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// modelBackend is the model of a backend, in Go maps and slices alone, that the differential tests, TestLogRoundTrip
// and the matrices compare the log backend with. An Update works on a copy of the state and keeps it if f returns nil.
// It is for sequential use: a View sees the state when it starts, and a waiting Update does not stop when ctx ends.
type modelBackend struct {
	mu  sync.Mutex  // guards st
	upd sync.Mutex  // one Update at a time
	st  *modelState // the committed state, which an Update replaces and never changes
}

type modelKey struct {
	kind content.RefKind
	id   string
}

// modelVersion is a version of a document; its hash is "" for a deletion.
type modelVersion struct {
	key               modelKey
	hash, savedAt, by string
}

type modelRevision struct {
	id, entry, hash, savedAt, by string
}

// modelState is the state of a model: the seq of a version or a revision is its index in versions or revisions, plus 1.
type modelState struct {
	objects   map[string][]byte // by hash
	versions  []modelVersion
	current   map[modelKey]int64 // the seq of the current version of each document
	revisions []modelRevision
	revByID   map[string]int64 // the seq of each revision
	events    [][]byte
	heads     map[audit.Log]audit.Head          // of the audit logs that have records
	index     [numIndexTables]map[string][]byte // the rows of the index tables
	number    int64                             // the last number
}

// clone returns a copy of s that shares the byte slices, which nothing changes.
func (s *modelState) clone() *modelState {
	c := &modelState{
		objects: maps.Clone(s.objects), versions: slices.Clone(s.versions), current: maps.Clone(s.current),
		revisions: slices.Clone(s.revisions), revByID: maps.Clone(s.revByID), events: slices.Clone(s.events),
		heads: maps.Clone(s.heads), number: s.number,
	}
	for i, rows := range s.index {
		c.index[i] = maps.Clone(rows)
	}
	return c
}

func newModelBackend() *modelBackend {
	st := &modelState{objects: map[string][]byte{}, current: map[modelKey]int64{}, revByID: map[string]int64{},
		heads: map[audit.Log]audit.Head{}}
	for i := range st.index {
		st.index[i] = map[string][]byte{}
	}
	return &modelBackend{st: st}
}

// newModelStore returns a store on a new model, with its originals in a world of simfs.
func newModelStore() *Store {
	return &Store{b: newModelBackend(), files: simfs.New("files"), now: func() time.Time { return testTime }}
}

func (b *modelBackend) committed() *modelState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.st
}

func (b *modelBackend) view(ctx context.Context, f func(backendTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f(&modelTx{st: b.committed()}); err != nil {
		return err
	}
	return ctx.Err()
}

func (b *modelBackend) update(ctx context.Context, f func(backendTx) error) error {
	b.upd.Lock()
	defer b.upd.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	st := b.committed().clone()
	if err := f(&modelTx{st: st}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	b.st = st
	b.mu.Unlock()
	return nil
}

// close keeps the state, so that a new Store on the same model is the store opened again.
func (b *modelBackend) close() error {
	return nil
}

type modelTx struct {
	st    *modelState
	wrote bool // a write other than those of the audit log has been made
}

// version returns the version seq, which the model has.
func (t *modelTx) version(seq int64) (Version, error) {
	mv := t.st.versions[seq-1]
	at, err := parseSavedAt(mv.savedAt)
	if err != nil {
		return Version{}, versionErr(seq, err)
	}
	return Version{Seq: seq, Kind: mv.key.kind, ID: mv.key.id, Hash: mv.hash, SavedAt: at, SavedBy: mv.by}, nil
}

// revisionInfo returns the revision seq, which the model has, without Data.
func (t *modelTx) revisionInfo(seq int64) (Revision, error) {
	mr := t.st.revisions[seq-1]
	at, err := parseSavedAt(mr.savedAt)
	if err != nil {
		return Revision{}, revisionErr(mr.id, err)
	}
	return Revision{Seq: seq, ID: mr.id, Entry: mr.entry, Hash: mr.hash, SavedAt: at, SavedBy: mr.by}, nil
}

// data returns the data of the object hash, which the model must have.
func (t *modelTx) data(hash string) ([]byte, error) {
	data, ok := t.st.objects[hash]
	if !ok {
		return nil, fmt.Errorf("the object %s is missing", hash)
	}
	return data, nil
}

func (t *modelTx) current(kind content.RefKind, id string) (Version, bool, error) {
	seq, ok := t.st.current[modelKey{kind, id}]
	if !ok {
		return Version{}, false, nil
	}
	v, err := t.version(seq)
	if err != nil {
		return Version{}, false, err
	}
	return v, true, nil
}

func (t *modelTx) addObject(hash string, data []byte) error {
	t.wrote = true
	if _, ok := t.st.objects[hash]; !ok {
		t.st.objects[hash] = bytes.Clone(data)
	}
	return nil
}

func (t *modelTx) addVersion(kind content.RefKind, id, hash, savedAt, by string) (int64, error) {
	t.wrote = true
	t.st.versions = append(t.st.versions, modelVersion{key: modelKey{kind, id}, hash: hash, savedAt: savedAt, by: by})
	return int64(len(t.st.versions)), nil
}

func (t *modelTx) setCurrent(kind content.RefKind, id string, seq int64) error {
	t.wrote = true
	key := modelKey{kind, id}
	if seq < 1 || seq > int64(len(t.st.versions)) || t.st.versions[seq-1].key != key {
		return fmt.Errorf("%s %q: the version %d is missing", kind, id, seq)
	}
	t.st.current[key] = seq
	return nil
}

func (t *modelTx) deleteCurrent(kind content.RefKind, id string) error {
	t.wrote = true
	delete(t.st.current, modelKey{kind, id})
	return nil
}

func (t *modelTx) document(kind content.RefKind, id string) (Document, bool, error) {
	v, ok, err := t.current(kind, id)
	if err != nil || !ok {
		return Document{}, false, err
	}
	data, err := t.data(v.Hash)
	if err != nil {
		return Document{}, false, err
	}
	return Document{Version: v, Data: data}, true, nil
}

func (t *modelTx) documents(kind content.RefKind) ([]Document, error) {
	var ids []string
	for key := range t.st.current {
		if key.kind == kind {
			ids = append(ids, key.id)
		}
	}
	slices.Sort(ids) // byte order, as the keys of the engine
	var docs []Document
	for _, id := range ids {
		d, _, err := t.document(kind, id)
		if err != nil {
			return docs, err
		}
		docs = append(docs, d)
	}
	return docs, nil
}

func (t *modelTx) history(kind content.RefKind, id string) ([]Version, error) {
	var versions []Version
	for i, mv := range t.st.versions {
		if mv.key != (modelKey{kind, id}) {
			continue
		}
		v, err := t.version(int64(i) + 1)
		if err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, nil
}

func (t *modelTx) object(hash string) ([]byte, bool, error) {
	data, ok := t.st.objects[hash]
	return data, ok, nil
}

func (t *modelTx) revisionExists(id string) (bool, error) {
	_, ok := t.st.revByID[id]
	return ok, nil
}

func (t *modelTx) addRevision(id, entry, hash, savedAt, by string) (int64, error) {
	t.wrote = true
	t.st.revisions = append(t.st.revisions, modelRevision{id: id, entry: entry, hash: hash, savedAt: savedAt, by: by})
	seq := int64(len(t.st.revisions))
	t.st.revByID[id] = seq
	return seq, nil
}

func (t *modelTx) revision(id string) (Revision, bool, error) {
	seq, ok := t.st.revByID[id]
	if !ok {
		return Revision{}, false, nil
	}
	r, err := t.revisionInfo(seq)
	if err != nil {
		return Revision{}, false, err
	}
	if r.Data, err = t.data(r.Hash); err != nil {
		return Revision{}, false, err
	}
	return r, true, nil
}

func (t *modelTx) revisions(entry string) ([]Revision, error) {
	var revs []Revision
	for i, mr := range t.st.revisions {
		if mr.entry != entry {
			continue
		}
		r, err := t.revisionInfo(int64(i) + 1)
		if err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	return revs, nil
}

func (t *modelTx) addEvent(data []byte) error {
	t.st.events = append(t.st.events, bytes.Clone(data))
	return nil
}

func (t *modelTx) events(ctx context.Context, fn func(number uint64, data []byte) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, data := range t.st.events {
		if !fn(uint64(i)+1, data) {
			break
		}
	}
	return nil
}

func (t *modelTx) auditHead(log audit.Log) (audit.Head, bool, error) {
	h, ok := t.st.heads[log]
	return h, ok, nil
}

func (t *modelTx) setAuditHead(h audit.Head) error {
	t.st.heads[h.Log] = h
	return nil
}

func (t *modelTx) auditReady() (bool, error) {
	return true, nil
}

func (t *modelTx) changed() bool {
	return t.wrote
}

func (t *modelTx) indexGet(table indexTable, key []byte) ([]byte, bool, error) {
	v, ok := t.st.index[table][string(key)]
	return v, ok, nil
}

func (t *modelTx) indexPut(table indexTable, key, value []byte) error {
	t.wrote = true
	t.st.index[table][string(key)] = bytes.Clone(value)
	return nil
}

func (t *modelTx) indexDelete(table indexTable, key []byte) error {
	t.wrote = true
	delete(t.st.index[table], string(key))
	return nil
}

func (t *modelTx) indexScan(table indexTable, prefix []byte, fn func(key, value []byte) bool) error {
	type row struct {
		key   string
		value []byte
	}
	var rows []row
	for k, v := range t.st.index[table] {
		if strings.HasPrefix(k, string(prefix)) {
			rows = append(rows, row{k, v})
		}
	}
	slices.SortFunc(rows, func(a, b row) int { return strings.Compare(a.key, b.key) }) // byte order, as the engine
	for _, r := range rows {
		if !fn([]byte(r.key), r.value) {
			break
		}
	}
	return nil
}

func (t *modelTx) indexReady() (bool, error) {
	return true, nil
}

func (t *modelTx) lastNumber() (int64, error) {
	return t.st.number, nil
}

func (t *modelTx) setLastNumber(n int64) error {
	t.wrote = true
	t.st.number = n
	return nil
}
