package attendance

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct{ recs []Record }

func (f *fakeStore) Append(_ context.Context, r Record) error { f.recs = append(f.recs, r); return nil }
func (f *fakeStore) Read(_ context.Context, from, to time.Time) ([]Record, error) {
	var out []Record
	for _, r := range f.recs {
		if !r.At.Before(from) && r.At.Before(to) {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestRecordRules(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	at := func(d, h int) time.Time { return time.Date(2026, 1, d, h, 0, 0, 0, loc) }
	now := at(20, 18)
	ctx := context.Background()
	svc := NewService(&fakeStore{}, loc)
	add := func(typ string, d, h int, source string) error {
		_, err := svc.Record(ctx, Record{TelegramID: 1, Name: "A", Type: typ, At: at(d, h), Source: source, Status: StatusWFO}, now)
		return err
	}
	expect := func(name string, err, want error) {
		t.Helper()
		if want == nil && err != nil || want != nil && !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", name, err, want)
		}
	}

	expect("out without in", add(TypeOut, 5, 17, SourceManual), ErrNotIn)
	if err := svc.Check(ctx, 1, TypeIn, at(5, 8), now, SourceManual); err != nil || len(svc.hist[1]) != 0 {
		t.Fatalf("Check must validate without storing: %v", err)
	}
	expect("5 Jan in", add(TypeIn, 5, 8, SourceManual), nil)
	expect("second in same day", add(TypeIn, 5, 9, SourceManual), ErrAlreadyIn)

	// Forgot to check out on 5 Jan; next day's check-in is accepted anyway.
	expect("6 Jan in after forgotten out", add(TypeIn, 6, 8, SourceManual), nil)
	forgot, _ := svc.ForgottenCheckouts(ctx, 1, at(1, 0), at(6, 12)) // seen on 6 Jan at noon
	if len(forgot) != 1 || !forgot[0].At.Equal(at(5, 8)) {
		t.Fatalf("forgotten = %+v", forgot)
	}
	// ...and the 5 Jan check-out can be filled in afterwards.
	expect("backfill 5 Jan out", add(TypeOut, 5, 17, SourceManual), nil)
	if forgot, _ := svc.ForgottenCheckouts(ctx, 1, at(1, 0), at(6, 12)); len(forgot) != 0 {
		t.Errorf("still forgotten after backfill: %+v", forgot)
	}
	expect("5 Jan out twice", add(TypeOut, 5, 18, SourceManual), ErrClosed)
	expect("in before an existing out", add(TypeIn, 5, 12, SourceManual), ErrAlreadyIn)
	expect("out after an out", add(TypeOut, 5, 20, SourceManual), ErrClosed)
	expect("same timestamp", add(TypeIn, 6, 8, SourceManual), ErrConflict)

	// Live check-out more than 16h after the check-in must name the time.
	if _, err := svc.Record(ctx, Record{TelegramID: 1, Type: TypeOut, At: at(7, 15), Source: SourceLive}, at(7, 15)); !errors.Is(err, ErrStaleOpen) {
		t.Errorf("stale live out: %v", err)
	}
	// Night shift: in 22:00, live out 06:00 next day (8h) is fine.
	expect("night in", add(TypeIn, 8, 22, SourceManual), nil)
	if _, err := svc.Record(ctx, Record{TelegramID: 1, Type: TypeOut, At: at(9, 6), Source: SourceLive}, at(9, 6)); err != nil {
		t.Errorf("night shift out: %v", err)
	}
	if r, ok, _ := svc.OpenBefore(ctx, 1, at(6, 17), now); !ok || !r.At.Equal(at(6, 8)) {
		t.Errorf("OpenBefore 6 Jan = %+v %v", r, ok)
	}

	// Another user is independent.
	if _, err := svc.Record(ctx, Record{TelegramID: 2, Type: TypeIn, At: at(5, 8), Source: SourceLive}, now); err != nil {
		t.Error(err)
	}
	if !IsRuleError(ErrStaleOpen) || IsRuleError(errors.New("sheets down")) {
		t.Error("IsRuleError")
	}
}

func TestSummarize(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	at := func(d, h, m int) time.Time { return time.Date(2026, 1, d, h, m, 0, 0, loc) }
	recs := []Record{
		{TelegramID: 1, Name: "A", Type: TypeIn, At: at(5, 8, 0), Status: StatusWFO},
		{TelegramID: 1, Name: "A", Type: TypeOut, At: at(5, 17, 30)},
		{TelegramID: 1, Name: "A", Type: TypeIn, At: at(6, 8, 0), Status: StatusWFH},
		{TelegramID: 1, Name: "A", Type: TypeOut, At: at(6, 16, 0)},
		{TelegramID: 1, Name: "A", Type: TypeIn, At: at(7, 8, 0)},   // never closed
		{TelegramID: 2, Name: "B", Type: TypeOut, At: at(5, 17, 0)}, // orphan out
	}
	got := Summarize(recs, at(20, 12, 0))
	if len(got) != 2 {
		t.Fatalf("users = %d", len(got))
	}
	a := got[0]
	if a.WFO != 1 || a.WFH != 1 {
		t.Errorf("A wfo/wfh = %d/%d", a.WFO, a.WFH)
	}
	if a.Days != 3 || a.Total != 17*time.Hour+30*time.Minute || len(a.Incomplete) != 1 || a.Incomplete[0] != "7 Jan" || a.Working {
		t.Errorf("A = %+v", a)
	}
	b := got[1]
	if b.Days != 0 || len(b.Incomplete) != 1 {
		t.Errorf("B = %+v", b)
	}

	// Same data viewed at noon on 7 Jan: the open check-in is "still working".
	a = Summarize(recs, at(7, 12, 0))[0]
	if len(a.Incomplete) != 0 || !a.Working {
		t.Errorf("A on 7 Jan = %+v", a)
	}
}

func TestPeriodWeekAcrossMonth(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	svc := NewService(&fakeStore{}, loc)
	// Wed 1 Oct 2025 -> week Mon 29 Sep .. Mon 6 Oct (spans two months)
	from, to, _, err := svc.Period("week", time.Date(2025, 10, 1, 10, 0, 0, 0, loc))
	if err != nil || from.Day() != 29 || from.Month() != time.September || to.Day() != 6 {
		t.Errorf("from=%v to=%v err=%v", from, to, err)
	}
	if _, _, _, err := svc.Period("bogus", time.Now()); !errors.Is(err, ErrBadPeriod) {
		t.Errorf("err = %v", err)
	}
}

func TestPeriodLabel(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	d := func(m time.Month, day int) time.Time { return time.Date(2026, m, day, 0, 0, 0, 0, loc) }
	for want, p := range map[string][2]time.Time{
		"September 2026":             {d(9, 1), d(10, 1)},
		"Minggu 5 Okt – 11 Okt 2026": {d(10, 5), d(10, 12)},
		"Selasa, 6 Okt 2026":         {d(10, 6), d(10, 7)},
		"1 Sep – 15 Sep 2026":        {d(9, 1), d(9, 16)},
	} {
		if got := PeriodLabel(p[0], p[1]); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
