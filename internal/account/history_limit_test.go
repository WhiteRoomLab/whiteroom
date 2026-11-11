package account

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
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
	wrong, unknown := failure{audit.LoginFailure, failWrongPassword}, failure{audit.LoginFailure, failUnknownLogin}
	busy, token := failure{audit.LoginFailure, failBusy()}, failure{audit.TokenFailure, tokenUnknown}
	late := minute.Add(59 * time.Second)

	for i := range failuresPerMinute {
		if !l.admit(minute.Add(time.Duration(i)*time.Second+999*time.Millisecond), classCompared, wrong) {
			t.Fatalf("failure %d of the minute that cost a hash is refused, want the first %d recorded", i+1, failuresPerMinute)
		}
	}
	// The class of the refusals has its places of its own: the failures that cost a hash did not use them.
	for i := range refusalsPerMinute {
		if !l.admit(minute.Add(time.Duration(i)*time.Second), classRefused, busy) {
			t.Fatalf("failure %d of the minute that cost no hash is refused, want the first %d recorded", i+1, refusalsPerMinute)
		}
	}
	if l.pending() {
		t.Error("counts are pending after the failures that are recorded")
	}
	// The failures after them are counted, of any kind, until the minute is over.
	if l.admit(late, classCompared, wrong) || l.admit(late, classCompared, unknown) || l.admit(late, classCompared, wrong) {
		t.Error("a failure that costs a hash, after the first 60 of the minute, is recorded")
	}
	if l.admit(late, classRefused, token) || l.admit(late, classRefused, busy) {
		t.Error("a failure that costs no hash, after the first 10 of the minute, is recorded")
	}
	if !l.pending() {
		t.Fatal("no counts are pending after failures that were not recorded")
	}
	// The next minute begins again, in both classes, and what was counted stays.
	if !l.admit(minute.Add(time.Minute), classCompared, wrong) || !l.admit(minute.Add(time.Minute), classRefused, token) {
		t.Error("the first failure of the next minute is refused")
	}
	for i := 1; i < failuresPerMinute; i++ {
		if !l.admit(minute.Add(time.Minute+time.Duration(i)*time.Millisecond), classCompared, unknown) {
			t.Fatalf("failure %d of the next minute that costs a hash is refused", i+1)
		}
	}
	for i := 1; i < refusalsPerMinute; i++ {
		if !l.admit(minute.Add(time.Minute+time.Duration(i)*time.Millisecond), classRefused, token) {
			t.Fatalf("failure %d of the next minute that costs no hash is refused", i+1)
		}
	}
	if l.admit(minute.Add(2*time.Minute-1), classCompared, unknown) || l.admit(minute.Add(2*time.Minute-1), classRefused, token) {
		t.Error("a failure after the places of the next minute is recorded")
	}

	got := l.take()
	if got == nil || got.counts[wrong] != 2 || got.counts[unknown] != 2 || got.counts[busy] != 1 || got.counts[token] != 2 ||
		len(got.counts) != 4 || !got.from.Equal(minute) || !got.to.Equal(minute.Add(2*time.Minute)) {
		t.Fatalf("take() = %+v, want 2, 2, 1 and 2, from %v to %v", got, minute, minute.Add(2*time.Minute))
	}
	if l.pending() || l.take() != nil {
		t.Error("counts are pending after take")
	}

	// restore counts them again, with what came meanwhile.
	l.restore(got, time.Time{})
	l.admit(minute.Add(10*time.Minute), classCompared, wrong)
	for range failuresPerMinute {
		l.admit(minute.Add(10*time.Minute), classCompared, wrong)
	}
	back := l.take()
	if back == nil || back.counts[wrong] != 3 || back.counts[unknown] != 2 || back.counts[busy] != 1 || back.counts[token] != 2 ||
		!back.from.Equal(minute) || !back.to.Equal(minute.Add(11*time.Minute)) {
		t.Errorf("take() after restore = %+v, want 3, 2, 1 and 2, from %v to %v", back, minute, minute.Add(11*time.Minute))
	}
}

// TestFailureLimitClassesAreApart checks that a class that is used up leaves the places of the other whole, whichever is
// used up first.
func TestFailureLimitClassesAreApart(t *testing.T) {
	at := time.Date(2026, 11, 8, 12, 3, 30, 0, time.UTC)
	wrong, busy := failure{audit.LoginFailure, failWrongPassword}, failure{audit.LoginFailure, failBusy()}
	for _, tt := range []struct {
		name         string
		first, other failureClass
		firstFailure failure
		otherFailure failure
		firstN       int
		otherN       int
	}{
		{"the refusals first", classRefused, classCompared, busy, wrong, refusalsPerMinute, failuresPerMinute},
		{"the failures that cost a hash first", classCompared, classRefused, wrong, busy, failuresPerMinute, refusalsPerMinute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var l failureLimit
			for range tt.firstN + 5 {
				l.admit(at, tt.first, tt.firstFailure)
			}
			for i := range tt.otherN {
				if !l.admit(at, tt.other, tt.otherFailure) {
					t.Fatalf("failure %d of the other class is refused after the first class was used up, want %d recorded", i+1, tt.otherN)
				}
			}
			if l.admit(at, tt.other, tt.otherFailure) {
				t.Errorf("failure %d of the other class is recorded", tt.otherN+1)
			}
			got := l.take()
			if got == nil || got.counts[tt.firstFailure] != 5 || got.counts[tt.otherFailure] != 1 || len(got.counts) != 2 {
				t.Errorf("take() = %+v, want 5 of the first kind and 1 of the other", got)
			}
		})
	}
}

// TestFailureLimitValues checks the numbers of 7.4: 60 failures that cost a hash and 10 that cost none are recorded in a
// minute, which makes at most 70 records of failures.
func TestFailureLimitValues(t *testing.T) {
	if failuresPerMinute != 60 || refusalsPerMinute != 10 || failureLimits != [numClasses]int{classCompared: 60, classRefused: 10} {
		t.Errorf("the limits are %d and %d (%v), want 60 and 10", failuresPerMinute, refusalsPerMinute, failureLimits)
	}
	if recordTimeout != 5*time.Second {
		t.Errorf("a record that stands alone waits %v for the store, want 5s", recordTimeout)
	}
}

func TestFailureLimitCountsWhatCouldNotBeWritten(t *testing.T) {
	var l failureLimit
	minute := time.Date(2026, 11, 8, 12, 3, 0, 0, time.UTC)
	wrong := failure{audit.LoginFailure, failWrongPassword}
	if !l.admit(minute.Add(10*time.Second), classCompared, wrong) {
		t.Fatal("the first failure is refused")
	}
	// The record of the failure that was let in could not be written: it is counted for the minute of its place, and the
	// place is not given back.
	l.count(minute.Add(10*time.Second), wrong)
	for range failuresPerMinute - 1 {
		if !l.admit(minute.Add(20*time.Second), classCompared, wrong) {
			t.Fatal("a failure of the places that are left is refused")
		}
	}
	if l.admit(minute.Add(30*time.Second), classCompared, wrong) {
		t.Error("the place of a failure that could not be written was given back")
	}
	got := l.take()
	if got == nil || got.counts[wrong] != 2 || !got.from.Equal(minute) || !got.to.Equal(minute.Add(time.Minute)) {
		t.Errorf("take() = %+v, want the failure that could not be written and the one over the limit, in the minute of 12:03", got)
	}
	l.count(minute.Add(5*time.Minute+59*time.Second), wrong)
	if got := l.take(); got == nil || got.counts[wrong] != 1 || !got.from.Equal(minute.Add(5*time.Minute)) ||
		!got.to.Equal(minute.Add(6*time.Minute)) {
		t.Errorf("take() = %+v, want 1, from 12:08 to 12:09", got)
	}
}

// TestFailureLimitTakesOncePerMinute checks that an Update takes the counts once in a minute of the clock, that take takes
// them in any minute and marks none, and that restore gives the minute back.
func TestFailureLimitTakesOncePerMinute(t *testing.T) {
	minute := time.Date(2026, 11, 8, 12, 3, 0, 0, time.UTC)
	wrong, busy := failure{audit.LoginFailure, failWrongPassword}, failure{audit.LoginFailure, failBusy()}
	at := func(minutes, seconds int) time.Time {
		return minute.Add(time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second)
	}

	t.Run("an Update takes the counts once in a minute", func(t *testing.T) {
		var l failureLimit
		// Nothing is counted: nothing is taken, and the minute is not marked.
		if got, marked := l.takeForUpdate(at(0, 1)); got != nil || !marked.IsZero() {
			t.Fatalf("takeForUpdate() without counts = %+v, %v, want nil and no minute", got, marked)
		}
		l.count(at(0, 5), wrong)
		got, marked := l.takeForUpdate(at(0, 10))
		if got == nil || got.counts[wrong] != 1 || !marked.Equal(minute) {
			t.Fatalf("takeForUpdate() = %+v, %v, want the count and the minute %v", got, marked, minute)
		}
		// What is counted after that waits for the next minute, at any time of this one.
		l.count(at(0, 20), busy)
		l.count(at(0, 59), wrong)
		if again, marked := l.takeForUpdate(at(0, 59)); again != nil || !marked.IsZero() {
			t.Errorf("takeForUpdate() in the minute in which one took = %+v, %v, want nil and no minute", again, marked)
		}
		if !l.pending() {
			t.Error("the counts are gone, though no one has taken them")
		}
		next, marked := l.takeForUpdate(at(1, 0))
		if next == nil || next.counts[busy] != 1 || next.counts[wrong] != 1 || len(next.counts) != 2 || !marked.Equal(at(1, 0)) ||
			!next.from.Equal(minute) || !next.to.Equal(at(1, 0)) {
			t.Errorf("takeForUpdate() in the next minute = %+v, %v, want the counts of 12:03, and the minute 12:04", next, marked)
		}
		if l.pending() {
			t.Error("counts are pending after they were taken")
		}
	})

	t.Run("take takes them in any minute and marks none", func(t *testing.T) {
		var l failureLimit
		l.count(at(0, 5), wrong)
		if got, _ := l.takeForUpdate(at(0, 10)); got == nil {
			t.Fatal("takeForUpdate() took nothing")
		}
		l.count(at(0, 20), busy)
		if got := l.take(); got == nil || got.counts[busy] != 1 || len(got.counts) != 1 {
			t.Errorf("take() in the minute in which an Update took = %+v, want the count that came after it", got)
		}
		// With no Update in the minute before it, an Update takes what comes after take: take marked nothing.
		var m failureLimit
		m.count(at(0, 5), wrong)
		if got := m.take(); got == nil || got.counts[wrong] != 1 {
			t.Fatalf("take() = %+v, want the count", got)
		}
		m.count(at(0, 30), busy)
		if got, marked := m.takeForUpdate(at(0, 40)); got == nil || got.counts[busy] != 1 || !marked.Equal(minute) {
			t.Errorf("takeForUpdate() after take in the same minute = %+v, %v, want the count and the minute", got, marked)
		}
	})

	t.Run("restore gives the minute back", func(t *testing.T) {
		var l failureLimit
		l.count(at(0, 5), wrong)
		got, marked := l.takeForUpdate(at(0, 10))
		if got == nil {
			t.Fatal("takeForUpdate() took nothing")
		}
		l.count(at(0, 20), busy) // comes while the Update runs
		l.restore(got, marked)
		again, marked := l.takeForUpdate(at(0, 30))
		if again == nil || again.counts[wrong] != 1 || again.counts[busy] != 1 || !marked.Equal(minute) {
			t.Errorf("takeForUpdate() after restore, in the same minute = %+v, %v, want what was taken and what came meanwhile", again, marked)
		}

		// What take returned has no minute to give back, and restore marks nothing.
		var m failureLimit
		m.count(at(0, 5), wrong)
		if taken := m.take(); taken != nil {
			m.restore(taken, time.Time{})
		}
		if got, marked := m.takeForUpdate(at(0, 10)); got == nil || !marked.Equal(minute) {
			t.Errorf("takeForUpdate() after take and restore = %+v, %v, want the count and the minute", got, marked)
		}
	})

	t.Run("restore leaves the minute that another Update has marked", func(t *testing.T) {
		var l failureLimit
		l.count(at(0, 5), wrong)
		first, firstMarked := l.takeForUpdate(at(0, 10))
		l.count(at(1, 5), busy)
		if second, _ := l.takeForUpdate(at(1, 10)); second == nil {
			t.Fatal("the Update of the next minute took nothing")
		}
		// The first Update, which is slow, does not commit now: the counts come back, and the minute 12:04 stays marked.
		l.restore(first, firstMarked)
		l.count(at(1, 20), wrong)
		if got, marked := l.takeForUpdate(at(1, 30)); got != nil || !marked.IsZero() {
			t.Errorf("takeForUpdate() in the minute that an Update marked = %+v, %v, want nil", got, marked)
		}
		got, _ := l.takeForUpdate(at(2, 0))
		if got == nil || got.counts[wrong] != 2 || len(got.counts) != 1 || !got.from.Equal(minute) || !got.to.Equal(at(2, 0)) {
			t.Errorf("takeForUpdate() in the minute 12:05 = %+v, want the two counts that wait, from 12:03", got)
		}
	})
}

func TestFailureLimitIsPerMinuteOfTheClock(t *testing.T) {
	// The minute is that of the instant, whatever zone the clock gives it in.
	var l failureLimit
	jst := time.FixedZone("JST", 9*3600)
	base := time.Date(2026, 11, 8, 21, 0, 30, 0, jst)
	wrong, busy := failure{audit.LoginFailure, failWrongPassword}, failure{audit.LoginFailure, failBusy()}
	for range failuresPerMinute {
		if !l.admit(base, classCompared, wrong) {
			t.Fatal("a failure of the first 60 is refused")
		}
	}
	for range refusalsPerMinute {
		if !l.admit(base, classRefused, busy) {
			t.Fatal("a refusal of the first 10 is refused")
		}
	}
	if l.admit(base.UTC(), classCompared, wrong) || l.admit(base.UTC(), classRefused, busy) {
		t.Error("the failure after the places, in another zone for the same instant, is recorded")
	}
	if !l.admit(base.Add(30*time.Second), classCompared, wrong) || !l.admit(base.Add(30*time.Second), classRefused, busy) {
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
		// The places of both classes are used up, so that every failure after them is counted.
		e.failures(t, failuresPerMinute)
		for range refusalsPerMinute {
			e.m.LoginRefused(t.Context(), mgrClient, "someone", RefusedBusy)
		}
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

	t.Run("an Update that does not commit leaves its minute to the next one", func(t *testing.T) {
		e := setup(t)
		e.failures(t, failuresPerMinute+4)
		boom := errors.New("boom")
		err := e.m.logged(t.Context(), func(_ *store.Tx, h *history) error {
			if err := h.add(audit.Record{Log: audit.LoginLog, Action: audit.Logout, Session: sessKey("x")}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("logged() = %v, want the error of the function", err)
		}
		// The Update took the counts, and gave them back with the minute: the next Update of the same minute takes them.
		before := e.mark(t)
		e.login(t, "yamada")
		e.gained(t, before, "login.suppressed", "login")
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
			wg.Go(func() {
				if _, _, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
					t.Errorf("Login() = %v, want ErrBadCredentials", err)
				}
			})
		}
		wg.Wait()
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatal(err)
		}
		// 60 that cost a hash and 10 that cost none are recorded one by one. The others are counted, and a record that is
		// written while they are counted takes the counts, so they may be in more than one record: all of them are there.
		recorded, refused, counted := 0, 0, map[string]int{}
		for _, r := range e.records(t, before) {
			if r.Action == audit.LoginFailure {
				recorded++
				if r.Result == "rate-limited" {
					refused++
				}
			}
			if r.Action == audit.LoginSuppressed {
				for _, key := range []string{"loginFailureRateLimited", "loginFailureWrongPassword"} {
					if r.Detail[key] == "" {
						continue
					}
					var c int
					if _, err := fmt.Sscan(r.Detail[key], &c); err != nil {
						t.Fatalf("the counts of a record are %v: %v", r.Detail, err)
					}
					counted[key] += c
				}
			}
		}
		if recorded != failuresPerMinute+refusalsPerMinute || refused != refusalsPerMinute ||
			counted["loginFailureRateLimited"] != n-refusalsPerMinute || counted["loginFailureWrongPassword"] != n-failuresPerMinute {
			t.Errorf("%d refusals and %d wrong passwords at once have made %d records (%d refusals) and the counts %v, want %d, %d, and %d and %d counted",
				n, n, recorded, refused, counted, failuresPerMinute+refusalsPerMinute, refusalsPerMinute, n-refusalsPerMinute, n-failuresPerMinute)
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

	t.Run("a failure that is not allowed by the limit of its class is not an error", func(t *testing.T) {
		closeStore := func(e *mgrEnv) {
			t.Helper()
			if err := e.s.Close(); err != nil {
				t.Fatal(err)
			}
		}
		// refuse and guess make one failure of each class with a store that fails when its record is written: a login that
		// is refused is recorded at once, and a login that fails is recorded after the password was compared.
		refuse := func(e *mgrEnv) {
			closeStore(e)
			e.m.LoginRefused(t.Context(), mgrClient, "yamada", RefusedBusy)
		}
		guess := func(e *mgrEnv) {
			e.m.hook = func(point string) {
				if point == "verified" {
					e.m.hook = nil
					closeStore(e)
				}
			}
			if _, _, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
				t.Errorf("Login() = %v, want ErrBadCredentials", err)
			}
		}
		useUpGuesses := func(e *mgrEnv) { e.failures(t, failuresPerMinute) }
		useUpRefusals := func(e *mgrEnv) {
			for range refusalsPerMinute {
				e.m.LoginRefused(t.Context(), mgrClient, "yamada", RefusedBusy)
			}
		}
		for _, tt := range []struct {
			name     string
			useUp    func(e *mgrEnv)
			fail     func(e *mgrEnv)
			wantCall int
		}{
			{"a refusal, when the refusals are used up", useUpRefusals, refuse, 0},
			{"a login, when the failures that cost a hash are used up", useUpGuesses, guess, 0},
			// A class that is used up does not stop the other from being tried.
			{"a refusal, when only the failures that cost a hash are used up", useUpGuesses, refuse, 1},
			{"a login, when only the refusals are used up", useUpRefusals, guess, 1},
		} {
			e, got := setup(t)
			tt.useUp(e)
			tt.fail(e)
			if len(*got) != tt.wantCall {
				t.Errorf("%s: OnRecordError was called with %+v, want %d calls: a failure that is over its limit is counted, and not written",
					tt.name, *got, tt.wantCall)
			}
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

// TestRefusalsDoNotCrowdOutGuesses checks that the logins that are refused before a hash is made and the tokens that are
// refused, which cost nothing to send, use only the places of their own in a minute, and leave the places of the failures
// that cost a hash, which record the address and the login name of a guess.
func TestRefusalsDoNotCrowdOutGuesses(t *testing.T) {
	minute := mgrTime.Truncate(time.Minute)
	cheap := Client{Addr: netip.MustParseAddr("198.51.100.1"), UserAgent: "cheap"}
	guesser := Client{Addr: netip.MustParseAddr("2001:db8::99"), UserAgent: "guesser"}
	setup := func(t *testing.T) (*mgrEnv, Account) {
		t.Helper()
		e := newMgr(t)
		e.first(t)
		return e, e.add(t, "yamada", RoleEditor)
	}
	summary := func(from time.Time, counts map[string]string) audit.Record {
		detail := map[string]string{"from": from.Format(time.RFC3339), "to": from.Add(time.Minute).Format(time.RFC3339)}
		for key, n := range counts {
			detail[key] = n
		}
		return audit.Record{Log: audit.LoginLog, Action: audit.LoginSuppressed, Detail: detail}
	}
	refused := func(c Client, login, result string) audit.Record {
		return audit.Record{Log: audit.LoginLog, Action: audit.LoginFailure, Result: result, Client: c.record(), Login: login}
	}
	repeat := func(n int, r audit.Record) []audit.Record {
		out := make([]audit.Record, n)
		for i := range out {
			out[i] = r
		}
		return out
	}

	t.Run("more refusals than places, and then a guess", func(t *testing.T) {
		e, yamada := setup(t)
		before := e.mark(t)
		for range refusalsPerMinute + 5 {
			e.m.LoginRefused(t.Context(), cheap, "x", RefusedTooLong)
		}
		if _, _, err := e.m.Login(t.Context(), guesser, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("Login() = %v, want ErrBadCredentials", err)
		}
		// The guess has its address and its login name, and the refusals over their places are counted before it.
		want := slices.Concat(repeat(refusalsPerMinute, refused(cheap, "x", "too-long")), []audit.Record{
			summary(minute, map[string]string{"loginFailureTooLong": "5"}),
			{Log: audit.LoginLog, Action: audit.LoginFailure, Result: "wrong-password", Account: yamada.ID, Client: guesser.record(), Login: "yamada"},
		})
		histWant(t, e.records(t, before), want...)
	})

	t.Run("a password that is too long, which Login turns away, is a refusal", func(t *testing.T) {
		e, _ := setup(t)
		before := e.mark(t)
		long := strings.Repeat("p", maxTypedPasswordBytes+1)
		for range refusalsPerMinute + 1 {
			if _, _, err := e.m.Login(t.Context(), cheap, "yamada", long, ""); !errors.Is(err, ErrBadCredentials) {
				t.Fatalf("Login() = %v, want ErrBadCredentials", err)
			}
		}
		e.failures(t, failuresPerMinute)
		records := e.records(t, before)
		if len(records) != refusalsPerMinute+1+failuresPerMinute {
			t.Fatalf("the records are %q, want %d refusals, the counts of one, and %d guesses", e.added(t, before), refusalsPerMinute, failuresPerMinute)
		}
		if got := histShape(records[refusalsPerMinute]); !reflect.DeepEqual(got, summary(minute, map[string]string{"loginFailureTooLong": "1"})) {
			t.Errorf("the record after the refusals is %+v, want the count of the refusal over the places", got)
		}
		for i, r := range records {
			want := "login.failure/wrong-password"
			switch {
			case i < refusalsPerMinute:
				want = "login.failure/too-long"
			case i == refusalsPerMinute:
				want = "login.suppressed"
			}
			if recordName(r) != want {
				t.Fatalf("record %d is %s, want %s", i+1, recordName(r), want)
			}
		}
	})

	t.Run("a token that is refused is a refusal", func(t *testing.T) {
		e, _ := setup(t)
		before := e.mark(t)
		for range refusalsPerMinute + 2 {
			if _, err := e.m.SetPassword(t.Context(), cheap, "x", mgrPassword("yamada")); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("SetPassword() = %v, want ErrInvalidToken", err)
			}
		}
		e.failures(t, failuresPerMinute)
		var tokens, guesses int
		var counts map[string]string
		for _, r := range e.records(t, before) {
			switch recordName(r) {
			case "token.failure/malformed":
				tokens++
			case "login.failure/wrong-password":
				guesses++
			case "login.suppressed":
				counts = r.Detail
			}
		}
		if tokens != refusalsPerMinute || guesses != failuresPerMinute || counts["tokenFailureMalformed"] != "2" || len(counts) != 3 {
			t.Errorf("%d refused tokens and %d wrong passwords were recorded, with the counts %v, want %d, %d and the count of 2 tokens",
				tokens, guesses, counts, refusalsPerMinute, failuresPerMinute)
		}
	})

	t.Run("each class begins again with the next minute", func(t *testing.T) {
		e, yamada := setup(t)
		for range refusalsPerMinute + 2 {
			e.m.LoginRefused(t.Context(), cheap, "x", RefusedBusy)
		}
		e.failures(t, failuresPerMinute+3)
		e.clock.add(time.Minute)
		before := e.mark(t)
		for range refusalsPerMinute + 1 {
			e.m.LoginRefused(t.Context(), cheap, "x", RefusedBusy)
		}
		e.failures(t, failuresPerMinute+1)

		// The first record of the minute has what the minute before counted: three guesses. An Update takes the counts once
		// in a minute, so the refusal and the guess over the places of this minute, which are counted after it, wait.
		want := slices.Concat([]audit.Record{summary(minute, map[string]string{"loginFailureWrongPassword": "3"})},
			repeat(refusalsPerMinute, refused(cheap, "x", "busy")),
			repeat(failuresPerMinute, audit.Record{Log: audit.LoginLog, Action: audit.LoginFailure, Result: "wrong-password",
				Account: yamada.ID, Client: mgrClient.record(), Login: "yamada"}),
		)
		histWant(t, e.records(t, before), want...)
		if !e.m.limit.pending() {
			t.Fatal("the failures over the places of the minute are not counted")
		}
		before = e.mark(t)
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatal(err)
		}
		histWant(t, e.records(t, before), summary(minute.Add(time.Minute),
			map[string]string{"loginFailureBusy": "1", "loginFailureWrongPassword": "1"}))
	})
}

// TestUnwrittenFailureIsCounted checks that a failure that took a place and whose record could not be written is
// reported, is counted for the next login.suppressed, which a store that writes again then takes, and does not give its
// place back, so that a store that does not answer keeps no more than the places of a minute waiting.
func TestUnwrittenFailureIsCounted(t *testing.T) {
	old := recordTimeout
	recordTimeout = 50 * time.Millisecond
	t.Cleanup(func() { recordTimeout = old })
	e := newMgr(t)
	e.first(t)
	e.add(t, "yamada", RoleEditor)
	type reported struct {
		operation string
		err       error
	}
	var got []reported
	e.m.OnRecordError(func(operation string, err error) { got = append(got, reported{operation, err}) })

	// Another Update holds the writer of the store, so that a record waits for it as long as recordTimeout, and is not
	// written.
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	stop := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(stop)
	go func() {
		done <- e.s.Update(context.Background(), func(*store.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	before := e.mark(t)

	other := Client{Addr: netip.MustParseAddr("198.51.100.2"), UserAgent: "other"}
	if _, _, err := e.m.Login(t.Context(), other, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("Login() = %v, want ErrBadCredentials", err)
	}
	e.m.LoginRefused(t.Context(), other, "yamada", RefusedRateLimited)
	if _, err := e.m.SetPassword(t.Context(), other, "x", mgrPassword("yamada")); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("SetPassword() = %v, want ErrInvalidToken", err)
	}
	var operations []string
	for _, r := range got {
		operations = append(operations, r.operation)
		if !errors.Is(r.err, context.DeadlineExceeded) {
			t.Errorf("OnRecordError was called with %v, want the end of the time that a record waits", r.err)
		}
	}
	if want := []string{"login.failure", "login.failure", "token.failure"}; !slices.Equal(operations, want) {
		t.Fatalf("OnRecordError was called for %q, want %q: once for each failure", operations, want)
	}
	e.unchanged(t, before)
	// The failures are counted, in the minute in which they came, and their places are not given back.
	if !e.m.limit.pending() || e.m.limit.written != [numClasses]int{classCompared: 1, classRefused: 2} {
		t.Errorf("the failures are counted = %v, and have used the places %v, want true and 1 and 2", e.m.limit.pending(), e.m.limit.written)
	}

	stop()
	if err := <-done; err != nil {
		t.Fatalf("the Update that held the writer = %v", err)
	}
	// Once the store writes again, the counts are recorded, with the minute and the reasons.
	if err := e.m.FlushSuppressed(t.Context()); err != nil {
		t.Fatalf("FlushSuppressed() = %v", err)
	}
	minute := mgrTime.Truncate(time.Minute)
	histWant(t, e.records(t, before), audit.Record{Log: audit.LoginLog, Action: audit.LoginSuppressed, Detail: map[string]string{
		"from": minute.Format(time.RFC3339), "to": minute.Add(time.Minute).Format(time.RFC3339),
		"loginFailureWrongPassword": "1", "loginFailureRateLimited": "1", "tokenFailureMalformed": "1",
	}})
	if e.m.limit.pending() {
		t.Error("the counts are pending after the record that took them")
	}

	// The place of the guess that was not written is not given back: of the 60 that follow in the same minute, 59 are
	// recorded, and the last is counted.
	before = e.mark(t)
	e.failures(t, failuresPerMinute)
	records := e.records(t, before)
	if len(records) != failuresPerMinute-1 {
		t.Errorf("%d guesses after the one that was not written have made %d records, want %d", failuresPerMinute, len(records), failuresPerMinute-1)
	}
	if !e.m.limit.pending() {
		t.Error("the last of the guesses is not counted")
	}

	// FlushSuppressed marked no minute, and no Update has taken counts in this one: the next Update takes the guess that
	// was counted.
	before = e.mark(t)
	e.login(t, "yamada")
	e.gained(t, before, "login.suppressed", "login")
}

// TestSuppressedOncePerMinute checks that the Updates of the failures that are recorded take the counts once in a minute
// of the Manager's clock. Refusals over their places and guesses that come by turns, the worst that a flood can do, make
// 70 records of failures and one record of the counts in a minute, and not one record of the counts for each guess; what is
// counted after it is recorded by the first Update of the next minute, or by FlushSuppressed in the same minute, and in
// the end the records tell every count.
func TestSuppressedOncePerMinute(t *testing.T) {
	minute := mgrTime.Truncate(time.Minute)
	cheap := Client{Addr: netip.MustParseAddr("198.51.100.1"), UserAgent: "cheap"}
	guesser := Client{Addr: netip.MustParseAddr("2001:db8::99"), UserAgent: "guesser"}
	const overGuesses = 5

	// setup uses up the places of the refusals, and then makes failuresPerMinute turns of a refusal over its places and a
	// guess, and overGuesses guesses over their places: the refusals that are counted are refused 60 times, the guesses
	// 5 times.
	setup := func(t *testing.T) (*mgrEnv, mgrMark) {
		t.Helper()
		e := newMgr(t)
		e.first(t)
		e.add(t, "yamada", RoleEditor)
		before := e.mark(t)
		for range refusalsPerMinute {
			e.m.LoginRefused(t.Context(), cheap, "x", RefusedBusy)
		}
		for range failuresPerMinute {
			e.m.LoginRefused(t.Context(), cheap, "x", RefusedBusy)
			if _, _, err := e.m.Login(t.Context(), guesser, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
				t.Fatalf("Login() = %v, want ErrBadCredentials", err)
			}
		}
		e.failures(t, overGuesses)
		return e, before
	}
	// summaries returns the records of the counts of records, and the sum of each count in them.
	summaries := func(t *testing.T, records []audit.Record) (n int, sum map[string]int) {
		t.Helper()
		sum = map[string]int{}
		for _, r := range records {
			if r.Action != audit.LoginSuppressed {
				continue
			}
			n++
			for key, value := range r.Detail {
				if key == "from" || key == "to" {
					continue
				}
				c, err := strconv.Atoi(value)
				if err != nil {
					t.Fatalf("the count %s of a record of the counts is %q", key, value)
				}
				sum[key] += c
			}
		}
		return n, sum
	}
	// wantSum is every count that was not recorded: the refusals after the first 10, and the guesses after the first 60.
	wantSum := map[string]int{"loginFailureBusy": failuresPerMinute, "loginFailureWrongPassword": overGuesses}

	t.Run("one record of the counts in the minute", func(t *testing.T) {
		e, before := setup(t)
		names := e.added(t, before)
		var refusals, guesses int
		for _, name := range names {
			switch name {
			case "login.failure/busy":
				refusals++
			case "login.failure/wrong-password":
				guesses++
			}
		}
		n, _ := summaries(t, e.records(t, before))
		// 70 records of failures and 1 of the counts, and not the 130 that a record of the counts for each guess made.
		if refusals != refusalsPerMinute || guesses != failuresPerMinute || n != 1 || len(names) != 71 {
			t.Fatalf("the minute has made %d records, %d refusals, %d guesses and %d of the counts, want 71, %d, %d and 1",
				len(names), refusals, guesses, n, refusalsPerMinute, failuresPerMinute)
		}
		// It is the one that the first guess took: before the guess, after the refusals that were recorded, and with the
		// refusal that was counted before it.
		records := e.records(t, before)
		if got := histShape(records[refusalsPerMinute]); !reflect.DeepEqual(got, audit.Record{Log: audit.LoginLog,
			Action: audit.LoginSuppressed, Detail: map[string]string{
				"from": minute.Format(time.RFC3339), "to": minute.Add(time.Minute).Format(time.RFC3339), "loginFailureBusy": "1",
			}}) {
			t.Errorf("the record of the counts is %+v, want the one refusal that was counted before the first guess", got)
		}
		if !e.m.limit.pending() {
			t.Error("the counts that came after it are gone")
		}
	})

	t.Run("the first Update of the next minute takes the rest", func(t *testing.T) {
		e, before := setup(t)
		e.clock.add(time.Minute)
		// The places of the new minute are free, and this guess is recorded, with the counts before it.
		if _, _, err := e.m.Login(t.Context(), guesser, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("Login() = %v, want ErrBadCredentials", err)
		}
		records := e.records(t, before)
		n, sum := summaries(t, records)
		if n != 2 || !reflect.DeepEqual(sum, wantSum) {
			t.Errorf("the records have %d of the counts, which add up to %v, want 2, and %v: every count is in one", n, sum, wantSum)
		}
		last := records[len(records)-2]
		if last.Action != audit.LoginSuppressed || last.Detail["from"] != minute.Format(time.RFC3339) ||
			last.Detail["to"] != minute.Add(time.Minute).Format(time.RFC3339) || last.Detail["loginFailureBusy"] != "59" ||
			last.Detail["loginFailureWrongPassword"] != "5" {
			t.Errorf("the record before the guess of the next minute is %+v, want the counts of the minute before", last)
		}
		if e.m.limit.pending() {
			t.Error("counts are pending after the record that took them")
		}
	})

	t.Run("FlushSuppressed writes the rest in the same minute", func(t *testing.T) {
		e, before := setup(t)
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatalf("FlushSuppressed() = %v", err)
		}
		n, sum := summaries(t, e.records(t, before))
		if n != 2 || !reflect.DeepEqual(sum, wantSum) {
			t.Errorf("the records have %d of the counts, which add up to %v, want 2 (the one of the Update and the one of the flush), and %v", n, sum, wantSum)
		}
		if e.m.limit.pending() {
			t.Error("counts are pending after FlushSuppressed")
		}
		// Nothing is left to take: a later write of the login history has only its own record.
		before = e.mark(t)
		e.login(t, "yamada")
		e.gained(t, before, "login")
		if err := e.m.FlushSuppressed(t.Context()); err != nil {
			t.Fatal(err)
		}
		e.gained(t, before, "login")
	})

	t.Run("a flood of three minutes, with a flush in each", func(t *testing.T) {
		// The server flushes every minute: each minute has 70 records of failures, the record of the counts that the Update
		// took, and the one of the flush. A turn of a refusal and a guess has the refusal counted from the 11th turn on.
		e := newMgr(t)
		e.first(t)
		e.add(t, "yamada", RoleEditor)
		for minutes := range 3 {
			before := e.mark(t)
			for range failuresPerMinute + 20 {
				e.m.LoginRefused(t.Context(), cheap, "x", RefusedBusy)
				if _, _, err := e.m.Login(t.Context(), guesser, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
					t.Fatalf("Login() = %v, want ErrBadCredentials", err)
				}
			}
			if err := e.m.FlushSuppressed(t.Context()); err != nil {
				t.Fatal(err)
			}
			records := e.records(t, before)
			n, sum := summaries(t, records)
			if len(records) != failuresPerMinute+refusalsPerMinute+2 || n != 2 ||
				sum["loginFailureBusy"] != failuresPerMinute+20-refusalsPerMinute || sum["loginFailureWrongPassword"] != 20 {
				t.Errorf("minute %d of a flood has made %d records, %d of them of the counts, which add up to %v, want %d, 2, and %d busy and 20 wrong passwords",
					minutes+1, len(records), n, sum, failuresPerMinute+refusalsPerMinute+2, failuresPerMinute+20-refusalsPerMinute)
			}
			e.clock.add(time.Minute)
		}
	})
}
