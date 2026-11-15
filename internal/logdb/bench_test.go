package logdb

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// BenchmarkScanAfter reads the last 100 of 1,000,000 keys of a table: "seek" with ScanAfter after the key before them,
// "skip" with a Scan whose callback passes the keys up to that one. CI does not run it.
func BenchmarkScanAfter(b *testing.B) {
	const total, batch, last = 1_000_000, 100_000, 100
	ctx := context.Background()
	db, err := Create(simfs.New(simDir), Options{CheckpointBytes: -1})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	key := func(i int) []byte { return fmt.Appendf(nil, "entry/%032d", i) }
	for from := 0; from < total; from += batch {
		err := db.Update(ctx, func(tx *Tx) error {
			for i := from; i < from+batch; i++ {
				if err := tx.Put(1, key(i), []byte("value")); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	prefix, after := []byte("entry/"), key(total-last-1)
	b.Run("seek", func(b *testing.B) {
		for range b.N {
			n := 0
			err := db.View(ctx, func(tx *Tx) error {
				tx.ScanAfter(1, prefix, after, func(_, _ []byte) bool {
					n++
					return true
				})
				return nil
			})
			if err != nil || n != last {
				b.Fatalf("%d keys, %v, want %d", n, err, last)
			}
		}
	})
	b.Run("skip", func(b *testing.B) {
		for range b.N {
			n := 0
			err := db.View(ctx, func(tx *Tx) error {
				tx.Scan(1, prefix, func(k, _ []byte) bool {
					if bytes.Compare(k, after) > 0 {
						n++
					}
					return true
				})
				return nil
			})
			if err != nil || n != last {
				b.Fatalf("%d keys, %v, want %d", n, err, last)
			}
		}
	})
}
