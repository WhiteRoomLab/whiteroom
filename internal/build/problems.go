package build

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// problems collects errors or warnings about the files of a site. It is safe for concurrent use.
type problems struct {
	mu      sync.Mutex
	n       int
	files   map[string]*fileProblems // the problems to locate, by the name of their file
	located content.Errors
}

type fileProblems struct {
	file content.SiteFile
	errs content.Errors
}

// add adds a problem at path, a JSON Pointer in f.
func (p *problems) add(f content.SiteFile, path, format string, args ...any) {
	p.addErr(f, &content.Error{Path: path, Msg: fmt.Sprintf(format, args...)})
}

// addErr adds the problems in err, if any, which are about f: those of content.Errors or a *content.Error at their
// paths, or else err at the root.
func (p *problems) addErr(f content.SiteFile, err error) {
	es := errorsOf(err)
	if len(es) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.files == nil {
		p.files = make(map[string]*fileProblems)
	}
	fp := p.files[f.Name]
	if fp == nil {
		fp = &fileProblems{file: f}
		p.files[f.Name] = fp
	}
	fp.errs = append(fp.errs, es...)
	p.n += len(es)
}

// addLocated adds problems that have their files and positions, such as those of content.DecodeFile.
func (p *problems) addLocated(es content.Errors) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.located = append(p.located, es...)
	p.n += len(es)
}

// len returns the number of problems added, with duplicates.
func (p *problems) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

// sorted returns the problems, located in their files, sorted by file, line, column, path and message, without
// duplicates.
func (p *problems) sorted() content.Errors {
	p.mu.Lock()
	defer p.mu.Unlock()
	es := slices.Clone(p.located)
	for _, fp := range p.files {
		es = append(es, content.LocateErrors(fp.errs, fp.file)...)
	}
	slices.SortFunc(es, compareErrors)
	return slices.CompactFunc(es, func(a, b *content.Error) bool { return *a == *b })
}

func compareErrors(a, b *content.Error) int {
	return cmp.Or(
		strings.Compare(a.File, b.File),
		cmp.Compare(a.Line, b.Line),
		cmp.Compare(a.Column, b.Column),
		strings.Compare(a.Path, b.Path),
		strings.Compare(a.Msg, b.Msg),
	)
}

// errorsOf returns the problems in err: those of content.Errors, a *content.Error, or else err as one at the root.
func errorsOf(err error) content.Errors {
	var es content.Errors
	var e *content.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &es):
		return es
	case errors.As(err, &e):
		return content.Errors{e}
	}
	return content.Errors{{Msg: err.Error()}}
}
