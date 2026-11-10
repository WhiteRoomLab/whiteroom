package wpformat

import "strings"

const nextPage = "<!--nextpage-->"

// SplitPages returns the pages of a post, in the way of WordPress 7.1.4: it splits s at <!--nextpage-->, takes away a line
// break that is next to it, takes away the delimiters of the block nextpage, and does not split at the start. The number of
// the pages minus one is the number of the page breaks.
func SplitPages(s string) []string {
	return splitPages(s, nil)
}

func splitPages(s string, m *meter) []string {
	m.look(len(s))
	if !strings.Contains(s, nextPage) {
		return []string{s}
	}
	m.look(6 * len(s))
	s = strings.ReplaceAll(s, "\n"+nextPage+"\n", nextPage)
	s = strings.ReplaceAll(s, "\n"+nextPage, nextPage)
	s = strings.ReplaceAll(s, nextPage+"\n", nextPage)
	s = strings.ReplaceAll(s, "<!-- wp:nextpage -->", "")
	s = strings.ReplaceAll(s, "<!-- /wp:nextpage -->", "")
	s = strings.TrimPrefix(s, nextPage)
	m.look(len(s))
	return strings.Split(s, nextPage)
}
