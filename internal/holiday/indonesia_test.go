package holiday

import (
	"testing"
	"time"
)

// Weekdays as printed in the official announcements; a typo in a date would
// almost certainly land on a different weekday.
var officialWeekday = map[string]time.Weekday{
	"2026-01-01": time.Thursday, "2026-01-16": time.Friday, "2026-02-17": time.Tuesday, "2026-03-19": time.Thursday,
	"2026-03-21": time.Saturday, "2026-03-22": time.Sunday, "2026-04-03": time.Friday, "2026-04-05": time.Sunday,
	"2026-05-01": time.Friday, "2026-05-14": time.Thursday, "2026-05-27": time.Wednesday, "2026-05-31": time.Sunday,
	"2026-06-01": time.Monday, "2026-06-16": time.Tuesday, "2026-08-17": time.Monday, "2026-08-25": time.Tuesday,
	"2026-12-25": time.Friday,
	"2026-02-16": time.Monday, "2026-03-18": time.Wednesday, "2026-03-20": time.Friday, "2026-03-23": time.Monday,
	"2026-03-24": time.Tuesday, "2026-05-15": time.Friday, "2026-05-28": time.Thursday, "2026-12-24": time.Thursday,

	"2027-01-01": time.Friday, "2027-01-05": time.Tuesday, "2027-02-06": time.Saturday, "2027-03-08": time.Monday,
	"2027-03-10": time.Wednesday, "2027-03-11": time.Thursday, "2027-03-26": time.Friday, "2027-03-28": time.Sunday,
	"2027-05-01": time.Saturday, "2027-05-06": time.Thursday, "2027-05-17": time.Monday, "2027-05-20": time.Thursday,
	"2027-06-01": time.Tuesday, "2027-06-06": time.Sunday, "2027-08-15": time.Sunday, "2027-08-17": time.Tuesday,
	"2027-12-25": time.Saturday, "2027-12-26": time.Sunday,
	"2027-02-05": time.Friday, "2027-03-09": time.Tuesday, "2027-03-12": time.Friday, "2027-03-15": time.Monday,
	"2027-03-25": time.Thursday, "2027-05-18": time.Tuesday, "2027-05-19": time.Wednesday, "2027-12-24": time.Friday,
}

func TestDefaults(t *testing.T) {
	count := map[string]int{}
	seen := map[string]bool{}
	for _, d := range Defaults {
		day, err := time.Parse("2006-01-02", d.Date)
		if err != nil {
			t.Fatalf("%s: %v", d.Date, err)
		}
		if seen[d.Date] {
			t.Errorf("duplicate date %s", d.Date)
		}
		seen[d.Date] = true
		want, ok := officialWeekday[d.Date]
		if !ok {
			t.Errorf("%s not in the official list", d.Date)
		} else if day.Weekday() != want {
			t.Errorf("%s is a %s, announcement says %s", d.Date, day.Weekday(), want)
		}
		count[d.Date[:4]+" "+d.Kind]++
	}
	for key, want := range map[string]int{
		"2026 " + KindNational: 17, "2026 " + KindJoint: 8,
		"2027 " + KindNational: 18, "2027 " + KindJoint: 8,
	} {
		if count[key] != want {
			t.Errorf("%s: %d days, SKB says %d", key, count[key], want)
		}
	}
	if len(seen) != len(officialWeekday) {
		t.Errorf("%d defaults vs %d official dates", len(seen), len(officialWeekday))
	}
}
