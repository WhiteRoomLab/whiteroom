package content

const (
	MaxDepth      = 32      // nesting depth of blocks; top-level blocks are at depth 1
	MaxBlocks     = 10000   // blocks in a document, including listItem, tableRow and tableCell
	MaxTableCells = 5000    // tableCell blocks in a table
	MaxTextBytes  = 1 << 20 // UTF-8 bytes in each string

	MaxMediaBytes    = 64 << 20    // bytes in an uploaded file, an image or a PDF
	MaxImagePixels   = 100_000_000 // width times height of an image
	MaxImageSide     = 10000       // width or height of an image size of a theme, in pixels
	MaxFileNameBytes = 255         // UTF-8 bytes in the name of an uploaded file, and in each segment of an output path

	MaxMenuDepth = 8    // nesting depth of menu items; top-level items are at depth 1
	MaxMenuItems = 1000 // items in a menu, at any depth

	MaxFormFields      = 100  // fields in a form
	MaxFormOptions     = 1000 // options of a field
	MaxNotifyAddresses = 10   // email addresses that a form notifies

	MaxPostsPerPage = 100 // entries on each page of a list, such as the latest posts
)
