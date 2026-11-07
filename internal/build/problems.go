package build

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
)

// problems collects errors or warnings about the files of a site. It is safe for concurrent use.
type problems struct {
	mu      sync.Mutex
	n       int
	files   map[string]*fileProblems // the problems to locate, by the name of their file
	located content.Errors
	read    func(name string) ([]byte, error) // reads a file that was added by its name alone, to locate its problems
}

type fileProblems struct {
	file content.SiteFile
	errs content.Errors
}

// add adds a problem at path, a JSON Pointer in f.
func (p *problems) add(f content.SiteFile, path, format string, args ...any) {
	p.addErr(f, &content.Error{Path: path, Msg: fmt.Sprintf(format, args...)})
}

// report returns the function that adds a problem to p, as siteplan reports them.
func (p *problems) report() siteplan.Report {
	return func(f content.SiteFile, pointer, msg string) { p.add(f, pointer, "%s", msg) }
}

// addErr adds the problems in err, if any, which are about f: those of content.Errors or a *content.Error at their
// paths, or else err at the root. If f has no Data, they are located with p.read when they are sorted.
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
	switch {
	case fp == nil:
		fp = &fileProblems{file: f}
		p.files[f.Name] = fp
	case fp.file.Data == nil: // so that a file with its Data is not read again
		fp.file = f
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
// duplicates. The files without Data are read with p.read, one at a time; if that fails, their problems have no
// position.
func (p *problems) sorted() content.Errors {
	p.mu.Lock()
	defer p.mu.Unlock()
	es := slices.Clone(p.located)
	for _, fp := range p.files {
		f := fp.file
		if f.Data == nil && p.read != nil {
			if data, err := p.read(f.Name); err == nil {
				f.Data = data
			}
		}
		es = append(es, content.LocateErrors(fp.errs, f)...)
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
