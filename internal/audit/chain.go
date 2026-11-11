package audit

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrBroken is wrapped by what the store finds wrong in a log when it verifies it.
var ErrBroken = errors.New("the audit log is broken")

// Head is the head of the chain of a log: the number of its last record, and the SHA-256 of the bytes that the store
// keeps of it. The prev of the next record is that hash.
type Head struct {
	Log  Log
	N    uint64
	Hash [32]byte
}

const headPrefix = "audit head "

// String returns the line that tells the head, which holds no personal information.
func (h Head) String() string {
	return fmt.Sprintf("%s%s %d sha256:%x", headPrefix, h.Log, h.N, h.Hash)
}

// ParseHead reads the head from line, from the "audit head " in it: what a logger such as journald has put before it
// is skipped.
func ParseHead(line string) (Head, error) {
	i := strings.LastIndex(line, headPrefix)
	if i < 0 {
		return Head{}, errors.New(`not a head of an audit log: no "audit head "`)
	}
	fields := strings.Fields(line[i+len(headPrefix):])
	if len(fields) != 3 {
		return Head{}, errors.New("not a head of an audit log: use the log, the number and the hash")
	}
	h := Head{Log: Log(fields[0])}
	if h.Log != LoginLog && h.Log != ActivityLog {
		return Head{}, errors.New("not a head of an audit log: unknown log")
	}
	n, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || n < 1 || strconv.FormatUint(n, 10) != fields[1] {
		return Head{}, errors.New("not a head of an audit log: invalid number")
	}
	h.N = n
	digits, ok := strings.CutPrefix(fields[2], "sha256:")
	if !ok || !isHex(digits, 64) {
		return Head{}, errors.New("not a head of an audit log: invalid hash")
	}
	if _, err := hex.Decode(h.Hash[:], []byte(digits)); err != nil {
		return Head{}, errors.New("not a head of an audit log: invalid hash")
	}
	return h, nil
}
