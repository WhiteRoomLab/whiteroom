package wpimport

import (
	"testing"
	"time"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestPostTime(t *testing.T) {
	tokyo := mustLocation(t, "Asia/Tokyo")
	newYork := mustLocation(t, "America/New_York")
	for _, tt := range []struct {
		name      string
		date, gmt string
		zone      *time.Location
		want      string // "" if postTime is to report false
	}{
		{"the offset of Japan", "2026-10-05 09:45:00", "2026-10-05 00:45:00", nil, "2026-10-05T09:45:00+09:00"},
		{"no offset", "2026-10-05 09:45:00", "2026-10-05 09:45:00", nil, "2026-10-05T09:45:00Z"},
		{"a half hour", "2026-10-05 09:45:00", "2026-10-05 04:15:00", nil, "2026-10-05T09:45:00+05:30"},
		{"behind UTC", "2026-01-05 09:45:00", "2026-01-05 17:45:00", nil, "2026-01-05T09:45:00-08:00"},
		{"the day before in UTC", "2026-10-05 01:00:00", "2026-10-04 16:00:00", nil, "2026-10-05T01:00:00+09:00"},
		{"the day after in UTC", "2026-10-04 20:00:00", "2026-10-05 04:00:00", nil, "2026-10-04T20:00:00-08:00"},
		{"the largest offset", "2026-10-05 14:00:00", "2026-10-05 00:00:00", nil, "2026-10-05T14:00:00+14:00"},
		{"the smallest offset", "2026-10-05 00:00:00", "2026-10-05 12:00:00", nil, "2026-10-05T00:00:00-12:00"},
		{"the offset wins over the zone", "2026-10-05 09:45:00", "2026-10-05 00:45:00", newYork, "2026-10-05T09:45:00+09:00"},
		{"space around", " 2026-10-05 09:45:00\n", "\t2026-10-05 00:45:00 ", nil, "2026-10-05T09:45:00+09:00"},
		{"the first year", "0001-01-01 00:00:00", "0001-01-01 00:00:00", nil, "0001-01-01T00:00:00Z"},
		{"the last year", "9999-12-31 23:59:59", "9999-12-31 09:59:59", nil, "9999-12-31T23:59:59+14:00"},

		{"no gmt", "2026-10-05 09:45:00", "", tokyo, "2026-10-05T09:45:00+09:00"},
		{"gmt is zeros", "2026-10-05 09:45:00", "0000-00-00 00:00:00", tokyo, "2026-10-05T09:45:00+09:00"},
		{"gmt in another form", "2026-10-05 09:45:00", "2026-10-05T00:45:00Z", tokyo, "2026-10-05T09:45:00+09:00"},
		{"gmt of year 0", "2026-10-05 09:45:00", "0000-01-01 00:00:00", tokyo, "2026-10-05T09:45:00+09:00"},
		{"summer time", "2026-07-01 12:00:00", "", newYork, "2026-07-01T12:00:00-04:00"},
		{"winter time", "2026-01-01 12:00:00", "", newYork, "2026-01-01T12:00:00-05:00"},
		{"the offset is too large", "2026-10-05 15:00:00", "2026-10-05 00:00:00", tokyo, "2026-10-05T15:00:00+09:00"},
		{"the offset is too small", "2026-10-05 00:00:00", "2026-10-05 13:00:00", tokyo, "2026-10-05T00:00:00+09:00"},
		{"the offset has seconds", "2026-10-05 09:45:30", "2026-10-05 00:45:00", tokyo, "2026-10-05T09:45:30+09:00"},
		{"the offset has seconds, gmt later", "2026-10-05 09:45:00", "2026-10-05 00:45:30", tokyo, "2026-10-05T09:45:00+09:00"},

		{"no zone and no gmt", "2026-10-05 09:45:00", "", nil, ""},
		{"no zone and gmt is zeros", "2026-10-05 09:45:00", "0000-00-00 00:00:00", nil, ""},
		{"no zone and the offset is too large", "2026-10-05 15:00:00", "2026-10-05 00:00:00", nil, ""},
		{"no zone and the offset has seconds", "2026-10-05 09:45:30", "2026-10-05 00:45:00", nil, ""},
		{"no date", "", "2026-10-05 00:45:00", tokyo, ""},
		{"date is zeros", "0000-00-00 00:00:00", "0000-00-00 00:00:00", tokyo, ""},
		{"date of year 0", "0000-01-01 00:00:00", "0000-01-01 00:00:00", tokyo, ""},
		{"date is in another form", "2026-10-05T09:45:00+09:00", "2026-10-05 00:45:00", tokyo, ""},
		{"date has no seconds", "2026-10-05 09:45", "2026-10-05 00:45", tokyo, ""},
		{"date is not a date", "2026-02-30 09:45:00", "2026-02-30 00:45:00", tokyo, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := postTime(tt.date, tt.gmt, tt.zone)
			if got != tt.want || ok != (tt.want != "") {
				t.Fatalf("postTime(%q, %q) = %q, %v; want %q", tt.date, tt.gmt, got, ok, tt.want)
			}
			if !ok {
				return
			}
			m := testMedia(got)
			if err := m.Validate(termSite()); err != nil {
				t.Errorf("a medium uploaded at %q is not valid: %v", got, err)
			}
		})
	}
}
