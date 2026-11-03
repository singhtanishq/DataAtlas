//go:build integration

// Cross-tenant isolation tests: with Row-Level Security enforced, a session
// scoped to Organization B must never see Organization A's rows, and
// sessions that do not set the tenant GUC must fail closed.
package tests_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

// rlsFixture migrates a fresh database and seeds two organizations with one
// data source each. It returns a raw connection pool (no tenant GUC set).
func rlsFixture(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	adminPool, err := pgxpool.New(ctx, testAdminURL(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	// Cleanups run LIFO: the pool close (registered first) runs last, after
	// the database drop — the drop needs a live pool.
	t.Cleanup(adminPool.Close)

	testDB := fmt.Sprintf("dataatlas_rls_test_%d", time.Now().UnixNano()/1_000_000)
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, testDB)); err != nil {
			t.Logf("cleanup: drop test database: %v", err)
		}
	})

	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, testDB)); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	parsed, err := url.Parse(testAdminURL(t))
	if err != nil {
		t.Fatalf("parse admin url: %v", err)
	}
	parsed.Path = "/" + testDB

	// Migrations and reference-data seeding run as the table owner.
	ownerURL := parsed.String()
	ownerPool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatalf("connect owner pool: %v", err)
	}
	defer ownerPool.Close()

	if _, err := db.Migrate(ctx, ownerPool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Seed a connector definition so data_sources can reference it.
	if _, err := ownerPool.Exec(ctx, `
		INSERT INTO connector_definitions (key, display_name, platform)
		VALUES ('postgres', 'PostgreSQL', 'postgresql')
		ON CONFLICT (key) DO NOTHING`); err != nil {
		t.Fatalf("seed connector definition: %v", err)
	}

	// All application-level traffic uses the restricted app role, which is
	// subject to Row-Level Security (NOSUPERUSER, NOBYPASSRLS).
	appURL, err := url.Parse(ownerURL)
	if err != nil {
		t.Fatalf("parse owner url: %v", err)
	}
	appURL.User = url.UserPassword("dataatlas_app", "app-local-dev-only")

	pool, err := pgxpool.New(ctx, appURL.String())
	if err != nil {
		t.Fatalf("connect app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// Seed two organizations with one data source each. Insertions run with
	// the corresponding GUC set per transaction (mimicking service code).
	seed := []struct {
		orgID   string
		orgSlug string
		srcName string
	}{
		{"11111111-1111-4111-8111-111111111111", "org-a", "org-a-source"},
		{"22222222-2222-4222-8222-222222222222", "org-b", "org-b-source"},
	}
	for _, row := range seed {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin seed tx: %v", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(
			`SET LOCAL app.current_organization_id = '%s'`, row.orgID)); err != nil {
			t.Fatalf("set guc: %v", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO organizations (id, name, slug) VALUES ($1, 'Org', $2)`,
			row.orgID, row.orgSlug); err != nil {
			t.Fatalf("seed org: %v", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO data_sources (organization_id, connector_key, name, slug)
			 VALUES ($1, 'postgres', $2, $2)`, row.orgID, row.srcName); err != nil {
			t.Fatalf("seed source: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit seed: %v", err)
		}
	}
	return pool
}

func TestRLSCrossTenantIsolation(t *testing.T) {
	pool := rlsFixture(t)
	ctx := context.Background()

	orgA := "11111111-1111-4111-8111-111111111111"
	orgB := "22222222-2222-4222-8222-222222222222"

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL app.current_organization_id = '%s'`, orgB)); err != nil {
		t.Fatal(err)
	}

	// Org B session sees exactly its own rows.
	scopedTables := []string{
		"organizations", "data_sources", "source_credentials", "data_assets",
		"datasets", "jobs", "lineage_edges", "quality_checks", "quality_results",
		"alerts", "audit_logs", "api_keys", "glossary_terms",
	}
	for _, table := range scopedTables {
		var countA, countB int
		// organizations is self-referencing: its row is identified by id.
		idColumn := "organization_id"
		if table == "organizations" {
			idColumn = "id"
		}
		if err := tx.QueryRow(ctx, fmt.Sprintf(
			`SELECT count(*) FILTER (WHERE %s = '%s'), count(*) FROM %s`, idColumn, orgA, table),
		).Scan(&countA, &countB); err != nil {
			t.Fatalf("count %s as org B: %v", table, err)
		}
		if countA != 0 {
			t.Errorf("table %s: org B session sees %d rows from org A; want 0", table, countA)
		}
		// Org B seeded only its organization and data source; any count above
		// that would indicate cross-tenant leakage.
		if table == "organizations" || table == "data_sources" {
			if countB != 1 {
				t.Errorf("table %s: org B session sees %d of its own rows; want 1", table, countB)
			}
		} else if countB != 0 {
			t.Errorf("table %s: org B session sees %d unexpected rows; want 0", table, countB)
		}
	}
}

// TestRLSFailClosedWithoutGUC verifies that sessions which never set the
// tenant context see nothing (defense against forgotten scoping).
func TestRLSFailClosedWithoutGUC(t *testing.T) {
	pool := rlsFixture(t)
	ctx := context.Background()

	scopedTables := []string{"data_sources", "datasets", "quality_checks", "alerts"}
	for _, table := range scopedTables {
		var n int
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n); err != nil {
			t.Fatalf("count %s without GUC: %v", table, err)
		}
		if n != 0 {
			t.Errorf("table %s: unscoped session sees %d rows; want 0 (fail closed)", table, n)
		}
	}
}

// TestRLSInsertEnforced checks that inserting rows for another organization
// is rejected by the WITH CHECK clause.
func TestRLSInsertEnforced(t *testing.T) {
	pool := rlsFixture(t)
	ctx := context.Background()

	orgB := "22222222-2222-4222-8222-222222222222"

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL app.current_organization_id = '%s'`, orgB)); err != nil {
		t.Fatal(err)
	}

	// Attempt to insert a source claiming to belong to org A while scoped to
	// org B: WITH CHECK must reject it.
	_, err = tx.Exec(ctx, `
		INSERT INTO data_sources (organization_id, connector_key, name, slug)
		VALUES ('11111111-1111-4111-8111-111111111111', 'postgres', 'smuggled', 'smuggled')`)
	if err == nil {
		t.Error("insert for another organization was accepted; want RLS violation")
	}
}

// TestRLSJobChildTables covers join-derived policies (job_inputs).
func TestRLSJobChildTables(t *testing.T) {
	pool := rlsFixture(t)
	ctx := context.Background()

	orgA := "11111111-1111-4111-8111-111111111111"
	orgB := "22222222-2222-4222-8222-222222222222"

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL app.current_organization_id = '%s'`, orgA)); err != nil {
		t.Fatal(err)
	}

	// As org A: create dataset + job + job_input referencing them.
	var assetID, datasetID, jobID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO data_assets (organization_id, data_source_id, asset_type, namespace, name)
		VALUES ($1, (SELECT id FROM data_sources WHERE organization_id = $1), 'TABLE', 'db', 't1')
		RETURNING id`, orgA).Scan(&assetID); err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO datasets (asset_id, organization_id) VALUES ($1, $2) RETURNING id`,
		assetID, orgA).Scan(&datasetID); err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO jobs (organization_id, namespace, name) VALUES ($1, 'ns', 'job1')
		RETURNING id`, orgA).Scan(&jobID); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO job_inputs (job_id, dataset_id) VALUES ($1, $2)`, jobID, datasetID); err != nil {
		t.Fatalf("insert job_input as org A: %v", err)
	}

	// Cross-check: org B session must not see the job_input row.
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL app.current_organization_id = '%s'`, orgB)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM job_inputs`).Scan(&n); err != nil {
		t.Fatalf("count job_inputs as org B: %v", err)
	}
	if n != 0 {
		t.Errorf("org B sees %d job_inputs rows; want 0", n)
	}
}
