package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/ingestion"
)

func (s *Server) handleTriggerProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermTriggerIngestion)
	if !ok {
		return
	}
	if s.ingestion == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ingestion_unavailable",
			"ingestion is not configured")
		return
	}
	datasetID := chi.URLParam(r, "datasetID")
	if _, err := uuid.Parse(datasetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid dataset id")
		return
	}
	run, err := s.ingestion.TriggerProfile(r.Context(), id.OrganizationID, datasetID, "MANUAL")
	if err != nil {
		switch {
		case errors.Is(err, ingestion.ErrRunNotFound):
			writeError(w, r, http.StatusNotFound, "not_found", err.Error())
		default:
			s.log.Error("profile trigger failed", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "ingestion_unavailable", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

func (s *Server) handleLatestProfile(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.ingestion == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ingestion_unavailable",
			"ingestion is not configured")
		return
	}
	datasetID := chi.URLParam(r, "datasetID")
	if _, err := uuid.Parse(datasetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid dataset id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	profile, err := s.ingestion.LatestProfile(r.Context(), identity.OrganizationID, datasetID)
	if err != nil {
		if errors.Is(err, ingestion.ErrRunNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found",
				"no profiling results for this dataset yet")
			return
		}
		s.log.Error("profile fetch failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not fetch profile")
		return
	}
	if profile.Columns == nil {
		profile.Columns = []ingestion.ColumnProfile{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile})
}

func (s *Server) handleProfileHistory(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.ingestion == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ingestion_unavailable",
			"ingestion is not configured")
		return
	}
	datasetID := chi.URLParam(r, "datasetID")
	if _, err := uuid.Parse(datasetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid dataset id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	runs, err := s.ingestion.ProfileHistory(r.Context(), identity.OrganizationID, datasetID)
	if err != nil {
		s.log.Error("profile history failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not fetch profile history")
		return
	}
	if runs == nil {
		runs = []ingestion.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}
