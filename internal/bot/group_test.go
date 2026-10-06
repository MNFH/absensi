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

func TestSentAt(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	h := &Handler{loc: loc}
	b := &tele.Bot{}

	// A message sent two hours ago (bot was offline) keeps its send time.
	sent := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	c := b.NewContext(tele.Update{Message: &tele.Message{Unixtime: sent.Unix()}})
	if got := h.sentAt(c); !got.Equal(sent) || got.Location() != loc {
		t.Errorf("queued message: got %v, want %v", got, sent)
	}

	// A timestamp in the future (clock skew) is never used.
	c = b.NewContext(tele.Update{Message: &tele.Message{Unixtime: time.Now().Add(time.Hour).Unix()}})
	if got := h.sentAt(c); got.After(time.Now()) {
		t.Errorf("future timestamp used: %v", got)
	}

	// No message (e.g. a button press): current time.
	if got := h.sentAt(b.NewContext(tele.Update{})); time.Since(got) > time.Second {
		t.Errorf("no message: got %v", got)
	}
}
