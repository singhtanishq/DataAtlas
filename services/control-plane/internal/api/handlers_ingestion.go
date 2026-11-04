package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/ingestion"
)

func (s *Server) handleTriggerDiscovery(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermTriggerIngestion)
	if !ok {
		return
	}
	if s.ingestion == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ingestion_unavailable",
			"ingestion is not configured")
		return
	}
	sourceID := chi.URLParam(r, "sourceID")
	if _, err := uuid.Parse(sourceID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid source id")
		return
	}
	run, err := s.ingestion.TriggerDiscovery(r.Context(), id.OrganizationID, sourceID, "MANUAL")
	if err != nil {
		if errors.Is(err, ingestion.ErrRunNotFound) || hasPrefix(err.Error(), "no discovery pipeline") {
			writeError(w, r, http.StatusNotFound, "not_found", err.Error())
			return
		}
		if hasPrefix(err.Error(), "job queue unavailable") {
			writeError(w, r, http.StatusServiceUnavailable, "ingestion_unavailable", err.Error())
			return
		}
		s.log.Error("discovery trigger failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not trigger discovery")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.ingestion == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ingestion_unavailable",
			"ingestion is not configured")
		return
	}
	run, err := s.ingestion.GetRun(r.Context(), id.OrganizationID, chi.URLParam(r, "runID"))
	if err != nil {
		if errors.Is(err, ingestion.ErrRunNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "run not found")
			return
		}
		s.log.Error("run fetch failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not fetch run")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *Server) handleListSourceRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.ingestion == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ingestion_unavailable",
			"ingestion is not configured")
		return
	}
	runs, err := s.ingestion.ListRuns(r.Context(), id.OrganizationID, chi.URLParam(r, "sourceID"))
	if err != nil {
		s.log.Error("run listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list runs")
		return
	}
	if runs == nil {
		runs = []ingestion.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
