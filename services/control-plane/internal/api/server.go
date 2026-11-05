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
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/catalog"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/governance"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/ingestion"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/lineage"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/observability"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/quality"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/sources"
)

// SessionCookie is the HTTP-only cookie carrying the opaque session token.
const SessionCookie = "dataatlas_session"

// Server holds the API dependencies.
type Server struct {
	cfg       *config.Config
	auth      *auth.Service
	sources   *sources.Service
	ingestion *ingestion.Service
	catalog   *catalog.Service
	quality   *quality.Service
	lineage   *lineage.Service
	obs       *observability.Service
	gov       *governance.Service
	log       *slog.Logger
}

// NewServer builds the full HTTP handler.
func NewServer(
	cfg *config.Config, authSvc *auth.Service, sourceSvc *sources.Service,
	ingestionSvc *ingestion.Service, catalogSvc *catalog.Service,
	qualitySvc *quality.Service, lineageSvc *lineage.Service,
	obsSvc *observability.Service, govSvc *governance.Service, logger *slog.Logger,
) http.Handler {
	s := &Server{cfg: cfg, auth: authSvc, sources: sourceSvc, ingestion: ingestionSvc,
		catalog: catalogSvc, quality: qualitySvc, lineage: lineageSvc, obs: obsSvc,
		gov: govSvc, log: logger}

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

		// Data source registry.
		v1.Post("/sources", s.handleRegisterSource)
		v1.Get("/sources", s.handleListSources)
		v1.Get("/sources/{sourceID}", s.handleGetSource)
		v1.Delete("/sources/{sourceID}", s.handleDeleteSource)
		v1.Post("/sources/{sourceID}/test", s.handleTestSource)
		v1.Post("/sources/{sourceID}/discover", s.handleTriggerDiscovery)
		v1.Get("/sources/{sourceID}/runs", s.handleListSourceRuns)
		v1.Get("/ingestion/runs/{runID}", s.handleGetRun)

		// Catalog.
		v1.Get("/catalog/datasets", s.handleSearchDatasets)
		v1.Get("/catalog/datasets/{datasetID}", s.handleDatasetDetails)
		v1.Get("/catalog/datasets/{datasetID}/columns", s.handleDatasetColumns)
		v1.Patch("/catalog/datasets/{datasetID}", s.handleUpdateDatasetDescription)
		v1.Post("/catalog/datasets/{datasetID}/owners", s.handleAssignOwner)
		v1.Post("/catalog/datasets/{datasetID}/tags", s.handleAddTag)
		v1.Delete("/catalog/datasets/{datasetID}/tags/{tagID}", s.handleRemoveTag)

		// Profiling.
		v1.Post("/catalog/datasets/{datasetID}/profile", s.handleTriggerProfile)
		v1.Get("/catalog/datasets/{datasetID}/profile", s.handleLatestProfile)
		v1.Get("/catalog/datasets/{datasetID}/profiles", s.handleProfileHistory)

		// Quality.
		v1.Post("/quality/checks", s.handleCreateCheck)
		v1.Get("/quality/checks", s.handleListChecks)
		v1.Patch("/quality/checks/{checkID}", s.handleUpdateCheck)
		v1.Delete("/quality/checks/{checkID}", s.handleDeleteCheck)
		v1.Post("/quality/runs", s.handleTriggerQualityRun)
		v1.Get("/quality/runs/{runID}", s.handleGetQualityRun)

		// Lineage.
		v1.Post("/lineage/openlineage", s.handleIngestOpenLineage)
		v1.Post("/lineage/dbt/manifest", s.handleIngestDBTManifest)
		v1.Get("/lineage/{assetID}/{direction}", s.handleLineageTraverse)
		v1.Get("/lineage/{assetID}/impact", s.handleImpactAnalysis)

		// Pipeline observability.
		v1.Get("/jobs", s.handleListJobs)
		v1.Get("/jobs/{jobID}/runs", s.handleListJobRuns)
		v1.Get("/observability/health", s.handlePipelineHealth)
		v1.Get("/freshness", s.handleFreshness)
		v1.Post("/catalog/datasets/{datasetID}/freshness", s.handleSetFreshnessExpectation)

		// Governance.
		v1.Post("/glossary/terms", s.handleCreateGlossaryTerm)
		v1.Get("/glossary/terms", s.handleListGlossaryTerms)
		v1.Post("/glossary/terms/{termID}/assets", s.handleAttachGlossaryTerm)
		v1.Get("/assets/{assetID}/terms", s.handleAssetTerms)
		v1.Post("/assets/{assetID}/classifications", s.handleSetAssetClassification)
		v1.Get("/assets/{assetID}/classifications", s.handleAssetClassifications)
		v1.Post("/catalog/columns/{columnID}/classifications", s.handleSetColumnClassification)
		v1.Get("/audit-logs", s.handleListAudit)

		// Alerts.
		v1.Get("/alerts", s.handleListAlerts)
		v1.Post("/alerts/{alertID}/acknowledge", s.handleAcknowledgeAlert)
		v1.Post("/alerts/{alertID}/resolve", s.handleResolveAlert)
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
