package admin

import (
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
)

const (
	// defaultPage is the limit of a page when the query gives none, and maxPage the most that it may give.
	defaultPage = 50
	maxPage     = 100
)

// queryForms maps the names that the query of a route may have to the check of a value: it reports whether the value has the
// form that the name takes.
type queryForms map[string]func(value string) bool

// readQuery reads the query of r, which may have only the names of forms, once each, with values of their forms, and must
// have the names of required. If it does not, it has answered 400 and returns false.
func readQuery(w http.ResponseWriter, r *http.Request, forms queryForms, required ...string) (url.Values, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeBadQuery(w)
		return nil, false
	}
	for name, values := range q {
		if check, known := forms[name]; !known || len(values) != 1 || !check(values[0]) {
			writeBadQuery(w)
			return nil, false
		}
	}
	for _, name := range required {
		if _, ok := q[name]; !ok {
			writeBadQuery(w)
			return nil, false
		}
	}
	return q, true
}

// isOffset reports whether v is the offset of a page: digits, from 0, that an int of 32 bits holds.
func isOffset(v string) bool {
	_, err := strconv.ParseUint(v, 10, 31)
	return err == nil
}

// isLimit reports whether v is the limit of a page: digits, from 1 to maxPage.
func isLimit(v string) bool {
	n, err := strconv.ParseUint(v, 10, 31)
	return err == nil && n >= 1 && n <= maxPage
}

// isBase reports whether v is the Seq of a version, which the query gives as base: digits, from 0.
func isBase(v string) bool {
	_, err := strconv.ParseUint(v, 10, 63)
	return err == nil
}

// baseOf returns the Seq that q, which readQuery has checked, gives as base.
func baseOf(q url.Values) int64 {
	n, _ := strconv.ParseUint(q.Get("base"), 10, 63)
	return int64(n)
}

// page is the part of a list that the query asks for.
type page struct{ offset, limit int }

// readListQuery reads the query of a list, which has the page (offset from 0, default 0, and limit from 1 to maxPage, default
// defaultPage) and the names of filters, and no others. If it fails it has answered 400 and returns false.
func readListQuery(w http.ResponseWriter, r *http.Request, filters queryForms) (pg page, q url.Values, ok bool) {
	forms := maps.Clone(filters)
	if forms == nil {
		forms = make(queryForms, 2)
	}
	forms["offset"], forms["limit"] = isOffset, isLimit
	if q, ok = readQuery(w, r, forms); !ok {
		return page{}, nil, false
	}
	pg = page{limit: defaultPage}
	if v := q.Get("offset"); v != "" {
		n, _ := strconv.ParseUint(v, 10, 31)
		pg.offset = int(n)
	}
	if v := q.Get("limit"); v != "" {
		n, _ := strconv.ParseUint(v, 10, 31)
		pg.limit = int(n)
	}
	return pg, q, true
}

// bounds returns the part of a list of n items that the page selects, as the from and the to of a slice.
func (pg page) bounds(n int) (from, to int) {
	from = min(pg.offset, n)
	return from, from + min(pg.limit, n-from)
}

// writeList writes {"items":[…],"total":…,"offset":…,"limit":…} to out: items writes the elements of the array, each one
// value, and the numbers are those of the list and its page. If items fails, so does writeList.
func writeList(out io.Writer, total int, pg page, items func(j *jsonStream) error) error {
	j := newJSONStream(out)
	j.object()
	j.key("items")
	j.array()
	if err := items(j); err != nil {
		return err
	}
	j.end()
	j.key("total")
	j.value(total)
	j.key("offset")
	j.value(pg.offset)
	j.key("limit")
	j.value(pg.limit)
	j.end()
	return j.done()
}
