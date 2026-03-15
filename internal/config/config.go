package config

import (
	"fmt"
	"net/url"

	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

type Config struct {
	Port         string `env:"PORT" envDefault:"8080"`
	TargetOrigin string `env:"TARGET_ORIGIN" envDefault:"https://httpbin.org"`
	DatabaseURL  string `env:"DATABASE_URL" envDefault:"file:p2pstream.db?cache=shared&mode=rwc"`
	Env          string `env:"ENV" envDefault:"development"` // development or production

	ParsedTargetOrigin *url.URL `env:"-"`
}

// Load reads .env files and environment variables into the Config struct.
func Load() (*Config, error) {
	// Attempt to load .env file; it's okay if it doesn't exist
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %w", err)
	}

	parsed, err := url.Parse(cfg.TargetOrigin)
	if err != nil {
		return nil, fmt.Errorf("invalid TARGET_ORIGIN %q: %w", cfg.TargetOrigin, err)
	}
	cfg.ParsedTargetOrigin = parsed

	return cfg, nil
}
