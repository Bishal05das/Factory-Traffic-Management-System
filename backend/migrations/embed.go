package migrations

import "embed"

// Files are embedded so the compiled server can migrate without a working-directory dependency.
//
//go:embed *.sql
var Files embed.FS
