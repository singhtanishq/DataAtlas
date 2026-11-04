// Package catalog implements search, retrieval and business-metadata
// editing over the ingested asset registry.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

// ErrNotFound is returned when an asset does not exist in the organization.
var ErrNotFound = errors.New("catalog: asset not found")

// Service provides catalog operations.
type Service struct {
	pool *db.Pool
}

// NewService builds the catalog service.
func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

// DatasetSummary is a list-row view of a dataset.
type DatasetSummary struct {
	ID            string          `json:"id"`
	AssetID       string          `json:"asset_id"`
	Namespace     string          `json:"namespace"`
	Name          string          `json:"name"`
	FullName      string          `json:"full_name"`
	AssetType     string          `json:"asset_type"`
	DataSourceID  *string         `json:"data_source_id"`
	SourceName    *string         `json:"source_name"`
	Description   *string         `json:"description"`
	RowCount      *int64          `json:"row_count"`
	ColumnCount   *int32          `json:"column_count"`
	LastSeenAt    *time.Time      `json:"last_seen_at"`
	NativeDetails json.RawMessage `json:"-"`
}

// SearchFilters constrain dataset search.
type SearchFilters struct {
	Query      string
	SourceType string
	AssetType  string
	Namespace  string
	Limit      int
	Offset     int
}

// Search returns datasets matching the filters with server-side pagination.
func (s *Service) Search(ctx context.Context, organizationID string, f SearchFilters) ([]DatasetSummary, int, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 25
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := []string{"a.organization_id = $1", "a.deleted_at IS NULL", "d.id IS NOT NULL"}
	args := []any{uuid.MustParse(organizationID)}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }

	if f.Query != "" {
		p := arg("%" + f.Query + "%")
		where = append(where,
			fmt.Sprintf(`(a.name ILIKE %s OR a.namespace ILIKE %s OR COALESCE(a.description,'') ILIKE %s)`, p, p, p))
	}
	if f.SourceType != "" {
		where = append(where, "ds.connector_key = "+arg(f.SourceType))
	}
	if f.AssetType != "" {
		where = append(where, "a.asset_type = "+arg(f.AssetType))
	}
	if f.Namespace != "" {
		where = append(where, "a.namespace = "+arg(f.Namespace))
	}
	whereSQL := strings.Join(where, " AND ")

	var total int
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*)
			FROM data_assets a
			JOIN datasets d ON d.asset_id = a.id AND d.deleted_at IS NULL
			LEFT JOIN data_sources ds ON ds.id = a.data_source_id
			WHERE `+whereSQL, args...).Scan(&total)
	})
	if err != nil {
		return nil, 0, err
	}

	var out []DatasetSummary
	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.id::text, a.id::text, a.namespace, a.name, a.full_name,
			       a.asset_type, a.data_source_id::text, ds.name, a.description,
			       d.row_count, d.column_count, a.last_seen_at
			FROM data_assets a
			JOIN datasets d ON d.asset_id = a.id AND d.deleted_at IS NULL
			LEFT JOIN data_sources ds ON ds.id = a.data_source_id
			WHERE `+whereSQL+`
			ORDER BY a.namespace, a.name
			LIMIT `+fmt.Sprintf("%d OFFSET %d", f.Limit, f.Offset), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ds DatasetSummary
			if err := rows.Scan(&ds.ID, &ds.AssetID, &ds.Namespace, &ds.Name, &ds.FullName,
				&ds.AssetType, &ds.DataSourceID, &ds.SourceName, &ds.Description,
				&ds.RowCount, &ds.ColumnCount, &ds.LastSeenAt); err != nil {
				return err
			}
			out = append(out, ds)
		}
		return rows.Err()
	})
	return out, total, err
}

// DatasetDetails is the full dataset detail payload.
type DatasetDetails struct {
	DatasetSummary
	DatabaseName *string         `json:"database_name"`
	SchemaName   *string         `json:"schema_name"`
	Location     *string         `json:"location"`
	RowMethod    *string         `json:"row_count_method"`
	CreatedAt    *time.Time      `json:"created_at"`
	UpdatedAt    *time.Time      `json:"updated_at"`
	Version      *int            `json:"current_schema_version"`
	IsBreaking   *bool           `json:"current_version_is_breaking"`
	NativeMeta   json.RawMessage `json:"native_metadata"`
	Owners       []Owner         `json:"owners"`
	Tags         []Tag           `json:"tags"`
}

// Owner is an active ownership assignment.
type Owner struct {
	AssignmentID  string  `json:"assignment_id"`
	OwnershipRole string  `json:"ownership_role"`
	OwnerType     string  `json:"owner_type"`
	UserID        *string `json:"user_id"`
	UserName      *string `json:"user_name"`
	TeamID        *string `json:"team_id"`
	TeamName      *string `json:"team_name"`
	AssignedAt    string  `json:"assigned_at"`
}

// Tag is a tag attached to an asset.
type Tag struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Color *string `json:"color"`
}

// Column is a dataset column with business annotations.
type Column struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Ordinal      int     `json:"ordinal"`
	DataType     string  `json:"data_type"`
	Nullable     bool    `json:"nullable"`
	DefaultValue *string `json:"default_value"`
	IsPrimaryKey bool    `json:"is_primary_key"`
	IsForeignKey bool    `json:"is_foreign_key"`
	FKReference  *string `json:"fk_reference"`
	Description  *string `json:"description"`
	NativeDescr  *string `json:"native_description"`
}

// GetDetails returns the full detail payload for one dataset.
func (s *Service) GetDetails(ctx context.Context, organizationID, datasetID string) (DatasetDetails, error) {
	var out DatasetDetails
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT d.id::text, a.id::text, a.namespace, a.name, a.full_name,
			       a.asset_type, a.data_source_id::text, ds.name, a.description,
			       d.row_count, d.column_count, a.last_seen_at,
			       d.database_name, d.schema_name, d.location, d.row_count_method,
			       d.created_at, d.updated_at, dv.version_number, dv.change_summary->>'is_breaking',
			       a.native_metadata
			FROM datasets d
			JOIN data_assets a ON a.id = d.asset_id
			LEFT JOIN data_sources ds ON ds.id = a.data_source_id
			LEFT JOIN dataset_versions dv ON dv.id = d.current_version_id
			WHERE d.id = $1 AND d.deleted_at IS NULL AND a.deleted_at IS NULL`, datasetID)
		var breaking *string
		if err := row.Scan(&out.ID, &out.AssetID, &out.Namespace, &out.Name, &out.FullName,
			&out.AssetType, &out.DataSourceID, &out.SourceName, &out.Description,
			&out.RowCount, &out.ColumnCount, &out.LastSeenAt,
			&out.DatabaseName, &out.SchemaName, &out.Location, &out.RowMethod,
			&out.CreatedAt, &out.UpdatedAt, &out.Version, &breaking,
			&out.NativeMeta); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if breaking != nil {
			isBreaking := *breaking == "true"
			out.IsBreaking = &isBreaking
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	out.Owners, err = s.listOwners(ctx, organizationID, out.AssetID)
	if err != nil {
		return out, err
	}
	out.Tags, err = s.listTags(ctx, organizationID, out.AssetID)
	return out, err
}

// ListColumns returns the active columns of a dataset.
func (s *Service) ListColumns(ctx context.Context, organizationID, datasetID string) ([]Column, error) {
	var cols []Column
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.id::text, c.name, c.ordinal, c.data_type, c.nullable,
			       c.default_value, c.is_primary_key, c.is_foreign_key, c.fk_reference,
			       c.description, c.native_description
			FROM dataset_columns c
			JOIN datasets d ON d.id = c.dataset_id
			WHERE d.id = $1 AND c.status = 'ACTIVE' AND d.deleted_at IS NULL
			ORDER BY c.ordinal`, datasetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Column
			if err := rows.Scan(&c.ID, &c.Name, &c.Ordinal, &c.DataType, &c.Nullable,
				&c.DefaultValue, &c.IsPrimaryKey, &c.IsForeignKey, &c.FKReference,
				&c.Description, &c.NativeDescr); err != nil {
				return err
			}
			cols = append(cols, c)
		}
		return rows.Err()
	})
	return cols, err
}

// UpdateDescriptionInput edits the merged business description.
type UpdateDescriptionInput struct {
	Description string `json:"description"`
}

// UpdateDescription sets the user-authored description on the asset.
func (s *Service) UpdateDescription(ctx context.Context, organizationID, datasetID, description string) error {
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE data_assets a SET description = $2, updated_at = now()
			FROM datasets d
			WHERE d.asset_id = a.id AND d.id = $1 AND a.deleted_at IS NULL`,
			datasetID, description)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AssignOwnerInput assigns an owner to an asset.
type AssignOwnerInput struct {
	OwnershipRole string  `json:"ownership_role"` // TECHNICAL_OWNER | BUSINESS_OWNER | DATA_STEWARD
	OwnerType     string  `json:"owner_type"`     // USER | TEAM
	UserID        *string `json:"user_id"`
	TeamID        *string `json:"team_id"`
	AssignedBy    string  `json:"-"`
}

// AssignOwner records a new current ownership assignment (history preserved:
// the previous assignment is closed by setting unassigned_at).
func (s *Service) AssignOwner(ctx context.Context, organizationID, datasetID string, in AssignOwnerInput) error {
	if in.OwnershipRole == "" || in.OwnerType == "" {
		return fmt.Errorf("ownership_role and owner_type are required")
	}
	if in.OwnerType == "USER" && in.UserID == nil {
		return fmt.Errorf("user_id is required for USER owner type")
	}
	if in.OwnerType == "TEAM" && in.TeamID == nil {
		return fmt.Errorf("team_id is required for TEAM owner type")
	}
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var assetID string
		err := tx.QueryRow(ctx, `
			SELECT a.id::text FROM data_assets a
			JOIN datasets d ON d.asset_id = a.id
			WHERE d.id = $1 AND a.deleted_at IS NULL`, datasetID).Scan(&assetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE ownership_assignments SET unassigned_at = now()
			WHERE asset_id = $1 AND unassigned_at IS NULL AND ownership_role = $2`,
			assetID, in.OwnershipRole); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO ownership_assignments
				(organization_id, asset_id, owner_type, owner_user_id, owner_team_id,
				 ownership_role, assigned_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			uuid.MustParse(organizationID), assetID, in.OwnerType,
			in.UserID, in.TeamID, in.OwnershipRole, uuid.MustParse(in.AssignedBy))
		return err
	})
}

// AddTagInput attaches a tag (creating it when new).
type AddTagInput struct {
	Name  string  `json:"name"`
	Color *string `json:"color"`
}

// AddTag attaches a tag to the dataset's asset.
func (s *Service) AddTag(ctx context.Context, organizationID, datasetID string, in AddTagInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("tag name is required")
	}
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var assetID string
		err := tx.QueryRow(ctx, `
			SELECT a.id::text FROM data_assets a
			JOIN datasets d ON d.asset_id = a.id
			WHERE d.id = $1 AND a.deleted_at IS NULL`, datasetID).Scan(&assetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var tagID string
		err = tx.QueryRow(ctx, `
			INSERT INTO tags (organization_id, name, color)
			VALUES ($1, $2, $3)
			ON CONFLICT (organization_id, name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id::text`,
			uuid.MustParse(organizationID), in.Name, in.Color).Scan(&tagID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO asset_tags (tag_id, asset_id) VALUES ($1, $2)
			ON CONFLICT (tag_id, asset_id) DO NOTHING`, tagID, assetID)
		return err
	})
}

// RemoveTag detaches a tag from the dataset's asset.
func (s *Service) RemoveTag(ctx context.Context, organizationID, datasetID, tagID string) error {
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM asset_tags
			WHERE asset_id = (SELECT a.id FROM data_assets a
				JOIN datasets d ON d.asset_id = a.id WHERE d.id = $1)
			  AND tag_id = $2`, datasetID, tagID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Service) listOwners(ctx context.Context, organizationID, assetID string) ([]Owner, error) {
	var owners []Owner
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT o.id::text, o.ownership_role, o.owner_type,
			       o.owner_user_id::text, u.display_name,
			       o.owner_team_id::text, t.name, o.assigned_at::text
			FROM ownership_assignments o
			LEFT JOIN users u ON u.id = o.owner_user_id
			LEFT JOIN teams t ON t.id = o.owner_team_id
			WHERE o.asset_id = $1 AND o.unassigned_at IS NULL
			ORDER BY o.assigned_at`, assetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o Owner
			if err := rows.Scan(&o.AssignmentID, &o.OwnershipRole, &o.OwnerType,
				&o.UserID, &o.UserName, &o.TeamID, &o.TeamName, &o.AssignedAt); err != nil {
				return err
			}
			owners = append(owners, o)
		}
		return rows.Err()
	})
	return owners, err
}

func (s *Service) listTags(ctx context.Context, organizationID, assetID string) ([]Tag, error) {
	var tags []Tag
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT g.id::text, g.name, g.color
			FROM asset_tags at
			JOIN tags g ON g.id = at.tag_id
			WHERE at.asset_id = $1
			ORDER BY g.name`, assetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Tag
			if err := rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
				return err
			}
			tags = append(tags, t)
		}
		return rows.Err()
	})
	return tags, err
}
