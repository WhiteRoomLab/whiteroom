package dbserver

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// bucket limits the rate of commits: it holds up to a second of them and is refilled at the rate, and a commit that
// finds it empty waits for its turn, which the commits behind it wait for too.
type bucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second; 0: no limit
	max    float64
	tokens float64
	last   time.Time
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
}

func newBucket(perSecond int) *bucket {
	b := &bucket{now: time.Now, sleep: sleepContext}
	if perSecond > 0 {
		b.rate = float64(perSecond)
		b.max = b.rate
	}
	return b
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (b *bucket) take(ctx context.Context) error {
	if b.rate == 0 {
		return nil
	}
	b.mu.Lock()
	now := b.now()
	if b.last.IsZero() {
		b.tokens = b.max
	} else if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(b.max, b.tokens+elapsed.Seconds()*b.rate)
	}
	b.last = now
	b.tokens--
	var wait time.Duration
	if b.tokens < 0 {
		wait = time.Duration(float64(time.Second) * -b.tokens / b.rate)
	}
	b.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	if err := b.sleep(ctx, wait); err != nil {
		b.mu.Lock()
		b.tokens = min(b.max, b.tokens+1)
		b.mu.Unlock()
		return err
	}
	return nil
}

var (
	verifyObjectText = regexp.MustCompile(`^object [0-9a-f]{64}: the data has the hash [0-9a-f]{64}$`)
	transactionText  = regexp.MustCompile(`^the transaction is too large: [0-9]+ bytes \(max [0-9]+\)$`)
)

const verifyDiffers = "the state in memory differs from the log"

// safeText returns the text of err for a reply or a log line. It is the text of an error of the OS, of a damaged file
// or of a file of a newer format, which name paths, sizes and offsets; of a sync that failed, the same for its cause; or
// one of the sentences of the engine's Verify, whose detail, which may hold values of rows, is cut. Any other error
// becomes a fixed sentence.
func safeText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if verifyObjectText.MatchString(text) || transactionText.MatchString(text) {
		return text
	}
	if i := strings.Index(text, verifyDiffers); i >= 0 {
		return text[:i+len(verifyDiffers)] + " (the detail is not sent: it may hold values of rows)"
	}
	var (
		pe *fs.PathError
		le *os.LinkError
		ce *logdb.CorruptError
		ve *logdb.VersionError
	)
	switch {
	case errors.As(err, &ce):
		return ce.Error()
	case errors.As(err, &ve):
		return ve.Error()
	case errors.As(err, &pe):
		return pe.Error()
	case errors.As(err, &le):
		return le.Error()
	}
	return "the engine failed"
}
