// Package observability exposes the pipeline registry: jobs, run history,
// aggregated health and dataset freshness.
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

// ErrNotFound is returned when an entity does not exist in the org.
var ErrNotFound = errors.New("observability: not found")

// Service reads pipeline observability data.
type Service struct {
	pool *db.Pool
}

// NewService builds the observability service.
func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

// JobSummary is a job with aggregated run statistics.
type JobSummary struct {
	ID            string     `json:"id"`
	Namespace     string     `json:"namespace"`
	Name          string     `json:"name"`
	Scheduler     *string    `json:"scheduler"`
	Status        string     `json:"status"`
	TotalRuns     int        `json:"total_runs"`
	Successful    int        `json:"successful_runs"`
	Failed        int        `json:"failed_runs"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastStatus    *string    `json:"last_status"`
	AvgDurationMS *float64   `json:"avg_duration_ms"`
}

// ListJobs returns jobs with run statistics.
func (s *Service) ListJobs(ctx context.Context, organizationID string) ([]JobSummary, error) {
	var out []JobSummary
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT j.id::text, j.namespace, j.name, j.scheduler, j.status,
			       count(jr.id) AS total,
			       count(jr.id) FILTER (WHERE jr.status = 'SUCCEEDED') AS ok,
			       count(jr.id) FILTER (WHERE jr.status = 'FAILED') AS failed,
			       max(jr.started_at) AS last_run,
			       (SELECT status FROM job_runs x WHERE x.job_id = j.id
			        ORDER BY started_at DESC NULLS LAST LIMIT 1) AS last_status,
			       avg(jr.duration_ms) AS avg_duration
			FROM jobs j
			LEFT JOIN job_runs jr ON jr.job_id = j.id
			WHERE j.organization_id = $1 AND j.deleted_at IS NULL
			GROUP BY j.id
			ORDER BY max(jr.started_at) DESC NULLS LAST, j.name`, uuid.MustParse(organizationID))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j JobSummary
			if err := rows.Scan(&j.ID, &j.Namespace, &j.Name, &j.Scheduler, &j.Status,
				&j.TotalRuns, &j.Successful, &j.Failed, &j.LastRunAt, &j.LastStatus,
				&j.AvgDurationMS); err != nil {
				return err
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

// JobRun is one job execution record.
type JobRun struct {
	ID         string          `json:"id"`
	JobID      string          `json:"job_id"`
	JobName    string          `json:"job_name"`
	RunID      string          `json:"run_id"`
	Status     string          `json:"status"`
	StartedAt  *time.Time      `json:"started_at"`
	EndedAt    *time.Time      `json:"ended_at"`
	DurationMS *int64          `json:"duration_ms"`
	Error      *string         `json:"error_message"`
	Facets     json.RawMessage `json:"source_facets"`
}

// ListJobRuns returns the runs of one job, newest first.
func (s *Service) ListJobRuns(ctx context.Context, organizationID, jobID string) ([]JobRun, error) {
	var out []JobRun
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1 AND deleted_at IS NULL)`,
			jobID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `
			SELECT jr.id::text, jr.job_id::text, j.name, jr.run_id, jr.status,
			       jr.started_at, jr.ended_at, jr.duration_ms, jr.error_message,
			       jr.source_facets
			FROM job_runs jr JOIN jobs j ON j.id = jr.job_id
			WHERE jr.job_id = $1
			ORDER BY jr.started_at DESC NULLS LAST LIMIT 100`, jobID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r JobRun
			if err := rows.Scan(&r.ID, &r.JobID, &r.JobName, &r.RunID, &r.Status,
				&r.StartedAt, &r.EndedAt, &r.DurationMS, &r.Error, &r.Facets); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// PipelineHealth summarizes run outcomes over a time window.
type PipelineHealth struct {
	WindowDays     int      `json:"window_days"`
	TotalRuns      int      `json:"total_runs"`
	SuccessfulRuns int      `json:"successful_runs"`
	FailedRuns     int      `json:"failed_runs"`
	AbortedRuns    int      `json:"aborted_runs"`
	SuccessRate    float64  `json:"success_rate"`
	AvgDurationMS  *float64 `json:"avg_duration_ms"`
}

// PipelineHealth aggregates job run outcomes.
func (s *Service) PipelineHealth(ctx context.Context, organizationID string, windowDays int) (PipelineHealth, error) {
	if windowDays <= 0 || windowDays > 90 {
		windowDays = 7
	}
	var h PipelineHealth
	h.WindowDays = windowDays
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE status = 'SUCCEEDED'),
			       count(*) FILTER (WHERE status = 'FAILED'),
			       count(*) FILTER (WHERE status = 'ABORTED'),
			       avg(duration_ms)
			FROM job_runs
			WHERE organization_id = $1 AND started_at >= now() - ($2 || ' days')::interval`,
			uuid.MustParse(organizationID), windowDays,
		).Scan(&h.TotalRuns, &h.SuccessfulRuns, &h.FailedRuns, &h.AbortedRuns, &h.AvgDurationMS)
	})
	if err != nil {
		return h, err
	}
	if h.TotalRuns > 0 {
		h.SuccessRate = float64(h.SuccessfulRuns) / float64(h.TotalRuns)
	}
	return h, nil
}

// FreshnessStatus is the freshness view of one dataset.
type FreshnessStatus struct {
	DatasetID    string     `json:"dataset_id"`
	FullName     string     `json:"full_name"`
	AssetType    string     `json:"asset_type"`
	ExpectedSecs *int64     `json:"expected_interval_seconds"`
	LatestSource *time.Time `json:"latest_source_timestamp"`
	Status       string     `json:"status"`
	AgeSeconds   *int64     `json:"age_seconds"`
}

// Freshness evaluates freshness expectations against observed evidence.
// Expectations come from freshness_records; observations come from the
// dataset's latest OpenLineage output event (evidence-based, never the
// metadata scan time).
func (s *Service) Freshness(ctx context.Context, organizationID string) ([]FreshnessStatus, error) {
	var out []FreshnessStatus
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH latest AS (
				SELECT f.dataset_id,
				       MAX(f.expected_interval_seconds) AS expected_secs
				FROM freshness_records f
				GROUP BY f.dataset_id
			),
			evidence AS (
				SELECT jr.organization_id, e.target_asset_id,
				       MAX(jr.started_at) AS last_run_at
				FROM lineage_edges e
				JOIN job_runs jr ON jr.id = e.job_run_id
				JOIN jobs j ON j.id = jr.job_id
				WHERE jr.status = 'SUCCEEDED'
				GROUP BY jr.organization_id, e.target_asset_id
			)
			SELECT d.id::text, a.full_name, a.asset_type,
			       l.expected_secs, ev.last_run_at,
			       CASE
			           WHEN l.expected_secs IS NULL THEN 'UNKNOWN'
			           WHEN ev.last_run_at IS NULL THEN 'UNKNOWN'
			           WHEN EXTRACT(EPOCH FROM (now() - ev.last_run_at)) <= l.expected_secs
			               THEN 'FRESH'
			           WHEN EXTRACT(EPOCH FROM (now() - ev.last_run_at)) <= l.expected_secs * 2
			               THEN 'WARNING'
			           ELSE 'STALE'
			       END AS status,
			       CASE WHEN ev.last_run_at IS NOT NULL THEN
			           EXTRACT(EPOCH FROM (now() - ev.last_run_at))::bigint
			       END AS age_seconds
			FROM datasets d
			JOIN data_assets a ON a.id = d.asset_id AND a.deleted_at IS NULL
			LEFT JOIN latest l ON l.dataset_id = d.id
			LEFT JOIN evidence ev ON ev.target_asset_id = d.asset_id
			WHERE d.organization_id = $1 AND d.deleted_at IS NULL
			ORDER BY status, a.full_name`, uuid.MustParse(organizationID))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f FreshnessStatus
			if err := rows.Scan(&f.DatasetID, &f.FullName, &f.AssetType,
				&f.ExpectedSecs, &f.LatestSource, &f.Status, &f.AgeSeconds); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// SetFreshnessExpectation records a freshness expectation for a dataset.
func (s *Service) SetFreshnessExpectation(ctx context.Context, organizationID, datasetID string, expectedSeconds int64) error {
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM datasets WHERE id = $1 AND deleted_at IS NULL)`,
			datasetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO freshness_records
				(dataset_id, organization_id, expected_interval_seconds, status)
			VALUES ($1, $2, $3, 'UNKNOWN')`,
			datasetID, uuid.MustParse(organizationID), expectedSeconds)
		return err
	})
}
