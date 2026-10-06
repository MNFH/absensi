package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

// In groups the bot never records attendance or shows data: the contact and
// location buttons only work in private chats, and locations would be
// visible to everyone. When someone calls the bot in a group, it replies
// with a button that opens the private chat.

const groupReplyEvery = time.Minute // per person per group, to avoid spam

func (h *Handler) onGroup(c tele.Context) error {
	m := c.Message()
	if m == nil || c.Callback() != nil {
		return nil
	}
	if m.GroupCreated || m.SuperGroupCreated || h.botJoined(m) {
		log.Printf("added to group %d (%s)", c.Chat().ID, c.Chat().Title)
		return c.Send("Halo semua! 👋 Saya bot absensi kantor.\n\n"+
			"Absen datang/pulang dilakukan lewat chat pribadi dengan saya, supaya lokasi kalian tidak terlihat di grup. "+
			"Tekan tombol di bawah untuk mulai.\n\n"+
			"Karyawan baru: nomor HP kalian harus sudah didaftarkan HR.", h.privateChatButton())
	}
	if m.MigrateTo != 0 { // group upgraded to a supergroup: keep its reminders
		ctx, cancel := ctxTimeout()
		defer cancel()
		if err := h.users.MigrateReminderGroup(ctx, c.Chat().ID, m.MigrateTo); err != nil {
			log.Printf("migrate group %d -> %d: %v", c.Chat().ID, m.MigrateTo, err)
		}
		return nil
	}
	if !h.addressed(m) {
		return nil
	}
	if handled, err := h.groupCommand(c); handled {
		return err
	}
	if !h.allowGroupReply(c.Chat().ID, c.Sender().ID) {
		return nil
	}
	log.Printf("group %d (%s): pointed user %d to private chat", c.Chat().ID, c.Chat().Title, c.Sender().ID)
	return c.Reply(fmt.Sprintf("Halo %s! 👋 Absen dan perintah lainnya dilakukan lewat chat pribadi dengan saya, "+
		"supaya lokasimu tidak terlihat di grup.\n\nTekan tombol di bawah, lalu tulis misalnya \"saya sudah datang\".",
		c.Sender().FirstName), h.privateChatButton())
}

func (h *Handler) privateChatButton() *tele.ReplyMarkup {
	kb := &tele.ReplyMarkup{}
	kb.Inline(kb.Row(kb.URL("💬 Buka chat absensi", "https://t.me/"+h.bot.Me.Username+"?start=absen")))
	return kb
}

func (h *Handler) botJoined(m *tele.Message) bool {
	if m.UserJoined != nil && m.UserJoined.ID == h.bot.Me.ID {
		return true
	}
	for _, u := range m.UsersJoined {
		if u.ID == h.bot.Me.ID {
			return true
		}
	}
	return false
}

// addressed reports whether a group message is meant for this bot: a
// command (not one addressed to another bot), a reply to the bot, or an
// @mention of the bot.
func (h *Handler) addressed(m *tele.Message) bool {
	me := h.bot.Me
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	if strings.HasPrefix(text, "/") {
		first := strings.Fields(text)[0]
		if _, target, ok := strings.Cut(first, "@"); ok {
			return strings.EqualFold(target, me.Username)
		}
		return true
	}
	if m.ReplyTo != nil && m.ReplyTo.Sender != nil && m.ReplyTo.Sender.ID == me.ID {
		return true
	}
	entities := m.Entities
	if m.Text == "" {
		entities = m.CaptionEntities
	}
	for _, e := range entities {
		switch e.Type {
		case tele.EntityMention:
			if strings.EqualFold(entityText(text, e), "@"+me.Username) {
				return true
			}
		case tele.EntityTMention:
			if e.User != nil && e.User.ID == me.ID {
				return true
			}
		}
	}
	return false
}

// entityText extracts an entity; Telegram offsets count UTF-16 code units.
func entityText(text string, e tele.MessageEntity) string {
	var units []uint16
	for _, r := range text {
		if r >= 0x10000 {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
	}
	if e.Offset < 0 || e.Offset+e.Length > len(units) {
		return ""
	}
	var out []rune
	seg := units[e.Offset : e.Offset+e.Length]
	for i := 0; i < len(seg); i++ {
		if u := seg[i]; u >= 0xD800 && u < 0xDC00 && i+1 < len(seg) {
			out = append(out, rune(u-0xD800)<<10|rune(seg[i+1]-0xDC00)+0x10000)
			i++
		} else {
			out = append(out, rune(u))
		}
	}
	return string(out)
}

func (h *Handler) allowGroupReply(chatID, userID int64) bool {
	key := fmt.Sprintf("%d:%d", chatID, userID)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.groupSeen) > 5000 { // keep memory bounded
		h.groupSeen = map[string]time.Time{}
	}
	if t, ok := h.groupSeen[key]; ok && time.Since(t) < groupReplyEvery {
		return false
	}
	h.groupSeen[key] = time.Now()
	return true
}
