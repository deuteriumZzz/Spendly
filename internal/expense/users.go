package expense

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrUserNotFound = errors.New("участник не найден")

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
