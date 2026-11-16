package dbclient

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
)

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func newSource(ctx context.Context, r io.Reader, size, limit int) *originalSource {
	return &originalSource{ctx: ctx, r: r, buf: make([]byte, size), limit: int64(limit), sum: sha256.New()}
}

// TestOriginalSource checks how PutOriginal reads its reader: ctx before each read, the limit, and the end.
func TestOriginalSource(t *testing.T) {
	t.Run("the context before each read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reads := 0
		s := newSource(ctx, readerFunc(func(p []byte) (int, error) {
			reads++
			cancel()
			return copy(p, "abc"), nil
		}), 4, 100)
		if n, err := s.next(); n != 3 || err != nil {
			t.Fatalf("next = %d, %v", n, err)
		}
		if n, err := s.next(); n != 0 || !errors.Is(err, context.Canceled) {
			t.Errorf("next after the context ended = %d, %v", n, err)
		}
		if _, err := s.next(); !errors.Is(err, context.Canceled) || reads != 1 {
			t.Errorf("the reader was read %d times, want 1", reads)
		}
	})

	t.Run("the limit", func(t *testing.T) {
		s := newSource(context.Background(), readerFunc(func(p []byte) (int, error) { return copy(p, "abcd"), nil }), 4, 10)
		for _, want := range []int{4, 4} {
			if n, err := s.next(); n != want || err != nil {
				t.Fatalf("next = %d, %v", n, err)
			}
		}
		if n, err := s.next(); n != 0 || err == nil || err.Error() != "the original is larger than 10 bytes" {
			t.Errorf("next over the limit = %d, %v", n, err)
		}
		if s.size != 8 {
			t.Errorf("the size is %d: the bytes over the limit are not counted", s.size)
		}
	})

	t.Run("the end with the last bytes", func(t *testing.T) {
		calls := 0
		s := newSource(context.Background(), readerFunc(func(p []byte) (int, error) {
			calls++
			return copy(p, "ab"), io.EOF
		}), 4, 10)
		if n, err := s.next(); n != 2 || err != nil {
			t.Fatalf("next = %d, %v", n, err)
		}
		if n, err := s.next(); n != 0 || !errors.Is(err, io.EOF) || calls != 1 {
			t.Errorf("next at the end = %d, %v after %d reads", n, err, calls)
		}
		if got := s.sum.Sum(nil); string(got) != string(sum256("ab")) {
			t.Error("the hash is not that of the bytes")
		}
	})

	t.Run("no progress", func(t *testing.T) {
		calls := 0
		s := newSource(context.Background(), readerFunc(func([]byte) (int, error) { calls++; return 0, nil }), 4, 10)
		if _, err := s.next(); !errors.Is(err, io.ErrNoProgress) || calls != maxEmptyReads {
			t.Errorf("next = %v after %d reads", err, calls)
		}
	})
}

func sum256(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}
