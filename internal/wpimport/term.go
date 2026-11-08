package wpimport

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const maxMetaKeysShown = 10

// termItem is a term of the WXR that is being made into a document.
type termItem struct {
	t        *wxr.Term
	taxonomy string // the name in the site settings
	id       string
	name     string // oneLine of the name, which can be empty here
	replaced bool   // oneLine replaced a line break or a tab in the name
	slug     string // of the term
	term     content.Term
	dropped  bool
	visited  bool // in the search for a cycle of parents
	onPath   bool // on the path that the search is following

	slugFirst bool // WordPress had the slug: importSlug did not change it
}

// Terms makes the terms (6.6.1) of the WXR in the default language of site, and returns them in the order of their IDs:
// the categories and the tags, and the terms of the custom taxonomies that site defines. What it leaves out or changes
// is recorded in the report.
//
// site is the one that Site returned, and Terms is called after Site and before the entries are converted, which find
// the terms with Ref (6.10.5). The error is for what should not happen, such as a default language that is not a language
// code.
func (imp *Import) Terms(site *content.Site) ([]content.Term, error) {
	if site == nil {
		return nil, errors.New("the settings of the site are needed to make terms")
	}
	if err := content.CheckLanguage(site.DefaultLanguage); err != nil {
		return nil, fmt.Errorf("the default language of the site: %w", err)
	}
	imp.reportSkippedTaxonomies(site)
	var items []*termItem
	for i := range imp.WXR.Terms {
		t := &imp.WXR.Terms[i]
		name := taxonomyName(t.Taxonomy, site)
		if name == "" {
			continue
		}
		id, err := imp.IDs.Assign(TermObject(t), content.RefTerm)
		if err != nil {
			return nil, err
		}
		items = append(items, &termItem{t: t, taxonomy: name, id: id})
	}

	// A term that cannot be made is dropped before any is converted, so that no term gets it for a parent.
	live := items[:0]
	for _, it := range items {
		it.name, it.replaced = oneLine(decodeRefs(it.t.Name))
		if len(it.name) > content.MaxTextBytes {
			imp.dropTerm(it, fmt.Sprintf("its name is too long: %d bytes (max %d)", len(it.name), content.MaxTextBytes))
			continue
		}
		live = append(live, it)
	}
	items = live

	// The slugs are decided for all the terms before any is made, so that a slug that WordPress had is not taken by one
	// that the import made.
	claims := make([]slugClaim, len(items))
	for i, it := range items {
		imp.termSlug(it)
		claims[i] = slugClaim{scope: it.taxonomy + "\x00" + site.DefaultLanguage, slug: it.slug, first: it.slugFirst}
	}
	for i, slug := range claimSlugs(claims) {
		if it := items[i]; slug != it.slug {
			imp.termEntry(Changed, it, "/slug", "the slug %q is also that of another term of %s; it is %q here",
				it.slug, typeText(it.t.Taxonomy), slug)
			it.slug = slug
		}
	}
	for _, it := range items {
		imp.convertTerm(site, it)
	}
	imp.breakParentCycles(items)
	items = imp.validateTerms(site, items)

	slices.SortFunc(items, func(a, b *termItem) int { return strings.Compare(a.id, b.id) })
	var terms []content.Term
	for _, it := range items {
		terms = append(terms, it.term)
	}
	return terms, nil
}

// reportSkippedTaxonomies records, for each taxonomy of the WXR whose terms are not imported, that they are not, once
// at the first term. Taxonomies that other conversions handle are left out.
func (imp *Import) reportSkippedTaxonomies(site *content.Site) {
	type skipped struct {
		first *wxr.Term
		n     int
	}
	var order []string
	skips := make(map[string]*skipped)
	for i := range imp.WXR.Terms {
		t := &imp.WXR.Terms[i]
		if ownedTaxonomies[t.Taxonomy] || taxonomyName(t.Taxonomy, site) != "" {
			continue
		}
		sk := skips[t.Taxonomy]
		if sk == nil {
			sk = &skipped{first: t}
			skips[t.Taxonomy] = sk
			order = append(order, t.Taxonomy)
		}
		sk.n++
	}
	for _, wp := range order {
		sk := skips[wp]
		verb, pronoun := "are", "them"
		if sk.n == 1 {
			verb, pronoun = "is", "it"
		}
		msg := fmt.Sprintf("%s of the taxonomy %s %s not imported; ", count(sk.n, "term"), typeText(wp), verb)
		switch {
		case unusedTaxonomies[wp]:
			msg += "whiteroom does not use it"
		case wp == "tag":
			// tag is the name of the standard taxonomy, so a definition of it cannot import these terms.
			msg += "whiteroom uses the name tag for post_tag"
		default:
			msg += fmt.Sprintf("define %s in the schema file to import %s", typeText(wp), pronoun)
		}
		imp.Report.Add(Entry{Kind: Skipped, Source: taxonomySource(imp.File, sk.first), Message: msg})
	}
}

// dropTerm leaves the term out of the import, and says why in the report.
func (imp *Import) dropTerm(it *termItem, reason string) {
	it.dropped = true
	imp.IDs.Drop(TermObject(it.t))
	imp.DropRef(it.id)
	imp.Report.Add(Entry{Kind: Skipped, Source: TermSource(imp.File, it.t), Message: "the term is not imported: " + reason})
}

// termEntry adds an entry about the term it, at pointer in its document.
func (imp *Import) termEntry(kind Kind, it *termItem, pointer, format string, args ...any) {
	imp.Report.Add(Entry{
		Kind:    kind,
		Source:  TermSource(imp.File, it.t),
		Message: fmt.Sprintf(format, args...),
		Target:  documentTarget(content.RefTerm, it.id, pointer),
	})
}

// termSlug decides the slug of the term it, before the slugs of the terms are made different: the slug of WordPress, as
// importSlug makes it, and term-<ID> if nothing is left of it.
func (imp *Import) termSlug(it *termItem) {
	t := it.t
	slug, why := importSlug(t.Slug)
	it.slugFirst = slug != "" && len(why) == 0
	if slug == "" {
		slug = "term"
		if t.ID >= 1 {
			slug += "-" + strconv.FormatInt(t.ID, 10)
		}
		imp.termEntry(Changed, it, "/slug", "nothing is left of the slug %q; it is %q here", t.Slug, slug)
	} else if len(why) > 0 {
		imp.termEntry(Changed, it, "/slug", "the slug %q is %q here: %s", t.Slug, slug, strings.Join(why, "; "))
	}
	it.slug = slug
}

// convertTerm makes the term of it, and records it.
func (imp *Import) convertTerm(site *content.Site, it *termItem) {
	t := it.t
	slug := it.slug

	name := it.name
	switch {
	case name == "":
		name = slug
		imp.termEntry(Changed, it, "/name", "the name is empty; it is the slug %q here", slug)
	case it.replaced:
		imp.termEntry(Changed, it, "/name", "line breaks and tabs in the name are spaces here")
	}

	description := ""
	if t.Description != "" {
		text, lost, err := htmlText(t.Description)
		switch {
		case err != nil:
			imp.termEntry(Skipped, it, "/description", "the description is not imported: it is too complex to read as HTML")
		case len(text) > content.MaxTextBytes:
			imp.termEntry(Skipped, it, "/description", "the description is not imported: it is too long: %d bytes (max %d)", len(text), content.MaxTextBytes)
		default:
			description = text
			if len(lost) > 0 {
				imp.termEntry(Changed, it, "/description", "%s", lostMessage(lost))
			}
		}
	}

	parent := ""
	if t.Parent != "" {
		parent = imp.parentID(site, it)
	}

	if len(t.Meta) > 0 {
		imp.termEntry(Skipped, it, "", "%s", metaMessage(t.Meta))
	}

	it.term = content.Term{
		ID:               it.id,
		Taxonomy:         it.taxonomy,
		Language:         site.DefaultLanguage,
		TranslationGroup: imp.IDs.NewID(content.RefTranslationGroup, TermObject(t).key()),
		Name:             name,
		Slug:             slug,
		Parent:           parent,
		Description:      description,
	}
	it.term.Normalize()
	imp.AddRef(content.RefTerm, it.id, content.Ref{Type: it.taxonomy, Language: site.DefaultLanguage})
}

// metaMessage is the sentence of the report about the termmeta, which is not imported: the keys are listed once each.
func metaMessage(meta wxr.MetaList) string {
	var keys []string
	seen := make(map[string]bool)
	for _, m := range meta {
		if !seen[m.Key] {
			seen[m.Key] = true
			keys = append(keys, m.Key)
		}
	}
	listed := make([]string, 0, maxMetaKeysShown)
	for _, k := range keys[:min(len(keys), maxMetaKeysShown)] {
		listed = append(listed, strconv.Quote(k))
	}
	list := strings.Join(listed, ", ")
	if len(keys) > maxMetaKeysShown {
		list += fmt.Sprintf(", and %d more", len(keys)-maxMetaKeysShown)
	}
	verb := "are"
	if len(keys) == 1 {
		verb = "is"
	}
	return fmt.Sprintf("termmeta %s %s not imported; whiteroom terms have no custom fields", list, verb)
}

// parentID returns the ID of the parent of the term it, which the WXR names by its slug, or "" and an entry in the report
// if it is to have none.
func (imp *Import) parentID(site *content.Site, it *termItem) string {
	t := it.t
	if def := site.Taxonomy(it.taxonomy); def == nil || !def.Hierarchical {
		imp.termEntry(Changed, it, "/parent", "the parent %q is left out, as %s has no hierarchy", t.Parent, typeText(t.Taxonomy))
		return ""
	}
	parent := imp.WXR.TermBySlug(t.Taxonomy, t.Parent)
	if parent == nil {
		imp.termEntry(Changed, it, "/parent", "the parent %q is not in the WXR; the term is at the top", t.Parent)
		return ""
	}
	_, id, ok := imp.IDs.Lookup(TermObject(parent))
	switch {
	case !ok:
		imp.termEntry(Changed, it, "/parent", "the parent %q is not imported; the term is at the top", t.Parent)
	case parent == t:
		imp.termEntry(Changed, it, "/parent", "the term is its own parent; it is at the top")
	default:
		return id
	}
	return ""
}

// breakParentCycles takes the parent from one term of each cycle of parents: the one with the lowest ID.
func (imp *Import) breakParentCycles(items []*termItem) {
	byID := make(map[string]*termItem, len(items))
	for _, it := range items {
		byID[it.id] = it
	}
	for _, it := range items {
		if it.visited {
			continue
		}
		var path []*termItem
		cur := it
		for cur != nil && !cur.visited {
			cur.visited, cur.onPath = true, true
			path = append(path, cur)
			cur = byID[cur.term.Parent]
		}
		if cur != nil && cur.onPath {
			cycle := path[slices.Index(path, cur):]
			lowest := slices.MinFunc(cycle, func(a, b *termItem) int { return strings.Compare(a.id, b.id) })
			lowest.term.Parent = ""
			imp.termEntry(Changed, lowest, "/parent", "the parents make a cycle; the term is at the top")
		}
		for _, p := range path {
			p.onPath = false
		}
	}
}

// validateTerms drops the terms that do not pass Validate, takes them from the children that have them for the parent,
// and returns the terms that are left. Nothing that convertTerm makes should fail: this only guards the documents.
func (imp *Import) validateTerms(site *content.Site, items []*termItem) []*termItem {
	children := make(map[string][]*termItem)
	for _, it := range items {
		if it.term.Parent != "" {
			children[it.term.Parent] = append(children[it.term.Parent], it)
		}
	}
	queue := slices.Clone(items)
	for i := 0; i < len(queue); i++ {
		it := queue[i]
		if it.dropped {
			continue
		}
		err := it.term.Validate(site, imp.Ref)
		if err == nil {
			continue
		}
		imp.dropTerm(it, errorText(err))
		for _, child := range children[it.id] {
			if !child.dropped && child.term.Parent == it.id {
				imp.termEntry(Changed, child, "/parent", "the parent %q is not imported; the term is at the top", child.t.Parent)
				child.term.Parent = ""
				queue = append(queue, child)
			}
		}
	}
	return slices.DeleteFunc(items, func(it *termItem) bool { return it.dropped })
}

// errorText joins the problems of err, which Validate returns, into one line.
func errorText(err error) string {
	var errs content.Errors
	if !errors.As(err, &errs) {
		return err.Error()
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "; ")
}
