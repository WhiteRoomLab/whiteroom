package logdb

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Placement is where the engine puts the data of new objects.
type Placement int

const (
	Inline Placement = iota // in the commit records
	Files                   // in files of objects/, which are durable before the commit record names them
	Hybrid                  // in files if they have at least Options.Threshold bytes, else inline
)

// toFile reports whether an object of size bytes goes in a file.
func (o *Options) toFile(size int) bool {
	switch o.Placement {
	case Files:
		return true
	case Hybrid:
		return size >= o.Threshold
	case Inline:
	}
	return false
}

// PutObject puts data in the store as an object, unless the store has it, and returns its SHA-256. It copies data.
func (tx *Tx) PutObject(data []byte) ([32]byte, error) {
	if err := tx.checkTx(); err != nil {
		return [32]byte{}, err
	}
	hash := sha256.Sum256(data)
	if tx.HasObject(hash) {
		return hash, nil
	}
	v := clone(data)
	if tx.objs == nil {
		tx.objs = map[[32]byte][]byte{}
	}
	tx.objs[hash] = v
	if tx.db.o.toFile(len(v)) {
		tx.ops = append(tx.ops, op{code: opFile, hash: hash, size: int64(len(v))})
	} else {
		tx.ops = append(tx.ops, op{code: opInline, hash: hash, value: v})
	}
	return hash, nil
}

// HasObject reports whether the store has the object hash.
func (tx *Tx) HasObject(hash [32]byte) bool {
	if tx.done {
		return false
	}
	if _, ok := tx.objs[hash]; ok {
		return true
	}
	_, ok := tx.snapshot().objects.Get(string(hash[:]))
	return ok
}

// Object returns the data of the object hash, as it is on disk: the engine does not check it against the hash. If the
// store does not have the object, the error wraps fs.ErrNotExist.
func (tx *Tx) Object(hash [32]byte) ([]byte, error) {
	if tx.done {
		return nil, errEnded
	}
	if data, ok := tx.objs[hash]; ok {
		return clone(data), nil
	}
	st := tx.snapshot()
	loc, ok := st.objects.Get(string(hash[:]))
	if !ok {
		return nil, fmt.Errorf("object %x: %w", hash[:], fs.ErrNotExist)
	}
	return tx.db.readObject(st.gen, hash, loc)
}

// Objects calls fn with the hash and the size of each object of the store, in ascending order of the hashes, until fn
// returns false.
func (tx *Tx) Objects(fn func(hash [32]byte, size int64) bool) {
	if tx.done {
		return
	}
	added := slices.SortedFunc(maps.Keys(tx.objs), func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })
	more := true
	next := func(hash [32]byte, size int64) bool {
		more = fn(hash, size)
		return more
	}
	tx.snapshot().objects.Ascend("", func(k string, loc objLoc) bool {
		for len(added) > 0 && string(added[0][:]) < k {
			if !next(added[0], int64(len(tx.objs[added[0]]))) {
				return false
			}
			added = added[1:]
		}
		return next([32]byte([]byte(k)), loc.size)
	})
	for ; more && len(added) > 0; added = added[1:] {
		next(added[0], int64(len(tx.objs[added[0]])))
	}
}

// objName is the name of the file of the object hash: objects/, the first 2 of its hex digits, a slash and all 64.
func objName(hash [32]byte) string {
	h := hex.EncodeToString(hash[:])
	return "objects/" + h[:2] + "/" + h
}

// readObject reads the data of the object hash of the generation gen, which is at loc.
func (db *DB) readObject(gen uint64, hash [32]byte, loc objLoc) ([]byte, error) {
	if loc.placement != placeInline {
		return db.readObjectFile(hash, loc.size)
	}
	b := make([]byte, loc.size)
	if err := db.handles.readAt(gen, loc.seg, b, loc.off); err != nil {
		return nil, err
	}
	return b, nil
}

// readObjectFile reads the file of the object hash, which must have a valid header and size bytes after it.
func (db *DB) readObjectFile(hash [32]byte, size int64) ([]byte, error) {
	name := objName(hash)
	p := path.Join(db.fsys.Name(), name)
	info, err := lstatEntry(db.fsys, name, false)
	if err != nil {
		return nil, err
	}
	if want := uint64(size) + objHeaderSize; uint64(info.Size()) != want {
		return nil, corruptFile(p, fmt.Sprintf("has %d bytes, want %d", info.Size(), want))
	}
	if info.Size() > math.MaxInt {
		return nil, fmt.Errorf("%s: too large to read", p)
	}
	f, err := db.fsys.Open(name)
	if err != nil {
		return nil, entryError(p, err, false)
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, info.Size())
	if err := readAt(f, b, 0, p); err != nil {
		return nil, err
	}
	if err := parseObjHeader(p, b); err != nil {
		return nil, err
	}
	return b[objHeaderSize:], nil
}

// createTemp creates a new file, whose name is prefix and 16 random hex digits, and returns it with its name.
func createTemp(fsys vfs.FS, prefix string) (vfs.File, string, error) {
	var err error
	for range 10 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(b[:])
		var f vfs.File
		if f, err = fsys.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, fs.ErrExist) {
			return f, name, err
		}
	}
	return nil, "", err
}
