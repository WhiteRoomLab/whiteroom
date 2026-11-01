package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// pageLanguage is what renderPages uses for the pages in a language.
type pageLanguage struct {
	tr    *theme.Renderer
	r     *render.Renderer
	menus map[string][]MenuItem
}

// renderPages writes the pages of b, rendered with the theme in at most workers goroutines, the site CSS (writeSiteCSS)
// and the assets of the theme into pub. A page whose CSS (pageData) has rules links to the page CSS file
// css/<the first 16 hex digits of its SHA-256>.css, which the pages with the same CSS share; renderPages writes each
// once, after the pages, and adds it to b.outputs in the order of the pages. The warnings go to b.warnings: those of the
// menus (menusOf), of the texts that the theme does not translate in a language, of the pages whose page templates the
// theme does not have (pageTemplate), and of rendering (renderEntry). b.res must be set. The error is ctx.Err() if ctx
// is done, or else the first in the order of the pages, such as that of executing a template, or one of writing.
func (b *builder) renderPages(ctx context.Context, pub *os.Root, workers int) error {
	languages := make(map[string]*pageLanguage, len(b.site.Languages))
	for _, lang := range b.site.Languages {
		tr, err := b.theme.Renderer(lang, b.base)
		if err != nil {
			return err
		}
		r := render.New(tr, b.res)
		languages[lang] = &pageLanguage{tr: tr, r: r, menus: b.menusOf(lang, r)}
		if keys := b.theme.MissingTranslations(lang); len(keys) > 0 {
			b.warnings.add(content.SiteFile{Name: "theme"}, "", "%s",
				untranslated(lang, keys, b.theme.Settings.DefaultLanguage))
		}
	}
	siteCSS, err := writeSiteCSS(pub, render.SiteCSS(b.theme.Settings))
	if err != nil {
		return err
	}
	if err := b.theme.WriteAssets(pub); err != nil {
		return err
	}

	var mu sync.Mutex
	sheets := make(map[string]string) // the page CSS by its path
	pageCSS := make([]string, len(b.pages))
	err = forEach(ctx, len(b.pages), workers, func(i int) error {
		p := b.pages[i]
		l := languages[p.language]
		data, css, err := b.pageData(p, l.r, l.menus)
		if err != nil {
			return err
		}
		data.SiteCSS = l.r.URL(siteCSS)
		if s := css.String(); s != "" {
			sum := sha256.Sum256([]byte(s))
			path := "/css/" + hex.EncodeToString(sum[:8]) + ".css"
			mu.Lock()
			sheets[path] = s
			mu.Unlock()
			pageCSS[i], data.PageCSS = path, l.r.URL(path)
		}
		var buf bytes.Buffer
		if err := l.tr.Execute(&buf, b.pageTemplate(p), data); err != nil {
			return fmt.Errorf("page %s: %w", p.path, err)
		}
		return writeFile(pub, p.file, buf.Bytes())
	})
	if err != nil {
		return err
	}
	written := make(map[string]bool)
	for i, path := range pageCSS {
		if path == "" || written[path] {
			continue
		}
		written[path] = true
		file := strings.TrimPrefix(path, "/")
		if err := writeFile(pub, file, []byte(sheets[path])); err != nil {
			return err
		}
		b.addOutput(path, file, source{what: "the page CSS of " + b.pages[i].path}, rankCore)
	}
	return nil
}

// pageTemplate returns the name of the page template of p (theme.Template). If p is a page whose template the theme
// does not have, which then gets that of a page without one, it warns at its revision.
func (b *builder) pageTemplate(p *page) string {
	x := theme.Page{Kind: p.kind}
	switch p.kind {
	case theme.KindEntry:
		x.Type, x.Template = p.entry.e.Type, p.entry.rev.Template
	case theme.KindTerm:
		x.Taxonomy = p.term.v.Taxonomy
	case theme.KindArchive:
		x.Type = p.typeName
	case theme.KindPosts, theme.KindAuthor, theme.KindNotFound:
	}
	name := b.theme.Template(x)
	if x.Template != "" && name != "page-"+x.Template+".html" {
		b.warnings.add(p.entry.revFile, "/template", "the theme has no page template %q; rendered with %s",
			x.Template, name)
	}
	return name
}

// writeSiteCSS writes css, the CSS of the site, to css/site.css in pub, after `@charset "UTF-8";` and a line break if
// it has characters that are not ASCII, and returns its path with its version: "?v=" and the first 8 hex digits of the
// SHA-256 of the file.
func writeSiteCSS(pub *os.Root, css string) (string, error) {
	if strings.ContainsFunc(css, func(r rune) bool { return r >= utf8.RuneSelf }) {
		css = "@charset \"UTF-8\";\n" + css
	}
	if err := writeFile(pub, "css/site.css", []byte(css)); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(css))
	return "/css/site.css?v=" + hex.EncodeToString(sum[:4]), nil
}

// untranslated returns the warning about keys, those of the texts of the theme that pages in language take from
// defaultLanguage, the default language of the theme.
func untranslated(language string, keys []string, defaultLanguage string) string {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = strconv.Quote(k)
	}
	if len(keys) == 1 {
		return fmt.Sprintf("no translation in %s for the key %s; pages in %s use its text in %s, "+
			"the default language of the theme", language, quoted[0], language, defaultLanguage)
	}
	return fmt.Sprintf("no translation in %s for the keys %s; pages in %s use their texts in %s, "+
		"the default language of the theme", language, strings.Join(quoted, ", "), language, defaultLanguage)
}
