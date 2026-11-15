package dbwire

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
	"unicode/utf8"
)

// checkErr fails unless err is what a bytes.Reader can give a Reader or a Dec: nothing, the end of the stream, a
// malformed message or the error reply of the peer.
func checkErr(t *testing.T, err error) {
	t.Helper()
	var re *RemoteError
	switch {
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, ErrProtocol), errors.As(err, &re):
	default:
		t.Fatalf("an error that is none of the expected ones: %T %v", err, err)
	}
}

func replyCodecs() []codec {
	var replies []codec
	for _, c := range codecs {
		if c.reply {
			replies = append(replies, c)
		}
	}
	return replies
}

// fuzzStream reads the frames of in, and the body of each message with the decoder of its kind (for a reply, the one
// that sel picks).
func fuzzStream(t *testing.T, sel int, in []byte) {
	replies := replyCodecs()
	requests := map[Kind]codec{}
	for _, c := range codecs {
		if !c.reply {
			requests[c.kind] = c
		}
	}

	r := NewReader(bytes.NewReader(in), 32)
	for range len(in)/HeaderSize + 2 {
		kind, err := r.Next()
		if err != nil {
			checkErr(t, err)
			break
		}
		c := requests[kind]
		if kind == KindOK {
			c = replies[sel%len(replies)]
		}
		if c.dec == nil { // an Error frame has no body
			continue
		}
		_, _, err = c.dec(&Dec{R: r})
		checkErr(t, err)
	}

	r = NewReader(bytes.NewReader(in), 32)
	for range len(in)/HeaderSize + 2 {
		err := r.Reply()
		if err != nil {
			checkErr(t, err)
			if !errors.As(err, new(*RemoteError)) {
				break
			}
		}
		_, err = io.Copy(io.Discard, r)
		checkErr(t, err)
		if err := r.Discard(); err != nil {
			checkErr(t, err)
		}
	}
}

// fuzzBody reads in as the body of c, and writes what it reads again.
func fuzzBody(t *testing.T, c codec, in []byte) {
	_, enc, err := c.dec(&Dec{R: bytes.NewReader(in)})
	checkErr(t, err)
	if err != nil || !c.exact {
		return
	}
	var buf bytes.Buffer
	e := &Enc{W: &buf}
	enc(e)
	if e.Err != nil || !bytes.Equal(buf.Bytes(), in) {
		t.Fatalf("%s: read %x without an error, and wrote %x, %v", c.name, in, buf.Bytes(), e.Err)
	}
}

// fuzzWriter writes in as a body through a Writer of a small limit, and reads it again. Fail gets in as the text.
func fuzzWriter(t *testing.T, sel int, in []byte) {
	limit := 1 + sel%40
	_, bodies, err := drain(message(t, limit, KindScan, in), limit)
	if err != nil || len(bodies) != 1 || !bytes.Equal(bodies[0], in) {
		t.Fatalf("a body of %d bytes with a limit of %d: %d bodies, %v", len(in), limit, len(bodies), err)
	}

	var buf bytes.Buffer
	if err := NewWriter(&buf, limit).Fail(Code(len(in)), string(in)); err != nil {
		t.Fatal(err)
	}
	var re *RemoteError
	if err := NewReader(&buf, limit).Reply(); !errors.As(err, &re) || re.Code != Code(len(in)) ||
		!utf8.ValidString(re.Msg) || len(re.Msg) > MaxErrorText || utf8.Valid(in) && len(in) <= MaxErrorText && re.Msg != string(in) {
		t.Fatalf("an error text of %d bytes: %v", len(in), err)
	}
}

func FuzzReader(f *testing.F) {
	replies := replyCodecs()
	seeds := []struct {
		codec string
		write func(e *Enc)
	}{
		{"Hello request", func(e *Enc) { e.HelloRequest() }},
		{"Hello reply", func(e *Enc) {
			e.HelloReply(HelloReply{Version: Version, Role: RoleWriter, Frame: 1 << 20, Commit: 1 << 20, Object: 1 << 20, Staged: 1 << 20, MaxKey: MaxKey})
		}},
		{"Snapshot reply", func(e *Enc) { e.SnapshotReply(7) }},
		{"Get request", func(e *Enc) { e.GetRequest(2, []byte("key")) }},
		{"Get reply", func(e *Enc) { e.GetReply(true, []byte("value")) }},
		{"Scan request", func(e *Enc) {
			e.ScanRequest(ScanRequest{Table: 2, Prefix: []byte("p"), HasAfter: true, After: []byte("a"), Limit: 5})
		}},
		{"Scan reply", func(e *Enc) { e.ScanRow([]byte("k"), []byte("v")); e.ScanRow(nil, nil); e.ScanEnd(true) }},
		{"Object request", func(e *Enc) { e.ObjectRequest(hash(1)) }},
		{"Object reply", func(e *Enc) { e.ObjectReply(true, []byte("object")) }},
		{"Original reply", func(e *Enc) { e.OriginalReply(3); e.Raw([]byte("abc")) }},
		{"Events reply", func(e *Enc) { e.Event(Event{Number: 1, Seq: 2, Data: AppendMark(nil, t2026)}); e.EventsEnd() }},
		{"Backup reply", func(e *Enc) {
			e.BackupEntry(BackupEntry{Kind: EntryPosition, Position: BackupPosition{Generation: 1, Segment: 1, End: 64, Seq: 1}})
			e.BackupEntry(BackupEntry{Kind: EntryHead, Line: "audit head login 1 sha256:" + string(bytes.Repeat([]byte("0"), 64))})
			e.BackupEntry(BackupEntry{Kind: EntryDir, Path: "files/ab"})
			e.BackupEntry(BackupEntry{Kind: EntryFile, Path: "files/ab/cd"})
			c := e.Chunks()
			_, _ = c.Write([]byte("content"))
			_ = c.Close()
			e.BackupEntry(BackupEntry{Kind: EntryEnd})
		}},
		{"PutObject request", func(e *Enc) { e.PutObjectRequest(hash(2), []byte("object")) }},
		{"PutOriginal request", func(e *Enc) {
			c := e.Chunks()
			_, _ = c.Write([]byte("original"))
			_ = c.Close()
			e.PutOriginalTrailer(hash(3), 8)
		}},
		{"Commit request", func(e *Enc) {
			e.CommitRequest(4)
			e.CommitOp(Op{Kind: OpPut, Table: 2, Key: []byte("k"), Value: []byte("v")})
			e.CommitOp(Op{Kind: OpDelete, Table: 2, Key: []byte("k")})
			e.CommitOp(Op{Kind: OpEvent, Value: []byte("{}")})
			e.CommitEnd()
		}},
		{"Commit reply", func(e *Enc) { e.CommitReply(5, t1969) }},
	}
	for _, s := range seeds {
		var c codec
		for _, cd := range codecs {
			if cd.name == s.codec {
				c = cd
			}
		}
		body := encodeBody(f, s.write)
		f.Add(append([]byte{byte(indexOf(codecs, c.name)<<1 | 1)}, body...)) // the body
		sel := 0
		for i, r := range replies {
			if r.kind == c.kind {
				sel = i
			}
		}
		wire := message(f, 16, cond(c.reply, KindOK, c.kind), body)
		f.Add(append([]byte{byte(sel << 1)}, wire...)) // the message
	}
	f.Add([]byte{0})
	f.Add(append([]byte{0}, errorFrame(3, "denied")...))
	f.Add(append([]byte{0}, rawFrame(byte(KindOK), 0, "ab")...))
	f.Add(append([]byte{0}, math.MaxUint8))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		mode, sel, in := data[0]&1, int(data[0]>>1), data[1:]
		if mode == 0 {
			fuzzStream(t, sel, in)
		} else {
			fuzzBody(t, codecs[sel%len(codecs)], in)
		}
		fuzzWriter(t, sel, in)
	})
}

func indexOf(cs []codec, name string) int {
	for i, c := range cs {
		if c.name == name {
			return i
		}
	}
	return 0
}
