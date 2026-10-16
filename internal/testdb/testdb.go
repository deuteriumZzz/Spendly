// Package testdb gives integration tests a migrated, empty Postgres database.
package testdb

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"spendly/migrations"
)

var (
	once     sync.Once
	setupErr error
)

// New returns a pool to TEST_DATABASE_URL with all tables but families
// truncated. Skips the test when the variable is not set.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL не задана — интеграционный тест пропущен")
	}
	ctx := context.Background()

	once.Do(func() { setupErr = setup(ctx, url) })
	if setupErr != nil {
		t.Fatal(setupErr)
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE users, expenses, messages, invites RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	return pool
}

// setup creates the test database if needed and applies migrations once per test binary.
func setup(ctx context.Context, url string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return err
	}
	name := cfg.Database
	cfg.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect to postgres db: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			return fmt.Errorf("create test db: %w", err)
		}
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrations.Up(ctx, pool)
}
