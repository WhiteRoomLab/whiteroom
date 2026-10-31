package render

import (
	"embed"
	"html/template"
	"io/fs"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// defaultFS holds the default templates of the block types, blocks/<type>.html, which use no functions, so that a theme
// can copy them to its blocks/.
//
//go:embed blocks/*.html
var defaultFS embed.FS

// defaultTemplates are the templates of defaultFS by type.
var defaultTemplates = func() map[content.BlockType]*template.Template {
	names, err := fs.Glob(defaultFS, "blocks/*.html")
	if err != nil {
		panic(err)
	}
	m := make(map[content.BlockType]*template.Template, len(names))
	for _, p := range names {
		src, err := defaultFS.ReadFile(p)
		if err != nil {
			panic(err)
		}
		name := strings.TrimPrefix(p, "blocks/")
		m[content.BlockType(strings.TrimSuffix(name, ".html"))] = template.Must(
			template.New(name).Option("missingkey=error").Parse(string(src)),
		)
	}
	return m
}()

// execute returns the HTML of a block of type t with data, from the block template of the theme for t or else the
// default template, without its trailing line breaks.
func (w *walker) execute(t content.BlockType, data any) (string, error) {
	var b strings.Builder
	var err error
	if w.r.tr.HasBlock(t) {
		err = w.r.tr.ExecuteBlock(&b, t, data)
	} else {
		err = defaultTemplates[t].Execute(&b, data)
	}
	return strings.TrimRight(b.String(), "\r\n"), err
}
