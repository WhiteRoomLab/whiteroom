package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// newDB creates a database in a new directory and returns it with its path. The end of the test closes it.
func newDB(t *testing.T) (*DB, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "whiteroom.db")
	d, err := Create(p)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, p
}

// exec runs query with args in a transaction that writes.
func exec(t *testing.T, d *DB, query string, args ...any) {
	t.Helper()
	if err := d.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		t.Fatalf("Write() of %q = %v", query, err)
	}
}

// count returns the number of rows of table, in a transaction that reads.
func count(t *testing.T, d *DB, table string) int {
	t.Helper()
	var n int
	if err := d.Read(t.Context(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT count(*) FROM " + table).Scan(&n)
	}); err != nil {
		t.Fatalf("Read() of the rows of %s = %v", table, err)
	}
	return n
}

// rows returns the rows of query, each with its columns joined by spaces.
func rows(tx *sql.Tx, query string, args ...any) ([]string, error) {
	rs, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	columns, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	var result []string
	for rs.Next() {
		values := make([]string, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rs.Scan(dest...); err != nil {
			return nil, err
		}
		result = append(result, strings.Join(values, " "))
	}
	return result, rs.Err()
}

// tables returns the user_version of d and the names of its tables.
func tables(t *testing.T, d *DB) (int, []string) {
	t.Helper()
	var version int
	var names []string
	if err := d.Read(t.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		var err error
		names, err = rows(tx, "SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name")
		return err
	}); err != nil {
		t.Fatalf("Read() of the tables = %v", err)
	}
	return version, names
}

// symlink makes a symbolic link at name to target.
func symlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot make symbolic links: %v", err)
	}
}

func TestCreateOpen(t *testing.T) {
	parent := t.TempDir()
	// SQLite decodes "%41" and ends the path at "?" or "#", unless they are escaped.
	dir := filepath.Join(parent, "a b%41c?d#e 日本語 &x=y")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "whiteroom.db")
	d, err := Create(p)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	exec(t, d, "INSERT INTO objects (hash, data) VALUES ('h', x'00ff')")
	for _, name := range []string{p, p + "-wal", p + "-shm"} {
		if info, err := os.Lstat(name); err != nil || info.Mode() != 0o600 {
			t.Errorf("Lstat(%q) = %v, %v; want a regular file of mode 0600", name, info, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(dir) {
		t.Errorf("entries beside the directory = %v, want only the directory", entries)
	}

	d, err = Open(p)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	var data []byte
	if err := d.Read(t.Context(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT data FROM objects WHERE hash = 'h'").Scan(&data)
	}); err != nil || !bytes.Equal(data, []byte{0x00, 0xff}) {
		t.Errorf("data after Open() = %x, %v; want 00ff", data, err)
	}
	if n := count(t, d, "objects"); n != 1 {
		t.Errorf("objects after Open() = %d, want 1", n)
	}
}

func TestCreateExisting(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.db")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.db")
	symlink(t, "missing.db", link)
	for _, p := range []string{file, link} {
		d, err := Create(p)
		if err == nil {
			_ = d.Close()
		}
		if !errors.Is(err, fs.ErrExist) {
			t.Errorf("Create(%q) = %v, want an error that wraps fs.ErrExist", p, err)
		}
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "data" {
		t.Errorf("the existing file after Create() = %q, %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "missing.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat() of the target of the link after Create() = %v, want fs.ErrNotExist", err)
	}
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	path := func(name string) string { return filepath.Join(dir, name) }
	d, err := Create(path("whiteroom.db"))
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	symlink(t, "whiteroom.db", path("link.db"))
	if err := os.Mkdir(path("dir.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path("text.db"), []byte(strings.Repeat("not a database\n", 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path("empty.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite3", path("other.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec("CREATE TABLE notes (note TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	d = openWith(t, path("newer.db"), true, append(slices.Clone(migrations), "CREATE TABLE extra (x)"))
	if err := d.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	d, err = Create(path("invalid.db"))
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	exec(t, d, "PRAGMA user_version = -1")
	if err := d.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	newer := fmt.Sprintf("the tables are of version %d, newer than this program knows (%d)", len(migrations)+1,
		len(migrations))
	for _, tt := range []struct {
		name string
		want string // "" for a missing file
	}{
		{name: "missing.db"},
		{name: "link.db", want: "not a regular file"},
		{name: "dir.db", want: "not a regular file"},
		{name: "text.db", want: "file is not a database"},
		{name: "empty.db", want: "not a database of whiteroom (application_id 0x0)"},
		{name: "other.db", want: "not a database of whiteroom (application_id 0x0)"},
		{name: "newer.db", want: newer},
		{name: "invalid.db", want: "the tables are of an invalid version -1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := path(tt.name)
			before, _ := os.ReadFile(p)
			d, err := Open(p)
			if err == nil {
				_ = d.Close()
			}
			if tt.want == "" {
				var pe *fs.PathError
				if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pe) || pe.Path != p {
					t.Errorf("Open() = %v, want an error of the path that wraps fs.ErrNotExist", err)
				}
			} else if want := p + ": " + tt.want; err == nil || err.Error() != want {
				t.Errorf("Open() = %v, want %q", err, want)
			}
			if after, _ := os.ReadFile(p); !bytes.Equal(after, before) {
				t.Errorf("Open() has changed the file")
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Lstat(p + suffix); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Lstat() of %s%s after Open() = %v, want fs.ErrNotExist", tt.name, suffix, err)
				}
			}
		})
	}
	if _, err := os.Lstat(path("missing.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat() of the missing file after Open() = %v, want fs.ErrNotExist", err)
	}
}

func TestOpenReadOnlyFile(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("every file is writable") // to root, or with a mode that Windows ignores
	}
	d, p := newDB(t)
	if err := d.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := os.Chmod(p, 0o400); err != nil {
		t.Fatal(err)
	}
	d, err := Open(p)
	if err == nil {
		_ = d.Close()
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("Open() of a file of mode 0400 = %v, want an error that wraps fs.ErrPermission", err)
	}
}

// openWith opens the database at p as Create does if create, else as Open does, but with the migrations ms. The end
// of the test closes it.
func openWith(t *testing.T, p string, create bool, ms []string) *DB {
	t.Helper()
	d, err := open(p, create, options{busyTimeout: 5 * time.Second, migrations: ms})
	if err != nil {
		t.Fatalf("open() with %d migrations = %v", len(ms), err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestMigrations(t *testing.T) {
	d, _ := newDB(t)
	version, names := tables(t, d)
	if version != len(migrations) {
		t.Fatalf("user_version after Create() = %d, want %d", version, len(migrations))
	}
	more := append(slices.Clone(migrations), "CREATE TABLE extra (x)")
	moreNames := slices.Sorted(slices.Values(append(slices.Clone(names), "extra")))
	checkTables := func(t *testing.T, d *DB, version int, names []string) {
		t.Helper()
		if gotVersion, got := tables(t, d); gotVersion != version || !slices.Equal(got, names) {
			t.Errorf("tables = %d, %q; want %d, %q", gotVersion, got, version, names)
		}
	}

	t.Run("new file", func(t *testing.T) {
		d := openWith(t, filepath.Join(t.TempDir(), "whiteroom.db"), true, more)
		checkTables(t, d, len(more), moreNames)
	})

	t.Run("file of the latest version", func(t *testing.T) {
		d, p := newDB(t)
		if err := d.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		checkTables(t, openWith(t, p, false, more), len(more), moreNames)
	})

	t.Run("failing migration", func(t *testing.T) {
		d, p := newDB(t)
		if err := d.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		bad := append(slices.Clone(migrations), "CREATE TABLE extra (x); CREATE TABLE objects (x)")
		d, err := open(p, false, options{busyTimeout: 5 * time.Second, migrations: bad})
		if err == nil {
			_ = d.Close()
		}
		want := fmt.Sprintf("%s: migrating the tables to version %d: table objects already exists", p, len(bad))
		if err == nil || err.Error() != want {
			t.Errorf("open() with a failing migration = %v, want %q", err, want)
		}
		checkTables(t, openWith(t, p, false, migrations), len(migrations), names)
	})
}

func TestSettings(t *testing.T) {
	d, _ := newDB(t)
	type settings struct {
		journalMode                                          string
		synchronous, foreignKeys, recursiveTriggers, timeout int
		queryOnly, applicationID, userVersion                int
	}
	var got settings
	read := func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT * FROM pragma_journal_mode, pragma_synchronous, pragma_foreign_keys,
			pragma_recursive_triggers, pragma_busy_timeout, pragma_query_only, pragma_application_id, pragma_user_version`).
			Scan(&got.journalMode, &got.synchronous, &got.foreignKeys, &got.recursiveTriggers, &got.timeout,
				&got.queryOnly, &got.applicationID, &got.userVersion)
	}
	want := settings{"wal", 2, 1, 1, 5000, 0, applicationID, len(migrations)}
	if err := d.Write(t.Context(), read); err != nil || got != want {
		t.Errorf("settings of the writer = %+v, %v; want %+v", got, err, want)
	}
	got = settings{}
	want.queryOnly = 1
	if err := d.Read(t.Context(), read); err != nil || got != want {
		t.Errorf("settings of the readers = %+v, %v; want %+v", got, err, want)
	}
}
