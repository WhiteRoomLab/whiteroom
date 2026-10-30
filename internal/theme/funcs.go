package theme

import (
	"html/template"
	"text/template/parse"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// stubs stand for the functions that templates can call besides the builtins of text/template, with their types, so
// that Load can parse and check templates. They return zero values. Renderer binds the real functions to clones of the
// namespaces, since Funcs on a clone replaces the functions of the same names.
var stubs = template.FuncMap{
	"t":     func(string, ...any) (string, error) { return "", nil },
	"asset": func(string) (string, error) { return "", nil },
	"dict":  func(...any) (map[string]any, error) { return nil, nil },
}

// funcExamples has a call of each function of stubs, for the messages of the checks.
var funcExamples = map[string]string{"t": `t "key"`, "asset": `asset "css/style.css"`, "dict": `dict "key" value`}

// ident checks id, a word in a command whose words are args: the first word if first, in the first command of its
// pipeline if unpiped. The functions of stubs must be the first word of a command that no value is piped into, and
// their arguments must follow the rules of the functions.
func (w *treeWalker) ident(id *parse.IdentifierNode, args []parse.Node, first, unpiped bool) {
	if id.Ident == "call" {
		w.errorf(id, "call is not allowed; template data holds no functions")
		return
	}
	example, ok := funcExamples[id.Ident]
	switch {
	case !ok:
	case !first:
		w.errorf(id, "%s as an argument needs parentheses, as in (%s)", id.Ident, example)
	case !unpiped:
		w.errorf(id, "%s cannot be piped into; write {{%s}}", id.Ident, example)
	case id.Ident == "t":
		w.t(id, args)
	case id.Ident == "asset":
		w.asset(id, args)
	default:
		w.dict(args)
	}
}

// t checks t "key" ["name" value ...]: the key is in the default translation, and the names are the placeholders of
// its text.
func (w *treeWalker) t(id *parse.IdentifierNode, args []parse.Node) {
	if len(args) < 2 {
		w.errorf(id, `t needs a key, as in {{t "key"}}`)
		return
	}
	var names []*parse.StringNode
	for i := 2; i < len(args); i += 2 {
		name, ok := args[i].(*parse.StringNode)
		if !ok {
			w.errorf(args[i], `the names of placeholders must be string constants, such as "page"`)
			continue
		}
		if i+1 == len(args) {
			w.errorf(name, "missing value for placeholder {%s}", name.Text)
		}
		names = append(names, name)
	}
	key, ok := args[1].(*parse.StringNode)
	if !ok {
		w.errorf(args[1], `the key of t must be a string constant, such as "readMore"`)
		return
	}
	c := w.c
	switch {
	case c.texts != nil:
		if _, ok := c.texts[key.Text]; !ok {
			w.errorf(key, "unknown key %q; keys must be in the default translation, %s", key.Text, c.textsFile)
			return
		}
		w.placeholders(key, names)
	case c.noTexts:
		w.errorf(key, "unknown key %q; the theme has no translation files, translations/<language>.yaml", key.Text)
	}
}

// placeholders checks that names are the placeholders of the default text of key, each once.
func (w *treeWalker) placeholders(key *parse.StringNode, names []*parse.StringNode) {
	want := w.c.placeholders(key.Text)
	has := make(map[string]bool, len(want))
	for _, p := range want {
		has[p] = true
	}
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		switch {
		case seen[n.Text]:
			w.errorf(n, "duplicate placeholder {%s}", n.Text)
		case !has[n.Text]:
			list := "no placeholders"
			if len(want) > 0 {
				list = braces(want)
			}
			w.errorf(n, "unknown placeholder {%s}; the default translation of %q has %s", n.Text, key.Text, list)
		}
		seen[n.Text] = true
	}
	var missing []string
	for _, p := range want {
		if !seen[p] {
			missing = append(missing, p)
		}
	}
	switch len(missing) {
	case 0:
	case 1:
		w.errorf(key, "missing value for placeholder %s", braces(missing))
	default:
		w.errorf(key, "missing values for placeholders %s", braces(missing))
	}
}

// placeholders returns the placeholders of the default text of key.
func (c *templateChecker) placeholders(key string) []string {
	p, ok := c.keyPlaceholders[key]
	if !ok {
		p = content.ThemeText{Text: c.texts[key]}.Placeholders()
		c.keyPlaceholders[key] = p
	}
	return p
}

// asset checks asset "path": the path is that of a file in assets/.
func (w *treeWalker) asset(id *parse.IdentifierNode, args []parse.Node) {
	switch len(args) {
	case 1:
		w.errorf(id, `asset needs a path, as in {{asset "css/style.css"}}`)
		return
	case 2:
	default:
		w.errorf(args[2], "asset takes one path")
	}
	p, ok := args[1].(*parse.StringNode)
	if !ok {
		w.errorf(args[1], `the path of asset must be a string constant, such as "css/style.css"`)
		return
	}
	if _, ok := w.c.l.t.assets[p.Text]; !ok && !w.c.l.assetsMissing {
		w.errorf(p, "unknown asset %q; paths are relative to assets/", p.Text)
	}
}

// dict checks dict "key" value ...: the keys are unique, and each has a value.
func (w *treeWalker) dict(args []parse.Node) {
	seen := make(map[string]bool)
	for i := 1; i < len(args); i += 2 {
		key, ok := args[i].(*parse.StringNode)
		if !ok {
			w.errorf(args[i], `the keys of dict must be string constants, such as "title"`)
			continue
		}
		if seen[key.Text] {
			w.errorf(key, "duplicate key %q in dict", key.Text)
		}
		seen[key.Text] = true
		if i+1 == len(args) {
			w.errorf(key, "missing value for key %q", key.Text)
		}
	}
}
