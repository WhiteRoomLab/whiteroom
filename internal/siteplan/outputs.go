package siteplan

import (
	"cmp"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// CheckOutputs checks outs and rs for duplicates (4.1), reporting the errors: two outputs with the same path, in the
// canonical form of froms, or with the same file; the file of an output in that of another as in a directory; and the
// from of a redirect without a query at the path of an output, compared as Redirect.Matches compares them (6.6.6),
// without a trailing "/". It takes outs in the order of compareOutputs and then rs (RankRedirect): each that conflicts
// with one before it gets the error, which names the first such one, so that each conflict is reported once.
func CheckOutputs(outs []Output, rs []Redirect, report Report) {
	sorted := slices.Clone(outs)
	slices.SortFunc(sorted, compareOutputs)
	paths := make(map[string]*Output) // by the path in the canonical form
	keys := make(map[string]*Output)  // by FromKey of the path
	files := make(map[string]*Output)
	dirs := make(map[string]*Output) // by each directory that a file is in
	for i := range sorted {
		o := &sorted[i]
		first, samePath := claim(paths, content.CanonicalFrom(o.Path), o)
		if samePath {
			report.addf(o.Src.File, o.Src.Pointer, "output path %q is also that of %s", o.Path, first.Src.What)
		}
		claim(keys, FromKey(o.Path), o)
		if o.File == "" {
			continue
		}
		// Already reported if the first output with the file has the same path.
		if f, sameFile := claim(files, o.File, o); sameFile && f != first {
			report.addf(o.Src.File, o.Src.Pointer, "output file %q is also that of %s", o.File, f.Src.What)
		}
		if d := dirs[o.File]; d != nil {
			report.addf(o.Src.File, o.Src.Pointer, "output file %q is also a directory of %q, the file of %s",
				o.File, d.File, d.Src.What)
		}
		parents := parentDirs(o.File)
		if j := slices.IndexFunc(parents, func(dir string) bool { return files[dir] != nil }); j >= 0 {
			report.addf(o.Src.File, o.Src.Pointer, "output file %q is in %q, which is the file of %s",
				o.File, parents[j], files[parents[j]].Src.What)
		}
		for _, dir := range parents {
			claim(dirs, dir, o)
		}
	}
	for _, r := range rs {
		if o := keys[FromKey(r.From)]; o != nil {
			report.addf(r.Src.File, r.Src.Pointer, "from %q is the output path %q of %s", r.From, o.Path, o.Src.What)
		}
	}
}

// compareOutputs orders outputs by rank, so that of two that conflict, the later one, which gets the error, comes from
// a document rather than from the core or the developers, and then by path, file and source.
func compareOutputs(a, b Output) int {
	return cmp.Or(
		cmp.Compare(a.Rank, b.Rank),
		strings.Compare(a.Path, b.Path),
		strings.Compare(a.File, b.File),
		strings.Compare(a.Src.File.Name, b.Src.File.Name),
		strings.Compare(a.Src.Pointer, b.Src.Pointer),
		strings.Compare(a.Src.What, b.Src.What),
	)
}

// parentDirs returns the directories that file, a path in the public directory, is in, from the top: "a" and "a/b"
// for "a/b/index.html".
func parentDirs(file string) []string {
	var dirs []string
	for i := range len(file) {
		if file[i] == '/' {
			dirs = append(dirs, file[:i])
		}
	}
	return dirs
}
