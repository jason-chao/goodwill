// Package web holds the dashboard's templates and static files.
package web

import "embed"

//go:embed templates static
var FS embed.FS
