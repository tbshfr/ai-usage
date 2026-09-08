// Package assets embeds the repo-root web/ directory (templates, static, public).
// go:embed cannot reference parent directories, so the directives live here
// at the module root; internal/web consumes the FS from this package.
package assets

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed web/templates/*.html
var Templates embed.FS

//go:embed all:web/static
var Static embed.FS

//go:embed web/public
var publicFiles embed.FS

var public fs.FS = publicFiles

// Reserved first segments belong to the API and dashboard muxes. Public
// files must never shadow these routes, including before authentication.
var reservedSegments = map[string]bool{
	"api": true, "static": true, "login": true, "logout": true,
	"health": true, "ready": true, "trends": true, "breakdowns": true,
	"sessions": true, "stats": true, "generations": true, "events": true,
	"fragments": true,
}

// Embedded files have zero modtimes. Use one timestamp per process so clients
// can revalidate cached public files until the server restarts.
var publicModTime = time.Now().UTC().Truncate(time.Second)

// ServePublic serves existing public files at root URLs and reports whether
// it handled the request. Only the embedded public tree is accessible; no
// host filesystem paths or symlinks are involved. Validate the decoded URL
// before any cleaning, and never expose directories or hidden files.
func ServePublic(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	name, ok := strings.CutPrefix(r.URL.Path, "/")
	if !ok || !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	if reservedSegments[strings.Split(name, "/")[0]] {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	b, err := fs.ReadFile(public, "web/public/"+name)
	if err != nil {
		return false
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, path.Base(name), publicModTime, bytes.NewReader(b))
	return true
}
