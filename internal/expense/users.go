package expense

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	ErrUserNotFound  = errors.New("участник не найден")
	ErrInviteInvalid = errors.New("приглашение недействительно")
)

// ValidateName trims a member's display name («Мама», «Сын»).
func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 40 {
		return "", &ValidationError{Field: "name", Msg: "имя — от 1 до 40 символов"}
	}
	return name, nil
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

// SaveMember adds a member from config. The config name is only the initial one:
// after that the head renames people in the panel, and a restart must not undo it.
func (db *DB) SaveMember(ctx context.Context, id, familyID int64, name string) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO users (telegram_id, family_id, name) VALUES ($1, $2, $3)
		ON CONFLICT (telegram_id) DO NOTHING`, id, familyID, name)
	if err != nil {
		return fmt.Errorf("save member: %w", err)
	}
	return nil
}

// RenameUser changes a member's display name within the family.
func (db *DB) RenameUser(ctx context.Context, familyID, id int64, name string) (User, error) {
	tag, err := db.pool.Exec(ctx, `UPDATE users SET name = $3 WHERE telegram_id = $1 AND family_id = $2`,
		id, familyID, name)
	if err != nil {
		return User{}, fmt.Errorf("rename user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return User{}, ErrUserNotFound
	}
	return User{ID: id, FamilyID: familyID, Name: name}, nil
}

// Invite is a one-time link into the family; Code goes into t.me/<bot>?start=<code>.
type Invite struct {
	Code      string    `json:"-"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateInvite issues a code valid for 7 days. rand.Text is 128 bits of base32,
// which also fits Telegram's start parameter alphabet.
func (db *DB) CreateInvite(ctx context.Context, familyID int64, name string) (Invite, error) {
	inv := Invite{Code: rand.Text(), Name: name}
	err := db.pool.QueryRow(ctx, `INSERT INTO invites (code, family_id, name, expires_at)
		VALUES ($1, $2, $3, now() + interval '7 days') RETURNING expires_at`, inv.Code, familyID, name).
		Scan(&inv.ExpiresAt)
	if err != nil {
		return Invite{}, fmt.Errorf("create invite: %w", err)
	}
	return inv, nil
}

// AcceptInvite spends the code and puts the user into its family under the
// invite's name. The UPDATE claims the row, so two people with one link
// cannot both get in.
func (db *DB) AcceptInvite(ctx context.Context, code string, userID int64) (User, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("accept invite: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op after Commit

	u := User{ID: userID}
	err = tx.QueryRow(ctx, `UPDATE invites SET used_by = $2, used_at = now()
		WHERE code = $1 AND used_at IS NULL AND expires_at > now() RETURNING family_id, name`, code, userID).
		Scan(&u.FamilyID, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInviteInvalid
	}
	if err != nil {
		return User{}, fmt.Errorf("accept invite: %w", err)
	}
	// An existing member just gets the new name. A member of another family is
	// not moved: the rollback leaves the code unspent.
	err = tx.QueryRow(ctx, `INSERT INTO users (telegram_id, family_id, name) VALUES ($1, $2, $3)
		ON CONFLICT (telegram_id) DO UPDATE SET name = EXCLUDED.name WHERE users.family_id = EXCLUDED.family_id
		RETURNING name`, userID, u.FamilyID, u.Name).Scan(&u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInviteInvalid
	}
	if err != nil {
		return User{}, fmt.Errorf("accept invite: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("accept invite: %w", err)
	}
	return u, nil
}

// InvitedUser returns a member who joined by an invite link, or ErrUserNotFound.
func (db *DB) InvitedUser(ctx context.Context, id int64) (User, error) {
	u := User{ID: id}
	err := db.pool.QueryRow(ctx, `SELECT u.family_id, u.name FROM users u
		WHERE u.telegram_id = $1
		  AND EXISTS (SELECT 1 FROM invites i WHERE i.used_by = u.telegram_id AND i.family_id = u.family_id)`, id).
		Scan(&u.FamilyID, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("invited user: %w", err)
	}
	return u, nil
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
