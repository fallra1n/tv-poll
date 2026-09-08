// Package migrations embeds the goose SQL migrations into the binary so
// `cmd/migrate` doesn't need the source tree at runtime (e.g. inside the
// Docker image).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
