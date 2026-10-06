package bot

import (
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"
)

func TestAddressed(t *testing.T) {
	me := &tele.User{ID: 42, Username: "HadirKantorBot", IsBot: true}
	h := &Handler{bot: &tele.Bot{Me: me}, groupSeen: map[string]time.Time{}}
	other := &tele.User{ID: 7}

	mention := func(text string, off, n int) *tele.Message {
		return &tele.Message{Text: text, Entities: tele.Entities{{Type: tele.EntityMention, Offset: off, Length: n}}}
	}
	tests := []struct {
		name string
		m    *tele.Message
		want bool
	}{
		{"plain command", &tele.Message{Text: "/datang"}, true},
		{"command for us", &tele.Message{Text: "/help@hadirkantorbot"}, true},
		{"command for another bot", &tele.Message{Text: "/help@OtherBot"}, false},
		{"mention", mention("halo @HadirKantorBot saya datang", 5, 15), true},
		{"mention after emoji", mention("😀 @HadirKantorBot", 3, 15), true}, // emoji = 2 UTF-16 units
		{"other mention", mention("halo @someone", 5, 8), false},
		{"reply to bot", &tele.Message{Text: "datang", ReplyTo: &tele.Message{Sender: me}}, true},
		{"reply to human", &tele.Message{Text: "datang", ReplyTo: &tele.Message{Sender: other}}, false},
		{"ordinary chat", &tele.Message{Text: "selamat pagi semua"}, false},
		{"text mention", &tele.Message{Text: "Bot tolong", Entities: tele.Entities{{Type: tele.EntityTMention, Offset: 0, Length: 3, User: me}}}, true},
	}
	for _, tc := range tests {
		if got := h.addressed(tc.m); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	if !h.allowGroupReply(1, 7) || h.allowGroupReply(1, 7) || !h.allowGroupReply(1, 8) || !h.allowGroupReply(2, 7) {
		t.Error("group reply rate limit should be per chat and per user")
	}
}
