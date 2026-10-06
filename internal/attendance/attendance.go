// Package attendance holds the check-in/check-out rules and summaries. The
// persistence backend (Google Sheets) is injected via Store.
package attendance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/absensi/internal/idfmt"
)

const (
	TypeIn  = "DATANG"
	TypeOut = "PULANG"

	SourceLive   = "live"
	SourceManual = "manual"

	StatusWFO = "WFO"
	StatusWFH = "WFH"
)

type Record struct {
	At         time.Time // claimed attendance time
	RecordedAt time.Time // when the bot received the command
	TelegramID int64
	Name       string
	Phone      string // HR-registered phone (digits), for matching with HR data
	Type       string
	Source     string
	Status     string // WFO or WFH ("" for rows written before this column existed)

	HasLocation bool
	Lat, Lng    float64
	Address     string // reverse-geocoded; may be empty
}

// Store persists attendance. Read returns records whose At falls in [from, to).
type Store interface {
	Append(ctx context.Context, r Record) error
	Read(ctx context.Context, from, to time.Time) ([]Record, error)
}

const (
	// defaultHistoryDays of records are kept in memory for validation; it
	// must cover MAX_BACKDATE_DAYS so late entries can be checked (see WithBackdate).
	defaultHistoryDays = 70
	// MaxLiveShift: a check-out without an explicit time may close a
	// check-in at most this old (allows night shifts). Older open check-ins
	// mean the person forgot to check out and must state the time.
	MaxLiveShift = 16 * time.Hour
)

var (
	ErrAlreadyIn = errors.New("kamu sudah absen datang di hari itu")
	ErrNotIn     = errors.New("tidak ada absen datang yang belum ditutup sebelum waktu itu")
	ErrClosed    = errors.New("absen datang itu sudah punya absen pulang")
	ErrStaleOpen = errors.New("absen datang terakhirmu sudah lebih dari 16 jam lalu")
	ErrConflict  = errors.New("waktu itu bertabrakan dengan catatanmu yang lain")
	ErrBadPeriod = errors.New("periode tidak valid, gunakan week, month, atau YYYY-MM")
)

// IsRuleError reports whether err is a business-rule rejection (as opposed
// to an infrastructure failure).
func IsRuleError(err error) bool {
	for _, e := range []error{ErrAlreadyIn, ErrNotIn, ErrClosed, ErrStaleOpen, ErrConflict} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

type Service struct {
	store Store
	loc   *time.Location

	historyDays int

	mu     sync.Mutex // guards hist and serialises writes
	hist   map[int64][]Record
	loaded bool
}

// WithBackdate widens the in-memory history so that entries up to days in
// the past (MAX_BACKDATE_DAYS) can still be validated against their
// neighbours. Call before first use.
func (s *Service) WithBackdate(days int) *Service {
	if need := days + 14; need > s.historyDays {
		s.historyDays = need
	}
	return s
}

func NewService(store Store, loc *time.Location) *Service {
	return &Service{store: store, loc: loc, historyDays: defaultHistoryDays, hist: map[int64][]Record{}}
}

// load fills the per-user history from the sheet once.
func (s *Service) load(ctx context.Context, now time.Time) error {
	if s.loaded {
		return nil
	}
	recs, err := s.store.Read(ctx, now.AddDate(0, 0, -s.historyDays), now.AddDate(0, 0, 2))
	if err != nil {
		return err
	}
	for _, r := range recs {
		s.hist[r.TelegramID] = append(s.hist[r.TelegramID], r)
	}
	for _, h := range s.hist {
		sort.SliceStable(h, func(i, j int) bool { return h[i].At.Before(h[j].At) })
	}
	s.loaded = true
	return nil
}

// neighbours returns the user's records just before and at/after at.
func (s *Service) neighbours(tgID int64, at time.Time) (prev, next *Record) {
	h := s.hist[tgID]
	i := sort.Search(len(h), func(i int) bool { return !h[i].At.Before(at) })
	if i > 0 {
		prev = &h[i-1]
	}
	if i < len(h) {
		next = &h[i]
	}
	return prev, next
}

func (s *Service) when(r *Record) string {
	t := r.At.In(s.loc)
	return idfmt.Date(t) + " " + t.Format("15:04")
}

// typedDate is how a user would type that date for a manual entry, e.g. "5 Oktober".
func typedDate(t time.Time) string { return fmt.Sprintf("%d %s", t.Day(), idfmt.Month(t.Month())) }

// validate checks a new event against the user's neighbouring records, so
// late entries can be slotted into the past. Caller holds s.mu.
func (s *Service) validate(tgID int64, typ string, at time.Time, source string) error {
	prev, next := s.neighbours(tgID, at)
	if next != nil && next.At.Equal(at) {
		return fmt.Errorf("%w (%s %s)", ErrConflict, next.Type, s.when(next))
	}
	switch typ {
	case TypeIn:
		if prev != nil && prev.Type == TypeIn && sameDay(prev.At, at) {
			return fmt.Errorf("%w (jam %s)", ErrAlreadyIn, prev.At.In(s.loc).Format("15:04"))
		}
		if next != nil && next.Type == TypeIn && sameDay(next.At, at) {
			return fmt.Errorf("%w (jam %s)", ErrAlreadyIn, next.At.In(s.loc).Format("15:04"))
		}
		if next != nil && next.Type == TypeOut {
			return fmt.Errorf("%w: sudah ada absen pulang %s setelahnya", ErrConflict, s.when(next))
		}
	case TypeOut:
		if prev == nil {
			return ErrNotIn
		}
		if prev.Type == TypeOut {
			return fmt.Errorf("%w (pulang %s)", ErrClosed, s.when(prev))
		}
		if next != nil && next.Type == TypeOut {
			return fmt.Errorf("%w (pulang %s)", ErrClosed, s.when(next))
		}
		if source == SourceLive && at.Sub(prev.At) > MaxLiveShift {
			return fmt.Errorf("%w (datang %s). Kalau kamu lupa absen pulang hari itu, tulis jam pulangnya, mis. \"pulang %s 17.00\"",
				ErrStaleOpen, s.when(prev), typedDate(prev.At.In(s.loc)))
		}
	default:
		return fmt.Errorf("tipe tidak dikenal: %s", typ)
	}
	return nil
}

// Check validates a prospective event without storing it, so the bot can
// reject it before asking the user for a location.
func (s *Service) Check(ctx context.Context, tgID int64, typ string, at, now time.Time, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx, now); err != nil {
		return err
	}
	return s.validate(tgID, typ, at, source)
}

// Record validates and stores one event. r.At is the claimed time; RecordedAt
// is stamped here.
func (s *Service) Record(ctx context.Context, r Record, now time.Time) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx, now); err != nil {
		return Record{}, err
	}
	if err := s.validate(r.TelegramID, r.Type, r.At, r.Source); err != nil {
		return Record{}, err
	}
	r.At = r.At.In(s.loc)
	r.RecordedAt = now.In(s.loc)
	if err := s.store.Append(ctx, r); err != nil {
		return Record{}, err
	}
	h := s.hist[r.TelegramID]
	i := sort.Search(len(h), func(i int) bool { return h[i].At.After(r.At) })
	h = append(h, Record{})
	copy(h[i+1:], h[i:])
	h[i] = r
	s.hist[r.TelegramID] = h
	return r, nil
}

// OpenBefore returns the check-in that a check-out at time at would close.
func (s *Service) OpenBefore(ctx context.Context, tgID int64, at, now time.Time) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx, now); err != nil {
		return Record{}, false, err
	}
	prev, _ := s.neighbours(tgID, at)
	if prev == nil || prev.Type != TypeIn {
		return Record{}, false, nil
	}
	return *prev, true, nil
}

// ForgottenCheckouts lists the user's check-ins since `since` that never got
// a check-out (excluding a still-plausible open session).
func (s *Service) ForgottenCheckouts(ctx context.Context, tgID int64, since, now time.Time) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx, now); err != nil {
		return nil, err
	}
	h := s.hist[tgID]
	var out []Record
	for i, r := range h {
		if r.Type != TypeIn || r.At.Before(since) {
			continue
		}
		if i+1 < len(h) {
			if h[i+1].Type == TypeIn {
				out = append(out, r)
			}
		} else if now.Sub(r.At) > MaxLiveShift {
			out = append(out, r)
		}
	}
	return out, nil
}

// Between returns records with At in [from, to), sorted by time.
func (s *Service) Between(ctx context.Context, from, to time.Time) ([]Record, error) {
	recs, err := s.store.Read(ctx, from, to)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].At.Before(recs[j].At) })
	return recs, nil
}

// Period resolves "", "week", "month" or "YYYY-MM" to [from, to) and an
// Indonesian label such as "Oktober 2026" or "Minggu 5 Okt – 11 Okt 2026".
func (s *Service) Period(arg string, now time.Time) (from, to time.Time, label string, err error) {
	now = now.In(s.loc)
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "week", "minggu":
		wd := (int(now.Weekday()) + 6) % 7 // Monday = 0
		from = time.Date(now.Year(), now.Month(), now.Day()-wd, 0, 0, 0, 0, s.loc)
		to = from.AddDate(0, 0, 7)
		label = fmt.Sprintf("Minggu %s – %s", idfmt.DateShort(from), idfmt.Date(to.AddDate(0, 0, -1)))
	case "", "month", "bulan":
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, s.loc)
		to = from.AddDate(0, 1, 0)
		label = idfmt.MonthYear(from)
	default:
		t, perr := time.ParseInLocation("2006-01", arg, s.loc)
		if perr != nil {
			return from, to, "", ErrBadPeriod
		}
		from, to = t, t.AddDate(0, 1, 0)
		label = idfmt.MonthYear(from)
	}
	return from, to, label, nil
}

// Day is one attendance session: a check-in and its check-out. A person who
// checks in twice on one date has two Days for that date.
type Day struct {
	TelegramID int64
	Name       string
	Phone      string
	Date       time.Time // local midnight of the check-in (of the check-out for an orphan)
	In, Out    *Record   // either may be nil
	Working    bool      // checked in today and not yet checked out
}

// Duration is the worked time; zero unless both ends exist.
func (d Day) Duration() time.Duration {
	if d.In == nil || d.Out == nil {
		return 0
	}
	return d.Out.At.Sub(d.In.At)
}

// Incomplete reports a forgotten check-out (not counting today's open
// session) or a check-out without a check-in.
func (d Day) Incomplete() bool { return d.In == nil || (d.Out == nil && !d.Working) }

// Status is the WFO/WFH of the check-in ("" if unknown).
func (d Day) Status() string {
	if d.In != nil {
		return d.In.Status
	}
	return ""
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// Days pairs records into sessions, per user in time order. recs need not
// be sorted. An open check-in on now's date is marked Working.
func Days(recs []Record, now time.Time) []Day {
	byUser := map[int64][]Record{}
	var order []int64
	for _, r := range recs {
		if _, ok := byUser[r.TelegramID]; !ok {
			order = append(order, r.TelegramID)
		}
		byUser[r.TelegramID] = append(byUser[r.TelegramID], r)
	}
	var out []Day
	for _, id := range order {
		rs := byUser[id]
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].At.Before(rs[j].At) })
		var open *Day
		for i := range rs {
			r := &rs[i]
			switch r.Type {
			case TypeIn:
				if open != nil {
					out = append(out, *open) // previous check-in never closed
				}
				open = &Day{TelegramID: id, Name: r.Name, Phone: r.Phone, Date: midnight(r.At), In: r}
			case TypeOut:
				if open == nil {
					out = append(out, Day{TelegramID: id, Name: r.Name, Phone: r.Phone, Date: midnight(r.At), Out: r})
					continue
				}
				open.Out = r
				out = append(out, *open)
				open = nil
			}
		}
		if open != nil {
			open.Working = sameDay(open.In.At, now)
			out = append(out, *open)
		}
	}
	return out
}

type UserSummary struct {
	TelegramID int64
	Name       string
	Phone      string
	Days       int           // distinct dates with a DATANG
	Total      time.Duration // sum of completed DATANG→PULANG pairs
	Incomplete []string      // dates (6 Okt) with an unpaired event
	WFO, WFH   int           // days whose first DATANG was WFO / WFH
	Working    bool          // checked in today and not yet checked out
}

func sameDay(a, b time.Time) bool {
	b = b.In(a.Location())
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// Summarize totals Days per user. recs need not be sorted.
func Summarize(recs []Record, now time.Time) []UserSummary {
	byUser := map[int64]*UserSummary{}
	seen := map[int64]map[time.Time]bool{}
	for _, d := range Days(recs, now) {
		u := byUser[d.TelegramID]
		if u == nil {
			u = &UserSummary{TelegramID: d.TelegramID}
			byUser[d.TelegramID] = u
			seen[d.TelegramID] = map[time.Time]bool{}
		}
		u.Name, u.Phone = d.Name, d.Phone // latest wins
		if d.In != nil && !seen[d.TelegramID][d.Date] {
			seen[d.TelegramID][d.Date] = true
			u.Days++
			switch d.In.Status {
			case StatusWFO:
				u.WFO++
			case StatusWFH:
				u.WFH++
			}
		}
		u.Total += d.Duration()
		if d.Incomplete() {
			u.Incomplete = append(u.Incomplete, idfmt.DateShort(d.Date))
		}
		u.Working = u.Working || d.Working
	}
	out := make([]UserSummary, 0, len(byUser))
	for _, u := range byUser {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// WorkdaysUntil counts Monday–Friday dates in [from, to) that are not after
// now's date, skipping days for which isOff returns true (holidays; may be nil).
func WorkdaysUntil(from, to, now time.Time, isOff func(time.Time) bool) int {
	n := 0
	last := midnight(now.In(from.Location()))
	for d := from; d.Before(to) && !d.After(last); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday && (isOff == nil || !isOff(d)) {
			n++
		}
	}
	return n
}

// PeriodLabel names [from, to) in Indonesian: "September 2026",
// "Minggu 5 Okt – 11 Okt 2026", "6 Okt 2026" or "1 Sep – 15 Sep 2026".
func PeriodLabel(from, to time.Time) string {
	last := to.AddDate(0, 0, -1)
	switch {
	case from.Day() == 1 && to.Equal(from.AddDate(0, 1, 0)):
		return idfmt.MonthYear(from)
	case from.Weekday() == time.Monday && to.Equal(from.AddDate(0, 0, 7)):
		return fmt.Sprintf("Minggu %s – %s", idfmt.DateShort(from), idfmt.Date(last))
	case to.Equal(from.AddDate(0, 0, 1)):
		return idfmt.Weekday(from) + ", " + idfmt.Date(from)
	}
	return fmt.Sprintf("%s – %s", idfmt.DateShort(from), idfmt.Date(last))
}
