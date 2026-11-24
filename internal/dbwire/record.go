package dbwire

import (
	"encoding/json/v2"
	"time"
)

// auditRecord is the shape of audit.Record, which audit.Decode reads. A test of internal/store pins them together.
type auditRecord struct {
	Log       string            `json:"log"`
	N         uint64            `json:"n"`
	Prev      string            `json:"prev"`
	At        time.Time         `json:"at"`
	Actor     string            `json:"actor"`
	Action    string            `json:"action"`
	Result    string            `json:"result"`
	Target    auditTarget       `json:"target"`
	Account   string            `json:"account"`
	Session   string            `json:"session"`
	Client    auditClient       `json:"client"`
	Login     string            `json:"login"`
	Operation string            `json:"operation"`
	Changed   bool              `json:"changed"`
	Before    auditVersion      `json:"before"`
	After     auditVersion      `json:"after"`
	Revision  string            `json:"revision"`
	Status    string            `json:"status"`
	Detail    map[string]string `json:"detail"`
}

type auditTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type auditClient struct {
	Addr      string `json:"addr"`
	UserAgent string `json:"userAgent"`
}

type auditVersion struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

// RecordShape returns the zero value of the type that ReadRecordID reads, for the test that pins it to audit.Record.
func RecordShape() any { return auditRecord{} }

// ReadRecordID returns the log, the number and the prev of the audit record that data is, and ok is true if audit.Decode
// reads data: the same members and types, no member that the record has not, no name twice, and a log that is login or
// activity, a number from 1, a time and an action. It is for the maintenance of the daemon, which does not check the
// values or the chain: VerifyAudit does. prev is "" if the record has none.
func ReadRecordID(data []byte) (log string, n uint64, prev string, ok bool) {
	var r auditRecord
	if err := json.Unmarshal(data, &r, json.RejectUnknownMembers(true)); err != nil {
		return "", 0, "", false
	}
	if r.Log != LogLogin && r.Log != LogActivity || r.N < 1 || r.At.IsZero() || r.Action == "" {
		return "", 0, "", false
	}
	return r.Log, r.N, r.Prev, true
}
