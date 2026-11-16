package dbclient

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// maxEmptyReads is how many reads in a row that give nothing PutOriginal puts up with.
const maxEmptyReads = 100

var errEmptyOriginal = errors.New("the original is empty")

// originalSource reads the bytes of an original in blocks, and hashes and counts them.
type originalSource struct {
	ctx   context.Context
	r     io.Reader
	buf   []byte
	limit int64
	size  int64
	sum   hash.Hash
	err   error // what r ended with
}

// next reads the next block into buf. It returns its length, or io.EOF at the end of r, or the error that ends the
// original: that of ctx, that of r, or the size over the limit.
func (s *originalSource) next() (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	for range maxEmptyReads {
		if err := s.ctx.Err(); err != nil {
			s.err = err
			return 0, err
		}
		n, err := s.r.Read(s.buf)
		s.err = err
		if n > 0 {
			if s.size+int64(n) > s.limit {
				s.err = fmt.Errorf("the original is larger than %d bytes", s.limit)
				return 0, s.err
			}
			s.size += int64(n)
			s.sum.Write(s.buf[:n])
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
	s.err = io.ErrNoProgress
	return 0, s.err
}

// send writes the request: the block of n bytes that next has read, the blocks after it, and the trailer. A failure of
// the reader leaves the request open, which ends the connection.
func (s *originalSource) send(e *dbwire.Enc, n int) {
	chunks := e.Chunks()
	for {
		if _, err := chunks.Write(s.buf[:n]); err != nil {
			return // e.Err is set
		}
		var err error
		if n, err = s.next(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			e.Err = &localError{err}
			return
		}
	}
	_ = chunks.Close()
	e.PutOriginalTrailer([32]byte(s.sum.Sum(nil)), s.size)
}

// PutOriginal saves what r reads, to its end, as an original of the daemon, which it streams in blocks of at most the
// frame size, and returns the SHA-256 and the size of what it read. It fails, with nothing left in the daemon, if r is
// empty, has more bytes than the daemon takes in an original, or fails; ctx is looked at before each read of r. A
// connection that fails in the middle ends the request, which the daemon drops. PutOriginal runs in the turn of the
// Updates: the connection of the writer carries one request at a time. A client of a reader fails with ErrReadOnly.
func (c *Client) PutOriginal(ctx context.Context, r io.Reader) (sum [32]byte, size int64, err error) {
	switch {
	case c.isClosed():
		return sum, 0, ErrClosed
	case c.role != dbwire.RoleWriter:
		return sum, 0, ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return sum, 0, err
	}
	if err := c.acquireTurn(ctx); err != nil {
		return sum, 0, err
	}
	defer c.releaseTurn()
	defer c.restoreWriter(ctx)
	cn, err := c.liveWriter(ctx)
	if err != nil {
		return sum, 0, err
	}
	src := &originalSource{ctx: ctx, r: r, buf: make([]byte, min(cn.hello.Frame, 1<<20)), limit: cn.hello.Object,
		sum: sha256.New()}
	// The first block is read before anything is sent, so that an empty original or a failing reader needs no request.
	first, err := src.next()
	switch {
	case errors.Is(err, io.EOF):
		return sum, 0, errEmptyOriginal
	case err != nil:
		return sum, 0, err
	}
	err = cn.call(ctx, dbwire.KindPutOriginal, func(e *dbwire.Enc) { src.send(e, first) }, dbwire.ReadEmpty)
	if err != nil {
		// The daemon ends the session after some refusals of an original, which it has not read to its end: restoreWriter
		// takes a new connection.
		cn.close()
		return [32]byte{}, 0, err
	}
	return [32]byte(src.sum.Sum(nil)), src.size, nil
}

// Original is an original of the daemon that is being read: the bytes come from the connection as Read asks for them.
// It is not for concurrent use. The daemon sends the bytes of its file as they are: the caller checks their SHA-256
// after the last one.
type Original struct {
	c    *Client
	cn   *conn
	ctx  context.Context
	size int64
	d    *dbwire.Dec
	body io.Reader
	read int64
	stop func() bool // of the watch of ctx, which sets a deadline on the connection

	done   bool // the reply has been read to its end, and the connection may be used again
	err    error
	closed bool
}

// notFoundError is the error of an original that the daemon does not have: its text is the daemon's.
type notFoundError struct{ err *dbwire.RemoteError }

func (e *notFoundError) Error() string   { return e.err.Msg }
func (e *notFoundError) Unwrap() []error { return []error{fs.ErrNotExist, e.err} }

// Original opens the original whose SHA-256 is hash. If the daemon has none, the error wraps fs.ErrNotExist and has the
// text of the daemon. It never waits for a View: it takes an idle connection of a reader, or dials one that does not
// count in Options.Readers, so that code that holds an original open and starts a View does not wait for itself. ctx is
// the context of the reads of the original.
func (c *Client) Original(ctx context.Context, hash [32]byte) (*Original, error) {
	if c.isClosed() {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cn, dialed, err := c.takeReader(ctx)
	if err != nil {
		return nil, err
	}
	o, err := c.openOriginal(ctx, cn, hash)
	if _, ok := errors.AsType[*ioError](err); ok && !dialed {
		// A connection that was idle may be one that the daemon has ended; the request did not take effect.
		if cn, err = c.connect(ctx, dbwire.RoleReader); err != nil {
			return nil, err
		}
		o, err = c.openOriginal(ctx, cn, hash)
	}
	if err != nil {
		c.putReader(cn)
		if re, ok := errors.AsType[*dbwire.RemoteError](err); ok && re.Code == dbwire.CodeNotFound {
			return nil, &notFoundError{re}
		}
		return nil, err
	}
	return o, nil
}

func (c *Client) openOriginal(ctx context.Context, cn *conn, hash [32]byte) (*Original, error) {
	o := &Original{c: c, cn: cn, ctx: ctx}
	err := cn.call(ctx, dbwire.KindOriginal, func(e *dbwire.Enc) { e.OriginalRequest(hash) }, func(d *dbwire.Dec) (err error) {
		o.d = d
		o.size, err = dbwire.ReadOriginalReply(d, cn.hello.Object)
		return err
	})
	if err != nil {
		return nil, err
	}
	o.body = o.d.Stream(o.size)
	o.stop = context.AfterFunc(ctx, func() { _ = cn.nc.SetDeadline(time.Unix(1, 0)) })
	return o, nil
}

// Size returns the number of bytes of the original.
func (o *Original) Size() int64 { return o.size }

// Read reads the bytes of the original, and returns io.EOF after the last. If the daemon ends its reply with an error,
// that is the error of the Read that comes to it.
func (o *Original) Read(p []byte) (int, error) {
	switch {
	case o.closed:
		return 0, fs.ErrClosed
	case o.err != nil:
		return 0, o.err
	case o.done:
		return 0, io.EOF
	}
	n, err := o.body.Read(p)
	o.read += int64(n)
	if err == nil {
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		// All the bytes have come: the reply ends there.
		if err = o.finish(); err == nil {
			return n, io.EOF
		}
	}
	o.fail(err)
	return n, o.err
}

// finish reads the end of the reply, after its last byte.
func (o *Original) finish() error {
	if err := o.d.End(); err != nil {
		return err
	}
	o.done = true
	return nil
}

// fail records err, which ended the reply. The connection can be used again after an error reply, which ends the reply
// as a whole message.
func (o *Original) fail(err error) {
	if _, ok := errors.AsType[*dbwire.RemoteError](err); ok {
		o.done = true
		o.err = err
		return
	}
	o.cn.close()
	o.err = o.cn.failure(o.ctx, err)
}

// Close ends the original. The connection goes back to the client if the reply has been read to its end, also when the
// last Read gave the last byte and not io.EOF; else it is closed.
func (o *Original) Close() error {
	if o.closed {
		return nil
	}
	var err error
	if !o.done && o.err == nil && o.read == o.size {
		if err = o.finish(); err != nil {
			o.fail(err)
			err = o.err
		}
	}
	o.closed = true
	if o.stop() && o.done {
		o.c.putReader(o.cn)
	} else {
		o.cn.close()
	}
	return err
}
