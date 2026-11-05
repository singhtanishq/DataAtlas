package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/lineage"
)

// handleIngestOpenLineage accepts OpenLineage runtime events. The endpoint
// is authenticated and tenant-scoped: events are processed for the caller's
// organization only.
func (s *Server) handleIngestOpenLineage(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authorize(w, r, auth.PermTriggerIngestion)
	if !ok {
		return
	}
	if s.lineage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "lineage_unavailable", "lineage module is not configured")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "could not read body")
		return
	}
	result, err := s.lineage.IngestOpenLineage(r.Context(), identity.OrganizationID, body)
	if err != nil {
		if errors.Is(err, lineage.ErrInvalidEvent) {
			writeError(w, r, http.StatusBadRequest, "invalid_event", err.Error())
			return
		}
		s.log.Error("openlineage ingestion failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not process event: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleIngestDBTManifest(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermTriggerIngestion)
	if !ok {
		return
	}
	if s.lineage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "lineage_unavailable", "lineage module is not configured")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "could not read body")
		return
	}
	sourceID := r.URL.Query().Get("source_id")
	if sourceID != "" {
		if _, err := uuid.Parse(sourceID); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid source id")
			return
		}
	}
	identity, _ := IdentityFrom(r.Context())
	result, err := s.lineage.ProcessDBTManifest(r.Context(), identity.OrganizationID, sourceID, body)
	if err != nil {
		if errors.Is(err, lineage.ErrInvalidManifest) {
			writeError(w, r, http.StatusBadRequest, "invalid_manifest", err.Error())
			return
		}
		s.log.Error("dbt manifest ingestion failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not process manifest")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLineageTraverse(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.lineage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "lineage_unavailable", "lineage module is not configured")
		return
	}
	assetID := chi.URLParam(r, "assetID")
	if _, err := uuid.Parse(assetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid asset id")
		return
	}
	direction := chi.URLParam(r, "direction")
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	identity, _ := IdentityFrom(r.Context())
	view, err := s.lineage.Traverse(r.Context(), identity.OrganizationID, assetID, direction, depth)
	if err != nil {
		if errors.Is(err, lineage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "asset not found")
			return
		}
		if direction != "upstream" && direction != "downstream" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.log.Error("lineage traversal failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not traverse lineage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"graph": view})
}

func (s *Server) handleImpactAnalysis(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.lineage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "lineage_unavailable", "lineage module is not configured")
		return
	}
	assetID := chi.URLParam(r, "assetID")
	if _, err := uuid.Parse(assetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid asset id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	analysis, err := s.lineage.AnalyzeImpact(r.Context(), identity.OrganizationID, assetID)
	if err != nil {
		if errors.Is(err, lineage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "asset not found")
			return
		}
		s.log.Error("impact analysis failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not analyze impact: "+err.Error())
		return
	}
	if analysis.DownstreamDatasets == nil {
		analysis.DownstreamDatasets = []lineage.GraphNode{}
	}
	if analysis.DependentJobs == nil {
		analysis.DependentJobs = []string{}
	}
	if analysis.QualityChecksAtRisk == nil {
		analysis.QualityChecksAtRisk = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"impact": analysis})
}
