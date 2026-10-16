// Command spendly runs the family expense service.
//
//	spendly serve    — REST API, web panel and (if TELEGRAM_TOKEN is set) the bot
//	spendly migrate  — apply database migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // APP_TIMEZONE works in a minimal image without system tzdata

	tgbot "github.com/go-telegram/bot"
	"github.com/jackc/pgx/v5/pgxpool"

	"spendly/internal/agent"
	"spendly/internal/api"
	"spendly/internal/bot"
	"spendly/internal/expense"
	"spendly/migrations"
)

func main() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(ctx)
	case "migrate":
		err = migrate(ctx)
	default:
		err = fmt.Errorf("unknown command %q, use serve or migrate", cmd)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := connect(ctx, url, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrations.Up(ctx, pool); err != nil {
		return err
	}
	slog.Info("migrations applied")
	return nil
}

func serve(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	// Each bot message may hold a lock connection plus a query connection.
	pool, err := connect(ctx, cfg.DatabaseURL, int32(2*cfg.MaxConcurrency+8)) //nolint:gosec // MaxConcurrency ≤ 100, checked in loadConfig
	if err != nil {
		return err
	}
	defer pool.Close()
	db := expense.NewDB(pool)
	dialogs := bot.NewDialogs(pool)

	for id, name := range cfg.Members {
		if err := db.SaveMember(ctx, id, expense.DefaultFamilyID, name); err != nil {
			return err
		}
	}
	if cfg.SeedDemo {
		if err := seedDemo(ctx, db, cfg.Location); err != nil {
			return fmt.Errorf("seed demo data: %w", err)
		}
	}

	go pruneMessages(ctx, dialogs)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.New(db, expense.DefaultFamilyID, cfg.Location, cfg.PanelHosts).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpErr := make(chan error, 1)
	go func() {
		slog.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
		}
	}()

	if cfg.TelegramToken == "" {
		slog.Warn("TELEGRAM_TOKEN is not set: the bot is disabled, only the web panel is running")
	} else {
		llm := &agent.Client{
			BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, Model: cfg.LLMModel,
			HTTP: &http.Client{Timeout: 60 * time.Second},
		}
		b := bot.New(db, dialogs, agent.New(llm, cfg.Location), bot.Config{
			FamilyID:       expense.DefaultFamilyID,
			Members:        cfg.Members,
			RatePerHour:    cfg.RatePerHour,
			MaxConcurrency: cfg.MaxConcurrency,
			HistorySize:    10,
			ReplyTimeout:   90 * time.Second,
		})
		tg, err := tgbot.New(cfg.TelegramToken, tgbot.WithDefaultHandler(b.Handle))
		if err != nil {
			return fmt.Errorf("telegram: %w", err)
		}
		go tg.Start(ctx)
		slog.Info("telegram bot started (long polling)", "model", cfg.LLMModel)
		if len(cfg.Members) == 0 {
			slog.Warn("FAMILY_MEMBERS is empty: open mode, anyone who finds the bot joins the family and uses the LLM budget")
		}
	}

	select {
	case <-ctx.Done():
	case err := <-httpErr:
		return err
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// messageRetention bounds how long chat texts are stored. They are needed for
// 6 h of dialog context, 1 h of rate limiting and about a day of redelivery dedupe.
const messageRetention = 7 * 24 * time.Hour

// pruneMessages runs hourly; with several replicas each one prunes, which is harmless.
func pruneMessages(ctx context.Context, dialogs *bot.Dialogs) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		n, err := dialogs.Prune(ctx, time.Now().Add(-messageRetention))
		if err != nil && ctx.Err() == nil {
			slog.Error("prune messages", "err", err)
		} else if n > 0 {
			slog.Info("pruned old messages", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// connect waits for Postgres to come up (up to ~30 s) instead of failing on
// the first refused connection.
func connect(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		if err = pool.Ping(ctx); err == nil {
			return pool, nil
		}
		if attempt == 30 {
			pool.Close()
			return nil, fmt.Errorf("postgres unavailable: %w", err)
		}
		slog.Warn("waiting for postgres", "err", err)
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		}
	}
}
