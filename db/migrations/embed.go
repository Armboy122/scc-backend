// Package migrations exposes the immutable, versioned SQL migration bundle.
package migrations

import "embed"

// Files contains every forward-only migration compiled into scc-migrate.
//
//go:embed *.sql
var Files embed.FS
