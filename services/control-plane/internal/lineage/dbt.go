package lineage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

// ErrInvalidManifest rejects malformed dbt artifacts.
var ErrInvalidManifest = errors.New("lineage: invalid dbt manifest")

// dbtManifest captures the fields DataAtlas consumes from manifest.json
// (schema v1x; nodes keyed by unique_id).
type dbtManifest struct {
	Metadata struct {
		ProjectName string `json:"project_name"`
		AdapterType string `json:"adapter_type"`
		DbtVersion  string `json:"dbt_version"`
	} `json:"metadata"`
	Nodes map[string]struct {
		UniqueID     string `json:"unique_id"`
		ResourceType string `json:"resource_type"`
		Name         string `json:"name"`
		Schema       string `json:"schema"`
		Database     string `json:"database"`
		Description  string `json:"description"`
		DependsOn    struct {
			Nodes []string `json:"nodes"`
		} `json:"depends_on"`
		Columns map[string]struct {
			Name        string `json:"name"`
			DataType    string `json:"data_type"`
			Description string `json:"description"`
		} `json:"columns"`
		Materialized string `json:"config,omitempty"`
	} `json:"nodes"`
	Sources map[string]struct {
		UniqueID   string `json:"unique_id"`
		Name       string `json:"name"`
		Schema     string `json:"schema"`
		Database   string `json:"database"`
		SourceName string `json:"source_name"`
	} `json:"sources"`
}

// ProcessDBTManifest ingests a dbt manifest.json: models and sources become
// assets, depends_on relationships become dbt-origin lineage edges, and
// column documentation is stored as native metadata. Existing assets with
// matching namespace/name are linked instead of duplicated.
func (s *Service) ProcessDBTManifest(
	ctx context.Context, organizationID, sourceID string, manifestBytes []byte,
) (map[string]any, error) {
	if len(manifestBytes) == 0 || len(manifestBytes) > 4*maxEventBodyBytes {
		return nil, fmt.Errorf("%w: manifest must be between 1 and 4 MiB", ErrInvalidManifest)
	}
	var manifest dbtManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if len(manifest.Nodes) == 0 && len(manifest.Sources) == 0 {
		return nil, fmt.Errorf("%w: manifest contains no nodes or sources", ErrInvalidManifest)
	}

	orgUUID := uuid.MustParse(organizationID)
	var sourcePtr *uuid.UUID
	if sourceID != "" {
		parsed, err := uuid.Parse(sourceID)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid source id", ErrInvalidManifest)
		}
		sourcePtr = &parsed
	}

	assetByKey := map[string]string{} // unique_id -> asset id
	created, linked := 0, 0

	err := s.pool.WithOrg(ctx, orgUUID.String(), func(tx pgx.Tx) error {
		// Sources first (they sit at the top of the DAG).
		for uniqueID, src := range manifest.Sources {
			namespace := "dbt." + src.SourceName
			name := src.Name
			assetID, c, err := ensureDBTAsset(ctx, tx, orgUUID, sourcePtr, "DBT_SOURCE", namespace, name, src.Schema)
			if err != nil {
				return err
			}
			assetByKey[uniqueID] = assetID
			if c {
				created++
			} else {
				linked++
			}
		}
		// Models.
		for uniqueID, node := range manifest.Nodes {
			if node.ResourceType != "model" && node.ResourceType != "seed" && node.ResourceType != "snapshot" {
				continue
			}
			assetType := "DBT_MODEL"
			if node.ResourceType == "seed" {
				assetType = "DBT_SEED"
			} else if node.ResourceType == "snapshot" {
				assetType = "DBT_SNAPSHOT"
			}
			namespace := "dbt." + manifest.Metadata.ProjectName
			assetID, c, err := ensureDBTAsset(
				ctx, tx, orgUUID, sourcePtr, assetType, namespace, node.Name, node.Schema,
			)
			if err != nil {
				return err
			}
			assetByKey[uniqueID] = assetID
			if c {
				created++
			} else {
				linked++
			}
			// Store column documentation as native metadata + native description.
			if len(node.Columns) > 0 {
				cols := map[string]string{}
				for _, col := range node.Columns {
					cols[col.Name] = col.Description
				}
				metaJSON, _ := json.Marshal(map[string]any{
					"dbt_columns":     cols,
					"dbt_description": node.Description,
					"dbt_version":     manifest.Metadata.DbtVersion,
				})
				if _, err := tx.Exec(ctx, `
					UPDATE data_assets SET native_metadata = native_metadata || $2, updated_at = now()
					WHERE id = $1`, assetID, metaJSON); err != nil {
					return err
				}
			}
		}
		// Edges from depends_on.
		for uniqueID, node := range manifest.Nodes {
			targetID, ok := assetByKey[uniqueID]
			if !ok {
				continue
			}
			for _, dep := range node.DependsOn.Nodes {
				sourceIDForEdge, ok := assetByKey[dep]
				if !ok {
					continue // dependency outside this manifest (e.g. external)
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO lineage_edges
						(organization_id, source_asset_id, target_asset_id,
						 relationship_type, origin, confidence)
					VALUES ($1, $2, $3, 'TRANSFORMATION', 'DBT', 'EXPLICIT')
					ON CONFLICT (organization_id, source_asset_id, target_asset_id,
						COALESCE(job_id, '00000000-0000-0000-0000-000000000000'::uuid), relationship_type)
					DO UPDATE SET last_seen_at = now()`,
					orgUUID, sourceIDForEdge, targetID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":         "processed",
		"assets_created": created,
		"assets_linked":  linked,
		"project":        manifest.Metadata.ProjectName,
	}, nil
}

func ensureDBTAsset(
	ctx context.Context, tx pgx.Tx, orgUUID uuid.UUID, sourceID *uuid.UUID,
	assetType, namespace, name, schemaName string,
) (string, bool, error) {
	var assetID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM data_assets
		WHERE organization_id = $1 AND namespace = $2 AND name = $3
		  AND asset_type = $4 AND deleted_at IS NULL`,
		orgUUID, namespace, name, assetType).Scan(&assetID)
	if err == nil {
		_, err = tx.Exec(ctx,
			`UPDATE data_assets SET last_seen_at = now() WHERE id = $1`, assetID)
		return assetID, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO data_assets
			(organization_id, data_source_id, asset_type, namespace, name, native_metadata, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, now()) RETURNING id::text`,
		orgUUID, sourceID, assetType, namespace, name,
		[]byte(fmt.Sprintf(`{"dbt_schema":%q}`, schemaName))).Scan(&assetID)
	if err != nil {
		return "", false, err
	}
	// Every dataset asset gets a datasets row for catalog addressing.
	if _, err := tx.Exec(ctx,
		`INSERT INTO datasets (asset_id, organization_id) VALUES ($1, $2)`,
		assetID, orgUUID); err != nil {
		return "", false, err
	}
	return assetID, true, nil
}

var _ = time.Now
var _ = db.OrgScopeKey
