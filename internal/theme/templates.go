package theme

import (
	"cmp"
	"fmt"
	"html/template"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template/parse"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// source is the text of a page template or a component, with its path from the theme root.
type source struct {
	path  string
	text  string
	lines []int // offsets at which the lines start, found when first needed
}

// position returns the line and the column, in characters, of the byte at offset pos, both from 1.
func (s *source) position(pos int) (line, column int) {
	if s.lines == nil {
		s.lines = []int{0}
		for i := range len(s.text) {
			if s.text[i] == '\n' {
				s.lines = append(s.lines, i+1)
			}
		}
	}
	pos = min(max(pos, 0), len(s.text))
	i, found := slices.BinarySearch(s.lines, pos)
	if !found {
		i--
	}
	return i + 1, utf8.RuneCountInString(s.text[s.lines[i]:pos]) + 1
}

// addPos adds the problem at the byte at offset pos in s.
func (l *loader) addPos(s *source, pos int, format string, args ...any) {
	line, column := s.position(pos)
	l.errs = append(l.errs, &content.Error{File: s.path, Line: line, Column: column, Msg: fmt.Sprintf(format, args...)})
}

// templateFile is a page template or a component that has parsed. Its templates have its path as the ParseName of
// their trees, and the root also as its Name.
type templateFile struct {
	*source
	root    *parse.Tree   // the file without its defines
	defines []*parse.Tree // the templates of its define and block actions, in the order of the file
}

// namespace is the namespace of a page template: the page template, its defines and the templates of the components.
type namespace struct {
	name  string                 // the file name of the page template, such as "entry.html"
	trees map[string]*parse.Tree // the templates by name
	tmpl  *template.Template     // the page template, with the same templates; never executed
}

// add adds tree as the template name, unless ns has a template of that name.
func (ns *namespace) add(name string, tree *parse.Tree) {
	if ns.trees[name] != nil {
		return
	}
	ns.trees[name] = tree
	if _, err := ns.tmpl.AddParseTree(name, tree); err != nil {
		// Only an executed template cannot have templates added.
		panic(err)
	}
}

// templateChecker parses the components and the page templates, and checks them.
type templateChecker struct {
	l          *loader
	files      map[string]*templateFile // the files that have parsed, by path
	components map[string]*parse.Tree   // the templates of the components by name: the components and their defines
	maybe      map[string]bool          // the names of the components that have not parsed, and those they seem to define

	texts           map[string]string   // the default translation, if it has loaded
	textsFile       string              // the path of its file
	noTexts         bool                // whether the theme has no translations at all
	keyPlaceholders map[string][]string // the placeholders of the default texts, by key, found when first needed

	calls        map[*parse.Tree][]call // the template calls of the templates checked so far
	unknown      map[*parse.TemplateNode]*unknownCall
	unknownOrder []*unknownCall
	escapeErrors map[string]bool // the escaping errors reported so far
}

// call is a template call in a file.
type call struct {
	node *parse.TemplateNode
	file *templateFile
}

// unknownCall is a template call that does not resolve in the namespaces of pages.
type unknownCall struct {
	call
	pages []string
}

// parseTemplates parses the components and the page templates into a namespace for each page template, and checks
// the templates that each page template reaches.
func (l *loader) parseTemplates() {
	c := &templateChecker{
		l:               l,
		files:           make(map[string]*templateFile),
		components:      make(map[string]*parse.Tree),
		maybe:           make(map[string]bool),
		keyPlaceholders: make(map[string][]string),
		calls:           make(map[*parse.Tree][]call),
		unknown:         make(map[*parse.TemplateNode]*unknownCall),
		escapeErrors:    make(map[string]bool),
	}
	c.findTexts()
	c.parseComponents()
	pages := c.parsePages()
	for _, ns := range pages {
		c.reach(ns)
	}
	for _, u := range c.unknownOrder {
		msg := fmt.Sprintf("unknown template %q", u.node.Name)
		if !strings.HasPrefix(u.file.path, "templates/") {
			msg += " when used by templates/" + strings.Join(u.pages, ", templates/")
		}
		l.addPos(u.file.source, int(u.node.Pos), "%s", msg)
	}
	for _, ns := range pages {
		c.escape(ns)
		l.t.namespaces[ns.name] = ns.tmpl
	}
}

// findTexts finds the default translation for the checks of t. Without theme.yaml, or with translation files that
// have not loaded, the keys are not checked.
func (c *templateChecker) findTexts() {
	l := c.l
	if l.settings == nil {
		return
	}
	lang := l.t.Settings.DefaultLanguage
	if texts := l.t.translations[lang]; lang != "" && texts != nil {
		c.texts, c.textsFile = texts, "translations/"+lang+".yaml"
		return
	}
	c.noTexts = lang == "" && len(l.languages) == 0 && !l.translationsUnread
}

// parse parses the file at p, or reports why it cannot.
func (c *templateChecker) parse(p, text string) *templateFile {
	s := &source{path: p, text: text}
	if !utf8.ValidString(text) {
		c.l.addPos(s, invalidUTF8(text), "invalid UTF-8")
		return nil
	}
	tmpl, err := template.New(p).Funcs(stubs).Parse(text)
	if err != nil {
		c.l.errs = append(c.l.errs, syntaxError(p, err))
		return nil
	}
	f := &templateFile{source: s}
	for _, x := range tmpl.Templates() {
		if x.Name() == p {
			f.root = x.Tree
		} else {
			f.defines = append(f.defines, x.Tree)
		}
	}
	slices.SortFunc(f.defines, func(a, b *parse.Tree) int { return cmp.Compare(a.Root.Pos, b.Root.Pos) })
	c.files[p] = f
	return f
}

// invalidUTF8 returns the offset of the first byte of s that is not valid UTF-8.
func invalidUTF8(s string) int {
	for i, r := range s {
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				return i
			}
		}
	}
	return len(s)
}

// syntaxError returns err from parsing the file at p, "template: <p>:<line>: <message>", with the line apart.
func syntaxError(p string, err error) *content.Error {
	e := &content.Error{File: p, Msg: err.Error()}
	rest, ok := strings.CutPrefix(e.Msg, "template: "+p+":")
	if !ok {
		return e
	}
	line, msg, ok := strings.Cut(rest, ": ")
	n, err := strconv.Atoi(line)
	if !ok || err != nil || n < 1 {
		return e
	}
	e.Line, e.Msg = n, msg
	if name, ok := strings.CutPrefix(msg, "template: multiple definition of template "); ok {
		e.Msg = "duplicate define " + name
	}
	return e
}

// defineAction finds the names of the define and block actions in a file that does not parse.
var defineAction = regexp.MustCompile(`\{\{-?\s*(?:define|block)\s+("(?:[^"\\\n]|\\.)*"|` + "`[^`]*`)")

// parseComponents parses the components and checks their defines, whose names must be unique.
func (c *templateChecker) parseComponents() {
	var files []*templateFile
	for _, name := range slices.Sorted(maps.Keys(c.l.t.components)) {
		text := c.l.t.components[name]
		f := c.parse("components/"+name+".html", text)
		if f == nil {
			c.maybe[name] = true
			for _, m := range defineAction.FindAllStringSubmatch(text, -1) {
				if s, err := strconv.Unquote(m[1]); err == nil {
					c.maybe[s] = true
				}
			}
			continue
		}
		c.components[name] = f.root
		files = append(files, f)
	}
	where := make(map[string][]string) // the paths of the components that define each name
	for _, f := range files {
		for _, d := range f.defines {
			where[d.Name] = append(where[d.Name], f.path)
		}
	}
	for _, f := range files {
		for _, d := range f.defines {
			if !c.checkDefine(f, d) {
				continue
			}
			if others := slices.DeleteFunc(slices.Clone(where[d.Name]), func(p string) bool { return p == f.path }); len(others) > 0 {
				c.defineError(f, d, "define %q is also in %s", d.Name, strings.Join(others, ", "))
			}
			if c.components[d.Name] == nil {
				c.components[d.Name] = d
			}
		}
	}
}

// parsePages parses the page templates and makes their namespaces. A page template may define the names that
// components define, to replace them, such as the blocks of a layout.
func (c *templateChecker) parsePages() []*namespace {
	var pages []*namespace
	for _, name := range slices.Sorted(maps.Keys(c.l.t.templates)) {
		f := c.parse("templates/"+name, c.l.t.templates[name])
		if f == nil {
			continue
		}
		ns := &namespace{
			name:  name,
			trees: make(map[string]*parse.Tree),
			tmpl:  template.New(name).Funcs(stubs).Option("missingkey=error"),
		}
		ns.add(name, f.root)
		for _, d := range f.defines {
			if c.checkDefine(f, d) {
				ns.add(d.Name, d)
			}
		}
		for _, n := range slices.Sorted(maps.Keys(c.components)) {
			ns.add(n, c.components[n])
		}
		ns.tmpl = ns.tmpl.Lookup(name)
		pages = append(pages, ns)
	}
	return pages
}

// checkDefine checks the name of d, a define or a block in f, and reports whether namespaces may have it: not if it
// has the name of a component.
func (c *templateChecker) checkDefine(f *templateFile, d *parse.Tree) bool {
	if err := checkDefineName(d.Name); err != nil {
		c.defineError(f, d, "%v", err)
	}
	if _, ok := c.l.t.components[d.Name]; ok {
		c.defineError(f, d, "define %q has the name of components/%s.html", d.Name, d.Name)
		return false
	}
	return true
}

// checkDefineName checks the name of a define or a block: names joined by "/", such as "entry/meta".
func checkDefineName(name string) error {
	for s := range strings.SplitSeq(name, "/") {
		if content.CheckName(s) != nil {
			return fmt.Errorf(`invalid define name %q: use ASCII letters, digits, "-" and "_", and "/" between parts`, name)
		}
	}
	return nil
}

func (c *templateChecker) defineError(f *templateFile, d *parse.Tree, format string, args ...any) {
	c.l.addPos(f.source, definePos(f.text, int(d.Root.Pos)), format, args...)
}

// definePos returns the offset of the name in the define or block action that the body at offset body follows, or
// body if it does not find the action.
func definePos(text string, body int) int {
	end := strings.LastIndex(text[:body], "}}")
	if end < 0 || strings.Trim(text[end+2:body], " \t\r\n") != "" {
		return body
	}
	start := strings.LastIndex(text[:end], "{{")
	if start < 0 {
		return body
	}
	action := strings.TrimLeft(strings.TrimPrefix(text[start+2:end], "-"), " \t\r\n")
	for _, keyword := range []string{"define", "block"} {
		if rest, ok := strings.CutPrefix(action, keyword); ok {
			if name := strings.TrimLeft(rest, " \t\r\n"); len(name) < len(rest) {
				return end - len(name)
			}
		}
	}
	return body
}

// reach checks the templates that the page template of ns reaches, and finds the template calls that do not resolve.
func (c *templateChecker) reach(ns *namespace) {
	root := ns.trees[ns.name]
	seen := map[*parse.Tree]bool{root: true}
	todo := []*parse.Tree{root}
	for len(todo) > 0 {
		tree := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		for _, x := range c.check(tree) {
			if t := ns.trees[x.node.Name]; t != nil {
				if !seen[t] {
					seen[t] = true
					todo = append(todo, t)
				}
				continue
			}
			if c.maybe[x.node.Name] || c.l.componentsMissing {
				continue
			}
			u := c.unknown[x.node]
			if u == nil {
				u = &unknownCall{call: x}
				c.unknown[x.node] = u
				c.unknownOrder = append(c.unknownOrder, u)
			}
			u.pages = append(u.pages, ns.name)
		}
	}
}

// check checks the calls of functions in tree, once, and returns its template calls.
func (c *templateChecker) check(tree *parse.Tree) []call {
	if calls, ok := c.calls[tree]; ok {
		return calls
	}
	w := &treeWalker{c: c, file: c.files[tree.ParseName]}
	w.list(tree.Root)
	c.calls[tree] = w.calls
	return w.calls
}

// treeWalker walks a template: it checks the calls of functions and collects the template calls.
type treeWalker struct {
	c     *templateChecker
	file  *templateFile
	calls []call
}

func (w *treeWalker) errorf(n parse.Node, format string, args ...any) {
	w.c.l.addPos(w.file.source, int(n.Position()), format, args...)
}

func (w *treeWalker) list(l *parse.ListNode) {
	if l == nil {
		return
	}
	for _, n := range l.Nodes {
		switch n := n.(type) {
		case *parse.ActionNode:
			w.pipe(n.Pipe)
		case *parse.IfNode:
			w.branch(&n.BranchNode)
		case *parse.RangeNode:
			w.branch(&n.BranchNode)
		case *parse.WithNode:
			w.branch(&n.BranchNode)
		case *parse.TemplateNode:
			w.calls = append(w.calls, call{n, w.file})
			w.pipe(n.Pipe)
		}
	}
}

func (w *treeWalker) branch(b *parse.BranchNode) {
	w.pipe(b.Pipe)
	w.list(b.List)
	w.list(b.ElseList)
}

func (w *treeWalker) pipe(p *parse.PipeNode) {
	if p == nil {
		return
	}
	for i, cmd := range p.Cmds {
		for j, arg := range cmd.Args {
			w.arg(arg, cmd.Args, j == 0, i == 0)
		}
	}
}

// arg checks n, a word of a command whose words are args: the first word if first, in the first command of its
// pipeline if unpiped.
func (w *treeWalker) arg(n parse.Node, args []parse.Node, first, unpiped bool) {
	switch n := n.(type) {
	case *parse.IdentifierNode:
		w.ident(n, args, first, unpiped)
	case *parse.PipeNode:
		w.pipe(n)
	case *parse.ChainNode:
		w.arg(n.Node, nil, false, unpiped)
	}
}
