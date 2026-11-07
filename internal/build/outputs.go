package build

import "github.com/WhiteRoomLab/whiteroom/internal/siteplan"

// addOutput adds an output at path, with file, from src with rank, to b.outputs.
func (b *builder) addOutput(path, file string, src siteplan.Source, rank int) {
	b.outputs = append(b.outputs, siteplan.Output{Path: path, File: file, Src: src, Rank: rank})
}

// checkPaths checks the outputs and the redirects of b for duplicates (4.1), adding the errors to p.
func (b *builder) checkPaths(p *problems) {
	siteplan.CheckOutputs(b.outputs, b.serverRedirects, p.report())
}
