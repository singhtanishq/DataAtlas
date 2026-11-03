package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("CONTROL_PLANE_PORT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppEnv != EnvDevelopment {
		t.Errorf("AppEnv = %q, want %q", cfg.AppEnv, EnvDevelopment)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.NATSURL != "nats://localhost:4222" {
		t.Errorf("NATSURL = %q, unexpected default", cfg.NATSURL)
	}
	if cfg.ShutdownGrace != 10*time.Second {
		t.Errorf("ShutdownGrace = %v, want 10s", cfg.ShutdownGrace)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("CONTROL_PLANE_PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SHUTDOWN_GRACE_SECONDS", "5")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppEnv != EnvProduction || cfg.Port != "9090" || cfg.LogLevel != "debug" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.ShutdownGrace != 5*time.Second {
		t.Errorf("ShutdownGrace = %v, want 5s", cfg.ShutdownGrace)
	}
}

func TestLoadInvalidEnv(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	if _, err := Load(); err == nil {
		t.Error("Load() expected error for invalid APP_ENV, got nil")
	}
}

func TestLoadInvalidGrace(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("SHUTDOWN_GRACE_SECONDS", "-3")
	if _, err := Load(); err == nil {
		t.Error("Load() expected error for negative SHUTDOWN_GRACE_SECONDS, got nil")
	}
}
