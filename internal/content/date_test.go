package content

import "testing"

func TestCheckDate(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{in: "2026-10-25", ok: true},
		{in: "2024-02-29", ok: true},
		{in: "0001-01-01", ok: true},
		{in: "9999-12-31", ok: true},
		{in: "2026-02-29"},
		{in: "2026-13-01"},
		{in: "2026-00-10"},
		{in: "2026-10-32"},
		{in: "2026-1-25"},
		{in: "2026-10-5"},
		{in: "26-10-25"},
		{in: "+2026-10-25"},
		{in: "2026/10/25"},
		{in: "2026-10-25 "},
		{in: "2026-10-25T00:00:00Z"},
		{in: "２０２６-10-25"},
		{in: ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = `invalid date "` + tt.in + `": use YYYY-MM-DD`
			}
			assertErrorMsg(t, checkDate(tt.in), msg)
		})
	}
}

func TestCheckDatetime(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{in: "2026-10-25T10:00:00+09:00", ok: true},
		{in: "2026-10-25T01:00:00Z", ok: true},
		{in: "2026-10-25T10:00:00-05:30", ok: true},
		{in: "2026-10-25T23:59:59+23:59", ok: true},
		{in: "2026-10-25T00:00:00-00:00", ok: true},
		{in: "2024-02-29T00:00:00Z", ok: true},
		{in: "2026-02-29T00:00:00Z"},
		{in: "2026-10-25T24:00:00Z"},
		{in: "2026-10-25T23:60:00Z"},
		{in: "2026-10-25T23:59:60Z"},
		{in: "2026-10-25T10:00:00+24:00"},
		{in: "2026-10-25T10:00:00+09:60"},
		{in: "2026-10-25T10:00:00"},
		{in: "2026-10-25T10:00Z"},
		{in: "2026-10-25T10:00:00.5Z"},
		{in: "2026-10-25T10:00:00,5Z"},
		{in: "2026-10-25T10:00:00+0900"},
		{in: "2026-10-25T10:00:00+09"},
		{in: "2026-10-25t10:00:00z"},
		{in: "2026-10-25 10:00:00Z"},
		{in: "2026-10-25"},
		{in: ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = `invalid date and time "` + tt.in + `": use RFC 3339 with seconds and a time zone offset`
			}
			assertErrorMsg(t, checkDatetime(tt.in), msg)
		})
	}
}
