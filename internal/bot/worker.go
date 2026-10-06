package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/absensi/internal/attendance"
	"github.com/absensi/internal/idfmt"
)

const timeHint = "Format waktu: <tanggal> <bulan> <jam>, contoh:\n" +
	"  /datang 5 Januari 08.00 wfo\n  /pulang 5 Januari 5.00PM\n  /pulang 5 Januari 17.00"

func fmtTime(t time.Time) string { return idfmt.DateTime(t) }

func (h *Handler) cmdStatus(c tele.Context) error {
	u := user(c)
	ctx, cancel := ctxTimeout()
	defer cancel()
	now := time.Now().In(h.loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.loc)
	recs, err := h.att.Between(ctx, from, from.AddDate(0, 0, 1))
	if err != nil {
		log.Printf("status: %v", err)
		return c.Send("Gagal membaca spreadsheet, coba lagi.")
	}
	var lines []string
	for _, r := range recs {
		if r.TelegramID == u.TelegramID {
			lines = append(lines, strings.TrimSpace(fmt.Sprintf("• %s %s %s", r.Type, r.At.Format("15:04:05"), statusLabel(r.Status))))
		}
	}
	if len(lines) == 0 {
		return c.Send("Belum ada catatan hari ini.")
	}
	return c.Send("Catatan hari ini:\n" + strings.Join(lines, "\n"))
}

func (h *Handler) cmdHistory(c tele.Context) error {
	u := user(c)
	ctx, cancel := ctxTimeout()
	defer cancel()
	now := time.Now().In(h.loc)
	from := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, h.loc)
	recs, err := h.att.Between(ctx, from, now.Add(time.Hour))
	if err != nil {
		log.Printf("history: %v", err)
		return c.Send("Gagal membaca spreadsheet, coba lagi.")
	}
	var mine []attendance.Record
	for _, r := range recs {
		if r.TelegramID == u.TelegramID {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return c.Send("Belum ada riwayat.")
	}
	if len(mine) > 10 {
		mine = mine[len(mine)-10:]
	}
	lines := make([]string, len(mine))
	for i, r := range mine {
		lines[i] = strings.TrimSpace(fmt.Sprintf("• %s  %s %s", idfmt.DateShort(r.At)+" "+r.At.Format("15:04"), r.Type, statusLabel(r.Status)))
	}
	return c.Send("10 catatan terakhir:\n" + strings.Join(lines, "\n"))
}
