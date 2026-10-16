package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	DatabaseURL    string
	HTTPAddr       string
	Location       *time.Location
	TelegramToken  string
	LLMBaseURL     string
	LLMAPIKey      string
	LLMModels      []string
	Members        map[int64]string
	PanelHosts     []string
	SeedDemo       bool
	MaxConcurrency int
	RatePerHour    int
}

// loadConfig reads the environment and reports every problem at once.
func loadConfig() (config, error) {
	c := config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		HTTPAddr:      envOr("HTTP_ADDR", ":8080"),
		TelegramToken: os.Getenv("TELEGRAM_TOKEN"),
		LLMBaseURL:    envOr("LLM_BASE_URL", "https://api.anthropic.com/v1/"),
		LLMAPIKey:     os.Getenv("LLM_API_KEY"),
		LLMModels:     splitList(os.Getenv("LLM_MODEL")),
		PanelHosts:    splitList(strings.ToLower(os.Getenv("PANEL_HOSTS"))),
		SeedDemo:      os.Getenv("SEED_DEMO") == "true",
	}
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if c.TelegramToken != "" && len(c.LLMModels) == 0 {
		errs = append(errs, errors.New("LLM_MODEL is required when TELEGRAM_TOKEN is set"))
	}
	var err error
	if c.Location, err = time.LoadLocation(envOr("APP_TIMEZONE", "Europe/Moscow")); err != nil {
		errs = append(errs, fmt.Errorf("APP_TIMEZONE: %w", err))
	}
	if c.MaxConcurrency, err = intEnv("MAX_CONCURRENCY", 8); err != nil {
		errs = append(errs, err)
	} else if c.MaxConcurrency > 100 {
		errs = append(errs, errors.New("MAX_CONCURRENCY: at most 100 (each message may hold two DB connections)"))
	}
	if c.RatePerHour, err = intEnv("RATE_LIMIT_PER_HOUR", 60); err != nil {
		errs = append(errs, err)
	}
	if c.Members, err = parseMembers(os.Getenv("FAMILY_MEMBERS")); err != nil {
		errs = append(errs, err)
	}
	return c, errors.Join(errs...)
}

// parseMembers reads "123456:Мама,789012:Папа".
func parseMembers(s string) (map[int64]string, error) {
	members := map[int64]string{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		idStr, name, _ := strings.Cut(item, ":")
		id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
		name = strings.TrimSpace(name)
		if err != nil || id <= 0 || name == "" {
			return nil, fmt.Errorf("FAMILY_MEMBERS: %q, expected telegram_id:Имя", item)
		}
		members[id] = name
	}
	return members, nil
}

// splitList reads "a, b,c" and drops empty items.
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: expected a positive integer, got %q", key, v)
	}
	return n, nil
}
