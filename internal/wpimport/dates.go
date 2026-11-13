package wpimport

import (
	"strings"
	"time"
)

const postDateLayout = "2006-01-02 15:04:05"

// postTime returns the time of an item of WordPress as RFC 3339 with seconds and an offset, from its post_date (date)
// and post_date_gmt (gmt), which WordPress writes as "2006-01-02 15:04:05". The offset is the difference between the two,
// if it is one that a time zone can have, in whole minutes. Otherwise date is read as a time in zone, if zone is not nil.
// It reports false if neither works, and for a date that is not a date, such as "0000-00-00 00:00:00".
func postTime(date, gmt string, zone *time.Location) (string, bool) {
	local, ok := parsePostDate(date)
	if !ok {
		return "", false
	}
	if utc, ok := parsePostDate(gmt); ok {
		if d := local.Sub(utc); d >= -12*time.Hour && d <= 14*time.Hour && d%time.Minute == 0 {
			return wallTime(local, time.FixedZone("", int(d/time.Second))), true
		}
	}
	if zone == nil {
		return "", false
	}
	return wallTime(local, zone), true
}

// parsePostDate reads s as a date of WordPress, with the fields as they are written: the time zone is UTC.
func parsePostDate(s string) (time.Time, bool) {
	t, err := time.Parse(postDateLayout, strings.TrimSpace(s))
	if err != nil || t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}, false
	}
	return t, true
}

// wallTime writes the fields of t, as the clock of loc shows them, in RFC 3339.
func wallTime(t time.Time, loc *time.Location) string {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc).Format(time.RFC3339)
}
