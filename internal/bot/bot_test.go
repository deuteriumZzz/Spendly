package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"spendly/internal/agent"
	"spendly/internal/expense"
	"spendly/internal/testdb"
)

// echoLLM answers every message with a fixed text and counts calls.
type echoLLM struct{ calls int }

func (e *echoLLM) Chat(_ context.Context, msgs []agent.Message, _ []agent.Tool) (agent.Message, error) {
	e.calls++
	return agent.Message{Role: "assistant", Content: "ответ на: " + msgs[len(msgs)-1].Content}, nil
}

func newBot(t *testing.T, cfg Config) (*Bot, *echoLLM, *expense.DB) {
	db := expense.NewDB(testdb.New(t))
	llm := &echoLLM{}
	cfg.FamilyID = expense.DefaultFamilyID
	cfg.MaxConcurrency = 2
	cfg.HistorySize = 10
	cfg.ReplyTimeout = 5 * time.Second
	if cfg.RatePerHour == 0 {
		cfg.RatePerHour = 60
	}
	return New(db, agent.New(llm, time.UTC), cfg), llm, db
}

func private(text string) *models.Message {
	return &models.Message{
		Text: text,
		Chat: models.Chat{ID: 101, Type: models.ChatTypePrivate},
		From: &models.User{ID: 101, FirstName: "Алиса"},
	}
}

func noTyping() func() { return func() {} }

func TestProcessRepliesAndKeepsHistory(t *testing.T) {
	b, llm, db := newBot(t, Config{})
	ctx := context.Background()
	if got := b.Process(ctx, 1, private("кофе 350"), noTyping); got != "ответ на: кофе 350" {
		t.Fatalf("reply = %q", got)
	}
	b.Process(ctx, 2, private("покажи"), noTyping)

	user, err := db.GetUser(ctx, 101)
	if err != nil || user.Name != "Алиса" {
		t.Fatalf("open mode did not add the user: %+v, %v", user, err)
	}
	hist, _ := db.History(ctx, 101, 1<<62, 10)
	if len(hist) != 4 || hist[1].Content != "ответ на: кофе 350" {
		t.Errorf("history = %+v", hist)
	}
	if llm.calls != 2 {
		t.Errorf("llm calls = %d, want 2", llm.calls)
	}
}

func TestProcessSkipsRedeliveredUpdate(t *testing.T) {
	b, llm, _ := newBot(t, Config{})
	ctx := context.Background()
	b.Process(ctx, 7, private("кофе 350"), noTyping)
	if got := b.Process(ctx, 7, private("кофе 350"), noTyping); got != "" || llm.calls != 1 {
		t.Errorf("redelivered update: reply %q, llm calls %d", got, llm.calls)
	}
}

func TestProcessGuards(t *testing.T) {
	b, llm, _ := newBot(t, Config{})
	ctx := context.Background()

	group := private("покажи траты")
	group.Chat.Type = models.ChatTypeGroup
	cases := map[string]struct {
		msg  *models.Message
		want string
	}{
		"group chat": {group, privateOnly},
		"photo":      {private(""), textOnly},
		"/start":     {private("/start"), greeting},
		"too long":   {private(strings.Repeat("а", maxInputRunes+1)), tooLong},
	}
	for name, c := range cases {
		if got := b.Process(ctx, 1, c.msg, noTyping); got != c.want {
			t.Errorf("%s: reply %q", name, got)
		}
	}
	if llm.calls != 0 {
		t.Errorf("guards called the LLM %d times", llm.calls)
	}
}

func TestProcessRejectsStrangersWhenMembersSet(t *testing.T) {
	b, llm, db := newBot(t, Config{Members: map[int64]string{555: "Мама"}})
	if err := db.SaveMember(context.Background(), 555, expense.DefaultFamilyID, "Мама"); err != nil {
		t.Fatal(err)
	}
	if got := b.Process(context.Background(), 1, private("кофе 350"), noTyping); !strings.Contains(got, "Telegram ID: 101") {
		t.Errorf("stranger reply = %q", got)
	}
	member := private("кофе 350")
	member.From.ID = 555
	if got := b.Process(context.Background(), 2, member, noTyping); got != "ответ на: кофе 350" || llm.calls != 1 {
		t.Errorf("member reply = %q, llm calls %d", got, llm.calls)
	}
}

func TestProcessRateLimit(t *testing.T) {
	b, llm, _ := newBot(t, Config{RatePerHour: 1})
	ctx := context.Background()
	b.Process(ctx, 1, private("кофе 350"), noTyping)
	if got := b.Process(ctx, 2, private("чай 100"), noTyping); got != rateLimited || llm.calls != 1 {
		t.Errorf("second message: reply %q, llm calls %d", got, llm.calls)
	}
}

func TestClip(t *testing.T) {
	long := strings.Repeat("я", maxReplyRunes+10)
	if got := []rune(clip(long)); len(got) != maxReplyRunes+1 {
		t.Errorf("clipped to %d runes", len(got))
	}
}
