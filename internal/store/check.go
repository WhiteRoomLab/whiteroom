package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The areas of a Problem, in the order in which Check looks at them.
const (
	AreaEngine   = "engine"   // the files of the engine: its log, and its objects
	AreaIndex    = "index"    // the rows of summaries and references, and the last number, against the documents
	AreaOriginal = "original" // the originals that the current media documents refer to
)

// Problem is one thing that Check finds wrong in a store. Target names the row or the original, "" if the problem is
// not about one; Detail is the reason, which never has the value of a row.
type Problem struct {
	Area   string
	Target string
	Detail string
}

var oneLine = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// String returns the problem on one line: the area, the target if it has one, and the detail.
func (p Problem) String() string {
	if p.Target == "" {
		return oneLine.Replace(p.Area + ": " + p.Detail)
	}
	return oneLine.Replace(p.Area + ": " + p.Target + ": " + p.Detail)
}

// errStop is the error with which the stages of Check end when found has returned false.
var errStop = errors.New("stop")

// Check looks for what is wrong in the store, without writing a file or a record, and calls found with each problem
// that it finds, until found returns false, which stops it: Check returns nil then, and looks at nothing more. found
// must not be nil. It looks at three areas in this order (Problem.Area): the engine, with its own check (the log, and
// the data of every object against its hash); the index, which must have the rows that the current documents and the
// revisions give, and no other, and a last number that is not below the largest number of the entries (the tables of
// the operations, the accounts and the sessions are records, which no document gives, and Check does not look at
// them); and the originals that the current media documents refer to, which are read to their ends, once for each hash,
// against it. An area that cannot go on, as when the engine cannot read an object, gives one problem, and Check goes
// on to the next area. A store of the table schema 1 has no index: that is the one problem of its index. Check works in
// a store that is opened by its writer, and in one that is opened to read only, which may be written at the same time:
// a store that is right gives no problem to either.
//
// The error of Check is that of ctx, context.Canceled or context.DeadlineExceeded, and no other: what else goes wrong
// is a problem.
func (s *Store) Check(ctx context.Context, found func(Problem) bool) error {
	c := &checker{ctx: ctx, found: found}
	if err := s.b.verify(ctx); err != nil {
		if endedBy(err) {
			return err
		}
		if !c.report(Problem{Area: AreaEngine, Detail: err.Error()}) {
			return nil
		}
	}
	media, err := c.checkIndex(s)
	if err != nil || c.stopped {
		return err
	}
	return c.checkOriginals(s, media)
}

// checker is a Check: the problems go to found, and stopped is set when it has returned false.
type checker struct {
	ctx     context.Context
	found   func(Problem) bool
	stopped bool
}

// report passes p to found, and returns false if Check must stop.
func (c *checker) report(p Problem) bool {
	if c.stopped {
		return false
	}
	if !c.found(p) {
		c.stopped = true
	}
	return !c.stopped
}

// endedBy reports whether err is the end of a context.
func endedBy(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// mediaOf is the media documents that refer to each original, by its hash, in the order of their IDs.
type mediaOf map[string][]string

// checkIndex checks the index, and finds the originals of the current media documents, in one View. It returns an
// error only if ctx has ended. If the View itself fails, as a reader does when it cannot read the commits that have
// become durable, that is one problem of the index, and the originals are not known: the next area gives one problem
// too.
func (c *checker) checkIndex(s *Store) (mediaOf, error) {
	var media mediaOf
	var mediaErr error
	err := s.View(c.ctx, func(t *Tx) error {
		// First, for the originals to be found when the index cannot be computed.
		media, mediaErr = mediaHashes(t)
		err := c.compareIndex(t)
		if c.stopped || endedBy(err) {
			return err
		}
		if err != nil {
			c.report(Problem{Area: AreaIndex, Detail: err.Error()})
		}
		return nil
	})
	switch {
	case c.stopped:
		return nil, nil
	case endedBy(err):
		return nil, err
	case err != nil:
		media, mediaErr = nil, err
		c.report(Problem{Area: AreaIndex, Detail: err.Error()})
	}
	if mediaErr != nil && !c.stopped {
		c.report(Problem{Area: AreaOriginal, Detail: "the media documents cannot be read: " + mediaErr.Error()})
	}
	return media, nil
}

// mediaHashes returns the media documents of t by the hashes of the originals that they refer to. A document that does
// not decode, and one without a hash, refers to none.
func mediaHashes(t *Tx) (mediaOf, error) {
	docs, err := t.Documents(content.RefMedia)
	if err != nil {
		return nil, err
	}
	media := mediaOf{}
	for _, d := range docs {
		doc, err := checkData(content.RefMedia, d.ID, d.Data)
		if err != nil {
			continue
		}
		if m, ok := doc.(*content.Media); ok && m.Hash != "" {
			media[m.Hash] = append(media[m.Hash], d.ID)
		}
	}
	return media, nil
}

// compareIndex compares the rows of summaries and references that t has with those that the documents give, and
// the last number with the largest number of the entries, and reports each difference as one problem. It returns the
// error that has stopped it, which is errStop if found has returned false.
func (c *checker) compareIndex(t *Tx) error {
	if err := t.checkIndex(); err != nil {
		if !errors.Is(err, ErrNoIndex) {
			return err
		}
		c.report(Problem{Area: AreaIndex, Detail: err.Error()})
		return nil
	}
	tables := []indexTable{indexSummaries, indexReferences}
	want := map[indexTable]map[string]struct{}{indexSummaries: {}, indexReferences: {}}
	largest, err := t.computeIndex(func(table indexTable, key, value []byte) error {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		want[table][string(key)] = struct{}{}
		got, ok, err := t.b.indexGet(table, key)
		switch {
		case err != nil:
			return err
		case !ok:
			c.indexProblem(table, key, "the row is missing")
		case !bytes.Equal(got, value):
			c.indexProblem(table, key, "the row differs from the one that the documents give")
		}
		return c.stopError()
	})
	if err != nil {
		if c.stopped || endedBy(err) {
			return err
		}
		return fmt.Errorf("the rows that the documents give cannot be computed: %w", err)
	}
	for _, table := range tables {
		var serr error
		if err := t.b.indexScan(table, nil, func(key, _ []byte) bool {
			if _, ok := want[table][string(key)]; !ok {
				c.indexProblem(table, key, "the row is not given by any document")
			}
			if serr = c.ctx.Err(); serr == nil {
				serr = c.stopError()
			}
			return serr == nil
		}); err != nil {
			return err
		}
		if serr != nil {
			return serr
		}
	}
	last, err := t.b.lastNumber()
	switch {
	case err != nil:
		c.report(Problem{Area: AreaIndex, Target: "last number", Detail: err.Error()})
	case last < largest:
		c.report(Problem{Area: AreaIndex, Target: "last number",
			Detail: fmt.Sprintf("it is %d, but the largest number of the entries is %d", last, largest)})
	}
	return c.stopError()
}

// stopError returns errStop if found has returned false.
func (c *checker) stopError() error {
	if c.stopped {
		return errStop
	}
	return nil
}

// indexProblem reports the problem of the row key of table.
func (c *checker) indexProblem(table indexTable, key []byte, detail string) {
	c.report(Problem{Area: AreaIndex, Target: rowName(table, key), Detail: detail})
}

// rowName returns the name of the row key of table, summaries or references, for a person.
func rowName(table indexTable, key []byte) string {
	name := indexTableNames[table]
	if table == indexSummaries {
		if kind, id, ok := bytes.Cut(key, []byte{0}); ok && len(kind) > 0 {
			return name + " " + docLabel(content.RefKind(kind), string(id))
		}
	} else if r, ok := splitRefKey(key); ok {
		target, _, _ := bytes.Cut(key, []byte{0})
		return fmt.Sprintf("%s %s -> %s (%s)", name, docLabel(r.Kind, r.ID), target, r.Role)
	}
	return fmt.Sprintf("%s %q", name, key)
}

// docLabel returns the kind and the ID of a document for a person: the kind alone for the settings document.
func docLabel(kind content.RefKind, id string) string {
	if id == "" {
		return string(kind)
	}
	return string(kind) + " " + id
}

// checkOriginals reads each original of media, in the order of their hashes, to its end.
func (c *checker) checkOriginals(s *Store, media mediaOf) error {
	for _, hash := range slices.Sorted(maps.Keys(media)) {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		err := s.verifyOriginal(c.ctx, hash)
		if endedBy(err) {
			return err
		}
		if err != nil && !c.report(Problem{Area: AreaOriginal, Target: hash,
			Detail: fmt.Sprintf("%s (of the media %s)", err, strings.Join(media[hash], ", "))}) {
			return nil
		}
	}
	return nil
}

// verifyOriginal reads the original with hash to its end, which checks its data against the hash, and checks ctx before
// each read.
func (s *Store) verifyOriginal(ctx context.Context, hash string) error {
	f, err := s.OpenOriginal(hash)
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, ctxReader{ctx, f})
	_ = f.Close()
	return err
}

// ctxReader reads from r unless ctx has ended.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
