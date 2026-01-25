// Package static provides embedded static assets.
package static

import "embed"

//go:embed css/output.css
var FS embed.FS
