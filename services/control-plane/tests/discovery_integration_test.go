//go:build integration

// Discovery trigger integration tests: runs are created, jobs land on
// JetStream, run tracking respects tenant isolation.
package tests_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/crypto"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/ingestion"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/sources"
)

func TestDiscoveryTriggerLifecycle(t *testing.T) {
	ctx := context.Background()

	adminPool, err := pgxpool.New(ctx, sourcesAdminURL(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(adminPool.Close)

	testDB := fmt.Sprintf("dataatlas_disc_test_%d", time.Now().UnixNano()/1_000_000)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, testDB))
	})
	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, testDB)); err != nil {
		t.Fatalf("create database: %v", err)
	}

	ownerURL, appURL := deriveURLs(t, testDB)
	ownerPool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerPool.Close()
	if _, err := db.Migrate(ctx, ownerPool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = ownerPool.Exec(ctx, `
		INSERT INTO connector_definitions (key, display_name, platform)
		VALUES ('postgres', 'PostgreSQL', 'postgresql') ON CONFLICT (key) DO NOTHING`)

	pool, err := db.NewPool(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// JetStream must be reachable; otherwise skip.
	nc, err := nats.Connect(natsURLForTests(), nats.Timeout(3*time.Second))
	if err != nil {
		t.Skipf("NATS unavailable: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Skipf("JetStream unavailable: %v", err)
	}
	if _, err := js.AccountInfo(); err != nil {
		t.Skipf("JetStream not enabled: %v", err)
	}

	// Seed an organization and a source with a discovery pipeline.
	orgID := uuid.Must(uuid.NewRandom()).String()
	sourceID := uuid.Must(uuid.NewRandom()).String()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	box, _ := cipher.Encrypt([]byte(`{"password":"not-used-here"}`))
	if err := pool.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO organizations (id, name, slug) VALUES ($1, 'Disc Org', $2)`,
			uuid.MustParse(orgID), "disc-"+orgID[:8]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO data_sources (id, organization_id, connector_key, name, slug, status, config)
			VALUES ($1, $2, 'postgres', 'Discovery Source', 'discovery-source', 'ACTIVE', '{}')`,
			uuid.MustParse(sourceID), uuid.MustParse(orgID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ingestion_pipelines
				(organization_id, data_source_id, name, pipeline_type, status)
			VALUES ($1, $2, 'Discovery Source discovery', 'METADATA_DISCOVERY', 'ACTIVE')`,
			uuid.MustParse(orgID), uuid.MustParse(sourceID)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO source_credentials (data_source_id, organization_id, ciphertext, nonce)
			VALUES ($1, $2, $3, $4)`,
			uuid.MustParse(sourceID), uuid.MustParse(orgID), box.Ciphertext, box.Nonce)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sourceSvc := sources.NewService(pool, cipher, nil, "")
	if _, err := sourceSvc.Get(ctx, orgID, sourceID); err != nil {
		t.Fatalf("source seed invalid: %v", err)
	}

	ingestionSvc := ingestion.NewService(pool, js, natsURLForTests())

	run, err := ingestionSvc.TriggerDiscovery(ctx, orgID, sourceID, "MANUAL")
	if err != nil {
		t.Fatalf("trigger discovery: %v", err)
	}
	if run.Status != "PENDING" {
		t.Errorf("run status = %s; want PENDING", run.Status)
	}

	info, err := js.StreamInfo(ingestion.IngestionStream)
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	if info.State.Msgs == 0 {
		t.Error("expected the discovery job on the stream")
	}

	// The published payload must carry tenant + run identity.
	msg, err := js.GetMsg(ingestion.IngestionStream, info.State.LastSeq)
	if err != nil {
		t.Fatalf("get msg: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["run_id"] != run.ID || payload["organization_id"] != orgID || payload["source_id"] != sourceID {
		t.Errorf("job payload mismatch: %v", payload)
	}

	got, err := ingestionSvc.GetRun(ctx, orgID, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.ID != run.ID {
		t.Error("run id mismatch")
	}

	runs, err := ingestionSvc.ListRuns(ctx, orgID, sourceID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("runs = %d; want 1", len(runs))
	}

	// A different organization cannot read the run.
	otherOrg := uuid.Must(uuid.NewRandom()).String()
	if _, err := ingestionSvc.GetRun(ctx, otherOrg, run.ID); err == nil {
		t.Error("cross-tenant run read succeeded; want not found")
	}
}

func natsURLForTests() string {
	if u := os.Getenv("TEST_NATS_URL"); u != "" {
		return u
	}
	return "nats://localhost:4222"
}

// deriveURLs returns the owner-role and app-role URLs for the test database.
func deriveURLs(t *testing.T, testDB string) (string, string) {
	t.Helper()
	parsed, err := url.Parse(sourcesAdminURL(t))
	if err != nil {
		t.Fatalf("parse admin url: %v", err)
	}
	parsed.Path = "/" + testDB
	owner := parsed.String()

	app, err := url.Parse(owner)
	if err != nil {
		t.Fatalf("parse owner url: %v", err)
	}
	app.User = url.UserPassword("dataatlas_app", "app-local-dev-only")
	return owner, app.String()
}
