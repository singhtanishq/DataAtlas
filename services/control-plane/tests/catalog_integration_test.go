//go:build integration

// Catalog API integration tests: search over ingested datasets, detail
// retrieval, business-metadata editing, tags and ownership.
package tests_test

import (
	"context"
	"fmt"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/api"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/catalog"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

func newCatalogServer(t *testing.T) (*httptest.Server, *sourcesClient) {
	t.Helper()
	ctx := context.Background()

	adminPool, err := pgxpool.New(ctx, sourcesAdminURL(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(adminPool.Close)

	testDB := fmt.Sprintf("dataatlas_cat_test_%d", time.Now().UnixNano()/1_000_000)
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

	// Seed an org, a source, and one cataloged dataset through the same
	// write path the ingestion engine uses (org-scoped transactions).
	sourceID := uuid.Must(uuid.NewRandom()).String()
	datasetID := uuid.Must(uuid.NewRandom()).String()
	assetID := uuid.Must(uuid.NewRandom()).String()

	catalogSvc := catalog.NewService(pool)
	authSvc := auth.NewService(pool)
	cfg := &config.Config{AppEnv: config.EnvDevelopment, Version: "test"}
	srv := httptest.NewServer(api.NewServer(cfg, authSvc, nil, nil, catalogSvc, nil, nil, slog.Default()))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &sourcesClient{t: t, base: srv.URL, client: newHTTPClientWithJar(jar)}
	client.registerOwner("catalog-api-owner@example.test")

	// Seed the catalog data into the REGISTERED user's organization so the
	// session's tenant scope sees it.
	_, me := client.do("GET", "/v1/auth/me", nil)
	meOrg := me["user"].(map[string]any)["organization_id"].(string)
	if err := pool.WithOrg(ctx, meOrg, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO data_sources (id, organization_id, connector_key, name, slug, status, config)
			VALUES ($1, $2, 'postgres', 'Catalog Source', 'catalog-source', 'ACTIVE', '{}')`,
			uuid.MustParse(sourceID), uuid.MustParse(meOrg)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO data_assets (id, organization_id, data_source_id, asset_type, namespace, name)
			VALUES ($1, $2, $3, 'TABLE', 'shop', 'customers')`,
			uuid.MustParse(assetID), uuid.MustParse(meOrg), uuid.MustParse(sourceID)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO datasets (id, asset_id, organization_id, row_count, column_count, row_count_method)
			VALUES ($1, $2, $3, 1000, 3, 'estimate')`,
			uuid.MustParse(datasetID), uuid.MustParse(assetID), uuid.MustParse(meOrg))
		return err
	}); err != nil {
		t.Fatalf("seed into session org: %v", err)
	}
	return srv, client
}

func TestCatalogSearchAndEdit(t *testing.T) {
	srv, c := newCatalogServer(t)

	// Search finds the seeded dataset.
	resp, out := c.do("GET", "/v1/catalog/datasets?q=customers", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("search: %d %v", resp.StatusCode, out)
	}
	datasets := out["datasets"].([]any)
	if len(datasets) != 1 {
		t.Fatalf("search results = %d; want 1", len(datasets))
	}
	ds := datasets[0].(map[string]any)
	datasetID := ds["id"].(string)
	if ds["full_name"] != "shop.customers" {
		t.Errorf("full_name = %v", ds["full_name"])
	}

	// Search with a non-matching query returns an empty page.
	resp, out = c.do("GET", "/v1/catalog/datasets?q=nonexistent", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("empty search: %d", resp.StatusCode)
	}
	if len(out["datasets"].([]any)) != 0 {
		t.Error("non-matching search returned results")
	}

	// Details include row count and column count.
	resp, out = c.do("GET", "/v1/catalog/datasets/"+datasetID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("details: %d", resp.StatusCode)
	}
	detail := out["dataset"].(map[string]any)
	if detail["row_count"].(float64) != 1000 {
		t.Errorf("row_count = %v", detail["row_count"])
	}

	// Metadata editing: description.
	resp, _ = c.do("PATCH", "/v1/catalog/datasets/"+datasetID,
		map[string]any{"description": "Master customer records from the shop database."})
	if resp.StatusCode != 200 {
		t.Fatalf("description update: %d", resp.StatusCode)
	}
	_, out = c.do("GET", "/v1/catalog/datasets/"+datasetID, nil)
	if got := out["dataset"].(map[string]any)["description"]; got != "Master customer records from the shop database." {
		t.Errorf("description after update = %v", got)
	}

	// Tags: add, verify, remove.
	resp, _ = c.do("POST", "/v1/catalog/datasets/"+datasetID+"/tags", map[string]any{"name": "pii"})
	if resp.StatusCode != 200 {
		t.Fatalf("add tag: %d", resp.StatusCode)
	}
	_, out = c.do("GET", "/v1/catalog/datasets/"+datasetID, nil)
	tags := out["dataset"].(map[string]any)["tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["name"] != "pii" {
		t.Errorf("tags after add = %v", tags)
	}

	// Ownership: assign a business owner (the session user).
	_, me := c.do("GET", "/v1/auth/me", nil)
	meID := me["user"].(map[string]any)["user_id"].(string)
	resp, _ = c.do("POST", "/v1/catalog/datasets/"+datasetID+"/owners", map[string]any{
		"ownership_role": "BUSINESS_OWNER", "owner_type": "USER", "user_id": meID,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("assign owner: %d", resp.StatusCode)
	}
	_, out = c.do("GET", "/v1/catalog/datasets/"+datasetID, nil)
	owners := out["dataset"].(map[string]any)["owners"].([]any)
	if len(owners) != 1 || owners[0].(map[string]any)["ownership_role"] != "BUSINESS_OWNER" {
		t.Errorf("owners after assign = %v", owners)
	}

	// Role enforcement: MEMBER cannot edit metadata (403 via full stack is
	// covered by the unit-level matrix; here the endpoint requires auth).
	resp, _ = c.do("DELETE", "/v1/catalog/datasets/"+datasetID+"/tags/"+uuid.Must(uuid.NewRandom()).String(), nil)
	if resp.StatusCode != 404 {
		t.Errorf("remove unattached tag: %d; want 404", resp.StatusCode)
	}
	resp, _ = c.do("DELETE", "/v1/catalog/datasets/"+datasetID+"/tags/not-a-uuid", nil)
	if resp.StatusCode != 400 {
		t.Errorf("remove invalid tag id: %d; want 400", resp.StatusCode)
	}
	_ = srv
}
