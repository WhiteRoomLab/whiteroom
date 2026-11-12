package admin

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
)

// logDeadline is the deadline of the routes that read a log. Each page is read from the start of the log (7.4), which takes a
// time that grows with the log, and 30 seconds, the deadline of the server, would soon not do.
const logDeadline = 120 * time.Second

// logRoutes registers the routes that read the login history and the activity log, newest first.
func (s *Server) logRoutes() {
	s.apiWithin("GET /api/logs/login", logDeadline, s.readLog(audit.LoginLog))
	s.apiWithin("GET /api/logs/activity", logDeadline, s.readLog(audit.ActivityLog))
}

// logForms are the names that the query of a log takes, with the check of the form of their values. That a value is the ID or
// the name that its filter takes is for audit.Filter.Check.
var logForms = queryForms{
	"from": isTime, "to": isTime, "account": isText, "action": isText, "targetKind": isText, "targetId": isText,
	"before": isBefore, "limit": isLimit,
}

func isTime(v string) bool {
	_, err := time.Parse(time.RFC3339, v)
	return err == nil
}

func isText(v string) bool { return v != "" }

// isBefore reports whether v is the number of a record, which the query gives as before: digits, from 1.
func isBefore(v string) bool {
	n, err := strconv.ParseUint(v, 10, 63)
	return err == nil && n >= 1
}

// errLogsBusy is the error of inLogs when another request is reading a log. It wraps context.DeadlineExceeded, as the error
// of inPlace does when no place is free in time, so that it is answered with 503.
var errLogsBusy = fmt.Errorf("another log is being read: %w", context.DeadlineExceeded)

// inLogs runs f in the place of reading the logs, which one request has at a time, apart from the place of heavy work. It does
// not wait for it: its error is errLogsBusy if another request has it. It calls at("checked") when it has the place, and
// at("sending") when the place has been given back, whether f has failed or not.
func (s *Server) inLogs(ctx context.Context, f func(ctx context.Context) error) error {
	select {
	case s.logReads <- struct{}{}:
	default:
		return errLogsBusy
	}
	err := s.readingLogs(ctx, f)
	s.at("sending")
	return err
}

func (s *Server) readingLogs(ctx context.Context, f func(ctx context.Context) error) error {
	defer func() { <-s.logReads }()
	s.at("checked")
	return f(ctx)
}

// readLog is GET /api/logs/<log>: the last limit records of the log that the query selects, newest first, and in next the
// number of the oldest of them if the log has older ones that the query selects, to give as before for the page after. The
// query has these names: from and to (RFC 3339; the records from from, up to but not including to), account (the actor, the
// account or the target of a record), action (a name, or a prefix that ends with a dot), targetKind, targetId, before and
// limit (1 to maxPage; defaultPage). The log is read from its start for every page, in the place of the logs and not in that of
// heavy work, within the count of the account; the answer is sent after the place has been given back.
func (s *Server) readLog(log audit.Log) routeFunc {
	return func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		if !s.allow(w, r, p, account.ViewLogs, account.Target{}, audit.Target{}) || !noOperationID(w, r) {
			return
		}
		q, ok := readQuery(w, r, logForms)
		if !ok {
			return
		}
		f := audit.Filter{
			Log: log, Account: q.Get("account"), Action: q.Get("action"),
			Target: audit.Target{Kind: q.Get("targetKind"), ID: q.Get("targetId")},
		}
		f.From, _ = time.Parse(time.RFC3339, q.Get("from"))
		f.To, _ = time.Parse(time.RFC3339, q.Get("to"))
		if f.Check() != nil {
			writeBadQuery(w)
			return
		}
		before, _ := strconv.ParseUint(q.Get("before"), 10, 63)
		limit := defaultPage
		if v := q.Get("limit"); v != "" {
			limit, _ = strconv.Atoi(v)
		}

		release := s.countAccount(w, p)
		if release == nil {
			return
		}
		defer release()
		out := newSpool(s.store)
		defer func() { _ = out.Close() }()
		err := s.inLogs(r.Context(), func(ctx context.Context) error {
			return s.readRecords(ctx, f, before, limit, out)
		})
		if err != nil {
			s.answerError(w, r, p, err, attempt{})
			return
		}
		s.sendSpool(w, r, http.StatusOK, nil, out)
	}
}

// readRecords reads the log from its start up to the record with the number before, or to its end if before is 0, and writes
// to out the last limit records that f selects. Records that have ended with the context are dropped: the error is the
// context's.
func (s *Server) readRecords(ctx context.Context, f audit.Filter, before uint64, limit int, out io.Writer) error {
	last := newestRecords{limit: limit}
	err := s.store.ReadAudit(ctx, f, func(r audit.Record) bool {
		if before > 0 && r.N >= before || ctx.Err() != nil {
			return false
		}
		last.add(r)
		return true
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return err
	}
	return writeRecords(out, last.newestFirst(), last.dropped)
}

// newestRecords keeps the last limit records that are added to it, in a ring, and tells whether it dropped any.
type newestRecords struct {
	limit   int
	buf     []audit.Record
	next    int // where the oldest record is once buf is full, and the next one goes
	dropped bool
}

func (n *newestRecords) add(r audit.Record) {
	if len(n.buf) < n.limit {
		n.buf = append(n.buf, r)
		return
	}
	n.buf[n.next] = r
	n.next = (n.next + 1) % n.limit
	n.dropped = true
}

// newestFirst returns the records that were kept, the last one added first.
func (n *newestRecords) newestFirst() []audit.Record {
	out := make([]audit.Record, 0, len(n.buf))
	for i := len(n.buf) - 1; i >= 0; i-- {
		out = append(out, n.buf[(n.next+i)%len(n.buf)])
	}
	return out
}

// writeRecords writes {"records":[…]} to out, and "next" if more, which is the number of the last of records, the oldest. A
// record is written as the store has it: its JSON, with the keys of audit.Record in their order and those of the detail
// sorted, and it is not checked again, so that a record that another build wrote with other rules is read as well.
func writeRecords(out io.Writer, records []audit.Record, more bool) error {
	j := newJSONStream(out)
	j.object()
	j.key("records")
	j.array()
	for _, r := range records {
		data, err := json.Marshal(r, json.Deterministic(true))
		if err != nil {
			return err
		}
		j.raw(data)
	}
	j.end()
	if more && len(records) > 0 {
		j.key("next")
		j.value(records[len(records)-1].N)
	}
	j.end()
	return j.done()
}
