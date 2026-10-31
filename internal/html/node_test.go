package html

import (
	"strings"
	"testing"
)

// childNames returns the names of the children of n, separated by spaces, after checking the links between them.
func childNames(t *testing.T, n *Node) string {
	t.Helper()
	var names []string
	var prev *Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Parent != n || c.PrevSibling != prev {
			t.Fatalf("child %q of %q has a wrong Parent or PrevSibling", c.Name, n.Name)
		}
		names = append(names, c.Name)
		prev = c
	}
	if n.LastChild != prev {
		t.Fatalf("LastChild of %q is not its last child", n.Name)
	}
	return strings.Join(names, " ")
}

func TestAppendInsertRemove(t *testing.T) {
	element := func(name string) *Node { return &Node{Type: ElementNode, Name: name} }
	p := element("p")
	a, b, c, d := element("a"), element("b"), element("c"), element("d")
	p.AppendChild(b)
	p.InsertBefore(d, nil)
	p.InsertBefore(a, b)
	p.InsertBefore(c, d)
	if got := childNames(t, p); got != "a b c d" {
		t.Fatalf("children %q, want %q", got, "a b c d")
	}
	for _, tt := range []struct {
		n    *Node
		want string
	}{
		{b, "a c d"},
		{a, "c d"},
		{d, "c"},
		{c, ""},
	} {
		p.RemoveChild(tt.n)
		if got := childNames(t, p); got != tt.want {
			t.Errorf("after RemoveChild(%s): children %q, want %q", tt.n.Name, got, tt.want)
		}
		if tt.n.Parent != nil || tt.n.PrevSibling != nil || tt.n.NextSibling != nil {
			t.Errorf("RemoveChild(%s) left links on the removed node", tt.n.Name)
		}
	}
	p.AppendChild(c)
	p.AppendChild(a)
	if got := childNames(t, p); got != "c a" {
		t.Errorf("children %q after appending removed nodes, want %q", got, "c a")
	}
}

func TestAppendInsertRemovePanics(t *testing.T) {
	element := func(name string) *Node { return &Node{Type: ElementNode, Name: name} }
	p, q := element("p"), element("q")
	a, b := element("a"), element("b")
	p.AppendChild(a)
	q.AppendChild(b)
	withPrev, withNext := element("x"), element("y")
	withPrev.PrevSibling = element("w")
	withNext.NextSibling = element("z")
	tests := []struct {
		name string
		f    func()
	}{
		{"AppendChild of a child of another node", func() { p.AppendChild(b) }},
		{"AppendChild of a child", func() { p.AppendChild(a) }},
		{"AppendChild of a node with a previous sibling", func() { p.AppendChild(withPrev) }},
		{"AppendChild of a node with a next sibling", func() { p.AppendChild(withNext) }},
		{"InsertBefore before a child of another node", func() { p.InsertBefore(element("c"), b) }},
		{"InsertBefore before a detached node", func() { p.InsertBefore(element("c"), element("d")) }},
		{"RemoveChild of a child of another node", func() { p.RemoveChild(b) }},
		{"RemoveChild of a detached node", func() { p.RemoveChild(element("c")) }},
	}
	for _, tt := range tests {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", tt.name)
				}
			}()
			tt.f()
		}()
	}
	if got, got2 := childNames(t, p), childNames(t, q); got != "a" || got2 != "b" {
		t.Errorf("children %q and %q after the panics, want %q and %q", got, got2, "a", "b")
	}
}
