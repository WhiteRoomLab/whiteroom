package dbwire

import (
	"io"
	"io/fs"
	"math"
	"strings"
	"time"
)

// ReadEmpty reads the body of a message that has none: the requests Snapshot, Release, Events, Verify and Backup, and
// the replies to Release, Verify, PutObject and PutOriginal. There is nothing to write for them.
func ReadEmpty(d *Dec) error { return d.End() }

// tag reads the byte before an element of a list: true for an element, false for the end of the list or an error.
func (d *Dec) tag() bool {
	switch v := d.U8(); {
	case d.Err != nil:
	case v == 0:
		return false
	case v == 1:
		return true
	default:
		d.fail(protocolf("a list tag of %d", v))
	}
	return false
}

// sized reads a size and that many bytes, at most max.
func (d *Dec) sized(what string, max int64) []byte {
	n := d.size()
	if d.Err != nil {
		return nil
	}
	if n > max {
		d.fail(limitf("%s of %d bytes, over the limit of %d", what, n, max))
		return nil
	}
	return d.fill(n)
}

// Hello

// HelloRequest writes the body of a Hello: Magic and Version.
func (e *Enc) HelloRequest() {
	e.Raw(Magic[:])
	e.U32(Version)
}

// ReadHelloRequest reads the magic and the version of a Hello, and no more: a Hello of another version may have another
// body, so the caller compares the version, and calls End only if it is its own.
func ReadHelloRequest(d *Dec) (version uint32, err error) {
	var m [4]byte
	d.Raw(m[:])
	version = d.U32()
	if d.Err == nil && m != Magic {
		d.fail(protocolf("a Hello without the magic"))
	}
	return version, d.Err
}

// HelloReply is the reply to a Hello: the version of the protocol, the role of the connection, the ID of the start of
// the daemon (random at each start), the last commit, whether the daemon has stopped writing, the limits that the
// client keeps to, and the identity of the data directory.
type HelloReply struct {
	Version uint32
	Role    Role
	BootID  [16]byte
	Seq     uint64
	Stopped bool
	Frame   int
	Commit  int64
	Object  int64
	Staged  int64
	MaxKey  int
	DataDir DirID
}

// DirID is the identity of a directory on its system: its device and inode numbers, as stat(2) tells them. The zero DirID
// is unknown.
type DirID struct{ Dev, Ino uint64 }

func (e *Enc) HelloReply(h HelloReply) {
	if h.Frame < 0 || uint64(h.Frame) > math.MaxUint32 || h.MaxKey < 0 || uint64(h.MaxKey) > math.MaxUint32 {
		e.fail(protocolf("a limit of a Hello reply is out of range"))
		return
	}
	e.U32(h.Version)
	e.U8(uint8(h.Role))
	e.Raw(h.BootID[:])
	e.U64(h.Seq)
	e.Bool(h.Stopped)
	e.U32(uint32(h.Frame))
	e.size(h.Commit)
	e.size(h.Object)
	e.size(h.Staged)
	e.U32(uint32(h.MaxKey))
	e.U64(h.DataDir.Dev)
	e.U64(h.DataDir.Ino)
}

// ReadHelloReply reads a Hello reply of this version of the protocol: another version is refused at once, and so are a
// role other than reader and writer and a frame limit that no Writer can keep.
func ReadHelloReply(d *Dec) (HelloReply, error) {
	var h HelloReply
	h.Version = d.U32()
	if d.Err == nil && h.Version != Version {
		d.fail(protocolf("the daemon speaks version %d, this program %d", h.Version, Version))
	}
	h.Role = Role(d.U8())
	d.Raw(h.BootID[:])
	h.Seq = d.U64()
	h.Stopped = d.Bool()
	frame := d.U32()
	h.Commit = d.size()
	h.Object = d.size()
	h.Staged = d.size()
	maxKey := d.U32()
	h.DataDir = DirID{Dev: d.U64(), Ino: d.U64()}
	switch {
	case d.Err != nil:
	case h.Role != RoleReader && h.Role != RoleWriter:
		d.fail(protocolf("a Hello reply with the role %d", uint8(h.Role)))
	case frame < 1 || frame > MaxFrame:
		d.fail(protocolf("a Hello reply with the frame limit %d", frame))
	case maxKey > math.MaxInt32:
		d.fail(protocolf("a Hello reply with the key limit %d", maxKey))
	default:
		h.Frame, h.MaxKey = int(frame), int(maxKey)
	}
	return h, d.End()
}

// Snapshot

// SnapshotReply writes the reply to a Snapshot: the sequence number of the commit that it holds.
func (e *Enc) SnapshotReply(seq uint64) { e.U64(seq) }

func ReadSnapshotReply(d *Dec) (seq uint64, err error) {
	seq = d.U64()
	return seq, d.End()
}

// Get

func (e *Enc) GetRequest(table uint32, key []byte) {
	e.U32(table)
	e.Bytes(key)
}

func ReadGetRequest(d *Dec, maxKey int64) (table uint32, key []byte, err error) {
	table = d.U32()
	key = d.Bytes(maxKey)
	return table, key, d.End()
}

// GetReply writes the reply to a Get: whether the key is there and, if it is, its value.
func (e *Enc) GetReply(found bool, value []byte) {
	e.Bool(found)
	if found {
		e.Bytes(value)
	}
}

func ReadGetReply(d *Dec, maxValue int64) (found bool, value []byte, err error) {
	found = d.Bool()
	if found {
		value = d.Bytes(maxValue)
	}
	return found, value, d.End()
}

// Scan

// ScanRequest asks for the rows of Table with the key prefix Prefix, in the order of the keys, after the key After if
// HasAfter, at most Limit of them (0: no limit).
type ScanRequest struct {
	Table    uint32
	Prefix   []byte
	HasAfter bool
	After    []byte
	Limit    uint32
}

func (e *Enc) ScanRequest(r ScanRequest) {
	e.U32(r.Table)
	e.Bytes(r.Prefix)
	e.Bool(r.HasAfter)
	if r.HasAfter {
		e.Bytes(r.After)
	} else {
		e.Bytes(nil)
	}
	e.U32(r.Limit)
}

// ReadScanRequest refuses a key After that is not empty without HasAfter.
func ReadScanRequest(d *Dec, maxKey int64) (ScanRequest, error) {
	var r ScanRequest
	r.Table = d.U32()
	r.Prefix = d.Bytes(maxKey)
	r.HasAfter = d.Bool()
	r.After = d.Bytes(maxKey)
	r.Limit = d.U32()
	if d.Err == nil && !r.HasAfter && len(r.After) > 0 {
		d.fail(protocolf("a Scan with a key after it that it does not ask for"))
	}
	return r, d.End()
}

// ScanRow writes a row of the reply to a Scan, and ScanEnd the end of the rows and whether the page ended before the
// keys did.
func (e *Enc) ScanRow(key, value []byte) {
	e.U8(1)
	e.Bytes(key)
	e.Bytes(value)
}

func (e *Enc) ScanEnd(more bool) {
	e.U8(0)
	e.Bool(more)
}

// ReadScanRow reads the next row of a reply to a Scan. ok is false at the end of the rows, which ReadScanEnd then
// finishes, and when d.Err is set.
func ReadScanRow(d *Dec, maxKey, maxValue int64) (key, value []byte, ok bool) {
	if !d.tag() {
		return nil, nil, false
	}
	key = d.Bytes(maxKey)
	value = d.Bytes(maxValue)
	return key, value, d.Err == nil
}

func ReadScanEnd(d *Dec) (more bool, err error) {
	more = d.Bool()
	return more, d.End()
}

// Object and Original

func (e *Enc) ObjectRequest(hash [32]byte) { e.Hash(hash) }

func ReadObjectRequest(d *Dec) ([32]byte, error) {
	h := d.Hash()
	return h, d.End()
}

// ObjectReply writes the reply to an Object: whether it is there and, if it is, its size and bytes.
func (e *Enc) ObjectReply(found bool, data []byte) {
	e.Bool(found)
	if found {
		e.U64(uint64(len(data)))
		e.Raw(data)
	}
}

func ReadObjectReply(d *Dec, maxSize int64) (found bool, data []byte, err error) {
	found = d.Bool()
	if found {
		data = d.sized("an object", maxSize)
	}
	return found, data, d.End()
}

func (e *Enc) OriginalRequest(hash [32]byte) { e.Hash(hash) }

func ReadOriginalRequest(d *Dec) ([32]byte, error) {
	h := d.Hash()
	return h, d.End()
}

// OriginalReply writes the size of an original, and the caller writes its bytes after it.
func (e *Enc) OriginalReply(size int64) { e.size(size) }

// ReadOriginalReply reads the size of an original; its bytes follow, for Dec.Stream and then End.
func ReadOriginalReply(d *Dec, maxSize int64) (size int64, err error) {
	size = d.size()
	if d.Err == nil && size > maxSize {
		d.fail(limitf("an original of %d bytes, over the limit of %d", size, maxSize))
	}
	return size, d.Err
}

// PutObject and PutOriginal

// PutObjectRequest writes the hash, the size and the bytes of an object. The size is that of data.
func (e *Enc) PutObjectRequest(hash [32]byte, data []byte) {
	e.Hash(hash)
	e.U64(uint64(len(data)))
	e.Raw(data)
}

func ReadPutObjectRequest(d *Dec, maxSize int64) (hash [32]byte, data []byte, err error) {
	hash = d.Hash()
	data = d.sized("an object", maxSize)
	return hash, data, d.End()
}

// PutOriginalTrailer writes what follows the chunks of the bytes of an original: their SHA-256 and their number.
func (e *Enc) PutOriginalTrailer(hash [32]byte, size int64) {
	e.Hash(hash)
	e.size(size)
}

func ReadPutOriginalTrailer(d *Dec) (hash [32]byte, size int64, err error) {
	hash = d.Hash()
	size = d.size()
	return hash, size, d.End()
}

// Chunks

// chunkMax is the largest chunk that a ChunkWriter writes.
const chunkMax = 1 << 20

// ChunkWriter writes what is written to it as chunks: a uint32 length n > 0 and n bytes, each. Close writes the end
// of the chunks, a length of 0.
type ChunkWriter struct{ e *Enc }

func (e *Enc) Chunks() *ChunkWriter { return &ChunkWriter{e} }

func (c *ChunkWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		n := min(len(p), chunkMax)
		c.e.U32(uint32(n))
		c.e.Raw(p[:n])
		if c.e.Err != nil {
			break
		}
		p = p[n:]
	}
	return total - len(p), c.e.Err
}

func (c *ChunkWriter) Close() error {
	c.e.U32(0)
	return c.e.Err
}

// ChunkReader reads the bytes of chunks, until their end: it returns io.EOF there, and the error of the Dec if the body
// ends or breaks first. The chunks are not held in memory.
type ChunkReader struct {
	d     *Dec
	max   int64
	total int64
	left  int64 // the bytes of the current chunk that are not read
	done  bool
}

// NewChunkReader returns a reader of the chunks that come next in the body of d, at most max bytes of them.
func NewChunkReader(d *Dec, max int64) *ChunkReader { return &ChunkReader{d: d, max: max} }

// Size returns the number of bytes read so far.
func (c *ChunkReader) Size() int64 { return c.total }

func (c *ChunkReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for c.left == 0 {
		if c.done {
			return 0, io.EOF
		}
		n := c.d.U32()
		if c.d.Err != nil {
			return 0, c.d.Err
		}
		if n == 0 {
			c.done = true
			return 0, io.EOF
		}
		if int64(n) > c.max-c.total {
			c.d.fail(limitf("chunks over the limit of %d bytes", c.max))
			return 0, c.d.Err
		}
		c.left = int64(n)
	}
	n := int(min(int64(len(p)), c.left))
	if !c.d.readFull(p[:n]) {
		return 0, c.d.Err
	}
	c.left -= int64(n)
	c.total += int64(n)
	return n, nil
}

// Events

// Event is an event of the log, as the reply to an Events has it.
type Event struct {
	Number, Seq uint64
	Data        []byte
}

// Event writes an event of the reply to an Events, and EventsEnd the end of the events.
func (e *Enc) Event(ev Event) {
	e.U8(1)
	e.U64(ev.Number)
	e.U64(ev.Seq)
	e.Bytes(ev.Data)
}

func (e *Enc) EventsEnd() { e.U8(0) }

// ReadEvent reads the next event of a reply to an Events. ok is false at the end of the events, after which it has
// checked that the body ends, and when d.Err is set.
func ReadEvent(d *Dec, maxData int64) (ev Event, ok bool) {
	if !d.tag() {
		_ = d.End()
		return Event{}, false
	}
	ev.Number = d.U64()
	ev.Seq = d.U64()
	ev.Data = d.Bytes(maxData)
	return ev, d.Err == nil
}

// Commit

// OpKind is the kind of an op of a Commit.
type OpKind uint8

const (
	OpPut    OpKind = 1
	OpDelete OpKind = 2
	OpEvent  OpKind = 3
)

// Op is an op of a Commit: a put of Value at Key in Table, a delete of Key in Table, or an event, whose data are in
// Value.
type Op struct {
	Kind  OpKind
	Table uint32
	Key   []byte
	Value []byte
}

// Size returns the bytes of the op as it is sent, for the limit of a Commit.
func (o Op) Size() int64 {
	switch o.Kind {
	case OpPut:
		return int64(1 + 4 + 4 + len(o.Key) + 4 + len(o.Value))
	case OpDelete:
		return int64(1 + 4 + 4 + len(o.Key))
	case OpEvent:
		return int64(1 + 4 + len(o.Value))
	}
	return 0
}

// CommitRequest writes the first part of a Commit, the sequence number that the writer expects to be the last; then
// CommitOp writes each op, and CommitEnd the end.
func (e *Enc) CommitRequest(expected uint64) { e.U64(expected) }

func (e *Enc) CommitOp(o Op) {
	switch o.Kind {
	case OpPut:
		e.U8(uint8(OpPut))
		e.U32(o.Table)
		e.Bytes(o.Key)
		e.Bytes(o.Value)
	case OpDelete:
		e.U8(uint8(OpDelete))
		e.U32(o.Table)
		e.Bytes(o.Key)
	case OpEvent:
		e.U8(uint8(OpEvent))
		e.Bytes(o.Value)
	default:
		e.fail(protocolf("an op of the kind %d", uint8(o.Kind)))
	}
}

func (e *Enc) CommitEnd() { e.U8(0) }

// ReadCommitRequest reads the expected sequence number of a Commit, and no more: the ops follow, for ReadCommitOp.
func ReadCommitRequest(d *Dec) (expected uint64, err error) {
	expected = d.U64()
	return expected, d.Err
}

// ReadCommitOp reads the next op of a Commit, with at most maxKey bytes of key and maxValue of value or data. ok is
// false at the end of the ops, after which it has checked that the body ends, and when d.Err is set.
func ReadCommitOp(d *Dec, maxKey, maxValue int64) (o Op, ok bool) {
	kind := OpKind(d.U8())
	switch {
	case d.Err != nil:
		return Op{}, false
	case kind == 0:
		_ = d.End()
		return Op{}, false
	case kind == OpPut:
		o.Table = d.U32()
		o.Key = d.Bytes(maxKey)
		o.Value = d.Bytes(maxValue)
	case kind == OpDelete:
		o.Table = d.U32()
		o.Key = d.Bytes(maxKey)
	case kind == OpEvent:
		o.Value = d.Bytes(maxValue)
	default:
		d.fail(protocolf("an op of the kind %d", uint8(kind)))
	}
	o.Kind = kind
	return o, d.Err == nil
}

// CommitReply writes the reply to a Commit: the new last sequence number and the mark, the time of the daemon.
func (e *Enc) CommitReply(seq uint64, mark time.Time) {
	sec, nsec := markFields(mark)
	e.U64(seq)
	e.I64(sec)
	e.U32(nsec)
}

func ReadCommitReply(d *Dec) (seq uint64, mark time.Time, err error) {
	seq = d.U64()
	sec := d.I64()
	nsec := d.U32()
	if d.Err == nil {
		t, ok := markTime(sec, nsec)
		if !ok {
			d.fail(protocolf("a commit time that is out of range"))
		}
		mark = t
	}
	return seq, mark, d.End()
}

// Backup

// EntryKind is the kind of an entry of the reply to a Backup.
type EntryKind uint8

const (
	EntryEnd      EntryKind = 0
	EntryDir      EntryKind = 1
	EntryFile     EntryKind = 2
	EntryHead     EntryKind = 3
	EntryPosition EntryKind = 4
)

// BackupPosition is the commit that a Backup copies, as logdb.Position has it.
type BackupPosition struct {
	Generation, Segment uint64
	End                 int64
	Seq                 uint64
	CRC                 uint32
}

// BackupEntry is an entry of the reply to a Backup: a directory or a file at Path (relative, slash separated), a line
// of a head in Line, or the Position. The content of a file follows its entry as chunks, to be read before the next
// entry.
type BackupEntry struct {
	Kind     EntryKind
	Path     string
	Line     string
	Position BackupPosition
}

// maxBackupPath bounds the path of an entry, in bytes.
const maxBackupPath = 4096

// validBackupPath reports whether p is a relative path of slash separated names that no system takes for another
// place: no empty, "." or ".." name, no backslash, colon or NUL.
func validBackupPath(p string) bool {
	return p != "." && fs.ValidPath(p) && !strings.ContainsAny(p, "\\:\x00")
}

// BackupEntry writes an entry of the reply to a Backup. For an EntryFile, the caller writes its content as chunks
// next, with Chunks. An entry of the kind EntryEnd ends the reply.
func (e *Enc) BackupEntry(en BackupEntry) {
	switch en.Kind {
	case EntryEnd:
		e.U8(uint8(EntryEnd))
	case EntryDir, EntryFile:
		if !validBackupPath(en.Path) || len(en.Path) > maxBackupPath {
			e.fail(protocolf("a backup entry with a path that is not valid"))
			return
		}
		e.U8(uint8(en.Kind))
		e.str16(en.Path)
	case EntryHead:
		if !validHeadLine([]byte(en.Line)) {
			e.fail(protocolf("a backup entry with a head line that is not valid"))
			return
		}
		e.U8(uint8(EntryHead))
		e.str16(en.Line)
	case EntryPosition:
		p := en.Position
		e.U8(uint8(EntryPosition))
		e.U64(p.Generation)
		e.U64(p.Segment)
		e.size(p.End)
		e.U64(p.Seq)
		e.U32(p.CRC)
	default:
		e.fail(protocolf("a backup entry of the kind %d", uint8(en.Kind)))
	}
}

// ReadBackupEntry reads the next entry of a reply to a Backup. ok is false at the end of the entries, after which it
// has checked that the body ends, and when d.Err is set. The paths and the head lines that it returns are valid.
func ReadBackupEntry(d *Dec) (en BackupEntry, ok bool) {
	kind := EntryKind(d.U8())
	switch {
	case d.Err != nil:
		return BackupEntry{}, false
	case kind == EntryEnd:
		_ = d.End()
		return BackupEntry{}, false
	case kind == EntryDir || kind == EntryFile:
		en.Path = d.str16(maxBackupPath)
		if d.Err == nil && !validBackupPath(en.Path) {
			d.fail(protocolf("a backup entry with a path that is not valid"))
		}
	case kind == EntryHead:
		en.Line = d.str16(MaxHeadLine)
		if d.Err == nil && !validHeadLine([]byte(en.Line)) {
			d.fail(protocolf("a backup entry with a head line that is not valid"))
		}
	case kind == EntryPosition:
		en.Position.Generation = d.U64()
		en.Position.Segment = d.U64()
		en.Position.End = d.size()
		en.Position.Seq = d.U64()
		en.Position.CRC = d.U32()
	default:
		d.fail(protocolf("a backup entry of the kind %d", uint8(kind)))
	}
	en.Kind = kind
	return en, d.Err == nil
}
