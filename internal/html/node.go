package html

type NodeType uint8

const (
	DocumentNode NodeType = iota + 1
	DocumentFragmentNode
	ElementNode
	TextNode
	CommentNode
	DoctypeNode
	ProcessingInstructionNode
)

// Namespace is the namespace of an element.
type Namespace uint8

const (
	HTML Namespace = iota
	SVG
	MathML
)

// Attr is an attribute. Namespace is "", or "xlink", "xml" or "xmlns" for the attributes of foreign elements that the
// parser puts in those namespaces, such as xlink:href; Name is then the local name (href).
type Attr struct {
	Namespace string
	Name      string
	Value     string
}

// Node is a node of a tree. Name is the local name of an element, the name of a doctype or the target of a processing
// instruction; Data is the data of a text, comment or processing instruction node.
type Node struct {
	Type               NodeType
	Namespace          Namespace // of an element
	Name               string
	Data               string
	PublicID, SystemID string // of a doctype
	Attrs              []Attr
	Content            *Node // the template contents of an HTML template element: a DocumentFragmentNode

	Parent, FirstChild, LastChild, PrevSibling, NextSibling *Node
}

// AppendChild adds c as the last child of n. It panics if c already has a parent or siblings.
func (n *Node) AppendChild(c *Node) {
	n.InsertBefore(c, nil)
}

// InsertBefore inserts c as a child of n before ref, or as the last child if ref is nil. It panics if c already has a
// parent or siblings, or if ref is not a child of n.
func (n *Node) InsertBefore(c, ref *Node) {
	if c.Parent != nil || c.PrevSibling != nil || c.NextSibling != nil {
		panic("html: InsertBefore of a node that has a parent or siblings")
	}
	prev := n.LastChild
	if ref != nil {
		if ref.Parent != n {
			panic("html: InsertBefore before a node that is not a child")
		}
		prev = ref.PrevSibling
	}
	if prev == nil {
		n.FirstChild = c
	} else {
		prev.NextSibling = c
	}
	if ref == nil {
		n.LastChild = c
	} else {
		ref.PrevSibling = c
	}
	c.Parent, c.PrevSibling, c.NextSibling = n, prev, ref
}

// RemoveChild removes c, a child of n, from the tree. It panics if c is not a child of n.
func (n *Node) RemoveChild(c *Node) {
	if c.Parent != n {
		panic("html: RemoveChild of a node that is not a child")
	}
	if c.PrevSibling == nil {
		n.FirstChild = c.NextSibling
	} else {
		c.PrevSibling.NextSibling = c.NextSibling
	}
	if c.NextSibling == nil {
		n.LastChild = c.PrevSibling
	} else {
		c.NextSibling.PrevSibling = c.PrevSibling
	}
	c.Parent, c.PrevSibling, c.NextSibling = nil, nil, nil
}
