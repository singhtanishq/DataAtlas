package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/ingestion"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/quality"
)

func (s *Server) handleCreateCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermManageQuality)
	if !ok {
		return
	}
	if s.quality == nil {
		writeError(w, r, http.StatusServiceUnavailable, "quality_unavailable", "quality module is not configured")
		return
	}
	var in quality.CreateCheckInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	check, err := s.quality.CreateCheck(r.Context(), id.OrganizationID, in)
	if err != nil {
		if isUserErrorMessage(err.Error()) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.log.Error("check creation failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not create check")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"check": check})
}

func (s *Server) handleListChecks(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.quality == nil {
		writeError(w, r, http.StatusServiceUnavailable, "quality_unavailable", "quality module is not configured")
		return
	}
	datasetID := r.URL.Query().Get("dataset_id")
	if datasetID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "dataset_id is required")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	checks, err := s.quality.ListChecks(r.Context(), identity.OrganizationID, datasetID)
	if err != nil {
		s.log.Error("check listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list checks")
		return
	}
	if checks == nil {
		checks = []quality.Check{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermManageQuality)
	if !ok {
		return
	}
	if s.quality == nil {
		writeError(w, r, http.StatusServiceUnavailable, "quality_unavailable", "quality module is not configured")
		return
	}
	var in quality.UpdateCheckInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	identity, _ := IdentityFrom(r.Context())
	check, err := s.quality.UpdateCheck(r.Context(), identity.OrganizationID, chi.URLParam(r, "checkID"), in)
	if err != nil {
		if errors.Is(err, quality.ErrCheckNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "check not found")
			return
		}
		if isUserErrorMessage(err.Error()) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.log.Error("check update failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not update check")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"check": check})
}

func (s *Server) handleDeleteCheck(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermManageQuality)
	if !ok {
		return
	}
	if s.quality == nil {
		writeError(w, r, http.StatusServiceUnavailable, "quality_unavailable", "quality module is not configured")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := s.quality.DeleteCheck(r.Context(), identity.OrganizationID, chi.URLParam(r, "checkID")); err != nil {
		if errors.Is(err, quality.ErrCheckNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "check not found")
			return
		}
		s.log.Error("check delete failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not delete check")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleTriggerQualityRun(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermManageQuality)
	if !ok {
		return
	}
	if s.quality == nil {
		writeError(w, r, http.StatusServiceUnavailable, "quality_unavailable", "quality module is not configured")
		return
	}
	run, err := s.quality.TriggerQualityRun(r.Context(), id.OrganizationID, "MANUAL")
	if err != nil {
		if isUserErrorMessage(err.Error()) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.log.Error("quality trigger failed", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "quality_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

func (s *Server) handleGetQualityRun(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.quality == nil {
		writeError(w, r, http.StatusServiceUnavailable, "quality_unavailable", "quality module is not configured")
		return
	}
	runID := chi.URLParam(r, "runID")
	if _, err := uuid.Parse(runID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid run id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	view, err := s.quality.GetRun(r.Context(), identity.OrganizationID, runID)
	if err != nil {
		if errors.Is(err, ingestion.ErrRunNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "run not found")
			return
		}
		s.log.Error("quality run fetch failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not fetch run")
		return
	}
	if view.Results == nil {
		view.Results = []quality.RunResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": view})
}

// isUserErrorMessage reports whether an error carries a user-safe message.
func isUserErrorMessage(msg string) bool {
	if msg == "" {
		return false
	}
	prefixes := []string{
		"unsupported", "dataset ", "no enabled", "no ingestion pipeline",
		"name and dataset_id", "freshness checks", "regex check",
		"numeric_range", "custom_sql", "structured checks", "invalid SQL",
		"custom_sql must", "unsafe identifier",
	}
	for _, p := range prefixes {
		if len(msg) >= len(p) && msg[:len(p)] == p {
			return true
		}
	}
	return false
}
