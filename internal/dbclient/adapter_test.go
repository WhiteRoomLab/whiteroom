package dbclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// txn is what a dbclient.Tx and an engine transaction have in common: the calls of a program that runs on both.
type txn interface {
	Seq() uint64
	Get(table uint32, key []byte) ([]byte, bool, error)
	Scan(table uint32, prefix []byte, fn func(key, value []byte) bool) error
	Put(table uint32, key, value []byte) error
	Delete(table uint32, key []byte) error
	PutObject(hash [32]byte, data []byte) error
	Object(hash [32]byte) ([]byte, bool, error)
	AppendEvent(data []byte) error
	Events(ctx context.Context, fn func(number uint64, data []byte) bool) error
}

var _ txn = (*dbclient.Tx)(nil)

// engineTx gives a logdb.Tx the calls of a dbclient.Tx.
type engineTx struct {
	tx *logdb.Tx
	db *logdb.DB
}

func (e engineTx) Seq() uint64 { return e.tx.Seq() }

func (e engineTx) Get(table uint32, key []byte) ([]byte, bool, error) {
	v, ok := e.tx.Get(table, key)
	return v, ok, nil
}

func (e engineTx) Scan(table uint32, prefix []byte, fn func(key, value []byte) bool) error {
	e.tx.Scan(table, prefix, fn)
	return nil
}

func (e engineTx) Put(table uint32, key, value []byte) error { return e.tx.Put(table, key, value) }
func (e engineTx) Delete(table uint32, key []byte) error     { return e.tx.Delete(table, key) }

func (e engineTx) PutObject(hash [32]byte, data []byte) error {
	got, err := e.tx.PutObject(data)
	if err == nil && got != hash {
		return fmt.Errorf("the data do not have the hash that was given")
	}
	return err
}

func (e engineTx) Object(hash [32]byte) ([]byte, bool, error) {
	data, err := e.tx.Object(hash)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (e engineTx) AppendEvent(data []byte) error { return e.tx.AppendEvent(data) }

func (e engineTx) Events(ctx context.Context, fn func(number uint64, data []byte) bool) error {
	seq := e.tx.Seq()
	return e.db.Events(ctx, func(ev logdb.Event) bool { return ev.Seq <= seq && fn(ev.Number, ev.Data) })
}

// updateEngine and viewEngine run f on an engine as the Update and the View of a client do.
func updateEngine(db *logdb.DB, f func(tx txn) error) error {
	return db.Update(bg, func(tx *logdb.Tx) error { return f(engineTx{tx, db}) })
}

func viewEngine(db *logdb.DB, f func(tx txn) error) error {
	return db.View(bg, func(tx *logdb.Tx) error { return f(engineTx{tx, db}) })
}

func updateClient(c *dbclient.Client, f func(tx txn) error) error {
	return c.Update(bg, func(tx *dbclient.Tx) error { return f(tx) })
}

func viewClient(c *dbclient.Client, f func(tx txn) error) error {
	return c.View(bg, func(tx *dbclient.Tx) error { return f(tx) })
}

// errAbort ends the function of an Update, to roll it back.
var errAbort = errors.New("abort")

// outcome says how an Update ended, in words that do not depend on which side it ran.
func outcome(err error) string {
	switch re, isRemote := errors.AsType[*dbwire.RemoteError](err); {
	case err == nil:
		return "committed"
	case errors.Is(err, errAbort):
		return "aborted"
	case isRemote:
		return fmt.Sprintf("refused by the daemon (%v): %s", re.Code, re.Msg)
	}
	return "failed: " + err.Error()
}

// state is what an engine holds, as text: the committed tables, objects and events.
type state struct {
	Seq     uint64
	Tables  map[uint32][]string
	Objects []object
	Events  []loggedEvent
}

type object struct {
	Hash [32]byte
	Size int64
}

type loggedEvent struct {
	Number, Seq uint64
	Data        []byte
}

func (o object) String() string { return fmt.Sprintf("%x:%d", o.Hash[:6], o.Size) }

// readState reads the engine db, which has the tables 1 to 12 at most.
func readState(db *logdb.DB) (state, error) {
	s := state{Tables: map[uint32][]string{}}
	err := db.View(bg, func(tx *logdb.Tx) error {
		s.Seq = tx.Seq()
		for table := uint32(1); table <= 12; table++ {
			tx.Scan(table, nil, func(k, v []byte) bool {
				s.Tables[table] = append(s.Tables[table], fmt.Sprintf("%q=%s", k, digest(v)))
				return true
			})
		}
		tx.Objects(func(hash [32]byte, size int64) bool {
			s.Objects = append(s.Objects, object{hash, size})
			return true
		})
		return nil
	})
	if err != nil {
		return state{}, err
	}
	err = db.Events(bg, func(ev logdb.Event) bool {
		s.Events = append(s.Events, loggedEvent{ev.Number, ev.Seq, bytes.Clone(ev.Data)})
		return true
	})
	return s, err
}

// digest shows the first bytes of a value and its length and SHA-256, so that equal texts are equal values.
func digest(v []byte) string {
	h := sha256.Sum256(v)
	n := min(len(v), 12)
	return fmt.Sprintf("%q+%d#%x", v[:n], len(v), h[:4])
}

// checkObjects reads every object of the engine and compares it with its hash.
func checkObjects(db *logdb.DB, objects []object) error {
	return db.View(bg, func(tx *logdb.Tx) error {
		for _, o := range objects {
			data, err := tx.Object(o.Hash)
			if err != nil {
				return err
			}
			if sha256.Sum256(data) != o.Hash || int64(len(data)) != o.Size {
				return fmt.Errorf("object %v has other data than its hash and size say", o)
			}
		}
		return nil
	})
}
