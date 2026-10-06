// Package timeparse parses user-typed attendance times such as
// "5 Januari 5.00AM", "5 Januari 17.00" or "5 January 5:30 pm".
package timeparse

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var months = map[string]time.Month{
	"januari": time.January, "january": time.January, "jan": time.January,
	"februari": time.February, "february": time.February, "feb": time.February,
	"maret": time.March, "march": time.March, "mar": time.March,
	"april": time.April, "apr": time.April,
	"mei": time.May, "may": time.May,
	"juni": time.June, "june": time.June, "jun": time.June,
	"juli": time.July, "july": time.July, "jul": time.July,
	"agustus": time.August, "august": time.August, "agu": time.August, "agt": time.August, "aug": time.August,
	"september": time.September, "sep": time.September, "sept": time.September,
	"oktober": time.October, "october": time.October, "okt": time.October, "oct": time.October,
	"november": time.November, "nov": time.November, "nopember": time.November,
	"desember": time.December, "december": time.December, "des": time.December, "dec": time.December,
}

// <day> <month> [year] <hour>[.:]<min>[ am|pm]
var re = regexp.MustCompile(`^(\d{1,2})\s+([a-z]+)(?:\s+(\d{4}))?\s+(\d{1,2})(?:[.:](\d{2}))?\s*(am|pm)?$`)

var (
	ErrFormat   = errors.New("format waktu tidak dikenali")
	ErrFuture   = errors.New("waktu tidak boleh di masa depan")
	ErrTooOld   = errors.New("waktu terlalu lama (melebihi batas)")
	ErrBadValue = errors.New("tanggal atau jam tidak valid")
)

// Parse interprets s relative to now (in now's location). A time without
// AM/PM is read as 24-hour. When the year is omitted the current year is
// used. The result must not be in the future (2 minute clock-skew tolerance)
// nor older than maxBackdateDays.
func Parse(s string, now time.Time, maxBackdateDays int) (time.Time, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	m := re.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, ErrFormat
	}
	day, _ := strconv.Atoi(m[1])
	month, ok := months[m[2]]
	if !ok {
		return time.Time{}, fmt.Errorf("%w: bulan %q", ErrFormat, m[2])
	}
	year := now.Year()
	if m[3] != "" {
		year, _ = strconv.Atoi(m[3])
	}
	hour, _ := strconv.Atoi(m[4])
	min := 0
	if m[5] != "" {
		min, _ = strconv.Atoi(m[5])
	}
	switch m[6] {
	case "am":
		if hour < 1 || hour > 12 {
			return time.Time{}, ErrBadValue
		}
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour < 1 || hour > 12 {
			return time.Time{}, ErrBadValue
		}
		if hour != 12 {
			hour += 12
		}
	}
	if hour > 23 || min > 59 || day < 1 {
		return time.Time{}, ErrBadValue
	}
	t := time.Date(year, month, day, hour, min, 0, 0, now.Location())
	// time.Date normalises overflow (31 Feb -> 3 Mar); reject that.
	if t.Day() != day || t.Month() != month {
		return time.Time{}, ErrBadValue
	}
	// No year typed and the date lies ahead: if the same date last year is
	// closer to now, the user means last year ("20 Desember" written in
	// January). Validation below then reports "too old" or "future" sensibly.
	if m[3] == "" && t.After(now.Add(2*time.Minute)) {
		prev := time.Date(year-1, month, day, hour, min, 0, 0, now.Location())
		if prev.Day() == day && now.Sub(prev) < t.Sub(now) {
			t = prev
		}
	}
	if err := Validate(t, now, maxBackdateDays); err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// MonthByName resolves Indonesian or English month names and abbreviations
// ("Desember", "des", "December", "dec").
func MonthByName(s string) (time.Month, bool) {
	m, ok := months[strings.ToLower(strings.TrimSpace(s))]
	return m, ok
}

// Validate rejects times in the future (2 minute clock-skew tolerance) or
// older than maxBackdateDays.
func Validate(t, now time.Time, maxBackdateDays int) error {
	if t.After(now.Add(2 * time.Minute)) {
		return ErrFuture
	}
	if maxBackdateDays > 0 && t.Before(now.AddDate(0, 0, -maxBackdateDays)) {
		return ErrTooOld
	}
	return nil
}
