package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/observability"
)

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.obs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "observability_unavailable",
			"observability module is not configured")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	jobs, err := s.obs.ListJobs(r.Context(), identity.OrganizationID)
	if err != nil {
		s.log.Error("job listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list jobs")
		return
	}
	if jobs == nil {
		jobs = []observability.JobSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleListJobRuns(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.obs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "observability_unavailable",
			"observability module is not configured")
		return
	}
	jobID := chi.URLParam(r, "jobID")
	if _, err := uuid.Parse(jobID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid job id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	runs, err := s.obs.ListJobRuns(r.Context(), identity.OrganizationID, jobID)
	if err != nil {
		if errors.Is(err, observability.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "job not found")
			return
		}
		s.log.Error("job run listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list job runs")
		return
	}
	if runs == nil {
		runs = []observability.JobRun{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handlePipelineHealth(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.obs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "observability_unavailable",
			"observability module is not configured")
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	identity, _ := IdentityFrom(r.Context())
	health, err := s.obs.PipelineHealth(r.Context(), identity.OrganizationID, days)
	if err != nil {
		s.log.Error("pipeline health failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not compute health")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"health": health})
}

func (s *Server) handleFreshness(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.obs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "observability_unavailable",
			"observability module is not configured")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	freshness, err := s.obs.Freshness(r.Context(), identity.OrganizationID)
	if err != nil {
		s.log.Error("freshness evaluation failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not evaluate freshness")
		return
	}
	if freshness == nil {
		freshness = []observability.FreshnessStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"freshness": freshness})
}

func (s *Server) handleSetFreshnessExpectation(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermManageQuality)
	if !ok {
		return
	}
	if s.obs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "observability_unavailable",
			"observability module is not configured")
		return
	}
	var in struct {
		ExpectedIntervalSeconds int64 `json:"expected_interval_seconds"`
	}
	if err := decodeJSON(r, &in); err != nil || in.ExpectedIntervalSeconds <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request",
			"expected_interval_seconds must be a positive integer")
		return
	}
	datasetID := chi.URLParam(r, "datasetID")
	if _, err := uuid.Parse(datasetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid dataset id")
		return
	}
	if err := s.obs.SetFreshnessExpectation(
		r.Context(), id.OrganizationID, datasetID, in.ExpectedIntervalSeconds); err != nil {
		if errors.Is(err, observability.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "dataset not found")
			return
		}
		s.log.Error("freshness expectation failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not set expectation")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	alerts, err := s.obs.ListAlerts(r.Context(), identity.OrganizationID, r.URL.Query().Get("status"))
	if err != nil {
		s.log.Error("alert listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list alerts")
		return
	}
	if alerts == nil {
		alerts = []observability.Alert{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (s *Server) handleAcknowledgeAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermManageQuality)
	if !ok {
		return
	}
	alertID := chi.URLParam(r, "alertID")
	if _, err := uuid.Parse(alertID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid alert id")
		return
	}
	if err := s.obs.AcknowledgeAlert(r.Context(), id.OrganizationID, alertID); err != nil {
		if errors.Is(err, observability.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "open alert not found")
			return
		}
		s.log.Error("alert acknowledge failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not acknowledge alert")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "acknowledged"})
}

func (s *Server) handleResolveAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, auth.PermManageQuality)
	if !ok {
		return
	}
	alertID := chi.URLParam(r, "alertID")
	if _, err := uuid.Parse(alertID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid alert id")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = decodeJSON(r, &in) // body optional
	if err := s.obs.ResolveAlert(r.Context(), id.OrganizationID, alertID, in.Note); err != nil {
		if errors.Is(err, observability.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "unresolved alert not found")
			return
		}
		s.log.Error("alert resolve failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not resolve alert")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}
