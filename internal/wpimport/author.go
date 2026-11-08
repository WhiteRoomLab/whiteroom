package wpimport

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const restUsersRoute = "/wp/v2/users"

// authorItem is a user of WordPress that is being made into an author profile.
type authorItem struct {
	login       string
	id          int64  // wp:author_id, or 0 if the WXR has none
	displayName string // as written, or "" if the WXR has no wp:author of the login
	email       string // wp:author_email, which is only compared with the names and is written nowhere
	source      Source
	user        *wprest.User // the same user in the REST API, or nil
	docID       string       // the ID of the profile
	slug        string       // of the profile
	slugFirst   bool         // the slug is made from that of the REST API, which WordPress had
}

// Authors makes the author profiles (6.6.3) of the authors of posts, in the default language of site, and returns them in
// the order of their IDs: the users of the WXR (wp:author, and the logins that only dc:creator names, as in WXR 1.0)
// that wrote a post that is not in the trash or an automatic draft. If the REST API gave the users, they are matched with
// the authors, and their slugs, bios and websites are used. A name that is an email address is not the name of a profile,
// and the report does not name a login that is one. What it leaves out or changes is recorded in the report.
//
// site is the one that Site returned, and Authors is called after Site and before the entries are converted, which find
// the profiles with Ref (6.10.5). The error is for what should not happen, such as a default language that is not a
// language code, or a REST API that is not that of the WXR. It is not meant to be called twice on one Import.
func (imp *Import) Authors(site *content.Site) ([]content.AuthorProfile, error) {
	if site == nil {
		return nil, errors.New("the settings of the site are needed to make author profiles")
	}
	if err := content.CheckLanguage(site.DefaultLanguage); err != nil {
		return nil, fmt.Errorf("the default language of the site: %w", err)
	}
	if err := imp.checkREST(); err != nil {
		return nil, err
	}
	cands := imp.authorCandidates()
	others, blocked := imp.matchUsers(cands)
	imp.reportUsers(others, blocked)

	wrote := imp.postAuthors()
	var items []*authorItem
	for _, it := range cands {
		if !wrote[it.login] {
			imp.Report.Add(Entry{Kind: Skipped, Source: it.source,
				Message: "the author has no posts; whiteroom makes author profiles only for the authors of posts"})
			continue
		}
		id, err := imp.IDs.Assign(UserObject(it.login), content.RefAuthor)
		if err != nil {
			return nil, err
		}
		it.docID = id
		items = append(items, it)
	}

	// The slugs are decided for all the authors before any profile is made, so that a slug that WordPress had is not taken
	// by one that the import made.
	claims := make([]slugClaim, len(items))
	for i, it := range items {
		imp.authorSlug(it)
		claims[i] = slugClaim{slug: it.slug, first: it.slugFirst}
	}
	for i, slug := range claimSlugs(claims) {
		if it := items[i]; slug != it.slug {
			imp.authorEntry(Changed, it, "/slug", "the slug %q is also that of another author; it is %q here", it.slug, slug)
			it.slug = slug
		}
	}
	var profiles []content.AuthorProfile
	for _, it := range items {
		if p, ok := imp.convertAuthor(site, it); ok {
			profiles = append(profiles, p)
		}
	}
	slices.SortFunc(profiles, func(a, b content.AuthorProfile) int { return strings.Compare(a.ID, b.ID) })
	return profiles, nil
}

// authorCandidates lists the users that the WXR names: the wp:author records in their order, then the logins that only
// dc:creator names, in the order of the first item that has them, at the place of that item.
func (imp *Import) authorCandidates() []*authorItem {
	var cands []*authorItem
	seen := make(map[string]bool)
	for i := range imp.WXR.Authors {
		a := &imp.WXR.Authors[i]
		seen[a.Login] = true
		cands = append(cands, &authorItem{
			login: a.Login, id: a.ID, displayName: a.DisplayName, email: a.Email, source: AuthorSource(imp.File, a),
		})
	}
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if it.Creator == "" || seen[it.Creator] {
			continue
		}
		seen[it.Creator] = true
		cands = append(cands, &authorItem{login: it.Creator, source: authorSource(imp.File, it.Pos, 0, it.Creator, "")})
	}
	return cands
}

// postAuthors returns the logins of the users that wrote a post that is not in the trash or an automatic draft.
func (imp *Import) postAuthors() map[string]bool {
	wrote := make(map[string]bool)
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if it.Type == "post" && it.Status != "trash" && it.Status != "auto-draft" && it.Creator != "" {
			wrote[it.Creator] = true
		}
	}
	return wrote
}

// matchUsers sets the user of each candidate that is a user of the REST API: the one with the same ID as the wp:author_id,
// or, for a candidate with no ID, the only one left whose slug is the one that the login makes. It returns the users that
// no candidate is, or, if the REST API did not give the users, the record of that.
func (imp *Import) matchUsers(cands []*authorItem) (others []*wprest.User, blocked *wprest.Unavailable) {
	rest := imp.REST
	if rest == nil {
		return nil, nil
	}
	for i := range rest.Unavailable {
		if rest.Unavailable[i].Route == restUsersRoute {
			return nil, &rest.Unavailable[i]
		}
	}
	users := rest.Users
	taken := make([]bool, len(users))
	byID := make(map[int64]int, len(users))
	for i := range users {
		if _, ok := byID[users[i].ID]; !ok && users[i].ID >= 1 {
			byID[users[i].ID] = i
		}
	}
	for _, c := range cands {
		if i, ok := byID[c.id]; ok && !taken[i] {
			c.user, taken[i] = &users[i], true
		}
	}

	type slugged struct{ first, n int } // n users that no candidate is have the slug, and first is the first of them
	var bySlug map[string]*slugged
	for _, c := range cands {
		if c.id >= 1 {
			continue
		}
		if bySlug == nil {
			bySlug = make(map[string]*slugged)
			for i := range users {
				if slug, err := wxr.DecodeSlug(users[i].Slug); err == nil && slug != "" && !taken[i] {
					if s := bySlug[slug]; s != nil {
						s.n++
					} else {
						bySlug[slug] = &slugged{first: i, n: 1}
					}
				}
			}
		}
		if s := bySlug[nicename(c.login)]; s != nil && s.n == 1 {
			c.user, taken[s.first], s.n = &users[s.first], true, 0
		}
	}
	for i := range users {
		if !taken[i] {
			others = append(others, &users[i])
		}
	}
	return others, nil
}

// reportUsers records what matchUsers found about the users of the REST API that is not about one author.
func (imp *Import) reportUsers(others []*wprest.User, blocked *wprest.Unavailable) {
	if blocked != nil {
		why := ""
		if text := unavailableText(*blocked); text != "" {
			why = " (" + text + ")"
		}
		imp.Report.Add(Entry{
			Kind:   Review,
			Source: RESTSource(imp.RESTFile, restUsersRoute),
			Message: "the REST API did not give the users" + why +
				"; the slugs of the authors are made from their logins, and they have no bios or websites",
		})
	}
	for _, u := range others {
		imp.Report.Add(Entry{
			Kind:    Skipped,
			Source:  RESTUserSource(imp.RESTFile, u),
			Message: fmt.Sprintf("user %d of the REST API is not an author in the WXR; it is not imported", u.ID),
		})
	}
}

// unavailableText tells the status and the error code of a route that the site did not give, such as
// "401 rest_user_cannot_view". The code is quoted if it is made of other than the characters that WordPress uses in one,
// so that the line of the report stays one line.
func unavailableText(u wprest.Unavailable) string {
	var parts []string
	if u.Status != 0 {
		parts = append(parts, strconv.Itoa(u.Status))
	}
	if code := u.Code; code != "" {
		if strings.ContainsFunc(code, func(c rune) bool { return !isCodeChar(c) }) {
			code = strconv.Quote(code)
		}
		parts = append(parts, code)
	}
	return strings.Join(parts, " ")
}

func isCodeChar(c rune) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-'
}

// authorEntry adds an entry about the author it, at pointer in its document.
func (imp *Import) authorEntry(kind Kind, it *authorItem, pointer, format string, args ...any) {
	imp.Report.Add(Entry{
		Kind:    kind,
		Source:  it.source,
		Message: fmt.Sprintf(format, args...),
		Target:  documentTarget(content.RefAuthor, it.docID, pointer),
	})
}

// convertAuthor makes the profile of it, and records it. It reports whether the profile is kept.
func (imp *Import) convertAuthor(site *content.Site, it *authorItem) (content.AuthorProfile, bool) {
	p := content.AuthorProfile{ID: it.docID, Name: imp.authorName(it), Slug: it.slug}
	if it.user != nil {
		p.Bio = imp.authorBio(site, it)
		p.Links = imp.authorLinks(it)
	}
	p.Normalize()
	imp.AddRef(content.RefAuthor, it.docID, content.Ref{})

	// Nothing that is made above should fail: this only guards the documents.
	if err := p.Validate(site, imp.Ref); err != nil {
		imp.IDs.Drop(UserObject(it.login))
		imp.DropRef(it.docID)
		imp.Report.Add(Entry{Kind: Skipped, Source: it.source, Message: "the author is not imported: " + errorText(err)})
		return content.AuthorProfile{}, false
	}
	return p, true
}

// authorName returns the name of the profile: the first of the display name of the WXR, the name of the REST API and the
// login that is not empty and is not an email address, which is something that hasEmailAddress finds or the email
// address of the author in the WXR. If there is none, it is the slug, to be corrected. A name other than the display name
// is recorded, with the reason.
func (imp *Import) authorName(it *authorItem) string {
	texts := []string{decodeRefs(it.displayName)}
	if it.user != nil {
		texts = append(texts, decodeRefs(it.user.Name))
	}
	texts = append(texts, it.login)
	displayName := ""
	for i, text := range texts {
		name, replaced := oneLine(text)
		if i == 0 {
			displayName = name
		}
		if name == "" || hasEmailAddress(name) || isAuthorEmail(name, it.email) {
			continue
		}
		switch {
		case i == 0:
		case displayName == "":
			imp.authorEntry(Changed, it, "/name", "the display name is empty; the name is %q here", name)
		default:
			imp.authorEntry(Changed, it, "/name", "the display name is an email address; the name is %q here", name)
		}
		if replaced {
			imp.authorEntry(Changed, it, "/name", "line breaks and tabs in the name are spaces here")
		}
		return name
	}
	imp.authorEntry(Review, it, "/name", "the names of the author are empty or email addresses; the name is the slug %q here; correct it", it.slug)
	return it.slug
}

// authorSlug decides the slug of the profile, before the slugs of the authors are made different: the slug of the REST
// API or, if it did not give one, the one that WordPress makes of the login, and author-<ID> if nothing is left of it.
func (imp *Import) authorSlug(it *authorItem) {
	var raw string
	fromLogin := it.user == nil || it.user.Slug == ""
	if fromLogin {
		raw = nicename(it.login)
	} else {
		raw = it.user.Slug
	}
	slug, why := importSlug(raw)
	it.slugFirst = !fromLogin && slug != ""
	switch {
	case slug == "":
		slug = "author"
		if it.id >= 1 {
			slug += "-" + strconv.FormatInt(it.id, 10)
		}
		if raw == "" {
			// Not the login, which can be an email address.
			imp.authorEntry(Changed, it, "/slug", "nothing is left of the slug that the login makes; it is %q here", slug)
		} else {
			imp.authorEntry(Changed, it, "/slug", "nothing is left of the slug %q; it is %q here", raw, slug)
		}
	case len(why) > 0:
		imp.authorEntry(Changed, it, "/slug", "the slug %q is %q here: %s", raw, slug, strings.Join(why, "; "))
	}
	if fromLogin {
		imp.authorEntry(Review, it, "/slug",
			"the slug %q is made from the login, as the REST API did not give it; the author page of WordPress may have had another URL", slug)
	}
	it.slug = slug
}

// authorBio returns the bio of the profile: the text of the profile that the REST API gave, in the default language.
func (imp *Import) authorBio(site *content.Site, it *authorItem) content.Localized {
	if it.user.Description == "" {
		return nil
	}
	pointer := "/bio/" + site.DefaultLanguage
	text, lost, err := htmlText(it.user.Description)
	switch {
	case err != nil:
		imp.authorEntry(Skipped, it, pointer, "the bio is not imported: it is too complex to read as HTML")
		return nil
	case len(text) > content.MaxTextBytes:
		imp.authorEntry(Skipped, it, pointer, "the bio is not imported: it is too long: %d bytes (max %d)", len(text), content.MaxTextBytes)
		return nil
	}
	if len(lost) > 0 {
		imp.authorEntry(Changed, it, pointer, "%s", lostMessage(lost))
	}
	if text == "" {
		return nil
	}
	return content.Localized{site.DefaultLanguage: text}
}

// authorLinks returns the links of the profile: the website that the REST API gave, if it is an http or https URL.
func (imp *Import) authorLinks(it *authorItem) []string {
	href := content.NormalizeHref(it.user.URL)
	if href == "" {
		return nil
	}
	if reason := webURLProblem(href); reason != "" {
		if hasEmailAddress(href) {
			// A mailto: address, or a password and a host.
			imp.authorEntry(Skipped, it, "/links", "the website is not imported: %s", reason)
		} else {
			imp.authorEntry(Skipped, it, "/links", "the website %q is not imported: %s", href, reason)
		}
		return nil
	}
	return []string{href}
}

// webURLProblem says why href, which content.NormalizeHref has cleaned, is not an http or https URL that a profile can
// link to, or returns "" if it is one.
func webURLProblem(href string) string {
	if err := content.CheckHref(href); err != nil {
		return err.Error()
	}
	if u, err := url.Parse(href); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "must be an http or https URL"
	}
	return ""
}

// hasEmailAddress reports whether s has something that looks like an email address, also in a longer text such as
// "John <john@example.com>": an "@" that follows a character other than white space and "@", and is followed by a run of
// characters other than white space and "@" that has a "." in it, but not as its first or last character.
func hasEmailAddress(s string) bool {
	for i, r := range s {
		if r != '@' || i == 0 {
			continue
		}
		if before, _ := utf8.DecodeLastRuneInString(s[:i]); unicode.IsSpace(before) || before == '@' {
			continue
		}
		domain := s[i+1:]
		if end := strings.IndexFunc(domain, func(r rune) bool { return unicode.IsSpace(r) || r == '@' }); end >= 0 {
			domain = domain[:end]
		}
		for k := 1; k < len(domain)-1; k++ {
			if domain[k] == '.' {
				return true
			}
		}
	}
	return false
}

// isAuthorEmail reports whether s is the email address of an author, which the WXR gives as email, without regard to
// case and to the white space around them.
func isAuthorEmail(s, email string) bool {
	email = strings.TrimSpace(email)
	return email != "" && strings.EqualFold(strings.TrimSpace(s), email)
}

// hidesLogin reports whether the login of an author is not named in the report and the documents: it is an email address
// or the email address of the author.
func hidesLogin(login, email string) bool {
	return hasEmailAddress(login) || isAuthorEmail(login, email)
}
