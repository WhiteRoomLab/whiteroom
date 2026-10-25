package content

const (
	MaxDepth      = 32      // nesting depth of blocks; top-level blocks are at depth 1
	MaxBlocks     = 10000   // blocks in a document, including listItem, tableRow and tableCell
	MaxTableCells = 5000    // tableCell blocks in a table
	MaxTextBytes  = 1 << 20 // UTF-8 bytes in each string
)
