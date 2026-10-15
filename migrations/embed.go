// Package migrations embeds the numbered SQL migrations so `plumbline-server
// migrate` applies exactly the files it was built with. The .sql files are the
// source of truth and are never edited once merged; add a new numbered file.
package migrations

import "embed"

// Files holds NNNN_description.sql at its root (tests/ is not embedded).
//
//go:embed *.sql
var Files embed.FS
