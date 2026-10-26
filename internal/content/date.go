package content

import (
	"fmt"
	"regexp"
	"time"
)

// datetimePattern matches the RFC 3339 form that checkDatetime accepts, apart from the ranges of the date and the time.
var datetimePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

// checkDate accepts a date such as "2026-10-25".
func checkDate(s string) error {
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return fmt.Errorf("invalid date %q: use YYYY-MM-DD", s)
	}
	return nil
}

// checkDatetime accepts a date and time in RFC 3339 with seconds and a time zone offset, such as "2026-10-25T10:00:00+09:00".
func checkDatetime(s string) error {
	// time.Parse alone would also accept fractions of a second and offsets such as "+24:00".
	if datetimePattern.MatchString(s) {
		if _, err := time.Parse(time.RFC3339, s); err == nil {
			return nil
		}
	}
	return fmt.Errorf(`invalid date and time %q: use RFC 3339 with seconds and a time zone offset, such as "2026-10-25T10:00:00+09:00"`, s)
}
