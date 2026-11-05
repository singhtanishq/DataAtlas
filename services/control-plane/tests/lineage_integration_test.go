//go:build integration

// Lineage integration tests: OpenLineage event processing (idempotent),
// graph traversal in both directions, impact analysis and dbt manifest
// ingestion.
package tests_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/api"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/catalog"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/lineage"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/quality"
)

func newLineageServer(t *testing.T) (*httptest.Server, *sourcesClient) {
	t.Helper()
	ctx := context.Background()

	adminPool, err := pgxpool.New(ctx, sourcesAdminURL(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(adminPool.Close)

	testDB := fmt.Sprintf("dataatlas_lin_test_%d", time.Now().UnixNano()/1_000_000)
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

	authSvc := auth.NewService(pool)
	cfg := &config.Config{AppEnv: config.EnvDevelopment, Version: "test"}
	srv := httptest.NewServer(api.NewServer(cfg, authSvc, nil, nil,
		catalog.NewService(pool), quality.NewService(pool, nil, ""),
		lineage.NewService(pool), slog.Default()))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &sourcesClient{t: t, base: srv.URL, client: newHTTPClientWithJar(jar)}
	client.registerOwner("lineage-owner@example.test")
	return srv, client
}

func olEvent(eventType, runID, jobNS, jobName string, inputs, outputs [][2]string) map[string]any {
	toDatasets := func(pairs [][2]string) []map[string]string {
		out := make([]map[string]string, 0, len(pairs))
		for _, p := range pairs {
			out = append(out, map[string]string{"namespace": p[0], "name": p[1]})
		}
		return out
	}
	return map[string]any{
		"eventType": eventType,
		"eventTime": time.Now().UTC().Format(time.RFC3339),
		"producer":  "https://dataatlas.test/producer",
		"schemaURL": "https://openlineage.io/spec/2-0-2/OpenLineage.json",
		"run":       map[string]any{"runId": runID},
		"job":       map[string]any{"namespace": jobNS, "name": jobName},
		"inputs":    toDatasets(inputs),
		"outputs":   toDatasets(outputs),
	}
}

func TestOpenLineageIngestionAndTraversal(t *testing.T) {
	_, c := newLineageServer(t)

	runID := uuid.Must(uuid.NewRandom()).String()
	// START: raw.customers -> staging.customers -> analytics.customer_metrics
	// modeled as one job with one input and one output, plus a second job.
	event1 := olEvent("START", runID, "analytics", "daily_customer_pipeline",
		[][2]string{{"postgres.raw", "customers"}},
		[][2]string{{"postgres.staging", "stg_customers"}})
	resp, out := c.do("POST", "/v1/lineage/openlineage", event1)
	if resp.StatusCode != 200 {
		t.Fatalf("ingest start: %d %v", resp.StatusCode, out)
	}

	// COMPLETE with an additional hop.
	event2 := olEvent("COMPLETE", runID, "analytics", "daily_customer_pipeline",
		[][2]string{{"postgres.raw", "customers"}},
		[][2]string{{"postgres.staging", "stg_customers"}})
	resp, _ = c.do("POST", "/v1/lineage/openlineage", event2)
	if resp.StatusCode != 200 {
		t.Fatalf("ingest complete: %d", resp.StatusCode)
	}

	runID2 := uuid.Must(uuid.NewRandom()).String()
	event3 := olEvent("COMPLETE", runID2, "analytics", "customer_metrics_builder",
		[][2]string{{"postgres.staging", "stg_customers"}},
		[][2]string{{"postgres.analytics", "customer_metrics"}})
	resp, _ = c.do("POST", "/v1/lineage/openlineage", event3)
	if resp.StatusCode != 200 {
		t.Fatalf("ingest second job: %d", resp.StatusCode)
	}

	// Find the customer_metrics asset for traversal.
	resp, out = c.do("GET", "/v1/catalog/datasets?q=customer_metrics", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("search: %d", resp.StatusCode)
	}
	datasets := out["datasets"].([]any)
	if len(datasets) != 1 {
		t.Fatalf("expected 1 dataset, got %d", len(datasets))
	}
	metricsAsset := datasets[0].(map[string]any)["asset_id"].(string)

	// Upstream traversal: raw.customers and staging both appear.
	resp, out = c.do("GET", "/v1/lineage/"+metricsAsset+"/upstream?depth=5", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("upstream: %d %v", resp.StatusCode, out)
	}
	graph := out["graph"].(map[string]any)
	nodes := graph["nodes"].([]any)
	if len(nodes) < 3 {
		t.Errorf("upstream nodes = %d; want >= 3", len(nodes))
	}
	names := map[string]bool{}
	for _, n := range nodes {
		names[n.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"customers", "stg_customers", "customer_metrics"} {
		if !names[want] {
			t.Errorf("upstream missing node %q (have %v)", want, names)
		}
	}

	// Downstream from raw customers reaches customer_metrics.
	resp, out = c.do("GET", "/v1/catalog/datasets?q=raw", nil)
	if resp.StatusCode != 200 || len(out["datasets"].([]any)) != 1 {
		t.Fatalf("root search: %d %v", resp.StatusCode, out)
	}
	rootAsset := out["datasets"].([]any)[0].(map[string]any)["asset_id"].(string)
	resp, out = c.do("GET", "/v1/lineage/"+rootAsset+"/downstream?depth=5", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("downstream: %d", resp.StatusCode)
	}
	downNames := map[string]bool{}
	for _, n := range out["graph"].(map[string]any)["nodes"].([]any) {
		downNames[n.(map[string]any)["name"].(string)] = true
	}
	if !downNames["customer_metrics"] {
		t.Errorf("downstream missing customer_metrics: %v", downNames)
	}

	// Impact analysis from raw customers.
	resp, out = c.do("GET", "/v1/lineage/"+rootAsset+"/impact", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("impact: %d %v", resp.StatusCode, out)
	}
	impact := out["impact"].(map[string]any)
	if len(impact["downstream_datasets"].([]any)) < 2 {
		t.Errorf("impact downstream = %v", impact["downstream_datasets"])
	}

	// Idempotency: re-delivering the same event creates no new edges.
	resp, out = c.do("POST", "/v1/lineage/openlineage", event2)
	if resp.StatusCode != 200 {
		t.Fatalf("re-ingest: %d", resp.StatusCode)
	}
	resp, out = c.do("GET", "/v1/lineage/"+rootAsset+"/downstream?depth=5", nil)
	if resp.StatusCode != 200 {
		t.Fatal("re-traverse failed")
	}
	edges := out["graph"].(map[string]any)["edges"].([]any)
	if len(edges) != 2 {
		t.Errorf("edges after re-ingest = %d; want 2", len(edges))
	}

	// Validation: malformed events are rejected.
	for name, body := range map[string]map[string]any{
		"missing eventType": {"eventTime": "2026-01-01T00:00:00Z", "job": map[string]any{"namespace": "n", "name": "j"}, "run": map[string]any{"runId": runID}},
		"bad eventType":     {"eventType": "MAYBE", "eventTime": "2026-01-01T00:00:00Z", "job": map[string]any{"namespace": "n", "name": "j"}, "run": map[string]any{"runId": runID}},
		"missing runId":     {"eventType": "START", "eventTime": "2026-01-01T00:00:00Z", "job": map[string]any{"namespace": "n", "name": "j"}, "run": map[string]any{}},
	} {
		resp, _ = c.do("POST", "/v1/lineage/openlineage", body)
		if resp.StatusCode != 400 {
			t.Errorf("%s: status = %d; want 400", name, resp.StatusCode)
		}
	}
}

func TestDBTManifestIngestion(t *testing.T) {
	_, c := newLineageServer(t)

	manifest := map[string]any{
		"metadata": map[string]any{
			"project_name": "acme_analytics",
			"adapter_type": "postgres",
			"dbt_version":  "1.8.0",
		},
		"sources": map[string]any{
			"source.acme.raw.customers": map[string]any{
				"unique_id": "source.acme.raw.customers", "name": "customers",
				"schema": "raw", "source_name": "raw",
			},
		},
		"nodes": map[string]any{
			"model.acme.stg_customers": map[string]any{
				"unique_id": "model.acme.stg_customers", "resource_type": "model",
				"name": "stg_customers", "schema": "staging",
				"description": "Cleaned customer records",
				"depends_on":  map[string]any{"nodes": []string{"source.acme.raw.customers"}},
				"columns": map[string]any{
					"email": map[string]any{"name": "email", "data_type": "text", "description": "Customer email"},
				},
			},
			"model.acme.customer_metrics": map[string]any{
				"unique_id": "model.acme.customer_metrics", "resource_type": "model",
				"name": "customer_metrics", "schema": "analytics",
				"depends_on": map[string]any{"nodes": []string{"model.acme.stg_customers"}},
			},
		},
	}
	body, _ := json.Marshal(manifest)
	resp, out := c.doRaw("POST", "/v1/lineage/dbt/manifest", body)
	if resp.StatusCode != 200 {
		t.Fatalf("manifest ingestion: %d %v", resp.StatusCode, out)
	}
	if out["assets_created"].(float64) != 3 {
		t.Errorf("assets_created = %v; want 3", out["assets_created"])
	}

	// Verify the lineage chain raw -> stg -> metrics.
	resp, out = c.do("GET", "/v1/catalog/datasets?q=customer_metrics", nil)
	if resp.StatusCode != 200 {
		t.Fatal("search failed")
	}
	metricsAsset := out["datasets"].([]any)[0].(map[string]any)["asset_id"].(string)
	resp, out = c.do("GET", "/v1/lineage/"+metricsAsset+"/upstream?depth=5", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("upstream: %d", resp.StatusCode)
	}
	names := map[string]bool{}
	for _, n := range out["graph"].(map[string]any)["nodes"].([]any) {
		names[n.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"customers", "stg_customers", "customer_metrics"} {
		if !names[want] {
			t.Errorf("dbt lineage missing %q: %v", want, names)
		}
	}

	// Malformed manifest rejected.
	resp, _ = c.doRaw("POST", "/v1/lineage/dbt/manifest", []byte(`{"metadata":{}}`))
	if resp.StatusCode != 400 {
		t.Errorf("empty manifest: %d; want 400", resp.StatusCode)
	}
}

func TestLineageTenantIsolation(t *testing.T) {
	_, orgA := newLineageServer(t)
	_, orgB := newLineageServer(t)

	// Org A ingests an edge.
	runID := uuid.Must(uuid.NewRandom()).String()
	event := olEvent("COMPLETE", runID, "analytics", "pipeline_a",
		[][2]string{{"ns", "secret_source_table"}},
		[][2]string{{"ns", "secret_target_table"}})
	resp, _ := orgA.do("POST", "/v1/lineage/openlineage", event)
	if resp.StatusCode != 200 {
		t.Fatalf("ingest: %d", resp.StatusCode)
	}

	// Org A finds the source asset.
	resp, out := orgA.do("GET", "/v1/catalog/datasets?q=secret_source", nil)
	if resp.StatusCode != 200 || len(out["datasets"].([]any)) != 1 {
		t.Fatalf("org A search: %d", resp.StatusCode)
	}
	assetID := out["datasets"].([]any)[0].(map[string]any)["asset_id"].(string)

	// Org B cannot traverse it.
	resp, _ = orgB.do("GET", "/v1/lineage/"+assetID+"/downstream", nil)
	if resp.StatusCode != 404 {
		t.Errorf("org B traversal = %d; want 404", resp.StatusCode)
	}
	resp, _ = orgB.do("GET", "/v1/lineage/"+assetID+"/impact", nil)
	if resp.StatusCode != 404 {
		t.Errorf("org B impact = %d; want 404", resp.StatusCode)
	}
}
