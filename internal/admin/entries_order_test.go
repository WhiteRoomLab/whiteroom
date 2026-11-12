package admin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// checkOrder checks that got, the IDs of the rows of a list, are in the order of key: the entries that have one first, by it
// in the direction that desc says, then those that have none, and entries that are alike in the order of their IDs.
func checkOrder(t *testing.T, what string, got []string, facts []entryFact, key func(x entryFact) (time.Time, bool), desc bool) {
	t.Helper()
	byID := make(map[string]entryFact, len(facts))
	for _, x := range facts {
		byID[x.id] = x
	}
	for i := 1; i < len(got); i++ {
		x, y := byID[got[i-1]], byID[got[i]]
		kx, hx := key(x)
		ky, hy := key(y)
		c := 0
		switch {
		case hx && !hy:
			c = -1
		case !hx && hy:
			c = 1
		case hx && hy:
			if c = kx.Compare(ky); desc {
				c = -c
			}
		}
		if c > 0 || c == 0 && x.id > y.id {
			t.Errorf("%s: %s is before %s, which is out of order (desc: %v)", what, x.id, y.id, desc)
			return
		}
	}
}

// reversed returns a copy of ids in the opposite order.
func reversed(ids []string) []string {
	out := slices.Clone(ids)
	slices.Reverse(out)
	return out
}

// publishedDate is the date of the published revision of an entry, and whether it has one.
func publishedDate(t *testing.T, x entryFact) (time.Time, bool) {
	t.Helper()
	if x.published == nil || x.published.Date == "" {
		return time.Time{}, false
	}
	d, err := time.Parse(time.RFC3339, x.published.Date)
	if err != nil {
		t.Fatal(err)
	}
	return d, true
}

// TestEntryListOrder shows the order of a list: by number by default and the largest first, by the other times with the
// entries that have none last whatever the direction, and entries that are alike in the order of their IDs.
func TestEntryListOrder(t *testing.T) {
	a, f := newEntryFixture(t)
	facts := a.entryFacts(t)
	all := ids(facts, func(entryFact) bool { return true })
	number := func(x entryFact) (time.Time, bool) { return time.Unix(int64(x.number), 0), true }
	createdAt := func(x entryFact) (time.Time, bool) { return x.createdAt, true }
	date := func(x entryFact) (time.Time, bool) { return publishedDate(t, x) }
	scheduledAt := func(x entryFact) (time.Time, bool) { return x.scheduledAt, x.hasScheduled }
	dated := ids(facts, func(x entryFact) bool { _, ok := date(x); return ok })
	undated := ids(facts, func(x entryFact) bool { _, ok := date(x); return !ok })
	if len(dated) != 26 || len(undated) != 19 {
		t.Fatalf("%d entries have a published date and %d have none, want 26 and 19", len(dated), len(undated))
	}

	rows := map[string][]string{}
	for _, tt := range []struct {
		query string
		key   func(x entryFact) (time.Time, bool)
		desc  bool
	}{
		{"", number, true},
		{"?sort=number", number, true},
		{"?order=desc", number, true},
		{"?sort=number&order=desc", number, true},
		{"?order=asc", number, false},
		{"?sort=number&order=asc", number, false},
		{"?sort=createdAt", createdAt, true},
		{"?sort=createdAt&order=desc", createdAt, true},
		{"?sort=createdAt&order=asc", createdAt, false},
		{"?sort=date", date, true},
		{"?sort=date&order=asc", date, false},
		{"?sort=scheduledAt", scheduledAt, true},
		{"?sort=scheduledAt&order=asc", scheduledAt, false},
	} {
		sep := "?"
		if tt.query != "" {
			sep = "&"
		}
		got, list := a.entryList(t, "viewer", "/api/entries"+tt.query+sep+"limit=100")
		if !slices.Equal(slices.Sorted(slices.Values(got)), all) || list.Total != len(all) {
			t.Errorf("GET /api/entries%s has %d rows of %d, want every entry once", tt.query, len(got), list.Total)
		}
		checkOrder(t, "GET /api/entries"+tt.query, got, facts, tt.key, tt.desc)
		rows[tt.query] = got
	}

	// The default is the number, the largest first; and the other direction is the exact opposite.
	if want := byNumber(facts, func(entryFact) bool { return true }); !slices.Equal(rows[""], want) {
		t.Errorf("GET /api/entries lists %v, want the entries by number, the largest first: %v", rows[""], want)
	}
	if got, want := rows["?order=asc"], reversed(rows[""]); !slices.Equal(got, want) {
		t.Errorf("GET /api/entries?order=asc lists %v, want the opposite of the default, %v", got, want)
	}
	// The newest is first by the time of making; those made at the same time are in the order of their IDs.
	if got := rows["?sort=createdAt"]; got[0] != f.authored {
		t.Errorf("by createdAt, the newest first, %s is first, want the entry that the author made last, %s", got[0], f.authored)
	}
	for _, query := range []string{"?sort=createdAt", "?sort=createdAt&order=asc"} {
		if at := slices.Index(rows[query], f.promoted); rows[query][at+1] != f.later {
			t.Errorf("GET /api/entries%s: %s is not just before %s, which was made at the same time", query, f.promoted, f.later)
		}
	}
	// Those without a date come after those that have one, in the order of their IDs, whatever the direction; the two that
	// have the same date are in the order of their IDs too.
	for _, query := range []string{"?sort=date", "?sort=date&order=asc"} {
		got := rows[query]
		if !slices.Equal(got[len(dated):], undated) {
			t.Errorf("GET /api/entries%s lists %v after those that have a date, want %v", query, got[len(dated):], undated)
		}
		if at := slices.Index(got, f.promoted); got[at+1] != f.later {
			t.Errorf("GET /api/entries%s: %s is not just before %s, which has the same date", query, f.promoted, f.later)
		}
	}
	if got := rows["?sort=date&order=asc"]; got[0] != "ent_01JD0000000000000000000001" {
		t.Errorf("by date, the oldest first, %s is first, want the post of 2026-10-01", got[0])
	}
	if got := rows["?sort=date"]; got[0] != f.promoted || got[1] != f.later || got[2] != "ent_01JD0000000000000000000022" {
		t.Errorf("by date, the newest first, %v come first, want the two of 2026-11-01 and then the post of 2026-10-30", got[:3])
	}
	// The scheduled ones: by the time, then the rest in the order of their IDs.
	rest := ids(facts, func(x entryFact) bool { return !x.hasScheduled })
	if got := rows["?sort=scheduledAt&order=asc"]; !slices.Equal(got, slices.Concat([]string{f.soon, f.afterwards}, rest)) {
		t.Errorf("by scheduledAt, the earliest first, the entries are %v, want the two scheduled and the %d others in the order of their IDs", got, len(rest))
	}
	if got := rows["?sort=scheduledAt"]; !slices.Equal(got, slices.Concat([]string{f.afterwards, f.soon}, rest)) {
		t.Errorf("by scheduledAt, the latest first, the entries are %v, want the two scheduled, the later first, and the %d others in the order of their IDs", got, len(rest))
	}
}

// TestEntryListPages shows the pages of a list: the total is that after the filters, whatever the page, and the pages together
// are the whole list, in order, without a row twice.
func TestEntryListPages(t *testing.T) {
	a, _ := newEntryFixture(t)
	facts := a.entryFacts(t)
	all := byNumber(facts, func(entryFact) bool { return true })
	drafts := byNumber(facts, func(x entryFact) bool { return x.status == "draft" })

	for _, tt := range []struct {
		query         string
		want          []string
		total         int
		offset, limit int
	}{
		{"", all, len(all), 0, defaultPage},
		{"?limit=10", all[:10], len(all), 0, 10},
		{"?limit=10&offset=10", all[10:20], len(all), 10, 10},
		{"?offset=10&limit=10", all[10:20], len(all), 10, 10},
		{"?limit=10&offset=40", all[40:], len(all), 40, 10},
		{"?limit=10&offset=45", nil, len(all), 45, 10},
		{"?limit=10&offset=1000", nil, len(all), 1000, 10},
		{"?offset=2147483647", nil, len(all), 2147483647, defaultPage},
		{"?limit=100", all, len(all), 0, 100},
		{"?limit=1", all[:1], len(all), 0, 1},
		{"?status=draft&limit=3&offset=2", drafts[2:5], len(drafts), 2, 3},
		{"?status=draft&offset=" + strconv.Itoa(len(drafts)-1), drafts[len(drafts)-1:], len(drafts), len(drafts) - 1, defaultPage},
		{"?status=draft&order=asc&limit=2", reversed(drafts)[:2], len(drafts), 0, 2},
	} {
		got, list := a.entryList(t, "viewer", "/api/entries"+tt.query)
		if !slices.Equal(got, slices.Concat(tt.want)) || list.Total != tt.total || list.Offset != tt.offset || list.Limit != tt.limit {
			t.Errorf("GET /api/entries%s: %v of %d at %d, limit %d, want %v of %d at %d, limit %d",
				tt.query, got, list.Total, list.Offset, list.Limit, tt.want, tt.total, tt.offset, tt.limit)
		}
	}

	// Page after page, the whole list once.
	var walked []string
	for offset := 0; ; offset += 7 {
		got, list := a.entryList(t, "viewer", "/api/entries?limit=7&offset="+strconv.Itoa(offset))
		if list.Total != len(all) {
			t.Fatalf("the page at %d has the total %d, want %d", offset, list.Total, len(all))
		}
		if len(got) == 0 {
			break
		}
		walked = append(walked, got...)
	}
	if !slices.Equal(walked, all) {
		t.Errorf("the pages of 7 together are %v, want the whole list %v", walked, all)
	}
}

// TestEntryListRows shows what a row has: the state of the entry as the store has it, the last revision with its title, and
// the slug and the date of the published revision, and nothing of what an entry does not have.
func TestEntryListRows(t *testing.T) {
	a, f := newEntryFixture(t)
	facts := a.entryFacts(t)
	b := body(t, "GET /api/entries", a.as("viewer").get("/api/entries?limit=100"), http.StatusOK)
	for _, null := range []string{`"latestRevision":null`, `"published":null`, `"title":null`, `"date":null`} {
		if strings.Contains(b, null) {
			t.Errorf("the list has %s, which is left out when there is nothing", null)
		}
	}
	got, list := a.entryList(t, "viewer", "/api/entries?limit=100")
	byID := make(map[string]entryFact, len(facts))
	for _, x := range facts {
		byID[x.id] = x
	}
	titled := 0
	for i, id := range got {
		x, row := byID[id], list.Items[i]
		doc := a.stored(t, content.RefEntry, id)
		if row.Version != versionOf(doc.Version) || string(row.Document) != strings.TrimSpace(string(doc.Data)) {
			t.Errorf("the row of %s has the version %+v and the document\n%s\nwant those of the store", id, row.Version, row.Document)
		}
		switch r := row.LatestRevision; {
		case x.last == nil && r != nil:
			t.Errorf("the row of %s has the last revision %+v, but the entry has none", id, r)
		case x.last != nil && r == nil:
			t.Errorf("the row of %s has no last revision, want %s", id, x.last.ID)
		case x.last != nil:
			if r.ID != x.last.ID || !r.SavedAt.Equal(x.last.SavedAt) || r.SavedBy != x.last.SavedBy {
				t.Errorf("the last revision in the row of %s is %+v, want %+v", id, r, x.last)
			}
			if (r.Title == nil) != (x.lastDoc.Title == "") || r.Title != nil && *r.Title != x.lastDoc.Title {
				t.Errorf("the title in the row of %s is %v, want %q", id, r.Title, x.lastDoc.Title)
			}
			if r.Title != nil {
				titled++
			}
		}
		switch p := row.Published; {
		case x.published == nil && p != nil:
			t.Errorf("the row of %s has the published %+v, but the entry has none", id, p)
		case x.published != nil && p == nil:
			t.Errorf("the row of %s has no published, want the slug %q", id, x.published.Slug)
		case x.published != nil:
			if p.Slug != x.published.Slug || (p.Date == nil) != (x.published.Date == "") || p.Date != nil && *p.Date != x.published.Date {
				t.Errorf("the published in the row of %s is %+v, want the slug %q and the date %q", id, p, x.published.Slug, x.published.Date)
			}
		}
	}

	// A few that show it: the title is that of the last revision and the slug is that of the published one, which differ.
	rows := map[string]entryRowBody{}
	for i, id := range got {
		rows[id] = list.Items[i]
	}
	if r := rows[f.promoted]; r.LatestRevision == nil || r.LatestRevision.Title == nil || *r.LatestRevision.Title != "特集をはずした下書き" ||
		r.Published == nil || r.Published.Slug != "promoted" || r.Published.Date == nil || *r.Published.Date != "2026-11-01T10:00:00+09:00" {
		t.Errorf("the row of the entry that is published and has a draft is %+v, want the title of the draft and the slug and the date of the published", r)
	}
	if r := rows[f.untitled]; r.LatestRevision == nil || r.LatestRevision.Title != nil || r.Published != nil {
		t.Errorf("the row of the entry whose revision has no title is %+v, want the last revision without a title", r)
	}
	if r := rows[f.drafted]; r.Published != nil || r.LatestRevision == nil || r.LatestRevision.SavedBy != a.people["editor"].account.ID {
		t.Errorf("the row of a draft is %+v, want a last revision saved by the editor, and nothing published", r)
	}
	if r := rows["ent_01JC0000000000000000000003"]; r.LatestRevision != nil || r.Published != nil {
		t.Errorf("the row of the draft without a revision is %+v, want neither", r)
	}
	if r := rows["ent_01JB8W2N5KD3F6H9J1M4P7R2T5"]; r.Published == nil || r.Published.Slug != "about" || r.Published.Date != nil {
		t.Errorf("the row of a published page is %+v, want the slug and no date", r)
	}
	if titled == 0 {
		t.Error("no row has a title")
	}
}

// TestEntryListTerms shows what the filter by term keeps: the entries that have the term in their published revision or in
// their last one, and not those whose last revision only mentions it elsewhere.
func TestEntryListTerms(t *testing.T) {
	a, f := newEntryFixture(t)
	facts := a.entryFacts(t)
	feature := "?term=" + f.feature

	// The three ways, and the one that is not: the last one the biggest number.
	got, list := a.entryList(t, "viewer", "/api/entries"+feature)
	if want := []string{f.drafted, f.later, f.promoted}; !slices.Equal(got, want) || list.Total != 3 {
		t.Errorf("GET /api/entries%s lists %v of %d, want %v: the draft that has the term, the entry whose last revision has it, and the entry whose published revision has it; not %s, which only mentions it in a text",
			feature, got, list.Total, want, f.linked)
	}
	for _, tt := range []struct {
		query string
		want  []string
	}{
		{feature + "&status=published", []string{f.later, f.promoted}},
		{feature + "&status=draft", []string{f.drafted}},
		{feature + "&status=archived,scheduled", nil},
		{feature + "&sort=date&order=asc", []string{f.promoted, f.later, f.drafted}},
		{feature + "&type=page", nil},
		{feature + "&language=en", nil},
		{feature + "&limit=1&offset=1", []string{f.later}},
	} {
		got, list := a.entryList(t, "viewer", "/api/entries"+tt.query)
		wantTotal := len(tt.want)
		if strings.Contains(tt.query, "limit=1") {
			wantTotal = 3
		}
		if !slices.Equal(got, slices.Concat(tt.want)) || list.Total != wantTotal {
			t.Errorf("GET /api/entries%s lists %v of %d, want %v of %d", tt.query, got, list.Total, tt.want, wantTotal)
		}
	}

	// Every term of the site, by the facts that the store has.
	terms := a.idsWhere(t, content.RefTerm, everyDocument)
	if len(terms) != 6 {
		t.Fatalf("the site has %d terms, want the 5 of the sample and the new one", len(terms))
	}
	used := 0
	for _, term := range terms {
		want := byNumber(facts, func(x entryFact) bool { return x.hasTermIn(term) })
		got, list := a.entryList(t, "editor", "/api/entries?limit=100&term="+term)
		if !slices.Equal(got, want) || list.Total != len(want) {
			t.Errorf("GET /api/entries?term=%s lists %v of %d, want %v", term, got, list.Total, want)
		}
		if len(want) > 0 {
			used++
		}
	}
	if used != 6 {
		t.Errorf("%d terms are used by an entry, want all 6", used)
	}
	if got, list := a.entryList(t, "viewer", "/api/entries?term="+missingTerm); len(got) != 0 || list.Total != 0 {
		t.Errorf("a term that nothing has lists %v of %d, want none", got, list.Total)
	}
}

// TestEntryListQueries shows the queries that a list refuses with 400: unknown names, a name twice, values of another form,
// and numbers out of range.
func TestEntryListQueries(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("viewer")
	bad := []string{
		"?limit=0", "?limit=101", "?limit=abc", "?limit=-1", "?limit=%2B1", "?limit=", "?limit=1&limit=2", "?limit=1.5", "?limit=99999999999999999999",
		"?offset=-1", "?offset=x", "?offset=", "?offset=1&offset=2", "?offset=2147483648", "?offset=%2B1",
		"?foo=1", "?foo", "?%zz", "?offset=1;limit=2", "?Limit=1", "?=1",
		"?type=", "?type=a%20b", "?type=a/b", "?type=post&type=page", "?Type=post",
		"?language=", "?language=JA", "?language=japanese", "?language=ja&language=en",
		"?status=", "?status=bogus", "?status=Draft", "?status=draft,", "?status=,draft", "?status=draft,,published", "?status=draft%20",
		"?status=draft,bogus", "?status=draft&status=published", "?status=draft;published",
		"?term=", "?term=x", "?term=" + missingEntry, "?term=" + strings.ToLower(newsTerm), "?term=" + newsTerm + "0", "?term=" + newsTerm + "&term=" + newsTerm,
		"?createdBy=", "?createdBy=x", "?createdBy=" + newsTerm, "?createdBy=" + strings.ToLower(importer),
		"?translationGroup=", "?translationGroup=x", "?translationGroup=" + newsTerm,
		"?sort=", "?sort=title", "?sort=Number", "?sort=number&sort=date", "?sort=updatedAt", "?sort=id",
		"?order=", "?order=up", "?order=ASC", "?order=asc&order=desc",
		"?taxonomy=category", "?kind=image", "?q=x", "?base=1", "?uploadedBy=" + importer,
	}
	for _, q := range bad {
		checkProblem(t, "GET /api/entries"+q, as.get("/api/entries"+q), "bad-query")
	}
	for _, q := range []string{"?limit=0", "?foo=1", "?status=bogus"} {
		if res := as.send(http.MethodHead, "/api/entries"+q, ""); res.StatusCode != http.StatusBadRequest {
			t.Errorf("HEAD /api/entries%s has the status %d, want 400", q, res.StatusCode)
		}
	}

	// The values that are accepted, also when they match nothing.
	for _, q := range []string{
		"?limit=1", "?limit=100", "?offset=0", "?offset=2147483647", "?limit=007&offset=000", "?status=draft,draft", "?type=post&language=fr",
		"?term=" + missingTerm, "?createdBy=" + importer, "?translationGroup=tg_01JD0000000000000000000099", "?sort=number&order=asc",
	} {
		if res := as.get("/api/entries" + q); res.StatusCode != http.StatusOK {
			t.Errorf("GET /api/entries%s has the status %d, want 200", q, res.StatusCode)
		}
	}
	res := as.send(http.MethodHead, "/api/entries", "")
	if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != "" || res.Header.Get("Content-Length") == "" || res.Header.Get("Content-Length") == "0" {
		t.Errorf("HEAD /api/entries: %d, %d bytes, Content-Length %q, want 200, no body and the length of the list",
			res.StatusCode, len(got), res.Header.Get("Content-Length"))
	}
}
