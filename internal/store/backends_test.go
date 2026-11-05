package store

import (
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// testBackend is a backend that the tests run on: SQLite if log is nil, else the engine of internal/logdb with *log.
type testBackend struct {
	name string
	log  *logdb.Options
}

// writerWait is how long a store of the log backend waits for the writer lock that another process holds (2 seconds by
// default), so that the tests of other processes do not wait long.
const writerWait = 50 * time.Millisecond

// allBackends are the backends that the tests run on: SQLite, and the log backend in each placement of objects. With a
// threshold of 50 bytes, hybrid puts the JSON of docJSON in files, and that of revJSON and of the settings document
// inline. The log backend writes the checkpoints that the growth of its log starts in the Update, not in the
// background, so that no goroutine of the engine calls the hook of the store, which the tests change.
var allBackends = []testBackend{
	{name: "sqlite"},
	{name: "log-inline", log: &logdb.Options{Placement: logdb.Inline, WriterWait: writerWait, SyncCheckpoints: true}},
	{name: "log-files", log: &logdb.Options{Placement: logdb.Files, WriterWait: writerWait, SyncCheckpoints: true}},
	{name: "log-hybrid", log: &logdb.Options{Placement: logdb.Hybrid, Threshold: 50, WriterWait: writerWait,
		SyncCheckpoints: true}},
}

var (
	// twoBackends are SQLite and the log backend with inline objects, for what every backend does with the same code,
	// such as the originals.
	twoBackends = allBackends[:2:2]
	sqliteOnly  = allBackends[:1:1]
	logBackends = allBackends[1:]
)

// forBackends runs f in a subtest of t, named by the backend, for each backend of set.
func forBackends(t *testing.T, set []testBackend, f func(t *testing.T, tb testBackend)) {
	t.Helper()
	for _, tb := range set {
		t.Run(tb.name, func(t *testing.T) { f(t, tb) })
	}
}

// backendByName returns the backend of allBackends with name.
func backendByName(name string) (testBackend, bool) {
	for _, tb := range allBackends {
		if tb.name == name {
			return tb, true
		}
	}
	return testBackend{}, false
}

func (tb testBackend) options() options {
	return options{log: tb.log}
}

// create makes a data directory with a store of tb at dir, as Create does for SQLite.
func (tb testBackend) create(dir string) (*Store, error) {
	if tb.log == nil {
		return Create(dir)
	}
	return create(dir, tb.options())
}

// open opens the store of tb in the data directory dir, as Open does for SQLite.
func (tb testBackend) open(dir string) (*Store, error) {
	if tb.log == nil {
		return Open(dir)
	}
	return open(dir, tb.options())
}

// newStore creates a store of tb in a new directory, and returns it with the directory. The end of the test closes it.
func newStore(t *testing.T, tb testBackend) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := tb.create(dir)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	s.now = func() time.Time { return testTime }
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// newDir creates a data directory of tb, and returns it with its store closed.
func newDir(t *testing.T, tb testBackend) string {
	t.Helper()
	s, dir := newStore(t, tb)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return dir
}

// reopen opens the store of tb in the data directory dir after a crash, and checks it. The end of the test closes it.
func reopen(t *testing.T, tb testBackend, dir string) *Store {
	t.Helper()
	s, err := tb.open(dir)
	if err != nil {
		t.Fatalf("Open() after the crash = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	checkIntegrity(t, s)
	checkConsistent(t, s)
	return s
}

// The helpers below look past the API of the store, at what its backend keeps; each calls that of the backend of s. A
// table is one of SQLite: objects, versions, documents or revisions.

// count returns the number of rows of table in s.
func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	if _, ok := s.b.(*logBackend); ok {
		return countLog(t, s, table)
	}
	return countSQLite(t, s, table)
}

// checkConsistent checks that the current version of every document is its latest one.
func checkConsistent(t *testing.T, s *Store) {
	t.Helper()
	if _, ok := s.b.(*logBackend); ok {
		checkConsistentLog(t, s)
		return
	}
	checkConsistentSQLite(t, s)
}

// checkIntegrity checks that the backend finds what it keeps intact, and that the data of every object has its hash.
func checkIntegrity(t *testing.T, s *Store) {
	t.Helper()
	if _, ok := s.b.(*logBackend); ok {
		checkIntegrityLog(t, s)
		return
	}
	checkIntegritySQLite(t, s)
}

// savedAts returns the saved_at of the rows of table in s, versions or revisions, in the order of saving, as they are
// in the table.
func savedAts(t *testing.T, s *Store, table string) []string {
	t.Helper()
	if _, ok := s.b.(*logBackend); ok {
		return savedAtsLog(t, s, table)
	}
	return savedAtsSQLite(t, s, table)
}

// corruptObjects changes the data of every object of s behind the back of the store, and returns what the data of an
// object, original, is after it.
func corruptObjects(t *testing.T, s *Store) func(original []byte) []byte {
	t.Helper()
	if _, ok := s.b.(*logBackend); ok {
		return corruptObjectsLog(t, s)
	}
	return corruptObjectsSQLite(t, s)
}

// corruptSavedAt changes the saved_at of every revision of s to value behind the back of the store.
func corruptSavedAt(t *testing.T, s *Store, value string) {
	t.Helper()
	if _, ok := s.b.(*logBackend); ok {
		corruptSavedAtLog(t, s, value)
		return
	}
	corruptSavedAtSQLite(t, s, value)
}

// dataLayout is what a backend keeps in a data directory: the mode of each file and directory in it, by its path in
// the data directory ("." for itself), after create, and after Close.
type dataLayout struct {
	created, closed map[string]fs.FileMode
}

// layout returns the layout of a data directory of tb, before anything is written to its store.
func layout(tb testBackend) dataLayout {
	if tb.log == nil {
		return sqliteLayout()
	}
	return logLayout()
}

// openErrorCase is a change of a data directory after which open fails with an error of a file in it.
type openErrorCase struct {
	name  string
	setup func(t *testing.T, path func(name string) string) // changes the data directory
	file  string                                            // the file of the error in the data directory
	want  string                                            // the error after the path of file
}

// openErrorCases returns the changes of the data directory of tb, a closed store with nothing in it, after which open
// fails, but those of files/, which all backends have.
func openErrorCases(tb testBackend) []openErrorCase {
	if tb.log == nil {
		return sqliteOpenErrors()
	}
	return logOpenErrors()
}
