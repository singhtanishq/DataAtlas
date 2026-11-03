package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
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

	router := buildRouter(cfg, logger)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
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

func buildRouter(cfg *config.Config, logger *slog.Logger) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(requestLogger(logger))
	r.Use(middleware.Recoverer)

	r.Get("/healthz", healthHandler(cfg))
	r.Get("/readyz", readyHandler(cfg))
	r.Get("/version", versionHandler(cfg))

	return r
}

// requestLogger emits one structured log line per request with timing.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
			start := time.Now()
			defer func() {
				logger.Info("http_request",
					"method", req.Method,
					"path", req.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration_ms", time.Since(start).Milliseconds(),
					"request_id", middleware.GetReqID(req.Context()),
				)
			}()
			next.ServeHTTP(ww, req)
		})
	}
}

func healthHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"service":  "control-plane",
			"env":      cfg.AppEnv,
			"time_utc": time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// readyHandler reports dependency readiness. Dependencies are wired in
// later milestones; until then the process reports ready once serving.
func readyHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		status := http.StatusOK
		writeJSON(w, status, map[string]any{
			"status": "ready",
			"env":    cfg.AppEnv,
			"checks": map[string]string{},
		})
	}
}

func versionHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"service": "control-plane",
			"version": cfg.Version,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
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
