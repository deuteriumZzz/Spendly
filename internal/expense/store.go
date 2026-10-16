package expense

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultFamilyID is the family created by the first migration.
const DefaultFamilyID int64 = 1

var (
	ErrNotFound     = errors.New("трата не найдена")
	ErrUserNotFound = errors.New("участник не найден")
)

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

// User is a family member; ID is the Telegram user id.
type User struct {
	ID       int64  `json:"id"`
	FamilyID int64  `json:"-"`
	Name     string `json:"name"`
}

func (db *DB) GetUser(ctx context.Context, id int64) (User, error) {
	u := User{ID: id}
	err := db.pool.QueryRow(ctx, `SELECT family_id, name FROM users WHERE telegram_id = $1`, id).
		Scan(&u.FamilyID, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// EnsureUser adds a newcomer to the family (open mode). An existing user keeps
// their family and name.
func (db *DB) EnsureUser(ctx context.Context, id, familyID int64, name string) (User, error) {
	_, err := db.pool.Exec(ctx, `INSERT INTO users (telegram_id, family_id, name) VALUES ($1, $2, $3)
		ON CONFLICT (telegram_id) DO NOTHING`, id, familyID, name)
	if err != nil {
		return User{}, fmt.Errorf("ensure user: %w", err)
	}
	return db.GetUser(ctx, id)
}

// SaveMember upserts a member from config; config is the source of truth for the name.
func (db *DB) SaveMember(ctx context.Context, id, familyID int64, name string) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO users (telegram_id, family_id, name) VALUES ($1, $2, $3)
		ON CONFLICT (telegram_id) DO UPDATE SET name = EXCLUDED.name`, id, familyID, name)
	if err != nil {
		return fmt.Errorf("save member: %w", err)
	}
	return nil
}

func (db *DB) Users(ctx context.Context, familyID int64) ([]User, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT telegram_id, family_id, name FROM users WHERE family_id = $1 ORDER BY name`, familyID)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	users, err := pgx.CollectRows(rows, pgx.RowToStructByPos[User])
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	if users == nil {
		users = []User{}
	}
	return users, nil
}

// Message is one line of the bot dialog history.
type Message struct {
	Role    string // "user" | "assistant"
	Content string
}

// SaveMessage stores a dialog message and returns its id. For an incoming
// message updateID dedupes Telegram redeliveries: ok=false means this update
// was already processed.
func (db *DB) SaveMessage(ctx context.Context, userID int64, updateID *int64, m Message) (id int64, ok bool, err error) {
	err = db.pool.QueryRow(ctx, `INSERT INTO messages (user_id, update_id, role, content)
		VALUES ($1, $2, $3, $4) ON CONFLICT (update_id) DO NOTHING RETURNING id`,
		userID, updateID, m.Role, m.Content).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("save message: %w", err)
	}
	return id, true, nil
}

// historyWindow drops stale context: yesterday's dialog should not change what
// «последнее» means today.
const historyWindow = 6 * time.Hour

// History returns up to n messages sent before message beforeID, oldest first.
func (db *DB) History(ctx context.Context, userID, beforeID int64, n int) ([]Message, error) {
	rows, err := db.pool.Query(ctx, `SELECT role, content FROM messages
		WHERE user_id = $1 AND id < $2 AND created_at > $3
		ORDER BY id DESC LIMIT $4`, userID, beforeID, time.Now().Add(-historyWindow), n)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	msgs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Message])
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	slices.Reverse(msgs)
	return msgs, nil
}

// PruneMessages deletes dialog messages older than before: chat texts are
// personal data and are needed only for a few hours of context.
func (db *DB) PruneMessages(ctx context.Context, before time.Time) (int64, error) {
	tag, err := db.pool.Exec(ctx, `DELETE FROM messages WHERE created_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("prune messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountUserMessages counts incoming messages since t — the per-user rate limit.
func (db *DB) CountUserMessages(ctx context.Context, userID int64, since time.Time) (int, error) {
	var n int
	err := db.pool.QueryRow(ctx, `SELECT count(*) FROM messages
		WHERE user_id = $1 AND role = 'user' AND created_at > $2`, userID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}
	return n, nil
}

// LockUser serializes one user's messages across all replicas with a Postgres
// advisory lock. It holds one pool connection until unlock is called.
func (db *DB) LockUser(ctx context.Context, userID int64) (unlock func(), err error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("lock user: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, userID); err != nil {
		conn.Release()
		return nil, fmt.Errorf("lock user: %w", err)
	}
	return func() {
		// The request context may already be cancelled; unlock regardless.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, userID); err != nil {
			// Unknown session state: close it so the lock dies with the session.
			_ = conn.Conn().Close(ctx)
		}
		conn.Release()
	}, nil
}
