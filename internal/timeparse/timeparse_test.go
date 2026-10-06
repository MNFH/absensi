package timeparse

import (
	"errors"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, loc)
	d := func(day, h, m int) time.Time { return time.Date(2026, 1, day, h, m, 0, 0, loc) }

	tests := []struct {
		in   string
		want time.Time
		err  error
	}{
		{"5 Januari 5.00AM", d(5, 5, 0), nil},
		{"5 Januari 5.00", d(5, 5, 0), nil},
		{"5 Januari 17.00", d(5, 17, 0), nil},
		{"5 january 5:30 pm", d(5, 17, 30), nil},
		{"  5   Jan   12.00 AM ", d(5, 0, 0), nil},
		{"5 Jan 12.00PM", d(5, 12, 0), nil},
		{"5 Januari 2026 08.15", d(5, 8, 15), nil},
		{"5 Januari 8", d(5, 8, 0), nil},
		{"25 Januari 08.00", time.Time{}, ErrFuture},
		{"20 Januari 12.01", d(20, 12, 1), nil},      // within skew
		{"1 Desember 08.00", time.Time{}, ErrTooOld}, // means Dec 2025: 50 days > 31
		{"1 Januari 2024 08.00", time.Time{}, ErrTooOld},
		{"31 Februari 08.00", time.Time{}, ErrBadValue},
		{"5 Januari 25.00", time.Time{}, ErrBadValue},
		{"5 Januari 13.00PM", time.Time{}, ErrBadValue},
		{"5 Foo 08.00", time.Time{}, ErrFormat},
		{"besok pagi", time.Time{}, ErrFormat},
	}
	for _, tc := range tests {
		got, err := Parse(tc.in, now, 31)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Errorf("%q: err = %v, want %v", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%q: got %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestParseAcrossYearEnd(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2027, 1, 10, 9, 0, 0, 0, loc)

	got, err := Parse("20 Desember 17.00", now, 62)
	if err != nil || !got.Equal(time.Date(2026, 12, 20, 17, 0, 0, 0, loc)) {
		t.Errorf("20 Desember in January: %v, %v", got, err)
	}
	// Explicit future year stays future.
	if _, err := Parse("20 Desember 2027 17.00", now, 62); !errors.Is(err, ErrFuture) {
		t.Errorf("explicit 2027: err = %v", err)
	}
	// Beyond the backdate window is still rejected.
	if _, err := Parse("1 Oktober 08.00", now, 62); !errors.Is(err, ErrTooOld) {
		t.Errorf("October with 62-day limit: err = %v", err)
	}
	// Later today is still "future", not last year.
	if _, err := Parse("10 Januari 18.00", now, 62); !errors.Is(err, ErrFuture) {
		t.Errorf("later today: err = %v", err)
	}
}

func TestParseLastDecemberWithinTwoMonths(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, loc)
	got, err := Parse("1 Desember 08.00", now, 62)
	if err != nil || !got.Equal(time.Date(2025, 12, 1, 8, 0, 0, 0, loc)) {
		t.Errorf("got %v, %v", got, err)
	}
}
