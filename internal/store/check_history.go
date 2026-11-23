package store

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// earlierOf is the versions of the media documents that no row of documents names, by the hash of the original that each
// one refers to.
type earlierOf map[string][]mediaVersion

type mediaVersion struct {
	id  string
	seq int64
}

// objectRef is a version, or a revision (kind ""), whose object checkHistory reads.
type objectRef struct {
	kind    content.RefKind
	id      string // of the document, or the revision
	seq     int64  // of the version
	hash    [sha256.Size]byte
	current bool // the version that the row of documents names
}

func (r objectRef) target() string {
	if r.kind == "" {
		return "revisions " + r.id
	}
	return versionTarget(r.kind, r.id, r.seq)
}

func versionTarget(kind content.RefKind, id string, seq int64) string {
	return fmt.Sprintf("history %s version %d", docLabel(kind, id), seq)
}

// currentRow is a row of documents that the walk of the history has to meet.
type currentRow struct {
	v      Version
	bad    bool // the row does not decode
	walked bool
}

type revisionInfo struct {
	entry   string
	decoded bool
}

// checkHistory compares the tables of the versions and the revisions with each other and with their objects in one View,
// and returns the versions of the media documents that no row of documents names. An error that is not the end of ctx or
// an unreachable daemon is one problem of the history, as in checkIndex, and no versions are known then.
func (c *checker) checkHistory(s *Store) (earlierOf, error) {
	var earlier earlierOf
	err := s.View(c.ctx, func(t *Tx) (err error) {
		earlier, err = c.compareHistory(t)
		return err
	})
	switch {
	case c.stopped:
		return nil, nil
	case unchecked(err):
		return nil, err
	case err != nil:
		c.report(Problem{Area: AreaHistory, Detail: err.Error()})
		return nil, nil
	}
	return earlier, nil
}

// compareHistory makes the checks of the history area in t, and returns errStop if found has returned false.
func (c *checker) compareHistory(t *Tx) (earlierOf, error) {
	rows, err := c.currentRows(t)
	if err != nil {
		return nil, err
	}
	refs, err := c.compareVersions(t, rows)
	if err != nil {
		return nil, err
	}
	more, err := c.compareRevisions(t)
	if err != nil {
		return nil, err
	}
	return c.readObjects(t, append(refs, more...))
}

// currentRows reads the rows of documents, which compareVersions compares with the last versions of the histories.
func (c *checker) currentRows(t *Tx) (map[DocumentID]*currentRow, error) {
	rows := map[DocumentID]*currentRow{}
	err := t.b.allCurrent(func(v Version, err error) bool {
		if v.Kind == "" {
			return c.report(Problem{Area: AreaHistory, Detail: err.Error()})
		}
		rows[DocumentID{v.Kind, v.ID}] = &currentRow{v: v, bad: err != nil}
		return true
	})
	if err != nil {
		return nil, err
	}
	return rows, c.stopError()
}

// compareVersions walks the history in the order of its keys, compares the last version of each document with its row of
// documents, and returns every version that is not a deletion, for checkHistory to read its object. A row of documents
// that no history belongs to is a problem too.
func (c *checker) compareVersions(t *Tx, rows map[DocumentID]*currentRow) ([]objectRef, error) {
	var refs []objectRef
	add := func(v Version, current bool) {
		if d, err := digest(v.Hash); err == nil {
			refs = append(refs, objectRef{kind: v.Kind, id: v.ID, seq: v.Seq, hash: d, current: current})
		}
	}
	last := func(v Version) { add(v, c.compareLast(rows, v)) }
	var prev Version
	have := false
	err := t.b.allVersions(func(v Version, err error) bool {
		switch {
		case err != nil && v.Kind == "":
			return c.report(Problem{Area: AreaHistory, Detail: err.Error()})
		case err != nil:
			return c.report(Problem{Area: AreaHistory, Target: versionTarget(v.Kind, v.ID, v.Seq),
				Detail: "the row cannot be decoded"})
		case have && v.Kind == prev.Kind && v.ID == prev.ID:
			add(prev, false)
		case have:
			last(prev)
		}
		prev, have = v, true
		return !c.stopped
	})
	if err != nil {
		return nil, err
	}
	if have && !c.stopped {
		last(prev)
	}
	if err := c.stopError(); err != nil {
		return nil, err
	}
	var rest []DocumentID
	for id, row := range rows {
		if !row.walked {
			rest = append(rest, id)
		}
	}
	slices.SortFunc(rest, func(a, b DocumentID) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ID, b.ID)) })
	for _, id := range rest {
		detail := "the document has no version in its history"
		if rows[id].bad {
			detail = "the row cannot be decoded"
		}
		if !c.report(Problem{Area: AreaHistory, Target: "documents " + docLabel(id.Kind, id.ID), Detail: detail}) {
			break
		}
	}
	return refs, c.stopError()
}

// compareLast compares last, the last version of the history of a document, with the row of documents, and reports
// whether the row names it.
func (c *checker) compareLast(rows map[DocumentID]*currentRow, last Version) (named bool) {
	row := rows[DocumentID{last.Kind, last.ID}]
	if row != nil {
		row.walked = true
	}
	report := func(detail string) {
		c.report(Problem{Area: AreaHistory, Target: "documents " + docLabel(last.Kind, last.ID), Detail: detail})
	}
	switch {
	case last.Hash == "":
		if row != nil {
			report(fmt.Sprintf("the row is there, but the last version of its history, %d, is a deletion", last.Seq))
		}
	case row == nil:
		report(fmt.Sprintf("the row is missing; the last version of its history is %d", last.Seq))
	case row.bad:
		report("the row cannot be decoded")
	case row.v.Seq != last.Seq:
		report(fmt.Sprintf("it is version %d, but the last version of its history is %d", row.v.Seq, last.Seq))
	case !row.v.SavedAt.Equal(last.SavedAt) || row.v.Hash != last.Hash || row.v.SavedBy != last.SavedBy:
		report(fmt.Sprintf("it differs from the version %d of its history", last.Seq))
	default:
		return true
	}
	return false
}

// compareRevisions checks that revisions and entryRevisions name each other, and returns every revision, for
// checkHistory to read its object.
func (c *checker) compareRevisions(t *Tx) ([]objectRef, error) {
	var refs []objectRef // in the order of the IDs
	type revision struct{ id, entry string }
	var listing []revision
	known := map[string]revisionInfo{}
	err := t.b.allRevisions(func(r Revision, err error) bool {
		if err != nil {
			known[r.ID] = revisionInfo{}
			return c.report(Problem{Area: AreaHistory, Target: "revisions " + r.ID, Detail: "the row cannot be decoded"})
		}
		d, err := digest(r.Hash)
		if err != nil {
			return true
		}
		known[r.ID] = revisionInfo{r.Entry, true}
		listing = append(listing, revision{r.ID, r.Entry})
		refs = append(refs, objectRef{id: r.ID, hash: d})
		return true
	})
	if err != nil {
		return nil, err
	}
	if err := c.stopError(); err != nil {
		return nil, err
	}
	listed := map[[2]string]struct{}{}
	err = t.b.allEntryRevisions(func(entry string, seq int64, id string, err error) bool {
		if err != nil {
			return c.report(Problem{Area: AreaHistory, Detail: err.Error()})
		}
		target := fmt.Sprintf("entryRevisions %s revision %d", entry, seq)
		switch info, ok := known[id]; {
		case !ok:
			c.report(Problem{Area: AreaHistory, Target: target, Detail: "the revision " + id + " is missing"})
		case info.decoded && info.entry != entry:
			c.report(Problem{Area: AreaHistory, Target: target,
				Detail: fmt.Sprintf("the revision %s is of the entry %s", id, info.entry)})
		default:
			listed[[2]string{entry, id}] = struct{}{}
		}
		return !c.stopped
	})
	if err != nil {
		return nil, err
	}
	for _, r := range listing {
		if err := c.stopError(); err != nil {
			return nil, err
		}
		if _, ok := listed[[2]string{r.entry, r.id}]; !ok {
			c.report(Problem{Area: AreaHistory, Target: "revisions " + r.id,
				Detail: fmt.Sprintf("the entry %s does not list it in entryRevisions", r.entry)})
		}
	}
	return refs, c.stopError()
}

// objectVerdict is what checkHistory finds of an object: why it is wrong, "" if it is not, and, for the object of a media
// document, the hash of the original that the document refers to.
type objectVerdict struct {
	problem  string
	original string
}

// readObjects reads the objects of refs, in windows, each object once, and reports a problem for each ref whose object
// is missing or has other data than its hash. It returns the versions of the media documents that no row names, by the
// hash of their originals.
func (c *checker) readObjects(t *Tx, refs []objectRef) (earlierOf, error) {
	verdicts := map[[sha256.Size]byte]objectVerdict{}
	earlier := earlierOf{}
	for len(refs) > 0 {
		if err := c.ctx.Err(); err != nil {
			return nil, err
		}
		chunk := refs[:min(chunkSize, len(refs))]
		refs = refs[len(chunk):]
		var hashes []string
		asked := map[[sha256.Size]byte]struct{}{}
		for _, r := range chunk {
			if _, ok := verdicts[r.hash]; ok {
				continue
			}
			if _, ok := asked[r.hash]; !ok {
				asked[r.hash] = struct{}{}
				hashes = append(hashes, hashText(r.hash[:]))
			}
		}
		// A hint: what it could not read, readObject reads again and reports.
		if err := t.b.preloadObjects(hashes); unchecked(err) {
			return nil, err
		}
		for _, r := range chunk {
			v, ok := verdicts[r.hash]
			if !ok {
				var err error
				if v, err = c.readObject(t, r); err != nil {
					return nil, err
				}
				verdicts[r.hash] = v
			}
			if v.problem != "" && !c.report(Problem{Area: AreaHistory, Target: r.target(), Detail: v.problem}) {
				return nil, errStop
			}
			if r.kind == content.RefMedia && !r.current && v.original != "" {
				earlier[v.original] = append(earlier[v.original], mediaVersion{r.id, r.seq})
			}
		}
		t.b.forget()
	}
	return earlier, nil
}

// readObject reads the object of r and checks its data against its hash. The error is that of the end of ctx or an
// unreachable daemon.
func (c *checker) readObject(t *Tx, r objectRef) (objectVerdict, error) {
	hash := hashText(r.hash[:])
	data, ok, err := t.b.object(hash)
	switch {
	case unchecked(err):
		return objectVerdict{}, err
	case err != nil:
		return objectVerdict{problem: err.Error()}, nil
	case !ok:
		return objectVerdict{problem: "the object " + hash + " is missing"}, nil
	}
	if err := verify(hash, data); err != nil {
		return objectVerdict{problem: err.Error()}, nil
	}
	var v objectVerdict
	if r.kind == content.RefMedia {
		if doc, err := checkData(content.RefMedia, r.id, data); err == nil {
			if m, ok := doc.(*content.Media); ok {
				v.original = m.Hash
			}
		}
	}
	return v, nil
}
