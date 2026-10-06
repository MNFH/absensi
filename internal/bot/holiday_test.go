package bot

import (
	"strings"
	"testing"
	"time"
)

func TestParseDayRange(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, loc)
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, loc) }
	tests := []struct {
		in       string
		from, to time.Time
		rest     string
		bad      bool
	}{
		{in: "2026-12-24 Libur kantor", from: d(2026, 12, 24), to: d(2026, 12, 24), rest: "Libur kantor"},
		{in: "24-12-2026 x", from: d(2026, 12, 24), to: d(2026, 12, 24), rest: "x"},
		{in: "24/12/2026", from: d(2026, 12, 24), to: d(2026, 12, 24)},
		{in: "24 Desember Natal kantor", from: d(2026, 12, 24), to: d(2026, 12, 24), rest: "Natal kantor"},
		{in: "2 Januari Libur", from: d(2027, 1, 2), to: d(2027, 1, 2), rest: "Libur"},        // already past this year -> next
		{in: "1 Oktober 2026 telat", from: d(2026, 10, 1), to: d(2026, 10, 1), rest: "telat"}, // explicit year kept
		{in: "29 Desember sampai 31 Desember Tutup akhir tahun", from: d(2026, 12, 29), to: d(2026, 12, 31), rest: "Tutup akhir tahun"},
		{in: "2026-12-29 s/d 2026-12-31", from: d(2026, 12, 29), to: d(2026, 12, 31)},
		{in: "31 Desember sampai 29 Desember x", bad: true},
		{in: "31 Februari x", bad: true},
		{in: "besok libur", bad: true},
		{in: "", bad: true},
	}
	for _, tc := range tests {
		from, to, rest, err := parseDayRange(strings.Fields(tc.in), now)
		if tc.bad {
			if err == nil {
				t.Errorf("%q: expected error, got %v..%v", tc.in, from, to)
			}
			continue
		}
		if err != nil || !from.Equal(tc.from) || !to.Equal(tc.to) || strings.Join(rest, " ") != tc.rest {
			t.Errorf("%q: got %v..%v rest=%q err=%v", tc.in, from.Format("2006-01-02"), to.Format("2006-01-02"), rest, err)
		}
	}
}
