// Package expense holds the expense model, input validation and Postgres storage.
package expense

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Category — одна из фиксированных категорий из ТЗ.
type Category string

// Categories — допустимые категории в порядке показа.
var Categories = []Category{"еда", "транспорт", "жильё", "развлечения", "здоровье", "покупки", "другое"}

// ParseCategory normalizes case, spaces and «е/ё» ("Жилье " → "жильё").
func ParseCategory(s string) (Category, error) {
	c := Category(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "жилье", "жильё"))
	if !slices.Contains(Categories, c) {
		return "", invalid("category", "неизвестная категория %q", s)
	}
	return c, nil
}

func (c *Category) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	parsed, err := ParseCategory(strings.Trim(string(b), `"`))
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// Money — сумма в копейках. Не float: копейки должны складываться точно.
type Money int64

// maxMoney keeps amounts inside NUMERIC(12,2).
const maxMoney Money = 1_000_000_000_00

// ParseMoney parses "350", "350.5", "1 200,50" into kopecks. Zero and negative
// amounts are rejected: a spending is always positive.
func ParseMoney(s string) (Money, error) {
	s = strings.NewReplacer(" ", "", " ", "", ",", ".").Replace(strings.TrimSpace(s))
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" || !digitsOnly(whole) || !digitsOnly(frac) || len(frac) > 2 || (hasFrac && frac == "") {
		return 0, invalid("amount", "некорректная сумма %q", s)
	}
	frac += strings.Repeat("0", 2-len(frac))
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil || Money(n) > maxMoney {
		return 0, invalid("amount", "слишком большая сумма %q", s)
	}
	if n == 0 {
		return 0, invalid("amount", "сумма должна быть больше нуля")
	}
	return Money(n), nil
}

func digitsOnly(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// String formats as a plain decimal in rubles: "350.50".
func (m Money) String() string {
	return fmt.Sprintf("%d.%02d", m/100, m%100)
}

func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(m.String()), nil
}

func (m *Money) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	parsed, err := ParseMoney(strings.Trim(string(b), `"`))
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// Expense — трата, как её отдают API и инструменты агента.
type Expense struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	UserName  string    `json:"user_name"`
	Amount    Money     `json:"amount"`
	Category  Category  `json:"category"`
	SpentAt   string    `json:"spent_at"` // YYYY-MM-DD
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const maxComment = 500

// Input — новая трата. В хранилище пользователя UserID подставляется из хранилища.
type Input struct {
	UserID   int64    `json:"user_id"`
	Amount   Money    `json:"amount"`
	Category Category `json:"category"`
	SpentAt  string   `json:"spent_at"`
	Comment  string   `json:"comment"`
}

// Validate checks required fields; today is "YYYY-MM-DD" in the family's timezone.
func (in Input) Validate(today string) error {
	if in.Amount <= 0 {
		return invalid("amount", "укажи сумму больше нуля")
	}
	if in.Category == "" {
		return invalid("category", "укажи категорию")
	}
	if len([]rune(in.Comment)) > maxComment {
		return invalid("comment", "комментарий длиннее %d символов", maxComment)
	}
	return validateDate(in.SpentAt, today)
}

// Patch — изменение траты; nil-поля не меняются.
type Patch struct {
	UserID   *int64    `json:"user_id"`
	Amount   *Money    `json:"amount"`
	Category *Category `json:"category"`
	SpentAt  *string   `json:"spent_at"`
	Comment  *string   `json:"comment"`
}

func (p Patch) Validate(today string) error {
	if p == (Patch{}) {
		return invalid("patch", "нечего менять")
	}
	if p.Amount != nil && *p.Amount <= 0 {
		return invalid("amount", "укажи сумму больше нуля")
	}
	if p.Category != nil && *p.Category == "" {
		return invalid("category", "укажи категорию")
	}
	if p.Comment != nil && len([]rune(*p.Comment)) > maxComment {
		return invalid("comment", "комментарий длиннее %d символов", maxComment)
	}
	if p.SpentAt != nil {
		return validateDate(*p.SpentAt, today)
	}
	return nil
}

// Filter narrows a listing. It can only narrow: the store scope is applied first.
type Filter struct {
	UserID   int64    // 0 — все участники в пределах хранилища
	Category Category // "" — любая
	From, To string   // YYYY-MM-DD включительно, "" — без границы
	Text     string   // подстрока комментария
	OrderBy  string   // "spent_at" (по умолчанию) или "created_at"
	Limit    int
	Offset   int
}

func (f Filter) Validate() error {
	for field, d := range map[string]string{"from": f.From, "to": f.To} {
		if d == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, d); err != nil {
			return invalid(field, "дата в формате ГГГГ-ММ-ДД")
		}
	}
	if f.OrderBy != "" && f.OrderBy != "spent_at" && f.OrderBy != "created_at" {
		return invalid("order", "сортировка только по spent_at или created_at")
	}
	return nil
}

// Summary — итоги по фильтру.
type Summary struct {
	Count      int                `json:"count"`
	Total      Money              `json:"total"`
	ByCategory map[Category]Money `json:"by_category"`
}

func validateDate(s, today string) error {
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return invalid("spent_at", "дата в формате ГГГГ-ММ-ДД")
	}
	if s > today { // ISO dates compare correctly as strings
		return invalid("spent_at", "дата %s в будущем", s)
	}
	return nil
}

// ValidationError — ошибка во входных данных: API отвечает 400, агент передаёт текст модели.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) *ValidationError {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}
