package api

import (
	"net/http"
	"time"
)

// systemHandlers expose liveness, readiness and version information.

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"service":  "control-plane",
		"env":      s.cfg.AppEnv,
		"time_utc": time.Now().UTC().Format(time.RFC3339),
	})
}

// ready reports dependency readiness. Database and broker probes are wired
// in as their integrations land.
func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"env":    s.cfg.AppEnv,
		"checks": map[string]string{},
	})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "control-plane",
		"version": s.cfg.Version,
	})
}
