// Package db provides the embedded SQL migration runner for the control
// plane. Migrations are plain SQL files applied in lexicographic order and
// tracked in schema_migrations. Each migration runs inside a transaction and
// a PostgreSQL advisory lock prevents concurrent runners.
package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	migrationspkg "github.com/singhtanishq/DataAtlas/services/control-plane/migrations"
)

// migrationsFS exposes the embedded *.sql files via the migrations package.
var migrationsFS = migrationspkg.FS()

// Migration is a single versioned schema change.
type Migration struct {
	Version int
	Name    string
	UpSQL   string
	DownSQL string
}

type appliedRow struct {
	Version   int
	Name      string
	AppliedAt time.Time
}

// LoadMigrations parses embedded migration files keyed as
// NNN_name.up.sql / NNN_name.down.sql.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	ups := map[int]Migration{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		var version int
		var stem, kind string
		if _, err := fmt.Sscanf(name, "%d_%s", &version, &stem); err != nil {
			return nil, fmt.Errorf("migration filename %q does not match NNN_name.up.sql", name)
		}
		// stem keeps the extension; split ".up.sql"/".down.sql" off.
		switch {
		case strings.HasSuffix(stem, ".up.sql"):
			kind = "up"
			stem = strings.TrimSuffix(stem, ".up.sql")
		case strings.HasSuffix(stem, ".down.sql"):
			kind = "down"
			stem = strings.TrimSuffix(stem, ".down.sql")
		default:
			return nil, fmt.Errorf("migration file %q must end in .up.sql or .down.sql", name)
		}
		content, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		m, ok := ups[version]
		if !ok {
			m = Migration{Version: version, Name: stem}
			ups[version] = m
		}
		switch kind {
		case "up":
			m.UpSQL = string(content)
		case "down":
			m.DownSQL = string(content)
		}
		ups[version] = m
	}

	migrations := make([]Migration, 0, len(ups))
	for _, m := range ups {
		if m.UpSQL == "" {
			return nil, fmt.Errorf("migration %d (%s) has no .up.sql", m.Version, m.Name)
		}
		migrations = append(migrations, m)
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// Migrate applies all pending up migrations. Safe to call repeatedly.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]Migration, error) {
	migrations, err := LoadMigrations()
	if err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}

	var newlyApplied []Migration
	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			continue
		}
		if err := applyMigration(ctx, pool, m); err != nil {
			return newlyApplied, fmt.Errorf("apply migration %03d_%s: %w", m.Version, m.Name, err)
		}
		newlyApplied = append(newlyApplied, m)
	}
	return newlyApplied, nil
}

// Rollback reverts the last n applied migrations.
func Rollback(ctx context.Context, pool *pgxpool.Pool, n int) ([]Migration, error) {
	migrations, err := LoadMigrations()
	if err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}

	// Revert newest first.
	var appliedSorted []Migration
	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			appliedSorted = append(appliedSorted, m)
		}
	}
	sort.Slice(appliedSorted, func(i, j int) bool { return appliedSorted[i].Version > appliedSorted[j].Version })

	var reverted []Migration
	for _, m := range appliedSorted {
		if len(reverted) >= n {
			break
		}
		if strings.TrimSpace(m.DownSQL) == "" {
			return reverted, fmt.Errorf("migration %03d_%s has no down migration", m.Version, m.Name)
		}
		if err := revertMigration(ctx, pool, m); err != nil {
			return reverted, fmt.Errorf("revert migration %03d_%s: %w", m.Version, m.Name, err)
		}
		reverted = append(reverted, m)
	}
	return reverted, nil
}

// Status returns applied and pending migrations.
func Status(ctx context.Context, pool *pgxpool.Pool) (applied []Migration, pending []Migration, err error) {
	migrations, err := LoadMigrations()
	if err != nil {
		return nil, nil, err
	}
	done, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range migrations {
		if _, ok := done[m.Version]; ok {
			applied = append(applied, m)
		} else {
			pending = append(pending, m)
		}
	}
	return applied, pending, nil
}

func ensureTrackingTable(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	return err
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int]appliedRow, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'schema_migrations')`,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return map[int]appliedRow{}, nil
	}
	rows, err := conn.Query(ctx, `SELECT version, name, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int]appliedRow{}
	for rows.Next() {
		var r appliedRow
		if err := rows.Scan(&r.Version, &r.Name, &r.AppliedAt); err != nil {
			return nil, err
		}
		out[r.Version] = r
	}
	return out, rows.Err()
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, m Migration) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Advisory lock serializes concurrent migration runners.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(728382911)`); err != nil {
		return err
	}
	if err := ensureTrackingTable(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, m.UpSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func revertMigration(ctx context.Context, pool *pgxpool.Pool, m Migration) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(728382911)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, m.DownSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, m.Version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
