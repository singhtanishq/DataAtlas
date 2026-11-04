// Package quality manages quality check configuration and run triggering.
package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/ingestion"
)

// ErrCheckNotFound is returned when a check does not exist in the org.
var ErrCheckNotFound = errors.New("quality: check not found")

// Service manages quality checks and runs.
type Service struct {
	pool *db.Pool
	js   nats.JetStreamContext
}

// NewService builds the quality service.
func NewService(pool *db.Pool, js nats.JetStreamContext, natsURL string) *Service {
	return &Service{pool: pool, js: js}
}

// Check is the API view of a quality check.
type Check struct {
	ID           string          `json:"id"`
	DatasetID    string          `json:"dataset_id"`
	Name         string          `json:"name"`
	Description  *string         `json:"description"`
	CheckType    string          `json:"check_type"`
	TargetColumn *string         `json:"target_column"`
	Parameters   json.RawMessage `json:"parameters"`
	Severity     string          `json:"severity"`
	Enabled      bool            `json:"enabled"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// CreateCheckInput is the payload for check creation.
type CreateCheckInput struct {
	DatasetID    string          `json:"dataset_id"`
	Name         string          `json:"name"`
	Description  *string         `json:"description"`
	CheckType    string          `json:"check_type"`
	TargetColumn *string         `json:"target_column"`
	Parameters   json.RawMessage `json:"parameters"`
	Severity     string          `json:"severity"`
}

var allowedCheckTypes = map[string]bool{
	"NOT_NULL": true, "UNIQUE": true, "ROW_COUNT": true, "ACCEPTED_VALUES": true,
	"NUMERIC_RANGE": true, "REGEX": true, "FRESHNESS": true,
	"REFERENTIAL_INTEGRITY": true, "SCHEMA": true, "CUSTOM_SQL": true,
}

var allowedSeverities = map[string]bool{
	"INFO": true, "WARNING": true, "ERROR": true, "CRITICAL": true,
}

// CreateCheck registers a new quality check.
func (s *Service) CreateCheck(ctx context.Context, organizationID string, in CreateCheckInput) (Check, error) {
	if in.Name == "" || in.DatasetID == "" {
		return Check{}, fmt.Errorf("name and dataset_id are required")
	}
	if !allowedCheckTypes[in.CheckType] {
		return Check{}, fmt.Errorf("unsupported check_type %q", in.CheckType)
	}
	if in.Severity == "" {
		in.Severity = "ERROR"
	}
	if !allowedSeverities[in.Severity] {
		return Check{}, fmt.Errorf("unsupported severity %q", in.Severity)
	}
	params := in.Parameters
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}

	checkID := uuid.Must(uuid.NewRandom())
	var out Check
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO quality_checks
				(id, organization_id, dataset_id, name, description, check_type,
				 target_column, parameters, severity)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id::text, dataset_id::text, name, description, check_type,
			          target_column, parameters, severity, enabled, created_at, updated_at`,
			checkID, uuid.MustParse(organizationID), in.DatasetID, in.Name,
			in.Description, in.CheckType, in.TargetColumn, params, in.Severity,
		).Scan(&out.ID, &out.DatasetID, &out.Name, &out.Description, &out.CheckType,
			&out.TargetColumn, &out.Parameters, &out.Severity, &out.Enabled,
			&out.CreatedAt, &out.UpdatedAt)
		if err != nil && isForeignKeyViolation(err) {
			return fmt.Errorf("dataset %q not found", in.DatasetID)
		}
		return err
	})
	return out, err
}

// ListChecks returns the checks for one dataset.
func (s *Service) ListChecks(ctx context.Context, organizationID, datasetID string) ([]Check, error) {
	var out []Check
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, dataset_id::text, name, description, check_type,
			       target_column, parameters, severity, enabled, created_at, updated_at
			FROM quality_checks WHERE dataset_id = $1 AND deleted_at IS NULL
			ORDER BY created_at`, datasetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Check
			if err := rows.Scan(&c.ID, &c.DatasetID, &c.Name, &c.Description,
				&c.CheckType, &c.TargetColumn, &c.Parameters, &c.Severity,
				&c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateCheckInput modifies an existing check.
type UpdateCheckInput struct {
	Name       *string         `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
	Severity   *string         `json:"severity"`
	Enabled    *bool           `json:"enabled"`
}

// UpdateCheck applies partial modifications.
func (s *Service) UpdateCheck(ctx context.Context, organizationID, checkID string, in UpdateCheckInput) (Check, error) {
	if in.Severity != nil && !allowedSeverities[*in.Severity] {
		return Check{}, fmt.Errorf("unsupported severity %q", *in.Severity)
	}
	var out Check
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE quality_checks SET
				name = COALESCE($3, name),
				parameters = COALESCE($4, parameters),
				severity = COALESCE($5, severity),
				enabled = COALESCE($6, enabled),
				updated_at = now()
			WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL
			RETURNING id::text, dataset_id::text, name, description, check_type,
			          target_column, parameters, severity, enabled, created_at, updated_at`,
			checkID, uuid.MustParse(organizationID), in.Name, in.Parameters,
			in.Severity, in.Enabled,
		).Scan(&out.ID, &out.DatasetID, &out.Name, &out.Description, &out.CheckType,
			&out.TargetColumn, &out.Parameters, &out.Severity, &out.Enabled,
			&out.CreatedAt, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCheckNotFound
		}
		return err
	})
	return out, err
}

// DeleteCheck soft-deletes a check.
func (s *Service) DeleteCheck(ctx context.Context, organizationID, checkID string) error {
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE quality_checks SET deleted_at = now(), updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL`, checkID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCheckNotFound
		}
		return nil
	})
}

// RunResult is one stored check result of a quality run.
type RunResult struct {
	CheckID       string          `json:"check_id"`
	CheckName     string          `json:"check_name"`
	CheckType     string          `json:"check_type"`
	Status        string          `json:"status"`
	RowsEvaluated *int64          `json:"rows_evaluated"`
	RowsFailed    *int64          `json:"rows_failed"`
	Actual        json.RawMessage `json:"actual"`
	ErrorMessage  *string         `json:"error_message"`
	DurationMS    *int64          `json:"duration_ms"`
}

// QualityRunView is an ingestion run enriched with quality results.
type QualityRunView struct {
	ingestion.Run
	Results []RunResult `json:"results"`
}

// TriggerQualityRun creates a PENDING run and publishes the job.
func (s *Service) TriggerQualityRun(ctx context.Context, organizationID, trigger string) (ingestion.Run, error) {
	if s.js == nil {
		return ingestion.Run{}, fmt.Errorf("job queue not configured")
	}
	// Only trigger when at least one enabled check exists.
	var count int
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM quality_checks WHERE enabled = true AND deleted_at IS NULL`,
		).Scan(&count)
	})
	if err != nil {
		return ingestion.Run{}, err
	}
	if count == 0 {
		return ingestion.Run{}, fmt.Errorf("no enabled quality checks")
	}

	// The job references any pipeline of the org (runs are org-scoped).
	var pipelineID string
	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id::text FROM ingestion_pipelines
			WHERE organization_id = $1 AND deleted_at IS NULL LIMIT 1`,
			uuid.MustParse(organizationID)).Scan(&pipelineID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Run{}, fmt.Errorf("no ingestion pipeline registered")
	}
	if err != nil {
		return ingestion.Run{}, err
	}

	runID := uuid.Must(uuid.NewRandom())
	payload, err := json.Marshal(map[string]string{
		"run_id": runID.String(), "organization_id": organizationID,
	})
	if err != nil {
		return ingestion.Run{}, err
	}

	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO ingestion_runs (id, pipeline_id, organization_id, trigger, status)
			VALUES ($1, $2, $3, $4, 'PENDING')`,
			runID, uuid.MustParse(pipelineID), uuid.MustParse(organizationID), trigger)
		return err
	})
	if err != nil {
		return ingestion.Run{}, err
	}
	if _, err := s.js.Publish(ingestion.QualitySubject, payload, nats.MsgId(runID.String())); err != nil {
		return ingestion.Run{}, fmt.Errorf("publish quality job: %w", err)
	}
	return s.getRun(ctx, organizationID, runID.String())
}

// GetRun returns a run with its stored results. The worker records the
// analytical quality run id in the ingestion run's progress payload.
func (s *Service) GetRun(ctx context.Context, organizationID, runID string) (QualityRunView, error) {
	var out QualityRunView
	run, err := s.getRun(ctx, organizationID, runID)
	if err != nil {
		return out, err
	}
	out.Run = run

	var progress struct {
		QualityRunID string `json:"quality_run_id"`
	}
	_ = json.Unmarshal(run.Progress, &progress)
	if progress.QualityRunID == "" {
		return out, nil // worker has not stored results yet
	}

	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT qr.check_id::text, COALESCE(qc.name, ''), COALESCE(qc.check_type, ''),
			       qr.status, qr.rows_evaluated, qr.rows_failed, qr.actual_value,
			       qr.error_message, qr.duration_ms
			FROM quality_results qr
			LEFT JOIN quality_checks qc ON qc.id = qr.check_id
			WHERE qr.quality_run_id = $1
			ORDER BY qr.executed_at DESC`, progress.QualityRunID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RunResult
			if err := rows.Scan(&r.CheckID, &r.CheckName, &r.CheckType, &r.Status,
				&r.RowsEvaluated, &r.RowsFailed, &r.Actual, &r.ErrorMessage,
				&r.DurationMS); err != nil {
				return err
			}
			out.Results = append(out.Results, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) getRun(ctx context.Context, organizationID, runID string) (ingestion.Run, error) {
	var run ingestion.Run
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id::text, pipeline_id::text, trigger, status, started_at,
			       finished_at, duration_ms, error_message, progress, created_at
			FROM ingestion_runs WHERE id = $1`, runID,
		).Scan(&run.ID, &run.PipelineID, &run.Trigger, &run.Status, &run.StartedAt,
			&run.FinishedAt, &run.DurationMS, &run.ErrorMessage, &run.Progress,
			&run.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ingestion.ErrRunNotFound
		}
		return err
	})
	return run, err
}

func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23503")
}
