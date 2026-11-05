// Package governance implements the business glossary, data classification
// and audit logging.
package governance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

// ErrNotFound is returned when an entity does not exist in the org.
var ErrNotFound = errors.New("governance: not found")

// Service implements governance operations.
type Service struct {
	pool *db.Pool
}

// NewService builds the governance service.
func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

// Term is a glossary term.
type Term struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Definition  string    `json:"definition"`
	Category    *string   `json:"category"`
	Synonyms    []string  `json:"synonyms"`
	Status      string    `json:"status"`
	OwnerUserID *string   `json:"owner_user_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ensureGlossary creates the org's single glossary lazily.
func (s *Service) ensureGlossary(ctx context.Context, tx pgx.Tx, orgUUID uuid.UUID) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM business_glossaries WHERE organization_id = $1`, orgUUID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return id, tx.QueryRow(ctx, `
		INSERT INTO business_glossaries (organization_id, name)
		VALUES ($1, 'Business Glossary') RETURNING id::text`, orgUUID).Scan(&id)
}

// CreateTermInput is the payload for term creation.
type CreateTermInput struct {
	Name       string   `json:"name"`
	Definition string   `json:"definition"`
	Category   *string  `json:"category"`
	Synonyms   []string `json:"synonyms"`
	Status     string   `json:"status"`
}

// CreateTerm adds a glossary term.
func (s *Service) CreateTerm(ctx context.Context, organizationID string, in CreateTermInput) (Term, error) {
	if in.Name == "" || in.Definition == "" {
		return Term{}, fmt.Errorf("name and definition are required")
	}
	if in.Status == "" {
		in.Status = "DRAFT"
	}
	if in.Status != "DRAFT" && in.Status != "PUBLISHED" && in.Status != "DEPRECATED" {
		return Term{}, fmt.Errorf("unsupported status %q", in.Status)
	}
	orgUUID := uuid.MustParse(organizationID)
	var out Term
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		glossaryID, err := s.ensureGlossary(ctx, tx, orgUUID)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO glossary_terms
				(glossary_id, organization_id, name, definition, category, synonyms, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id::text, name, definition, category, synonyms, status, owner_user_id::text, created_at, updated_at`,
			glossaryID, orgUUID, in.Name, in.Definition, in.Category, in.Synonyms, in.Status,
		).Scan(&out.ID, &out.Name, &out.Definition, &out.Category, &out.Synonyms,
			&out.Status, &out.OwnerUserID, &out.CreatedAt, &out.UpdatedAt)
	})
	return out, err
}

// ListTerms returns all glossary terms.
func (s *Service) ListTerms(ctx context.Context, organizationID string) ([]Term, error) {
	var out []Term
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, definition, category, synonyms, status,
			       owner_user_id::text, created_at, updated_at
			FROM glossary_terms WHERE deleted_at IS NULL ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Term
			if err := rows.Scan(&t.ID, &t.Name, &t.Definition, &t.Category, &t.Synonyms,
				&t.Status, &t.OwnerUserID, &t.CreatedAt, &t.UpdatedAt); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// AttachTerm links a glossary term to an asset.
func (s *Service) AttachTerm(ctx context.Context, organizationID, termID, assetID string) error {
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM glossary_terms WHERE id = $1 AND deleted_at IS NULL)`,
			termID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO glossary_term_assets (term_id, asset_id) VALUES ($1, $2)
			ON CONFLICT (term_id, asset_id) DO NOTHING`, termID, assetID)
		return err
	})
}

// TermsForAsset returns the terms attached to an asset.
func (s *Service) TermsForAsset(ctx context.Context, organizationID, assetID string) ([]Term, error) {
	var out []Term
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id::text, t.name, t.definition, t.category, t.synonyms, t.status,
			       t.owner_user_id::text, t.created_at, t.updated_at
			FROM glossary_term_assets gta
			JOIN glossary_terms t ON t.id = gta.term_id AND t.deleted_at IS NULL
			WHERE gta.asset_id = $1 ORDER BY t.name`, assetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Term
			if err := rows.Scan(&t.ID, &t.Name, &t.Definition, &t.Category, &t.Synonyms,
				&t.Status, &t.OwnerUserID, &t.CreatedAt, &t.UpdatedAt); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// ------------------------------------------------------------------
// Classification
// ------------------------------------------------------------------

// Classification is one classification label with provenance.
type Classification struct {
	ID              string  `json:"id"`
	Level           string  `json:"classification"`
	Source          string  `json:"source"`
	Confidence      string  `json:"confidence"`
	ReviewStatus    string  `json:"review_status"`
	DetectionMethod *string `json:"detection_method"`
}

var allowedLevels = map[string]bool{
	"PUBLIC": true, "INTERNAL": true, "CONFIDENTIAL": true,
	"RESTRICTED": true, "SENSITIVE": true,
}

// SetAssetClassification assigns a classification to an asset.
func (s *Service) SetAssetClassification(
	ctx context.Context, organizationID, assetID, level, source, confidence, reviewStatus string,
) (Classification, error) {
	if !allowedLevels[level] {
		return Classification{}, fmt.Errorf("unsupported classification %q", level)
	}
	if source == "" {
		source = "MANUAL"
	}
	if confidence == "" {
		confidence = "HIGH"
	}
	if reviewStatus == "" {
		reviewStatus = "CONFIRMED"
	}
	orgUUID := uuid.MustParse(organizationID)
	var out Classification
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO asset_classifications
				(asset_id, organization_id, classification, source, confidence, review_status)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (asset_id, classification) DO UPDATE SET
				source = EXCLUDED.source, confidence = EXCLUDED.confidence,
				review_status = EXCLUDED.review_status, updated_at = now()
			RETURNING id::text, classification, source, confidence, review_status, NULL`,
			assetID, orgUUID, level, source, confidence, reviewStatus,
		).Scan(&out.ID, &out.Level, &out.Source, &out.Confidence, &out.ReviewStatus, &out.DetectionMethod)
	})
	return out, err
}

// AssetClassifications lists the classifications of an asset.
func (s *Service) AssetClassifications(ctx context.Context, organizationID, assetID string) ([]Classification, error) {
	var out []Classification
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, classification, source, confidence, review_status, NULL
			FROM asset_classifications WHERE asset_id = $1`, assetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Classification
			if err := rows.Scan(&c.ID, &c.Level, &c.Source, &c.Confidence,
				&c.ReviewStatus, &c.DetectionMethod); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// ColumnClassificationInput classifies a column.
type ColumnClassificationInput struct {
	Level           string  `json:"classification"`
	Confidence      string  `json:"confidence"`
	DetectionMethod *string `json:"detection_method"`
}

// SetColumnClassification assigns a classification to a column.
func (s *Service) SetColumnClassification(
	ctx context.Context, organizationID, columnID string, in ColumnClassificationInput,
) (Classification, error) {
	if !allowedLevels[in.Level] {
		return Classification{}, fmt.Errorf("unsupported classification %q", in.Level)
	}
	if in.Confidence == "" {
		in.Confidence = "MEDIUM"
	}
	source := "MANUAL"
	if in.DetectionMethod != nil {
		source = "RULE"
	}
	orgUUID := uuid.MustParse(organizationID)
	var out Classification
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO column_classifications
				(column_id, organization_id, classification, source, detection_method, confidence)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (column_id, classification) DO UPDATE SET
				source = EXCLUDED.source, detection_method = EXCLUDED.detection_method,
				confidence = EXCLUDED.confidence, updated_at = now()
			RETURNING id::text, classification, source, confidence, 'UNREVIEWED', detection_method`,
			columnID, orgUUID, in.Level, source, in.DetectionMethod, in.Confidence,
		).Scan(&out.ID, &out.Level, &out.Source, &out.Confidence, &out.ReviewStatus, &out.DetectionMethod)
	})
	return out, err
}

// ColumnClassifications lists the classifications of a column.
func (s *Service) ColumnClassifications(ctx context.Context, organizationID, columnID string) ([]Classification, error) {
	var out []Classification
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, classification, source, confidence, review_status, detection_method
			FROM column_classifications WHERE column_id = $1`, columnID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Classification
			if err := rows.Scan(&c.ID, &c.Level, &c.Source, &c.Confidence,
				&c.ReviewStatus, &c.DetectionMethod); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// AuditEntry is one audit log record.
type AuditEntry struct {
	ID          int64     `json:"id"`
	ActorUserID *string   `json:"actor_user_id"`
	ActorKind   string    `json:"actor_kind"`
	Action      string    `json:"action"`
	EntityType  string    `json:"entity_type"`
	EntityID    *string   `json:"entity_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// RecordAudit writes an audit entry. Failures never break the caller.
func (s *Service) RecordAudit(
	ctx context.Context, organizationID, actorUserID, actorKind,
	action, entityType, entityID string,
) {
	_ = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var actor *uuid.UUID
		if actorUserID != "" {
			parsed, err := uuid.Parse(actorUserID)
			if err == nil {
				actor = &parsed
			}
		}
		var entity *uuid.UUID
		if entityID != "" {
			parsed, err := uuid.Parse(entityID)
			if err == nil {
				entity = &parsed
			}
		}
		if actorKind == "" {
			actorKind = "USER"
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_logs
				(organization_id, actor_user_id, actor_kind, action, entity_type, entity_id)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			uuid.MustParse(organizationID), actor, actorKind, action, entityType, entity)
		return err
	})
}

// ListAudit returns recent audit entries.
func (s *Service) ListAudit(ctx context.Context, organizationID string, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []AuditEntry
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, actor_user_id::text, actor_kind, action, entity_type, entity_id::text, created_at
			FROM audit_logs WHERE organization_id = $1
			ORDER BY created_at DESC LIMIT $2`, uuid.MustParse(organizationID), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e AuditEntry
			if err := rows.Scan(&e.ID, &e.ActorUserID, &e.ActorKind, &e.Action,
				&e.EntityType, &e.EntityID, &e.CreatedAt); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}
