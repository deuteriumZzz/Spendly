package expense_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"spendly/internal/expense"
	"spendly/internal/testdb"
)

const today = "2026-10-16"

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

func mustCreate(t *testing.T, s *expense.Store, in expense.Input) expense.Expense {
	t.Helper()
	e, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The bot's main guarantee: a user scope never sees or changes someone else's expense.
func TestUserScopeIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	aliceStore, bobStore := f.db.ForUser(f.alice), f.db.ForUser(f.bob)

	// Alice's store ignores a foreign user_id in the input.
	coffee := mustCreate(t, aliceStore, expense.Input{UserID: f.bob.ID, Amount: 35000, Category: "еда", SpentAt: today, Comment: "кофе"})
	if coffee.UserID != f.alice.ID {
		t.Fatalf("expense owner = %d, want alice", coffee.UserID)
	}

	if items, _ := bobStore.List(ctx, expense.Filter{}); len(items) != 0 {
		t.Errorf("bob sees %d expenses, want 0", len(items))
	}
	if items, _ := bobStore.List(ctx, expense.Filter{UserID: f.alice.ID}); len(items) != 0 {
		t.Errorf("bob's filter by alice widened the scope: %d expenses", len(items))
	}
	amount := expense.Money(1)
	if _, err := bobStore.Update(ctx, coffee.ID, expense.Patch{Amount: &amount}); !errors.Is(err, expense.ErrNotFound) {
		t.Errorf("bob update alice's expense: err = %v, want ErrNotFound", err)
	}
	if _, err := bobStore.Delete(ctx, coffee.ID); !errors.Is(err, expense.ErrNotFound) {
		t.Errorf("bob delete alice's expense: err = %v, want ErrNotFound", err)
	}
	if sum, _ := bobStore.Sum(ctx, expense.Filter{}); sum.Count != 0 {
		t.Errorf("bob's sum counts %d expenses, want 0", sum.Count)
	}
	if items, _ := f.db.ForFamily(expense.DefaultFamilyID).List(ctx, expense.Filter{}); len(items) != 1 || items[0].Amount != 35000 {
		t.Errorf("family sees %+v, want alice's untouched coffee", items)
	}
	if items, _ := f.db.ForUser(expense.User{}).List(ctx, expense.Filter{}); len(items) != 0 {
		t.Errorf("zero-value user scope sees %d expenses, want 0", len(items))
	}
}

func TestListFiltersAndSum(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	family := f.db.ForFamily(expense.DefaultFamilyID)
	mustCreate(t, family, expense.Input{UserID: f.alice.ID, Amount: 35000, Category: "еда", SpentAt: "2026-10-01", Comment: "кофе"})
	mustCreate(t, family, expense.Input{UserID: f.alice.ID, Amount: 120000, Category: "транспорт", SpentAt: "2026-10-15", Comment: "Такси"})
	taxi := mustCreate(t, family, expense.Input{UserID: f.bob.ID, Amount: 90000, Category: "транспорт", SpentAt: "2026-09-30", Comment: "такси 100%"})

	cases := []struct {
		name   string
		filter expense.Filter
		want   int
	}{
		{"all", expense.Filter{}, 3},
		{"by user", expense.Filter{UserID: f.bob.ID}, 1},
		{"by category", expense.Filter{Category: "транспорт"}, 2},
		{"by period", expense.Filter{From: "2026-10-01", To: "2026-10-31"}, 2},
		{"by text, case-insensitive", expense.Filter{Text: "такси"}, 2},
		{"percent is literal", expense.Filter{Text: "100%"}, 1},
		{"limit", expense.Filter{Limit: 1}, 1},
	}
	for _, c := range cases {
		items, err := family.List(ctx, c.filter)
		if err != nil || len(items) != c.want {
			t.Errorf("%s: got %d items, err %v; want %d", c.name, len(items), err, c.want)
		}
	}

	latest, _ := family.List(ctx, expense.Filter{OrderBy: "created_at", Limit: 1})
	if len(latest) != 1 || latest[0].ID != taxi.ID {
		t.Errorf("latest created = %+v, want bob's taxi", latest)
	}

	sum, err := family.Sum(ctx, expense.Filter{Category: "транспорт"})
	if err != nil || sum.Count != 2 || sum.Total != 210000 || sum.ByCategory["транспорт"] != 210000 {
		t.Errorf("sum = %+v, %v; want 2 expenses for 2100.00", sum, err)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := f.db.ForUser(f.alice)
	e := mustCreate(t, s, expense.Input{Amount: 35000, Category: "еда", SpentAt: today, Comment: "кофе"})

	time.Sleep(10 * time.Millisecond)
	amount, comment := expense.Money(45000), ""
	upd, err := s.Update(ctx, e.ID, expense.Patch{Amount: &amount, Comment: &comment})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Amount != 45000 || upd.Comment != "" || upd.Category != "еда" || !upd.UpdatedAt.After(e.UpdatedAt) {
		t.Errorf("updated = %+v", upd)
	}

	other := f.bob.ID
	if _, err := s.Update(ctx, e.ID, expense.Patch{UserID: &other}); err == nil {
		t.Error("user scope reassigned an expense to another user")
	}

	del, err := s.Delete(ctx, e.ID)
	if err != nil || del.ID != e.ID {
		t.Fatalf("delete = %+v, %v", del, err)
	}
	if _, err := s.Delete(ctx, e.ID); !errors.Is(err, expense.ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func TestCreateForUnknownMember(t *testing.T) {
	f := setup(t)
	_, err := f.db.ForFamily(expense.DefaultFamilyID).Create(context.Background(),
		expense.Input{UserID: 999, Amount: 100, Category: "еда", SpentAt: today})
	var verr *expense.ValidationError
	if !errors.As(err, &verr) {
		t.Errorf("err = %v, want ValidationError", err)
	}
}

func TestMessagesDedupeAndHistory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	update := int64(555)
	if _, ok, err := f.db.SaveMessage(ctx, f.alice.ID, &update, expense.Message{Role: "user", Content: "кофе 350"}); err != nil || !ok {
		t.Fatalf("first save: ok=%v err=%v", ok, err)
	}
	if _, ok, err := f.db.SaveMessage(ctx, f.alice.ID, &update, expense.Message{Role: "user", Content: "кофе 350"}); err != nil || ok {
		t.Errorf("redelivered update saved again: ok=%v err=%v", ok, err)
	}
	if _, _, err := f.db.SaveMessage(ctx, f.alice.ID, nil, expense.Message{Role: "assistant", Content: "Записал"}); err != nil {
		t.Fatal(err)
	}
	next := int64(556)
	current, _, _ := f.db.SaveMessage(ctx, f.alice.ID, &next, expense.Message{Role: "user", Content: "покажи"})

	hist, err := f.db.History(ctx, f.alice.ID, current, 10)
	if err != nil || len(hist) != 2 || hist[0].Content != "кофе 350" || hist[1].Role != "assistant" {
		t.Errorf("history = %+v, %v", hist, err)
	}
	if n, _ := f.db.CountUserMessages(ctx, f.alice.ID, time.Now().Add(-time.Hour)); n != 2 {
		t.Errorf("user messages in the last hour = %d, want 2", n)
	}
}

func TestListLimitIsCapped(t *testing.T) {
	f := setup(t)
	s := f.db.ForUser(f.alice)
	for range 60 {
		mustCreate(t, s, expense.Input{Amount: 100, Category: "еда", SpentAt: today})
	}
	ctx := context.Background()
	if items, _ := s.List(ctx, expense.Filter{Limit: 500}); len(items) != 60 {
		t.Errorf("limit 500 returned %d, want all 60 (capped at 200, not reset to the default)", len(items))
	}
	if items, _ := s.List(ctx, expense.Filter{}); len(items) != 50 {
		t.Errorf("default limit returned %d, want 50", len(items))
	}
}

func TestPruneMessages(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, _, err := f.db.SaveMessage(ctx, f.alice.ID, nil, expense.Message{Role: "user", Content: "кофе 350"}); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.PruneMessages(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Errorf("pruned a fresh message: n=%d err=%v", n, err)
	}
	if n, err := f.db.PruneMessages(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Errorf("old message not pruned: n=%d err=%v", n, err)
	}
}

func TestLockUserSerializes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	unlock, err := f.db.LockUser(ctx, f.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := f.db.LockUser(waitCtx, f.alice.ID); err == nil {
		t.Fatal("second lock acquired while the first is held")
	}
	unlock()
	unlock2, err := f.db.LockUser(ctx, f.alice.ID)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}
