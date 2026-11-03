// Package migrations embeds the SQL migration files so the runner (and any
// binary that needs them) can load them without reading the filesystem.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the embedded migration scripts rooted at the SQL directory.
func FS() fs.FS {
	return files
}
