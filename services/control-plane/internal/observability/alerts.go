package observability

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Alert is one operational alert.
type Alert struct {
	ID         string    `json:"id"`
	AlertType  string    `json:"alert_type"`
	Severity   string    `json:"severity"`
	Title      string    `json:"title"`
	Summary    *string   `json:"summary"`
	EntityType *string   `json:"entity_type"`
	EntityID   *string   `json:"entity_id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListAlerts returns alerts, newest first, optionally filtered by status.
func (s *Service) ListAlerts(ctx context.Context, organizationID, status string) ([]Alert, error) {
	if status != "" && status != "OPEN" && status != "ACKNOWLEDGED" && status != "RESOLVED" {
		status = ""
	}
	var out []Alert
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		query := `
			SELECT id::text, alert_type, severity, title, summary,
			       entity_type, entity_id::text, status, created_at
			FROM alerts WHERE organization_id = $1`
		args := []any{uuid.MustParse(organizationID)}
		if status != "" {
			query += ` AND status = $2`
			args = append(args, status)
		}
		query += ` ORDER BY created_at DESC LIMIT 100`
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Alert
			if err := rows.Scan(&a.ID, &a.AlertType, &a.Severity, &a.Title,
				&a.Summary, &a.EntityType, &a.EntityID, &a.Status, &a.CreatedAt); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// AcknowledgeAlert marks an alert acknowledged.
func (s *Service) AcknowledgeAlert(ctx context.Context, organizationID, alertID string) error {
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE alerts SET status = 'ACKNOWLEDGED', acknowledged_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'OPEN'`, alertID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ResolveAlert closes an alert.
func (s *Service) ResolveAlert(ctx context.Context, organizationID, alertID, note string) error {
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE alerts SET status = 'RESOLVED', resolved_at = now(),
				resolution_note = NULLIF($2, ''), updated_at = now()
			WHERE id = $1 AND status <> 'RESOLVED'`, alertID, note)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
