package edit

import (
	"encoding/json/v2"
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// testClock is the clock of an Editor that a test sets between its operations.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// newClockSample is newSample with a clock that the test sets, from testNow.
func newClockSample(t *testing.T) (*Editor, *store.Store, *testClock) {
	t.Helper()
	ed, s := newSample(t)
	clock := &testClock{now: testNow}
	ed.SetClock(clock.Now)
	return ed, s, clock
}

// operationIDs returns the IDs that s has records of, in order.
func operationIDs(t *testing.T, s *store.Store) []string {
	t.Helper()
	var ids []string
	err := s.View(t.Context(), func(tx *store.Tx) error {
		return tx.Operations(func(id string, _ []byte) bool {
			ids = append(ids, id)
			return true
		})
	})
	if err != nil {
		t.Fatalf("Operations() = %v", err)
	}
	return ids
}

// operationData returns the record of the operation ID id as the store keeps it.
func operationData(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	var data []byte
	err := s.View(t.Context(), func(tx *store.Tx) (err error) {
		data, _, err = tx.Operation(id)
		return err
	})
	if err != nil {
		t.Fatalf("Operation(%q) = %v", id, err)
	}
	return string(data)
}

// putOperationData puts data as the record of the operation ID id, as an earlier program or another could have.
func putOperationData(t *testing.T, s *store.Store, id, data string) {
	t.Helper()
	err := s.Update(t.Context(), storeWrite(func(tx *store.Tx) error { return tx.PutOperation(id, []byte(data)) }))
	if err != nil {
		t.Fatalf("PutOperation(%q) = %v", id, err)
	}
}

// putOperationAt puts a record of the operation ID id that has the time at, and a request that no operation makes.
func putOperationAt(t *testing.T, s *store.Store, id string, at time.Time) {
	t.Helper()
	data, err := json.Marshal(record{Request: "sha256:" + strings.Repeat("0", 64), At: at.UTC()})
	if err != nil {
		t.Fatal(err)
	}
	putOperationData(t, s, id, string(data))
}

// expireRecords returns the records operations.expire of the activity log of s, from the oldest.
func expireRecords(t *testing.T, s *store.Store) []audit.Record {
	t.Helper()
	var out []audit.Record
	for _, r := range activity(t, s) {
		if r.Action == audit.OperationsExpire {
			out = append(out, r)
		}
	}
	return out
}

// checkExpired checks that the activity log of s has the records operations.expire that tell of the numbers of records
// that want has, and that each is the system's, with the detail expired alone.
func checkExpired(t *testing.T, s *store.Store, want ...string) {
	t.Helper()
	var got []string
	for _, r := range expireRecords(t, s) {
		got = append(got, r.Detail["expired"])
		if r.Actor != audit.System || r.Target != (audit.Target{}) || r.Operation != "" || r.Changed || len(r.Detail) != 1 ||
			r.Revision != "" || r.Status != "" || r.Before != (audit.Version{}) || r.After != (audit.Version{}) {
			t.Errorf("the record of operations.expire is %+v, want the system's with the detail expired alone", r)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("the records of operations.expire tell of %q records, want %q", got, want)
	}
	verifyChains(t, s)
}

// TestOperationRecordTime checks that the record of an operation ID has the time of the operation, in UTC.
func TestOperationRecordTime(t *testing.T) {
	for _, tt := range []struct {
		name string
		now  time.Time
		want string
	}{
		{"UTC", testNow, `"at":"2026-11-07T10:00:00Z"}`},
		{"another zone", testNow.In(time.FixedZone("JST", 9*60*60)), `"at":"2026-11-07T10:00:00Z"}`},
		{"fractions", testNow.Add(123456789 * time.Nanosecond), `"at":"2026-11-07T10:00:00.123456789Z"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			ed.SetClock(func() time.Time { return tt.now })
			op := Op{By: userID, ID: testOpID(1)}
			must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
			data := operationData(t, s, op.ID)
			if !strings.HasSuffix(data, tt.want) || !strings.HasPrefix(data, `{"request":"sha256:`) {
				t.Errorf("the record is %s, want it to start with the request and end with %s", data, tt.want)
			}
			rec, err := decodeRecord(op.ID, []byte(data))
			if err != nil || !rec.At.Equal(tt.now) || rec.At.Location() != time.UTC {
				t.Errorf("the time of the record is %v, %v; want %v in UTC", rec.At, err, tt.now)
			}
		})
	}

	// An operation without an ID makes no record, and a retry does not change the time of the record.
	ed, s, clock := newClockSample(t)
	op := Op{By: userID, ID: testOpID(2)}
	first := must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	before := operationData(t, s, op.ID)
	clock.Set(testNow.Add(10 * time.Minute))
	again := must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	if !reflect.DeepEqual(first, again) || operationData(t, s, op.ID) != before {
		t.Errorf("a retry returns %+v and leaves the record %s; want %+v and the record %s", again,
			operationData(t, s, op.ID), first, before)
	}
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if got := operationIDs(t, s); !slices.Equal(got, []string{op.ID}) {
		t.Errorf("the operation IDs that have a record are %q, want %q", got, op.ID)
	}
}

// TestOperationsExpire checks that the record of an operation ID answers a retry for 24 hours, and is removed by the next
// operation after that, with the record operations.expire.
func TestOperationsExpire(t *testing.T) {
	ed, s, clock := newClockSample(t)
	var notified []Result
	ed.Notify(func(r Result) { notified = append(notified, r) })
	op := Op{By: userID, ID: testOpID(1)}

	// The first operation looks for records to remove, and finds none: nothing is written for that.
	first := must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	checkExpired(t, s)
	entries := len(activity(t, s))
	before := snapshot(t, s)

	// Within 24 hours, a retry returns the first result and does nothing, however many operations come between.
	clock.Set(testNow.Add(24*time.Hour - time.Minute))
	again := must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	if !reflect.DeepEqual(first, again) || snapshot(t, s) != before {
		t.Errorf("a retry after 23 hours and 59 minutes = %+v; want the first result %+v and no change", again, first)
	}
	if len(notified) != 1 {
		t.Errorf("Notify has been called %d times, want 1: a retry that returns a recorded result is not notified", len(notified))
	}
	if got := len(activity(t, s)); got != entries {
		t.Errorf("the activity log has %d records after the retry, want %d", got, entries)
	}
	checkExpired(t, s)

	// After 25 hours, an operation of any kind, also one without an operation ID, removes the record first.
	clock.Set(testNow.Add(25 * time.Hour))
	other := must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if operationRecorded(t, s, op.ID) {
		t.Errorf("the record of %s is still there after 25 hours and an operation", op.ID)
	}
	checkExpired(t, s, "1")
	log := activity(t, s)
	if len(log) != entries+2 || log[entries].Action != audit.OperationsExpire || log[entries+1].Action != audit.EntryCreate {
		t.Errorf("the activity log ends with %v, want the removal and then the operation", actions(log[entries:]))
	}
	if len(notified) != 2 || notified[1].ID != other.ID {
		t.Errorf("Notify has been called with %d results, the last %+v; want 2, the last that of the operation (the removal "+
			"is not notified)", len(notified), notified[len(notified)-1])
	}

	// The same ID again is a new operation, and gets a record of its own.
	again = must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	if again.ID == first.ID || again.ID == other.ID {
		t.Errorf("CreateEntry() with the ID of a removed record made %s, want a new entry (not %s or %s)", again.ID,
			first.ID, other.ID)
	}
	rec, err := decodeRecord(op.ID, []byte(operationData(t, s, op.ID)))
	if err != nil || !rec.At.Equal(testNow.Add(25*time.Hour)) {
		t.Errorf("the new record has the time %v, %v; want that of the new operation", rec.At, err)
	}
	// And it answers a retry in its turn.
	if retry := must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja")); !reflect.DeepEqual(retry, again) {
		t.Errorf("a retry of the new operation = %+v, want %+v", retry, again)
	}
	checkExpired(t, s, "1")
}

// actions returns the actions of records.
func actions(records []audit.Record) []audit.Action {
	var out []audit.Action
	for _, r := range records {
		out = append(out, r.Action)
	}
	return out
}

// TestOperationsExpireEvery checks that an operation looks for the records to remove at most once an hour, and at the
// first operation of an Editor, whatever the age of the records.
func TestOperationsExpireEvery(t *testing.T) {
	ed, s, clock := newClockSample(t)
	op1, op2, op3 := testOpID(1), testOpID(2), testOpID(3)
	putOperationAt(t, s, op1, testNow.Add(-48*time.Hour))

	// The first operation removes the record that is old, before it.
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if operationRecorded(t, s, op1) {
		t.Errorf("the record of %s is there after the first operation", op1)
	}
	checkExpired(t, s, "1")

	// Within the hour, a record that is old stays, for any number of operations.
	putOperationAt(t, s, op2, testNow.Add(-48*time.Hour))
	for _, after := range []time.Duration{0, time.Minute, 30 * time.Minute, time.Hour - time.Nanosecond} {
		clock.Set(testNow.Add(after))
		must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
		if !operationRecorded(t, s, op2) {
			t.Errorf("the record of %s is gone %v after the last look, want it there until an hour has passed", op2, after)
		}
	}
	checkExpired(t, s, "1")

	// At the hour, the look is made again.
	clock.Set(testNow.Add(time.Hour))
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if operationRecorded(t, s, op2) {
		t.Errorf("the record of %s is there an hour after the last look, want it removed", op2)
	}
	checkExpired(t, s, "1", "1")

	// The next look is an hour after that one, not after the first.
	putOperationAt(t, s, op3, testNow.Add(-48*time.Hour))
	clock.Set(testNow.Add(time.Hour + 59*time.Minute))
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if !operationRecorded(t, s, op3) {
		t.Errorf("the record of %s is gone 59 minutes after the last look, want it there", op3)
	}
	clock.Set(testNow.Add(2 * time.Hour))
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if operationRecorded(t, s, op3) {
		t.Errorf("the record of %s is there an hour after the last look, want it removed", op3)
	}
	checkExpired(t, s, "1", "1", "1")

	// Another Editor of the store looks at its first operation.
	putOperationAt(t, s, op1, testNow.Add(-48*time.Hour))
	other := testEditor(t, s)
	other.ids = content.NewIDSource(rand.NewChaCha8([32]byte{'o', 't', 'h', 'e', 'r'}))
	other.SetClock(clock.Now)
	must(t)(other.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if operationRecorded(t, s, op1) {
		t.Errorf("the record of %s is there after the first operation of another Editor, want it removed", op1)
	}
	checkExpired(t, s, "1", "1", "1", "1")
}

// TestOperationsExpireBoundary checks which records the look removes: those made 24 hours before the time of the
// operation or earlier, and those without a time, and not those that are newer, those that are in the future, and those
// that cannot be read; and that it tells how many it has removed, and none when there are none.
func TestOperationsExpireBoundary(t *testing.T) {
	ed, s, clock := newClockSample(t)
	now := testNow.Add(100 * time.Hour)
	clock.Set(now)
	removed := map[string]time.Time{
		testOpID(1): now.Add(-24 * time.Hour),
		testOpID(2): now.Add(-24*time.Hour - time.Nanosecond),
		testOpID(3): now.Add(-25 * time.Hour),
		testOpID(4): time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	kept := map[string]time.Time{
		testOpID(5): now.Add(-24*time.Hour + time.Nanosecond),
		testOpID(6): now.Add(-23 * time.Hour),
		testOpID(7): now,
		testOpID(8): now.Add(time.Hour), // the clock was ahead
	}
	for id, at := range removed {
		putOperationAt(t, s, id, at)
	}
	for id, at := range kept {
		putOperationAt(t, s, id, at)
	}
	// A record without a time is from an earlier program, and old. One that cannot be read stays: its retry fails with an
	// error that tells so, and it is for someone to look at.
	const legacy = `{"request":"sha256:00","result":{"kind":"entry","version":{"seq":1,"kind":"entry",` +
		`"savedAt":"2026-11-07T10:00:00Z","savedBy":"` + userID + `"}}}`
	putOperationData(t, s, testOpID(9), legacy)
	unreadable := map[string]string{
		testOpID(10): "not JSON",
		testOpID(11): `{"request":"sha256:00","at":"yesterday"}`,
		testOpID(12): `{"request":1}`,
		testOpID(13): "",
	}
	for id, data := range unreadable {
		putOperationData(t, s, id, data)
	}

	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	want := slices.Sorted(func(yield func(string) bool) {
		for id := range kept {
			if !yield(id) {
				return
			}
		}
		for id := range unreadable {
			if !yield(id) {
				return
			}
		}
	})
	if got := operationIDs(t, s); !slices.Equal(got, want) {
		t.Errorf("the operation IDs that have a record are %q, want %q", got, want)
	}
	for id, data := range unreadable {
		if got := operationData(t, s, id); got != data {
			t.Errorf("the record of %s is %q, want %q: it is not read, and stays as it is", id, got, data)
		}
	}
	checkExpired(t, s, "5") // 4 with a time that is not newer, and the one without a time

	// The next look, an hour later, removes the two that have become old, and not those that are newer.
	clock.Set(now.Add(time.Hour))
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	for id, wantRecorded := range map[string]bool{
		testOpID(5): false, testOpID(6): false, testOpID(7): true, testOpID(8): true,
	} {
		if got := operationRecorded(t, s, id); got != wantRecorded {
			t.Errorf("the record of %s is there after the second look: %v, want %v", id, got, wantRecorded)
		}
	}
	checkExpired(t, s, "5", "2")

	// A look that finds none writes nothing.
	clock.Set(now.Add(2 * time.Hour))
	before := len(activity(t, s))
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if got := len(activity(t, s)); got != before+1 {
		t.Errorf("the activity log has %d records after an operation and a look that removes nothing, want %d", got, before+1)
	}
	checkExpired(t, s, "5", "2")
}

// TestOperationsExpireLegacy checks that a record that an earlier program made, without a time, answers a retry until the
// next look, which removes it.
func TestOperationsExpireLegacy(t *testing.T) {
	ed, s, clock := newClockSample(t)
	op := Op{By: userID, ID: testOpID(1)}
	first := must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	data := operationData(t, s, op.ID)
	const at = `,"at":"2026-11-07T10:00:00Z"}`
	if !strings.HasSuffix(data, at) {
		t.Fatalf("the record is %s, want it to end with %s", data, at)
	}
	putOperationData(t, s, op.ID, strings.TrimSuffix(data, at)+"}")

	// It is a record as it was made before the time was kept, and it answers a retry.
	clock.Set(testNow.Add(10 * time.Minute))
	before := snapshot(t, s)
	again := must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	if !reflect.DeepEqual(first, again) || snapshot(t, s) != before {
		t.Errorf("a retry with a record without a time = %+v; want the first result %+v and no change", again, first)
	}
	checkExpired(t, s)

	// The next look removes it, however new the operation was.
	clock.Set(testNow.Add(time.Hour))
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if operationRecorded(t, s, op.ID) {
		t.Errorf("the record without a time is still there after a look")
	}
	checkExpired(t, s, "1")
	if again = must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja")); again.ID == first.ID {
		t.Errorf("CreateEntry() after the record was removed returned the first entry %s, want a new one", again.ID)
	}
}

// TestOperationsExpireFails checks that an operation succeeds when the Update that removes the records fails, that the
// records stay then, and that the next look is an hour after the one that failed.
func TestOperationsExpireFails(t *testing.T) {
	ed, s, clock := newClockSample(t)
	errExpire := errors.New("expire")
	calls := 0
	ed.beforeExpire = func() error {
		calls++
		return errExpire
	}
	putOperationAt(t, s, testOpID(1), testNow.Add(-48*time.Hour))

	// The first operation: the look fails, and the operation does not.
	op := Op{By: userID, ID: testOpID(2)}
	res, err := ed.CreateEntry(t.Context(), op, content.TypePost, "ja")
	if err != nil || res.ID == "" {
		t.Fatalf("CreateEntry() when the look fails = %+v, %v; want it to succeed", res, err)
	}
	if calls != 1 || !operationRecorded(t, s, testOpID(1)) || !operationRecorded(t, s, op.ID) {
		t.Errorf("after the failed look: %d calls, the old record %v, the new record %v; want 1, true, true", calls,
			operationRecorded(t, s, testOpID(1)), operationRecorded(t, s, op.ID))
	}
	checkExpired(t, s)

	// Not again within the hour, whether it failed or not.
	ed.beforeExpire = nil
	clock.Set(testNow.Add(59 * time.Minute))
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if !operationRecorded(t, s, testOpID(1)) {
		t.Errorf("the old record is gone 59 minutes after the look that failed, want it there")
	}
	clock.Set(testNow.Add(time.Hour))
	must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"))
	if operationRecorded(t, s, testOpID(1)) || !operationRecorded(t, s, op.ID) {
		t.Errorf("the old record is there %v and the new one %v an hour after the look that failed, want false and true",
			operationRecorded(t, s, testOpID(1)), operationRecorded(t, s, op.ID))
	}
	checkExpired(t, s, "1")
}

// TestOperationsExpireConcurrent checks that of the operations of several goroutines that come together at the first
// look, one makes it.
func TestOperationsExpireConcurrent(t *testing.T) {
	ed, s, _ := newClockSample(t)
	for n := 1; n <= 3; n++ {
		putOperationAt(t, s, testOpID(n), testNow.Add(-48*time.Hour))
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := operationIDs(t, s); got != nil {
		t.Errorf("the operation IDs that have a record are %q, want none", got)
	}
	checkExpired(t, s, "3")
}
