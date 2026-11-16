package dbwire

import (
	"errors"
	"fmt"
)

// Version is the version of the protocol. A Hello of another version is refused.
const Version uint32 = 1

// Magic starts the body of a Hello request.
var Magic = [4]byte{'W', 'R', 'D', 'B'}

// The names of the two socket files in the directory of the sockets of a daemon.
const (
	WriterSocket = "writer.sock"
	ReaderSocket = "reader.sock"
)

// Role is what a connection may do: a reader reads, and the one writer also puts and commits.
type Role uint8

const (
	RoleReader Role = 1
	RoleWriter Role = 2
)

func (r Role) String() string {
	switch r {
	case RoleReader:
		return "reader"
	case RoleWriter:
		return "writer"
	}
	return fmt.Sprintf("role %d", uint8(r))
}

// Kind is the kind of a message: one of the requests, or the kind of a reply.
type Kind uint8

const (
	KindHello       Kind = 0x01
	KindSnapshot    Kind = 0x02
	KindRelease     Kind = 0x03
	KindGet         Kind = 0x04
	KindScan        Kind = 0x05
	KindObject      Kind = 0x06
	KindOriginal    Kind = 0x07
	KindEvents      Kind = 0x08
	KindVerify      Kind = 0x09
	KindBackup      Kind = 0x0A
	KindPutObject   Kind = 0x0B
	KindPutOriginal Kind = 0x0C
	KindCommit      Kind = 0x0D
	KindOK          Kind = 0x80
	KindError       Kind = 0x81
)

var kindNames = map[Kind]string{
	KindHello: "Hello", KindSnapshot: "Snapshot", KindRelease: "Release", KindGet: "Get", KindScan: "Scan",
	KindObject: "Object", KindOriginal: "Original", KindEvents: "Events", KindVerify: "Verify", KindBackup: "Backup",
	KindPutObject: "PutObject", KindPutOriginal: "PutOriginal", KindCommit: "Commit", KindOK: "OK", KindError: "Error",
}

func (k Kind) IsRequest() bool { return k >= KindHello && k <= KindCommit }

// WriterOnly reports whether only the writer may send a request of kind k.
func (k Kind) WriterOnly() bool {
	return k == KindPutObject || k == KindPutOriginal || k == KindCommit
}

func (k Kind) String() string {
	if s, ok := kindNames[k]; ok {
		return s
	}
	return fmt.Sprintf("kind 0x%02x", uint8(k))
}

func (k Kind) known() bool {
	_, ok := kindNames[k]
	return ok
}

// HeaderSize is the size of the header of a frame.
const HeaderSize = 8

// MaxFrame bounds the payload of a frame: a Writer and a Reader keep their limit between 1 and it.
const MaxFrame = 1 << 30

const flagEnd = 0x01

// ErrProtocol is wrapped by every refusal of a malformed frame or body.
var ErrProtocol = errors.New("malformed message")

// ErrLimit is wrapped, with ErrProtocol, by the refusal of a size over a limit that the reader was given.
var ErrLimit = errors.New("over a size limit")

type limitError struct{ err error }

func (e *limitError) Error() string        { return e.err.Error() }
func (e *limitError) Unwrap() error        { return e.err }
func (e *limitError) Is(target error) bool { return target == ErrLimit }

func protocolf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrProtocol}, args...)...)
}

func limitf(format string, args ...any) error {
	return &limitError{protocolf(format, args...)}
}
