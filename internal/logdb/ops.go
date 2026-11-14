package logdb

import (
	"encoding/binary"
	"fmt"
)

// op is a change in a record. Its code says which fields it uses: put table, key and value; delete table and key;
// inline object hash and value; file object hash and size; event number, seq and value. valueOff is the offset of
// value in the record body, set by the encoders and by decodeOps.
type op struct {
	code     opCode
	table    uint32
	key      string
	value    []byte
	hash     [32]byte
	size     int64
	number   uint64
	seq      seqNum
	valueOff int
}

// baseEnd is the payload of the record that ends the base of a generation: how many base records came before it,
// and the counts of the state they hold.
type baseEnd struct {
	parts, keys, objects, events, nextEvent uint64
}

// record is a decoded record body.
type record struct {
	typ  recType
	seq  seqNum
	part uint64  // base
	ops  []op    // commit, base
	end  baseEnd // base-end
}

func appendBodyStart(dst []byte, typ recType, seq seqNum) []byte {
	return binary.LittleEndian.AppendUint64(append(dst, byte(typ)), uint64(seq))
}

func appendCommit(dst []byte, seq seqNum, ops []op) []byte {
	start := len(dst)
	return appendOps(appendBodyStart(dst, recCommit, seq), start, ops)
}

func appendBase(dst []byte, seq seqNum, part uint64, ops []op) []byte {
	start := len(dst)
	return appendOps(binary.AppendUvarint(appendBodyStart(dst, recBase, seq), part), start, ops)
}

func appendBaseEnd(dst []byte, seq seqNum, e baseEnd) []byte {
	dst = appendBodyStart(dst, recBaseEnd, seq)
	for _, v := range [...]uint64{e.parts, e.keys, e.objects, e.events, e.nextEvent} {
		dst = binary.AppendUvarint(dst, v)
	}
	return dst
}

// appendOps appends ops to a body that starts at dst[start], and sets the valueOff of the ops with a value. It panics
// on an op that decodeOps would refuse.
func appendOps(dst []byte, start int, ops []op) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(ops)))
	for i := range ops {
		o := &ops[i]
		o.valueOff = 0
		dst = append(dst, byte(o.code))
		switch o.code {
		case opPut, opDelete:
			if o.table == 0 || len(o.key) > MaxKey {
				panic(fmt.Sprintf("logdb: op %d with table %d and a key of %d bytes", o.code, o.table, len(o.key)))
			}
			dst = binary.AppendUvarint(dst, uint64(o.table))
			dst = binary.AppendUvarint(dst, uint64(len(o.key)))
			dst = append(dst, o.key...)
			if o.code == opPut {
				dst = appendValue(dst, start, o)
			}
		case opInline:
			dst = appendValue(append(dst, o.hash[:]...), start, o)
		case opFile:
			if o.size < 0 {
				panic(fmt.Sprintf("logdb: file object of %d bytes", o.size))
			}
			dst = binary.AppendUvarint(append(dst, o.hash[:]...), uint64(o.size))
		case opEvent:
			dst = binary.AppendUvarint(binary.AppendUvarint(dst, o.number), uint64(o.seq))
			dst = appendValue(dst, start, o)
		default:
			panic(fmt.Sprintf("logdb: unknown op %d", o.code))
		}
	}
	return dst
}

func appendValue(dst []byte, start int, o *op) []byte {
	dst = binary.AppendUvarint(append(dst, byte(envPlain)), uint64(len(o.value)))
	o.valueOff = len(dst) - start
	return append(dst, o.value...)
}

// uvarint is binary.Uvarint that also refuses an encoding longer than needed, so that every value has one encoding.
func uvarint(b []byte) (uint64, int) {
	v, n := binary.Uvarint(b)
	if n > 1 && b[n-1] == 0 {
		return 0, -n
	}
	return v, n
}

// decoder reads the fields of b in turn. After the first problem, whose reason it keeps, every read returns zero.
type decoder struct {
	b      []byte
	i      int
	short  string // the reason when b ends inside a field
	reason string
}

func (d *decoder) fail(reason string) {
	if d.reason == "" {
		d.reason = reason
	}
}

func (d *decoder) u8() byte {
	if d.reason != "" {
		return 0
	}
	if d.i == len(d.b) {
		d.fail(d.short)
		return 0
	}
	d.i++
	return d.b[d.i-1]
}

func (d *decoder) uvarint() uint64 {
	if d.reason != "" {
		return 0
	}
	v, n := uvarint(d.b[d.i:])
	switch {
	case n == 0:
		d.fail(d.short)
		return 0
	case n < 0:
		d.fail("bad uvarint")
		return 0
	}
	d.i += n
	return v
}

func (d *decoder) bytes(n uint64) []byte {
	if d.reason != "" {
		return nil
	}
	k, ok := toInt(n)
	if !ok || k > len(d.b)-d.i {
		d.fail(d.short)
		return nil
	}
	d.i += k
	return d.b[d.i-k : d.i]
}

func (d *decoder) table() uint32 {
	t := d.uvarint()
	id, ok := toUint32(t)
	if d.reason == "" && (!ok || id == 0) {
		d.fail(fmt.Sprintf("invalid table %d", t))
	}
	return id
}

func (d *decoder) key() string {
	n := d.uvarint()
	if d.reason == "" && n > MaxKey {
		d.fail(fmt.Sprintf("key too long: %d bytes (max %d)", n, MaxKey))
	}
	return string(d.bytes(n))
}

// value reads a value and returns it with its offset in the body, which starts base bytes before d.b.
func (d *decoder) value(base int) ([]byte, int) {
	if r := envelopeReason(envKind(d.u8())); r != "" {
		d.fail(r)
	}
	n := d.uvarint()
	off := base + d.i
	return d.bytes(n), off
}

// size reads the size of an object.
func (d *decoder) size() int64 {
	v := d.uvarint()
	size, ok := toInt64(v)
	if d.reason == "" && !ok {
		d.fail(fmt.Sprintf("bad object size %d", v))
	}
	return size
}

func (d *decoder) hash() (h [32]byte) {
	copy(h[:], d.bytes(32))
	return h
}

// decodeOps decodes the ops in p, which is the rest of a record body after its first base bytes, or says why it
// cannot. Keys are copied; values alias p.
func decodeOps(p []byte, base int) ([]op, string) {
	d := &decoder{b: p, short: "truncated op"}
	count := d.uvarint()
	ops := make([]op, 0, min(count, uint64(len(p))/3)) // an op has at least 3 bytes
	for range count {
		if d.reason != "" {
			break
		}
		o := op{code: opCode(d.u8())}
		switch o.code {
		case opPut, opDelete:
			o.table = d.table()
			o.key = d.key()
			if o.code == opPut {
				o.value, o.valueOff = d.value(base)
			}
		case opInline:
			o.hash = d.hash()
			o.value, o.valueOff = d.value(base)
		case opFile:
			o.hash = d.hash()
			o.size = d.size()
		case opEvent:
			o.number = d.uvarint()
			o.seq = seqNum(d.uvarint())
			o.value, o.valueOff = d.value(base)
		default:
			d.fail(fmt.Sprintf("unknown op %d", o.code))
		}
		ops = append(ops, o)
	}
	if d.reason == "" && d.i != len(p) {
		d.fail("trailing bytes")
	}
	if d.reason != "" {
		return nil, d.reason
	}
	return ops, ""
}

// parseBody decodes a record body, which aliases b as decodeOps says, or says why it cannot. The sequence of the
// records and the meaning of their ops are for the caller to check.
func parseBody(b []byte) (record, string) {
	if len(b) < minBody {
		return record{}, "truncated body"
	}
	r := record{typ: recType(b[0]), seq: seqNum(binary.LittleEndian.Uint64(b[1:]))}
	d := &decoder{b: b, i: minBody, short: "truncated body"}
	switch r.typ {
	case recCommit:
	case recBase:
		r.part = d.uvarint()
	case recBaseEnd:
		r.end.parts = d.uvarint()
		r.end.keys = d.uvarint()
		r.end.objects = d.uvarint()
		r.end.events = d.uvarint()
		r.end.nextEvent = d.uvarint()
		if d.reason == "" && d.i != len(b) {
			d.fail("trailing bytes")
		}
		if d.reason != "" {
			return record{}, d.reason
		}
		return r, ""
	default:
		return record{}, fmt.Sprintf("unknown record type %d", r.typ)
	}
	if d.reason != "" {
		return record{}, d.reason
	}
	ops, reason := decodeOps(b[d.i:], d.i)
	if reason != "" {
		return record{}, reason
	}
	r.ops = ops
	return r, ""
}
