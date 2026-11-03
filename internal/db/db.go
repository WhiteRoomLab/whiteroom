// Package db creates and opens the SQLite database of whiteroom (3.4), brings its tables to the latest version, and
// runs transactions on it: many that read at the same time, and one that writes at a time.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	// The driver "sqlite3" of sql.Open.
	_ "github.com/mattn/go-sqlite3"
)

const applicationID = 0x5768526D // "WhRm"

// DB is an SQLite database file of whiteroom in WAL mode, with a pool of connections that read and a single
// connection that writes. Its methods may be called concurrently.
type DB struct {
	read, write *sql.DB
}

type options struct {
	busyTimeout time.Duration
	migrations  []string
}

// Create makes the database file at path, which must not exist, with the tables of the latest version. When it fails
// after making the file, the file stays.
func Create(path string) (*DB, error) {
	return open(path, true, options{busyTimeout: 5 * time.Second, migrations: migrations})
}

// Open opens the database file at path that Create has made, and brings its tables to the latest version. It refuses
// other files, and tables of a newer version, without changing them.
func Open(path string) (*DB, error) {
	return open(path, false, options{busyTimeout: 5 * time.Second, migrations: migrations})
}

// uriPath escapes the characters that SQLite decodes, or ends the path at, in a URI filename.
var uriPath = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

func open(path string, create bool, o options) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if create {
		// SQLite gives the -wal and -shm files the mode of this file.
		if err := openClose(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
			return nil, err
		}
	} else {
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: not a regular file", path)
		}
		// SQLite would open a file that it cannot write as read-only, and fail only at the first write.
		if err := openClose(abs, os.O_RDWR, 0); err != nil {
			return nil, err
		}
	}
	// The pools open connections at any time, so every setting is in the DSN.
	dsn := "file:" + uriPath.Replace(abs) +
		"?mode=rw&_synchronous=FULL&_foreign_keys=1&_recursive_triggers=1&_busy_timeout=" +
		strconv.FormatInt(o.busyTimeout.Milliseconds(), 10)
	write, err := sql.Open("sqlite3", dsn+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	read, err := sql.Open("sqlite3", dsn+"&_query_only=1")
	if err != nil {
		_ = write.Close()
		return nil, err
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	n := max(4, runtime.GOMAXPROCS(0))
	read.SetMaxOpenConns(n)
	read.SetMaxIdleConns(n)
	d := &DB{read: read, write: write}
	if err := d.prepare(create, o.migrations); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

func openClose(path string, flag int, perm fs.FileMode) error {
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return err
	}
	return f.Close()
}

// prepare checks that the file is a new one or one of whiteroom before it writes anything, then sets WAL mode,
// brings the tables to the latest version of migrations, and checks that the readers can open the file.
func (d *DB) prepare(create bool, migrations []string) error {
	ctx := context.Background()
	var app, version int64
	var mode string
	const q = "SELECT * FROM pragma_application_id, pragma_user_version, pragma_journal_mode"
	if err := d.write.QueryRowContext(ctx, q).Scan(&app, &version, &mode); err != nil {
		return err
	}
	if err := check(create, app, version, len(migrations)); err != nil {
		return err
	}
	if mode != "wal" {
		if err := d.write.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
			return err
		}
		if mode != "wal" {
			return fmt.Errorf("cannot use WAL mode: journal_mode is %q", mode)
		}
	}
	// Only a migration takes the write lock, which another process may hold for a while.
	if create || version < int64(len(migrations)) {
		if err := d.Write(ctx, func(tx *sql.Tx) error { return migrate(tx, create, migrations) }); err != nil {
			return err
		}
	}
	return d.read.PingContext(ctx)
}

// check returns why open refuses a file with application_id app and user_version version, or nil.
func check(create bool, app, version int64, latest int) error {
	switch {
	case create && (app != 0 || version != 0):
		return errors.New("not a new database")
	case !create && app != applicationID:
		return fmt.Errorf("not a database of whiteroom (application_id 0x%X)", uint32(app))
	case version < 0:
		return fmt.Errorf("the tables are of an invalid version %d", version)
	case version > int64(latest):
		return fmt.Errorf("the tables are of version %d, newer than this program knows (%d)", version, latest)
	}
	return nil
}

// migrate brings the tables to the latest version of migrations, unless another process has done it.
func migrate(tx *sql.Tx, create bool, migrations []string) error {
	var app, version int64
	if err := tx.QueryRow("SELECT * FROM pragma_application_id, pragma_user_version").Scan(&app, &version); err != nil {
		return err
	}
	if err := check(create, app, version, len(migrations)); err != nil {
		return err
	}
	if create {
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
			return err
		}
	} else if version == int64(len(migrations)) {
		return nil
	}
	for v := int(version); v < len(migrations); v++ {
		if _, err := tx.Exec(migrations[v]); err != nil {
			return fmt.Errorf("migrating the tables to version %d: %w", v+1, err)
		}
	}
	_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)))
	return err
}

func (d *DB) Close() error {
	return errors.Join(d.read.Close(), d.write.Close())
}

// Read runs f in a transaction that only reads, and that sees the database as it was when f first read it.
func (d *DB) Read(ctx context.Context, f func(*sql.Tx) error) error {
	return run(ctx, d.read, &sql.TxOptions{ReadOnly: true}, f)
}

// Write runs f in a transaction that writes, and commits it if f returns nil. Transactions that write run one at a
// time: Write waits for the others of d until ctx ends, and for those of other processes up to 5 seconds.
func (d *DB) Write(ctx context.Context, f func(*sql.Tx) error) error {
	return run(ctx, d.write, nil, f)
}

func run(ctx context.Context, pool *sql.DB, opts *sql.TxOptions, f func(*sql.Tx) error) error {
	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	panicked := true
	defer func() {
		if panicked {
			_ = tx.Rollback()
		}
	}()
	err = f(tx)
	panicked = false
	if err != nil {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			return errors.Join(err, rerr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		// When ctx ends, database/sql rolls back, and Commit may fail with sql.ErrTxDone.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}
