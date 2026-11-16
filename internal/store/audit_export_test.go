package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// headLine is a line that the export of the heads in the daemon writes: the log, the number and the hash, and nothing
// else.
var headLine = regexp.MustCompile(`^audit head (login|activity) [1-9][0-9]* sha256:[0-9a-f]{64}$`)

// parseHeads reads the heads from the lines of out, as a person who keeps the log of a process does: each line has what
// a journal puts before it.
func parseHeads(t *testing.T, out string) []audit.Head {
	t.Helper()
	var heads []audit.Head
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if !headLine.MatchString(line) {
			t.Errorf("the line %q is not a head and nothing else", line)
		}
		h, err := audit.ParseHead("Nov 11 12:00:00 host whiteroom[4321]: " + line)
		if err != nil {
			t.Fatalf("ParseHead(%q) = %v", line, err)
		}
		heads = append(heads, h)
	}
	return heads
}

// exported runs the export of the heads of the daemon of s (dbserver's ExportHeads, which whiteroom-db serve runs on its
// standard error) as a process runs it: it begins, more runs, a tick comes, and the context ends. It returns the heads
// that it has written, which it reads from its lines.
func exported(t *testing.T, s *Store, more func()) []audit.Head {
	t.Helper()
	srv := daemonOf(t, s).Server()
	var out bytes.Buffer // only ExportHeads writes to it, and the test reads it after ExportHeads has returned
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() {
		done <- srv.ExportHeads(ctx, &out, ticks, func(err error) { t.Errorf("ExportHeads() has reported %v", err) })
	}()
	if more != nil {
		more()
	}
	select {
	case ticks <- time.Now():
	case err := <-done:
		t.Fatalf("ExportHeads() = %v before its context has ended", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("ExportHeads() = %v", err)
	}
	return parseHeads(t, out.String())
}

// TestExportedHeadsFindARewrite checks what the heads that the daemon writes outside the store find: a rewrite of the
// records that also makes the chain and the heads of the table agree, which VerifyAudit without them does not find, and
// not a rewrite of the records that were added after the last head was written.
func TestExportedHeadsFindARewrite(t *testing.T) {
	forge := func(r *audit.Record) { r.Detail = map[string]string{"event": "forged"} }
	// The events of auditFixture are 1 activity 1 (store.create), 2 activity 2, 3 login 1, 4 activity 3, 5 activity 4,
	// 6 login 2, 7 activity 5, 8 activity 6 and 9 login 3; then, if more, 10 activity 7 and 11 login 4, which are added
	// after the export has returned.
	merge := func(a, b map[uint64][]byte) map[uint64][]byte {
		out := map[uint64][]byte{}
		for _, m := range []map[uint64][]byte{a, b} {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}
	for _, tt := range []struct {
		name     string
		more     bool
		rewrite  func(t *testing.T, events []logdb.Event) logdb.Rewrite
		anchored string // the error of VerifyAudit with the heads that the export wrote; "" for none
	}{
		{"nothing rewritten", false, func(t *testing.T, events []logdb.Event) logdb.Rewrite {
			return rewriteRecords(nil, nil, nil)
		}, ""},
		{"the last record of the activity log dropped, with its head set back", false,
			func(t *testing.T, events []logdb.Event) logdb.Rewrite {
				h := audit.Head{Log: audit.ActivityLog, N: 5, Hash: sha256.Sum256(events[6].Data)}
				return rewriteRecords(nil, []uint64{8}, map[audit.Log]*audit.Head{audit.ActivityLog: &h})
			},
			`audit log "activity": the head kept outside the store is record 6, but the log ends with record 5 (event 7)`},
		{"the login history dropped, with its head", false, func(t *testing.T, events []logdb.Event) logdb.Rewrite {
			return rewriteRecords(nil, []uint64{3, 6, 9}, map[audit.Log]*audit.Head{audit.LoginLog: nil})
		}, `audit log "login": the head kept outside the store is record 3, but the log has no record`},
		{"the activity log made again from its third record", false,
			func(t *testing.T, events []logdb.Event) logdb.Rewrite {
				replace, head := rechain(t, events, audit.ActivityLog, 3, forge)
				return rewriteRecords(replace, nil, map[audit.Log]*audit.Head{audit.ActivityLog: &head})
			},
			`audit log "activity": record 6 (event 8): its hash is not that of the head kept outside the store`},
		{"the activity log made again from its first record", false,
			func(t *testing.T, events []logdb.Event) logdb.Rewrite {
				replace, head := rechain(t, events, audit.ActivityLog, 1, func(r *audit.Record) { r.At = r.At.Add(time.Hour) })
				return rewriteRecords(replace, nil, map[audit.Log]*audit.Head{audit.ActivityLog: &head})
			},
			`audit log "activity": record 6 (event 8): its hash is not that of the head kept outside the store`},
		{"the login history made again from its second record", false,
			func(t *testing.T, events []logdb.Event) logdb.Rewrite {
				replace, head := rechain(t, events, audit.LoginLog, 2, forge)
				return rewriteRecords(replace, nil, map[audit.Log]*audit.Head{audit.LoginLog: &head})
			},
			`audit log "login": record 3 (event 9): its hash is not that of the head kept outside the store`},
		{"a record before the last head made again, with those after it", true,
			func(t *testing.T, events []logdb.Event) logdb.Rewrite {
				replace, head := rechain(t, events, audit.ActivityLog, 3, forge)
				return rewriteRecords(replace, nil, map[audit.Log]*audit.Head{audit.ActivityLog: &head})
			},
			`audit log "activity": record 6 (event 8): its hash is not that of the head kept outside the store`},
		// The limit: the records that were added after the last head was written are in no head outside the store.
		{"the records added after the last head dropped, with their heads set back", true,
			func(t *testing.T, events []logdb.Event) logdb.Rewrite {
				activity := audit.Head{Log: audit.ActivityLog, N: 6, Hash: sha256.Sum256(events[7].Data)}
				login := audit.Head{Log: audit.LoginLog, N: 3, Hash: sha256.Sum256(events[8].Data)}
				return rewriteRecords(nil, []uint64{10, 11}, map[audit.Log]*audit.Head{audit.ActivityLog: &activity, audit.LoginLog: &login})
			}, ""},
		{"the records added after the last head made again", true,
			func(t *testing.T, events []logdb.Event) logdb.Rewrite {
				activity, activityHead := rechain(t, events, audit.ActivityLog, 7, forge)
				login, loginHead := rechain(t, events, audit.LoginLog, 4, forge)
				return rewriteRecords(merge(activity, login), nil,
					map[audit.Log]*audit.Head{audit.ActivityLog: &activityHead, audit.LoginLog: &loginHead})
			}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, dir, fixture := auditFixture(t)
			anchors := exported(t, s, nil)
			for _, h := range fixture {
				if !slices.Contains(anchors, h) {
					t.Errorf("the heads that the export wrote lack %v, which the store had when it stopped", h)
				}
			}
			if err := s.VerifyAudit(t.Context(), anchors); err != nil {
				t.Fatalf("VerifyAudit() with the heads that the export wrote, before the rewrite = %v", err)
			}
			if tt.more {
				update(t, s, func(tx *Tx) error {
					return errors.Join(tx.Record(eventRecord("after")), tx.Record(loginRecord(audit.Logout, "after")))
				})
			}
			events := rawEvents(t, s)
			s = closeAndRewrite(t, s, dir, tt.rewrite(t, events))
			tt.anchored = renumber(events, tt.anchored)
			if err := s.VerifyAudit(t.Context(), nil); err != nil {
				t.Errorf("VerifyAudit() without the heads = %v, want nil: the rewrite is one that the store alone cannot find", err)
			}
			err := s.VerifyAudit(t.Context(), anchors)
			switch {
			case tt.anchored == "" && err != nil:
				t.Errorf("VerifyAudit() with the heads that the export wrote = %v, want nil", err)
			case tt.anchored != "" && (err == nil || err.Error() != tt.anchored || !errors.Is(err, audit.ErrBroken)):
				t.Errorf("VerifyAudit() with the heads that the export wrote = %v, want %q that wraps audit.ErrBroken", err,
					tt.anchored)
			}
		})
	}
}

// TestExportWhileWriting checks that the export of the daemon, which reads the heads in a View, goes on with the writer:
// the heads that it writes never go back, the last of each log is the head of the store, and every head holds for the
// store.
func TestExportWhileWriting(t *testing.T) {
	s, _ := newLogStore(t, logdb.Options{})
	srv := daemonOf(t, s).Server()
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() {
		done <- srv.ExportHeads(ctx, &out, ticks, func(err error) { t.Errorf("ExportHeads() has reported %v", err) })
	}()
	for i := range 120 {
		update(t, s, func(tx *Tx) error {
			return errors.Join(tx.Record(eventRecord("x")), tx.Record(loginRecord(audit.Login, "a")))
		})
		if i%10 == 0 {
			select {
			case ticks <- time.Now():
			case err := <-done:
				t.Fatalf("ExportHeads() = %v before its context has ended", err)
			}
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("ExportHeads() = %v", err)
	}
	heads := parseHeads(t, out.String())
	last := map[audit.Log]audit.Head{}
	for _, h := range heads {
		if prev, ok := last[h.Log]; ok && h.N < prev.N {
			t.Errorf("the head of the %s log went from record %d back to record %d", h.Log, prev.N, h.N)
		}
		last[h.Log] = h
	}
	final, err := s.AuditHeads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range final {
		if last[h.Log] != h {
			t.Errorf("the last head of the %s log that the export wrote is %+v, want that of the store, %+v", h.Log, last[h.Log], h)
		}
	}
	if len(heads) < 4 {
		t.Errorf("the export wrote %d heads, want one of each log at least at the start and at the end", len(heads))
	}
	if err := s.VerifyAudit(t.Context(), heads); err != nil {
		t.Errorf("VerifyAudit() with every head that the export wrote = %v", err)
	}
}

// TestExportWithoutAnAuditLog checks that the export of the daemon of a store of the table schema 5, which has no audit
// log and no row of the table of the heads, writes nothing and fails with nothing: the daemon does not read the audit log.
func TestExportWithoutAnAuditLog(t *testing.T) {
	s, dir := newLogStore(t, logdb.Options{})
	toSchema5(t, s, dir)
	r, err := openLogReader(dir, options{})
	if err != nil {
		t.Fatalf("openLogReader() = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	var reported []error
	err = daemonOf(t, r).Server().ExportHeads(ctx, &out, nil, func(err error) { reported = append(reported, err) })
	if err != nil || len(reported) != 0 || out.Len() != 0 {
		t.Errorf("ExportHeads() = %v, reported %v, wrote %q; want nil, nothing reported, and nothing written", err, reported,
			out.String())
	}
}
