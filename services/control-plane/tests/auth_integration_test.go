//go:build integration

// End-to-end authentication integration tests over the real HTTP stack:
// registration, session cookie flow, logout, and cross-tenant membership
// enforcement.
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

	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/api"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

func adminURL(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgresql://dataatlas:dataatlas@localhost:5432/postgres?sslmode=disable"
}

// newAuthServer migrates a fresh database and returns an httptest.Server
// with the full API stack.
func newAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	adminPool, err := pgxpool.New(ctx, adminURL(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(adminPool.Close)

	testDB := fmt.Sprintf("dataatlas_auth_test_%d", time.Now().UnixNano()/1_000_000)
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, testDB)); err != nil {
			t.Logf("cleanup drop: %v", err)
		}
	})
	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, testDB)); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	ownerURL, err := url.Parse(adminURL(t))
	if err != nil {
		t.Fatal(err)
	}
	ownerURL.Path = "/" + testDB

	ownerPool, err := pgxpool.New(ctx, ownerURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer ownerPool.Close()

	// Apply migrations as owner.
	applied, err := db.Migrate(ctx, ownerPool)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("no migrations applied")
	}

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

	cfg := &config.Config{
		AppEnv:  config.EnvDevelopment,
		Port:    "0",
		Version: "test",
	}
	authSvc := auth.NewService(pool)
	srv := httptest.NewServer(api.NewServer(cfg, authSvc, slog.Default()))
	t.Cleanup(srv.Close)
	return srv
}

type authClient struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newAuthClient(t *testing.T, srv *httptest.Server) *authClient {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &authClient{t: t, base: srv.URL, client: &http.Client{Jar: jar}}
}

func (c *authClient) post(t *testing.T, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (c *authClient) get(t *testing.T, path string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := c.client.Get(c.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestRegisterLoginMeLogoutFlow(t *testing.T) {
	srv := newAuthServer(t)
	c := newAuthClient(t, srv)

	// Register creates user + organization + OWNER membership + session.
	resp, body := c.post(t, "/v1/auth/register", map[string]any{
		"email":             "owner@example.test",
		"password":          "correct-horse-battery",
		"display_name":      "Demo Owner",
		"organization_name": "Acme Analytics Corporation",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d body = %v", resp.StatusCode, body)
	}
	user := body["user"].(map[string]any)
	if user["role"] != "OWNER" || user["organization_name"] != "Acme Analytics Corporation" {
		t.Errorf("unexpected registration identity: %v", user)
	}

	// Me resolves the session cookie.
	resp, body = c.get(t, "/v1/auth/me")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me status = %d body = %v", resp.StatusCode, body)
	}
	if body["user"].(map[string]any)["email"] != "owner@example.test" {
		t.Errorf("me returned wrong user: %v", body)
	}

	// Duplicate registration is rejected.
	resp, _ = c.post(t, "/v1/auth/register", map[string]any{
		"email": "owner@example.test", "password": "correct-horse-battery",
		"display_name": "Again", "organization_name": "Other Co",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate registration status = %d; want 409", resp.StatusCode)
	}

	// Logout revokes the session.
	resp, _ = c.post(t, "/v1/auth/logout", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("logout status = %d", resp.StatusCode)
	}
	resp, _ = c.get(t, "/v1/auth/me")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("me after logout status = %d; want 401", resp.StatusCode)
	}

	// Login with the same credentials succeeds.
	c2 := newAuthClient(t, srv)
	resp, body = c2.post(t, "/v1/auth/login", map[string]any{
		"email": "owner@example.test", "password": "correct-horse-battery",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d body = %v", resp.StatusCode, body)
	}

	// Wrong password rejected.
	c3 := newAuthClient(t, srv)
	resp, _ = c3.post(t, "/v1/auth/login", map[string]any{
		"email": "owner@example.test", "password": "wrong-password-here",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad login status = %d; want 401", resp.StatusCode)
	}
}

func TestCrossOrganizationSessionIsolation(t *testing.T) {
	srv := newAuthServer(t)

	// Two independent organizations.
	orgA := newAuthClient(t, srv)
	resp, bodyA := orgA.post(t, "/v1/auth/register", map[string]any{
		"email": "a@example.test", "password": "password-a-long-enough",
		"display_name": "Owner A", "organization_name": "Org A",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("org A register = %d %v", resp.StatusCode, bodyA)
	}
	orgAID := bodyA["user"].(map[string]any)["organization_id"].(string)

	orgB := newAuthClient(t, srv)
	resp, _ = orgB.post(t, "/v1/auth/register", map[string]any{
		"email": "b@example.test", "password": "password-b-long-enough",
		"display_name": "Owner B", "organization_name": "Org B",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("org B register failed")
	}

	// Org B cannot switch into org A's organization.
	resp, _ = orgB.post(t, "/v1/auth/organizations/switch", map[string]any{
		"organization_id": orgAID,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org B switch into org A = %d; want 403", resp.StatusCode)
	}

	// Org B's session still resolves to org B only.
	resp, body := orgB.get(t, "/v1/auth/me")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("org B me = %d", resp.StatusCode)
	}
	me := body["user"].(map[string]any)
	if me["organization_id"] == orgAID {
		t.Error("org B session resolved into org A")
	}
}

func TestRegistrationValidation(t *testing.T) {
	srv := newAuthServer(t)
	c := newAuthClient(t, srv)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"bad email", map[string]any{"email": "nope", "password": "long-enough-password", "display_name": "X", "organization_name": "Y"}},
		{"short password", map[string]any{"email": "v@example.test", "password": "short", "display_name": "X", "organization_name": "Y"}},
		{"missing org", map[string]any{"email": "v2@example.test", "password": "long-enough-password", "display_name": "X"}},
	}
	for _, tc := range cases {
		resp, _ := c.post(t, "/v1/auth/register", tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400", tc.name, resp.StatusCode)
		}
	}
}

func TestUnauthenticatedAccessDenied(t *testing.T) {
	srv := newAuthServer(t)
	resp, err := http.Get(srv.URL + "/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated me = %d; want 401", resp.StatusCode)
	}
}
