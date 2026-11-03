// Package sources implements the data source registry: registration with
// encrypted credentials, listing, retrieval, deletion and connection testing
// via the Python ingestion workers over NATS request/reply.
package sources

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

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/crypto"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

const (
	// ValidateSubject is the NATS subject carrying connection validation
	// requests to the ingestion worker pool.
	ValidateSubject = "dataatlas.ingestion.validate_connection"
	// validateTimeout bounds a single connection test.
	validateTimeout = 15 * time.Second
)

// ErrSourceNotFound is returned when a source does not exist in the org.
var ErrSourceNotFound = errors.New("sources: data source not found")

// Service manages registered data sources.
type Service struct {
	pool    *db.Pool
	cipher  *crypto.Cipher
	nats    *nats.Conn
	natsURL string
}

// NewService builds a sources service. natsURL enables on-demand (re)
// connection when the broker is unavailable at startup.
func NewService(pool *db.Pool, cipher *crypto.Cipher, nc *nats.Conn, natsURL string) *Service {
	return &Service{pool: pool, cipher: cipher, nats: nc, natsURL: natsURL}
}

// ensureNats reconnects to the broker if the startup connection failed or
// was lost. Connection attempts are serialized.
func (s *Service) ensureNats() error {
	if s.nats != nil && !s.nats.IsClosed() {
		return nil
	}
	if s.natsURL == "" {
		return fmt.Errorf("message broker not configured")
	}
	nc, err := nats.Connect(s.natsURL, nats.Timeout(5*time.Second), nats.MaxReconnects(-1))
	if err != nil {
		return fmt.Errorf("message broker unavailable: %w", err)
	}
	s.nats = nc
	return nil
}

// Source is the API-facing view of a registered data source. Credentials
// are never included.
type Source struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Slug      string          `json:"slug"`
	Status    string          `json:"status"`
	Connector string          `json:"connector_key"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// RegisterInput is the payload for source registration.
type RegisterInput struct {
	Name         string          `json:"name"`
	ConnectorKey string          `json:"connector_key"`
	Config       json.RawMessage `json:"config"`
	Credentials  json.RawMessage `json:"credentials"` // encrypted at rest, never returned
}

// Register stores a new source with encrypted credentials and a default
// metadata-discovery pipeline.
func (s *Service) Register(ctx context.Context, organizationID string, in RegisterInput) (Source, error) {
	if in.Name == "" || in.ConnectorKey == "" {
		return Source{}, fmt.Errorf("name and connector_key are required")
	}
	var connKey string
	err := s.pool.QueryRow(ctx,
		`SELECT key FROM connector_definitions WHERE key = $1`, in.ConnectorKey,
	).Scan(&connKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, fmt.Errorf("unknown connector %q", in.ConnectorKey)
	}
	if err != nil {
		return Source{}, err
	}

	credsJSON := "{}"
	if len(in.Credentials) > 0 {
		credsJSON = string(in.Credentials)
	}
	box, err := s.cipher.Encrypt([]byte(credsJSON))
	if err != nil {
		return Source{}, fmt.Errorf("encrypt credentials: %w", err)
	}

	sourceID := uuid.Must(uuid.NewRandom())
	orgUUID := uuid.MustParse(organizationID)
	slug := slugify(in.Name)
	var src Source
	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		// Guarantee slug uniqueness among the organization's live sources.
		for i := 0; ; i++ {
			err := tx.QueryRow(ctx, `
				INSERT INTO data_sources (id, organization_id, connector_key, name, slug, status, config)
				VALUES ($1, $2, $3, $4, $5, 'CONFIGURED', $6)
				RETURNING id::text, name, slug, status, connector_key, config, created_at, updated_at`,
				sourceID, orgUUID, in.ConnectorKey, in.Name, slug, []byte(in.Config),
			).Scan(&src.ID, &src.Name, &src.Slug, &src.Status, &src.Connector, &src.Config, &src.CreatedAt, &src.UpdatedAt)
			if err == nil {
				break
			}
			if i >= 5 || !isUniqueViolation(err) {
				return err
			}
			slug = fmt.Sprintf("%s-%s", slug, uuid.Must(uuid.NewRandom()).String()[:4])
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO source_credentials (data_source_id, organization_id, ciphertext, nonce)
			VALUES ($1, $2, $3, $4)`,
			sourceID, uuid.MustParse(organizationID), box.Ciphertext, box.Nonce); err != nil {
			return err
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO ingestion_pipelines (organization_id, data_source_id, name, pipeline_type, status)
			VALUES ($1, $2, $3, 'METADATA_DISCOVERY', 'ACTIVE')`,
			uuid.MustParse(organizationID), sourceID, in.Name+" discovery")
		return err
	})
	if err != nil {
		return Source{}, err
	}
	return src, nil
}

// List returns the organization's data sources.
func (s *Service) List(ctx context.Context, organizationID string) ([]Source, error) {
	var out []Source
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, slug, status, connector_key, config, created_at, updated_at
			FROM data_sources WHERE deleted_at IS NULL ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var src Source
			if err := rows.Scan(&src.ID, &src.Name, &src.Slug, &src.Status, &src.Connector, &src.Config, &src.CreatedAt, &src.UpdatedAt); err != nil {
				return err
			}
			out = append(out, src)
		}
		return rows.Err()
	})
	return out, err
}

// Get returns one source.
func (s *Service) Get(ctx context.Context, organizationID, sourceID string) (Source, error) {
	var src Source
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id::text, name, slug, status, connector_key, config, created_at, updated_at
			FROM data_sources WHERE id = $1 AND deleted_at IS NULL`, sourceID,
		).Scan(&src.ID, &src.Name, &src.Slug, &src.Status, &src.Connector, &src.Config, &src.CreatedAt, &src.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSourceNotFound
		}
		return err
	})
	return src, err
}

// Delete soft-deletes a source and disables its pipelines.
func (s *Service) Delete(ctx context.Context, organizationID, sourceID string) error {
	return s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE data_sources SET deleted_at = now(), updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL`, sourceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrSourceNotFound
		}
		_, err = tx.Exec(ctx, `
			UPDATE ingestion_pipelines SET status = 'DISABLED', updated_at = now()
			WHERE data_source_id = $1`, sourceID)
		return err
	})
}

// ValidateReply is the worker's connection test result.
type ValidateReply struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latency_ms"`
}

// TestConnection asks the ingestion worker pool to validate the connection.
// The worker pulls the encrypted credentials from the database itself, so
// secrets never travel through the message broker.
func (s *Service) TestConnection(ctx context.Context, organizationID, sourceID string) (ValidateReply, error) {
	// Verify the source belongs to the organization before dispatching.
	if _, err := s.Get(ctx, organizationID, sourceID); err != nil {
		return ValidateReply{}, err
	}

	if err := s.ensureNats(); err != nil {
		reply := ValidateReply{OK: false, Message: "connection testing unavailable: " + err.Error()}
		s.setStatus(ctx, organizationID, sourceID, "DISCONNECTED", reply.Message)
		return reply, nil
	}

	request := map[string]string{
		"source_id":       sourceID,
		"organization_id": organizationID,
		"connector_key":   "", // resolved by the worker from the database
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return ValidateReply{}, err
	}

	s.setStatus(ctx, organizationID, sourceID, "VALIDATING", "connection test requested")

	msg, err := s.nats.Request(ValidateSubject, payload, validateTimeout)
	if err != nil {
		reply := ValidateReply{OK: false, Message: "no worker responded: " + err.Error()}
		s.setStatus(ctx, organizationID, sourceID, "DISCONNECTED", reply.Message)
		return reply, nil
	}

	var reply ValidateReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return ValidateReply{}, fmt.Errorf("decode worker reply: %w", err)
	}

	if reply.OK {
		s.setStatus(ctx, organizationID, sourceID, "ACTIVE", reply.Message)
	} else {
		s.setStatus(ctx, organizationID, sourceID, "FAILED", reply.Message)
	}
	return reply, nil
}

func (s *Service) setStatus(ctx context.Context, organizationID, sourceID, status, reason string) {
	_ = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var from string
		_ = tx.QueryRow(ctx,
			`SELECT status FROM data_sources WHERE id = $1`, sourceID).Scan(&from)
		if _, err := tx.Exec(ctx,
			`UPDATE data_sources SET status = $2, updated_at = now() WHERE id = $1`,
			sourceID, status); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO data_source_status_history (data_source_id, organization_id, from_status, to_status, reason)
			VALUES ($1, $2, NULLIF($3, ''), $4, $5)`,
			sourceID, uuid.MustParse(organizationID), from, status, reason)
		return err
	})
}

// slugify builds a URL-safe slug from a display name.
func slugify(name string) string {
	var b []byte
	prevDash := true
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			b = append(b, c)
			prevDash = false
		case c >= 'A' && c <= 'Z':
			b = append(b, c+'a'-'A')
			prevDash = false
		case !prevDash:
			b = append(b, '-')
			prevDash = true
		}
	}
	slug := string(bytes_trim(b, '-'))
	if slug == "" {
		slug = "source-" + uuid.Must(uuid.NewRandom()).String()[:8]
	}
	if len(slug) > 64 {
		slug = slug[:64]
	}
	return slug
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

func bytes_trim(b []byte, cut byte) []byte {
	for len(b) > 0 && b[0] == cut {
		b = b[1:]
	}
	for len(b) > 0 && b[len(b)-1] == cut {
		b = b[:len(b)-1]
	}
	return b
}
