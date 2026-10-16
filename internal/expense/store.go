package expense

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultFamilyID is the family created by the first migration.
const DefaultFamilyID int64 = 1

var ErrNotFound = errors.New("трата не найдена")

// DB is the root data access. Expenses are only reachable through a Store
// that is already scoped to a family (web panel) or to one user (bot).
type DB struct{ pool *pgxpool.Pool }

func NewDB(pool *pgxpool.Pool) *DB { return &DB{pool: pool} }

func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

// Store reads and changes expenses inside its scope. The scope is added to
// every SQL statement, so a filter or an id from outside cannot widen it.
type Store struct {
	pool     *pgxpool.Pool
	familyID int64
	userID   int64 // 0 — вся семья
}

// ForFamily is the web panel scope: every member of one family.
func (db *DB) ForFamily(familyID int64) *Store {
	return &Store{pool: db.pool, familyID: familyID}
}

// ForUser is the bot scope: only u's own expenses. A zero-value user gets a
// scope that matches nothing instead of silently becoming family-wide.
func (db *DB) ForUser(u User) *Store {
	id := u.ID
	if id == 0 {
		id = -1
	}
	return &Store{pool: db.pool, familyID: u.FamilyID, userID: id}
}

// query numbers placeholders as arguments are added.
type query struct{ args []any }

func (q *query) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

func (s *Store) scope(q *query) []string {
	conds := []string{"e.family_id = " + q.arg(s.familyID)}
	if s.userID != 0 {
		conds = append(conds, "e.user_id = "+q.arg(s.userID))
	}
	return conds
}

func (s *Store) where(q *query, f Filter) string {
	conds := s.scope(q)
	if f.UserID != 0 {
		conds = append(conds, "e.user_id = "+q.arg(f.UserID))
	}
	if f.Category != "" {
		conds = append(conds, "e.category = "+q.arg(f.Category))
	}
	if f.From != "" {
		conds = append(conds, "e.spent_at >= "+q.arg(f.From)+"::date")
	}
	if f.To != "" {
		conds = append(conds, "e.spent_at <= "+q.arg(f.To)+"::date")
	}
	if f.Text != "" {
		like := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Text)
		conds = append(conds, "e.comment ILIKE '%' || "+q.arg(like)+" || '%'")
	}
	return strings.Join(conds, " AND ")
}

const selectCols = `e.id, e.user_id, u.name, (e.amount * 100)::bigint, e.category,
	to_char(e.spent_at, 'YYYY-MM-DD'), COALESCE(e.comment, ''), e.created_at, e.updated_at`

func scanExpense(row pgx.Row) (Expense, error) {
	var e Expense
	err := row.Scan(&e.ID, &e.UserID, &e.UserName, &e.Amount, &e.Category,
		&e.SpentAt, &e.Comment, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

// List pages with limit/offset: a family has thousands of expenses, not
// millions, so offset stays cheap; the index on (family_id, spent_at) serves it.
func (s *Store) List(ctx context.Context, f Filter) ([]Expense, error) {
	q := &query{}
	where := s.where(q, f)
	order := "e.spent_at DESC, e.id DESC"
	if f.OrderBy == "created_at" {
		order = "e.created_at DESC, e.id DESC"
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	sql := fmt.Sprintf(`SELECT %s FROM expenses e JOIN users u ON u.telegram_id = e.user_id
		WHERE %s ORDER BY %s LIMIT %d OFFSET %d`, selectCols, where, order, limit, max(f.Offset, 0))

	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return nil, fmt.Errorf("list expenses: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Expense, error) { return scanExpense(r) })
	if err != nil {
		return nil, fmt.Errorf("list expenses: %w", err)
	}
	if items == nil {
		items = []Expense{}
	}
	return items, nil
}

// Sum totals the filtered expenses in SQL — the LLM never adds numbers itself.
func (s *Store) Sum(ctx context.Context, f Filter) (Summary, error) {
	q := &query{}
	sql := `SELECT e.category, count(*), (sum(e.amount) * 100)::bigint
		FROM expenses e WHERE ` + s.where(q, f) + ` GROUP BY e.category`
	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return Summary{}, fmt.Errorf("sum expenses: %w", err)
	}
	defer rows.Close()

	sum := Summary{ByCategory: map[Category]Money{}}
	for rows.Next() {
		var (
			c Category
			n int
			m Money
		)
		if err := rows.Scan(&c, &n, &m); err != nil {
			return Summary{}, fmt.Errorf("sum expenses: %w", err)
		}
		sum.Count += n
		sum.Total += m
		sum.ByCategory[c] = m
	}
	return sum, rows.Err()
}

// UserTotal — траты одного участника в пределах фильтра.
type UserTotal struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Total  Money  `json:"total"`
}

// TotalsByUser sums the filtered expenses per member, largest first.
func (s *Store) TotalsByUser(ctx context.Context, f Filter) ([]UserTotal, error) {
	q := &query{}
	sql := `SELECT e.user_id, u.name, count(*), (sum(e.amount) * 100)::bigint
		FROM expenses e JOIN users u ON u.telegram_id = e.user_id
		WHERE ` + s.where(q, f) + ` GROUP BY e.user_id, u.name ORDER BY 4 DESC, u.name`
	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return nil, fmt.Errorf("totals by user: %w", err)
	}
	totals, err := pgx.CollectRows(rows, pgx.RowToStructByPos[UserTotal])
	if err != nil {
		return nil, fmt.Errorf("totals by user: %w", err)
	}
	if totals == nil {
		totals = []UserTotal{}
	}
	return totals, nil
}

func (s *Store) Create(ctx context.Context, in Input) (Expense, error) {
	if s.userID != 0 {
		in.UserID = s.userID
	}
	row := s.pool.QueryRow(ctx, `
		WITH x AS (
			INSERT INTO expenses (family_id, user_id, amount, category, spent_at, comment)
			VALUES ($1, $2, $3::bigint / 100.0, $4, $5::date, NULLIF($6, ''))
			RETURNING *
		)
		SELECT `+selectCols+` FROM x e JOIN users u ON u.telegram_id = e.user_id`,
		s.familyID, in.UserID, in.Amount, in.Category, in.SpentAt, in.Comment)
	e, err := scanExpense(row)
	return e, mapErr("create expense", err)
}

func (s *Store) Update(ctx context.Context, id int64, p Patch) (Expense, error) {
	if s.userID != 0 && p.UserID != nil {
		return Expense{}, invalid("user_id", "нельзя передать трату другому участнику")
	}
	q := &query{}
	sets := []string{"updated_at = now()"}
	if p.UserID != nil {
		sets = append(sets, "user_id = "+q.arg(*p.UserID))
	}
	if p.Amount != nil {
		sets = append(sets, "amount = "+q.arg(*p.Amount)+"::bigint / 100.0")
	}
	if p.Category != nil {
		sets = append(sets, "category = "+q.arg(*p.Category))
	}
	if p.SpentAt != nil {
		sets = append(sets, "spent_at = "+q.arg(*p.SpentAt)+"::date")
	}
	if p.Comment != nil {
		sets = append(sets, "comment = NULLIF("+q.arg(*p.Comment)+", '')")
	}
	conds := append(s.scope(q), "e.id = "+q.arg(id))

	row := s.pool.QueryRow(ctx, `
		WITH x AS (
			UPDATE expenses AS e SET `+strings.Join(sets, ", ")+`
			WHERE `+strings.Join(conds, " AND ")+`
			RETURNING e.*
		)
		SELECT `+selectCols+` FROM x e JOIN users u ON u.telegram_id = e.user_id`, q.args...)
	e, err := scanExpense(row)
	return e, mapErr("update expense", err)
}

// Delete removes an expense and returns it, so the bot can say what was deleted.
func (s *Store) Delete(ctx context.Context, id int64) (Expense, error) {
	q := &query{}
	conds := append(s.scope(q), "e.id = "+q.arg(id))
	row := s.pool.QueryRow(ctx, `
		WITH x AS (
			DELETE FROM expenses AS e WHERE `+strings.Join(conds, " AND ")+`
			RETURNING e.*
		)
		SELECT `+selectCols+` FROM x e JOIN users u ON u.telegram_id = e.user_id`, q.args...)
	e, err := scanExpense(row)
	return e, mapErr("delete expense", err)
}

func mapErr(op string, err error) error {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23503": // foreign_key_violation
		return invalid("user_id", "такого участника нет в семье")
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
