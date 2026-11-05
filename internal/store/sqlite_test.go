package store

import (
	"database/sql"
	"io/fs"
	"os"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/db"
)

// sqliteDB returns the database of s, a store on SQLite.
func sqliteDB(t *testing.T, s *Store) *db.DB {
	t.Helper()
	b, ok := s.b.(*sqliteBackend)
	if !ok {
		t.Fatalf("the backend of the store is %T, want SQLite", s.b)
	}
	return b.db
}

// countSQLite returns the number of rows of table in s, a store on SQLite.
func countSQLite(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := sqliteDB(t, s).Read(t.Context(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
	}); err != nil {
		t.Fatalf("count of the rows of %s = %v", table, err)
	}
	return n
}

// consistency counts the documents whose row in documents is not their latest version: a row whose version is not
// the latest, a row although the latest version is a deletion, or no row although it is not.
const consistency = `SELECT count(*) FROM (SELECT kind, id, max(seq) AS seq FROM versions GROUP BY kind, id) AS latest
JOIN versions AS v ON v.seq = latest.seq
LEFT JOIN documents AS d ON d.kind = latest.kind AND d.id = latest.id
WHERE (v.object IS NULL) != (d.version IS NULL) OR d.version != latest.seq`

// checkConsistentSQLite is checkConsistent of a store on SQLite.
func checkConsistentSQLite(t *testing.T, s *Store) {
	t.Helper()
	var n int
	if err := sqliteDB(t, s).Read(t.Context(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), consistency).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("documents that are not at their latest versions = %d, %v; want 0", n, err)
	}
}

// checkIntegritySQLite checks that SQLite finds the database of s intact, and that the data of every object has its
// hash.
func checkIntegritySQLite(t *testing.T, s *Store) {
	t.Helper()
	var result string
	var changed []string
	if err := sqliteDB(t, s).Read(t.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&result); err != nil {
			return err
		}
		rows, err := tx.QueryContext(t.Context(), "SELECT hash, data FROM objects")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var hash string
			var data []byte
			if err := rows.Scan(&hash, &data); err != nil {
				return err
			}
			if sum(data) != hash {
				changed = append(changed, hash)
			}
		}
		return rows.Err()
	}); err != nil || result != "ok" || changed != nil {
		t.Errorf("integrity_check = %q, objects with other data %q, %v; want ok and none", result, changed, err)
	}
}

// savedAtsSQLite is savedAts of a store on SQLite.
func savedAtsSQLite(t *testing.T, s *Store, table string) []string {
	t.Helper()
	var got []string
	if err := sqliteDB(t, s).Read(t.Context(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(t.Context(), "SELECT saved_at FROM "+table+" ORDER BY seq")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var at string
			if err := rows.Scan(&at); err != nil {
				return err
			}
			got = append(got, at)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("saved_at of the versions = %v", err)
	}
	return got
}

// corruptObjectsSQLite changes the data of every object of s, a store on SQLite, to one JSON, past the trigger that
// keeps it from changing.
func corruptObjectsSQLite(t *testing.T, s *Store) func(original []byte) []byte {
	t.Helper()
	bad := docJSON(content.RefEntry, pageID)
	if err := sqliteDB(t, s).Write(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "DROP TRIGGER objects_no_update"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "UPDATE objects SET data = ?", bad)
		return err
	}); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	return func([]byte) []byte { return bad }
}

// corruptSavedAtSQLite changes the saved_at of every revision of s, a store on SQLite, to value, past the trigger that
// keeps it from changing.
func corruptSavedAtSQLite(t *testing.T, s *Store, value string) {
	t.Helper()
	if err := sqliteDB(t, s).Write(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "DROP TRIGGER revisions_no_update"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "UPDATE revisions SET saved_at = ?", value)
		return err
	}); err != nil {
		t.Fatalf("Write() = %v", err)
	}
}

// sqliteLayout is the layout of a data directory of SQLite: whiteroom.db, with its WAL and shared memory while the
// store is open, and files/.
func sqliteLayout() dataLayout {
	const dirMode, fileMode = fs.ModeDir | 0o700, fs.FileMode(0o600)
	return dataLayout{
		created: map[string]fs.FileMode{
			".": dirMode, "files": dirMode, "whiteroom.db": fileMode, "whiteroom.db-wal": fileMode,
			"whiteroom.db-shm": fileMode,
		},
		closed: map[string]fs.FileMode{".": dirMode, "files": dirMode, "whiteroom.db": fileMode},
	}
}

// sqliteOpenErrors are the changes of whiteroom.db after which Open fails.
func sqliteOpenErrors() []openErrorCase {
	return []openErrorCase{
		{
			name:  "missing whiteroom.db",
			setup: func(t *testing.T, path func(string) string) { remove(t, path("whiteroom.db")) },
			file:  "whiteroom.db",
			want:  "missing file",
		},
		{
			name: "whiteroom.db as a symbolic link",
			setup: func(t *testing.T, path func(string) string) {
				rename(t, path("whiteroom.db"), path("other.db"))
				symlink(t, "other.db", path("whiteroom.db"))
			},
			file: "whiteroom.db",
			want: "symbolic links are not allowed",
		},
		{
			name: "whiteroom.db as a directory",
			setup: func(t *testing.T, path func(string) string) {
				remove(t, path("whiteroom.db"))
				mkdir(t, path("whiteroom.db"))
			},
			file: "whiteroom.db",
			want: "must be a regular file",
		},
		{
			name: "whiteroom.db of another program",
			setup: func(t *testing.T, path func(string) string) {
				remove(t, path("whiteroom.db"))
				if err := os.WriteFile(path("whiteroom.db"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			file: "whiteroom.db",
			want: "not a database of whiteroom (application_id 0x0)",
		},
	}
}
