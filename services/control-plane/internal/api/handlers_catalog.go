package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/catalog"
)

func (s *Server) handleSearchDatasets(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.catalog == nil {
		writeError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog is not configured")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	filters := catalog.SearchFilters{
		Query:      q.Get("q"),
		SourceType: q.Get("source"),
		AssetType:  q.Get("type"),
		Namespace:  q.Get("namespace"),
		Limit:      limit,
		Offset:     offset,
	}
	identity, _ := IdentityFrom(r.Context())
	datasets, total, err := s.catalog.Search(r.Context(), identity.OrganizationID, filters)
	if err != nil {
		s.log.Error("dataset search failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "search failed: "+err.Error())
		return
	}
	if datasets == nil {
		datasets = []catalog.DatasetSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"datasets": datasets,
		"total":    total,
		"limit":    filters.Limit,
		"offset":   filters.Offset,
	})
}

func (s *Server) handleDatasetDetails(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.catalog == nil {
		writeError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog is not configured")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	datasetID := chi.URLParam(r, "datasetID")
	details, err := s.catalog.GetDetails(r.Context(), identity.OrganizationID, datasetID)
	if err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "dataset not found: "+datasetID)
			return
		}
		s.log.Error("dataset fetch failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not fetch dataset")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataset": details})
}

func (s *Server) handleDatasetColumns(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.catalog == nil {
		writeError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog is not configured")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	cols, err := s.catalog.ListColumns(r.Context(), identity.OrganizationID, chi.URLParam(r, "datasetID"))
	if err != nil {
		s.log.Error("column listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list columns")
		return
	}
	if cols == nil {
		cols = []catalog.Column{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"columns": cols})
}

func (s *Server) handleUpdateDatasetDescription(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermEditMetadata)
	if !ok {
		return
	}
	if s.catalog == nil {
		writeError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog is not configured")
		return
	}
	var in catalog.UpdateDescriptionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := s.catalog.UpdateDescription(r.Context(), identity.OrganizationID, chi.URLParam(r, "datasetID"), in.Description); err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "dataset not found")
			return
		}
		s.log.Error("description update failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not update description")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleAssignOwner(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermEditMetadata)
	if !ok {
		return
	}
	if s.catalog == nil {
		writeError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog is not configured")
		return
	}
	var in catalog.AssignOwnerInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	identity, _ := IdentityFrom(r.Context())
	in.AssignedBy = identity.UserID
	if err := s.catalog.AssignOwner(r.Context(), identity.OrganizationID, chi.URLParam(r, "datasetID"), in); err != nil {
		switch {
		case errors.Is(err, catalog.ErrNotFound):
			writeError(w, r, http.StatusNotFound, "not_found", "dataset not found")
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "assigned"})
}

func (s *Server) handleAddTag(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermEditMetadata)
	if !ok {
		return
	}
	if s.catalog == nil {
		writeError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog is not configured")
		return
	}
	var in catalog.AddTagInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := s.catalog.AddTag(r.Context(), identity.OrganizationID, chi.URLParam(r, "datasetID"), in); err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "dataset not found")
			return
		}
		s.log.Error("tag add failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not add tag")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "added"})
}

func (s *Server) handleRemoveTag(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermEditMetadata)
	if !ok {
		return
	}
	if s.catalog == nil {
		writeError(w, r, http.StatusServiceUnavailable, "catalog_unavailable", "catalog is not configured")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if _, err := uuid.Parse(chi.URLParam(r, "tagID")); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid tag id")
		return
	}
	if err := s.catalog.RemoveTag(r.Context(), identity.OrganizationID, chi.URLParam(r, "datasetID"), chi.URLParam(r, "tagID")); err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "tag not attached")
			return
		}
		s.log.Error("tag remove failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not remove tag")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
