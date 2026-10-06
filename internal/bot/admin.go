package bot

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/absensi/internal/attendance"
	"github.com/absensi/internal/db"
	"github.com/absensi/internal/idfmt"
	"github.com/absensi/internal/nlu"
	"github.com/absensi/internal/report"
)

// adminErr turns store errors into a user-facing reply.
func adminErr(c tele.Context, what string, err error) error {
	switch {
	case errors.Is(err, db.ErrNotFound), errors.Is(err, db.ErrExists), errors.Is(err, db.ErrSelfAction),
		errors.Is(err, db.ErrLastAdmin), errors.Is(err, db.ErrInvalidRole), errors.Is(err, db.ErrBadPhone):
		return c.Send("⚠️ " + err.Error())
	}
	log.Printf("%s: %v", what, err)
	return c.Send("Terjadi kesalahan sistem.")
}

func (h *Handler) cmdAddUser(c tele.Context) error {
	args := c.Args()
	const usage = "Format: /adduser <nomor telepon> <nama>\nContoh: /adduser 08123456789 Budi Santoso"
	if len(args) < 2 {
		return c.Send(usage)
	}
	name := strings.Join(args[1:], " ")
	ctx, cancel := ctxTimeout()
	defer cancel()
	if err := h.users.AddUser(ctx, user(c).TelegramID, args[0], name); err != nil {
		return adminErr(c, "adduser", err)
	}
	phone, _ := db.NormalizePhone(args[0])
	return c.Send(fmt.Sprintf("✅ %s (%s) didaftarkan sebagai worker.\nMinta mereka kirim /start ke bot lalu bagikan nomor teleponnya untuk verifikasi.", name, phone))
}

// refreshMenu re-reads the user and updates their Telegram command menu.
func (h *Handler) refreshMenu(ref string) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	if u, err := h.users.Resolve(ctx, ref); err == nil {
		h.syncCommands(u)
	}
}

func (h *Handler) refCmd(c tele.Context, usage, what string, nargs int, fn func(ctx ctxT, actor int64, args []string) error, okMsg string) error {
	if len(c.Args()) < nargs {
		return c.Send(usage)
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	if err := fn(ctx, user(c).TelegramID, c.Args()); err != nil {
		return adminErr(c, what, err)
	}
	h.refreshMenu(c.Args()[0])
	return c.Send(fmt.Sprintf(okMsg, c.Args()[0]))
}

func (h *Handler) cmdRevoke(c tele.Context) error {
	return h.refCmd(c, "Format: /revoke <nomor telepon>", "revoke", 1,
		func(ctx ctxT, a int64, args []string) error { return h.users.SetActive(ctx, a, args[0], false) },
		"✅ Akses %s dicabut.")
}

func (h *Handler) cmdGrant(c tele.Context) error {
	return h.refCmd(c, "Format: /grant <nomor telepon>", "grant", 1,
		func(ctx ctxT, a int64, args []string) error { return h.users.SetActive(ctx, a, args[0], true) },
		"✅ Akses %s diaktifkan.")
}

func (h *Handler) cmdSetRole(c tele.Context) error {
	return h.refCmd(c, "Format: /setrole <nomor telepon> admin|worker", "setrole", 2,
		func(ctx ctxT, a int64, args []string) error {
			return h.users.SetRole(ctx, a, args[0], strings.ToLower(args[1]))
		}, "✅ Role %s diubah.")
}

func (h *Handler) cmdSetName(c tele.Context) error {
	return h.refCmd(c, "Format: /setname <nomor telepon> <nama>", "setname", 2,
		func(ctx ctxT, a int64, args []string) error {
			return h.users.SetName(ctx, a, args[0], strings.Join(args[1:], " "))
		}, "✅ Nama %s diubah.")
}

func (h *Handler) cmdSetPhone(c tele.Context) error {
	return h.refCmd(c, "Format: /setphone <nomor lama> <nomor baru>\nAkun Telegram-nya akan dilepas; pekerja perlu verifikasi ulang dengan nomor baru.", "setphone", 2,
		func(ctx ctxT, a int64, args []string) error { return h.users.SetPhone(ctx, a, args[0], args[1]) },
		"✅ Nomor %s diganti. Pekerja harus verifikasi ulang.")
}

func (h *Handler) cmdUnbind(c tele.Context) error {
	return h.refCmd(c, "Format: /unbind <nomor telepon>\nMelepas akun Telegram yang terhubung (mis. pekerja ganti akun); verifikasi ulang diperlukan.", "unbind", 1,
		func(ctx ctxT, a int64, args []string) error { return h.users.Unbind(ctx, a, args[0]) },
		"✅ Akun Telegram untuk %s dilepas.")
}

func (h *Handler) cmdUsers(c tele.Context) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	us, err := h.users.ListUsers(ctx)
	if err != nil {
		return adminErr(c, "users", err)
	}
	var sb strings.Builder
	for _, u := range us {
		mark := "✅"
		switch {
		case !u.IsActive:
			mark = "⛔"
		case !u.Verified():
			mark = "⏳"
		}
		id := u.Phone
		if id == "" {
			id = "ID " + itoa(u.TelegramID)
		}
		fmt.Fprintf(&sb, "%s %s [%s] %s", mark, u.Name, u.Role, id)
		if u.IsActive && !u.Verified() {
			sb.WriteString(" (belum verifikasi)")
		}
		sb.WriteByte('\n')
	}
	if sb.Len() == 0 {
		return c.Send("Belum ada pengguna.")
	}
	return sendLong(c, "✅ aktif  ⏳ belum verifikasi  ⛔ dicabut\n\n"+sb.String())
}

func (h *Handler) cmdAudit(c tele.Context) error {
	n := 15
	if len(c.Args()) > 0 {
		if v, err := strconv.Atoi(c.Args()[0]); err == nil && v > 0 && v <= 100 {
			n = v
		}
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	es, err := h.users.RecentAudit(ctx, n)
	if err != nil {
		return adminErr(c, "audit", err)
	}
	if len(es) == 0 {
		return c.Send("Log kosong.")
	}
	var sb strings.Builder
	for _, e := range es {
		fmt.Fprintf(&sb, "%s  %s oleh %s", idfmt.DateShort(e.At.In(h.loc))+" "+e.At.In(h.loc).Format("15:04"), e.Action, e.Actor)
		if e.Target != "" && e.Target != e.Actor {
			sb.WriteString(" → " + e.Target)
		}
		if e.Detail != "{}" {
			sb.WriteString(" " + e.Detail)
		}
		sb.WriteByte('\n')
	}
	return sendLong(c, sb.String())
}

// cmdSummary sends the recap as an Excel file (a chat list doesn't scale to
// hundreds of people) plus a short overview in the caption.
func (h *Handler) cmdSummary(c tele.Context) error {
	ctx, cancel := ctxTimeout()
	defer cancel()

	// Arguments: a period (week | month | YYYY-MM) and/or one user's phone.
	period, ref := "", ""
	for _, a := range c.Args() {
		switch l := strings.ToLower(a); {
		case l == "week" || l == "minggu" || l == "month" || l == "bulan" || (len(l) == 7 && l[4] == '-'):
			period = l
		default:
			ref = a
		}
	}
	var only *db.User
	if ref != "" {
		u, err := h.users.Resolve(ctx, ref)
		if err != nil {
			return adminErr(c, "summary", err)
		}
		only = u
	}
	from, to, label, err := h.att.Period(period, time.Now().In(h.loc))
	if err != nil {
		return c.Send(err.Error())
	}
	return h.sendSummary(c, from, to, label, only)
}

// summaryFromIntent serves a free-text recap request ("rekap fajar bulan
// september"). Read-only, admins only.
func (h *Handler) summaryFromIntent(c tele.Context, in nlu.Intent) error {
	if user(c).Role != db.RoleAdmin {
		return c.Send("Rekap kehadiran hanya untuk HR. Catatanmu sendiri bisa dilihat dengan /riwayat.")
	}
	now := time.Now().In(h.loc)
	from, to, label, _ := h.att.Period("month", now)
	if !in.From.IsZero() {
		from, to = in.From, in.To.AddDate(0, 0, 1) // To is inclusive
		label = attendance.PeriodLabel(from, to)
	}
	switch {
	case from.After(now):
		return c.Send(fmt.Sprintf("Periode %s belum dimulai.", label))
	case to.Sub(from) > maxSummaryDays*24*time.Hour:
		return c.Send(fmt.Sprintf("Periode %s terlalu panjang. Maksimal %d hari per rekap, coba per bulan.", label, maxSummaryDays))
	}

	var only *db.User
	if in.Person != "" {
		ctx, cancel := ctxTimeout()
		us, err := h.users.ListUsers(ctx)
		cancel()
		if err != nil {
			return adminErr(c, "summary", err)
		}
		matches := matchUsers(us, in.Person)
		switch len(matches) {
		case 0:
			return c.Send(fmt.Sprintf("Tidak ada karyawan bernama atau bernomor %q. Cek daftar dengan /users.", in.Person))
		case 1:
			only = &matches[0]
		default:
			var sb strings.Builder
			fmt.Fprintf(&sb, "Ada %d karyawan yang cocok dengan %q:\n", len(matches), in.Person)
			for i, u := range matches {
				if i == 10 {
					fmt.Fprintf(&sb, "…dan %d lainnya\n", len(matches)-10)
					break
				}
				fmt.Fprintf(&sb, "• %s (%s)\n", u.Name, idfmt.Phone(u.Phone))
			}
			sb.WriteString("\nSebutkan nama lengkap atau nomor HP-nya.")
			return c.Send(sb.String())
		}
	}
	return h.sendSummary(c, from, to, label, only)
}

const maxSummaryDays = 93 // about three months; a wider matrix becomes unreadable

// matchUsers finds employees by phone number or name: an exact (case
// insensitive) name wins, otherwise every word of the query must appear in
// the name.
func matchUsers(users []db.User, q string) []db.User {
	if p, err := db.NormalizePhone(q); err == nil {
		for _, u := range users {
			if u.Phone == p {
				return []db.User{u}
			}
		}
	}
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	q = norm(q)
	if q == "" {
		return nil
	}
	var out []db.User
	for _, u := range users {
		name := norm(u.Name)
		if name == q {
			return []db.User{u}
		}
		all := true
		for _, w := range strings.Fields(q) {
			if !strings.Contains(name, w) {
				all = false
				break
			}
		}
		if all {
			out = append(out, u)
		}
	}
	return out
}

// sendSummary builds the Excel recap for [from, to) — everyone, or only one
// user — and sends it with a short caption.
func (h *Handler) sendSummary(c tele.Context, from, to time.Time, label string, only *db.User) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	now := time.Now().In(h.loc)
	_ = c.Notify(tele.UploadingDocument)
	recs, err := h.att.Between(ctx, from, to)
	if err != nil {
		log.Printf("summary: %v", err)
		return c.Send("Gagal membaca spreadsheet, coba lagi.")
	}
	us, err := h.users.ListUsers(ctx)
	if err != nil {
		return adminErr(c, "summary", err)
	}

	if only != nil {
		filtered := recs[:0:0]
		for _, r := range recs {
			if only.Verified() && r.TelegramID == only.TelegramID {
				filtered = append(filtered, r)
			}
		}
		recs = filtered
	}
	people := report.PeopleFrom(us, recs, only)
	if len(people) == 0 {
		return c.Send("Tidak ada data untuk " + label + ".")
	}

	data, st, err := report.Build(report.Input{Title: label, From: from, To: to, Now: now, People: people, Records: recs,
		Holidays: h.holidayNames(ctx, from, to)})
	if err != nil {
		log.Printf("summary report: %v", err)
		return c.Send("Gagal membuat file rekap.")
	}

	name := "rekap-" + from.Format("2006-01-02") + "_" + to.AddDate(0, 0, -1).Format("2006-01-02")
	if from.Day() == 1 && to.Equal(from.AddDate(0, 1, 0)) {
		name = "rekap-" + from.Format("2006-01")
	}
	if only != nil {
		name += "-" + strings.ReplaceAll(strings.ToLower(only.Name), " ", "-")
		label += " · " + only.Name
	}
	caption := fmt.Sprintf("📊 Rekap %s\n%d orang · %d hari kerja sejauh ini", label, st.People, st.Workdays)
	if st.Workdays > 0 && only == nil {
		caption += fmt.Sprintf("\nTingkat kehadiran: %.0f%%", st.Attendance*100)
	}
	if st.Incomplete > 0 {
		caption += fmt.Sprintf("\n⚠️ %d kali lupa absen pulang (kotak kuning di file)", st.Incomplete)
	}
	if st.WorkingNow > 0 {
		caption += fmt.Sprintf("\n🟢 %d orang sedang bekerja", st.WorkingNow)
	}
	caption += "\n\nSheet \"Rekap\": tabel per tanggal · Sheet \"Detail\": jam datang/pulang & lokasi."
	return c.Send(&tele.Document{File: tele.FromReader(bytes.NewReader(data)), FileName: name + ".xlsx", Caption: caption})
}

func (h *Handler) cmdToday(c tele.Context) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	now := time.Now().In(h.loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.loc)
	recs, err := h.att.Between(ctx, from, from.AddDate(0, 0, 1))
	if err != nil {
		log.Printf("today: %v", err)
		return c.Send("Gagal membaca spreadsheet, coba lagi.")
	}
	us, err := h.users.ListUsers(ctx)
	if err != nil {
		return adminErr(c, "today", err)
	}
	in := map[int64]attendance.Record{}
	out := map[int64]time.Time{}
	for _, r := range recs { // recs are sorted by time
		switch r.Type {
		case attendance.TypeIn:
			if _, ok := in[r.TelegramID]; !ok {
				in[r.TelegramID] = r
			}
			delete(out, r.TelegramID)
		case attendance.TypeOut:
			out[r.TelegramID] = r.At
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Kehadiran %s, %s\n", idfmt.Weekday(now), idfmt.Date(now))
	if name := h.holidayToday(ctx, now); name != "" {
		fmt.Fprintf(&sb, "🏖️ Hari ini libur: %s\n", name)
	}
	sb.WriteString("\n")
	for _, u := range us {
		if !u.IsActive {
			continue
		}
		r, came := in[u.TelegramID]
		came = came && u.Verified()
		switch {
		case !came:
			fmt.Fprintf(&sb, "❌ %s — belum hadir\n", u.Name)
		case hasKey(out, u.TelegramID):
			fmt.Fprintf(&sb, "🏁 %s — datang %s %s, pulang %s\n", u.Name, r.At.Format("15:04"), statusLabel(r.Status), out[u.TelegramID].Format("15:04"))
		default:
			fmt.Fprintf(&sb, "🟢 %s — datang %s %s\n", u.Name, r.At.Format("15:04"), statusLabel(r.Status))
		}
	}
	return sendLong(c, sb.String())
}

func hasKey(m map[int64]time.Time, k int64) bool { _, ok := m[k]; return ok }
