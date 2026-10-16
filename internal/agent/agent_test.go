package agent

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"spendly/internal/expense"
	"spendly/internal/testdb"
)

// fakeLLM replays scripted replies and records what the agent sent.
type fakeLLM struct {
	t       *testing.T
	replies []Message
	sent    [][]Message
}

func (f *fakeLLM) Chat(_ context.Context, msgs []Message, _ []Tool) (Message, error) {
	f.sent = append(f.sent, slices.Clone(msgs))
	if len(f.replies) == 0 {
		f.t.Fatal("unexpected LLM call")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r, nil
}

// toolResult is what the agent sent back after the n-th LLM reply (0-based).
func (f *fakeLLM) toolResult(n int) string {
	msgs := f.sent[n+1]
	return msgs[len(msgs)-1].Content
}

func call(name, args string) Message {
	return Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: name, Arguments: args}}}}
}

func say(text string) Message { return Message{Role: "assistant", Content: text} }

type fixture struct {
	db         *expense.DB
	alice, bob expense.User
}

func setup(t *testing.T) fixture {
	db := expense.NewDB(testdb.New(t))
	ctx := context.Background()
	alice, err := db.EnsureUser(ctx, 101, expense.DefaultFamilyID, "Алиса")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, 102, expense.DefaultFamilyID, "Боб")
	if err != nil {
		t.Fatal(err)
	}
	return fixture{db: db, alice: alice, bob: bob}
}

func newAgent(llm LLM) *Agent {
	loc, _ := time.LoadLocation("Europe/Moscow")
	a := New(llm, loc)
	a.now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, loc) } // пятница
	return a
}

func TestAddExpense(t *testing.T) {
	f := setup(t)
	llm := &fakeLLM{t: t, replies: []Message{
		call("add_expense", `{"amount": 350, "category": "еда", "spent_at": "2026-10-16", "comment": "кофе"}`),
		say("Записано: кофе — 350 ₽"),
	}}
	reply, err := newAgent(llm).Reply(context.Background(), f.db.ForUser(f.alice), nil, "кофе 350")
	if err != nil || reply != "Записано: кофе — 350 ₽" {
		t.Fatalf("reply = %q, %v", reply, err)
	}
	if system := llm.sent[0][0].Content; !strings.Contains(system, "2026-10-16, пятница") {
		t.Errorf("system prompt lacks today's date: %q", system)
	}
	if res := llm.toolResult(0); !strings.Contains(res, `"amount":350.00`) {
		t.Errorf("tool result = %s", res)
	}
	items, _ := f.db.ForUser(f.alice).List(context.Background(), expense.Filter{})
	if len(items) != 1 || items[0].Amount != 35000 || items[0].Comment != "кофе" {
		t.Errorf("stored = %+v", items)
	}
}

// Even if the model guesses another user's expense id or passes a user_id,
// the scoped store keeps the data isolated.
func TestCannotTouchAnotherUsersExpense(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	bobs, err := f.db.ForUser(f.bob).Create(ctx, expense.Input{Amount: 120000, Category: "транспорт", SpentAt: "2026-10-15", Comment: "такси"})
	if err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{t: t, replies: []Message{
		call("delete_expense", `{"id": `+strconv.FormatInt(bobs.ID, 10)+`}`),
		call("add_expense", `{"amount": 1, "category": "еда", "spent_at": "2026-10-16", "comment": "x", "user_id": 102}`),
		say("Такой траты нет"),
	}}
	if _, err := newAgent(llm).Reply(ctx, f.db.ForUser(f.alice), nil, "удали такси"); err != nil {
		t.Fatal(err)
	}
	if res := llm.toolResult(0); !strings.Contains(res, "не найдена") {
		t.Errorf("delete of bob's expense returned %s, want not found", res)
	}
	if items, _ := f.db.ForUser(f.bob).List(ctx, expense.Filter{}); len(items) != 1 {
		t.Errorf("bob has %d expenses, want his taxi untouched", len(items))
	}
	if items, _ := f.db.ForUser(f.alice).List(ctx, expense.Filter{}); len(items) != 1 || items[0].UserID != f.alice.ID {
		t.Errorf("user_id from the model was not ignored: %+v", items)
	}
}

func TestInvalidArgumentsGoBackToModel(t *testing.T) {
	f := setup(t)
	llm := &fakeLLM{t: t, replies: []Message{
		call("add_expense", `{"amount": 350, "category": "кофе", "spent_at": "2026-10-16", "comment": "кофе"}`),
		call("add_expense", `{"amount": 350, "category": "еда", "spent_at": "2026-10-17", "comment": "кофе"}`),
		say("Уточни, пожалуйста"),
	}}
	if _, err := newAgent(llm).Reply(context.Background(), f.db.ForUser(f.alice), nil, "кофе 350"); err != nil {
		t.Fatal(err)
	}
	if res := llm.toolResult(0); !strings.Contains(res, "категория") {
		t.Errorf("bad category result = %s", res)
	}
	if res := llm.toolResult(1); !strings.Contains(res, "в будущем") {
		t.Errorf("future date result = %s", res)
	}
	if items, _ := f.db.ForUser(f.alice).List(context.Background(), expense.Filter{}); len(items) != 0 {
		t.Errorf("invalid expenses were stored: %+v", items)
	}
}

func TestFindReportsTotalsAndSumUsesSQL(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := f.db.ForUser(f.alice)
	for _, amount := range []expense.Money{35000, 45000, 120000} {
		if _, err := s.Create(ctx, expense.Input{Amount: amount, Category: "еда", SpentAt: "2026-10-16", Comment: "еда"}); err != nil {
			t.Fatal(err)
		}
	}
	llm := &fakeLLM{t: t, replies: []Message{
		call("find_expenses", `{"limit": 1, "order": "created_at"}`),
		call("sum_expenses", `{"category": "еда", "from": "2026-10-01"}`),
		say("ok"),
	}}
	if _, err := newAgent(llm).Reply(ctx, s, nil, "сколько на еду"); err != nil {
		t.Fatal(err)
	}
	if find := llm.toolResult(0); !strings.Contains(find, `"total_count":3`) || !strings.Contains(find, `"amount":1200.00`) {
		t.Errorf("find result = %s, want the latest expense and total_count 3", find)
	}
	if sum := llm.toolResult(1); !strings.Contains(sum, `"total":2000.00`) {
		t.Errorf("sum result = %s", sum)
	}
}

func TestStepLimitStopsLoop(t *testing.T) {
	f := setup(t)
	a := newAgent(nil)
	replies := make([]Message, a.maxSteps)
	for i := range replies {
		replies[i] = call("find_expenses", `{}`)
	}
	a.llm = &fakeLLM{t: t, replies: replies}
	reply, err := a.Reply(context.Background(), f.db.ForUser(f.alice), nil, "???")
	if err != nil || reply != fallbackReply {
		t.Errorf("reply = %q, %v; want fallback", reply, err)
	}
}

func TestHistoryIsSentBeforeMessage(t *testing.T) {
	f := setup(t)
	llm := &fakeLLM{t: t, replies: []Message{say("ok")}}
	history := []expense.Message{{Role: "user", Content: "удали такси"}, {Role: "assistant", Content: "Какое?"}}
	if _, err := newAgent(llm).Reply(context.Background(), f.db.ForUser(f.alice), history, "первое"); err != nil {
		t.Fatal(err)
	}
	got := llm.sent[0]
	if len(got) != 4 || got[1].Content != "удали такси" || got[2].Role != "assistant" || got[3].Content != "первое" {
		t.Errorf("messages = %+v", got)
	}
}
