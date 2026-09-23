package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	DatabaseURL       string
	AdminAPIKey       string
	Port              string
	ReconcileInterval time.Duration
	MigrationsDir     string
}

func Load() (Config, error) {
	interval := 30 * time.Second
	if v := os.Getenv("RECONCILE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("RECONCILE_INTERVAL: %w", err)
		}
		interval = d
	}

	cfg := Config{
		DatabaseURL:       envOr("DATABASE_URL", "postgres://voteguard:voteguard@localhost:5432/voteguard?sslmode=disable"),
		AdminAPIKey:       envOr("ADMIN_API_KEY", "dev-admin-key-change-me"),
		Port:              envOr("PORT", "8080"),
		ReconcileInterval: interval,
		MigrationsDir:     envOr("MIGRATIONS_DIR", "migrations"),
	}
	if cfg.AdminAPIKey == "" {
		return Config{}, fmt.Errorf("ADMIN_API_KEY must be set")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
