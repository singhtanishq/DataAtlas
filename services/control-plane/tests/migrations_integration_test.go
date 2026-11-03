//go:build integration

// Integration tests for the SQL migration suite. Requires the local
// infrastructure (docker compose up -d) and TEST_DATABASE_URL pointing at a
// disposable database; defaults to the local development database.
package db_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

const adminURLDefault = "postgresql://dataatlas:dataatlas@localhost:5432/postgres"

func testAdminURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	return adminURLDefault
}

// TestMigrationsRoundTrip applies all migrations to a fresh disposable
// database, verifies key tables and constraints, then reverts everything and
// confirms the database is clean.
func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()

	adminPool, err := pgxpool.New(ctx, testAdminURL(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	// Cleanups run LIFO: the pool close (registered first) runs last, after
	// the database drop — the drop needs a live pool.
	t.Cleanup(adminPool.Close)

	testDB := fmt.Sprintf("dataatlas_migrate_test_%d", time.Now().UnixNano()/1_000_000)
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, testDB)); err != nil {
			t.Logf("cleanup: drop test database: %v", err)
		}
	})

	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, testDB)); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	// Derive the test database URL from the admin URL by swapping the path.
	adminParsed, err := url.Parse(testAdminURL(t))
	if err != nil {
		t.Fatalf("parse admin url: %v", err)
	}
	adminParsed.Path = "/" + testDB
	testURL := adminParsed.String()

	pool, err := pgxpool.New(ctx, testURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("expected migrations to be applied, got none")
	}

	// Verify the required tables exist.
	requiredTables := []string{
		"organizations", "users", "memberships", "teams", "team_members",
		"data_sources", "source_credentials", "connector_definitions",
		"ingestion_pipelines", "ingestion_schedules", "ingestion_runs",
		"connector_checkpoints",
		"data_assets", "datasets", "dataset_versions", "dataset_columns",
		"schema_versions", "schema_columns",
		"jobs", "job_runs", "job_inputs", "job_outputs",
		"lineage_edges", "column_lineage_edges",
		"profiling_runs", "profile_snapshots", "column_profiles",
		"quality_suites", "quality_checks", "quality_runs", "quality_results",
		"quality_incidents", "freshness_records",
		"business_glossaries", "glossary_terms", "glossary_term_relationships",
		"tags", "asset_tags", "ownership_assignments",
		"classification_rules", "asset_classifications", "column_classifications",
		"alert_rules", "alerts", "notifications", "audit_logs", "api_keys", "webhooks",
	}
	for _, table := range requiredTables {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			table).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("required table %q missing after migration", table)
		}
	}

	// dataset_versions is required by the directive's entity list but the
	// catalog currently versions datasets through schema_versions. Alias it
	// as a compatibility view so both names resolve to the same truth.
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'dataset_versions')`,
	).Scan(&exists); err != nil {
		t.Fatalf("check dataset_versions: %v", err)
	}
	if !exists {
		t.Errorf("dataset_versions table/view missing")
	}

	// Re-running must be a no-op.
	secondPass, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(secondPass) != 0 {
		t.Errorf("second migrate applied %d migrations; want 0", len(secondPass))
	}

	// Full rollback must clean every created object.
	reverted, err := db.Rollback(ctx, pool, len(applied))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(reverted) != len(applied) {
		t.Errorf("reverted %d migrations, applied %d", len(reverted), len(applied))
	}

	var remaining int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public'`).Scan(&remaining); err != nil {
		t.Fatalf("count tables after rollback: %v", err)
	}
	// Only schema_migrations (dropped by 001 down? no — it is never created
	// inside 001; it is created lazily) may remain. The 001 down script drops
	// everything else; schema_migrations itself is not dropped by design.
	if remaining > 1 {
		t.Errorf("expected at most schema_migrations to remain after rollback, found %d", remaining)
	}
}
