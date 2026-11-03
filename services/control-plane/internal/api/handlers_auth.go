package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/auth"
	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/config"
)

type identityContextKey struct{}

// withIdentity stores the authenticated identity on the context.
func withIdentity(ctx context.Context, id auth.Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, id)
}

// IdentityFrom returns the authenticated identity for the request.
func IdentityFrom(ctx context.Context) (auth.Identity, bool) {
	id, ok := ctx.Value(identityContextKey{}).(auth.Identity)
	return id, ok
}

// setSessionCookie writes the session cookie. The token is opaque and only
// ever stored hashed server-side.
func setSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(7 * 24 * time.Hour),
	})
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

type registerRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	DisplayName      string `json:"display_name"`
	OrganizationName string `json:"organization_name"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusServiceUnavailable, "auth_unavailable",
			"authentication is not configured (DATABASE_URL missing)")
		return
	}
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request",
			"invalid JSON body: "+err.Error())
		return
	}

	identity, token, err := s.auth.Register(r.Context(), auth.RegisterInput{
		Email:            req.Email,
		Password:         req.Password,
		DisplayName:      req.DisplayName,
		OrganizationName: req.OrganizationName,
	})
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrEmailTaken):
			writeError(w, r, http.StatusConflict, "email_taken",
				"an account with this email already exists")
		default:
			// Validation errors are surfaced as 400 with the service message.
			if isValidationError(err) {
				writeError(w, r, http.StatusBadRequest, "validation_failed", err.Error())
				return
			}
			s.log.Error("registration failed", "error", err)
			writeError(w, r, http.StatusInternalServerError, "internal",
				"registration failed")
		}
		return
	}

	setSessionCookie(w, token, s.cfg.AppEnv == config.EnvProduction)
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":               identity,
		"session_expires_at": time.Now().Add(7 * 24 * time.Hour).UTC(),
	})
}

func isValidationError(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "a valid email") ||
		strings.HasPrefix(msg, "password must") ||
		strings.HasPrefix(msg, "display name must") ||
		strings.HasPrefix(msg, "organization name must")
}

type loginRequest struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	OrganizationID string `json:"organization_id"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusServiceUnavailable, "auth_unavailable",
			"authentication is not configured (DATABASE_URL missing)")
		return
	}
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request",
			"invalid JSON body: "+err.Error())
		return
	}

	identity, token, err := s.auth.Login(r.Context(), req.Email, req.Password,
		req.OrganizationID, r.UserAgent(), clientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeError(w, r, http.StatusUnauthorized, "invalid_credentials",
				"invalid email or password")
		case errors.Is(err, auth.ErrNotAMember):
			writeError(w, r, http.StatusForbidden, "not_a_member",
				"not a member of the requested organization")
		case errors.Is(err, auth.ErrNoOrganization):
			writeError(w, r, http.StatusForbidden, "no_organization",
				"account has no organization membership")
		default:
			s.log.Error("login failed", "error", err)
			writeError(w, r, http.StatusInternalServerError, "internal", "login failed")
		}
		return
	}

	setSessionCookie(w, token, s.cfg.AppEnv == config.EnvProduction)
	writeJSON(w, http.StatusOK, map[string]any{"user": identity})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusServiceUnavailable, "auth_unavailable",
			"authentication is not configured (DATABASE_URL missing)")
		return
	}
	c, err := r.Cookie(SessionCookie)
	if err == nil {
		if err := s.auth.Logout(r.Context(), c.Value); err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal", "logout failed")
			return
		}
	}
	clearSessionCookie(w, s.cfg.AppEnv == config.EnvProduction)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": identity})
}

type switchOrgRequest struct {
	OrganizationID string `json:"organization_id"`
}

func (s *Server) handleSwitchOrganization(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusServiceUnavailable, "auth_unavailable",
			"authentication is not configured (DATABASE_URL missing)")
		return
	}
	var req switchOrgRequest
	if err := decodeJSON(r, &req); err != nil || req.OrganizationID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request",
			"organization_id is required")
		return
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}

	identity, err := s.auth.SwitchOrganization(r.Context(), c.Value, req.OrganizationID)
	if err != nil {
		if errors.Is(err, auth.ErrNotAMember) {
			writeError(w, r, http.StatusForbidden, "not_a_member",
				"not a member of the requested organization")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "internal", "organization switch failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": identity})
}
