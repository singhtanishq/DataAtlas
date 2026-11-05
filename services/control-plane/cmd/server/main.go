package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/api"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/catalog"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/crypto"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/ingestion"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/lineage"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/observability"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/quality"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/sources"
)

const serverReadHeaderTimeout = 10 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.LogLevel, cfg.AppEnv)
	slog.SetDefault(logger)

	ctx := context.Background()

	var (
		authSvc      *auth.Service
		sourceSvc    *sources.Service
		ingestionSvc *ingestion.Service
		catalogSvc   *catalog.Service
		qualitySvc   *quality.Service
		lineageSvc   *lineage.Service
		obsSvc       *observability.Service
	)
	if cfg.DatabaseURL != "" {
		pool, err := db.NewPool(ctx, cfg.DatabaseURL)
		if err != nil {
			logger.Error("failed to connect to database", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		authSvc = auth.NewService(pool)

		// Credential encryption and the message broker power the source
		// registry; both degrade gracefully when unconfigured.
		if cfg.AppEncryptionKey != "" {
			cipher, err := crypto.NewCipher(cfg.AppEncryptionKey)
			if err != nil {
				logger.Error("invalid APP_ENCRYPTION_KEY", "error", err)
				os.Exit(1)
			}
			var nc *nats.Conn
			if cfg.NATSURL != "" {
				nc, err = nats.Connect(cfg.NATSURL, nats.Timeout(5*time.Second), nats.MaxReconnects(-1))
				if err != nil {
					logger.Warn("NATS unavailable; connection tests will fail until it returns", "error", err)
					nc = nil
				} else {
					defer nc.Close()
					logger.Info("connected to NATS", "url", cfg.NATSURL)
				}
			}
			sourceSvc = sources.NewService(pool, cipher, nc, cfg.NATSURL)

			// Discovery runs publish durable jobs through JetStream; the
			// service reconnects lazily when the broker was down at boot.
			var js nats.JetStreamContext
			if nc != nil {
				if jsCtx, jsErr := nc.JetStream(); jsErr == nil {
					js = jsCtx
				}
			}
			ingestionSvc = ingestion.NewService(pool, js, cfg.NATSURL)
			qualitySvc = quality.NewService(pool, js, cfg.NATSURL)
		} else {
			logger.Warn("APP_ENCRYPTION_KEY not set; source registration disabled")
		}
		catalogSvc = catalog.NewService(pool)
		logger.Info("database connection pool established")
	} else {
		logger.Warn("DATABASE_URL not set; auth endpoints disabled")
	}

	handler := api.NewServer(cfg, authSvc, sourceSvc, ingestionSvc, catalogSvc, qualitySvc, lineageSvc, obsSvc, logger)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
	}

	// Graceful shutdown: stop accepting new requests, drain in-flight ones.
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("control plane listening", "addr", srv.Addr, "env", cfg.AppEnv)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		logger.Info("shutdown signal received, draining connections")
		drainCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
		defer cancel()
		if err := srv.Shutdown(drainCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}

	logger.Info("control plane stopped")
}

func newLogger(level, env string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}
	var handler slog.Handler
	if env == config.EnvDevelopment {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
