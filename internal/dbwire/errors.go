package dbwire

import "fmt"

// Code is the code of an error reply.
type Code uint16

const (
	// CodeProtocol: a malformed frame or message, or one out of order. The daemon closes the connection after it.
	CodeProtocol Code = 1
	CodeVersion  Code = 2
	// CodeDenied: the role may not send the message, or the user of the peer is not the writer.
	CodeDenied Code = 3
	// CodeBusy: another writer is connected, or the readers are at their limit.
	CodeBusy       Code = 4
	CodeNoSnapshot Code = 5
	CodeNotFound   Code = 6
	// CodeSeq: the expected sequence number is not the current one.
	CodeSeq Code = 7
	// CodeRule: a rule of the daemon (an append-only table, no audit event, a reserved or empty event, a row of the
	// heads table, an empty commit).
	CodeRule    Code = 8
	CodeLimit   Code = 9
	CodeNoSpace Code = 10
	// CodeStopped: the daemon has stopped writing after a sync failed.
	CodeStopped Code = 11
	// CodeHash: the data do not have the hash or the size that was given.
	CodeHash Code = 12
	// CodeFailed: the engine failed or found damage.
	CodeFailed   Code = 13
	CodeInternal Code = 14
)

var codeNames = map[Code]string{
	CodeProtocol: "protocol", CodeVersion: "version", CodeDenied: "denied", CodeBusy: "busy",
	CodeNoSnapshot: "no snapshot", CodeNotFound: "not found", CodeSeq: "seq", CodeRule: "rule", CodeLimit: "limit",
	CodeNoSpace: "no space", CodeStopped: "stopped", CodeHash: "hash", CodeFailed: "failed", CodeInternal: "internal",
}

func (c Code) String() string {
	if s, ok := codeNames[c]; ok {
		return s
	}
	return fmt.Sprintf("code %d", uint16(c))
}

// MaxErrorText bounds the text of an error reply, in bytes.
const MaxErrorText = 1024

// RemoteError is an error reply of the peer.
type RemoteError struct {
	Code Code
	Msg  string
}

func (e *RemoteError) Error() string { return e.Msg }
