package build

import "github.com/WhiteRoomLab/whiteroom/internal/siteplan"

// resolveRedirects sets b.serverRedirects to the redirects that the servers serve (siteplan.Site.ResolveRedirects),
// given the pages and the outputs that b has planned. A redirect to an entry that is not published is left out, with a
// warning to b.warnings. The errors, such as those of mediaPath, go to p.
func (b *builder) resolveRedirects(mediaPath func(id string) (string, error), p *problems) {
	plan := &siteplan.Plan{
		Outputs: b.outputs, EntryPaths: b.entryPaths, TermPaths: b.termPaths, AuthorPaths: b.authorPaths,
	}
	b.serverRedirects = b.sp.ResolveRedirects(plan, mediaPath, b.warnings.report(), p.report())
}
