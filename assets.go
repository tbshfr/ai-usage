// Package assets embeds the repo-root web/ directory (templates + static).
// go:embed cannot reference parent directories, so the directives live here
// at the module root; internal/web consumes the FS from this package.
package assets

import "embed"

//go:embed web/templates/*.html
var Templates embed.FS

//go:embed all:web/static
var Static embed.FS
