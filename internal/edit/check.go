package edit

import (
	"cmp"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// collector collects the problems that siteplan reports.
type collector struct{ errs content.Errors }

func (c *collector) report(f content.SiteFile, pointer, msg string) {
	c.errs = append(c.errs, &content.Error{File: f.Name, Path: pointer, Msg: msg})
}

// mediaPath returns the path that stands for the file of a medium: the names of the files need their originals, which
// only the build reads.
func mediaPath(id string) (string, error) { return "/media/" + id + "/", nil }

// stages runs the checks of s in the stages of the build (4.3): the checks across entities; the plan of the pages and
// the other outputs; and the redirects with the duplicates of the output paths. It calls yield with the problems of each
// stage, and after the last also with the warnings of the redirects, and stops when yield returns false. The checks
// that need the files of the media, the page CSS or the parts of a sitemap stay the build's alone.
func stages(s *siteplan.Site, yield func(problems, warnings content.Errors) bool) {
	var across collector
	s.CheckAcross(across.report)
	if !yield(across.errs, nil) {
		return
	}
	var planned collector
	plan := s.Plan(planned.report)
	if !yield(planned.errs, nil) {
		return
	}
	var outputs, warnings collector
	redirects := s.ResolveRedirects(plan, mediaPath, warnings.report, outputs.report)
	siteplan.CheckOutputs(plan.Outputs, redirects, outputs.report)
	yield(outputs.errs, warnings.errs)
}

// problemKey is what two problems have in common if they are the same one before and after a change: they differ
// in the positions of their lines, which a change in the file moves.
type problemKey struct{ file, pointer, msg string }

func keyOf(e *content.Error) problemKey { return problemKey{e.File, e.Path, e.Msg} }

// problemKeys returns the keys of the problems and the warnings of s in every stage.
func problemKeys(s *siteplan.Site) map[problemKey]bool {
	keys := make(map[problemKey]bool)
	stages(s, func(problems, warnings content.Errors) bool {
		for _, e := range slices.Concat(problems, warnings) {
			keys[keyOf(e)] = true
		}
		return true
	})
	return keys
}

// checkSite runs the checks across the site of the build on after, the model of the site with a change, and returns the
// problems that the change brings: those of the first stage that has any that before, the model of the site without the
// change, does not have, by file, JSON Pointer and message. Without problems, it returns the new warnings of the
// redirects. They are located as the build locates its own and sorted. before is called at most once, when after has a
// problem or a warning; it returns nil if the model cannot be made, which makes every problem new, and a nil before is
// that too.
func (r *run) checkSite(after *siteplan.Site, before func() (*siteplan.Site, error)) (problems, warnings content.Errors, err error) {
	var old map[problemKey]bool
	made := false
	fresh := func(es content.Errors) (content.Errors, error) {
		if len(es) == 0 {
			return nil, nil
		}
		if !made && before != nil {
			made = true
			m, err := before()
			if err != nil {
				return nil, err
			}
			if m != nil {
				old = problemKeys(m)
			}
		}
		var out content.Errors
		for _, e := range es {
			if !old[keyOf(e)] {
				out = append(out, e)
			}
		}
		return out, nil
	}
	stages(after, func(p, w content.Errors) bool {
		if problems, err = fresh(p); err != nil || len(problems) > 0 {
			return false
		}
		warnings, err = fresh(w)
		return err == nil
	})
	if err != nil {
		return nil, nil, err
	}
	if problems, err = r.locate(problems); err != nil {
		return nil, nil, err
	}
	if warnings, err = r.locate(warnings); err != nil {
		return nil, nil, err
	}
	return problems, warnings, nil
}

// finish checks the site with the change that the Update has made, whose model is after: the operation fails with the
// new problems, or else res gets the new warnings.
func (r *run) finish(res *Result, after *siteplan.Site) error {
	problems, warnings, err := r.checkSite(after, r.before)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return problems
	}
	res.Warnings = sorted(slices.Concat(res.Warnings, warnings))
	return nil
}

// locate returns copies of es, problems that siteplan has reported by the name of their files and their JSON Pointers,
// with the lines and the columns of their pointers, as the build locates its problems: in the bytes that the caller
// gave, or else in those that the store has, or in the schema file; a problem in a file that is none of them, such as one
// of the theme, has none. Only the files with problems are read.
func (r *run) locate(es content.Errors) (content.Errors, error) {
	byFile := make(map[string]content.Errors)
	for _, e := range es {
		byFile[e.File] = append(byFile[e.File], e)
	}
	var located content.Errors
	for _, name := range slices.Sorted(maps.Keys(byFile)) {
		data, err := r.fileData(name)
		if err != nil {
			return nil, err
		}
		located = append(located, content.LocateErrors(byFile[name], content.SiteFile{Name: name, Data: data})...)
	}
	return sorted(located), nil
}

// kindOfDir are the kinds of the documents by the directory of their files in a site directory.
var kindOfDir = func() map[string]content.RefKind {
	m := make(map[string]content.RefKind)
	for _, kind := range []content.RefKind{
		content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
		content.RefRedirect,
	} {
		m[content.DocumentDir(kind)] = kind
	}
	return m
}()

// fileData returns the bytes of the file name in which its problems are located: those that the caller gave, the schema
// file, or those that the store has of a document, a revision or the settings document; nil for any other file.
func (r *run) fileData(name string) ([]byte, error) {
	if data, ok := r.given[name]; ok {
		return data, nil
	}
	if name != "" && name == r.e.schema.Name {
		return r.e.schema.Data, nil
	}
	data, err := r.stored(name)
	// The names are those of the model, but a file that the store has not is no reason to fail.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// stored returns the bytes that the store has of the file name.
func (r *run) stored(name string) ([]byte, error) {
	if name == settingsName {
		d, err := r.tx.Document(store.SettingsKind, "")
		return d.Data, err
	}
	dir, file, _ := strings.Cut(name, "/")
	id, _ := strings.CutSuffix(file, ".json")
	if dir == "revisions" && content.CheckID(content.RefRevision, id) == nil {
		rev, err := r.tx.Revision(id)
		return rev.Data, err
	}
	if kind, ok := kindOfDir[dir]; ok && content.CheckID(kind, id) == nil {
		d, err := r.tx.Document(kind, id)
		return d.Data, err
	}
	return nil, nil
}

// sorted returns es sorted by file, line, column, JSON Pointer and message, without duplicates, as the build sorts its
// problems.
func sorted(es content.Errors) content.Errors {
	es = slices.Clone(es)
	slices.SortFunc(es, func(a, b *content.Error) int {
		return cmp.Or(
			strings.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Column, b.Column),
			strings.Compare(a.Path, b.Path),
			strings.Compare(a.Msg, b.Msg),
		)
	})
	return slices.CompactFunc(es, func(a, b *content.Error) bool { return *a == *b })
}
