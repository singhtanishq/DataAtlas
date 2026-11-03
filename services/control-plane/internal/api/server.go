// Package api wires HTTP routing, middleware and handlers for the control
// plane API.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
)

// SessionCookie is the HTTP-only cookie carrying the opaque session token.
const SessionCookie = "dataatlas_session"

// Server holds the API dependencies.
type Server struct {
	cfg  *config.Config
	auth *auth.Service
	log  *slog.Logger
}

// NewServer builds the full HTTP handler.
func NewServer(cfg *config.Config, authSvc *auth.Service, logger *slog.Logger) http.Handler {
	s := &Server{cfg: cfg, auth: authSvc, log: logger}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.requestLogger(s.log))
	r.Use(middleware.Recoverer)

	r.Get("/healthz", s.health)
	r.Get("/readyz", s.ready)
	r.Get("/version", s.version)

	// Authentication endpoints are rate limited per client IP.
	r.Group(func(ar chi.Router) {
		ar.Use(s.rateLimit(authRateLimit))
		ar.Post("/v1/auth/register", s.handleRegister)
		ar.Post("/v1/auth/login", s.handleLogin)
	})

	r.Route("/v1", func(v1 chi.Router) {
		v1.Use(s.requireAuth)
		v1.Post("/auth/logout", s.handleLogout)
		v1.Get("/auth/me", s.handleMe)
		v1.Post("/auth/organizations/switch", s.handleSwitchOrganization)
	})

	return r
}

const (
	authRateLimitRequests = 20
	authRateLimitWindow   = time.Minute
)

// rateLimiter is a fixed-window limiter keyed by client IP.
type rateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	counts map[string]*rateBucket
}

type rateBucket struct {
	windowStart time.Time
	count       int
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{window: window, limit: limit, counts: map[string]*rateBucket{}}
}

// allow reports whether the key may proceed and when to retry if not.
func (rl *rateLimiter) allow(key string) (bool, time.Time) {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.counts[key]
	if !ok || now.Sub(b.windowStart) >= rl.window {
		rl.counts[key] = &rateBucket{windowStart: now, count: 1}
		return true, time.Time{}
	}
	if b.count >= rl.limit {
		return false, b.windowStart.Add(rl.window)
	}
	b.count++
	return true, time.Time{}
}

func (s *Server) rateLimit(rl *rateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				key = r.RemoteAddr
			}
			ok, retryAt := rl.allow(key)
			if !ok {
				retry := time.Until(retryAt).Round(time.Second)
				if retry < time.Second {
					retry = time.Second
				}
				w.Header().Set("Retry-After", retry.String())
				writeError(w, r, http.StatusTooManyRequests, "rate_limited",
					"too many requests; retry later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

var authRateLimit = newRateLimiter(authRateLimitRequests, authRateLimitWindow)

// requireAuth resolves the session cookie into an identity and stores it on
// the request context.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			writeError(w, r, http.StatusServiceUnavailable, "auth_unavailable",
				"authentication is not configured (DATABASE_URL missing)")
			return
		}
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated",
				"authentication required")
			return
		}
		identity, err := s.auth.IdentityForToken(r.Context(), c.Value)
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrSessionExpired):
				writeError(w, r, http.StatusUnauthorized, "session_expired", "session expired")
			case errors.Is(err, auth.ErrSessionRevoked):
				writeError(w, r, http.StatusUnauthorized, "session_revoked", "session revoked")
			default:
				writeError(w, r, http.StatusUnauthorized, "unauthenticated",
					"authentication required")
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
	})
}

func (s *Server) requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- response helpers -------------------------------------------------

type errorBody struct {
	Error errorEnvelope `json:"error"`
}

type errorEnvelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorEnvelope{
		Code:      code,
		Message:   message,
		RequestID: middleware.GetReqID(r.Context()),
	}})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
