// Package bot wires Telegram updates to the user store and attendance service.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/absensi/internal/attendance"
	"github.com/absensi/internal/db"
	"github.com/absensi/internal/geocode"
	"github.com/absensi/internal/nlu"
)

type Handler struct {
	bot         *tele.Bot
	users       *db.Store
	att         *attendance.Service
	loc         *time.Location
	maxBackdate int

	nlu *nlu.Client // nil when GEMINI_API_KEY is not set
	geo *geocode.Client

	mu         sync.Mutex
	deniedSeen map[int64]time.Time    // rate-limit for audit of rejected users
	pend       map[int64]*pending     // check-in/out waiting for status/location
	aiCalls    map[int64][]time.Time  // recent Gemini calls per user
	imports    map[int64]*importPlan  // uploaded user lists awaiting confirmation, per admin
	groupSeen  map[string]time.Time   // last group reply per "chat:user"
	members    map[string]memberEntry // group membership cache per "chat:user"
	stop       chan struct{}          // stops the reminder scheduler
}

func New(token string, users *db.Store, att *attendance.Service, loc *time.Location, maxBackdate int,
	ai *nlu.Client, geo *geocode.Client) (*Handler, error) {
	b, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 30 * time.Second},
		OnError: func(err error, c tele.Context) {
			log.Printf("handler error: %v", err)
		},
	})
	if err != nil {
		return nil, err
	}
	h := &Handler{
		bot: b, users: users, att: att, loc: loc, maxBackdate: maxBackdate, nlu: ai, geo: geo,
		deniedSeen: map[int64]time.Time{}, pend: map[int64]*pending{}, aiCalls: map[int64][]time.Time{}, imports: map[int64]*importPlan{}, groupSeen: map[string]time.Time{}, members: map[string]memberEntry{}, stop: make(chan struct{}),
	}
	h.routes()
	return h, nil
}

func (h *Handler) Start() {
	go h.runScheduler(h.stop)
	h.bot.Start()
}
func (h *Handler) Username() string { return h.bot.Me.Username }
func (h *Handler) Stop() {
	close(h.stop)
	h.bot.Stop()
}

func (h *Handler) routes() {
	h.bot.Use(h.auth)
	h.bot.Handle(tele.OnAddedToGroup, func(tele.Context) error { return nil }) // handled in auth -> onGroup
	h.bot.Handle(tele.OnMigration, func(tele.Context) error { return nil })    // handled in auth -> onGroup
	groupOnly := func(c tele.Context) error {
		return c.Send("Perintah ini dipakai di grup: tambahkan bot ke grup kantor, lalu ketik /help di sana.")
	}
	h.bot.Handle("/ingatkan", groupOnly)
	h.bot.Handle("/jadwal", groupOnly)

	// available to every registered user
	h.bot.Handle("/start", h.cmdStart)
	h.bot.Handle("/help", h.cmdHelp)
	in := func(c tele.Context) error { return h.fromCommand(c, attendance.TypeIn, c.Message().Payload) }
	out := func(c tele.Context) error { return h.fromCommand(c, attendance.TypeOut, c.Message().Payload) }
	h.bot.Handle("/datang", in)
	h.bot.Handle("/checkin", in)
	h.bot.Handle("/pulang", out)
	h.bot.Handle("/checkout", out)
	h.bot.Handle("/batal", h.cancel)
	h.bot.Handle("/status", h.cmdStatus)
	h.bot.Handle("/riwayat", h.cmdHistory)
	h.bot.Handle("/libur", h.cmdLibur) // listing for everyone; changes are admin-only inside
	h.bot.Handle(tele.OnText, h.onText)
	h.bot.Handle(tele.OnLocation, h.onLocation)
	h.bot.Handle(tele.OnContact, func(c tele.Context) error {
		return c.Send("Kamu sudah terverifikasi. Ketik /help untuk bantuan.")
	})
	h.bot.Handle(&btnWFO, h.onStatus) // btnWFH shares the same unique id; data tells them apart

	// admin only
	adm := h.bot.Group()
	adm.Use(h.requireAdmin)
	adm.Handle("/adduser", h.cmdAddUser)
	adm.Handle("/revoke", h.cmdRevoke)
	adm.Handle("/grant", h.cmdGrant)
	adm.Handle("/setrole", h.cmdSetRole)
	adm.Handle("/setphone", h.cmdSetPhone)
	adm.Handle("/setname", h.cmdSetName)
	adm.Handle("/unbind", h.cmdUnbind)
	adm.Handle("/users", h.cmdUsers)
	adm.Handle("/summary", h.cmdSummary)
	adm.Handle("/today", h.cmdToday)
	adm.Handle("/audit", h.cmdAudit)
	adm.Handle("/import", h.cmdImport)
	adm.Handle(tele.OnDocument, h.onDocument)
	adm.Handle(&btnImportYes, h.onImportConfirm) // btnImportNo shares the unique id
}

// SyncAllCommands refreshes the per-user command menu for every active user.
func (h *Handler) SyncAllCommands(ctx context.Context) {
	// Group menu: picking a command from it sends /cmd@HadirKantorBot, which
	// Telegram delivers even when the bot's group privacy mode is on.
	if err := h.bot.SetCommands(groupMenu, tele.CommandScope{Type: tele.CommandScopeAllGroupChats}); err != nil {
		log.Printf("set group commands: %v", err)
	}
	us, err := h.users.ListUsers(ctx)
	if err != nil {
		log.Printf("sync commands: %v", err)
		return
	}
	for i := range us {
		if us[i].IsActive {
			h.syncCommands(&us[i])
		}
	}
}

func (h *Handler) syncCommands(u *db.User) {
	if u.TelegramID <= 0 { // unverified, or a demo user with a fake negative ID
		return // no Telegram chat to configure
	}
	scope := tele.CommandScope{Type: tele.CommandScopeChat, ChatID: u.TelegramID}
	var err error
	if !u.IsActive {
		err = h.bot.DeleteCommands(scope)
	} else {
		err = h.bot.SetCommands(menuFor(u.Role), scope)
	}
	if err != nil {
		log.Printf("set commands for %d: %v", u.TelegramID, err)
	}
}

// ---- middleware ----

type ctxT = context.Context

func ctxTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func user(c tele.Context) *db.User { return c.Get("user").(*db.User) }

func (h *Handler) auth(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if c.Sender() == nil || c.Chat() == nil {
			return nil
		}
		switch c.Chat().Type {
		case tele.ChatPrivate:
		case tele.ChatGroup, tele.ChatSuperGroup:
			return h.onGroup(c) // only ever points people to the private chat
		default:
			return nil // channels
		}
		ctx, cancel := ctxTimeout()
		defer cancel()
		id := c.Sender().ID
		u, err := h.users.GetByTelegramID(ctx, id)
		switch {
		case err == nil && u.IsActive:
			c.Set("user", u)
			return next(c)
		case err == nil || errors.Is(err, db.ErrNotFound):
			// Not (yet) verified: the only thing allowed is sharing their own contact.
			if m := c.Message(); m != nil && m.Contact != nil {
				return h.bindContact(c)
			}
			h.deny(ctx, id, c.Text())
			return c.Send(verifyPrompt(err == nil), contactKeyboard())
		default:
			log.Printf("auth lookup %d: %v", id, err)
			return c.Send("Terjadi kesalahan sistem, coba lagi sebentar.")
		}
	}
}

func verifyPrompt(revoked bool) string {
	if revoked {
		return "Akses akunmu sudah dicabut. Hubungi HR."
	}
	return "Halo! Untuk memakai bot absensi, verifikasi nomor teleponmu dulu: tekan tombol di bawah untuk membagikan nomor Telegram-mu. " +
		"Nomor itu harus sudah didaftarkan HR."
}

func contactKeyboard() *tele.ReplyMarkup {
	kb := &tele.ReplyMarkup{ResizeKeyboard: true, OneTimeKeyboard: true}
	kb.Reply(kb.Row(kb.Contact("📱 Bagikan nomor telepon saya")))
	return kb
}

// bindContact verifies a shared contact belongs to the sender and matches a
// number HR pre-registered, then locks the Telegram account to that number.
func (h *Handler) bindContact(c tele.Context) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	m, id := c.Message(), c.Sender().ID
	ct := m.Contact

	if m.IsForwarded() || ct.UserID != id {
		_ = h.users.Audit(ctx, id, "bind_rejected", ct.PhoneNumber, map[string]string{"why": "not own contact"})
		return c.Send("Itu bukan nomor akun Telegram-mu. Tekan tombol \"Bagikan nomor telepon saya\" (bukan memilih kontak lain).", contactKeyboard())
	}
	phone, err := db.NormalizePhone(ct.PhoneNumber)
	if err != nil {
		return c.Send("Nomor yang dibagikan tidak valid.", contactKeyboard())
	}
	u, err := h.users.BindTelegram(ctx, phone, id)
	switch {
	case err == nil:
		h.syncCommands(u)
		return c.Send(fmt.Sprintf("✅ Terverifikasi sebagai %s.\n\n%s", u.Name, helpText(u.Role)), removeKB())
	case errors.Is(err, db.ErrPhoneUnknown), errors.Is(err, db.ErrInactive),
		errors.Is(err, db.ErrBoundElsewhere), errors.Is(err, db.ErrAccountInUse):
		_ = h.users.Audit(ctx, id, "bind_rejected", phone, map[string]string{"why": err.Error()})
		return c.Send("⚠️ "+err.Error()+". Hubungi HR dan sebutkan nomor "+phone+".", removeKB())
	default:
		log.Printf("bind %s: %v", phone, err)
		return c.Send("Terjadi kesalahan sistem, coba lagi sebentar.")
	}
}

func (h *Handler) deny(ctx context.Context, id int64, text string) {
	h.mu.Lock()
	last, seen := h.deniedSeen[id]
	if seen && time.Since(last) < 10*time.Minute {
		h.mu.Unlock()
		return
	}
	h.deniedSeen[id] = time.Now()
	h.mu.Unlock()
	if len(text) > 100 {
		text = text[:100]
	}
	if err := h.users.Audit(ctx, id, "denied", itoa(id), map[string]string{"text": text}); err != nil {
		log.Printf("audit denied: %v", err)
	}
}

func (h *Handler) requireAdmin(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if user(c).Role != db.RoleAdmin {
			return c.Send("Perintah ini hanya untuk admin (HR).")
		}
		return next(c)
	}
}

// ---- generic ----

func (h *Handler) cmdStart(c tele.Context) error {
	u := user(c)
	h.syncCommands(u)
	return c.Send("Halo " + u.Name + "!\n\n" + helpText(u.Role))
}

func (h *Handler) cmdHelp(c tele.Context) error { return c.Send(helpText(user(c).Role)) }

// sendLong splits s on line boundaries to respect Telegram's 4096 char limit.
func sendLong(c tele.Context, s string) error {
	const limit = 3800
	for len(s) > limit {
		cut := strings.LastIndex(s[:limit], "\n")
		if cut <= 0 {
			cut = limit
		}
		if err := c.Send(s[:cut]); err != nil {
			return err
		}
		s = strings.TrimLeft(s[cut:], "\n")
	}
	return c.Send(s)
}
