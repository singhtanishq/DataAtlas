//go:build integration

// Sources API integration tests: registration with encrypted credentials,
// listing, retrieval (credentials never exposed), deletion, status
// transitions when no worker is available, and cross-tenant authorization.
package tests_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/api"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/crypto"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/ingestion"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/sources"
)

const testEncryptionKey = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func sourcesAdminURL(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	pw := os.Getenv("PGPASSWORD")
	if pw == "" {
		pw = "dataatlas"
	}
	return fmt.Sprintf("postgresql://dataatlas:%s@localhost:5432/postgres?sslmode=disable", pw)
}

func newSourcesServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	adminPool, err := pgxpool.New(ctx, sourcesAdminURL(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(adminPool.Close)

	testDB := fmt.Sprintf("dataatlas_sources_test_%d", time.Now().UnixNano()/1_000_000)
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, testDB)); err != nil {
			t.Logf("cleanup drop: %v", err)
		}
	})
	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, testDB)); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	ownerURL, err := url.Parse(sourcesAdminURL(t))
	if err != nil {
		t.Fatal(err)
	}
	ownerURL.Path = "/" + testDB

	ownerPool, err := pgxpool.New(ctx, ownerURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer ownerPool.Close()

	if _, err := db.Migrate(ctx, ownerPool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The postgres connector definition ships as seed data with the first
	// discovery milestone; register it here for API tests.
	_, _ = ownerPool.Exec(ctx, `
		INSERT INTO connector_definitions (key, display_name, platform)
		VALUES ('postgres', 'PostgreSQL', 'postgresql') ON CONFLICT (key) DO NOTHING`)

	appURL, err := url.Parse(ownerURL.String())
	if err != nil {
		t.Fatal(err)
	}
	appURL.User = url.UserPassword("dataatlas_app", "app-local-dev-only")
	pool, err := db.NewPool(ctx, appURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	cipher, err := crypto.NewCipher(testEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	sourceSvc := sources.NewService(pool, cipher, nil, "") // no NATS: worker tests live in the Python suite
	ingestionSvc := ingestion.NewService(pool, nil, "")
	authSvc := auth.NewService(pool)

	cfg := &config.Config{
		AppEnv:           config.EnvDevelopment,
		Port:             "0",
		Version:          "test",
		AppEncryptionKey: testEncryptionKey,
	}
	srv := httptest.NewServer(api.NewServer(cfg, authSvc, sourceSvc, ingestionSvc, nil, nil, nil, nil, slog.Default()))
	t.Cleanup(srv.Close)
	return srv
}

type sourcesClient struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newSourcesClient(t *testing.T, srv *httptest.Server) *sourcesClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &sourcesClient{t: t, base: srv.URL, client: newHTTPClientWithJar(jar)}
}

// newHTTPClientWithJar builds an HTTP client sharing the test cookie jar.
func newHTTPClientWithJar(jar *cookiejar.Jar) *http.Client {
	return &http.Client{Jar: jar}
}

func (c *sourcesClient) do(method, path string, body any) (*http.Response, map[string]any) {
	c.t.Helper()
	var raw []byte
	if body != nil {
		marshaled, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		raw = marshaled
	}
	return c.doRaw(method, path, raw)
}

// doRaw sends a request with a raw JSON body.
func (c *sourcesClient) doRaw(method, path string, raw []byte) (*http.Response, map[string]any) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	if len(raw) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (c *sourcesClient) registerOwner(email string) *sourcesClient {
	c.t.Helper()
	resp, out := c.do("POST", "/v1/auth/register", map[string]any{
		"email":             email,
		"password":          "password-long-enough",
		"display_name":      "Source Owner",
		"organization_name": "Source Org " + email,
	})
	if resp.StatusCode != http.StatusCreated {
		c.t.Fatalf("register %s: %d %v", email, resp.StatusCode, out)
	}
	return c
}

func TestSourceRegistrationLifecycle(t *testing.T) {
	srv := newSourcesServer(t)
	c := newSourcesClient(t, srv).registerOwner("source-owner@example.test")

	body := map[string]any{
		"name":          "Local Warehouse",
		"connector_key": "postgres",
		"config":        map[string]any{"host": "localhost", "port": 5432, "database": "dataatlas"},
		"credentials":   map[string]any{"password": "secret-source-password"},
	}
	resp, out := c.do("POST", "/v1/sources", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register source: %d %v", resp.StatusCode, out)
	}
	src := out["source"].(map[string]any)
	sourceID := src["id"].(string)

	// Credentials must never appear in any API response.
	raw, _ := json.Marshal(out)
	if bytes.Contains(raw, []byte("secret-source-password")) {
		t.Error("credential material leaked through the registration response")
	}

	// List contains exactly the registered source.
	resp, out = c.do("GET", "/v1/sources", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list sources: %d", resp.StatusCode)
	}
	if len(out["sources"].([]any)) != 1 {
		t.Fatalf("expected 1 source, got %v", out["sources"])
	}

	// Get returns config but never credentials.
	resp, out = c.do("GET", "/v1/sources/"+sourceID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get source: %d", resp.StatusCode)
	}
	raw, _ = json.Marshal(out)
	if bytes.Contains(raw, []byte("secret-source-password")) {
		t.Error("credential material leaked through the get response")
	}

	// Connection test without a worker reports disconnect state gracefully.
	resp, out = c.do("POST", "/v1/sources/"+sourceID+"/test", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test connection: %d %v", resp.StatusCode, out)
	}
	result := out["result"].(map[string]any)
	if result["ok"] != false {
		t.Errorf("expected failed validation without worker, got %v", result)
	}

	_, out = c.do("GET", "/v1/sources/"+sourceID, nil)
	if got := out["source"].(map[string]any)["status"]; got != "DISCONNECTED" {
		t.Errorf("source status after failed test = %v; want DISCONNECTED", got)
	}

	// Delete soft-deletes; subsequent reads 404.
	resp, _ = c.do("DELETE", "/v1/sources/"+sourceID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = c.do("GET", "/v1/sources/"+sourceID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete: %d; want 404", resp.StatusCode)
	}
}

func TestSourceAuthorizationCrossTenant(t *testing.T) {
	srv := newSourcesServer(t)
	owner := newSourcesClient(t, srv).registerOwner("src-auth-owner@example.test")
	other := newSourcesClient(t, srv).registerOwner("src-auth-other@example.test")

	resp, out := owner.do("POST", "/v1/sources", map[string]any{
		"name": "Restricted Source", "connector_key": "postgres",
		"config": map[string]any{"host": "localhost"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("owner register source: %d %v", resp.StatusCode, out)
	}
	sourceID := out["source"].(map[string]any)["id"].(string)

	// The other organization cannot see, modify, or test the source.
	resp, _ = other.do("GET", "/v1/sources/"+sourceID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-tenant source get = %d; want 404", resp.StatusCode)
	}
	resp, _ = other.do("DELETE", "/v1/sources/"+sourceID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-tenant source delete = %d; want 404", resp.StatusCode)
	}
	resp, _ = other.do("POST", "/v1/sources/"+sourceID+"/test", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-tenant source test = %d; want 404", resp.StatusCode)
	}
}
