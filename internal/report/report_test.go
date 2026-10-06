package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/absensi/internal/attendance"
)

func TestBuild(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, loc) }
	rec := func(id int64, typ, status string, d, h int) attendance.Record {
		return attendance.Record{TelegramID: id, Name: map[int64]string{1: "Andi", 2: "Budi"}[id], Phone: "628111111111",
			Type: typ, Status: status, At: at(d, h), Source: attendance.SourceLive, HasLocation: true, Lat: -6.2, Lng: 106.8, Address: "Jakarta"}
	}
	recs := []attendance.Record{
		rec(1, attendance.TypeIn, attendance.StatusWFO, 1, 8), rec(1, attendance.TypeOut, "", 1, 17), // Thu: 9h
		rec(1, attendance.TypeIn, attendance.StatusWFH, 2, 8), // Fri: forgot check-out
		rec(1, attendance.TypeIn, attendance.StatusWFO, 6, 8), // Tue (today): still working
		rec(2, attendance.TypeIn, attendance.StatusWFO, 5, 9), rec(2, attendance.TypeOut, "", 5, 18),
	}
	people := []Person{
		{TelegramID: 2, Name: "Budi", Phone: "628222222222", Account: "Aktif"},
		{TelegramID: 1, Name: "Andi", Phone: "628111111111", Account: "Aktif"},
		{TelegramID: 0, Name: "Indah", Phone: "628333333333", Account: "Belum verifikasi"},
	}
	now := at(6, 12)
	data, st, err := Build(Input{Title: "Oktober 2026", From: at(1, 0), To: time.Date(2026, 11, 1, 0, 0, 0, 0, loc), Now: now, People: people, Records: recs})
	if err != nil {
		t.Fatal(err)
	}
	// Workdays 1–6 Oct: Thu 1, Fri 2, Mon 5, Tue 6.
	if st.People != 3 || st.Workdays != 4 || st.Incomplete != 1 || st.WorkingNow != 1 {
		t.Errorf("stats = %+v", st)
	}
	// Andi present 3/4, Budi 1/4 -> 4 of 8.
	if st.Attendance != 0.5 {
		t.Errorf("attendance = %v", st.Attendance)
	}

	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	get := func(sheet, ref string) string { v, _ := f.GetCellValue(sheet, ref); return v }

	// Rows are sorted by name: Andi (5), Budi (6), Indah (7). Date columns start at E = 1 Oct.
	checks := map[string]string{
		"B5": "Andi", "C5": "08111111111", "E5": "WFO", "F5": "WFH", "G5": "", "I5": "-", "J5": "WFO",
		"B6": "Budi", "E6": "-", "I6": "WFO", "K6": "", // K = 7 Oct, future
		"B7": "Indah", "E7": "", // unverified: never marked absent
	}
	for ref, want := range checks {
		if got := get(sheetMatrix, ref); got != want {
			t.Errorf("%s = %q, want %q", ref, got, want)
		}
	}
	// Totals block starts after 31 date columns: E + 31 = AJ (Hadir), AK Hari Kerja, …, AN Tidak Hadir, AO Lupa Pulang.
	for ref, want := range map[string]string{"AJ4": "Hadir", "AJ5": "3", "AK5": "4", "AN5": "1", "AO5": "1", "AJ6": "1", "AN6": "3"} {
		if got := get(sheetMatrix, ref); got != want {
			t.Errorf("%s = %q, want %q", ref, got, want)
		}
	}
	if got := get(sheetMatrix, "E4"); got != "1\nKam" {
		t.Errorf("date header = %q", got)
	}

	rows, _ := f.GetRows(sheetDetail)
	if len(rows) != 1+4 { // header + 4 sessions
		t.Fatalf("detail rows = %d", len(rows))
	}
	if rows[1][0] != "1 Okt 2026" || rows[1][2] != "Andi" || rows[1][7] != "9:00" {
		t.Errorf("detail row 1 = %v", rows[1])
	}
	notes := map[string]bool{}
	for _, r := range rows[1:] {
		if len(r) > 8 {
			notes[r[8]] = true
		}
	}
	if !notes["lupa absen pulang"] || !notes["sedang bekerja"] {
		t.Errorf("notes = %v", notes)
	}
}

func TestWindowNoAbsenceBeforeJoinOrAfterLeave(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, loc) }
	people := []Person{
		{TelegramID: 1, Name: "A Baru", Account: "Aktif", Since: day(5).Add(10 * time.Hour)}, // joined Mon 5 Oct
		{TelegramID: 2, Name: "B Keluar", Account: "Dicabut", Until: day(2).Add(17 * time.Hour)},
	}
	recs := []attendance.Record{
		{TelegramID: 2, Name: "B Keluar", Type: attendance.TypeIn, At: day(1).Add(8 * time.Hour)},
		{TelegramID: 2, Name: "B Keluar", Type: attendance.TypeOut, At: day(1).Add(17 * time.Hour)},
	}
	data, st, err := Build(Input{Title: "Oktober 2026", From: day(1), To: day(1).AddDate(0, 1, 0), Now: day(6).Add(12 * time.Hour), People: people, Records: recs})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := excelize.OpenReader(bytes.NewReader(data))
	get := func(ref string) string { v, _ := f.GetCellValue(sheetMatrix, ref); return v }
	// A (row 5): 1–2 Oct blank (before joining), 5–6 Oct absent; 2 own workdays.
	// B (row 6): 1 Oct present, 2 Oct still counted (left that day), 5–6 Oct blank.
	for ref, want := range map[string]string{
		"E5": "", "F5": "", "I5": "-", "J5": "-", "AK5": "2", "AN5": "2",
		"E6": "H", "F6": "-", "I6": "", "J6": "", "AK6": "2", "AN6": "1",
	} {
		if got := get(ref); got != want {
			t.Errorf("%s = %q, want %q", ref, got, want)
		}
	}
	// Attendance rate only counts active people: A was present 0 of 2.
	if st.Attendance != 0 {
		t.Errorf("attendance = %v", st.Attendance)
	}
}

func TestHolidays(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	day := func(d int) time.Time { return time.Date(2026, 12, d, 0, 0, 0, 0, loc) }
	people := []Person{{TelegramID: 1, Name: "A", Account: "Aktif"}}
	recs := []attendance.Record{ // worked on the 24th although it is cuti bersama
		{TelegramID: 1, Name: "A", Type: attendance.TypeIn, Status: attendance.StatusWFO, At: day(24).Add(8 * time.Hour)},
		{TelegramID: 1, Name: "A", Type: attendance.TypeOut, At: day(24).Add(12 * time.Hour)},
	}
	data, st, err := Build(Input{Title: "Desember 2026", From: day(21), To: day(28), Now: day(27).Add(10 * time.Hour),
		People: people, Records: recs, Holidays: map[string]string{"2026-12-24": "Cuti bersama Natal", "2026-12-25": "Natal"}})
	if err != nil {
		t.Fatal(err)
	}
	// Mon 21 – Fri 25 has 5 weekdays, minus 2 holidays = 3 working days.
	if st.Workdays != 3 {
		t.Errorf("workdays = %d", st.Workdays)
	}
	f, _ := excelize.OpenReader(bytes.NewReader(data))
	get := func(ref string) string { v, _ := f.GetCellValue(sheetMatrix, ref); return v }
	// E=21 F=22 G=23 H=24 I=25: absent on working days, present on the 24th, nothing on the 25th.
	for ref, want := range map[string]string{"E5": "-", "F5": "-", "G5": "-", "H5": "WFO", "I5": "",
		"L5": "1", "M5": "3", "P5": "3"} { // dates E–K, then L Hadir 1, M Hari Kerja 3, P Tidak Hadir 3
		if got := get(ref); got != want {
			t.Errorf("%s = %q, want %q", ref, got, want)
		}
	}
	rows, _ := f.GetRows(sheetMatrix)
	found := false
	for _, r := range rows {
		for _, c := range r {
			found = found || strings.Contains(c, "25 Des (Jumat): Natal")
		}
	}
	if !found {
		t.Error("holiday list under the legend is missing")
	}
}
