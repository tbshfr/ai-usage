package assets

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"
)

func TestServePublic(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		for _, target := range []string{"/robots.txt", "/favicon.ico", "/robots.txt?v=1"} {
			rec := httptest.NewRecorder()
			if !ServePublic(rec, httptest.NewRequest(method, target, nil)) || rec.Code != 200 {
				t.Fatalf("%s %s: status %d", method, target, rec.Code)
			}
			if method == "HEAD" && rec.Body.Len() != 0 {
				t.Error("HEAD returned a body")
			}
		}
	}
}

func TestServePublicRejectsUnsafeAndNonFilePaths(t *testing.T) {
	for _, target := range []string{
		"/", "/missing.txt", "/web/public/robots.txt", "/templates/layout.html",
		"/../templates/layout.html", "/%2e%2e/templates/layout.html",
		"/nested/%2e%2e/robots.txt", "/nested%2f..%2frobots.txt",
		"/%252e%252e/templates/layout.html", "/..%5ctemplates%5clayout.html",
		"//robots.txt", "/./robots.txt", "/robots.txt/", "/robots.txt%00",
		"/.env", "/.git/config", "/static/app.css", "/etc/passwd",
	} {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if ServePublic(rec, httptest.NewRequest("GET", target, nil)) {
				t.Error("unexpectedly served path")
			}
			if rec.Body.Len() != 0 {
				t.Error("rejected path returned content")
			}
		})
	}
	rec := httptest.NewRecorder()
	if ServePublic(rec, httptest.NewRequest("POST", "/robots.txt", nil)) {
		t.Error("served POST")
	}
}

func TestServePublicReservedSegments(t *testing.T) {
	original := public
	t.Cleanup(func() { public = original })
	files := fstest.MapFS{}
	public = files
	for _, segment := range []string{
		"api", "static", "login", "logout", "health", "ready", "trends",
		"breakdowns", "sessions", "stats", "generations", "events", "fragments",
		"setup",
	} {
		for _, name := range []string{segment, segment + "/x"} {
			files["web/public/"+name] = &fstest.MapFile{Data: []byte("must not be served")}
			for _, method := range []string{"GET", "HEAD"} {
				rec := httptest.NewRecorder()
				if ServePublic(rec, httptest.NewRequest(method, "/"+name, nil)) {
					t.Errorf("%s /%s: served reserved path", method, name)
				}
				if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
					t.Errorf("%s /%s: rejected path changed response", method, name)
				}
			}
			delete(files, "web/public/"+name)
		}
	}
	files["web/public/api-guide.txt"] = &fstest.MapFile{Data: []byte("public")}
	if !ServePublic(httptest.NewRecorder(), httptest.NewRequest("GET", "/api-guide.txt", nil)) {
		t.Error("rejected non-reserved first segment")
	}
}

func TestServePublicRevalidation(t *testing.T) {
	for _, target := range []string{"/favicon.ico", "/robots.txt"} {
		first := httptest.NewRecorder()
		if !ServePublic(first, httptest.NewRequest("GET", target, nil)) || first.Code != http.StatusOK {
			t.Fatalf("%s: initial request failed", target)
		}
		modified := first.Header().Get("Last-Modified")
		if _, err := http.ParseTime(modified); err != nil {
			t.Fatalf("%s: invalid Last-Modified %q", target, modified)
		}
		for _, method := range []string{"GET", "HEAD"} {
			req := httptest.NewRequest(method, target, nil)
			req.Header.Set("If-Modified-Since", modified)
			rec := httptest.NewRecorder()
			if !ServePublic(rec, req) || rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
				t.Errorf("%s %s: expected empty 304, got %d (%d bytes)", method, target, rec.Code, rec.Body.Len())
			}
			if rec.Header().Get("Cache-Control") != "no-cache" {
				t.Error("revalidation must retain Cache-Control: no-cache")
			}
		}
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("If-Modified-Since", publicModTime.Add(-time.Second).Format(http.TimeFormat))
		rec := httptest.NewRecorder()
		if !ServePublic(rec, req) || rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("%s: stale cache must receive content", target)
		}
	}
}
