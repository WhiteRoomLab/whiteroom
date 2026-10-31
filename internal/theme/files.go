package theme

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// readRoot reads the entries at the root of the theme that it knows.
func (l *loader) readRoot() {
	entries, err := fs.ReadDir(l.fsys, ".")
	if err != nil {
		l.addf("", "cannot read the theme directory: %v", pathless(err))
		return
	}
	found := make(map[string]bool)
	for _, d := range entries {
		name := d.Name()
		switch name {
		case "theme.yaml":
			if l.checkType(name, d, false) {
				l.readSettings(d)
			}
		case "templates":
			if l.checkType(name, d, true) {
				l.readTemplates()
			}
		case "blocks":
			if l.checkType(name, d, true) {
				l.readBlocks()
			}
		case "components":
			if !l.checkType(name, d, true) || !l.readComponents() {
				l.componentsMissing = true
			}
		case "assets":
			if !l.checkType(name, d, true) || !l.readAssets() {
				l.assetsMissing = true
			}
		case "translations":
			if !l.checkType(name, d, true) || !l.readTranslations() {
				l.translationsUnread = true
			}
		default:
			continue
		}
		found[name] = true
	}
	if !found["theme.yaml"] {
		l.addf("theme.yaml", "missing file")
	}
	if !found["templates"] {
		l.addf("templates", "missing directory")
	}
}

// checkType reports whether d, the entry at p, is a directory if dir is true, or a regular file if not, and adds the
// problem if it is not.
func (l *loader) checkType(p string, d fs.DirEntry, dir bool) bool {
	t := d.Type()
	switch {
	case t&fs.ModeSymlink != 0:
		l.addf(p, "symbolic links are not allowed")
	case dir && !t.IsDir():
		l.addf(p, "must be a directory")
	case !dir && !t.IsRegular():
		l.addf(p, "must be a regular file")
	default:
		return true
	}
	return false
}

func (l *loader) readSettings(d fs.DirEntry) {
	const name = "theme.yaml"
	data, ok := l.readFile(name, d, content.MaxTextBytes)
	if !ok {
		return
	}
	s, err := content.LoadThemeSettings(name, data)
	if err != nil {
		l.addErrors(name, err)
		return
	}
	l.t.Settings = s
	l.settings = &yamlFile{name: name, data: data}
}

// readFile reads d, the regular file at p, if it has at most limit bytes.
func (l *loader) readFile(p string, d fs.DirEntry, limit int) ([]byte, bool) {
	info, err := d.Info()
	if err != nil {
		l.ioError(p, err)
		return nil, false
	}
	if info.Size() > int64(limit) {
		l.addf(p, "too large: %d bytes (max %d)", info.Size(), limit)
		return nil, false
	}
	f, err := l.fsys.Open(p)
	if err != nil {
		l.ioError(p, err)
		return nil, false
	}
	defer func() { _ = f.Close() }()
	var b bytes.Buffer
	b.Grow(int(max(info.Size(), 0)) + bytes.MinRead)
	// The file may have grown since its size was read.
	if _, err := b.ReadFrom(io.LimitReader(f, int64(limit)+1)); err != nil {
		l.ioError(p, err)
		return nil, false
	}
	if b.Len() > limit {
		l.addf(p, "too large: at least %d bytes (max %d)", b.Len(), limit)
		return nil, false
	}
	return b.Bytes(), true
}

// readDir returns the entries of the directory dir, without hidden ones, or reports why it cannot.
func (l *loader) readDir(dir string) ([]fs.DirEntry, bool) {
	entries, err := fs.ReadDir(l.fsys, dir)
	if err != nil {
		l.ioError(dir, err)
		return nil, false
	}
	return slices.DeleteFunc(entries, func(d fs.DirEntry) bool { return hidden(d.Name()) }), true
}

// walk calls file for each regular file under dir, such as "components/entry/card.html", with its path rel from dir,
// such as "entry/card.html". It ignores hidden entries, and reports the other entries that are neither regular files
// nor directories, and the directories whose names check rejects. It reports whether every entry that is not hidden
// was a directory that it has read or a regular file that it has passed to file.
func (l *loader) walk(dir string, check func(name string) error, file func(p, rel string, d fs.DirEntry)) bool {
	complete := true
	_ = fs.WalkDir(l.fsys, dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			l.ioError(p, err)
			complete = false
			return fs.SkipDir
		case p == dir:
		case hidden(d.Name()):
			if d.IsDir() {
				return fs.SkipDir
			}
		case d.IsDir():
			if err := check(d.Name()); err != nil {
				l.add(p, err)
			}
		case d.Type()&fs.ModeSymlink != 0:
			l.addf(p, "symbolic links are not allowed")
			complete = false
		case !d.Type().IsRegular():
			l.addf(p, "must be a regular file or a directory")
			complete = false
		default:
			file(p, p[len(dir)+1:], d)
		}
		return nil
	})
	return complete
}

// hidden reports whether name is that of a hidden entry, such as .git or .DS_Store.
func hidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

func (l *loader) readTemplates() {
	entries, ok := l.readDir("templates")
	if !ok {
		return
	}
	l.templatesRead = true
	for _, d := range entries {
		name := d.Name()
		p := "templates/" + name
		l.templateFiles = append(l.templateFiles, name)
		if d.IsDir() {
			l.addf(p, "subdirectories are not allowed; put components in components/")
			continue
		}
		if !l.checkType(p, d, false) {
			continue
		}
		if err := checkTemplateName(name); err != nil {
			l.add(p, err)
			continue
		}
		if data, ok := l.readFile(p, d, content.MaxTextBytes); ok {
			l.t.templates[name] = string(data)
		}
	}
}

// fixedTemplates are the names of the page templates without a name in them, and templatePrefixes the prefixes of
// the others, which a name follows, as in entry-works.html.
var (
	fixedTemplates   = []string{"entry.html", "list.html", "posts.html", "term.html", "author.html", "404.html"}
	templatePrefixes = []string{"entry-", "page-", "term-", "archive-"}
)

func checkTemplateName(name string) error {
	if slices.Contains(fixedTemplates, name) {
		return nil
	}
	if base, ok := strings.CutSuffix(name, ".html"); ok {
		for _, prefix := range templatePrefixes {
			if rest, ok := strings.CutPrefix(base, prefix); ok && rest != "" {
				return content.CheckName(rest)
			}
		}
	}
	return fmt.Errorf("unknown template name %q (use entry.html, entry-{type}.html, page-{template}.html, list.html, "+
		"posts.html, term.html, term-{taxonomy}.html, archive-{type}.html, author.html, 404.html)", name)
}

// checkTemplates checks that templates/ has entry.html and list.html, and that its page templates for pages,
// page-<name>.html, are those of pageTemplates in theme.yaml.
func (l *loader) checkTemplates() {
	if !l.templatesRead {
		return
	}
	for _, name := range []string{"entry.html", "list.html"} {
		if !slices.Contains(l.templateFiles, name) {
			l.addf("templates/"+name, "missing file; every theme needs entry.html and list.html")
		}
	}
	if l.settings == nil {
		return
	}
	declared := l.t.Settings.PageTemplates
	for _, name := range l.templateFiles {
		base, _ := strings.CutSuffix(name, ".html")
		x, ok := strings.CutPrefix(base, "page-")
		if !ok || checkTemplateName(name) != nil {
			continue
		}
		if !slices.ContainsFunc(declared, func(pt content.ThemeName) bool { return pt.Name == x }) {
			l.addf("templates/"+name, "unknown page template %q; declare it in pageTemplates of theme.yaml", x)
		}
	}
	for _, pt := range declared {
		if !slices.Contains(l.templateFiles, "page-"+pt.Name+".html") {
			l.addAt(l.settings, "/pageTemplates/"+pt.Name, "missing templates/page-%s.html", pt.Name)
		}
	}
}

func (l *loader) readBlocks() {
	entries, ok := l.readDir("blocks")
	if !ok {
		return
	}
	for _, d := range entries {
		name := d.Name()
		p := "blocks/" + name
		if d.IsDir() {
			l.addf(p, "subdirectories are not allowed")
			continue
		}
		if !l.checkType(p, d, false) {
			continue
		}
		t, err := blockTemplateType(name)
		if err != nil {
			l.add(p, err)
			continue
		}
		if data, ok := l.readFile(p, d, content.MaxTextBytes); ok {
			l.t.blocks[t] = string(data)
		}
	}
}

// blockTypes are the types of blocks that themes may have block templates for, blocks/<type>.html: every type but
// html and legacy.
var blockTypes = []content.BlockType{
	content.BlockParagraph, content.BlockHeading, content.BlockList, content.BlockListItem, content.BlockQuote,
	content.BlockImage, content.BlockTable, content.BlockTableRow, content.BlockTableCell, content.BlockButtons,
	content.BlockButton, content.BlockSeparator, content.BlockSpacer, content.BlockCode, content.BlockEmbed,
	content.BlockColumns, content.BlockColumn, content.BlockGroup, content.BlockFile, content.BlockForm,
}

// blockTemplateType returns the type of blocks of the block template whose file is name, such as image.html.
func blockTemplateType(name string) (content.BlockType, error) {
	if base, ok := strings.CutSuffix(name, ".html"); ok {
		if t := content.BlockType(base); slices.Contains(blockTypes, t) {
			return t, nil
		}
	}
	names := make([]string, len(blockTypes))
	for i, t := range blockTypes {
		names[i] = string(t) + ".html"
	}
	return "", fmt.Errorf("unknown block template name %q (use %s)", name, strings.Join(names, ", "))
}

// readComponents reads the components, named by their paths from components/ without ".html", such as "entry/card",
// and reports whether every entry that may be a component has loaded.
func (l *loader) readComponents() bool {
	loaded := true
	complete := l.walk("components", content.CheckName, func(p, rel string, d fs.DirEntry) {
		name, ok := strings.CutSuffix(rel, ".html")
		if !ok {
			l.addf(p, "unknown file %q; components are <name>.html files", d.Name())
			return
		}
		if err := content.CheckName(path.Base(name)); err != nil {
			l.add(p, err)
			loaded = false
			return
		}
		data, ok := l.readFile(p, d, content.MaxTextBytes)
		if !ok {
			loaded = false
			return
		}
		l.t.components[name] = string(data)
	})
	return complete && loaded
}
