package html

// tokenType is the kind of a token of the tokenizer (13.2.5).
type tokenType uint8

const (
	doctypeToken tokenType = iota + 1
	startTagToken
	endTagToken
	commentToken
	characterToken
	processingInstructionToken
	eofToken
)

// token is a token of the tokenizer. name is the tag name, the DOCTYPE name or the processing instruction target; data
// is the characters of a character token (one or more), the comment data or the processing instruction data.
type token struct {
	typ         tokenType
	name        string
	data        string
	attrs       []Attr // of a tag, in source order, without the attributes whose name repeats an earlier one
	selfClosing bool

	// Of a DOCTYPE. An empty name is a missing one, since a name that is set is never empty; the identifiers can be
	// empty, so hasPublicID and hasSystemID tell them from missing ones.
	publicID, systemID       string
	hasPublicID, hasSystemID bool
	forceQuirks              bool
}
