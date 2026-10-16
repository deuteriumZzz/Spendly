package main

import (
	"context"
	"time"

	"spendly/internal/expense"
)

// seedDemo fills an empty database with a demo family so the panel has
// something to show on the first run (SEED_DEMO=true). Demo ids are far below
// real Telegram user ids.
func seedDemo(ctx context.Context, db *expense.DB, loc *time.Location) error {
	family := db.ForFamily(expense.DefaultFamilyID)
	if sum, err := family.Sum(ctx, expense.Filter{}); err != nil || sum.Count > 0 {
		return err
	}
	members := []struct {
		id   int64
		name string
	}{{1001, "Мама (демо)"}, {1002, "Папа (демо)"}, {1003, "Аня (демо)"}}
	for _, m := range members {
		if err := db.SaveMember(ctx, m.id, expense.DefaultFamilyID, m.name); err != nil {
			return err
		}
	}

	items := []struct {
		who      int
		daysAgo  int
		amount   expense.Money
		category expense.Category
		comment  string
	}{
		{0, 0, 35000, "еда", "кофе"},
		{1, 0, 120000, "транспорт", "такси"},
		{2, 1, 45000, "развлечения", "кино"},
		{0, 1, 284000, "еда", "продукты"},
		{1, 2, 6500000, "жильё", "аренда"},
		{0, 3, 189000, "здоровье", "аптека"},
		{2, 3, 7000, "транспорт", "метро"},
		{1, 4, 450000, "покупки", "кроссовки"},
		{0, 5, 312000, "еда", "продукты"},
		{2, 6, 25000, "еда", "шаурма"},
		{1, 8, 90000, "транспорт", "такси"},
		{0, 9, 150000, "развлечения", "театр"},
		{1, 11, 540000, "жильё", "коммуналка"},
		{2, 12, 129900, "покупки", "наушники"},
		{0, 14, 276000, "еда", "продукты"},
		{1, 17, 300000, "здоровье", "стоматолог"},
		{2, 20, 60000, "другое", "подарок другу"},
		{0, 24, 41000, "еда", "кафе"},
	}
	today := time.Now().In(loc)
	for _, it := range items {
		_, err := family.Create(ctx, expense.Input{
			UserID:   members[it.who].id,
			Amount:   it.amount,
			Category: it.category,
			SpentAt:  today.AddDate(0, 0, -it.daysAgo).Format(time.DateOnly),
			Comment:  it.comment,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
