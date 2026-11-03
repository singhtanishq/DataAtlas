// Package config loads control-plane configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Environment names recognized by the control plane.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Config holds all runtime configuration for the control plane.
type Config struct {
	AppEnv        string
	Port          string
	LogLevel      string
	Version       string
	ShutdownGrace time.Duration
	DatabaseURL   string
	RedisURL      string
	NATSURL       string
}

// Load reads configuration from environment variables, applying defaults.
func Load() (*Config, error) {
	cfg := &Config{
		AppEnv:        envOr("APP_ENV", EnvDevelopment),
		Port:          envOr("CONTROL_PLANE_PORT", "8080"),
		LogLevel:      envOr("LOG_LEVEL", "info"),
		Version:       envOr("APP_VERSION", "0.1.0"),
		ShutdownGrace: 10 * time.Second,
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		RedisURL:      envOr("REDIS_URL", "redis://localhost:6379/0"),
		NATSURL:       envOr("NATS_URL", "nats://localhost:4222"),
	}

	if cfg.AppEnv != EnvDevelopment && cfg.AppEnv != EnvProduction && cfg.AppEnv != "test" {
		return nil, fmt.Errorf("invalid APP_ENV %q", cfg.AppEnv)
	}

	if grace := os.Getenv("SHUTDOWN_GRACE_SECONDS"); grace != "" {
		seconds, err := strconv.Atoi(grace)
		if err != nil || seconds <= 0 {
			return nil, errors.New("SHUTDOWN_GRACE_SECONDS must be a positive integer")
		}
		cfg.ShutdownGrace = time.Duration(seconds) * time.Second
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
