package sources

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

// builtinConnectors are the connector definitions that ship with DataAtlas.
// They are registry metadata (not tenant data) and are upserted idempotently
// at startup so a fresh database can immediately register sources.
var builtinConnectors = []struct {
	Key         string
	DisplayName string
	Platform    string
}{
	{"postgres", "PostgreSQL", "postgresql"},
	{"csv_file", "CSV File", "file"},
	{"parquet_file", "Parquet File", "file"},
}

// SeedConnectorDefinitions upserts the built-in connector definitions.
func SeedConnectorDefinitions(ctx context.Context, pool *db.Pool) error {
	return pool.WithOrgAdmin(ctx, func(tx pgx.Tx) error {
		for _, c := range builtinConnectors {
			if _, err := tx.Exec(ctx, `
				INSERT INTO connector_definitions (key, display_name, platform)
				VALUES ($1, $2, $3)
				ON CONFLICT (key) DO UPDATE SET
					display_name = EXCLUDED.display_name,
					platform = EXCLUDED.platform,
					updated_at = now()`,
				c.Key, c.DisplayName, c.Platform); err != nil {
				return err
			}
		}
		return nil
	})
}
