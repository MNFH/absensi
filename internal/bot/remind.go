package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/absensi/internal/attendance"
	"github.com/absensi/internal/db"
	"github.com/absensi/internal/idfmt"
)

// HR attendance reminders in groups: /ingatkan (now) and /jadwal (automatic,
// Monday–Friday). Only members of that group who still have to check in (or
// out) are tagged.

const (
	DefaultRemindIn  = "12:00"
	DefaultRemindOut = "23:00"

	remindCatchUp   = 30 * time.Minute // still send if the bot was down at the scheduled minute
	mentionsPerMsg  = 30               // keep each message's mention list modest
	memberCacheTTL  = 30 * time.Minute
	schedulerPeriod = 30 * time.Second
)

const groupAdminHelp = `Perintah HR di grup ini:
/ingatkan — tag anggota yang belum absen datang hari ini
/ingatkan pulang — tag yang sudah datang tapi belum absen pulang
/jadwal — lihat jadwal pengingat otomatis
/jadwal on — aktifkan (datang ` + DefaultRemindIn + `, pulang ` + DefaultRemindOut + `, Senin–Jumat)
/jadwal datang 12:00 · /jadwal pulang 23:00 — ubah jam
/jadwal datang off · /jadwal pulang off · /jadwal off — matikan`

type memberEntry struct {
	ok bool
	at time.Time
}

// groupCommand handles HR commands in a group. It returns handled=false when
// the message isn't one of them (the caller then points the user to the
// private chat).
func (h *Handler) groupCommand(c tele.Context) (handled bool, err error) {
	fields := strings.Fields(c.Message().Text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false, nil
	}
	cmd, _, _ := strings.Cut(strings.ToLower(fields[0]), "@")
	args := fields[1:]
	if cmd != "/ingatkan" && cmd != "/jadwal" && cmd != "/help" {
		return false, nil
	}

	ctx, cancel := ctxTimeout()
	defer cancel()
	u, uerr := h.users.GetByTelegramID(ctx, c.Sender().ID)
	isAdmin := uerr == nil && u.IsActive && u.Role == db.RoleAdmin
	if !isAdmin {
		return false, nil // regular users get the usual "chat me privately" reply
	}

	switch cmd {
	case "/help":
		return true, c.Reply(groupAdminHelp)
	case "/ingatkan":
		kind := db.RemindIn
		if len(args) > 0 && strings.EqualFold(args[0], "pulang") {
			kind = db.RemindOut
		}
		return true, h.sendReminder(ctx, c.Chat(), kind, false)
	default:
		return true, h.cmdSchedule(ctx, c, u, args)
	}
}

func (h *Handler) cmdSchedule(ctx context.Context, c tele.Context, admin *db.User, args []string) error {
	chat := c.Chat()
	set := func(kind, clock string) error {
		return h.users.SetReminderTime(ctx, admin.TelegramID, chat.ID, chat.Title, kind, clock)
	}
	var err error
	switch {
	case len(args) == 0:
		// show only
	case len(args) == 1 && strings.EqualFold(args[0], "on"):
		if err = set(db.RemindIn, DefaultRemindIn); err == nil {
			err = set(db.RemindOut, DefaultRemindOut)
		}
	case len(args) == 1 && strings.EqualFold(args[0], "off"):
		if err = set(db.RemindIn, ""); err == nil {
			err = set(db.RemindOut, "")
		}
	case len(args) == 2 && (strings.EqualFold(args[0], "datang") || strings.EqualFold(args[0], "pulang")):
		kind := db.RemindIn
		if strings.EqualFold(args[0], "pulang") {
			kind = db.RemindOut
		}
		clock := ""
		if !strings.EqualFold(args[1], "off") {
			if clock, err = db.ParseClock(args[1]); err != nil {
				return c.Reply("⚠️ " + err.Error())
			}
		}
		err = set(kind, clock)
	default:
		return c.Reply(groupAdminHelp)
	}
	if err != nil {
		log.Printf("schedule %d: %v", chat.ID, err)
		return c.Reply("Terjadi kesalahan sistem.")
	}

	g, gerr := h.users.GetReminderGroup(ctx, chat.ID)
	if errors.Is(gerr, db.ErrNotFound) || (gerr == nil && g.InTime == "" && g.OutTime == "") {
		return c.Reply("Pengingat otomatis di grup ini: nonaktif.\nAktifkan dengan /jadwal on (datang " + DefaultRemindIn + ", pulang " + DefaultRemindOut + ").")
	}
	if gerr != nil {
		return c.Reply("Terjadi kesalahan sistem.")
	}
	show := func(s string) string {
		if s == "" {
			return "nonaktif"
		}
		return "jam " + s
	}
	return c.Reply(fmt.Sprintf("Pengingat otomatis di grup ini (Senin–Jumat):\n⏰ Belum absen datang: %s\n🌙 Belum absen pulang: %s",
		show(g.InTime), show(g.OutTime)))
}

// sendReminder tags the group's members who still need to check in
// (RemindIn) or who checked in today but not out (RemindOut). When auto is
// true and nobody needs reminding, nothing is posted.
func (h *Handler) sendReminder(ctx context.Context, chat *tele.Chat, kind string, auto bool) error {
	now := time.Now().In(h.loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.loc)
	recs, err := h.att.Between(ctx, today, today.AddDate(0, 0, 1))
	if err != nil {
		log.Printf("reminder %d: read sheet: %v", chat.ID, err)
		if auto {
			return err
		}
		_, err = h.bot.Send(chat, "Gagal membaca spreadsheet, coba lagi.")
		return err
	}
	users, err := h.users.ListUsers(ctx)
	if err != nil {
		return err
	}

	checkedIn := map[int64]bool{}
	working := map[int64]bool{}
	for _, d := range attendance.Days(recs, now) {
		if d.In != nil {
			checkedIn[d.TelegramID] = true
		}
		if d.Working {
			working[d.TelegramID] = true
		}
	}
	var targets []db.User
	for _, u := range users {
		if !u.IsActive || u.TelegramID <= 0 {
			continue
		}
		need := !checkedIn[u.TelegramID]
		if kind == db.RemindOut {
			need = working[u.TelegramID]
		}
		if need && h.isMember(chat.ID, u.TelegramID) {
			targets = append(targets, u)
		}
	}

	date := idfmt.Weekday(now) + ", " + idfmt.Date(now)
	if len(targets) == 0 {
		if auto {
			return nil
		}
		msg := "✅ Semua anggota grup ini sudah absen datang hari ini."
		if kind == db.RemindOut {
			msg = "✅ Tidak ada anggota grup ini yang belum absen pulang hari ini."
		}
		_, err := h.bot.Send(chat, msg)
		return err
	}

	head := fmt.Sprintf("⏰ <b>Pengingat absen datang</b> · %s\nBelum absen datang (%d orang):", date, len(targets))
	foot := "Yuk absen lewat chat pribadi dengan bot 👇"
	if kind == db.RemindOut {
		head = fmt.Sprintf("🌙 <b>Pengingat absen pulang</b> · %s\nSudah datang tapi belum absen pulang (%d orang):", date, len(targets))
		foot = "Kalau sudah selesai kerja, absen pulang lewat chat pribadi 👇\nJam pulang bisa diisi manual, mis. \"pulang jam 17.30\"."
	}
	for i := 0; i < len(targets); i += mentionsPerMsg {
		end := min(i+mentionsPerMsg, len(targets))
		var tags []string
		for _, u := range targets[i:end] {
			tags = append(tags, fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, u.TelegramID, html.EscapeString(u.Name)))
		}
		text := strings.Join(tags, ", ")
		if i == 0 {
			text = head + "\n\n" + text
		}
		opts := []any{tele.ModeHTML}
		if end == len(targets) {
			text += "\n\n" + foot
			opts = append(opts, h.privateChatButton())
		}
		if _, err := h.bot.Send(chat, text, opts...); err != nil {
			return err
		}
	}
	log.Printf("reminder %s sent to group %d (%s): %d people", kind, chat.ID, chat.Title, len(targets))
	return nil
}

// isMember reports whether user is in the group, cached for memberCacheTTL
// (one API call per person otherwise).
func (h *Handler) isMember(chatID, userID int64) bool {
	key := fmt.Sprintf("%d:%d", chatID, userID)
	h.mu.Lock()
	e, ok := h.members[key]
	h.mu.Unlock()
	if ok && time.Since(e.at) < memberCacheTTL {
		return e.ok
	}
	m, err := h.bot.ChatMemberOf(&tele.Chat{ID: chatID}, &tele.User{ID: userID})
	member := false
	if err == nil {
		switch m.Role {
		case tele.Creator, tele.Administrator, tele.Member:
			member = true
		case tele.Restricted:
			member = m.Member
		}
	}
	h.mu.Lock()
	h.members[key] = memberEntry{ok: member, at: time.Now()}
	h.mu.Unlock()
	return member
}

// runScheduler sends the automatic reminders until stop is closed.
func (h *Handler) runScheduler(stop <-chan struct{}) {
	t := time.NewTicker(schedulerPeriod)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			h.schedulerTick(time.Now().In(h.loc))
		}
	}
}

func (h *Handler) schedulerTick(now time.Time) {
	if wd := now.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	groups, err := h.users.ListReminderGroups(ctx)
	if err != nil {
		log.Printf("scheduler: %v", err)
		return
	}
	if len(groups) == 0 || h.holidayToday(ctx, now) != "" {
		return // no automatic reminders on holidays
	}
	for _, g := range groups {
		for _, kind := range []string{db.RemindIn, db.RemindOut} {
			clock := g.InTime
			if kind == db.RemindOut {
				clock = g.OutTime
			}
			if !dueNow(clock, now) {
				continue
			}
			// Mark first so two ticks (or a restart) never send twice.
			if ok, err := h.users.MarkReminderSent(ctx, g.ChatID, kind, now); err != nil || !ok {
				continue
			}
			err := h.sendReminder(ctx, &tele.Chat{ID: g.ChatID, Title: g.Title, Type: tele.ChatSuperGroup}, kind, true)
			if err != nil {
				log.Printf("reminder %s to group %d: %v", kind, g.ChatID, err)
				if botRemoved(err) {
					_ = h.users.DeleteReminderGroup(ctx, g.ChatID)
					log.Printf("bot is no longer in group %d; its reminders were removed", g.ChatID)
				}
			}
		}
	}
}

// dueNow reports whether clock ("HH:MM", "" = off) is within the catch-up
// window ending at now.
func dueNow(clock string, now time.Time) bool {
	if clock == "" {
		return false
	}
	t, err := time.Parse("15:04", clock)
	if err != nil {
		return false
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	return !now.Before(at) && now.Before(at.Add(remindCatchUp))
}

func botRemoved(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "kicked") || strings.Contains(s, "chat not found") ||
		strings.Contains(s, "not a member") || strings.Contains(s, "group chat was upgraded")
}
