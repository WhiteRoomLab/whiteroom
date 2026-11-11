package account

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

func TestFailureLimitCounts(t *testing.T) {
	var l failureLimit
	minute := time.Date(2026, 11, 8, 12, 3, 0, 0, time.UTC)
	wrong, unknown := failure{audit.LoginFailure, failWrongPassword}, failure{audit.TokenFailure, tokenUnknown}

	for i := range failuresPerMinute {
		if !l.admit(minute.Add(time.Duration(i)*time.Second+999*time.Millisecond), wrong) {
			t.Fatalf("failure %d of the minute is refused, want the first %d recorded", i+1, failuresPerMinute)
		}
	}
	if l.pending() {
		t.Error("counts are pending after the failures that are recorded")
	}
	// The failures after them are counted, of any kind, until the minute is over.
	if l.admit(minute.Add(59*time.Second), wrong) || l.admit(minute.Add(59*time.Second), unknown) || l.admit(minute.Add(59*time.Second), wrong) {
		t.Error("a failure after the first 60 of the minute is recorded")
	}
	if !l.pending() {
		t.Fatal("no counts are pending after failures that were not recorded")
	}
	// The next minute begins again, and what was counted stays.
	if !l.admit(minute.Add(time.Minute), wrong) {
		t.Error("the first failure of the next minute is refused")
	}
	for i := 1; i < failuresPerMinute; i++ {
		if !l.admit(minute.Add(time.Minute+time.Duration(i)*time.Millisecond), unknown) {
			t.Fatalf("failure %d of the next minute is refused", i+1)
		}
	}
	if l.admit(minute.Add(2*time.Minute-1), unknown) {
		t.Error("the 61st failure of the next minute is recorded")
	}

	got := l.take()
	if got == nil || got.counts[wrong] != 2 || got.counts[unknown] != 2 || len(got.counts) != 2 ||
		!got.from.Equal(minute) || !got.to.Equal(minute.Add(2*time.Minute)) {
		t.Fatalf("take() = %+v, want 2 and 2, from %v to %v", got, minute, minute.Add(2*time.Minute))
	}
	if l.pending() || l.take() != nil {
		t.Error("counts are pending after take")
	}

	// restore counts them again, with what came meanwhile.
	l.restore(got)
	l.admit(minute.Add(10*time.Minute), wrong)
	for range failuresPerMinute {
		l.admit(minute.Add(10*time.Minute), wrong)
	}
	back := l.take()
	if back == nil || back.counts[wrong] != 3 || back.counts[unknown] != 2 || !back.from.Equal(minute) || !back.to.Equal(minute.Add(11*time.Minute)) {
		t.Errorf("take() after restore = %+v, want 3 and 2, from %v to %v", back, minute, minute.Add(11*time.Minute))
	}
}

func TestFailureLimitIsPerMinuteOfTheClock(t *testing.T) {
	// The minute is that of the instant, whatever zone the clock gives it in.
	var l failureLimit
	jst := time.FixedZone("JST", 9*3600)
	base := time.Date(2026, 11, 8, 21, 0, 30, 0, jst)
	wrong := failure{audit.LoginFailure, failWrongPassword}
	for range failuresPerMinute {
		if !l.admit(base, wrong) {
			t.Fatal("a failure of the first 60 is refused")
		}
	}
	if l.admit(base.UTC(), wrong) {
		t.Error("the 61st failure, in another zone for the same instant, is recorded")
	}
	if !l.admit(base.Add(30*time.Second), wrong) {
		t.Error("the first failure of the next minute is refused")
	}
}

// failures makes n failures of a login with a wrong password, and checks that the answer is the same every time.
func (e *mgrEnv) failures(t *testing.T, n int) {
	t.Helper()
	for range n {
		if _, _, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("Login() = %v, want ErrBadCredentials", err)
		}
	}
}

func TestFailureLimit(t *testing.T) {
	setup := func(t *testing.T) *mgrEnv {
		t.Helper()
		e := newMgr(t)
		e.first(t)
		e.add(t, "yamada", RoleEditor)
		return e
	}
	minute := mgrTime.Truncate(time.Minute)

	t.Run("60 a minute are recorded, and the others counted", func(t *testing.T) {
		e := setup(t)
		before := e.mark(t)
		e.failures(t, 100)
		names := e.added(t, before)
		if len(names) != failuresPerMinute {
			t.Fatalf("100 failures in a minute have made %d records, want %d", len(names), failuresPerMinute)
		}
		for _, name := range names {
			if name != "login.failure/wrong-password" {
				t.Fatalf("a record is %q, want login.failure/wrong-password", name)
			}
		}
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatalf("FlushSuppressed() = %v", err)
		}
		records := e.records(t, before)
		want := audit.Record{Log: audit.LoginLog, Action: audit.LoginSuppressed, Detail: map[string]string{
			"from": minute.Format(time.RFC3339), "to": minute.Add(time.Minute).Format(time.RFC3339), "loginFailureWrongPassword": "40",
		}}
		if len(records) != failuresPerMinute+1 || !reflect.DeepEqual(histShape(records[failuresPerMinute]), want) {
			t.Fatalf("the last of %d records is %+v, want %+v", len(records), histShape(records[len(records)-1]), want)
		}
		// What was counted is recorded once.
		before = e.mark(t)
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatal(err)
		}
		e.unchanged(t, before)
	})

	t.Run("the next minute records again", func(t *testing.T) {
		e := setup(t)
		e.failures(t, 70)
		e.clock.add(time.Minute)
		before := e.mark(t)
		e.failures(t, 5)
		// The first record of the new minute has the counts of the one before.
		records := e.records(t, before)
		if len(records) != 6 || records[0].Action != audit.LoginSuppressed || records[0].Detail["loginFailureWrongPassword"] != "10" ||
			records[0].Detail["from"] != minute.Format(time.RFC3339) || records[0].Detail["to"] != minute.Add(time.Minute).Format(time.RFC3339) {
			t.Fatalf("the records are %q, want the counts of 10 first and then 5 failures", e.added(t, before))
		}
		for _, r := range records[1:] {
			if recordName(r) != "login.failure/wrong-password" {
				t.Errorf("a record is %q", recordName(r))
			}
		}
	})

	t.Run("every kind is counted, by reason", func(t *testing.T) {
		e := setup(t)
		e.failures(t, failuresPerMinute)
		before := e.mark(t)
		// One of each kind of failure that the people who are not logged in make.
		e.failures(t, 2)
		for _, login := range []string{"nobody", "nobody", "nobody"} {
			_, _, _ = e.m.Login(t.Context(), mgrClient, login, mgrPassword("other"), "")
		}
		_, _, _ = e.m.Login(t.Context(), mgrClient, "yamada", strings.Repeat("a", maxTypedPasswordBytes+1), "")
		for _, reason := range []Refusal{RefusedTooLong, RefusedRateLimited, RefusedRateLimited, RefusedBusy} {
			e.m.LoginRefused(t.Context(), mgrClient, "someone", reason)
		}
		e.m.LoginRefused(t.Context(), mgrClient, "someone", "another reason")
		for _, token := range []string{"x", "y", tokSample} {
			_, _ = e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada"))
		}
		// A success is not a failure of someone who is not logged in: it is recorded, and it takes the counts.
		e.login(t, "yamada")
		records := e.records(t, before)
		if len(records) != 2 || records[0].Action != audit.LoginSuppressed || records[1].Action != audit.Login {
			t.Fatalf("the records are %q, want the counts and then the login", e.added(t, before))
		}
		want := map[string]string{
			"from": minute.Format(time.RFC3339), "to": minute.Add(time.Minute).Format(time.RFC3339),
			"loginFailureWrongPassword": "2", "loginFailureUnknownLogin": "3", "loginFailureTooLong": "2",
			"loginFailureRateLimited": "2", "loginFailureBusy": "1", "loginFailureRefused": "1",
			"tokenFailureMalformed": "2", "tokenFailureUnknown": "1",
		}
		if !reflect.DeepEqual(records[0].Detail, want) {
			t.Errorf("the counts are %v, want %v", records[0].Detail, want)
		}
		if err := records[0].Check(); err != nil {
			t.Errorf("the record of the counts is refused: %v", err)
		}
	})

	t.Run("the next write of the login history takes the counts, once", func(t *testing.T) {
		e := setup(t)
		_, token := e.login(t, "yamada")
		e.failures(t, failuresPerMinute+3)

		// A request that writes nothing leaves the counts.
		before := e.mark(t)
		if _, err := e.m.Authenticate(t.Context(), mgrClient, token); err != nil {
			t.Fatal(err)
		}
		if err := e.m.Logout(t.Context(), mgrClient, tokSample); err != nil {
			t.Fatal(err)
		}
		e.unchanged(t, before)
		if !e.m.limit.pending() {
			t.Fatal("the counts are gone after requests that write nothing")
		}

		e.clock.add(sessionTouch)
		before = e.mark(t)
		if _, err := e.m.Authenticate(t.Context(), mgrClient, token); err != nil {
			t.Fatal(err)
		}
		e.gained(t, before, "login.suppressed", "session.seen")
		if e.m.limit.pending() {
			t.Error("the counts are pending after the record that took them")
		}
		e.clock.add(sessionTouch)
		before = e.mark(t)
		if _, err := e.m.Authenticate(t.Context(), mgrClient, token); err != nil {
			t.Fatal(err)
		}
		e.gained(t, before, "session.seen")
	})

	t.Run("an Update that does not commit counts them again", func(t *testing.T) {
		e := setup(t)
		e.failures(t, failuresPerMinute+4)
		boom := errors.New("boom")
		before := e.mark(t)
		err := e.m.logged(t.Context(), func(_ *store.Tx, h *history) error {
			// The counts go with the first record, and the ones after it must not forget that they did.
			for _, key := range []string{"x", "y", "z"} {
				if err := h.add(audit.Record{Log: audit.LoginLog, Action: audit.Logout, Session: sessKey(key)}); err != nil {
					return err
				}
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("logged() = %v, want the error of the function", err)
		}
		e.unchanged(t, before)
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatal(err)
		}
		records := e.records(t, before)
		if len(records) != 1 || records[0].Detail["loginFailureWrongPassword"] != "4" {
			t.Errorf("the records are %+v, want the counts of 4 failures", records)
		}
	})

	t.Run("a login whose session is not written counts them again", func(t *testing.T) {
		e := setup(t)
		_, token := e.login(t, "yamada")
		e.failures(t, failuresPerMinute+2)
		before := e.mark(t)
		// A year that the row cannot hold: the old session is swept, and then the new one cannot be written.
		e.clock.set(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		if _, _, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("yamada"), ""); err == nil || errors.Is(err, ErrBadCredentials) {
			t.Fatalf("Login() = %v, want an error of the store", err)
		}
		// The sweep's records were not committed, and the failure of the login is recorded with the counts, once.
		e.gained(t, before, "login.suppressed", "login.failure/error")
		if !e.hasSession(t, token) {
			t.Error("the session was removed by a login that failed")
		}
		if e.m.limit.pending() {
			t.Error("the counts are pending after they were recorded")
		}
	})

	t.Run("a store that fails keeps the counts", func(t *testing.T) {
		e := setup(t)
		e.failures(t, failuresPerMinute+1)
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := e.m.FlushSuppressed(t.Context()); err == nil {
			t.Error("FlushSuppressed() on a closed store = nil, want its error")
		}
		if !e.m.limit.pending() {
			t.Error("the counts are gone after a flush that failed")
		}
	})

	t.Run("there is nothing to flush", func(t *testing.T) {
		e := setup(t)
		e.failures(t, 3)
		before := e.mark(t)
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatal(err)
		}
		e.unchanged(t, before)
		// A closed store is not met, since nothing is written.
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Errorf("FlushSuppressed() with nothing to record on a closed store = %v, want nil", err)
		}
	})

	t.Run("failures at the same time", func(t *testing.T) {
		e := setup(t)
		before := e.mark(t)
		const n = 150
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				e.m.LoginRefused(t.Context(), Client{Addr: netip.MustParseAddr(fmt.Sprintf("203.0.113.%d", i%250+1))}, "x", RefusedRateLimited)
			})
		}
		wg.Wait()
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatal(err)
		}
		// 60 are recorded one by one. The others are counted, and a record that is written while they are counted takes the
		// counts, so they may be in more than one record: all of them are there.
		recorded, counted := 0, 0
		for _, r := range e.records(t, before) {
			if r.Action == audit.LoginFailure {
				recorded++
			}
			if r.Action == audit.LoginSuppressed {
				var c int
				if _, err := fmt.Sscan(r.Detail["loginFailureRateLimited"], &c); err != nil {
					t.Fatalf("the counts of a record are %v: %v", r.Detail, err)
				}
				counted += c
			}
		}
		if recorded != failuresPerMinute || counted != n-failuresPerMinute {
			t.Errorf("%d refusals at once have made %d records and %d counts, want %d and %d", n, recorded, counted, failuresPerMinute, n-failuresPerMinute)
		}
	})
}

func TestLoginRefused(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	other := Client{Addr: netip.MustParseAddr("2001:db8::7"), UserAgent: "a client"}
	long := strings.Repeat("x", 100)
	before := e.mark(t)
	e.m.LoginRefused(t.Context(), other, " Someone\n", RefusedTooLong)
	e.m.LoginRefused(t.Context(), other, long, RefusedRateLimited)
	e.m.LoginRefused(t.Context(), Client{}, "", RefusedBusy)
	e.m.LoginRefused(t.Context(), other, "yamada", Refusal("not one that is known"))
	// The records have no account, even for a login name that an account has: the server did not look.
	histWant(t, e.records(t, before),
		audit.Record{Log: audit.LoginLog, Action: audit.LoginFailure, Result: "too-long", Client: other.record(), Login: "someone"},
		audit.Record{Log: audit.LoginLog, Action: audit.LoginFailure, Result: "rate-limited", Client: other.record(), Login: long[:64]},
		audit.Record{Log: audit.LoginLog, Action: audit.LoginFailure, Result: "busy"},
		audit.Record{Log: audit.LoginLog, Action: audit.LoginFailure, Result: "refused", Client: other.record(), Login: "yamada"},
	)
}

func TestRecordErrors(t *testing.T) {
	type reported struct {
		operation string
		err       error
	}
	setup := func(t *testing.T) (*mgrEnv, *[]reported) {
		t.Helper()
		e := newMgr(t)
		e.first(t)
		e.add(t, "yamada", RoleEditor)
		var got []reported
		e.m.OnRecordError(func(operation string, err error) { got = append(got, reported{operation, err}) })
		return e, &got
	}

	t.Run("a failure that cannot be recorded is answered as it is", func(t *testing.T) {
		e, got := setup(t)
		// The store fails after the password was compared, as when the disk is gone.
		e.m.hook = func(point string) {
			if point == "verified" {
				e.m.hook = nil
				if err := e.s.Close(); err != nil {
					t.Error(err)
				}
			}
		}
		p, token, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("other"), "")
		if !errors.Is(err, ErrBadCredentials) || err.Error() != sessWrong || !reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("Login() = %+v, %v, want ErrBadCredentials and no token", p, err)
		}
		if len(*got) != 1 || (*got)[0].operation != "login.failure" || (*got)[0].err == nil {
			t.Errorf("OnRecordError was called with %+v, want once, for login.failure", *got)
		}
	})

	t.Run("a refusal", func(t *testing.T) {
		e, got := setup(t)
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		e.m.LoginRefused(t.Context(), mgrClient, "yamada", RefusedBusy)
		if len(*got) != 1 || (*got)[0].operation != "login.failure" {
			t.Errorf("OnRecordError was called with %+v, want once, for login.failure", *got)
		}
	})

	t.Run("an error that shows nothing typed", func(t *testing.T) {
		e, got := setup(t)
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		const typed = "a password that nobody may see in an error"
		e.m.LoginRefused(t.Context(), mgrClient, typed, RefusedTooLong)
		if len(*got) != 1 || strings.Contains((*got)[0].err.Error(), typed) {
			t.Errorf("OnRecordError was called with %+v, want an error without the login name", *got)
		}
	})

	t.Run("a failure that is not allowed by the limit is not an error", func(t *testing.T) {
		e, got := setup(t)
		e.failures(t, failuresPerMinute)
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		e.m.LoginRefused(t.Context(), mgrClient, "yamada", RefusedBusy)
		if len(*got) != 0 {
			t.Errorf("OnRecordError was called with %+v, want none: the failure is counted, and not written", *got)
		}
	})

	t.Run("no function", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		e.m.LoginRefused(t.Context(), mgrClient, "yamada", RefusedBusy) // does not panic
	})

	t.Run("the end of the context of the request does not stop the record", func(t *testing.T) {
		e, got := setup(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before := e.mark(t)
		e.m.LoginRefused(ctx, mgrClient, "yamada", RefusedBusy)
		e.gained(t, before, "login.failure/busy")
		if len(*got) != 0 {
			t.Errorf("OnRecordError was called with %+v, want none", *got)
		}
	})
}
