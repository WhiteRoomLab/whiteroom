package dbwire

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// hexBytes returns the bytes of the hexadecimal text s, in which the spaces and the bars are for the reader.
func hexBytes(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.NewReplacer(" ", "", "|", "").Replace(s))
	if err != nil {
		t.Fatalf("the bytes %q: %v", s, err)
	}
	return b
}

// TestGolden pins the bytes of protocol version 2: every request and every reply, as the whole message with its frame, from
// fixed inputs, written here as hexadecimal text and not made by the code that is tested, and read back from those bytes.
// A daemon and a client of another build speak these bytes, so they do not change once this version is released: a change
// that makes this test fail is a change of the protocol, which is a new Version.
func TestGolden(t *testing.T) {
	mark := time.Unix(0x0102030405, 0x0a0b0c0d).UTC()
	const markHex = "00 01 0000000102030405 0a0b0c0d"
	// hash(b) is b, b+1, ... b+31.
	const (
		hash1 = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
		hash2 = "02030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f2021"
		hash3 = "030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122"
		hash4 = "0405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20212223"
	)
	backupFile := func(e *Enc) {
		e.BackupEntry(BackupEntry{Kind: EntryFile, Path: "files/a"})
		c := e.Chunks()
		_, _ = c.Write([]byte("xyz"))
		_ = c.Close()
	}

	for _, c := range []struct {
		name  string
		codec string // the codec that reads the body
		write func(e *Enc)
		want  any    // what the codec reads
		wire  string // the header of the message, a bar, and its body
	}{
		{"Hello request", "Hello request", func(e *Enc) { e.HelloRequest() }, Version,
			"01 01 0000 00000008 | 57524442 00000002"},
		{"Hello reply of the writer", "Hello reply", func(e *Enc) {
			e.HelloReply(HelloReply{Version: Version, Role: RoleWriter, BootID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
				Seq: 0x1122334455667788, Stopped: true, Frame: 0x10000, Commit: 0x100000, Object: 0x200000, Staged: 0x300000, MaxKey: 0x1000,
				DataDir: DirID{Dev: 0x0103, Ino: 0x0a0b0c0d0e0f1011}})
		}, HelloReply{Version: Version, Role: RoleWriter, BootID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
			Seq: 0x1122334455667788, Stopped: true, Frame: 0x10000, Commit: 0x100000, Object: 0x200000, Staged: 0x300000, MaxKey: 0x1000,
			DataDir: DirID{Dev: 0x0103, Ino: 0x0a0b0c0d0e0f1011}},
			"80 01 0000 0000004e | 00000002 02 0102030405060708090a0b0c0d0e0f10 1122334455667788 01 00010000 " +
				"0000000000100000 0000000000200000 0000000000300000 00001000 0000000000000103 0a0b0c0d0e0f1011"},
		{"Hello reply of a reader", "Hello reply", func(e *Enc) {
			e.HelloReply(HelloReply{Version: Version, Role: RoleReader, BootID: [16]byte{0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa},
				Frame: 0x1000, Commit: 1, Object: 2, Staged: 3, MaxKey: 0x1000})
		}, HelloReply{Version: Version, Role: RoleReader, BootID: [16]byte{0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa},
			Frame: 0x1000, Commit: 1, Object: 2, Staged: 3, MaxKey: 0x1000},
			"80 01 0000 0000004e | 00000002 01 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 0000000000000000 00 00001000 " +
				"0000000000000001 0000000000000002 0000000000000003 00001000 0000000000000000 0000000000000000"},
		{"Snapshot request", "Snapshot request", func(*Enc) {}, nil, "02 01 0000 00000000 |"},
		{"Snapshot reply", "Snapshot reply", func(e *Enc) { e.SnapshotReply(0x0102030405060708) }, uint64(0x0102030405060708),
			"80 01 0000 00000008 | 0102030405060708"},
		{"Release request", "Release request", func(*Enc) {}, nil, "03 01 0000 00000000 |"},
		{"Release reply", "Release reply", func(*Enc) {}, nil, "80 01 0000 00000000 |"},
		{"Get request", "Get request", func(e *Enc) { e.GetRequest(2, []byte("key")) }, []any{uint32(2), []byte("key")},
			"04 01 0000 0000000b | 00000002 00000003 6b6579"},
		{"Get reply that finds", "Get reply", func(e *Enc) { e.GetReply(true, []byte("value")) }, []any{true, []byte("value")},
			"80 01 0000 0000000a | 01 00000005 76616c7565"},
		{"Get reply that does not", "Get reply", func(e *Enc) { e.GetReply(false, nil) }, []any{false, []byte(nil)},
			"80 01 0000 00000001 | 00"},
		{"Scan request after a key", "Scan request", func(e *Enc) {
			e.ScanRequest(ScanRequest{Table: 2, Prefix: []byte("p"), HasAfter: true, After: []byte("a"), Limit: 10})
		}, ScanRequest{Table: 2, Prefix: []byte("p"), HasAfter: true, After: []byte("a"), Limit: 10},
			"05 01 0000 00000013 | 00000002 00000001 70 01 00000001 61 0000000a"},
		{"Scan request from the start", "Scan request", func(e *Enc) { e.ScanRequest(ScanRequest{Table: 3}) },
			ScanRequest{Table: 3, Prefix: []byte{}, After: []byte{}},
			"05 01 0000 00000011 | 00000003 00000000 00 00000000 00000000"},
		{"Scan reply", "Scan reply", func(e *Enc) {
			e.ScanRow([]byte("k1"), []byte("value1"))
			e.ScanRow([]byte("k2"), []byte("v2"))
			e.ScanEnd(true)
		}, []any{[][2][]byte{{[]byte("k1"), []byte("value1")}, {[]byte("k2"), []byte("v2")}}, true},
			"80 01 0000 00000020 | 01 00000002 6b31 00000006 76616c756531 | 01 00000002 6b32 00000002 7632 | 00 01"},
		{"Object request", "Object request", func(e *Enc) { e.ObjectRequest(hash(1)) }, hash(1), "06 01 0000 00000020 | " + hash1},
		{"Object reply that finds", "Object reply", func(e *Enc) { e.ObjectReply(true, []byte("data")) }, []any{true, []byte("data")},
			"80 01 0000 0000000d | 01 0000000000000004 64617461"},
		{"Object reply that does not", "Object reply", func(e *Enc) { e.ObjectReply(false, nil) }, []any{false, []byte(nil)},
			"80 01 0000 00000001 | 00"},
		{"Original request", "Original request", func(e *Enc) { e.OriginalRequest(hash(2)) }, hash(2), "07 01 0000 00000020 | " + hash2},
		{"Original reply", "Original reply", func(e *Enc) { e.OriginalReply(3); e.Raw([]byte("abc")) }, []any{int64(3), []byte("abc")},
			"80 01 0000 0000000b | 0000000000000003 616263"},
		{"Events request", "Events request", func(*Enc) {}, nil, "08 01 0000 00000000 |"},
		{"Events reply", "Events reply", func(e *Enc) {
			e.Event(Event{Number: 5, Seq: 3, Data: []byte("ab")})
			e.Event(Event{Number: 6, Seq: 3, Data: AppendMark(nil, mark)})
			e.EventsEnd()
		}, []Event{{Number: 5, Seq: 3, Data: []byte("ab")}, {Number: 6, Seq: 3, Data: AppendMark(nil, mark)}},
			"80 01 0000 0000003b | 01 0000000000000005 0000000000000003 00000002 6162 | " +
				"01 0000000000000006 0000000000000003 0000000e " + markHex + " | 00"},
		{"Verify request", "Verify request", func(*Enc) {}, nil, "09 01 0000 00000000 |"},
		{"Verify reply", "Verify reply", func(*Enc) {}, nil, "80 01 0000 00000000 |"},
		{"Backup request", "Backup request", func(*Enc) {}, nil, "0a 01 0000 00000000 |"},
		{"Backup reply", "Backup reply", func(e *Enc) {
			e.BackupEntry(BackupEntry{Kind: EntryPosition, Position: BackupPosition{Generation: 1, Segment: 2, End: 3, Seq: 4, CRC: 0x05060708}})
			e.BackupEntry(BackupEntry{Kind: EntryHead, Line: "line 7"})
			e.BackupEntry(BackupEntry{Kind: EntryDir, Path: "files"})
			backupFile(e)
			e.BackupEntry(BackupEntry{Kind: EntryEnd})
		}, []any{
			BackupEntry{Kind: EntryPosition, Position: BackupPosition{Generation: 1, Segment: 2, End: 3, Seq: 4, CRC: 0x05060708}},
			BackupEntry{Kind: EntryHead, Line: "line 7"},
			BackupEntry{Kind: EntryDir, Path: "files"},
			BackupEntry{Kind: EntryFile, Path: "files/a"}, []byte("xyz"),
		},
			"80 01 0000 0000004c | " +
				"04 0000000000000001 0000000000000002 0000000000000003 0000000000000004 05060708 | " +
				"03 0006 6c696e652037 | 01 0005 66696c6573 | " +
				"02 0007 66696c65732f61 00000003 78797a 00000000 | 00"},
		{"PutObject request", "PutObject request", func(e *Enc) { e.PutObjectRequest(hash(3), []byte("data")) },
			[]any{hash(3), []byte("data")}, "0b 01 0000 0000002c | " + hash3 + " 0000000000000004 64617461"},
		{"PutObject reply", "PutObject reply", func(*Enc) {}, nil, "80 01 0000 00000000 |"},
		{"PutOriginal request", "PutOriginal request", func(e *Enc) {
			c := e.Chunks()
			_, _ = c.Write([]byte("ab"))
			_ = c.Close()
			e.PutOriginalTrailer(hash(4), 2)
		}, []any{[]byte("ab"), hash(4), int64(2)},
			"0c 01 0000 00000032 | 00000002 6162 00000000 | " + hash4 + " 0000000000000002"},
		{"PutOriginal reply", "PutOriginal reply", func(*Enc) {}, nil, "80 01 0000 00000000 |"},
		{"Commit request", "Commit request", func(e *Enc) {
			e.CommitRequest(7)
			e.CommitOp(Op{Kind: OpPut, Table: 2, Key: []byte("k"), Value: []byte("v")})
			e.CommitOp(Op{Kind: OpDelete, Table: 3, Key: []byte("d")})
			e.CommitOp(Op{Kind: OpEvent, Value: []byte("ev")})
			e.CommitOp(Op{Kind: OpPut, Table: TableHeads, Key: []byte("activity"), Value: []byte("line 7")})
			e.CommitEnd()
		}, []any{uint64(7), []Op{
			{Kind: OpPut, Table: 2, Key: []byte("k"), Value: []byte("v")},
			{Kind: OpDelete, Table: 3, Key: []byte("d")},
			{Kind: OpEvent, Value: []byte("ev")},
			{Kind: OpPut, Table: TableHeads, Key: []byte("activity"), Value: []byte("line 7")},
		}},
			"0d 01 0000 00000044 | 0000000000000007 | " +
				"01 00000002 00000001 6b 00000001 76 | 02 00000003 00000001 64 | 03 00000002 6576 | " +
				"01 0000000b 00000008 6163746976697479 00000006 6c696e652037 | 00"},
		{"Commit reply", "Commit reply", func(e *Enc) { e.CommitReply(8, mark) }, []any{uint64(8), mark},
			"80 01 0000 00000014 | 0000000000000008 0000000102030405 0a0b0c0d"},
	} {
		t.Run(c.name, func(t *testing.T) {
			wire := hexBytes(t, c.wire)
			cd := codecNamed(t, c.codec)
			if got := message(t, 1<<20, cond(cd.reply, KindOK, cd.kind), encodeBody(t, c.write)); !bytes.Equal(got, wire) {
				t.Errorf("the message is\n %x\nwant\n %x", got, wire)
			}
			r := NewReader(bytes.NewReader(wire), 1<<20)
			if cd.reply {
				if err := r.Reply(); err != nil {
					t.Fatal(err)
				}
			} else if kind, err := r.Next(); err != nil || kind != cd.kind {
				t.Fatalf("Next = %v, %v, want %v", kind, err, cd.kind)
			}
			v, _, err := cd.dec(&Dec{R: r})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(v, c.want) {
				t.Errorf("read %#v, want %#v", v, c.want)
			}
		})
	}
}

// TestGoldenError pins the Error frame with each code, the text and none, and the mark of a commit.
func TestGoldenError(t *testing.T) {
	for _, c := range []struct {
		code Code
		wire string
	}{
		{CodeProtocol, "81 01 0000 00000006 | 0001 0002 6e6f"},
		{CodeVersion, "81 01 0000 00000006 | 0002 0002 6e6f"},
		{CodeDenied, "81 01 0000 00000006 | 0003 0002 6e6f"},
		{CodeBusy, "81 01 0000 00000006 | 0004 0002 6e6f"},
		{CodeNoSnapshot, "81 01 0000 00000006 | 0005 0002 6e6f"},
		{CodeNotFound, "81 01 0000 00000006 | 0006 0002 6e6f"},
		{CodeSeq, "81 01 0000 00000006 | 0007 0002 6e6f"},
		{CodeRule, "81 01 0000 00000006 | 0008 0002 6e6f"},
		{CodeLimit, "81 01 0000 00000006 | 0009 0002 6e6f"},
		{CodeNoSpace, "81 01 0000 00000006 | 000a 0002 6e6f"},
		{CodeStopped, "81 01 0000 00000006 | 000b 0002 6e6f"},
		{CodeHash, "81 01 0000 00000006 | 000c 0002 6e6f"},
		{CodeFailed, "81 01 0000 00000006 | 000d 0002 6e6f"},
		{CodeInternal, "81 01 0000 00000006 | 000e 0002 6e6f"},
	} {
		t.Run(c.code.String(), func(t *testing.T) {
			wire := hexBytes(t, c.wire)
			var buf bytes.Buffer
			if err := NewWriter(&buf, 64).Fail(c.code, "no"); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(buf.Bytes(), wire) {
				t.Errorf("the frame is\n %x\nwant\n %x", buf.Bytes(), wire)
			}
			var re *RemoteError
			if err := NewReader(bytes.NewReader(wire), 64).Reply(); !errors.As(err, &re) || re.Code != c.code || re.Msg != "no" {
				t.Errorf("read %v, want the code %v and the text %q", err, c.code, "no")
			}
		})
	}

	t.Run("no text", func(t *testing.T) {
		wire := hexBytes(t, "81 01 0000 00000004 | 0001 0000")
		var buf bytes.Buffer
		if err := NewWriter(&buf, 64).Fail(CodeProtocol, ""); err != nil || !bytes.Equal(buf.Bytes(), wire) {
			t.Errorf("the frame is %x (%v), want %x", buf.Bytes(), err, wire)
		}
	})

	t.Run("the mark", func(t *testing.T) {
		mark := time.Unix(0x0102030405, 0x0a0b0c0d).UTC()
		want := hexBytes(t, "00 01 0000000102030405 0a0b0c0d")
		if got := AppendMark(nil, mark); !bytes.Equal(got, want) {
			t.Errorf("the mark is %x, want %x", got, want)
		}
		if got, ok := ParseMark(want); !ok || !got.Equal(mark) {
			t.Errorf("ParseMark = %v, %v, want %v", got, ok, mark)
		}
	})
}
