package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/absensi/internal/db"
	"github.com/absensi/internal/holiday"
	"github.com/absensi/internal/idfmt"
	"github.com/absensi/internal/timeparse"
)

const maxHolidayRange = 31 // days per /libur tambah or hapus

const liburHelp = `Hari libur:
/libur — daftar libur 3 bulan ke depan
/libur 2027 — semua libur di tahun itu

Khusus HR:
/libur tambah <tanggal> <keterangan>
  mis. /libur tambah 2 Januari Libur kantor
       /libur tambah 29 Desember sampai 31 Desember Tutup akhir tahun
/libur hapus <tanggal> — jadikan hari kerja biasa (mis. cuti bersama tetap masuk)
  mis. /libur hapus 24 Desember

Tanggal bisa ditulis 2026-12-24, 24-12-2026, 24/12/2026 atau 24 Desember [2026].
Tanpa tahun: tahun ini, atau tahun depan kalau tanggalnya sudah lewat.`

// SeedHolidays loads the official SKB defaults. Idempotent: days HR changed
// or cancelled are left alone.
func SeedHolidays(ctx context.Context, store *db.Store) error {
	days := make([]db.Holiday, 0, len(holiday.Defaults))
	for _, d := range holiday.Defaults {
		t, err := time.Parse("2006-01-02", d.Date)
		if err != nil {
			return err
		}
		days = append(days, db.Holiday{Day: t, Name: d.Name, Kind: d.Kind})
	}
	return store.SeedHolidays(ctx, days)
}

// holidayNames returns active holidays in [from, to) keyed "YYYY-MM-DD".
func (h *Handler) holidayNames(ctx context.Context, from, to time.Time) map[string]string {
	hs, err := h.users.HolidaysBetween(ctx, from, to)
	if err != nil {
		log.Printf("holidays: %v", err)
		return nil
	}
	out := map[string]string{}
	for _, d := range hs {
		if d.Active {
			out[d.Key()] = d.Name
		}
	}
	return out
}

// holidayToday returns today's holiday name, or "".
func (h *Handler) holidayToday(ctx context.Context, now time.Time) string {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.loc)
	return h.holidayNames(ctx, day, day.AddDate(0, 0, 1))[day.Format("2006-01-02")]
}

func (h *Handler) cmdLibur(c tele.Context) error {
	args := c.Args()
	isAdmin := user(c).Role == db.RoleAdmin
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "tambah", "add":
			if !isAdmin {
				return c.Send("Mengubah hari libur hanya untuk HR.")
			}
			return h.liburTambah(c, args[1:])
		case "hapus", "batal", "remove":
			if !isAdmin {
				return c.Send("Mengubah hari libur hanya untuk HR.")
			}
			return h.liburHapus(c, args[1:])
		case "help", "bantuan":
			return c.Send(liburHelp)
		}
	}

	ctx, cancel := ctxTimeout()
	defer cancel()
	now := time.Now().In(h.loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.loc)
	from, to, title := today, today.AddDate(0, 3, 0), "Libur 3 bulan ke depan"
	if len(args) == 1 {
		y, err := strconv.Atoi(args[0])
		if err != nil || y < 2000 || y > 2100 {
			return c.Send(liburHelp)
		}
		from = time.Date(y, 1, 1, 0, 0, 0, 0, h.loc)
		to, title = from.AddDate(1, 0, 0), fmt.Sprintf("Libur tahun %d", y)
	}
	hs, err := h.users.HolidaysBetween(ctx, from, to)
	if err != nil {
		return adminErr(c, "libur", err)
	}
	var sb strings.Builder
	sb.WriteString(title + ":\n")
	n := 0
	for _, d := range hs {
		t := time.Date(d.Day.Year(), d.Day.Month(), d.Day.Day(), 0, 0, 0, 0, h.loc)
		if !d.Active {
			if isAdmin { // HR also sees the days they turned into working days
				fmt.Fprintf(&sb, "• %s, %s — %s ❌ dibatalkan (hari kerja)\n", idfmt.WeekdayShort(t), idfmt.Date(t), d.Name)
			}
			continue
		}
		n++
		fmt.Fprintf(&sb, "• %s, %s — %s (%s)\n", idfmt.WeekdayShort(t), idfmt.Date(t), d.Name, holiday.KindLabel(d.Kind))
	}
	if n == 0 {
		sb.WriteString("(tidak ada)\n")
	}
	if isAdmin {
		sb.WriteString("\nUbah: /libur tambah … · /libur hapus … · /libur help")
	}
	return sendLong(c, sb.String())
}

func (h *Handler) liburTambah(c tele.Context, args []string) error {
	from, to, rest, err := parseDayRange(args, time.Now().In(h.loc))
	if err != nil {
		return c.Send("⚠️ " + err.Error() + "\n\n" + liburHelp)
	}
	name := strings.TrimSpace(strings.Join(rest, " "))
	if name == "" {
		return c.Send("Tulis keterangannya, mis. /libur tambah 2 Januari Libur kantor")
	}
	if days := int(to.Sub(from).Hours()/24) + 1; days > maxHolidayRange {
		return c.Send(fmt.Sprintf("Maksimal %d hari sekaligus.", maxHolidayRange))
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	var added []string
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if err := h.users.SetHoliday(ctx, user(c).TelegramID, d, name); err != nil {
			return adminErr(c, "libur tambah", err)
		}
		added = append(added, idfmt.WeekdayShort(d)+", "+idfmt.Date(d))
	}
	return c.Send(fmt.Sprintf("✅ Ditetapkan sebagai libur (%s):\n• %s\n\nTidak dihitung sebagai hari kerja di rekap, dan tidak ada pengingat otomatis.",
		name, strings.Join(added, "\n• ")))
}

func (h *Handler) liburHapus(c tele.Context, args []string) error {
	from, to, rest, err := parseDayRange(args, time.Now().In(h.loc))
	if err == nil && len(rest) > 0 {
		err = errors.New("tanggal tidak dikenali")
	}
	if err != nil {
		return c.Send("⚠️ " + err.Error() + "\n\n" + liburHelp)
	}
	if days := int(to.Sub(from).Hours()/24) + 1; days > maxHolidayRange {
		return c.Send(fmt.Sprintf("Maksimal %d hari sekaligus.", maxHolidayRange))
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	var done, missing []string
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		name, err := h.users.CancelHoliday(ctx, user(c).TelegramID, d)
		switch {
		case errors.Is(err, db.ErrNotFound):
			missing = append(missing, idfmt.Date(d))
		case err != nil:
			return adminErr(c, "libur hapus", err)
		default:
			done = append(done, fmt.Sprintf("%s, %s (%s)", idfmt.WeekdayShort(d), idfmt.Date(d), name))
		}
	}
	var sb strings.Builder
	if len(done) > 0 {
		sb.WriteString("✅ Sekarang hari kerja biasa:\n• " + strings.Join(done, "\n• "))
	}
	if len(missing) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("Bukan hari libur: " + strings.Join(missing, ", "))
	}
	return c.Send(sb.String())
}

var numericDate = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$|^(\d{1,2})[-/](\d{1,2})[-/](\d{4})$`)

// parseDayRange reads "<date> [sampai|s/d|- <date>]" from the start of args
// and returns the remaining words.
func parseDayRange(args []string, now time.Time) (from, to time.Time, rest []string, err error) {
	from, n, err := parseDay(args, now)
	if err != nil {
		return
	}
	rest = args[n:]
	to = from
	if len(rest) > 0 {
		switch strings.ToLower(rest[0]) {
		case "sampai", "s/d", "sd", "-", "hingga":
			var m int
			to, m, err = parseDay(rest[1:], now)
			if err != nil {
				return
			}
			rest = rest[1+m:]
			if to.Before(from) {
				err = errors.New("tanggal akhir sebelum tanggal awal")
			}
		}
	}
	return
}

// parseDay reads one date from the start of args: 2026-12-24, 24-12-2026,
// 24/12/2026, or "24 Desember [2026]". Returns how many words it used.
func parseDay(args []string, now time.Time) (time.Time, int, error) {
	bad := errors.New("tanggal tidak dikenali")
	if len(args) == 0 {
		return time.Time{}, 0, errors.New("tanggal belum ditulis")
	}
	mk := func(y, m, d int) (time.Time, error) {
		t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, now.Location())
		if m < 1 || m > 12 || t.Day() != d || t.Month() != time.Month(m) {
			return time.Time{}, errors.New("tanggal tidak valid")
		}
		return t, nil
	}
	if g := numericDate.FindStringSubmatch(args[0]); g != nil {
		a := func(i int) int { v, _ := strconv.Atoi(g[i]); return v }
		var t time.Time
		var err error
		if g[1] != "" {
			t, err = mk(a(1), a(2), a(3))
		} else {
			t, err = mk(a(6), a(5), a(4))
		}
		return t, 1, err
	}
	if len(args) < 2 {
		return time.Time{}, 0, bad
	}
	day, err := strconv.Atoi(args[0])
	month, ok := timeparse.MonthByName(args[1])
	if err != nil || !ok {
		return time.Time{}, 0, bad
	}
	if len(args) >= 3 {
		if y, err := strconv.Atoi(args[2]); err == nil && y >= 2000 && y <= 2100 {
			t, err := mk(y, int(month), day)
			return t, 3, err
		}
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	t, err := mk(now.Year(), int(month), day)
	if err == nil && t.Before(today) {
		t, err = mk(now.Year()+1, int(month), day) // planning ahead: next occurrence
	}
	return t, 2, err
}
