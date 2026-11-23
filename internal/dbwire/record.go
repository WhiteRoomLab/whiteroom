package dbwire

import "encoding/json/v2"

// ReadRecordID returns the log, the number and the prev of the audit record that data is, which it reads from the
// members log, n and prev of the top-level object and nothing else: a record with another member is read too. Names
// are matched exactly, and an object with a name twice is not read, as audit.Decode has it. It is for the maintenance
// of the daemon, which does not check records: VerifyAudit does. ok is false unless data is a JSON object with a string
// log and a positive integer n; prev is "" if the object has none.
func ReadRecordID(data []byte) (log string, n uint64, prev string, ok bool) {
	var id struct {
		Log  *string `json:"log"`
		N    *uint64 `json:"n"`
		Prev *string `json:"prev"`
	}
	if err := json.Unmarshal(data, &id); err != nil || id.Log == nil || id.N == nil || *id.N == 0 {
		return "", 0, "", false
	}
	if id.Prev != nil {
		prev = *id.Prev
	}
	return *id.Log, *id.N, prev, true
}
