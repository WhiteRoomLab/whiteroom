package wpimport

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// EntryOptions is what the conversion of the entries (6.10.5) needs besides the WXR.
type EntryOptions struct {
	// Site is the one that Site returned, and that Terms, Authors and Media were given.
	Site *content.Site
	// Settings is the settings that the person who imports corrected, or nil: then there is no table of the page templates,
	// no default category and no SEO plugin.
	Settings *Settings
	// By is the ID of the account that creates the entries.
	By string
}

// EntryDoc is the documents of one entry: Entry is entries/<ID>.json, and Revision is revisions/<RevisionID>.json.
type EntryDoc struct {
	Post       int64 // the wp:post_id
	Entry      content.Entry
	RevisionID string
	Revision   content.Revision
}

// PlanEntries is the part of the plan (6.10.5) that is for the entries: it gives the ID of an entry to each item of a type
// that is imported (post, page, and the custom types of defs, the schema file as UnmarshalSchema reads it, or nil) that is
// not in the trash nor an automatic draft, and records it with the type and the language of the settings s, as AddRef
// does. It adds nothing to the report. Call it before Site, which finds the front page among the records. Entries reads
// the type and the language of each entry from the records, so a conversion of other languages (#57) changes this.
func (imp *Import) PlanEntries(s *Settings, defs *content.Site) error {
	if s == nil {
		return errors.New("the settings are needed to plan the entries")
	}
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		name := typeName(it.Type, defs)
		if name == "" || discardedStatus(it.Status) != "" {
			continue
		}
		id, err := imp.IDs.Assign(PostObject(it), content.RefEntry)
		if err != nil {
			return err
		}
		imp.AddRef(content.RefEntry, id, content.Ref{Type: name, Language: s.Site.Language})
	}
	return nil
}

// discardedStatus returns why an item with the status wp is not imported as it is: it is in the trash, or an automatic
// draft. It returns "" for the others.
func discardedStatus(wp string) string {
	switch wp {
	case "trash":
		return "it is in the trash"
	case "auto-draft":
		return "it is an auto-draft"
	}
	return ""
}

// Entries makes the entries (6.4) of the items of the WXR, each as the state and the revision of its content, and gives
// them to emit one at a time, in the order of the WXR, so that the caller can write each and let it go. The type and the
// language of an entry are those that PlanEntries recorded. What it leaves out or changes is recorded in the report.
//
// What is given is the caller's: Entries keeps nothing of it after emit returns. The body of an entry is converted by Body
// (6.10.2) just before the entry is given, so that what Entries holds is the WXR, the table, a little of each entry, and one
// body. If the entry has no excerpt, the text before the read more tag is its excerpt. The blocks that would make the JSON of
// the revision larger than content.MaxDocumentBytes, from the first that does not fit, are left out.
//
// An item that cannot be published, such as a post with no title, becomes a draft (6.10.1), as does every private item
// and every item with a password. An item in the trash and an automatic draft, and one whose date and whose modification
// time cannot be read, are not imported: they are taken out of the table and the references. site is the one that Site
// returned.
//
// If the settings name an SEO plugin, the entry has the SEO settings of that plugin's postmeta: the title, the description,
// the canonical URL, noindex and the image for sharing. The report counts the other postmeta of the plugin, which whiteroom
// has no setting for, by key, once each, at the channel. If the site had Yoast SEO, its primary category is the first
// category of an entry that has more, where the link of the item does not tell it.
//
// What the build checks across entries (6.7) is made to hold: a published page has a published parent, and a scheduled
// page a published or scheduled one, or else it is a draft with its parent; the published and scheduled entries of a
// language and a type (and under a parent, for pages) have different slugs; and the front page and the posts page of
// opts.Site are taken out of it, as Media does the logo and the icon, if they are not published entries. PathRef gives
// the slug and the parent of the pages that are published or scheduled.
//
// Call it once, after PlanEntries, Site, Terms, Authors and Media: it adds to the report each time. The error is for an
// argument that is wrong, an item that was not planned, a conversion that fails, ctx that is done, and an error of emit,
// which stops it. The report and the table are not changed by what is wrong with an argument or the plan.
func (imp *Import) Entries(ctx context.Context, opts EntryOptions, emit func(*EntryDoc) error) error {
	site := opts.Site
	if site == nil {
		return errors.New("the settings of the site are needed to make entries")
	}
	if err := content.CheckLanguage(site.DefaultLanguage); err != nil {
		return fmt.Errorf("the default language of the site: %w", err)
	}
	// LoadLocation gives UTC for "".
	if site.TimeZone == "" {
		return errors.New("the time zone of the site: it is empty")
	}
	zone, err := time.LoadLocation(site.TimeZone)
	if err != nil {
		return fmt.Errorf("the time zone of the site: %w", err)
	}
	if err := content.CheckID(content.RefUser, opts.By); err != nil {
		return fmt.Errorf("the account that creates the entries: %w", err)
	}
	if emit == nil {
		return errors.New("a function to take the entries is needed to make entries")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	c := &entryConv{imp: imp, site: site, opts: opts, zone: zone}
	items, discarded, err := c.collect()
	if err != nil {
		return err
	}
	imp.reportSkippedTypes(site)
	for _, d := range discarded {
		c.discard(d.it, d.why)
	}
	live := items[:0]
	for _, ei := range items {
		if c.begin(ei) {
			live = append(live, ei)
		}
	}
	for _, ei := range live {
		c.convert(ei)
	}
	c.setParents(live)
	c.reportGroups()
	c.reportSEO()
	for _, ei := range live {
		c.tidy(ei)
	}
	c.draftUnderUnpublished(live)
	c.uniqueSlugs(live)
	c.leaveOutFrontPages(live)
	c.recordPaths(live)
	return c.emitAll(ctx, live, emit)
}

// entryConv makes the entries of the items of the WXR.
type entryConv struct {
	imp  *Import
	site *content.Site
	opts EntryOptions
	zone *time.Location // the time zone of the site
	// takeOutUnit is what tidy takes a unit out of a revision with: nil for clearUnit, which a test replaces.
	takeOutUnit func(rev *content.Revision, unit string) bool

	// groups are the items that something is not imported of, counted by the steps of convert and recorded after them.
	groups    map[groupKey]*itemGroup
	groupKeys []groupKey // in the order of the first item of each
	// seoSkips counts the items that have each postmeta of the SEO plugin that is not imported, in the order of the plugin's
	// table; reportSEO records them. It is nil until an item has one.
	seoSkips []int
	// defaultCat is the default category of the settings, which defaultCategory finds when it is first needed.
	defaultCat struct {
		done, ok bool
		pick     termPick
	}
}

// entryItem is an item of the WXR that is being made into an entry.
type entryItem struct {
	it    *wxr.Item
	id    string
	revID string
	entry content.Entry // with the status draft: emitAll sets the status that status tells
	rev   content.Revision
	// status is the status that the entry gets, which the conversion only ever changes to draft. scheduledAt is the date of
	// publication of a scheduled one.
	status      content.Status
	scheduledAt string
	slugFirst   bool // WordPress had the slug: importSlug did not change it
}

// discardedItem is an item that is not imported because it is in the trash or an automatic draft.
type discardedItem struct {
	it  *wxr.Item
	why string
}

// collect finds the items that are imported as entries, with what the plan recorded of each, and the items that are not.
// It records nothing.
func (c *entryConv) collect() (items []*entryItem, discarded []discardedItem, err error) {
	for i := range c.imp.WXR.Items {
		it := &c.imp.WXR.Items[i]
		if typeName(it.Type, c.site) == "" {
			continue
		}
		if why := discardedStatus(it.Status); why != "" {
			discarded = append(discarded, discardedItem{it: it, why: why})
			continue
		}
		id, ref, ok := c.planned(it)
		if !ok {
			return nil, nil, fmt.Errorf("item %d (%s) is not planned as an entry; call PlanEntries before Site", it.ID, typeText(it.Type))
		}
		items = append(items, &entryItem{
			it: it, id: id, revID: c.imp.IDs.NewID(content.RefRevision, PostObject(it).key()),
			entry: content.Entry{Type: ref.Type, Language: ref.Language},
		})
	}
	return items, discarded, nil
}

// planned returns the ID and the record of the entry that the plan made of it, if it is one that the site has.
func (c *entryConv) planned(it *wxr.Item) (id string, ref content.Ref, ok bool) {
	kind, id, ok := c.imp.IDs.Lookup(PostObject(it))
	if !ok || kind != content.RefEntry {
		return "", content.Ref{}, false
	}
	ref, ok = c.imp.Ref(content.RefEntry, id)
	if !ok || c.site.Type(ref.Type) == nil || !slices.Contains(c.site.Languages, ref.Language) {
		return "", content.Ref{}, false
	}
	return id, ref, true
}

// discard leaves the item it, which is not imported, out of the table and the references, and says why in the report.
func (c *entryConv) discard(it *wxr.Item, why string) {
	if kind, id, ok := c.imp.IDs.Lookup(PostObject(it)); ok && kind == content.RefEntry {
		c.imp.IDs.Drop(PostObject(it))
		c.imp.DropRef(id)
	}
	c.imp.Report.Add(Entry{Kind: Skipped, Source: ItemSource(c.imp.File, it), Message: "the entry is not imported: " + why})
}

// entryNote adds an entry of the report about the item ei, at pointer in the document of the state of the entry.
func (c *entryConv) entryNote(kind Kind, ei *entryItem, pointer, format string, args ...any) {
	c.imp.entryAt(kind, ItemSource(c.imp.File, ei.it), documentTarget(content.RefEntry, ei.id, pointer), format, args...)
}

// revisionNote adds an entry of the report about the item ei, at pointer in the document of the revision.
func (c *entryConv) revisionNote(kind Kind, ei *entryItem, pointer, format string, args ...any) {
	target := Target{File: "revisions/" + ei.revID + ".json", Pointer: pointer}
	c.imp.entryAt(kind, ItemSource(c.imp.File, ei.it), target, format, args...)
}

// begin makes the state of the entry of ei as a draft, and reports whether the item is imported. It is not if its time
// cannot be read.
func (c *entryConv) begin(ei *entryItem) bool {
	it := ei.it
	createdAt, ok := postTime(it.Date, it.DateGMT, c.zone)
	fromModified := !ok
	if !ok {
		if createdAt, ok = postTime(it.Modified, it.ModifiedGMT, c.zone); !ok {
			c.discard(it, fmt.Sprintf("its post_date %q and post_modified %q cannot be read", it.Date, it.Modified))
			return false
		}
	}
	ei.entry.ID = ei.id
	ei.entry.Number = int(it.ID)
	ei.entry.TranslationGroup = c.imp.IDs.NewID(content.RefTranslationGroup, PostObject(it).key())
	ei.entry.Status = content.StatusDraft
	ei.entry.CreatedAt = createdAt
	ei.entry.CreatedBy = c.opts.By
	// The entry can only fail here by a mistake of this conversion.
	if err := ei.entry.Validate(c.site); err != nil {
		c.discard(it, errorText(err))
		return false
	}
	if fromModified {
		c.entryNote(Changed, ei, "/createdAt", "the post_date %q cannot be read; the entry was created at its post_modified here", it.Date)
	}
	return true
}

// convert makes the items of the revision of ei that do not refer to others, and decides its status.
func (c *entryConv) convert(ei *entryItem) {
	c.setStatus(ei)
	c.setDate(ei)
	c.setTitle(ei)
	c.setSlug(ei)
	c.setExcerpt(ei)
	c.setTerms(ei)
	c.setAuthor(ei)
	c.setFeaturedImage(ei)
	c.setSticky(ei)
	c.setOrder(ei)
	c.setTemplate(ei)
	c.setSEO(ei)
}

// setStatus decides the status of ei from wp:status and wp:post_password: a private or protected item is always a draft.
func (c *entryConv) setStatus(ei *entryItem) {
	it := ei.it
	status := content.StatusDraft
	switch it.Status {
	case "publish":
		status = content.StatusPublished
	case "future":
		status = content.StatusScheduled
	case "draft", "private":
	default:
		c.entryNote(Changed, ei, "/status", "the status %q is a draft here", it.Status)
	}
	private, protected := it.Status == "private", it.Password != ""
	switch {
	case private && protected:
		c.entryNote(Review, ei, "/status", "the entry was private and protected by a password; it is a draft here, and the password is not imported")
	case private:
		c.entryNote(Review, ei, "/status", "the entry was private; it is a draft here")
	case protected && status != content.StatusDraft:
		c.entryNote(Review, ei, "/status", "the entry was protected by a password; it is a draft here, and the password is not imported")
	case protected:
		c.entryNote(Review, ei, "/status", "the password of the entry is not imported")
	}
	if protected {
		status = content.StatusDraft
	}
	ei.status = status
}

// setDate decides the date of a post, and the date at which the entry is published if it is scheduled.
func (c *entryConv) setDate(ei *entryItem) {
	it := ei.it
	at, ok := postTime(it.Date, it.DateGMT, c.zone)
	// WordPress sets the date of a draft that has none, which is a post_date_gmt that is not a date, when it publishes it.
	_, gmtOK := parsePostDate(it.DateGMT)
	undated := (it.Status == "draft" || it.Status == "pending") && !gmtOK
	if ei.entry.Type == content.TypePost && ok && !undated {
		ei.rev.Date = at
	}
	if ei.status != content.StatusScheduled {
		return
	}
	if !ok {
		ei.status = content.StatusDraft
		c.entryNote(Review, ei, "/status", "the entry cannot be scheduled: its post_date %q cannot be read; it is a draft here", it.Date)
		return
	}
	ei.scheduledAt = at
}

// setTitle makes the title of ei of the characters of wp:title.
func (c *entryConv) setTitle(ei *entryItem) {
	text, lost, err := htmlText(ei.it.Title)
	if err != nil {
		c.revisionNote(Skipped, ei, "/title", "the title is not imported: it is too complex to read as HTML")
		return
	}
	title, replaced := oneLine(text)
	title = content.NormalizeLine(title)
	if len(title) > content.MaxTextBytes {
		c.revisionNote(Skipped, ei, "/title", "the title is not imported: it is too long: %d bytes (max %d)", len(title), content.MaxTextBytes)
		return
	}
	ei.rev.Title = title
	if len(lost) > 0 {
		c.revisionNote(Changed, ei, "/title", "%s", lostMessage(lost))
	}
	if replaced {
		c.revisionNote(Changed, ei, "/title", "line breaks and tabs in the title are spaces here")
	}
}

// setSlug makes the slug of ei of wp:post_name, as for a term. A published or scheduled entry needs one, so it gets
// entry-<wp:post_id> if it has none, and so does an entry of which nothing is left of it. A draft may have none.
func (c *entryConv) setSlug(ei *entryItem) {
	it := ei.it
	fallback := "entry-" + strconv.FormatInt(it.ID, 10)
	if it.Name == "" {
		if publishing(ei.status) {
			ei.rev.Slug = fallback
			c.revisionNote(Changed, ei, "/slug", "the entry has no slug; it is %q here", fallback)
		}
		return
	}
	slug, why := importSlug(it.Name)
	switch {
	case slug == "":
		slug = fallback
		c.revisionNote(Changed, ei, "/slug", "nothing is left of the slug %q; it is %q here", it.Name, slug)
	case len(why) > 0:
		c.revisionNote(Changed, ei, "/slug", "the slug %q is %q here: %s", it.Name, slug, strings.Join(why, "; "))
	default:
		ei.slugFirst = true
	}
	ei.rev.Slug = slug
}

// setExcerpt makes the excerpt of ei of the characters of the excerpt that the author wrote.
func (c *entryConv) setExcerpt(ei *entryItem) {
	if strings.TrimSpace(ei.it.Excerpt) == "" {
		return
	}
	text, lost, err := htmlText(ei.it.Excerpt)
	switch {
	case err != nil:
		c.revisionNote(Skipped, ei, "/excerpt", "the excerpt is not imported: it is too complex to read as HTML")
	case len(text) > content.MaxTextBytes:
		c.revisionNote(Skipped, ei, "/excerpt", "the excerpt is not imported: it is too long: %d bytes (max %d)", len(text), content.MaxTextBytes)
	default:
		ei.rev.Excerpt = text
		if len(lost) > 0 {
			c.revisionNote(Changed, ei, "/excerpt", "%s", lostMessage(lost))
		}
	}
}

// revisionOptions returns what Validate needs to check a revision of entry, as one to publish if publish.
func (c *entryConv) revisionOptions(entry *content.Entry, publish bool) content.RevisionOptions {
	return content.RevisionOptions{Site: c.site, Entry: entry, Publish: publish, Import: true, Lookup: c.imp.Ref}
}

// tidy makes the revision of ei pass the validation: it normalizes it, takes out what does not pass as a draft, and makes
// the entry a draft if the revision does not pass as one to publish.
func (c *entryConv) tidy(ei *entryItem) {
	ei.rev.Normalize(c.site.Type(ei.entry.Type))
	takeOut := clearUnit
	if c.takeOutUnit != nil {
		takeOut = c.takeOutUnit
	}
	// Nothing that convert makes should fail: this only guards the documents. A unit that is wrong again after it was taken
	// out ends the loop as one that cannot be taken out does.
	taken := make(map[string]bool)
	for {
		err := ei.rev.Validate(c.revisionOptions(&ei.entry, false))
		if err == nil {
			break
		}
		first := firstError(err)
		unit := revisionUnit(first.Path)
		if taken[unit] || !takeOut(&ei.rev, unit) {
			ei.rev = content.Revision{}
			c.revisionNote(Skipped, ei, "", "the content of the entry is not imported: %s", first.Error())
			break
		}
		taken[unit] = true
		c.revisionNote(Skipped, ei, unit, "the %s is not imported: %s", unitName(unit), first.Msg)
	}
	if !publishing(ei.status) {
		return
	}
	if err := ei.rev.Validate(c.revisionOptions(&ei.entry, true)); err != nil {
		ei.status = content.StatusDraft
		c.entryNote(Review, ei, "/status", "the entry cannot be published: %s; it is a draft here", errorText(err))
	}
}

// publishing reports whether an entry of the status s is, or is to be, published, so that its revision needs what a
// publication needs.
func publishing(s content.Status) bool {
	return s == content.StatusPublished || s == content.StatusScheduled
}

// firstError returns the first problem of err, which Validate returns.
func firstError(err error) *content.Error {
	var errs content.Errors
	if errors.As(err, &errs) && len(errs) > 0 {
		return errs[0]
	}
	return &content.Error{Msg: err.Error()}
}

// revisionUnit returns the unit of the revision that the problem at the JSON Pointer path is in, which is taken out
// whole: the first item of the pointer, such as /title, with the taxonomy, the key or the name after /terms, /seo and
// /fields. It is "" for a problem of the revision as a whole.
func revisionUnit(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch parts[0] {
	case "terms", "seo", "fields":
		if len(parts) > 1 {
			return "/" + parts[0] + "/" + parts[1]
		}
	}
	return "/" + parts[0]
}

// unitNames are the names that the report gives the units whose name is not that of their key.
var unitNames = map[string]string{
	"/seo/title":       "title for search engines",
	"/seo/description": "description for search engines",
	"/seo/canonical":   "canonical URL",
	"/seo/ogImage":     "image for sharing",
}

// unitName returns the name of a unit of the revision for the report: that of its key, if it has no other.
func unitName(unit string) string {
	if name, ok := unitNames[unit]; ok {
		return name
	}
	return strings.TrimPrefix(unit, "/")
}

// clearUnit takes the unit out of rev, and reports whether there was something to take out.
func clearUnit(rev *content.Revision, unit string) bool {
	var s *string
	switch unit {
	case "/title":
		s = &rev.Title
	case "/slug":
		s = &rev.Slug
	case "/excerpt":
		s = &rev.Excerpt
	case "/date":
		s = &rev.Date
	case "/author":
		s = &rev.Author
	case "/featuredImage":
		s = &rev.FeaturedImage
	case "/template":
		s = &rev.Template
	case "/parent":
		s = &rev.Parent
	case "/seo/title":
		s = &rev.SEO.Title
	case "/seo/description":
		s = &rev.SEO.Description
	case "/seo/canonical":
		s = &rev.SEO.Canonical
	case "/seo/ogImage":
		s = &rev.SEO.OGImage
	case "/sticky":
		was := rev.Sticky
		rev.Sticky = false
		return was
	case "/order":
		was := rev.Order != 0
		rev.Order = 0
		return was
	default:
		taxonomy, ok := strings.CutPrefix(unit, "/terms/")
		if !ok {
			return false
		}
		taxonomy = pointerUnescaper.Replace(taxonomy)
		if _, ok := rev.Terms[taxonomy]; !ok {
			return false
		}
		delete(rev.Terms, taxonomy)
		if len(rev.Terms) == 0 {
			rev.Terms = nil
		}
		return true
	}
	if *s == "" {
		return false
	}
	*s = ""
	return true
}

// emitAll gives the entries of items to emit, in their order, each after its body is made and it passed the validation.
func (c *entryConv) emitAll(ctx context.Context, items []*entryItem, emit func(*EntryDoc) error) error {
	for _, ei := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := ei.entry
		entry.Status = ei.status
		switch ei.status {
		case content.StatusPublished:
			entry.PublishedRevision = ei.revID
		case content.StatusScheduled:
			entry.ScheduledRevision, entry.ScheduledAt = ei.revID, ei.scheduledAt
		case content.StatusDraft, content.StatusArchived:
		}
		if err := entry.Validate(c.site); err != nil {
			return fmt.Errorf("the entry of item %d does not pass validation: %s", ei.it.ID, errorText(err))
		}
		made, err := c.setBody(ei)
		if err != nil {
			return err
		}
		if err := c.checkRevision(ei, &entry, made); err != nil {
			return err
		}
		if err := emit(&EntryDoc{Post: ei.it.ID, Entry: entry, RevisionID: ei.revID, Revision: ei.rev}); err != nil {
			return err
		}
		// What is given away is the caller's, the body most of all: the conversion does not hold on to it.
		ei.rev = content.Revision{}
	}
	return nil
}
