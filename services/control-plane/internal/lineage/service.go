// Package lineage implements lineage ingestion (OpenLineage events, dbt
// artifacts), graph traversal and impact analysis.
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

// OpenLineage event field limits guard against pathological payloads.
const (
	maxEventBodyBytes = 1 << 20 // 1 MiB
	maxIODatasets     = 100
)

// ErrInvalidEvent rejects malformed OpenLineage payloads.
var ErrInvalidEvent = errors.New("lineage: invalid OpenLineage event")

// olEvent is the subset of the OpenLineage RuntimeEvent spec DataAtlas
// consumes. Validation follows the spec: eventType, eventTime, job and run
// are required for every run event.
type olEvent struct {
	EventType *string    `json:"eventType"`
	EventTime *time.Time `json:"eventTime"`
	Producer  *string    `json:"producer"`
	SchemaURL *string    `json:"schemaURL"`
	Run       struct {
		RunID  string         `json:"runId"`
		Facets map[string]any `json:"facets"`
	} `json:"run"`
	Job struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"job"`
	Inputs  []olDataset `json:"inputs"`
	Outputs []olDataset `json:"outputs"`
}

type olDataset struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Facets    map[string]any `json:"facets"`
}

var allowedEventTypes = map[string]bool{
	"START": true, "COMPLETE": true, "FAIL": true, "ABORT": true,
}

// Service implements lineage operations.
type Service struct {
	pool *db.Pool
}

// NewService builds the lineage service.
func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

// IngestOpenLineage validates and processes one event. Processing is
// idempotent: re-delivered events update the same job run and refresh edge
// visibility instead of creating duplicates.
func (s *Service) IngestOpenLineage(ctx context.Context, organizationID string, body []byte) (map[string]any, error) {
	if len(body) == 0 || len(body) > maxEventBodyBytes {
		return nil, fmt.Errorf("%w: payload must be between 1 and 1 MiB", ErrInvalidEvent)
	}
	var event olEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	if event.EventType == nil || !allowedEventTypes[*event.EventType] {
		return nil, fmt.Errorf("%w: eventType must be one of START, COMPLETE, FAIL, ABORT", ErrInvalidEvent)
	}
	if event.EventTime == nil {
		return nil, fmt.Errorf("%w: eventTime is required", ErrInvalidEvent)
	}
	if event.Job.Namespace == "" || event.Job.Name == "" {
		return nil, fmt.Errorf("%w: job.namespace and job.name are required", ErrInvalidEvent)
	}
	if event.Run.RunID == "" {
		return nil, fmt.Errorf("%w: run.runId is required", ErrInvalidEvent)
	}
	if len(event.Inputs) > maxIODatasets || len(event.Outputs) > maxIODatasets {
		return nil, fmt.Errorf("%w: too many input/output datasets", ErrInvalidEvent)
	}
	for _, ds := range append(append([]olDataset{}, event.Inputs...), event.Outputs...) {
		if ds.Namespace == "" || ds.Name == "" {
			return nil, fmt.Errorf("%w: input and output datasets need namespace and name", ErrInvalidEvent)
		}
	}

	orgUUID := uuid.MustParse(organizationID)
	runID := uuid.MustParse(event.Run.RunID)

	// The job's own asset entry (asset_type JOB) makes jobs addressable in
	// the graph.
	_, jobID, err := s.ensureJob(ctx, orgUUID, event)
	if err != nil {
		return nil, err
	}

	runState := map[string]string{
		"START": "RUNNING", "COMPLETE": "SUCCEEDED", "FAIL": "FAILED", "ABORT": "ABORTED",
	}
	jobUUID := uuid.MustParse(jobID)
	jobRunID, err := s.upsertJobRun(ctx, orgUUID, jobUUID, runID, runState[*event.EventType], *event.EventTime, body)
	if err != nil {
		return nil, err
	}

	edges := 0
	if *event.EventType == "START" || *event.EventType == "COMPLETE" {
		for _, in := range event.Inputs {
			inID, err := s.ensureDatasetAsset(ctx, orgUUID, event.Job.Namespace, in)
			if err != nil {
				return nil, err
			}
			for _, out := range event.Outputs {
				outID, err := s.ensureDatasetAsset(ctx, orgUUID, event.Job.Namespace, out)
				if err != nil {
					return nil, err
				}
				if err := s.upsertEdge(ctx, orgUUID, inID, outID, &jobUUID, jobRunID, body); err != nil {
					return nil, err
				}
				edges++
			}
		}
	}

	return map[string]any{
		"status":        "processed",
		"event_type":    *event.EventType,
		"job":           event.Job.Namespace + "/" + event.Job.Name,
		"run_id":        runID.String(),
		"edges_current": edges,
	}, nil
}

func (s *Service) ensureJob(ctx context.Context, orgUUID uuid.UUID, event olEvent) (string, string, error) {
	var assetID, jobID string
	err := s.pool.WithOrg(ctx, orgUUID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id::text FROM jobs
			WHERE organization_id = $1 AND namespace = $2 AND name = $3 AND deleted_at IS NULL`,
			orgUUID, event.Job.Namespace, event.Job.Name).Scan(&jobID)
		if err == nil {
			return tx.QueryRow(ctx, `SELECT asset_id::text FROM jobs WHERE id = $1`, jobID).Scan(&assetID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Create the job's asset entry, then the job.
		if err := tx.QueryRow(ctx, `
			INSERT INTO data_assets (organization_id, asset_type, namespace, name, display_name)
			VALUES ($1, 'JOB', $2, $3, $3) RETURNING id::text`,
			orgUUID, event.Job.Namespace, event.Job.Name).Scan(&assetID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO jobs (organization_id, asset_id, namespace, name, scheduler)
			VALUES ($1, $2, $3, $4, 'openlineage') RETURNING id::text`,
			orgUUID, assetID, event.Job.Namespace, event.Job.Name).Scan(&jobID)
	})
	return assetID, jobID, err
}

func (s *Service) upsertJobRun(
	ctx context.Context, orgUUID uuid.UUID, jobID, runID uuid.UUID,
	status string, eventTime time.Time, raw []byte,
) (string, error) {
	var jobRunID string
	var facets map[string]any
	_ = json.Unmarshal(raw, &facets)
	facetsJSON, _ := json.Marshal(map[string]any{"event": json.RawMessage(raw)})
	_ = facets
	err := s.pool.WithOrg(ctx, orgUUID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO job_runs (job_id, organization_id, run_id, status, started_at, source_facets)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (organization_id, job_id, run_id) DO UPDATE SET
				status = EXCLUDED.status,
				ended_at = CASE WHEN $4 IN ('SUCCEEDED', 'FAILED', 'ABORTED')
					THEN $5 ELSE job_runs.ended_at END,
				updated_at = now()
			RETURNING id::text`,
			jobID, orgUUID, runID, status, eventTime, facetsJSON,
		).Scan(&jobRunID)
	})
	return jobRunID, err
}

func (s *Service) ensureDatasetAsset(
	ctx context.Context, orgUUID uuid.UUID, jobNamespace string, ds olDataset,
) (string, error) {
	var assetID string
	err := s.pool.WithOrg(ctx, orgUUID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id::text FROM data_assets
			WHERE organization_id = $1 AND namespace = $2 AND name = $3 AND deleted_at IS NULL
			  AND asset_type <> 'JOB'
			ORDER BY CASE WHEN asset_type = 'EXTERNAL' THEN 1 ELSE 0 END
			LIMIT 1`,
			orgUUID, ds.Namespace, ds.Name).Scan(&assetID)
		if err == nil {
			_, err = tx.Exec(ctx,
				`UPDATE data_assets SET last_seen_at = now() WHERE id = $1`, assetID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Unknown datasets become EXTERNAL assets; discovery later promotes
		// them to concrete types for their source. Every dataset asset also
		// gets a datasets row so the catalog can address it.
		if err := tx.QueryRow(ctx, `
			INSERT INTO data_assets (organization_id, asset_type, namespace, name, last_seen_at)
			VALUES ($1, 'EXTERNAL', $2, $3, now()) RETURNING id::text`,
			orgUUID, ds.Namespace, ds.Name).Scan(&assetID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO datasets (asset_id, organization_id) VALUES ($1, $2)`,
			assetID, orgUUID); err != nil {
			return err
		}
		return nil
	})
	return assetID, err
}

func (s *Service) upsertEdge(
	ctx context.Context, orgUUID uuid.UUID, sourceAssetID, targetAssetID string,
	jobID *uuid.UUID, jobRunID string, raw []byte,
) error {
	return s.pool.WithOrg(ctx, orgUUID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO lineage_edges
				(organization_id, source_asset_id, target_asset_id, relationship_type,
				 job_id, job_run_id, origin, confidence, event_reference)
			VALUES ($1, $2, $3, 'TRANSFORMATION', $4, $5, 'OPENLINEAGE', 'EXPLICIT', $6)
			ON CONFLICT (organization_id, source_asset_id, target_asset_id,
				COALESCE(job_id, '00000000-0000-0000-0000-000000000000'::uuid), relationship_type)
			DO UPDATE SET last_seen_at = now(), job_run_id = EXCLUDED.job_run_id`,
			orgUUID, sourceAssetID, targetAssetID,
			nullableUUID(jobID), uuid.MustParse(jobRunID), []byte(raw))
		return err
	})
}

func nullableUUID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return *id
}
