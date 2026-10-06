// Package report builds the attendance recap as an Excel file: a matrix of
// people × dates on the first sheet and one row per session on the second.
package report

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/absensi/internal/attendance"
	"github.com/absensi/internal/db"
	"github.com/absensi/internal/idfmt"
)

// Person is one row of the matrix.
type Person struct {
	TelegramID int64 // 0 = never verified (no attendance possible)
	Name       string
	Phone      string    // normalised digits
	Account    string    // "Aktif", "Belum verifikasi", "Dicabut", "Tidak terdaftar"
	Since      time.Time // not marked absent before this date (zero = no limit)
	Until      time.Time // nor after this date (zero = no limit)
}

type Input struct {
	Title    string // e.g. "Oktober 2026"
	From, To time.Time
	Now      time.Time
	People   []Person
	Records  []attendance.Record
	Holidays map[string]string // "YYYY-MM-DD" -> name, active holidays only
}

const (
	sheetMatrix = "Rekap"
	sheetDetail = "Detail"
	headerRow   = 4
	firstDayCol = 5 // A No, B Nama, C Telepon, D Akun, E… dates
)

// Stats are returned so the chat message can show a short summary.
type Stats struct {
	People     int
	Workdays   int
	Attendance float64 // present person-workdays / possible person-workdays
	Incomplete int     // sessions with a forgotten check-out
	WorkingNow int
}

func Build(in Input) ([]byte, Stats, error) {
	loc := in.From.Location()
	days := attendance.Days(in.Records, in.Now)
	today := time.Date(in.Now.In(loc).Year(), in.Now.In(loc).Month(), in.Now.In(loc).Day(), 0, 0, 0, 0, loc)

	var dates []time.Time
	for d := in.From; d.Before(in.To); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d)
	}
	holidayName := func(d time.Time) string { return in.Holidays[d.Format("2006-01-02")] }
	isHoliday := func(d time.Time) bool { return holidayName(d) != "" }
	workdays := attendance.WorkdaysUntil(in.From, in.To, in.Now, isHoliday)

	// Index sessions by person and date.
	type cell struct {
		status     string
		incomplete bool
		working    bool
	}
	byPerson := map[int64]map[time.Time]*cell{}
	for _, d := range days {
		if d.In == nil {
			continue // orphan check-out: listed in Detail only
		}
		m := byPerson[d.TelegramID]
		if m == nil {
			m = map[time.Time]*cell{}
			byPerson[d.TelegramID] = m
		}
		c := m[d.Date]
		if c == nil {
			c = &cell{status: d.Status()}
			m[d.Date] = c
		}
		c.incomplete = c.incomplete || d.Incomplete()
		c.working = c.working || d.Working
	}

	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", sheetMatrix); err != nil {
		return nil, Stats{}, err
	}
	st, err := newStyles(f)
	if err != nil {
		return nil, Stats{}, err
	}

	// ---- Sheet 1: matrix ----
	set := func(col, row int, v any, style int) {
		ref, _ := excelize.CoordinatesToCellName(col, row)
		_ = f.SetCellValue(sheetMatrix, ref, v)
		if style != 0 {
			_ = f.SetCellStyle(sheetMatrix, ref, ref, style)
		}
	}
	lastCol := firstDayCol + len(dates) + 6
	set(1, 1, "Rekap Kehadiran "+in.Title, st.title)
	set(1, 2, fmt.Sprintf("Periode %s – %s · %d hari kerja sampai %s · dibuat %s",
		idfmt.Date(in.From), idfmt.Date(in.To.AddDate(0, 0, -1)), workdays, idfmt.Date(today), idfmt.DateTime(in.Now)), st.note)

	heads := []string{"No", "Nama", "Telepon", "Akun"}
	for i, h := range heads {
		set(i+1, headerRow, h, st.header)
	}
	for i, d := range dates {
		style := st.header
		switch {
		case isHoliday(d):
			style = st.headerHoliday
		case isWeekend(d):
			style = st.headerWeekend
		}
		set(firstDayCol+i, headerRow, fmt.Sprintf("%d\n%s", d.Day(), idfmt.WeekdayShort(d)), style)
	}
	totals := []string{"Hadir", "Hari Kerja", "WFO", "WFH", "Tidak Hadir", "Lupa Pulang", "Total Jam"}
	for i, h := range totals {
		set(firstDayCol+len(dates)+i, headerRow, h, st.header)
	}

	var people []Person
	for _, p := range in.People { // skip people who joined after / left before the period
		if (!p.Since.IsZero() && !p.Since.Before(in.To)) || (!p.Until.IsZero() && p.Until.Before(in.From)) {
			continue
		}
		people = append(people, p)
	}
	sort.SliceStable(people, func(i, j int) bool { return strings.ToLower(people[i].Name) < strings.ToLower(people[j].Name) })

	var stats Stats
	presentTotal, possibleTotal := 0, 0
	for i, p := range people {
		row := headerRow + 1 + i
		set(1, row, i+1, st.plain)
		set(2, row, p.Name, st.plain)
		set(3, row, idfmt.Phone(p.Phone), st.plain)
		set(4, row, p.Account, st.plain)

		cells := byPerson[p.TelegramID]
		present, wfo, wfh, absent, forgot, ownWorkdays := 0, 0, 0, 0, 0, 0
		for j, d := range dates {
			col := firstDayCol + j
			if !isWeekend(d) && !isHoliday(d) && !d.After(today) && p.TelegramID != 0 && p.covers(d) {
				ownWorkdays++
			}
			c := cells[d]
			switch {
			case c != nil:
				text, style := c.status, st.wfo
				if text == "" {
					text = "H"
				}
				if c.status == attendance.StatusWFH {
					style = st.wfh
				}
				if c.incomplete {
					style = st.incomplete
					forgot++
				}
				if c.working {
					style = st.working
				}
				set(col, row, text, style)
				present++
				if c.status == attendance.StatusWFH {
					wfh++
				} else if c.status == attendance.StatusWFO {
					wfo++
				}
			case isHoliday(d):
				set(col, row, "", st.holiday)
			case d.After(today):
				set(col, row, "", st.future)
			case isWeekend(d):
				set(col, row, "", st.weekend)
			case p.TelegramID == 0 || !p.covers(d):
				set(col, row, "", st.plainCenter) // not registered/verified yet, or already left: never "absent"
			default:
				set(col, row, "-", st.absent)
				absent++
			}
		}
		var total time.Duration
		for _, d := range days {
			if d.TelegramID == p.TelegramID && p.TelegramID != 0 {
				total += d.Duration()
				if d.Working {
					stats.WorkingNow++
				}
			}
		}
		c := firstDayCol + len(dates)
		set(c, row, present, st.num)
		set(c+1, row, ownWorkdays, st.num)
		set(c+2, row, wfo, st.num)
		set(c+3, row, wfh, st.num)
		set(c+4, row, absent, st.num)
		set(c+5, row, forgot, st.num)
		set(c+6, row, total.Hours()/24, st.hours) // Excel time value, shown as [h]:mm

		stats.Incomplete += forgot
		if p.TelegramID != 0 && p.Account == "Aktif" {
			presentTotal += present
			possibleTotal += present + absent
		}
	}
	stats.People = len(people)
	stats.Workdays = workdays
	if possibleTotal > 0 {
		stats.Attendance = float64(presentTotal) / float64(possibleTotal)
	}

	// Legend under the table.
	lr := headerRow + len(people) + 2
	legend := []struct {
		text  string
		style int
	}{
		{"WFO", st.wfo}, {"Hadir di kantor", 0},
		{"WFH", st.wfh}, {"Hadir dari rumah", 0},
		{"WFO", st.incomplete}, {"Lupa absen pulang (jam hari itu tidak dihitung)", 0},
		{"WFO", st.working}, {"Sedang bekerja (belum absen pulang hari ini)", 0},
		{"-", st.absent}, {"Tidak hadir di hari kerja", 0},
		{"", st.holiday}, {"Libur nasional / cuti bersama / libur kantor", 0},
	}
	set(2, lr, "Keterangan:", st.bold)
	for i := 0; i < len(legend); i += 2 {
		r := lr + 1 + i/2
		set(2, r, legend[i].text, legend[i].style)
		set(3, r, legend[i+1].text, st.note)
	}
	set(2, lr+8, "Hari Kerja = Senin–Jumat selain hari libur, selama orang itu terdaftar (sebelum didaftarkan / setelah dicabut dibiarkan kosong). Total Jam hanya dari hari dengan absen datang dan pulang.", st.note)
	if names := holidaysIn(dates, holidayName); len(names) > 0 {
		set(2, lr+10, "Hari libur di periode ini:", st.bold)
		for i, n := range names {
			set(2, lr+11+i, n, st.note)
		}
	}

	_ = f.SetColWidth(sheetMatrix, "A", "A", 5)
	_ = f.SetColWidth(sheetMatrix, "B", "B", 26)
	_ = f.SetColWidth(sheetMatrix, "C", "C", 15)
	_ = f.SetColWidth(sheetMatrix, "D", "D", 16)
	firstDay, _ := excelize.ColumnNumberToName(firstDayCol)
	lastDay, _ := excelize.ColumnNumberToName(firstDayCol + len(dates) - 1)
	_ = f.SetColWidth(sheetMatrix, firstDay, lastDay, 5.5)
	firstTot, _ := excelize.ColumnNumberToName(firstDayCol + len(dates))
	lastTot, _ := excelize.ColumnNumberToName(lastCol)
	_ = f.SetColWidth(sheetMatrix, firstTot, lastTot, 11)
	_ = f.SetRowHeight(sheetMatrix, headerRow, 30)
	freeze, _ := excelize.CoordinatesToCellName(firstDayCol, headerRow+1)
	_ = f.SetPanes(sheetMatrix, &excelize.Panes{Freeze: true, XSplit: firstDayCol - 1, YSplit: headerRow, TopLeftCell: freeze, ActivePane: "bottomRight"})
	if len(people) > 0 {
		lastRef, _ := excelize.CoordinatesToCellName(lastCol, headerRow+len(people))
		_ = f.AutoFilter(sheetMatrix, "A4:"+lastRef, nil)
	}

	// ---- Sheet 2: one row per session ----
	if _, err := f.NewSheet(sheetDetail); err != nil {
		return nil, Stats{}, err
	}
	dh := []string{"Tanggal", "Hari", "Nama", "Telepon", "Status", "Datang", "Pulang", "Durasi", "Keterangan",
		"Lokasi Datang", "Peta Datang", "Lokasi Pulang", "Peta Pulang"}
	for i, h := range dh {
		ref, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheetDetail, ref, h)
		_ = f.SetCellStyle(sheetDetail, ref, ref, st.header)
	}
	sort.SliceStable(days, func(i, j int) bool {
		if !days[i].Date.Equal(days[j].Date) {
			return days[i].Date.Before(days[j].Date)
		}
		return strings.ToLower(days[i].Name) < strings.ToLower(days[j].Name)
	})
	for i, d := range days {
		r := i + 2
		put := func(col int, v any, style int) {
			ref, _ := excelize.CoordinatesToCellName(col, r)
			_ = f.SetCellValue(sheetDetail, ref, v)
			if style != 0 {
				_ = f.SetCellStyle(sheetDetail, ref, ref, style)
			}
		}
		put(1, idfmt.Date(d.Date), 0)
		put(2, idfmt.Weekday(d.Date), 0)
		put(3, d.Name, 0)
		put(4, idfmt.Phone(d.Phone), 0)
		put(5, d.Status(), 0)
		var notes []string
		if d.In != nil {
			put(6, d.In.At.Format("15:04"), 0)
			if d.In.Source == attendance.SourceManual {
				notes = append(notes, "datang diisi manual")
			}
			if d.In.HasLocation {
				put(10, d.In.Address, 0)
				link(f, 11, r, d.In)
			}
		}
		if d.Out != nil {
			put(7, d.Out.At.Format("15:04"), 0)
			if d.Out.Source == attendance.SourceManual {
				notes = append(notes, "pulang diisi manual")
			}
			if d.Out.HasLocation {
				put(12, d.Out.Address, 0)
				link(f, 13, r, d.Out)
			}
		}
		switch {
		case d.In == nil:
			notes = append([]string{"pulang tanpa absen datang"}, notes...)
		case d.Working:
			notes = append([]string{"sedang bekerja"}, notes...)
		case d.Out == nil:
			notes = append([]string{"lupa absen pulang"}, notes...)
		default:
			put(8, d.Duration().Hours()/24, st.hours)
		}
		put(9, strings.Join(notes, "; "), 0)
	}
	widths := []float64{13, 8, 26, 15, 8, 8, 8, 8, 28, 45, 11, 45, 11}
	for i, w := range widths {
		c, _ := excelize.ColumnNumberToName(i + 1)
		_ = f.SetColWidth(sheetDetail, c, c, w)
	}
	_ = f.SetPanes(sheetDetail, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	if len(days) > 0 {
		_ = f.AutoFilter(sheetDetail, fmt.Sprintf("A1:M%d", len(days)+1), nil)
	}

	f.SetActiveSheet(0)
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, Stats{}, err
	}
	return buf.Bytes(), stats, nil
}

func link(f *excelize.File, col, row int, r *attendance.Record) {
	ref, _ := excelize.CoordinatesToCellName(col, row)
	url := fmt.Sprintf("https://www.google.com/maps?q=%.6f,%.6f", r.Lat, r.Lng)
	_ = f.SetCellValue(sheetDetail, ref, "Buka peta")
	_ = f.SetCellHyperLink(sheetDetail, ref, url, "External")
}

func isWeekend(d time.Time) bool { return d.Weekday() == time.Saturday || d.Weekday() == time.Sunday }

type styles struct {
	title, note, bold, header, headerWeekend, headerHoliday, plain, plainCenter, num, hours int
	wfo, wfh, incomplete, working, absent, weekend, future, holiday                         int
}

func newStyles(f *excelize.File) (styles, error) {
	border := []excelize.Border{
		{Type: "left", Color: "D9D9D9", Style: 1}, {Type: "right", Color: "D9D9D9", Style: 1},
		{Type: "top", Color: "D9D9D9", Style: 1}, {Type: "bottom", Color: "D9D9D9", Style: 1},
	}
	center := &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true}
	fill := func(c string) excelize.Fill { return excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{c}} }
	hoursFmt := "[h]:mm"

	var s styles
	var err error
	mk := func(dst *int, st *excelize.Style) {
		if err != nil {
			return
		}
		*dst, err = f.NewStyle(st)
	}
	mk(&s.title, &excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}})
	mk(&s.note, &excelize.Style{Font: &excelize.Font{Color: "595959", Size: 9}})
	mk(&s.bold, &excelize.Style{Font: &excelize.Font{Bold: true}})
	mk(&s.header, &excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: fill("305496"), Alignment: center, Border: border})
	mk(&s.headerWeekend, &excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: fill("8EA9DB"), Alignment: center, Border: border})
	mk(&s.plain, &excelize.Style{Border: border, Alignment: &excelize.Alignment{Vertical: "center"}})
	mk(&s.plainCenter, &excelize.Style{Border: border, Alignment: center})
	mk(&s.num, &excelize.Style{Border: border, Alignment: center})
	mk(&s.hours, &excelize.Style{Border: border, Alignment: center, CustomNumFmt: &hoursFmt})
	mk(&s.wfo, &excelize.Style{Fill: fill("C6EFCE"), Font: &excelize.Font{Color: "006100", Size: 9}, Alignment: center, Border: border})
	mk(&s.wfh, &excelize.Style{Fill: fill("BDD7EE"), Font: &excelize.Font{Color: "1F4E78", Size: 9}, Alignment: center, Border: border})
	mk(&s.incomplete, &excelize.Style{Fill: fill("FFD966"), Font: &excelize.Font{Color: "7F6000", Size: 9, Bold: true}, Alignment: center, Border: border})
	mk(&s.working, &excelize.Style{Fill: fill("E2EFDA"), Font: &excelize.Font{Color: "375623", Size: 9, Italic: true}, Alignment: center, Border: border})
	mk(&s.absent, &excelize.Style{Fill: fill("FFC7CE"), Font: &excelize.Font{Color: "9C0006"}, Alignment: center, Border: border})
	mk(&s.weekend, &excelize.Style{Fill: fill("EDEDED"), Border: border})
	mk(&s.holiday, &excelize.Style{Fill: fill("FCE4D6"), Border: border})
	mk(&s.headerHoliday, &excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: fill("C65911"), Alignment: center, Border: border})
	mk(&s.future, &excelize.Style{Border: border})
	return s, err
}

// PeopleFrom chooses the matrix rows: active users, plus anyone with
// records in the period (revoked users, or IDs no longer in the table). If
// only is set, just that user. Each person's window is from registration
// (or first record, if earlier) until their last record if revoked, so
// nobody is marked absent before joining or after leaving.
func PeopleFrom(users []db.User, recs []attendance.Record, only *db.User) []Person {
	first := map[int64]time.Time{}
	last := map[int64]attendance.Record{}
	for _, r := range recs {
		if f, ok := first[r.TelegramID]; !ok || r.At.Before(f) {
			first[r.TelegramID] = r.At
		}
		if l, ok := last[r.TelegramID]; !ok || r.At.After(l.At) {
			last[r.TelegramID] = r
		}
	}
	var out []Person
	inTable := map[int64]bool{}
	for _, u := range users {
		if only != nil && u.ID != only.ID {
			continue
		}
		_, hasRecs := last[u.TelegramID]
		hasRecs = hasRecs && u.Verified()
		if !u.IsActive && !hasRecs {
			continue
		}
		p := Person{TelegramID: u.TelegramID, Name: u.Name, Phone: u.Phone, Account: "Aktif", Since: u.CreatedAt}
		if f, ok := first[u.TelegramID]; ok && u.Verified() && f.Before(p.Since) {
			p.Since = f
		}
		switch {
		case !u.IsActive:
			p.Account = "Dicabut"
			p.Until = last[u.TelegramID].At
		case !u.Verified():
			p.Account = "Belum verifikasi"
		}
		out = append(out, p)
		if u.Verified() {
			inTable[u.TelegramID] = true
		}
	}
	if only == nil {
		for id, r := range last {
			if !inTable[id] {
				out = append(out, Person{TelegramID: id, Name: r.Name, Phone: r.Phone, Account: "Tidak terdaftar",
					Since: first[id], Until: r.At})
			}
		}
	}
	return out
}

// covers reports whether date d (local midnight) is inside the person's window.
func (p Person) covers(d time.Time) bool {
	day := func(t time.Time) time.Time {
		t = t.In(d.Location())
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, d.Location())
	}
	if !p.Since.IsZero() && d.Before(day(p.Since)) {
		return false
	}
	if !p.Until.IsZero() && d.After(day(p.Until)) {
		return false
	}
	return true
}

// holidaysIn lists "6 Okt (Selasa): name" for the holidays among dates.
func holidaysIn(dates []time.Time, name func(time.Time) string) []string {
	var out []string
	for _, d := range dates {
		if n := name(d); n != "" {
			out = append(out, fmt.Sprintf("%s (%s): %s", idfmt.DateShort(d), idfmt.Weekday(d), n))
		}
	}
	return out
}
