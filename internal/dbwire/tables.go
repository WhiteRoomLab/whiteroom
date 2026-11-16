package dbwire

// TableVersion is the table schema that this protocol version works with.
const TableVersion = 7

// The tables of internal/store that the daemon acts on without reading rows.
const (
	TableMeta           uint32 = 1
	TableHistory        uint32 = 3
	TableRevisions      uint32 = 4
	TableEntryRevisions uint32 = 5

	// TableHeads has the name of an audit log as the key and the line of its head as the value, which the daemon
	// writes out as it is.
	TableHeads uint32 = 11
)

// MetaSchema is the key of the schema in TableMeta; its value is a uvarint.
const MetaSchema = "schema"

// The bounds of a row of TableHeads, in bytes.
const (
	MaxHeadKey  = 64
	MaxHeadLine = 255
)

// AppendOnly reports whether the daemon refuses to change or delete a row of table.
func AppendOnly(table uint32) bool {
	return table == TableHistory || table == TableRevisions || table == TableEntryRevisions
}

// ValidHeadRow reports whether key and value may be a row of TableHeads.
func ValidHeadRow(key, value []byte) bool {
	if len(key) < 1 || len(key) > MaxHeadKey {
		return false
	}
	for _, c := range key {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return validHeadLine(value)
}

func validHeadLine(line []byte) bool {
	if len(line) < 1 || len(line) > MaxHeadLine {
		return false
	}
	for _, c := range line {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}
