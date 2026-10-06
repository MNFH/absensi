// Package idfmt formats dates, durations and phone numbers the way
// Indonesian HR staff read them (Go's time.Format only knows English names).
package idfmt

import (
	"fmt"
	"strings"
	"time"
)

var (
	months      = [...]string{"Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
	shortMonths = [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
	days        = [...]string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	shortDays   = [...]string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"}
)

// Month returns e.g. "Oktober".
func Month(m time.Month) string { return months[m-1] }

// MonthYear returns e.g. "Oktober 2026".
func MonthYear(t time.Time) string { return fmt.Sprintf("%s %d", Month(t.Month()), t.Year()) }

// Date returns e.g. "6 Okt 2026".
func Date(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), shortMonths[t.Month()-1], t.Year())
}

// DateShort returns e.g. "6 Okt".
func DateShort(t time.Time) string { return fmt.Sprintf("%d %s", t.Day(), shortMonths[t.Month()-1]) }

// DateTime returns e.g. "6 Okt 2026 14:05:09 WIB".
func DateTime(t time.Time) string { return Date(t) + " " + t.Format("15:04:05 MST") }

// Weekday returns e.g. "Selasa".
func Weekday(t time.Time) string { return days[t.Weekday()] }

// WeekdayShort returns e.g. "Sel".
func WeekdayShort(t time.Time) string { return shortDays[t.Weekday()] }

// Duration returns e.g. "19 jam 18 menit" (rounded to the minute).
func Duration(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	switch {
	case m < 60:
		return fmt.Sprintf("%d menit", m)
	case m%60 == 0:
		return fmt.Sprintf("%d jam", m/60)
	}
	return fmt.Sprintf("%d jam %d menit", m/60, m%60)
}

// Phone turns stored digits "628123456789" into the local form "08123456789".
func Phone(p string) string {
	if strings.HasPrefix(p, "62") {
		return "0" + p[2:]
	}
	return p
}
