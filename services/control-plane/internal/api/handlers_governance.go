package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/governance"
)

func (s *Server) handleCreateGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authorize(w, r, auth.PermEditMetadata)
	if !ok {
		return
	}
	if s.gov == nil {
		writeError(w, r, http.StatusServiceUnavailable, "governance_unavailable", "governance module is not configured")
		return
	}
	var in governance.CreateTermInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	term, err := s.gov.CreateTerm(r.Context(), identity.OrganizationID, in)
	if err != nil {
		if isUserErrorMessage(err.Error()) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.log.Error("term creation failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not create term")
		return
	}
	s.gov.RecordAudit(r.Context(), identity.OrganizationID, identity.UserID, "USER",
		"glossary.term_created", "glossary_term", term.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"term": term})
}

func (s *Server) handleListGlossaryTerms(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.gov == nil {
		writeError(w, r, http.StatusServiceUnavailable, "governance_unavailable", "governance module is not configured")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	terms, err := s.gov.ListTerms(r.Context(), identity.OrganizationID)
	if err != nil {
		s.log.Error("term listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list terms")
		return
	}
	if terms == nil {
		terms = []governance.Term{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"terms": terms})
}

func (s *Server) handleAttachGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermEditMetadata)
	if !ok {
		return
	}
	if s.gov == nil {
		writeError(w, r, http.StatusServiceUnavailable, "governance_unavailable", "governance module is not configured")
		return
	}
	var in struct {
		AssetID string `json:"asset_id"`
	}
	if err := decodeJSON(r, &in); err != nil || in.AssetID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "asset_id is required")
		return
	}
	if _, err := uuid.Parse(in.AssetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid asset id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := s.gov.AttachTerm(r.Context(), identity.OrganizationID, chi.URLParam(r, "termID"), in.AssetID); err != nil {
		if errors.Is(err, governance.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "term not found")
			return
		}
		s.log.Error("term attach failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not attach term")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "attached"})
}

func (s *Server) handleAssetTerms(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.gov == nil {
		writeError(w, r, http.StatusServiceUnavailable, "governance_unavailable", "governance module is not configured")
		return
	}
	assetID := chi.URLParam(r, "assetID")
	if _, err := uuid.Parse(assetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid asset id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	terms, err := s.gov.TermsForAsset(r.Context(), identity.OrganizationID, assetID)
	if err != nil {
		s.log.Error("asset terms failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list terms")
		return
	}
	if terms == nil {
		terms = []governance.Term{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"terms": terms})
}

func (s *Server) handleSetAssetClassification(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermEditMetadata)
	if !ok {
		return
	}
	if s.gov == nil {
		writeError(w, r, http.StatusServiceUnavailable, "governance_unavailable", "governance module is not configured")
		return
	}
	var in struct {
		Classification string `json:"classification"`
		Confidence     string `json:"confidence"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	assetID := chi.URLParam(r, "assetID")
	if _, err := uuid.Parse(assetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid asset id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	classification, err := s.gov.SetAssetClassification(
		r.Context(), identity.OrganizationID, assetID, in.Classification, "MANUAL", in.Confidence, "CONFIRMED")
	if err != nil {
		if isUserErrorMessage(err.Error()) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.log.Error("classification failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not classify asset")
		return
	}
	s.gov.RecordAudit(r.Context(), identity.OrganizationID, identity.UserID, "USER",
		"classification.set", "data_asset", assetID)
	writeJSON(w, http.StatusOK, map[string]any{"classification": classification})
}

func (s *Server) handleAssetClassifications(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.gov == nil {
		writeError(w, r, http.StatusServiceUnavailable, "governance_unavailable", "governance module is not configured")
		return
	}
	assetID := chi.URLParam(r, "assetID")
	if _, err := uuid.Parse(assetID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid asset id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	list, err := s.gov.AssetClassifications(r.Context(), identity.OrganizationID, assetID)
	if err != nil {
		s.log.Error("classification listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list classifications")
		return
	}
	if list == nil {
		list = []governance.Classification{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"classifications": list})
}

func (s *Server) handleSetColumnClassification(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermEditMetadata)
	if !ok {
		return
	}
	if s.gov == nil {
		writeError(w, r, http.StatusServiceUnavailable, "governance_unavailable", "governance module is not configured")
		return
	}
	var in governance.ColumnClassificationInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	columnID := chi.URLParam(r, "columnID")
	if _, err := uuid.Parse(columnID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid column id")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	classification, err := s.gov.SetColumnClassification(r.Context(), identity.OrganizationID, columnID, in)
	if err != nil {
		if isUserErrorMessage(err.Error()) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.log.Error("column classification failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not classify column")
		return
	}
	s.gov.RecordAudit(r.Context(), identity.OrganizationID, identity.UserID, "USER",
		"classification.column_set", "dataset_column", columnID)
	writeJSON(w, http.StatusOK, map[string]any{"classification": classification})
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorize(w, r, auth.PermRead)
	if !ok {
		return
	}
	if s.gov == nil {
		writeError(w, r, http.StatusServiceUnavailable, "governance_unavailable", "governance module is not configured")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	entries, err := s.gov.ListAudit(r.Context(), identity.OrganizationID, 100)
	if err != nil {
		s.log.Error("audit listing failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "could not list audit logs")
		return
	}
	if entries == nil {
		entries = []governance.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
