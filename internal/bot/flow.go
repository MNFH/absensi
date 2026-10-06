package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/absensi/internal/attendance"
	"github.com/absensi/internal/idfmt"
	"github.com/absensi/internal/nlu"
	"github.com/absensi/internal/timeparse"
)

// A check-in/out is a short conversation: understand the intent (command or
// free text via Gemini) -> ask WFO/WFH if unknown -> ask for a location ->
// record. The half-finished request is kept in memory per user.

const (
	pendingTTL = 5 * time.Minute
	lblCancel  = "Batal"
	lblSkip    = "Lewati lokasi"
	aiPerMin   = 10
)

type pending struct {
	typ     string
	at      time.Time
	source  string
	status  string // "" until chosen (check-in only; check-out inherits)
	expires time.Time
}

var (
	statusMarkup = &tele.ReplyMarkup{}
	btnWFO       = statusMarkup.Data("🏢 WFO", "st", attendance.StatusWFO)
	btnWFH       = statusMarkup.Data("🏠 WFH", "st", attendance.StatusWFH)
)

func init() {
	statusMarkup.Inline(statusMarkup.Row(btnWFO, btnWFH))
}

func statusLabel(s string) string {
	switch s {
	case attendance.StatusWFO:
		return "🏢 WFO"
	case attendance.StatusWFH:
		return "🏠 WFH"
	}
	return ""
}

// splitStatus pulls a standalone "wfo"/"wfh" word out of a command argument.
func splitStatus(arg string) (rest, status string) {
	var kept []string
	for _, w := range strings.Fields(arg) {
		switch strings.ToLower(w) {
		case "wfo":
			status = attendance.StatusWFO
		case "wfh":
			status = attendance.StatusWFH
		default:
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " "), status
}

func (h *Handler) getPending(id int64) *pending {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.pend[id]
	if p != nil && time.Now().After(p.expires) {
		delete(h.pend, id)
		return nil
	}
	return p
}

func (h *Handler) setPending(id int64, p *pending) {
	h.mu.Lock()
	h.pend[id] = p
	h.mu.Unlock()
}

func (h *Handler) clearPending(id int64) {
	h.mu.Lock()
	delete(h.pend, id)
	h.mu.Unlock()
}

// allowAI is a small per-user rate limit to protect the Gemini quota.
func (h *Handler) allowAI(id int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	cut := time.Now().Add(-time.Minute)
	kept := h.aiCalls[id][:0]
	for _, t := range h.aiCalls[id] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= aiPerMin {
		h.aiCalls[id] = kept
		return false
	}
	h.aiCalls[id] = append(kept, time.Now())
	return true
}

// fromCommand handles /datang and /pulang [time] [wfo|wfh].
func (h *Handler) fromCommand(c tele.Context, typ, payload string) error {
	rest, status := splitStatus(payload)
	now := time.Now().In(h.loc)
	at, source := now, attendance.SourceLive
	if rest != "" {
		var err error
		at, err = timeparse.Parse(rest, now, h.maxBackdate)
		if err != nil {
			return c.Send(fmt.Sprintf("Waktu tidak bisa dipakai: %v.\n\n%s", err, timeHint))
		}
		source = attendance.SourceManual
	}
	return h.begin(c, typ, at, source, status)
}

// begin validates the request up front (so nobody shares a location for
// nothing), then asks for whatever is still missing.
func (h *Handler) begin(c tele.Context, typ string, at time.Time, source, status string) error {
	u := user(c)
	ctx, cancel := ctxTimeout()
	defer cancel()
	now := time.Now().In(h.loc)

	if err := h.att.Check(ctx, u.TelegramID, typ, at, now, source); err != nil {
		return h.recordErr(c, u.TelegramID, typ, err)
	}
	if typ == attendance.TypeOut { // check-out inherits the status of the check-in it closes
		if in, ok, err := h.att.OpenBefore(ctx, u.TelegramID, at, now); err == nil && ok {
			status = in.Status
		}
	}
	p := &pending{typ: typ, at: at, source: source, status: status, expires: time.Now().Add(pendingTTL)}
	h.setPending(u.TelegramID, p)
	if p.status == "" && typ == attendance.TypeIn {
		return c.Send("Kamu bekerja dari mana hari ini?", statusMarkup)
	}
	return h.askLocation(c, p)
}

func (h *Handler) askLocation(c tele.Context, p *pending) error {
	kb := &tele.ReplyMarkup{ResizeKeyboard: true, OneTimeKeyboard: true}
	loc := kb.Location("📍 Kirim lokasi saya")
	rows := []tele.Row{kb.Row(loc)}
	msg := "Kirim lokasimu dengan tombol di bawah"
	if p.typ == attendance.TypeIn {
		msg += " (wajib untuk datang)."
		rows = append(rows, kb.Row(kb.Text(lblCancel)))
	} else {
		msg += " (boleh dilewati untuk pulang)."
		rows = append(rows, kb.Row(kb.Text(lblSkip), kb.Text(lblCancel)))
	}
	msg += "\nJika tombol tidak muncul (mis. Telegram Desktop), pakai 📎 → Lokasi."
	kb.Reply(rows...)
	return c.Send(msg, kb)
}

func (h *Handler) onStatus(c tele.Context) error {
	u := user(c)
	p := h.getPending(u.TelegramID)
	if p == nil || p.typ != attendance.TypeIn {
		return c.Respond(&tele.CallbackResponse{Text: "Permintaan sudah kedaluwarsa, ulangi dari awal."})
	}
	status := strings.ToUpper(c.Callback().Data)
	if status != attendance.StatusWFO && status != attendance.StatusWFH {
		return c.Respond()
	}
	h.mu.Lock()
	p.status = status
	h.mu.Unlock()
	_ = c.Respond()
	_ = c.Edit("Status: " + statusLabel(p.status))
	return h.askLocation(c, p)
}

func (h *Handler) onLocation(c tele.Context) error {
	u := user(c)
	p := h.getPending(u.TelegramID)
	if p == nil || (p.typ == attendance.TypeIn && p.status == "") {
		return c.Send("Tidak ada permintaan datang/pulang yang menunggu lokasi. Ketik mis. \"saya sudah datang\" dulu.")
	}
	m := c.Message()
	if m.IsForwarded() {
		return c.Send("Lokasi hasil forward tidak diterima. Kirim lokasimu sendiri.")
	}
	if m.Location == nil {
		return nil
	}
	return h.finalize(c, p, &attendanceLoc{lat: float64(m.Location.Lat), lng: float64(m.Location.Lng)})
}

type attendanceLoc struct{ lat, lng float64 }

func (h *Handler) skipLocation(c tele.Context) error {
	p := h.getPending(user(c).TelegramID)
	if p == nil {
		return c.Send("Tidak ada permintaan yang menunggu.", removeKB())
	}
	if p.typ == attendance.TypeIn {
		return c.Send("Lokasi wajib untuk datang. Kirim lokasimu atau ketik Batal.")
	}
	return h.finalize(c, p, nil)
}

func (h *Handler) cancel(c tele.Context) error {
	h.clearPending(user(c).TelegramID)
	return c.Send("Dibatalkan.", removeKB())
}

func removeKB() *tele.ReplyMarkup { return &tele.ReplyMarkup{RemoveKeyboard: true} }

func (h *Handler) finalize(c tele.Context, p *pending, loc *attendanceLoc) error {
	u := user(c)
	ctx, cancel := ctxTimeout()
	defer cancel()

	r := attendance.Record{
		TelegramID: u.TelegramID, Name: u.Name, Phone: u.Phone, Type: p.typ, At: p.at, Source: p.source, Status: p.status,
	}
	if loc != nil {
		r.HasLocation, r.Lat, r.Lng = true, loc.lat, loc.lng
		gctx, gcancel := context.WithTimeout(ctx, 8*time.Second)
		addr, err := h.geo.Reverse(gctx, loc.lat, loc.lng)
		gcancel()
		if err != nil {
			log.Printf("reverse geocode: %v", err) // non-fatal: coordinates are still recorded
		}
		r.Address = addr
	}

	saved, err := h.att.Record(ctx, r, time.Now().In(h.loc))
	if err != nil {
		return h.recordErr(c, u.TelegramID, p.typ, err)
	}
	h.clearPending(u.TelegramID)

	verb := map[string]string{attendance.TypeIn: "Datang", attendance.TypeOut: "Pulang"}[p.typ]
	var sb strings.Builder
	fmt.Fprintf(&sb, "✅ %s dicatat: %s", verb, fmtTime(saved.At))
	if s := statusLabel(saved.Status); s != "" {
		sb.WriteString("\n" + s)
	}
	switch {
	case !saved.HasLocation:
		sb.WriteString("\n📍 (tanpa lokasi)")
	case saved.Address != "":
		sb.WriteString("\n📍 " + saved.Address)
	default:
		fmt.Fprintf(&sb, "\n📍 %.5f, %.5f", saved.Lat, saved.Lng)
	}
	sb.WriteString(h.forgottenNote(ctx, u.TelegramID))
	return c.Send(sb.String(), removeKB())
}

// recordErr maps service errors to replies. Business-rule errors end the
// pending request; infrastructure errors keep it so the user can retry.
func (h *Handler) recordErr(c tele.Context, id int64, typ string, err error) error {
	switch {
	case attendance.IsRuleError(err):
		h.clearPending(id)
		return c.Send("⚠️ "+err.Error(), removeKB())
	}
	log.Printf("record %d %s: %v", id, typ, err)
	return c.Send("Gagal menyimpan ke spreadsheet, catatan belum tersimpan. Kirim lokasi lagi untuk mencoba ulang.")
}

// onText handles free-text chat. With Gemini configured the message is
// classified by the model; otherwise (or if Gemini fails) a keyword match on
// "datang"/"pulang" is used.
func (h *Handler) onText(c tele.Context) error {
	text := strings.TrimSpace(c.Text())
	switch text {
	case "":
		return nil
	case lblCancel:
		return h.cancel(c)
	case lblSkip:
		return h.skipLocation(c)
	}
	u := user(c)

	if h.nlu != nil && h.allowAI(u.TelegramID) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		now := time.Now().In(h.loc)
		in, err := h.nlu.Extract(ctx, text, now)
		cancel()
		if err == nil {
			return h.fromIntent(c, in, now)
		}
		log.Printf("gemini: %v", err) // fall through to keywords
	}

	first, rest, _ := strings.Cut(text, " ")
	switch strings.ToLower(first) {
	case "datang":
		return h.fromCommand(c, attendance.TypeIn, rest)
	case "pulang":
		return h.fromCommand(c, attendance.TypeOut, rest)
	}
	if h.nlu != nil {
		return c.Send("Maaf, aku belum paham. Coba tulis mis. \"saya sudah datang di kantor\" atau \"pulang\", atau pakai /help.")
	}
	return c.Send("Perintah tidak dikenal. Ketik /help untuk daftar perintah.")
}

func (h *Handler) fromIntent(c tele.Context, in nlu.Intent, now time.Time) error {
	var typ string
	switch in.Action {
	case nlu.ActionCheckIn:
		typ = attendance.TypeIn
	case nlu.ActionCheckOut:
		typ = attendance.TypeOut
	case nlu.ActionSummary:
		return h.summaryFromIntent(c, in)
	default:
		return c.Send("Aku hanya bisa mencatat kehadiran. Tulis mis. \"saya sudah datang\" atau \"pulang jam 5 sore\". /help untuk perintah lain.")
	}
	at, source := now, attendance.SourceLive
	if !in.At.IsZero() {
		if err := timeparse.Validate(in.At, now, h.maxBackdate); err != nil {
			return c.Send(fmt.Sprintf("Waktu tidak bisa dipakai: %v.\n\n%s", err, timeHint))
		}
		at, source = in.At, attendance.SourceManual
	}
	return h.begin(c, typ, at, source, strings.ToUpper(in.Status))
}

// forgottenNote asks the user to fill in check-outs they forgot (within the
// window in which a manual time is still accepted).
func (h *Handler) forgottenNote(ctx context.Context, tgID int64) string {
	now := time.Now().In(h.loc)
	since := now.AddDate(0, 0, -h.maxBackdate)
	forgot, err := h.att.ForgottenCheckouts(ctx, tgID, since, now)
	if err != nil || len(forgot) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n⚠️ Kamu belum absen pulang di:")
	for _, r := range forgot {
		t := r.At.In(h.loc)
		fmt.Fprintf(&sb, "\n• %s, %s (datang %s)", idfmt.Weekday(t), idfmt.Date(t), t.Format("15:04"))
	}
	last := forgot[len(forgot)-1].At.In(h.loc)
	fmt.Fprintf(&sb, "\nIsi jam pulangnya dengan mengetik, mis.: pulang %d %s 17.00", last.Day(), idfmt.Month(last.Month()))
	return sb.String()
}
