package logdb

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"
)

// GCResult counts the files that CollectGarbage has removed.
type GCResult struct {
	Segments int // of other generations
	Objects  int // files of objects that the state does not have in files
	Temps    int // temporary files of objects and of checkpoints
}

// CollectGarbage removes the files that the state does not need: the segments of the other generations, which a
// compaction that has failed or crashed leaves; the files of objects that the state does not have in files, which a
// commit that failed after it wrote them, or a compaction that dropped them, leaves; and the temporary files of objects
// and of checkpoints, which failed syncs and crashes leave. Names that the engine does not make stay. It waits for a
// Compact that runs until ctx ends.
func (m *Maintenance) CollectGarbage(ctx context.Context) (GCResult, error) {
	db := m.db
	if err := db.turn(ctx); err != nil {
		return GCResult{}, err
	}
	defer func() { <-db.sem }()
	switch {
	case db.closed.Load():
		return GCResult{}, errClosed
	case db.failed != nil:
		return GCResult{}, db.failed
	}
	var res GCResult
	st := db.cur.Load()
	if err := db.gcSegments(ctx, st.gen, &res); err != nil {
		return res, err
	}
	if err := db.gcObjects(ctx, st, &res); err != nil {
		return res, err
	}
	return res, db.gcCheckpointTemps(ctx, &res)
}

// gcSegments removes the segments of the generations other than gen, from the last one, and syncs log/ after each, so
// that a crash leaves no gap in a newer generation, which recovery would refuse.
func (db *DB) gcSegments(ctx context.Context, gen uint64, res *GCResult) error {
	segs, err := listSegments(db.fsys)
	if err != nil {
		return err
	}
	for _, s := range slices.Backward(segs) {
		if s.gen == gen {
			continue
		}
		if err := db.gcRemove(ctx, s.name); err != nil {
			return err
		}
		res.Segments++
		if err := db.fsys.SyncDir("log"); err != nil {
			return db.fail(err)
		}
	}
	return nil
}

// gcObjects removes, in each directory of objects/, the files of objects that st does not have in files, and the
// temporary files, and then syncs the directory.
func (db *DB) gcObjects(ctx context.Context, st *state, res *GCResult) error {
	dirs, err := db.fsys.ReadDir("objects")
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if !d.IsDir() || !isHex(d.Name(), 2) {
			continue
		}
		dir := "objects/" + d.Name()
		entries, err := db.fsys.ReadDir(dir)
		if err != nil {
			return err
		}
		removed := false
		for _, e := range entries {
			name := e.Name()
			temp := strings.HasPrefix(name, ".tmp-")
			unused := isHex(name, 64) && name[:2] == d.Name() && !db.hasFile(st, name)
			if !e.Type().IsRegular() || !temp && !unused {
				continue
			}
			if err := db.gcRemove(ctx, dir+"/"+name); err != nil {
				return err
			}
			if temp {
				res.Temps++
			} else {
				res.Objects++
			}
			removed = true
		}
		if removed {
			if err := db.fsys.SyncDir(dir); err != nil {
				return db.fail(err)
			}
		}
	}
	return nil
}

// hasFile reports whether st has the object whose hash has the hex digits h in a file.
func (db *DB) hasFile(st *state, h string) bool {
	hash, _ := hex.DecodeString(h)
	loc, ok := st.objects.Get(string(hash))
	return ok && loc.placement == placeFile
}

// gcCheckpointTemps removes the temporary files of checkpoints, and then syncs the directory of the store.
func (db *DB) gcCheckpointTemps(ctx context.Context, res *GCResult) error {
	entries, err := db.fsys.ReadDir(".")
	if err != nil {
		return err
	}
	removed := false
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), ".tmp-checkpoint-") {
			continue
		}
		if err := db.gcRemove(ctx, e.Name()); err != nil {
			return err
		}
		res.Temps++
		removed = true
	}
	if removed {
		if err := db.fsys.SyncDir("."); err != nil {
			return db.fail(err)
		}
	}
	return nil
}

// gcRemove removes the file name, after the hook gc:remove, unless ctx has ended.
func (db *DB) gcRemove(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := db.hook("gc:remove"); err != nil {
		return err
	}
	return db.fsys.Remove(name)
}

// isHex reports whether s has n lower case hex digits.
func isHex(s string, n int) bool {
	_, ok := parseHex(s)
	return len(s) == n && ok
}
