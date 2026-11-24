package store

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// dbwire.ReadRecordID reads exactly what audit.Decode reads, so that whiteroom-db compact and accept-break take for a record
// what VerifyAudit does. dbwire cannot import audit, and has its own type of the shape of audit.Record: these tests pin them
// together.

// jsonShape describes the JSON that Go type t reads: the names of its members and the kinds of their values, to any depth.
func jsonShape(t reflect.Type) string {
	switch {
	case t == reflect.TypeFor[time.Time]():
		return "time"
	case t.Kind() == reflect.Struct:
		var members []string
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" || !f.IsExported() {
				members = append(members, "!"+f.Name) // a member that has no name of its own is a difference too
				continue
			}
			members = append(members, name+":"+jsonShape(f.Type))
		}
		slices.Sort(members)
		return "{" + strings.Join(members, ",") + "}"
	case t.Kind() == reflect.Map:
		return "map[" + t.Key().Kind().String() + "]" + jsonShape(t.Elem())
	}
	return t.Kind().String()
}

// TestAuditRecordShape checks that the type that dbwire reads a record into has the members of audit.Record and the same
// kinds of values, in the structs inside it too.
func TestAuditRecordShape(t *testing.T) {
	want, got := jsonShape(reflect.TypeFor[audit.Record]()), jsonShape(reflect.TypeOf(dbwire.RecordShape()))
	if want != got {
		t.Errorf("dbwire reads a record as\n%s\nand audit.Decode as\n%s", got, want)
	}
	if !strings.Contains(want, "target:{id:string,kind:string}") || !strings.Contains(want, "detail:map[string]string") {
		t.Errorf("the shape of audit.Record is %s, which has no nested struct or no map: the check does not look deep enough", want)
	}
}

// fullRecord is a record that has every member that audit.Record has, and every member of the structs inside it.
const fullRecord = `{"log":"login","n":3,"prev":"sha256:71547e237559add83f6c9279fdb5762eafee857a1060e2d97c0316a70cefb6ba",` +
	`"at":"2026-11-12T01:02:03.456789Z","actor":"system","action":"login.failure","result":"wrong-password",` +
	`"target":{"kind":"entry","id":"ent_01JB8W2N5KD3F6H9J1M4P7R2T5"},"account":"usr_01JB8T7H4MA2C5E8G1J4K7N3P6","session":"s",` +
	`"client":{"addr":"203.0.113.7","userAgent":"Mozilla/5.0"},"login":"yamada","operation":"op_01JBG3N6Y6A9D2F5H8K1N4Q7S0",` +
	`"changed":true,"before":{"seq":17,"hash":"sha256:43cc23fa52b87b4cc1d02b5b114154151d6adddb17c9fddc06b027fa99e24008"},` +
	`"after":{"seq":18,"hash":"sha256:43cc23fa52b87b4cc1d02b5b114154151d6adddb17c9fddc06b027fa99e24008"},` +
	`"revision":"rev_01JB8W2N5KD3F6H9J1M4P7R2T5","status":"done","detail":{"n":"7","prev":"x"}}`

// zeroMembers returns the names of the members of v, a struct that is read from JSON, that are zero.
func zeroMembers(v reflect.Value, prefix string) []string {
	var zero []string
	for i := range v.NumField() {
		name := prefix + v.Type().Field(i).Name
		if f := v.Field(i); f.Kind() == reflect.Struct && f.Type() != reflect.TypeFor[time.Time]() {
			zero = append(zero, zeroMembers(f, name+".")...)
		} else if f.IsZero() {
			zero = append(zero, name)
		}
	}
	return zero
}

// staticRecords are records that do not depend on a store: one with every member, one of each log with only what a record
// must have, and the first record that a store writes.
func staticRecords() [][]byte {
	return [][]byte{
		[]byte(fullRecord),
		[]byte(strings.Replace(fullRecord, `"log":"login"`, `"log":"activity"`, 1)),
		[]byte(`{"log":"login","n":1,"at":"2026-11-03T08:00:00Z","action":"x"}`),
		[]byte(`{"log":"activity","n":2,"prev":"p","at":"2026-11-03T08:00:00.5+09:00","action":"x"}`),
		[]byte(createRecordJSON),
	}
}

// mutations returns the records that data, a record that is an object, becomes when each of its members, and each member of
// the objects inside it, is taken away, is another value, or is not alone, and other things that make it no record.
func mutations(t testing.TB, data []byte) [][]byte {
	t.Helper()
	var members map[string]jsontext.Value
	if err := json.Unmarshal(data, &members); err != nil {
		t.Fatalf("%s is no object: %v", data, err)
	}
	values := []string{`null`, `1`, `0`, `-1`, `1.5`, `"x"`, `""`, `true`, `[]`, `{}`, `18446744073709551616`, `"0001-01-01T00:00:00Z"`,
		`"2026-11-12T01:02:03"`, `"2026-02-30T00:00:00Z"`, `"other"`}
	var out [][]byte
	emit := func(m map[string]jsontext.Value) {
		b, err := json.Marshal(m, json.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	with := func(m map[string]jsontext.Value, name string, v jsontext.Value) map[string]jsontext.Value {
		c := maps.Clone(m)
		if v == nil {
			delete(c, name)
		} else {
			c[name] = v
		}
		return c
	}
	for _, name := range slices.Sorted(maps.Keys(members)) {
		emit(with(members, name, nil))
		for _, v := range values {
			emit(with(members, name, jsontext.Value(v)))
		}
		var inner map[string]jsontext.Value
		if json.Unmarshal(members[name], &inner) != nil || inner == nil {
			continue
		}
		set := func(m map[string]jsontext.Value) {
			b, err := json.Marshal(m, json.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			emit(with(members, name, b))
		}
		set(with(inner, "extra", jsontext.Value(`"1"`)))
		for _, key := range slices.Sorted(maps.Keys(inner)) {
			set(with(inner, key, nil))
			for _, v := range values {
				set(with(inner, key, jsontext.Value(v)))
			}
		}
	}
	emit(with(members, "extra", jsontext.Value(`"1"`)))
	emit(with(members, "LOG", members["log"]))
	text := string(data)
	for _, s := range []string{
		"", " " + text, text + " ", text + "x", text + text, "\x00" + text, text[:len(text)/2], text[:len(text)-1],
		strings.Replace(text, `"log":`, `"log":"login","log":`, 1), strings.Replace(text, `"n":3`, `"n":3,"n":4`, 1),
		strings.Replace(text, `"login":"yamada"`, "\"login\":\"a\xffb\"", 1), strings.Replace(text, `"n":`, `"n":`, 1),
		strings.Replace(text, `"at":"2026-11-12T01:02:03.456789Z"`, `"at":"2026-11-12T01:02:03.456789Z","at":"2026-11-12T01:02:03Z"`, 1),
		"[" + text + "]", "null", "{}", "[]",
	} {
		out = append(out, []byte(s))
	}
	return out
}

// checkSameRecord checks that ReadRecordID reads data if and only if audit.Decode does, and reads the same.
func checkSameRecord(t testing.TB, data []byte) {
	t.Helper()
	log, n, prev, ok := dbwire.ReadRecordID(data)
	r, err := audit.Decode(data)
	switch {
	case ok != (err == nil):
		t.Fatalf("dbwire.ReadRecordID of %q reads it: %v, and audit.Decode = %v", data, ok, err)
	case ok && (log != string(r.Log) || n != r.N || prev != r.Prev):
		t.Fatalf("dbwire.ReadRecordID of %q = %q, %d, %q; audit.Decode has %q, %d, %q", data, log, n, prev, r.Log, r.N, r.Prev)
	}
}

// TestAuditReadRecordIDDifferential checks, over the records that the store writes and the many records that they become
// when a member is taken away, changed, added or doubled, that dbwire.ReadRecordID reads what audit.Decode reads and nothing
// else.
func TestAuditReadRecordIDDifferential(t *testing.T) {
	s, _, _ := auditFixture(t)
	var corpus [][]byte
	for _, e := range rawEvents(t, s) {
		corpus = append(corpus, e.Data)
	}
	corpus = append(corpus, staticRecords()...)
	full, err := audit.Decode([]byte(fullRecord))
	if err != nil {
		t.Fatalf("audit.Decode of the record that has every member = %v", err)
	}
	if zero := zeroMembers(reflect.ValueOf(full), ""); len(zero) != 0 {
		t.Fatalf("the record that has every member lacks %v", zero)
	}
	readable, total := 0, 0
	for _, data := range corpus {
		checkSameRecord(t, data)
		for _, m := range mutations(t, data) {
			checkSameRecord(t, m)
			if _, _, _, ok := dbwire.ReadRecordID(m); ok {
				readable++
			}
			total++
		}
	}
	// Some of the changes leave a record, and most make none: the test would not find a difference if all did the same.
	if total < 1000 || readable < 100 || readable > total/2 {
		t.Errorf("%d of the %d records that were made are read: the corpus does not tell the two readers apart", readable, total)
	}
}

// FuzzReadRecordIDAgrees is the differential test over inputs that a fuzz finds. The static records, and the records that they
// become, are its seeds, which go in the tests of CI.
func FuzzReadRecordIDAgrees(f *testing.F) {
	for _, data := range staticRecords() {
		f.Add(data)
		for _, m := range mutations(f, data) {
			f.Add(m)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkSameRecord(t, data) })
}

// checkSameHeadLine checks that dbwire.ParseHeadLine reads a line as audit.ParseHead does, or does not read it: a line that
// ParseHeadLine reads is exactly the line that audit.Head.String writes, and audit.ParseHead reads the same head from it.
func checkSameHeadLine(t testing.TB, line string) {
	t.Helper()
	log, n, hash, ok := dbwire.ParseHeadLine([]byte(line))
	head, err := audit.ParseHead(line)
	if !ok {
		return
	}
	want := audit.Head{Log: audit.Log(log), N: n, Hash: hash}
	if err != nil || head != want || want.String() != line {
		t.Errorf("ParseHeadLine(%q) = %q, %d, %x; audit.ParseHead = %+v, %v; String = %q", line, log, n, hash, head, err,
			want.String())
	}
}

// TestParseHeadLineAgrees checks that dbwire.ParseHeadLine, which the daemon reads the rows of the table heads with, reads
// the lines that audit.Head.String writes, and no other: a line that audit.ParseHead refuses, or reads from a part of it
// (what a logger put before it, more space, upper case digits), is refused too.
func TestParseHeadLineAgrees(t *testing.T) {
	var hash [32]byte
	for i := range hash {
		hash[i] = byte(i*37 + 11)
	}
	for _, log := range []audit.Log{audit.LoginLog, audit.ActivityLog} {
		for _, n := range []uint64{1, 2, 9, 10, 255, 1 << 32, 1<<64 - 1} {
			line := audit.Head{Log: log, N: n, Hash: hash}.String()
			got, number, sum, ok := dbwire.ParseHeadLine([]byte(line))
			if !ok || got != string(log) || number != n || sum != hash {
				t.Errorf("ParseHeadLine(%q) = %q, %d, %x, %v", line, got, number, sum, ok)
			}
			checkSameHeadLine(t, line)
		}
	}
	valid := audit.Head{Log: audit.LoginLog, N: 12, Hash: hash}.String()
	digits := valid[len(valid)-64:]
	for _, line := range []string{
		"", " ", valid + "\n", valid + " ", " " + valid, "journal: " + valid, "x" + valid, strings.ToUpper(valid),
		strings.Replace(valid, digits, strings.ToUpper(digits), 1), strings.Replace(valid, "login", "other", 1),
		strings.Replace(valid, "login 12", "login 012", 1), strings.Replace(valid, "login 12", "login 0", 1),
		strings.Replace(valid, "login 12", "login -12", 1), strings.Replace(valid, "login 12", "login +12", 1),
		strings.Replace(valid, "login 12", "login 18446744073709551616", 1), strings.Replace(valid, "login ", "login  ", 1),
		strings.Replace(valid, "login ", "login\t", 1), strings.Replace(valid, "sha256:", "sha512:", 1),
		strings.Replace(valid, "sha256:", "", 1), valid[:len(valid)-1], valid + "0", valid + " x", "audit head login 12",
		"audit head login sha256:" + digits, strings.Replace(valid, "audit head", "audit  head", 1),
		strings.Replace(valid, digits[:1], "g", 1),
	} {
		if _, _, _, ok := dbwire.ParseHeadLine([]byte(line)); ok {
			t.Errorf("ParseHeadLine(%q) reads the line", line)
		}
		checkSameHeadLine(t, line)
	}
}
