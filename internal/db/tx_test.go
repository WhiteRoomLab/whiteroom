package db

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// hold starts a Write of d that waits in f until release is called, and returns when f has started. release returns
// the error of that Write; the end of the test calls it too.
func hold(t *testing.T, d *DB) (release func() error) {
	t.Helper()
	started, done := make(chan struct{}), make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- d.Write(context.Background(), func(*sql.Tx) error {
			close(started)
			<-done
			return nil
		})
	}()
	select {
	case <-started:
	case err := <-errc:
		t.Fatalf("Write() = %v", err)
	}
	release = sync.OnceValue(func() error {
		close(done)
		return <-errc
	})
	t.Cleanup(func() { _ = release() })
	return release
}

func TestReadCannotWrite(t *testing.T) {
	d, _ := newDB(t)
	err := d.Read(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO objects (hash, data) VALUES ('h', x'00')")
		return err
	})
	if want := "attempt to write a readonly database"; err == nil || err.Error() != want {
		t.Errorf("Read() that writes = %v, want %q", err, want)
	}
	if n := count(t, d, "objects"); n != 0 {
		t.Errorf("objects after Read() = %d, want 0", n)
	}
}

func TestSnapshot(t *testing.T) {
	d, _ := newDB(t)
	var first, second int
	err := d.Read(t.Context(), func(tx *sql.Tx) error {
		exec(t, d, "INSERT INTO objects (hash, data) VALUES ('a', x'00')")
		if err := tx.QueryRow("SELECT count(*) FROM objects").Scan(&first); err != nil {
			return err
		}
		exec(t, d, "INSERT INTO objects (hash, data) VALUES ('b', x'00')")
		return tx.QueryRow("SELECT count(*) FROM objects").Scan(&second)
	})
	// The first Write commits before the first read, and the second one after it.
	if err != nil || first != 1 || second != 1 {
		t.Errorf("objects in Read() = %d, then %d, %v; want 1, then 1", first, second, err)
	}
	if n := count(t, d, "objects"); n != 2 {
		t.Errorf("objects in a new Read() = %d, want 2", n)
	}
}

func TestError(t *testing.T) {
	d, _ := newDB(t)
	errTest := errors.New("test")
	err := d.Write(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO objects (hash, data) VALUES ('h', x'00')"); err != nil {
			return err
		}
		return errTest
	})
	if !errors.Is(err, errTest) || err.Error() != errTest.Error() {
		t.Errorf("Write() = %v, want the error of f", err)
	}
	if n := count(t, d, "objects"); n != 0 {
		t.Errorf("objects after Write() = %d, want 0", n)
	}
	err = d.Read(t.Context(), func(*sql.Tx) error { return errTest })
	if !errors.Is(err, errTest) || err.Error() != errTest.Error() {
		t.Errorf("Read() = %v, want the error of f", err)
	}
}

func TestPanic(t *testing.T) {
	d, _ := newDB(t)
	p := func() (r any) {
		defer func() { r = recover() }()
		_ = d.Write(t.Context(), func(tx *sql.Tx) error {
			if _, err := tx.Exec("INSERT INTO objects (hash, data) VALUES ('h', x'00')"); err != nil {
				return err
			}
			panic("test")
		})
		return nil
	}()
	if p != "test" {
		t.Errorf("panic of Write() = %v, want the panic of f", p)
	}
	if n := count(t, d, "objects"); n != 0 {
		t.Errorf("objects after the panic = %d, want 0", n)
	}
	exec(t, d, "INSERT INTO objects (hash, data) VALUES ('h', x'00')")
	if n := count(t, d, "objects"); n != 1 {
		t.Errorf("objects after the next Write() = %d, want 1", n)
	}
}

func TestOneWriter(t *testing.T) {
	d, _ := newDB(t)
	exec(t, d, "CREATE TABLE counter (n INTEGER NOT NULL) STRICT")
	exec(t, d, "INSERT INTO counter (n) VALUES (0)")
	const writers, writes = 8, 10
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range writes {
				if err := d.Write(t.Context(), func(tx *sql.Tx) error {
					var n int
					if err := tx.QueryRow("SELECT n FROM counter").Scan(&n); err != nil {
						return err
					}
					_, err := tx.Exec("UPDATE counter SET n = ?", n+1)
					return err
				}); err != nil {
					t.Errorf("Write() = %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	var n int
	if err := d.Read(t.Context(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT n FROM counter").Scan(&n)
	}); err != nil || n != writers*writes {
		t.Errorf("counter = %d, %v; want %d", n, err, writers*writes)
	}
}

func TestWaitingWriterContext(t *testing.T) {
	d, _ := newDB(t)
	release := hold(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	called := false
	err := d.Write(ctx, func(*sql.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Errorf("Write() while another writes = %v (f called: %v), want context.DeadlineExceeded", err, called)
	}
	if err := release(); err != nil {
		t.Errorf("the other Write() = %v", err)
	}
}

// TestOtherDB opens the file twice, as two processes do.
func TestOtherDB(t *testing.T) {
	d, p := newDB(t)
	release := hold(t, d)
	// It would fail after the busy timeout if it waited for the write lock.
	other, err := open(p, false, options{busyTimeout: 100 * time.Millisecond, migrations: migrations})
	if err != nil {
		t.Fatalf("open() while another DB writes = %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	err = other.Write(t.Context(), func(*sql.Tx) error { return nil })
	var se sqlite3.Error
	if !errors.As(err, &se) || se.Code != sqlite3.ErrBusy || err.Error() != "database is locked" {
		t.Errorf("Write() while another DB writes = %v, want sqlite3.ErrBusy", err)
	}
	if n := count(t, other, "objects"); n != 0 {
		t.Errorf("objects in Read() while another DB writes = %d, want 0", n)
	}
	if err := release(); err != nil {
		t.Errorf("Write() of the first DB = %v", err)
	}
	exec(t, other, "INSERT INTO objects (hash, data) VALUES ('h', x'00')")
	if n := count(t, d, "objects"); n != 1 {
		t.Errorf("objects that the first DB reads = %d, want 1", n)
	}
}
