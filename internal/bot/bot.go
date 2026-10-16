// Package bot handles Telegram messages: access rules, dedupe, rate limit,
// dialog history and the agent call.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"spendly/internal/agent"
	"spendly/internal/expense"
)

type Config struct {
	FamilyID       int64
	Members        map[int64]string // FAMILY_MEMBERS; empty — open mode, anyone joins the family
	RatePerHour    int
	MaxConcurrency int
	HistorySize    int
	ReplyTimeout   time.Duration
}

type Bot struct {
	db      *expense.DB
	dialogs *Dialogs
	agent   *agent.Agent
	cfg     Config
	sem     chan struct{} // bounds concurrent LLM work, and with it DB connections
	queue   userQueue
}

func New(db *expense.DB, dialogs *Dialogs, a *agent.Agent, cfg Config) *Bot {
	return &Bot{db: db, dialogs: dialogs, agent: a, cfg: cfg, sem: make(chan struct{}, cfg.MaxConcurrency),
		queue: userQueue{m: map[int64]*userSlot{}}}
}

// userQueue lines up one user's messages inside this process before they take
// a semaphore slot: otherwise a burst from one user fills every slot with
// messages waiting for that user's lock, and the rest of the family waits too.
type userQueue struct {
	mu sync.Mutex
	m  map[int64]*userSlot
}

type userSlot struct {
	turn    chan struct{}
	waiting int
}

func (q *userQueue) enter(ctx context.Context, userID int64) (leave func(), ok bool) {
	q.mu.Lock()
	s := q.m[userID]
	if s == nil {
		s = &userSlot{turn: make(chan struct{}, 1)}
		q.m[userID] = s
	}
	s.waiting++
	q.mu.Unlock()

	done := func() {
		q.mu.Lock()
		if s.waiting--; s.waiting == 0 {
			delete(q.m, userID)
		}
		q.mu.Unlock()
	}
	select {
	case s.turn <- struct{}{}:
		return func() { <-s.turn; done() }, true
	case <-ctx.Done():
		done()
		return nil, false
	}
}

const (
	maxInputRunes = 1000
	maxReplyRunes = 4000 // Telegram limit is 4096
)

const (
	greeting = `Привет! Я веду учёт расходов. Пиши обычным текстом, без команд:
• кофе 350
• такси 1200 вчера
• сколько потратил на еду в этом месяце
• покажи траты за неделю
• последнее было не 350, а 450
• удали такси

Здесь видны только твои траты. Глава семьи видит траты всех в веб-панели.`
	privateOnly = "Я работаю только в личных сообщениях, чтобы твои траты не видели остальные. Напиши мне напрямую."
	textOnly    = "Пока понимаю только текст. Напиши, например: «кофе 350»."
	tooLong     = "Слишком длинное сообщение. Опиши трату короче, например: «такси 1200 вчера»."
	rateLimited = "Слишком много сообщений за час. Попробуй чуть позже."
	failed      = "Не получилось обработать сообщение — попробуй ещё раз через минуту."
)

// Handle is the go-telegram/bot handler; the library runs it in a goroutine per update.
func (b *Bot) Handle(ctx context.Context, tg *tgbot.Bot, upd *models.Update) {
	msg := upd.Message
	if msg == nil || msg.From == nil || msg.From.IsBot {
		return
	}
	// Telegram shows «печатает…» for ~5 s, while the agent may think longer.
	typing := func() (stop func()) {
		ctx, cancel := context.WithCancel(ctx)
		go func() {
			tick := time.NewTicker(4 * time.Second)
			defer tick.Stop()
			for {
				// Cosmetic: a lost «печатает…» must not affect the reply.
				_, _ = tg.SendChatAction(ctx, &tgbot.SendChatActionParams{ChatID: msg.Chat.ID, Action: models.ChatActionTyping})
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
		return cancel
	}
	reply := b.Process(ctx, upd.ID, msg, typing)
	if reply == "" {
		return
	}
	if _, err := tg.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: msg.Chat.ID, Text: reply}); err != nil {
		slog.Error("telegram send", "chat", msg.Chat.ID, "err", err)
	}
}

// Process turns one incoming message into a reply; "" means stay silent.
// It does not depend on how the update arrived (long polling or webhook).
// typing starts a «печатает…» indicator and returns a func that stops it.
func (b *Bot) Process(ctx context.Context, updateID int64, msg *models.Message, typing func() (stop func())) string {
	if msg.Chat.Type != models.ChatTypePrivate {
		return privateOnly
	}
	text := strings.TrimSpace(msg.Text)
	switch {
	case text == "":
		return textOnly
	case text == "/start" || strings.HasPrefix(text, "/start ") || text == "/help":
		return greeting
	case utf8.RuneCountInString(text) > maxInputRunes:
		return tooLong
	}

	user, err := b.user(ctx, msg.From)
	if errors.Is(err, expense.ErrUserNotFound) {
		return fmt.Sprintf("Этот бот только для участников семьи. Твой Telegram ID: %d — попроси главу семьи добавить его в FAMILY_MEMBERS.", msg.From.ID)
	}
	if err != nil {
		return b.fail("resolve user", msg.From.ID, err)
	}

	// Order matters: the user's queue (free to wait in), then a semaphore slot,
	// then the cross-replica advisory lock. At most MaxConcurrency messages hold
	// a lock connection, and one user's burst cannot take every slot.
	leave, ok := b.queue.enter(ctx, user.ID)
	if !ok {
		return ""
	}
	defer leave()
	select {
	case b.sem <- struct{}{}:
	case <-ctx.Done():
		return ""
	}
	defer func() { <-b.sem }()
	unlock, err := b.dialogs.Lock(ctx, user.ID)
	if err != nil {
		return b.fail("lock user", user.ID, err)
	}
	defer unlock()

	msgID, fresh, err := b.dialogs.Save(ctx, user.ID, &updateID, "user", text)
	if err != nil {
		return b.fail("save message", user.ID, err)
	}
	if !fresh {
		return "" // Telegram redelivered an update that was already handled
	}
	if n, err := b.dialogs.CountUserMessages(ctx, user.ID, time.Now().Add(-time.Hour)); err != nil {
		slog.Error("rate limit check", "user", user.ID, "err", err)
	} else if n > b.cfg.RatePerHour {
		return rateLimited
	}
	history, err := b.dialogs.History(ctx, user.ID, msgID, b.cfg.HistorySize)
	if err != nil {
		return b.fail("load history", user.ID, err)
	}

	stopTyping := typing()
	actx, cancel := context.WithTimeout(ctx, b.cfg.ReplyTimeout)
	defer cancel()
	reply, err := b.agent.Reply(actx, b.db.ForUser(user), history, text)
	stopTyping()
	if err != nil {
		if reply == "" {
			return b.fail("agent", user.ID, err)
		}
		// Data already changed: the reply lists what, so the user doesn't repeat it.
		slog.Error("bot: agent failed after changes", "user", user.ID, "err", err)
	}
	reply = clip(reply)
	if _, _, err := b.dialogs.Save(ctx, user.ID, nil, "assistant", reply); err != nil {
		slog.Error("save reply", "user", user.ID, "err", err)
	}
	return reply
}

func (b *Bot) user(ctx context.Context, from *models.User) (expense.User, error) {
	if len(b.cfg.Members) > 0 {
		if _, ok := b.cfg.Members[from.ID]; !ok {
			return expense.User{}, expense.ErrUserNotFound
		}
		return b.db.GetUser(ctx, from.ID) // members are saved from config at startup
	}
	name := strings.TrimSpace(from.FirstName + " " + from.LastName)
	if name == "" {
		name = from.Username
	}
	if name == "" {
		name = strconv.FormatInt(from.ID, 10)
	}
	return b.db.EnsureUser(ctx, from.ID, b.cfg.FamilyID, name)
}

func (b *Bot) fail(op string, userID int64, err error) string {
	slog.Error("bot: "+op, "user", userID, "err", err)
	return failed
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxReplyRunes {
		return s
	}
	return string([]rune(s)[:maxReplyRunes]) + "…"
}
