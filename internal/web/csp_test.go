package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

// The CSP allows only 'self', so no page may ship an executable inline
// script: every <script> is either an external file (src=) or a
// type="application/json" data block consumed by app.js.
var scriptTag = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

func TestNoExecutableInlineScripts(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	for _, p := range []string{
		"/?" + fullRangeQuery,
		"/trends?" + fullRangeQuery,
		"/breakdowns?" + fullRangeQuery,
		"/sessions",
		"/fragments/trends?" + fullRangeQuery,
	} {
		_, body := get(t, srv.URL+p)
		for _, m := range scriptTag.FindAllStringSubmatch(body, -1) {
			attrs, content := m[1], m[2]
			if strings.Contains(attrs, "src=") {
				continue
			}
			if !strings.Contains(attrs, `type="application/json"`) {
				t.Errorf("%s: executable inline script %q", p, m[0])
			}
			// Data blocks must hold JSON, not code.
			trimmed := strings.TrimSpace(content)
			if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
				t.Errorf("%s: non-JSON script data block %q", p, trimmed)
			}
		}
	}
}

// The login page's styles live in app.css since the CSP forbids inline
// <style> blocks.
func TestNoInlineStyles(t *testing.T) {
	srv := serverOnDB(t, seedtest.DB(t))
	for _, p := range []string{"/", "/login"} {
		_, body := get(t, srv.URL+p)
		if strings.Contains(body, "<style") {
			t.Errorf("%s: page contains an inline <style> block", p)
		}
	}
	_, body := get(t, srv.URL+"/")
	wantContains(t, body, `{"includeIndicatorCSS":false}`)
}
