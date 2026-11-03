package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/sources"
)

// authorize enforces a permission for the request identity.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, permission auth.Permission) (auth.Identity, bool) {
	id, present := IdentityFrom(r.Context())
	if !present {
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return auth.Identity{}, false
	}
	if !auth.Authorize(auth.Role(id.Role), permission) {
		writeError(w, r, http.StatusForbidden, "forbidden",
			"role "+id.Role+" does not hold "+string(permission))
		return auth.Identity{}, false
	}
	return id, true
}

func (s *Server) handleRegisterSource(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		writeError(w, r, http.StatusServiceUnavailable, "sources_unavailable",
			"source registry is not configured (APP_ENCRYPTION_KEY missing)")
		return
	}
	id, ok := s.authorize(w, r, auth.PermManageSources)
	if !ok {
		return
	}
	var in sources.RegisterInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	src, err := s.sources.Register(r.Context(), id.OrganizationID, in)
	if err != nil {
		if errors.Is(err, sources.ErrSourceNotFound) { // unknown connector surfaces as bad request
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.log.Error("source registration failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "source registration failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"source": src})
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		writeError(w, r, http.StatusServiceUnavailable, "sources_unavailable",
			"source registry is not configured (APP_ENCRYPTION_KEY missing)")
		return
	}
	id, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	list, err := s.sources.List(r.Context(), id.OrganizationID)
	if err != nil {
		s.log.Error("source listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list sources")
		return
	}
	if list == nil {
		list = []sources.Source{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": list})
}

func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		writeError(w, r, http.StatusServiceUnavailable, "sources_unavailable",
			"source registry is not configured (APP_ENCRYPTION_KEY missing)")
		return
	}
	id, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	src, err := s.sources.Get(r.Context(), id.OrganizationID, chi.URLParam(r, "sourceID"))
	if err != nil {
		if errors.Is(err, sources.ErrSourceNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "data source not found")
			return
		}
		s.log.Error("source fetch failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not fetch source")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": src})
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		writeError(w, r, http.StatusServiceUnavailable, "sources_unavailable",
			"source registry is not configured (APP_ENCRYPTION_KEY missing)")
		return
	}
	id, ok := s.authorize(w, r, auth.PermManageSources)
	if !ok {
		return
	}
	if err := s.sources.Delete(r.Context(), id.OrganizationID, chi.URLParam(r, "sourceID")); err != nil {
		if errors.Is(err, sources.ErrSourceNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "data source not found")
			return
		}
		s.log.Error("source delete failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not delete source")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleTestSource(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		writeError(w, r, http.StatusServiceUnavailable, "sources_unavailable",
			"source registry is not configured (APP_ENCRYPTION_KEY missing)")
		return
	}
	id, ok := s.authorize(w, r, auth.PermManageSources)
	if !ok {
		return
	}
	sourceID := chi.URLParam(r, "sourceID")
	if _, err := uuid.Parse(sourceID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid source id")
		return
	}
	reply, err := s.sources.TestConnection(r.Context(), id.OrganizationID, sourceID)
	if err != nil {
		if errors.Is(err, sources.ErrSourceNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "data source not found")
			return
		}
		s.log.Error("connection test failed", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "test_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": reply})
}
