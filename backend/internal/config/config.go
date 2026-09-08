// Package config loads runtime configuration from environment variables
// (optionally via a local .env file), per docs/ai/01-stack.md.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Config holds all runtime configuration for the API process.
type Config struct {
	// HTTPAddr serves the public + admin + internal routers (everything
	// except /metrics, which is intentionally split off — see MetricsAddr).
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"`
	// MetricsAddr serves only GET /metrics, unauthenticated. Kept on a
	// separate listener (default loopback-only) so votes_accepted_total
	// with a poll_id label can't leak live results publicly before an
	// admin has published them (docs/ai/05-api-contract.md review: C7).
	MetricsAddr string `env:"METRICS_ADDR" envDefault:"127.0.0.1:9090"`

	DatabaseURL string `env:"DATABASE_URL,required"`
	RedisAddr   string `env:"REDIS_ADDR" envDefault:"127.0.0.1:6379"`
	RedisDB     int    `env:"REDIS_DB" envDefault:"0"`

	// AdminToken authenticates the admin router (single static bearer
	// token, compared with subtle.ConstantTimeCompare per
	// docs/ai/01-stack.md — no roles, no OIDC for this take-home).
	AdminToken string `env:"ADMIN_TOKEN,required"`

	// VoteTokenSecret signs vote tokens (HMAC-SHA256). VoteTokenSecretPrev
	// is optional and lets a secret rotation not invalidate tokens issued
	// under the previous secret mid-broadcast: verification tries both,
	// signing always uses the current one.
	VoteTokenSecret     string `env:"VOTE_TOKEN_SECRET,required"`
	VoteTokenSecretPrev string `env:"VOTE_TOKEN_SECRET_PREV" envDefault:""`

	// CORSAllowedOrigins is a comma-separated explicit allowlist — never
	// "*", since vote-token cookies are sent with credentials
	// (docs/ai/01-stack.md, "Граница с фронтендом").
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS" envSeparator:","`

	// CookieSecure controls the Secure flag on the vote_token cookie.
	// Defaults to true (SameSite=None requires Secure in real browsers);
	// set to false only for plain-http local development.
	CookieSecure bool `env:"COOKIE_SECURE" envDefault:"true"`

	// DefaultVotingWindowSeconds is the fallback voting window when a
	// poll doesn't specify one (docs/ai/02-load-model.md §6.4: default 5
	// minutes even though the spot is ~60s, since ~35% of votes arrive
	// after the spot ends).
	DefaultVotingWindowSeconds int `env:"DEFAULT_VOTING_WINDOW_SECONDS" envDefault:"300"`

	// RateLimitRPS/Burst bound the local per-IP token bucket shared by
	// /token and /votes (docs/ai/03-deduplication.md §3.2: draft
	// threshold 60 req/10s per IP, deliberately generous).
	RateLimitRPS   float64 `env:"RATE_LIMIT_RPS" envDefault:"6"`
	RateLimitBurst int     `env:"RATE_LIMIT_BURST" envDefault:"60"`

	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
}

// Load reads configuration from the environment. If a .env file exists at
// envPath it is loaded first (without overriding variables already set in
// the real environment), matching docs/ai/01-stack.md's
// caarlos0/env + .env choice.
func Load(envPath string) (Config, error) {
	if err := loadDotEnv(envPath); err != nil {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse env: %w", err)
	}
	return cfg, nil
}

// loadDotEnv is a minimal KEY=VALUE parser — no need to pull in a
// dependency for this: comments, blank lines, and optional surrounding
// quotes are supported, existing environment variables are never
// overridden.
func loadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}
