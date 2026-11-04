// Package ingestion triggers and tracks metadata discovery runs. Run
// requests are published to a JetStream stream and consumed durably by the
// Python ingestion workers.
package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

const (
	// DiscoverySubject carries discovery job requests on the INGESTION stream.
	DiscoverySubject = "ingestion.discover"
	// IngestionStream is the JetStream stream backing ingestion jobs.
	IngestionStream = "INGESTION"
	// publishTimeout bounds JetStream publishes.
	publishTimeout = 10 * time.Second
)

// ErrRunNotFound is returned when a run does not exist in the organization.
var ErrRunNotFound = errors.New("ingestion: run not found")

// Service triggers discovery and reads run history.
type Service struct {
	pool    *db.Pool
	js      nats.JetStreamContext
	natsURL string
}

// NewService builds the ingestion service. natsURL enables on-demand
// JetStream connection when the broker is unavailable at startup.
func NewService(pool *db.Pool, js nats.JetStreamContext, natsURL string) *Service {
	return &Service{pool: pool, js: js, natsURL: natsURL}
}

// ensureJetStream reconnects when the startup connection failed or was lost.
func (s *Service) ensureJetStream() error {
	if s.js != nil {
		return nil
	}
	if s.natsURL == "" {
		return fmt.Errorf("job queue not configured")
	}
	nc, err := nats.Connect(s.natsURL, nats.Timeout(5*time.Second), nats.MaxReconnects(-1))
	if err != nil {
		return fmt.Errorf("message broker unavailable: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream unavailable: %w", err)
	}
	s.js = js
	return nil
}

// Run is the API view of an ingestion run.
type Run struct {
	ID           string          `json:"id"`
	PipelineID   string          `json:"pipeline_id"`
	Trigger      string          `json:"trigger"`
	Status       string          `json:"status"`
	StartedAt    *time.Time      `json:"started_at"`
	FinishedAt   *time.Time      `json:"finished_at"`
	DurationMS   *int64          `json:"duration_ms"`
	ErrorMessage *string         `json:"error_message"`
	Progress     json.RawMessage `json:"progress"`
	CreatedAt    time.Time       `json:"created_at"`
}

// TriggerDiscovery creates a PENDING run and publishes the job to JetStream.
func (s *Service) TriggerDiscovery(ctx context.Context, organizationID, sourceID, trigger string) (Run, error) {
	if err := s.ensureJetStream(); err != nil {
		return Run{}, err
	}
	// The source must exist in this organization.
	var pipelineID string
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT p.id::text
			FROM ingestion_pipelines p
			JOIN data_sources ds ON ds.id = p.data_source_id
			WHERE p.data_source_id = $1 AND p.pipeline_type = 'METADATA_DISCOVERY'
			  AND ds.deleted_at IS NULL AND p.deleted_at IS NULL
			LIMIT 1`, sourceID,
		).Scan(&pipelineID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, fmt.Errorf("no discovery pipeline for source %s", sourceID)
	}
	if err != nil {
		return Run{}, err
	}

	runID := uuid.Must(uuid.NewRandom())
	payload, err := json.Marshal(map[string]string{
		"run_id":          runID.String(),
		"source_id":       sourceID,
		"organization_id": organizationID,
	})
	if err != nil {
		return Run{}, err
	}

	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO ingestion_runs (id, pipeline_id, organization_id, trigger, status)
			VALUES ($1, $2, $3, $4, 'PENDING')`,
			runID, uuid.MustParse(pipelineID), uuid.MustParse(organizationID), trigger)
		return err
	})
	if err != nil {
		return Run{}, err
	}

	if err := s.ensureStream(); err != nil {
		return Run{}, fmt.Errorf("ensure ingestion stream: %w", err)
	}
	if _, err := s.js.Publish(DiscoverySubject, payload, nats.MsgId(runID.String())); err != nil {
		// Mark the run failed so it does not linger as PENDING forever.
		_ = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`UPDATE ingestion_runs SET status = 'FAILED', finished_at = now(),
				 error_message = $2 WHERE id = $1`, runID, "publish failed: "+err.Error())
			return err
		})
		return Run{}, fmt.Errorf("publish discovery job: %w", err)
	}

	run, err := s.GetRun(ctx, organizationID, runID.String())
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// ensureStream creates the INGESTION stream if it does not exist yet. The
// control plane owns stream creation so jobs can be published before any
// worker has started.
func (s *Service) ensureStream() error {
	if _, err := s.js.StreamInfo(IngestionStream); err == nil {
		return nil
	}
	_, err := s.js.AddStream(&nats.StreamConfig{
		Name:      IngestionStream,
		Subjects:  []string{"ingestion.>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		MaxAge:    7 * 24 * time.Hour,
	})
	if err != nil {
		return err
	}
	return nil
}

// GetRun returns one run.
func (s *Service) GetRun(ctx context.Context, organizationID, runID string) (Run, error) {
	var run Run
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id::text, pipeline_id::text, trigger, status, started_at,
			       finished_at, duration_ms, error_message, progress, created_at
			FROM ingestion_runs WHERE id = $1`, runID,
		).Scan(&run.ID, &run.PipelineID, &run.Trigger, &run.Status, &run.StartedAt,
			&run.FinishedAt, &run.DurationMS, &run.ErrorMessage, &run.Progress, &run.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	})
	return run, err
}

// ListRuns returns the runs for one source, newest first.
func (s *Service) ListRuns(ctx context.Context, organizationID, sourceID string) ([]Run, error) {
	var runs []Run
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.id::text, r.pipeline_id::text, r.trigger, r.status, r.started_at,
			       r.finished_at, r.duration_ms, r.error_message, r.progress, r.created_at
			FROM ingestion_runs r
			JOIN ingestion_pipelines p ON p.id = r.pipeline_id
			WHERE p.data_source_id = $1
			ORDER BY r.created_at DESC LIMIT 50`, sourceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var run Run
			if err := rows.Scan(&run.ID, &run.PipelineID, &run.Trigger, &run.Status,
				&run.StartedAt, &run.FinishedAt, &run.DurationMS, &run.ErrorMessage,
				&run.Progress, &run.CreatedAt); err != nil {
				return err
			}
			runs = append(runs, run)
		}
		return rows.Err()
	})
	return runs, err
}
