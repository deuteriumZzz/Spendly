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

func TestTotalsByUser(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	family := f.db.ForFamily(expense.DefaultFamilyID)
	mustCreate(t, family, expense.Input{UserID: f.alice.ID, Amount: 35000, Category: "еда", SpentAt: today})
	mustCreate(t, family, expense.Input{UserID: f.bob.ID, Amount: 120000, Category: "транспорт", SpentAt: today})
	mustCreate(t, family, expense.Input{UserID: f.bob.ID, Amount: 10000, Category: "еда", SpentAt: today})

	got, err := family.TotalsByUser(ctx, expense.Filter{})
	if err != nil || len(got) != 2 || got[0].Name != "Боб" || got[0].Total != 130000 || got[0].Count != 2 || got[1].Total != 35000 {
		t.Errorf("totals = %+v, %v; want Боб 1300.00 (2) first, then Алиса 350.00", got, err)
	}
	if got, _ := family.TotalsByUser(ctx, expense.Filter{Category: "транспорт"}); len(got) != 1 || got[0].UserID != f.bob.ID {
		t.Errorf("filtered totals = %+v", got)
	}
	if got, _ := f.db.ForUser(f.alice).TotalsByUser(ctx, expense.Filter{}); len(got) != 1 || got[0].UserID != f.alice.ID {
		t.Errorf("user scope leaked other members: %+v", got)
	}
}

func TestInvites(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	inv, err := f.db.CreateInvite(ctx, expense.DefaultFamilyID, "Мама")
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Code) < 20 || time.Until(inv.ExpiresAt) < 6*24*time.Hour {
		t.Errorf("invite %+v", inv)
	}
	if _, err := f.db.InvitedUser(ctx, 555); !errors.Is(err, expense.ErrUserNotFound) {
		t.Errorf("not joined yet: %v", err)
	}

	u, err := f.db.AcceptInvite(ctx, inv.Code, 555)
	if err != nil || u.Name != "Мама" || u.FamilyID != expense.DefaultFamilyID {
		t.Fatalf("accept: %+v, %v", u, err)
	}
	if got, err := f.db.InvitedUser(ctx, 555); err != nil || got.Name != "Мама" {
		t.Errorf("invited user: %+v, %v", got, err)
	}
	// One-time: whoever gets the forwarded link next stays out.
	for _, code := range []string{inv.Code, "no-such-code"} {
		if _, err := f.db.AcceptInvite(ctx, code, 556); !errors.Is(err, expense.ErrInviteInvalid) {
			t.Errorf("code %q: %v, want ErrInviteInvalid", code, err)
		}
	}

	// A member who was already here (open mode, config) takes the invite's name.
	if _, err := f.db.InvitedUser(ctx, f.alice.ID); !errors.Is(err, expense.ErrUserNotFound) {
		t.Errorf("alice is not invited yet: %v", err)
	}
	inv2, _ := f.db.CreateInvite(ctx, expense.DefaultFamilyID, "Дочь")
	if u, err := f.db.AcceptInvite(ctx, inv2.Code, f.alice.ID); err != nil || u.Name != "Дочь" {
		t.Errorf("existing member: %+v, %v", u, err)
	}
}

func TestInviteExpiresAndKeepsOtherFamilies(t *testing.T) {
	pool := testdb.New(t)
	db := expense.NewDB(pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO families (id, name) VALUES (2, 'Соседи') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureUser(ctx, 900, 2, "Сосед"); err != nil {
		t.Fatal(err)
	}

	inv, _ := db.CreateInvite(ctx, expense.DefaultFamilyID, "Мама")
	// Someone from another family is not moved, and the code stays usable.
	if _, err := db.AcceptInvite(ctx, inv.Code, 900); !errors.Is(err, expense.ErrInviteInvalid) {
		t.Errorf("other family: %v", err)
	}
	if u, _ := db.GetUser(ctx, 900); u.FamilyID != 2 || u.Name != "Сосед" {
		t.Errorf("other family member changed: %+v", u)
	}
	if _, err := db.AcceptInvite(ctx, inv.Code, 555); err != nil {
		t.Errorf("code was spent by the failed attempt: %v", err)
	}

	old, _ := db.CreateInvite(ctx, expense.DefaultFamilyID, "Папа")
	if _, err := pool.Exec(ctx, `UPDATE invites SET expires_at = now() - interval '1 second' WHERE code = $1`, old.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptInvite(ctx, old.Code, 556); !errors.Is(err, expense.ErrInviteInvalid) {
		t.Errorf("expired code: %v", err)
	}
}

func TestRenameUser(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.db.RenameUser(ctx, expense.DefaultFamilyID, f.alice.ID, "Мама"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RenameUser(ctx, 2, f.alice.ID, "Чужая"); !errors.Is(err, expense.ErrUserNotFound) {
		t.Errorf("rename from another family: %v", err)
	}
	// The config name is only the initial one: a restart keeps the panel's rename.
	if err := f.db.SaveMember(ctx, f.alice.ID, expense.DefaultFamilyID, "Алиса"); err != nil {
		t.Fatal(err)
	}
	if u, _ := f.db.GetUser(ctx, f.alice.ID); u.Name != "Мама" {
		t.Errorf("name after restart = %q, want Мама", u.Name)
	}
}
