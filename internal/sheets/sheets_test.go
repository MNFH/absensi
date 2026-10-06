package sheets

import (
	"testing"
	"time"

	"github.com/absensi/internal/attendance"
)

func TestParseRow(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	c := &Client{loc: loc}

	full := []any{"2026-01-05 08:01:02", "2026-01-05", "08:00:00", "123", "Budi", "628123", "DATANG", "WFO",
		"-6.200000", "106.816666", "Monas, Jakarta", "https://www.google.com/maps?q=-6.200000,106.816666", "live"}
	r, ok := c.parseRow(full)
	if !ok || r.TelegramID != 123 || r.Phone != "628123" || r.Status != "WFO" || !r.HasLocation || r.Lat != -6.2 || r.Address != "Monas, Jakarta" || r.Source != "live" {
		t.Fatalf("full row: %+v ok=%v", r, ok)
	}
	if r.At.Hour() != 8 || r.Type != attendance.TypeIn {
		t.Errorf("time/type: %+v", r)
	}

	// Check-out without location: Sheets drops trailing empty cells.
	short := []any{"2026-01-05 17:01:00", "2026-01-05", "17:00:00", "123", "Budi", "628123", "PULANG", "WFO"}
	r, ok = c.parseRow(short)
	if !ok || r.HasLocation || r.Type != attendance.TypeOut {
		t.Fatalf("short row: %+v ok=%v", r, ok)
	}

	for _, bad := range [][]any{{"x"}, {"", "bogus", "08:00:00", "1", "n", "", "DATANG"}, {"", "2026-01-05", "08:00:00", "abc", "n", "", "DATANG"}, {"", "2026-01-05", "08:00:00", "1", "n", "", "LAIN"}} {
		if _, ok := c.parseRow(bad); ok {
			t.Errorf("row %v should be rejected", bad)
		}
	}
}
