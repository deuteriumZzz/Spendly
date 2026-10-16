package bot

import (
	"context"
	"testing"
	"time"

	"spendly/internal/expense"
	"spendly/internal/testdb"
)

func newDialogs(t *testing.T) (*Dialogs, int64) {
	pool := testdb.New(t)
	u, err := expense.NewDB(pool).EnsureUser(context.Background(), 101, expense.DefaultFamilyID, "Алиса")
	if err != nil {
		t.Fatal(err)
	}
	return NewDialogs(pool), u.ID
}

func TestDialogsDedupeAndHistory(t *testing.T) {
	d, user := newDialogs(t)
	ctx := context.Background()
	update := int64(555)
	if _, ok, err := d.Save(ctx, user, &update, "user", "кофе 350"); err != nil || !ok {
		t.Fatalf("first save: ok=%v err=%v", ok, err)
	}
	if _, ok, err := d.Save(ctx, user, &update, "user", "кофе 350"); err != nil || ok {
		t.Errorf("redelivered update saved again: ok=%v err=%v", ok, err)
	}
	if _, _, err := d.Save(ctx, user, nil, "assistant", "Записано"); err != nil {
		t.Fatal(err)
	}
	next := int64(556)
	current, _, _ := d.Save(ctx, user, &next, "user", "покажи")

	hist, err := d.History(ctx, user, current, 10)
	if err != nil || len(hist) != 2 || hist[0].Content != "кофе 350" || hist[1].Role != "assistant" {
		t.Errorf("history = %+v, %v", hist, err)
	}
	if n, _ := d.CountUserMessages(ctx, user, time.Now().Add(-time.Hour)); n != 2 {
		t.Errorf("user messages in the last hour = %d, want 2", n)
	}
}

func TestDialogsPrune(t *testing.T) {
	d, user := newDialogs(t)
	ctx := context.Background()
	if _, _, err := d.Save(ctx, user, nil, "user", "кофе 350"); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Prune(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Errorf("pruned a fresh message: n=%d err=%v", n, err)
	}
	if n, err := d.Prune(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Errorf("old message not pruned: n=%d err=%v", n, err)
	}
}

func TestDialogsLockSerializes(t *testing.T) {
	d, user := newDialogs(t)
	ctx := context.Background()
	unlock, err := d.Lock(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := d.Lock(waitCtx, user); err == nil {
		t.Fatal("second lock acquired while the first is held")
	}
	unlock()
	unlock2, err := d.Lock(ctx, user)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}
