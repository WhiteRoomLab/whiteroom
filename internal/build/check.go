package build

import "context"

// Check does what Build does before it writes a release, but for the paths of the media files, and writes nothing: it
// reads the settings, the documents and the theme of src, checks what is published (the documents, across the documents,
// the planned outputs), and resolves the redirects and checks the paths of the outputs and of the redirects for
// duplicates. It returns nil if Build would get past all that, the warnings aside, which Check does not return; else
// the errors that Build returns, content.Errors in the same order, or an error of src as it is, or ctx.Err().
//
// url is Options.URL: it replaces the URL of the site as Build does, so that the same src and url give the same errors; a
// url that does not pass is an error before anything is read.
//
// It does not open the originals of the media, so it does not find what Build finds in them (originals that are
// missing or of another size, images that cannot be read, the paths of the media files that clash with others). It
// does not render the pages either, so it does not find a template that cannot be executed. It needs neither an output
// directory nor a cache directory.
func Check(ctx context.Context, src Source, url string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkURL(url); err != nil {
		return err
	}
	b, err := load(src, url)
	if err != nil {
		return err
	}
	p := problems{read: b.revisionData}
	// The path of a medium is known only from its original; it does not matter for the checks.
	b.resolveRedirects(func(id string) (string, error) { return "/media/" + id + "/", nil }, &p)
	b.checkPaths(&p)
	if p.len() > 0 {
		return p.sorted()
	}
	return ctx.Err()
}
