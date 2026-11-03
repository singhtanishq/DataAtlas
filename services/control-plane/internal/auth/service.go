package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

const (
	sessionTTL          = 7 * 24 * time.Hour
	dummyVerifyPassword = "dummy-verify-hash-for-unknown-user"
	maxEmailLength      = 254
	maxPasswordLength   = 128
)

var (
	emailPattern = regexp.MustCompile(`^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$`)
	slugPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)
)
var (
	ErrEmailTaken         = errors.New("auth: email already registered")
	ErrInvalidCredentials = errors.New("auth: invalid email or password")
	ErrSessionExpired     = errors.New("auth: session expired")
	ErrSessionRevoked     = errors.New("auth: session revoked")
	ErrNoOrganization     = errors.New("auth: user has no organization membership")
	ErrNotAMember         = errors.New("auth: not a member of the organization")
)

// Membership describes the user's role in one organization.
type Membership struct {
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	OrganizationSlug string `json:"organization_slug"`
	Role             string `json:"role"`
}

// Identity is the authenticated principal attached to a session.
type Identity struct {
	UserID           string       `json:"user_id"`
	Email            string       `json:"email"`
	DisplayName      string       `json:"display_name"`
	SessionID        string       `json:"-"`
	OrganizationID   string       `json:"organization_id"`
	OrganizationName string       `json:"organization_name"`
	Role             string       `json:"role"`
	Memberships      []Membership `json:"memberships,omitempty"`
}

// Service implements registration and session management.
type Service struct {
	pool *db.Pool
	// dummyHash verifies unknown-email logins at comparable cost to real
	// ones, reducing timing-based account enumeration.
	dummyHash string
}

// NewService builds an auth service. The dummy hash is derived once.
func NewService(pool *db.Pool) *Service {
	hash, _ := HashPassword(dummyVerifyPassword)
	return &Service{pool: pool, dummyHash: hash}
}

// RegisterInput is the registration payload.
type RegisterInput struct {
	Email            string
	Password         string
	DisplayName      string
	OrganizationName string
}

// validate normalizes and checks the registration payload.
func (in *RegisterInput) validate() error {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.OrganizationName = strings.TrimSpace(in.OrganizationName)

	if len(in.Email) == 0 || len(in.Email) > maxEmailLength || !emailPattern.MatchString(in.Email) {
		return fmt.Errorf("a valid email address is required")
	}
	if len(in.Password) < 12 || len(in.Password) > maxPasswordLength {
		return fmt.Errorf("password must be between 12 and %d characters", maxPasswordLength)
	}
	if len(in.DisplayName) == 0 || len(in.DisplayName) > 100 {
		return fmt.Errorf("display name must be between 1 and 100 characters")
	}
	if len(in.OrganizationName) == 0 || len(in.OrganizationName) > 100 {
		return fmt.Errorf("organization name must be between 1 and 100 characters")
	}
	return nil
}

// slugify builds an organization slug from the display name.
func slugify(name string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteRune('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 64 {
		slug = slug[:64]
	}
	if !slugPattern.MatchString(slug) {
		// Fall back to a random slug when the name produces nothing valid.
		slug = "org-" + shortID(8)
	}
	return slug
}

// Register creates a user, an organization, and an OWNER membership, and
// starts a session. The organization id is generated client-side so the
// tenant context can be bound before the first organization-scoped insert.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Identity, string, error) {
	if err := in.validate(); err != nil {
		return Identity{}, "", err
	}

	passwordHash, err := HashPassword(in.Password)
	if err != nil {
		return Identity{}, "", fmt.Errorf("hash password: %w", err)
	}

	userID := uuid.Must(uuid.NewRandom())
	orgID := uuid.Must(uuid.NewRandom())
	slug := slugify(in.OrganizationName)

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return Identity{}, "", err
	}

	var identity Identity
	err = s.pool.WithOrg(ctx, orgID.String(), func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE email = $1 AND deleted_at IS NULL)`,
			in.Email).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrEmailTaken
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO users (id, email, password_hash, display_name)
			VALUES ($1, $2, $3, $4)`,
			userID, in.Email, passwordHash, in.DisplayName); err != nil {
			return err
		}

		// Guarantee slug uniqueness among live organizations.
		for i := 0; ; i++ {
			_, err := tx.Exec(ctx, `
				INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
				orgID, in.OrganizationName, slug)
			if err == nil {
				break
			}
			if i >= 5 || !isUniqueViolation(err) {
				return err
			}
			slug = fmt.Sprintf("%s-%s", slug, shortID(4))
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO memberships (organization_id, user_id, role)
			VALUES ($1, $2, $3)`, orgID, userID, string(RoleOwner)); err != nil {
			return err
		}

		if err := insertSession(ctx, tx, userID, orgID, tokenHash, "", ""); err != nil {
			return err
		}

		identity = Identity{
			UserID:           userID.String(),
			Email:            in.Email,
			DisplayName:      in.DisplayName,
			OrganizationID:   orgID.String(),
			OrganizationName: in.OrganizationName,
			Role:             string(RoleOwner),
		}
		return nil
	})
	if err != nil {
		return Identity{}, "", err
	}
	return identity, token, nil
}

// shortID returns the first n bytes of a fresh UUID as a compact string.
func shortID(n int) string {
	id := uuid.Must(uuid.NewRandom())
	return strings.ReplaceAll(id.String()[:n], "-", "x")
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

var sessionTokens = map[string]string{} // compile-time placeholder, removed by tests

func newSessionToken() (token, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("auth: generate session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	tokenHash = hex.EncodeToString(sum[:])
	return token, tokenHash, nil
}

func insertSession(ctx context.Context, tx pgx.Tx, userID, orgID uuid.UUID, tokenHash, userAgent, ip string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, expires_at, active_org_id, user_agent, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		userID, tokenHash, time.Now().Add(sessionTTL), orgID, userAgent, ip)
	return err
}

// Login verifies credentials and opens a session for the given (or first)
// organization membership.
func (s *Service) Login(ctx context.Context, email, password, requestedOrgID, userAgent, ip string) (Identity, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	var (
		userID       uuid.UUID
		passwordHash string
		displayName  string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, password_hash, display_name FROM users
		WHERE email = $1 AND deleted_at IS NULL`, email,
	).Scan(&userID, &passwordHash, &displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		// Burn comparable time to slow account enumeration.
		_, _ = VerifyPassword(password, s.dummyHash)
		return Identity{}, "", ErrInvalidCredentials
	}
	if err != nil {
		return Identity{}, "", err
	}

	ok, err := VerifyPassword(password, passwordHash)
	if err != nil || !ok {
		return Identity{}, "", ErrInvalidCredentials
	}

	memberships, err := s.userMemberships(ctx, userID)
	if err != nil {
		return Identity{}, "", err
	}
	if len(memberships) == 0 {
		return Identity{}, "", ErrNoOrganization
	}

	selected := memberships[0]
	if requestedOrgID != "" {
		found := false
		for _, m := range memberships {
			if m.OrganizationID == requestedOrgID {
				selected = m
				found = true
				break
			}
		}
		if !found {
			return Identity{}, "", ErrNotAMember
		}
	}

	orgUUID, err := uuid.Parse(selected.OrganizationID)
	if err != nil {
		return Identity{}, "", fmt.Errorf("auth: parse organization id: %w", err)
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return Identity{}, "", err
	}

	var sessionID uuid.UUID
	err = s.pool.WithOrg(ctx, selected.OrganizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO sessions (user_id, token_hash, expires_at, active_org_id, user_agent, ip_address)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			userID, tokenHash, time.Now().Add(sessionTTL), orgUUID, userAgent, ip,
		).Scan(&sessionID)
	})
	if err != nil {
		return Identity{}, "", err
	}

	return Identity{
		UserID:           userID.String(),
		Email:            email,
		DisplayName:      displayName,
		SessionID:        sessionID.String(),
		OrganizationID:   selected.OrganizationID,
		OrganizationName: selected.OrganizationName,
		Role:             selected.Role,
		Memberships:      memberships,
	}, token, nil
}

// IdentityForToken resolves a session token into an identity, updating the
// last-seen marker and enforcing expiry/revocation.
func (s *Service) IdentityForToken(ctx context.Context, token string) (Identity, error) {
	if token == "" {
		return Identity{}, ErrInvalidCredentials
	}
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])

	var identity Identity
	var expiresAt, revokedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT se.id, se.expires_at, se.revoked_at, u.id, u.email, u.display_name,
		       se.active_org_id
		FROM sessions se
		JOIN users u ON u.id = se.user_id AND u.deleted_at IS NULL
		WHERE se.token_hash = $1`, tokenHash,
	).Scan(&identity.SessionID, &expiresAt, &revokedAt,
		&identity.UserID, &identity.Email, &identity.DisplayName,
		&identity.OrganizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, ErrInvalidCredentials
	}
	if err != nil {
		return Identity{}, err
	}
	if revokedAt != nil {
		return Identity{}, ErrSessionRevoked
	}
	if expiresAt != nil && time.Now().After(*expiresAt) {
		return Identity{}, ErrSessionExpired
	}

	memberships, err := s.userMemberships(ctx, mustParseUUID(identity.UserID))
	if err != nil {
		return Identity{}, err
	}
	for _, m := range memberships {
		if m.OrganizationID == identity.OrganizationID {
			identity.OrganizationName = m.OrganizationName
			identity.Role = m.Role
			break
		}
	}
	if identity.Role == "" {
		return Identity{}, ErrNotAMember
	}

	_, err = s.pool.Exec(ctx,
		`UPDATE sessions SET last_seen_at = now() WHERE id = $1`,
		mustParseUUID(identity.SessionID))
	if err != nil {
		return Identity{}, err
	}
	return identity, nil
}

// SwitchOrganization re-points an active session at another membership.
func (s *Service) SwitchOrganization(ctx context.Context, token, organizationID string) (Identity, error) {
	identity, err := s.IdentityForToken(ctx, token)
	if err != nil {
		return Identity{}, err
	}
	for _, m := range identity.Memberships {
		if m.OrganizationID == organizationID {
			_, err := s.pool.Exec(ctx,
				`UPDATE sessions SET active_org_id = $1 WHERE id = $2`,
				mustParseUUID(organizationID), mustParseUUID(identity.SessionID))
			if err != nil {
				return Identity{}, err
			}
			identity.OrganizationID = m.OrganizationID
			identity.OrganizationName = m.OrganizationName
			identity.Role = m.Role
			return identity, nil
		}
	}
	return Identity{}, ErrNotAMember
}

// Logout revokes the session identified by its token.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
		hex.EncodeToString(sum[:]))
	return err
}

func (s *Service) userMemberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT organization_id, organization_name, organization_slug, role FROM get_user_memberships($1)`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.OrganizationID, &m.OrganizationName, &m.OrganizationSlug, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func mustParseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic(fmt.Sprintf("invalid uuid %q: %v", s, err))
	}
	return id
}
