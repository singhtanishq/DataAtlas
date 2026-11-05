// Package db provides shared database access helpers for the control plane.
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OrgScopeKey is the PostgreSQL custom GUC carrying the tenant context for
// Row-Level Security. It must be set inside every transaction that touches
// organization-owned tables.
const OrgScopeKey = "app.current_organization_id"

// Pool wraps the pgx connection pool.
type Pool struct {
	*pgxpool.Pool
}

// NewPool opens a connection pool.
func NewPool(ctx context.Context, databaseURL string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Pool{pool}, nil
}

// WithOrgAdmin runs fn inside a plain transaction with no tenant GUC.
// Use only for registry/global metadata (e.g. connector definitions) that
// is not organization-scoped.
func (p *Pool) WithOrgAdmin(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WithOrg runs fn inside a transaction whose Row-Level Security tenant
// context is bound to organizationID.
func (p *Pool) WithOrg(ctx context.Context, organizationID string, fn func(pgx.Tx) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, OrgScopeKey, organizationID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
