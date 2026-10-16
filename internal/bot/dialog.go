package bot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"spendly/internal/agent"
)

// Dialogs keeps the bot's chat state in Postgres — history, dedupe of
// Telegram redeliveries, rate-limit counts and per-user locks — so several
// replicas can share it. It is bot state, not expense data.
type Dialogs struct{ pool *pgxpool.Pool }

func NewDialogs(pool *pgxpool.Pool) *Dialogs { return &Dialogs{pool: pool} }

// Save stores a dialog message and returns its id. For an incoming message
// updateID dedupes Telegram redeliveries: ok=false means this update was
// already processed.
func (d *Dialogs) Save(ctx context.Context, userID int64, updateID *int64, role, content string) (id int64, ok bool, err error) {
	err = d.pool.QueryRow(ctx, `INSERT INTO messages (user_id, update_id, role, content)
		VALUES ($1, $2, $3, $4) ON CONFLICT (update_id) DO NOTHING RETURNING id`,
		userID, updateID, role, content).Scan(&id)
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
func (d *Dialogs) History(ctx context.Context, userID, beforeID int64, n int) ([]agent.Message, error) {
	rows, err := d.pool.Query(ctx, `SELECT role, content FROM messages
		WHERE user_id = $1 AND id < $2 AND created_at > $3
		ORDER BY id DESC LIMIT $4`, userID, beforeID, time.Now().Add(-historyWindow), n)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	msgs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (agent.Message, error) {
		var m agent.Message
		return m, r.Scan(&m.Role, &m.Content)
	})
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	slices.Reverse(msgs)
	return msgs, nil
}

// CountUserMessages counts incoming messages since t — the per-user rate limit.
func (d *Dialogs) CountUserMessages(ctx context.Context, userID int64, since time.Time) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM messages
		WHERE user_id = $1 AND role = 'user' AND created_at > $2`, userID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}
	return n, nil
}

// Prune deletes messages older than before: chat texts are personal data and
// are needed only for a few hours of context.
func (d *Dialogs) Prune(ctx context.Context, before time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM messages WHERE created_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("prune messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Lock serializes one user's messages across all replicas with a Postgres
// advisory lock. It holds one pool connection until unlock is called.
func (d *Dialogs) Lock(ctx context.Context, userID int64) (unlock func(), err error) {
	conn, err := d.pool.Acquire(ctx)
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
