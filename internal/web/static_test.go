package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticCaching(t *testing.T) {
	handler := staticHandler()
	for _, name := range []string{"app.css", "app.js", "vendor/htmx.min.js", "vendor/hx-sse.min.js", "vendor/uplot.min.js", "vendor/uplot.min.css"} {
		for _, target := range []string{staticURL(name), "/static/" + name, "/static/" + name + "?v=old"} {
			t.Run(target, func(t *testing.T) {
				first := httptest.NewRecorder()
				handler.ServeHTTP(first, httptest.NewRequest("GET", target, nil))
				if first.Code != http.StatusOK || first.Body.Len() == 0 {
					t.Fatalf("initial response: %d", first.Code)
				}
				wantCache := "no-cache"
				if target == staticURL(name) {
					wantCache = "public, max-age=31536000, immutable"
				}
				if got := first.Header().Get("Cache-Control"); got != wantCache {
					t.Fatalf("cache policy: %q", got)
				}
				etag := first.Header().Get("ETag")
				if etag == "" {
					t.Fatal("missing ETag")
				}
				for _, method := range []string{"GET", "HEAD"} {
					for _, validator := range []string{etag, "W/" + etag, "\"old\", " + etag} {
						req := httptest.NewRequest(method, target, nil)
						req.Header.Set("If-None-Match", validator)
						rec := httptest.NewRecorder()
						handler.ServeHTTP(rec, req)
						if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
							t.Fatalf("%s revalidation: %d, %d bytes", method, rec.Code, rec.Body.Len())
						}
						if rec.Header().Get("Cache-Control") != wantCache {
							t.Fatal("304 lost cache policy")
						}
					}
				}
				req := httptest.NewRequest("GET", target, nil)
				req.Header.Set("If-None-Match", "\"old\"")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
					t.Fatal("stale validator did not receive content")
				}
			})
		}
	}
}

func TestMissingStaticAssetNotImmutable(t *testing.T) {
	rec := httptest.NewRecorder()
	staticHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/static/missing.js?v="+staticHashes["app.js"], nil))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || rec.Header().Get("ETag") != "" {
		t.Fatalf("missing asset response: %d, %v", rec.Code, rec.Header())
	}
}
