package build

import (
	"cmp"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// checkPaths checks the outputs and the redirects of b for duplicates (4.1), adding the errors to p.
func (b *builder) checkPaths(p *problems) {
	checkOutputs(b.outputs, b.serverRedirects, p)
}

// checkOutputs checks outs and rs for duplicates (4.1), adding the errors to p: two outputs with the same path, in the
// canonical form of froms, or with the same file; the file of an output in that of another as in a directory; and the
// from of a redirect without a query at the path of an output, compared as Redirect.Matches compares them (6.6.6),
// without a trailing "/". It takes outs in the order of compareOutputs and then rs (rankRedirect): each that conflicts
// with one before it gets the error, which names the first such one, so that each conflict is reported once.
func checkOutputs(outs []output, rs []redirect, p *problems) {
	sorted := slices.Clone(outs)
	slices.SortFunc(sorted, compareOutputs)
	paths := make(map[string]*output) // by the path in the canonical form
	keys := make(map[string]*output)  // by fromKey of the path
	files := make(map[string]*output)
	dirs := make(map[string]*output) // by each directory that a file is in
	for i := range sorted {
		o := &sorted[i]
		first, samePath := claim(paths, content.CanonicalFrom(o.path), o)
		if samePath {
			p.add(o.src.file, o.src.pointer, "output path %q is also that of %s", o.path, first.src.what)
		}
		claim(keys, fromKey(o.path), o)
		if o.file == "" {
			continue
		}
		// Already reported if the first output with the file has the same path.
		if f, sameFile := claim(files, o.file, o); sameFile && f != first {
			p.add(o.src.file, o.src.pointer, "output file %q is also that of %s", o.file, f.src.what)
		}
		if d := dirs[o.file]; d != nil {
			p.add(o.src.file, o.src.pointer, "output file %q is also a directory of %q, the file of %s",
				o.file, d.file, d.src.what)
		}
		parents := parentDirs(o.file)
		if j := slices.IndexFunc(parents, func(dir string) bool { return files[dir] != nil }); j >= 0 {
			p.add(o.src.file, o.src.pointer, "output file %q is in %q, which is the file of %s",
				o.file, parents[j], files[parents[j]].src.what)
		}
		for _, dir := range parents {
			claim(dirs, dir, o)
		}
	}
	for _, r := range rs {
		if o := keys[fromKey(r.from)]; o != nil {
			p.add(r.src.file, r.src.pointer, "from %q is the output path %q of %s", r.from, o.path, o.src.what)
		}
	}
}

// compareOutputs orders outputs by rank, so that of two that conflict, the later one, which gets the error, comes from
// a document rather than from the core or the developers, and then by path, file and source.
func compareOutputs(a, b output) int {
	return cmp.Or(
		cmp.Compare(a.rank, b.rank),
		strings.Compare(a.path, b.path),
		strings.Compare(a.file, b.file),
		strings.Compare(a.src.file.Name, b.src.file.Name),
		strings.Compare(a.src.pointer, b.src.pointer),
		strings.Compare(a.src.what, b.src.what),
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
