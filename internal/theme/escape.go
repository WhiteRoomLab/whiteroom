package theme

import (
	"errors"
	"fmt"
	"html/template"
	"slices"
	"sort"
	"strings"
	"text/template/parse"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// escape executes a clone of ns to find the escaping errors of html/template, which escapes templates when they first
// execute, and reports them. Since there is no data, other errors of execution are ignored, and the execution stops
// before any node of the templates runs.
func (c *templateChecker) escape(ns *namespace) {
	e := c.probe(ns, nil, -1, -1)
	if e == nil || e.ErrorCode == template.ErrNoSuchTemplate {
		// Load reports the template calls that do not resolve.
		return
	}
	err := c.escapeError(ns, e)
	if key := err.Error(); !c.escapeErrors[key] {
		c.escapeErrors[key] = true
		c.l.errs = append(c.l.errs, err)
	}
}

// escapeError returns e, an escaping error in ns, at its file and position. html/template gives the node of some
// errors; it gives the others only the name of the executed template, so locate finds them. An error at the end of
// the template of ns, that it ends in a non-text context, is in its file without a position.
func (c *templateChecker) escapeError(ns *namespace, e *template.Error) *content.Error {
	var f *templateFile
	var pos int
	switch {
	case e.Node != nil:
		f, pos = c.nodeFile(e.Node), int(e.Node.Position())
	case e.ErrorCode != template.ErrEndContext:
		f, pos = c.locate(ns, e)
	}
	if f == nil {
		return &content.Error{File: ns.path, Msg: e.Description}
	}
	line, column := f.position(pos)
	return &content.Error{File: f.path, Line: line, Column: column, Msg: e.Description}
}

// nodeFile returns the file of the template that n is in.
func (c *templateChecker) nodeFile(n parse.Node) *templateFile {
	// The location is "<ParseName>:<line>:<column>", and ParseName is the path of the file.
	loc, _ := (*parse.Tree)(nil).ErrorContext(n)
	if i := strings.LastIndexByte(loc, ':'); i >= 0 {
		if j := strings.LastIndexByte(loc[:i], ':'); j >= 0 {
			return c.files[loc[:j]]
		}
	}
	return nil
}

// step leads from a list of nodes into the node at index: into the List or, if elseList, the ElseList of a branch, or
// into the template that a template call calls.
type step struct {
	index    int
	elseList bool
}

// maxSteps limits the steps of locate.
const maxSteps = 100

// reentry is the prefix of the description of an error that html/template finds in the second pass over the body of
// a range, which it escapes twice to check that it ends in the context in which it starts.
const reentry = "on range loop re-entry: "

// locate finds the file of e, an escaping error in ns without a node, and its offset in the file, or returns a nil
// file. It escapes clones of ns whose lists of nodes are cut short, to find the first node with which e arises: into
// branches and called templates, down to a text node.
func (c *templateChecker) locate(ns *namespace, e *template.Error) (*templateFile, int) {
	var path []step
	tree := ns.trees[ns.name]
	list := tree.Root
	called := map[string]bool{ns.name: true} // the templates on path
	var f *templateFile
	var pos int
	for range maxSteps {
		k := sort.Search(len(list.Nodes), func(i int) bool { return same(c.probe(ns, path, i+1, -1), e) }) + 1
		if k > len(list.Nodes) {
			break
		}
		f, pos = c.files[tree.ParseName], int(list.Nodes[k-1].Position())
		s := step{index: k - 1}
		switch n := list.Nodes[k-1].(type) {
		case *parse.TextNode:
			return f, pos + c.textOffset(ns, path, k, n, e)
		case *parse.TemplateNode:
			// An error in a template that is on path already, called again through others, is at the call.
			if tree = ns.trees[n.Name]; tree == nil || called[n.Name] {
				return f, pos
			}
			called[n.Name] = true
			list = tree.Root
		case *parse.IfNode, *parse.RangeNode, *parse.WithNode:
			if _, ok := n.(*parse.RangeNode); ok && strings.HasPrefix(e.Description, reentry) {
				// Cutting the body would change the second pass.
				return f, pos
			}
			b := branch(n)
			switch {
			case same(c.probe(ns, append(path, s), len(b.List.Nodes), -1), e):
				list = b.List
			case b.ElseList != nil:
				s.elseList = true
				list = b.ElseList
			default:
				return f, pos
			}
		default:
			return f, pos
		}
		path = append(path, s)
	}
	return f, pos
}

// textOffset returns the offset in n, the text node with which e arises, of the character at which it arises.
func (c *templateChecker) textOffset(ns *namespace, path []step, k int, n *parse.TextNode, e *template.Error) int {
	if e.ErrorCode != template.ErrBadHTML && e.ErrorCode != template.ErrSlashAmbig {
		// An escape sequence or a character set of JavaScript or CSS that the text does not finish, before an action.
		_, size := utf8.DecodeLastRune(n.Text)
		return len(n.Text) - size
	}
	// These arise at a character, and the text before it gives no such error, except in the second pass over the body
	// of a range, which starts in another context when the text is cut.
	return sort.Search(len(n.Text), func(i int) bool {
		got := c.probe(ns, path, k, i+1)
		return got != nil && got.ErrorCode == e.ErrorCode && !strings.HasPrefix(got.Description, reentry)
	})
}

// probe escapes a clone of ns and returns its escaping error, without running any node of the templates. The clone is
// cut short if k >= 0: each list on path ends at the node that its step leads into, the branches whose List it leads
// into have no ElseList, the called templates are copies for that call, which call themselves where they did, and the
// last list has its first k nodes. If text >= 0, the last of them, a text node, has its first text bytes.
func (c *templateChecker) probe(ns *namespace, path []step, k, text int) *template.Error {
	clone, err := ns.tmpl.Clone()
	if err != nil {
		// Only an executed template cannot be cloned.
		panic(err)
	}
	if k >= 0 {
		list := clone.Tree.Root
		for i, s := range path {
			list.Nodes = list.Nodes[:s.index+1]
			switch n := list.Nodes[s.index].(type) {
			case *parse.TemplateNode:
				callee := n.Name
				tree := clone.Lookup(callee).Tree.Copy()
				n.Name = fmt.Sprintf("%s\x00%d", callee, i)
				forEachCall(tree.Root, func(x *parse.TemplateNode) {
					if x.Name == callee {
						x.Name = n.Name
					}
				})
				if _, err := clone.AddParseTree(n.Name, tree); err != nil {
					panic(err)
				}
				list = tree.Root
			default:
				b := branch(n)
				if s.elseList {
					list = b.ElseList
				} else {
					b.ElseList = nil
					list = b.List
				}
			}
		}
		list.Nodes = list.Nodes[:k]
		if text >= 0 {
			n := list.Nodes[k-1].(*parse.TextNode)
			n.Text = n.Text[:text]
		}
	}
	// A space keeps the escaping contexts, and its write to stopWriter ends the execution before the template's nodes.
	root := clone.Tree.Root
	root.Nodes = slices.Insert(root.Nodes, 0, stopText.Copy())
	var e *template.Error
	if errors.As(clone.Execute(stopWriter{}, nil), &e) {
		return e
	}
	return nil
}

func same(a, b *template.Error) bool {
	return a != nil && a.ErrorCode == b.ErrorCode && a.Description == b.Description
}

// forEachCall calls f for each template call in list.
func forEachCall(list *parse.ListNode, f func(*parse.TemplateNode)) {
	if list == nil {
		return
	}
	for _, n := range list.Nodes {
		if x, ok := n.(*parse.TemplateNode); ok {
			f(x)
		} else if b := branch(n); b != nil {
			forEachCall(b.List, f)
			forEachCall(b.ElseList, f)
		}
	}
}

func branch(n parse.Node) *parse.BranchNode {
	switch n := n.(type) {
	case *parse.IfNode:
		return &n.BranchNode
	case *parse.RangeNode:
		return &n.BranchNode
	case *parse.WithNode:
		return &n.BranchNode
	}
	return nil
}

// stopWriter fails at the first write, as the escape check needs no output.
type stopWriter struct{}

func (stopWriter) Write([]byte) (int, error) { return 0, errStopped }

var errStopped = errors.New("stopped")

// stopText is a text node of a space, parsed so that it has a tree like the other nodes, for probe.
var stopText = template.Must(template.New("").Parse(" ")).Tree.Root.Nodes[0]
