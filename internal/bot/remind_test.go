package bot

import (
	"errors"
	"testing"
	"time"

	"github.com/absensi/internal/db"
)

func TestDueNow(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	at := func(h, m int) time.Time { return time.Date(2026, 10, 6, h, m, 0, 0, loc) }
	tests := []struct {
		clock string
		now   time.Time
		want  bool
	}{
		{"12:00", at(11, 59), false},
		{"12:00", at(12, 0), true},
		{"12:00", at(12, 29), true}, // bot was down at 12:00: catch up
		{"12:00", at(12, 30), false},
		{"23:00", at(23, 15), true},
		{"", at(12, 0), false},
		{"bogus", at(12, 0), false},
	}
	for _, tc := range tests {
		if got := dueNow(tc.clock, tc.now); got != tc.want {
			t.Errorf("dueNow(%q, %s) = %v, want %v", tc.clock, tc.now.Format("15:04"), got, tc.want)
		}
	}
}

func TestBotRemoved(t *testing.T) {
	if !botRemoved(errors.New("telegram: Forbidden: bot was kicked from the supergroup chat (403)")) ||
		!botRemoved(errors.New("telegram: Bad Request: chat not found (400)")) ||
		botRemoved(errors.New("context deadline exceeded")) {
		t.Error("botRemoved misclassifies errors")
	}
}

func TestMatchUsers(t *testing.T) {
	us := []db.User{
		{ID: 1, Name: "Fajar Nugroho", Phone: "628000000106"},
		{ID: 2, Name: "Andi Pratama", Phone: "628000000101"},
		{ID: 3, Name: "Andi Wijaya", Phone: "628000000111"},
		{ID: 4, Name: "Andi", Phone: "628000000112"},
	}
	ids := func(q string) []int64 {
		var out []int64
		for _, u := range matchUsers(us, q) {
			out = append(out, u.ID)
		}
		return out
	}
	for q, want := range map[string][]int64{
		"fajar":          {1},
		"FAJAR  nugroho": {1},
		"andi":           {4}, // exact name wins over partial matches
		"andi pratama":   {2},
		"pratama andi":   {2}, // word order doesn't matter
		"wijaya":         {3},
		"0800-000-0106":  {1}, // = 08000000106, how the demo phone is shown
		"budi":           nil,
		"   ":            nil,
	} {
		got := ids(q)
		if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
			t.Errorf("%q: got %v, want %v", q, got, want)
		}
	}
	// Without an exact name, several partial matches are all returned.
	if got := matchUsers(us[:3], "andi"); len(got) != 2 {
		t.Errorf("ambiguous: got %d", len(got))
	}
}
