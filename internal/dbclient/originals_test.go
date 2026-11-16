package dbclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// objectLimit is the largest original that the daemons of these tests take.
const objectLimit = 100_000

func withObjectLimit(o *dbtest.Options) { o.Limits.Object = objectLimit }

func originalPath(h [32]byte) string { return fmt.Sprintf("files/%x/%x", h[:1], h) }

// originalFiles returns the files of files/ in the file system of the daemon: path and content.
func (f *fixture) originalFiles() map[string]string {
	m := map[string]string{}
	f.fs.Walk(func(p string, data []byte) {
		if strings.HasPrefix(p, "files/") {
			m[p] = string(data)
		}
	})
	return m
}

// noTemps waits until the daemon has no temporary file of PutOriginal: it removes it when the connection of the request
// ends, which it notices a moment after the client has closed it.
func (f *fixture) noTemps() {
	f.t.Helper()
	eventually(f.t, "the daemon to remove the temporary files", func() bool {
		for p := range f.originalFiles() {
			if strings.Contains(p, ".tmp-") {
				return false
			}
		}
		return true
	})
}

func data(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i%251) + byte(i/251)
	}
	return b
}

func putOriginal(t testing.TB, c *dbclient.Client, b []byte) [32]byte {
	t.Helper()
	h, size, err := c.PutOriginal(bg, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("PutOriginal of %d bytes: %v", len(b), err)
	}
	if h != sha256.Sum256(b) || size != int64(len(b)) {
		t.Fatalf("PutOriginal of %d bytes gave %x and %d", len(b), h, size)
	}
	return h
}

// readOriginal reads the original h to its end.
func readOriginal(t testing.TB, c *dbclient.Client, h [32]byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(bg, waitLimit)
	defer cancel()
	o, err := c.Original(ctx, h)
	if err != nil {
		t.Fatalf("Original: %v", err)
	}
	got, err := io.ReadAll(o)
	if err != nil {
		t.Fatalf("reading the original: %v", err)
	}
	if o.Size() != int64(len(got)) {
		t.Errorf("Size is %d, and the original has %d bytes", o.Size(), len(got))
	}
	must(t, o.Close())
	return got
}

// failingReader gives n bytes and then err.
type failingReader struct {
	n   int
	err error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, r.err
	}
	n := min(len(p), r.n)
	clear(p[:n])
	r.n -= n
	return n, nil
}

// gatedReader gives the bytes of r, and calls before at each Read.
type gatedReader struct {
	r      io.Reader
	before func()
}

func (g *gatedReader) Read(p []byte) (int, error) {
	g.before()
	return g.r.Read(p)
}

// readerWithEOF gives the last bytes with io.EOF.
type readerWithEOF struct{ r *bytes.Reader }

func (r readerWithEOF) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err == nil && r.r.Len() == 0 {
		err = io.EOF
	}
	return n, err
}

// trickleReader gives one byte, then nothing, then one byte.
type trickleReader struct {
	b     []byte
	empty bool
}

func (r *trickleReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	if r.empty = !r.empty; r.empty {
		return 0, nil
	}
	p[0] = r.b[0]
	r.b = r.b[1:]
	return 1, nil
}

// sizeReader notes the largest buffer that it was asked to fill, and how many bytes it has given.
type sizeReader struct {
	r    io.Reader
	most int
	n    int
}

func (r *sizeReader) Read(p []byte) (int, error) {
	r.most = max(r.most, len(p))
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

type nothingReader struct{}

func (nothingReader) Read([]byte) (int, error) { return 0, nil }

func TestOriginals(t *testing.T) {
	t.Run("a round trip", func(t *testing.T) {
		f := newFixture(t, withObjectLimit)
		w, rec := f.writer(dbclient.Options{})
		for i, n := range []int{1, 2, testFrame - 1, testFrame, testFrame + 1, 3*testFrame + 5, 40_000, objectLimit} {
			b := data(n, byte(i))
			h := putOriginal(t, w, b)
			if got := f.originalFiles()[originalPath(h)]; got != string(b) {
				t.Errorf("the file of the original of %d bytes has %d bytes", n, len(got))
			}
			if got := readOriginal(t, w, h); !bytes.Equal(got, b) {
				t.Errorf("the original of %d bytes came back as %d bytes", n, len(got))
			}
		}
		f.noTemps()
		if got := rec.dialed(dbwire.RoleWriter); got != 1 {
			t.Errorf("%d connections of the writer, want the one that all the originals went on", got)
		}
		if got := rec.dialed(dbwire.RoleReader); got != 1 {
			t.Errorf("%d connections of a reader, want the one that all the reads went on", got)
		}
		// The same original twice.
		b := data(5000, 77)
		first := putOriginal(t, w, b)
		if second := putOriginal(t, w, b); first != second {
			t.Error("the same data have two hashes")
		}
		if n := len(f.originalFiles()); n != 9 {
			t.Errorf("files/ has %d files, want 9", n)
		}
	})

	t.Run("readers of other shapes", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		b := data(3*testFrame, 9)
		// The last bytes with io.EOF, and one byte at a time with reads that give nothing between.
		for _, r := range []io.Reader{readerWithEOF{bytes.NewReader(b)}, &trickleReader{b: b}} {
			h, size, err := w.PutOriginal(bg, r)
			if err != nil || size != int64(len(b)) || h != sha256.Sum256(b) {
				t.Errorf("PutOriginal = %x, %d, %v", h, size, err)
			}
		}
		if _, _, err := w.PutOriginal(bg, nothingReader{}); !errors.Is(err, io.ErrNoProgress) {
			t.Errorf("a reader that gives nothing: %v", err)
		}
	})

	t.Run("blocks of at most the frame", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		r := &sizeReader{r: bytes.NewReader(data(20_000, 3))}
		if _, _, err := w.PutOriginal(bg, r); err != nil {
			t.Fatal(err)
		}
		if r.most == 0 || r.most > testFrame {
			t.Errorf("the client asked the reader for %d bytes at once, want at most the frame of %d", r.most, testFrame)
		}
	})

	t.Run("an empty original", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		h, size, err := w.PutOriginal(bg, bytes.NewReader(nil))
		if err == nil || err.Error() != "the original is empty" || h != ([32]byte{}) || size != 0 {
			t.Errorf("PutOriginal = %x, %d, %v", h, size, err)
		}
		if rec.count(dbwire.KindPutOriginal) != 0 {
			t.Error("the client sent a request for an empty original")
		}
		f.noTemps()
		if n := len(f.originalFiles()); n != 0 {
			t.Errorf("files/ has %d files", n)
		}
		putOriginal(t, w, []byte("x")) // the connection goes on
		if got := rec.dialed(dbwire.RoleWriter); got != 1 {
			t.Errorf("%d connections of the writer", got)
		}
	})

	t.Run("over the limit", func(t *testing.T) {
		f := newFixture(t, withObjectLimit)
		w, rec := f.writer(dbclient.Options{})
		for _, n := range []int{objectLimit + 1, 3 * objectLimit} {
			h, size, err := w.PutOriginal(bg, bytes.NewReader(data(n, 1)))
			if err == nil || err.Error() != "the original is larger than 100000 bytes" || h != ([32]byte{}) || size != 0 {
				t.Errorf("PutOriginal of %d bytes = %x, %d, %v", n, h, size, err)
			}
			if errors.Is(err, dbclient.ErrUnavailable) {
				t.Errorf("the error says that the daemon cannot be reached: %v", err)
			}
			f.noTemps()
			if n := len(f.originalFiles()); n != 0 {
				t.Errorf("files/ has %d files", n)
			}
		}
		// The client stops reading at the limit.
		over := &sizeReader{r: bytes.NewReader(data(10*objectLimit, 1))}
		if _, _, err := w.PutOriginal(bg, over); err == nil {
			t.Fatal("PutOriginal succeeded")
		}
		if over.n > objectLimit+testFrame {
			t.Errorf("the client read %d bytes of a reader that is over the limit of %d", over.n, objectLimit)
		}
		f.noTemps()
		// The request that was not complete ended its connection, and the next one goes on a new one.
		putOriginal(t, w, data(objectLimit, 2))
		// Not fewer: a new connection may be refused once, as the daemon has not ended the session of the old one yet.
		if got := rec.dialed(dbwire.RoleWriter); got < 4 {
			t.Errorf("%d connections of the writer, want the first and one after each of the three that were cut", got)
		}
	})

	t.Run("a reader that fails", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		errBoom := errors.New("boom")
		for _, n := range []int{0, 1, 2 * testFrame, 5*testFrame + 3} {
			h, size, err := w.PutOriginal(bg, &failingReader{n: n, err: errBoom})
			if !errors.Is(err, errBoom) || h != ([32]byte{}) || size != 0 {
				t.Errorf("a reader that fails after %d bytes: PutOriginal = %x, %d, %v", n, h, size, err)
			}
			if errors.Is(err, dbclient.ErrUnavailable) {
				t.Errorf("the error says that the daemon cannot be reached: %v", err)
			}
			f.noTemps()
		}
		if n := len(f.originalFiles()); n != 0 {
			t.Errorf("files/ has %d files", n)
		}
		putOriginal(t, w, []byte("x"))
		// The reader that fails at its first read needs no request; each of the three others left one open.
		if got := rec.dialed(dbwire.RoleWriter); got < 4 {
			t.Errorf("%d connections of the writer, want the first and one after each of the three that were cut", got)
		}
	})

	t.Run("a reader client", func(t *testing.T) {
		f := newFixture(t, nil)
		f.writer(dbclient.Options{})
		r, rec := f.open(dbwire.RoleReader, dbclient.Options{})
		rec.reset()
		_, _, err := r.PutOriginal(bg, strings.NewReader("data"))
		if !errors.Is(err, dbclient.ErrReadOnly) {
			t.Errorf("got %v", err)
		}
		wantRequests(t, rec)
		if n := len(f.originalFiles()); n != 0 {
			t.Errorf("files/ has %d files", n)
		}
	})

	t.Run("a closed client", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		h := putOriginal(t, w, []byte("data"))
		must(t, w.Close())
		if _, _, err := w.PutOriginal(bg, strings.NewReader("data")); !errors.Is(err, dbclient.ErrClosed) {
			t.Errorf("PutOriginal: %v", err)
		}
		if _, err := w.Original(bg, h); !errors.Is(err, dbclient.ErrClosed) {
			t.Errorf("Original: %v", err)
		}
	})

	t.Run("a missing original", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		h := sha256.Sum256([]byte("nothing"))
		o, err := w.Original(bg, h)
		want := fmt.Sprintf("%s: not found", originalPath(h))
		if o != nil || err == nil || !errors.Is(err, fs.ErrNotExist) || err.Error() != want {
			t.Errorf("Original = %v, %v, want an error %q that wraps fs.ErrNotExist", o, err, want)
		}
		if re := remote(t, err); re.Code != dbwire.CodeNotFound {
			t.Errorf("the code is %v", re.Code)
		}
		// The connection goes on.
		b := data(3000, 5)
		h = putOriginal(t, w, b)
		if got := readOriginal(t, w, h); !bytes.Equal(got, b) {
			t.Error("the original is not the data")
		}
		if got := rec.dialed(dbwire.RoleReader); got != 1 {
			t.Errorf("%d connections of a reader", got)
		}
	})

	t.Run("a daemon that refuses", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		// A directory in place of the file is the daemon's error, which keeps its text and is not a missing original.
		h := sha256.Sum256([]byte("a directory"))
		must(t, f.fs.Mkdir(fmt.Sprintf("files/%x", h[:1]), 0o700))
		must(t, f.fs.Mkdir(originalPath(h), 0o700))
		o, err := w.Original(bg, h)
		re := remote(t, err)
		if o != nil || re.Code != dbwire.CodeFailed || errors.Is(err, fs.ErrNotExist) || !strings.HasSuffix(re.Msg, ": must be a regular file") {
			t.Errorf("Original = %v, %v", o, err)
		}
		putOriginal(t, w, []byte("x"))
	})

	t.Run("a connection that the daemon has ended", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{DialWait: 5 * time.Second})
		b := data(9000, 4)
		h := putOriginal(t, w, b)
		readOriginal(t, w, h) // a connection of a reader is kept
		rec.cutAll()
		if got := readOriginal(t, w, h); !bytes.Equal(got, b) {
			t.Error("the original is not the data")
		}
		if got := rec.dialed(dbwire.RoleReader); got != 2 {
			t.Errorf("%d connections of a reader, want the first and one after it was ended", got)
		}
		// PutOriginal finds out before it reads from its reader, which cannot be read again.
		rec.cutAll()
		r := &sizeReader{r: bytes.NewReader(b)}
		if got, size, err := w.PutOriginal(bg, r); err != nil || got != h || size != int64(len(b)) {
			t.Errorf("PutOriginal after the connection was ended: %x, %d, %v", got, size, err)
		}
		if got := rec.dialed(dbwire.RoleWriter); got < 2 {
			t.Errorf("%d connections of the writer, want the first and one after it was ended", got)
		}
	})
}

func TestPutOriginalTurn(t *testing.T) {
	f := newFixture(t, nil)
	w, _ := f.writer(dbclient.Options{})
	const wait = 100 * time.Millisecond

	t.Run("after an Update", func(t *testing.T) {
		inUpdate, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
		updated := make(chan error, 1)
		go func() {
			updated <- w.Update(bg, func(tx *dbclient.Tx) error {
				close(inUpdate)
				<-release
				return nil
			})
		}()
		<-inUpdate
		var once sync.Once
		put := make(chan error, 1)
		go func() {
			r := &gatedReader{r: strings.NewReader("data"), before: func() { once.Do(func() { close(started) }) }}
			_, _, err := w.PutOriginal(bg, r)
			put <- err
		}()
		select {
		case <-started:
			t.Fatal("PutOriginal went on while an Update held the writer connection")
		case <-time.After(wait):
		}
		close(release)
		must(t, <-updated)
		must(t, <-put)
	})

	t.Run("before an Update", func(t *testing.T) {
		inPut, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var once sync.Once
		put := make(chan error, 1)
		go func() {
			r := &gatedReader{r: strings.NewReader("more data"), before: func() { once.Do(func() { close(inPut); <-release }) }}
			_, _, err := w.PutOriginal(bg, r)
			put <- err
		}()
		<-inPut
		updated := make(chan error, 1)
		go func() {
			updated <- w.Update(bg, func(tx *dbclient.Tx) error {
				close(started)
				return nil
			})
		}()
		select {
		case <-started:
			t.Fatal("an Update went on while a PutOriginal held the writer connection")
		case <-time.After(wait):
		}
		close(release)
		must(t, <-put)
		must(t, <-updated)
	})

	t.Run("a context that ends while it waits", func(t *testing.T) {
		inUpdate, release := make(chan struct{}), make(chan struct{})
		updated := make(chan error, 1)
		go func() {
			updated <- w.Update(bg, func(tx *dbclient.Tx) error {
				close(inUpdate)
				<-release
				return nil
			})
		}()
		<-inUpdate
		ctx, cancel := context.WithTimeout(bg, 20*time.Millisecond)
		defer cancel()
		if _, _, err := w.PutOriginal(ctx, strings.NewReader("data")); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("got %v", err)
		}
		close(release)
		must(t, <-updated)
	})
}

func TestOriginalNeverWaits(t *testing.T) {
	f := newFixture(t, nil)
	w, rec := f.writer(dbclient.Options{Readers: 1})
	b := data(3*testFrame, 8)
	h := putOriginal(t, w, b)
	ctx, cancel := context.WithTimeout(bg, waitLimit)
	defer cancel()

	// An original that is open does not count: a View runs, and the View has an original of its own.
	o, err := w.Original(ctx, h)
	must(t, err)
	if rec.dialed(dbwire.RoleReader) != 1 {
		t.Errorf("the original took %d connections of a reader", rec.dialed(dbwire.RoleReader))
	}
	must(t, w.View(ctx, func(tx *dbclient.Tx) error {
		get(t, tx, 2, "k") // the View has the only place of the Views, and a connection
		o2, err := w.Original(ctx, h)
		if err != nil {
			return err
		}
		if got, err := io.ReadAll(o2); err != nil || !bytes.Equal(got, b) {
			t.Errorf("the second original: %d bytes, %v", len(got), err)
		}
		return o2.Close()
	}))
	if got, err := io.ReadAll(o); err != nil || !bytes.Equal(got, b) {
		t.Errorf("the first original: %d bytes, %v", len(got), err)
	}
	must(t, o.Close())
	dials := rec.dialed(dbwire.RoleReader)
	if dials != 3 {
		t.Errorf("%d connections of a reader, want 3: the first original, the View, and the second original", dials)
	}
	// Of the three connections, one is kept: Options.Readers is 1.
	if got := rec.liveConns(); got != 2 {
		t.Errorf("%d connections are open, want the writer's and one idle connection of a reader", got)
	}
	for range 3 {
		readOriginal(t, w, h)
		must(t, w.View(ctx, func(tx *dbclient.Tx) error { get(t, tx, 2, "k"); return nil }))
	}
	if got := rec.dialed(dbwire.RoleReader); got != dials {
		t.Errorf("the connection was not reused: %d connections of a reader, want %d", got, dials)
	}
	// A View that holds its connection, and an original read to its end inside it.
	must(t, w.View(ctx, func(tx *dbclient.Tx) error {
		get(t, tx, 2, "k")
		if got := readOriginal(t, w, h); !bytes.Equal(got, b) {
			t.Error("the original is not the data")
		}
		return nil
	}))
}

// TestOriginalHalfRead checks that a connection with the rest of a reply on it does not go back to the pool.
func TestOriginalHalfRead(t *testing.T) {
	f := newFixture(t, nil)
	w, rec := f.writer(dbclient.Options{})
	b := data(5*testFrame, 8)
	h := putOriginal(t, w, b)
	o, err := w.Original(bg, h)
	must(t, err)
	buf := make([]byte, 100)
	if _, err := io.ReadFull(o, buf); err != nil || !bytes.Equal(buf, b[:100]) {
		t.Fatalf("the first bytes: %v", err)
	}
	must(t, o.Close())
	must(t, o.Close())
	if _, err := o.Read(buf); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("Read after Close: %v", err)
	}
	// The next original gets a connection that has nothing left on it.
	if got := readOriginal(t, w, h); !bytes.Equal(got, b) {
		t.Errorf("the next original has %d bytes", len(got))
	}
	if rec.dialed(dbwire.RoleReader) != 2 {
		t.Errorf("%d connections of a reader, want 2: the half-read one is closed", rec.dialed(dbwire.RoleReader))
	}
	// All the bytes read with no Read that gives io.EOF: the connection goes back.
	o, err = w.Original(bg, h)
	must(t, err)
	all := make([]byte, o.Size())
	if _, err := io.ReadFull(o, all); err != nil || !bytes.Equal(all, b) {
		t.Fatalf("the original: %v", err)
	}
	must(t, o.Close())
	readOriginal(t, w, h)
	if rec.dialed(dbwire.RoleReader) != 2 {
		t.Errorf("%d connections of a reader, want 2: the connection of a whole reply goes back", rec.dialed(dbwire.RoleReader))
	}
}

func TestOriginalContext(t *testing.T) {
	t.Run("an original that is being read", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		b := data(objectLimit, 6)
		h := putOriginal(t, w, b)
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		o, err := w.Original(ctx, h)
		must(t, err)
		buf := make([]byte, 50)
		if _, err := io.ReadFull(o, buf); err != nil {
			t.Fatal(err)
		}
		cancel()
		// The bytes of a frame that has come are read first; the next frame is not.
		var rerr error
		for range 1900 {
			if _, rerr = o.Read(buf); rerr != nil {
				break
			}
		}
		if !errors.Is(rerr, context.Canceled) {
			t.Errorf("Read after the context ended: %v", rerr)
		}
		if _, err := o.Read(buf); !errors.Is(err, context.Canceled) {
			t.Errorf("the next Read: %v", err)
		}
		must(t, o.Close())
		// The client goes on.
		if got := readOriginal(t, w, h); !bytes.Equal(got, b) {
			t.Error("the original is not the data")
		}
		if rec.dialed(dbwire.RoleReader) != 2 {
			t.Errorf("%d connections of a reader", rec.dialed(dbwire.RoleReader))
		}
	})

	t.Run("a context that has ended", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		h := putOriginal(t, w, []byte("data"))
		ctx, cancel := context.WithCancel(bg)
		cancel()
		if _, err := w.Original(ctx, h); !errors.Is(err, context.Canceled) {
			t.Errorf("Original: %v", err)
		}
		if _, _, err := w.PutOriginal(ctx, strings.NewReader("data")); !errors.Is(err, context.Canceled) {
			t.Errorf("PutOriginal: %v", err)
		}
		if rec.dialed(dbwire.RoleReader) != 0 {
			t.Error("a context that had ended made a connection")
		}
	})

	t.Run("a put that is being sent", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		reads := 0
		r := &gatedReader{r: bytes.NewReader(data(10*testFrame, 1)), before: func() {
			if reads++; reads == 3 {
				cancel()
			}
		}}
		_, _, err := w.PutOriginal(ctx, r)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("PutOriginal: %v", err)
		}
		if reads != 3 {
			t.Errorf("the reader was read %d times, want 3: the context is looked at before each read", reads)
		}
		f.noTemps()
		if n := len(f.originalFiles()); n != 0 {
			t.Errorf("files/ has %d files", n)
		}
		putOriginal(t, w, []byte("x"))
		if rec.dialed(dbwire.RoleWriter) < 2 {
			t.Errorf("%d connections of the writer", rec.dialed(dbwire.RoleWriter))
		}
	})
}

// TestOriginalDaemonError checks a reply that ends with an error after bytes, a size that the client refuses, and a reply
// that ends early, with a daemon of the test.
func TestOriginalDaemonError(t *testing.T) {
	// Each reply is written in frames of 4 bytes, so that what comes before the end is on the connection.
	serveOriginal := func(t *testing.T, reply func(wr *dbwire.Writer)) *fakeDialer {
		return &fakeDialer{serve: func(rd *dbwire.Reader, wr *dbwire.Writer) {
			fakeHello(t, rd, wr, dbwire.RoleReader)
			for {
				kind, err := rd.Next()
				if err != nil {
					return
				}
				if kind != dbwire.KindOriginal {
					t.Errorf("the request is a %v", kind)
					return
				}
				_ = rd.Discard()
				wr.SetMaxFrame(4)
				reply(wr)
			}
		}}
	}
	open := func(t *testing.T, d *fakeDialer) *dbclient.Client {
		noLeaks(t)
		c, err := dbclient.Open(bg, d, dbwire.RoleReader, dbclient.Options{DialWait: -1})
		must(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	var h [32]byte

	t.Run("an error after some bytes", func(t *testing.T) {
		d := serveOriginal(t, func(wr *dbwire.Writer) {
			wr.Begin(dbwire.KindOK)
			e := &dbwire.Enc{W: wr}
			e.OriginalReply(12)
			_, _ = wr.Write([]byte("abcdefgh"))
			_ = wr.Fail(dbwire.CodeFailed, "the file is shorter than its size")
		})
		c := open(t, d)
		o, err := c.Original(bg, h)
		must(t, err)
		got, err := io.ReadAll(o)
		if string(got) != "abcdefgh" {
			t.Errorf("got %q", got)
		}
		re := remote(t, err)
		if re.Code != dbwire.CodeFailed || re.Msg != "the file is shorter than its size" {
			t.Errorf("got %v", err)
		}
		if _, err := o.Read(make([]byte, 1)); err == nil || err.Error() != re.Msg {
			t.Errorf("a Read after the error: %v", err)
		}
		must(t, o.Close())
		// The reply was whole: the connection goes on.
		o, err = c.Original(bg, h)
		must(t, err)
		_ = o.Close()
		if d.dials != 1 {
			t.Errorf("%d dials, want the connection of the error reply to be used again", d.dials)
		}
	})

	t.Run("an error after all the bytes", func(t *testing.T) {
		d := serveOriginal(t, func(wr *dbwire.Writer) {
			wr.Begin(dbwire.KindOK)
			e := &dbwire.Enc{W: wr}
			e.OriginalReply(8)
			_, _ = wr.Write([]byte("abcdefgh"))
			_ = wr.Fail(dbwire.CodeFailed, "the file is longer than its size")
		})
		c := open(t, d)
		o, err := c.Original(bg, h)
		must(t, err)
		got, err := io.ReadAll(o)
		if string(got) != "abcdefgh" || err == nil || err.Error() != "the file is longer than its size" {
			t.Errorf("got %q, %v", got, err)
		}
		must(t, o.Close())
		// The same when the caller does not read to io.EOF: Close tells.
		o, err = c.Original(bg, h)
		must(t, err)
		if _, err := io.ReadFull(o, make([]byte, 8)); err != nil {
			t.Fatal(err)
		}
		if err := o.Close(); err == nil || err.Error() != "the file is longer than its size" {
			t.Errorf("Close: %v", err)
		}
	})

	t.Run("a size over the limit", func(t *testing.T) {
		d := serveOriginal(t, func(wr *dbwire.Writer) {
			wr.Begin(dbwire.KindOK)
			e := &dbwire.Enc{W: wr}
			e.OriginalReply(1 << 40)
			_ = wr.End()
		})
		c := open(t, d)
		o, err := c.Original(bg, h)
		if o != nil || !errors.Is(err, dbwire.ErrLimit) {
			t.Errorf("Original = %v, %v", o, err)
		}
	})

	t.Run("a reply that ends early", func(t *testing.T) {
		d := serveOriginal(t, func(wr *dbwire.Writer) {
			wr.Begin(dbwire.KindOK)
			e := &dbwire.Enc{W: wr}
			e.OriginalReply(12)
			_, _ = wr.Write([]byte("abcd"))
			_ = wr.End()
		})
		c := open(t, d)
		o, err := c.Original(bg, h)
		must(t, err)
		got, err := io.ReadAll(o)
		if string(got) != "abcd" || !errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("got %q, %v", got, err)
		}
		must(t, o.Close())
	})
}

// fakeHello answers the Hello of a client as a daemon that gives it the role.
func fakeHello(t testing.TB, rd *dbwire.Reader, wr *dbwire.Writer, role dbwire.Role) {
	readHello(t, rd)
	wr.Begin(dbwire.KindOK)
	e := &dbwire.Enc{W: wr}
	e.HelloReply(dbwire.HelloReply{Version: dbwire.Version, Role: role, Frame: 4096, Commit: 1 << 20, Object: objectLimit,
		Staged: 1 << 20, MaxKey: dbwire.MaxKey})
	_ = wr.End()
}
