package web

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

func TestAllTemplatesRender(t *testing.T) {
	data := &pageData{
		Title: "Test", Version: "dev",
		D:       &normalize.Generation{},
		Detail:  &periodDetailView{},
		Reasons: &reasonsDetailView{},
	}
	for name, tmpl := range pageTmpls {
		t.Run("page/"+name, func(t *testing.T) {
			var body bytes.Buffer
			if err := tmpl.ExecuteTemplate(&body, "layout", data); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, tmpl := range fragTmpls {
		t.Run("fragment/"+name, func(t *testing.T) {
			var body bytes.Buffer
			if err := tmpl.ExecuteTemplate(&body, name, data); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRenderFailureDiscardsPartialOutput(t *testing.T) {
	s := &server{}
	for _, kind := range []string{"page", "fragment", "login"} {
		t.Run(kind, func(t *testing.T) {
			templates, name, entry := pageTmpls, "dashboard", "layout"
			if kind == "fragment" {
				templates, name, entry = fragTmpls, "dashboard-stats", "dashboard-stats"
			}
			if kind == "login" {
				name = "login"
			}
			original := templates[name]
			t.Cleanup(func() { templates[name] = original })
			templates[name] = template.Must(template.New(entry).Funcs(funcs).Parse(
				`partial page {{staticURL "missing-asset.css"}} trailing page`))
			rec := httptest.NewRecorder()
			switch kind {
			case "page":
				s.render(rec, name, &pageData{})
			case "fragment":
				s.renderFrag(rec, name, &pageData{})
			case "login":
				s.renderLoginError(rec, http.StatusUnauthorized, "Wrong credentials")
			}
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			if rec.Body.String() != "internal error\n" {
				t.Fatalf("unexpected body: %q", rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Fatalf("content type: %q", got)
			}
		})
	}
}

func TestRenderLoginErrorPreservesStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		rec := httptest.NewRecorder()
		(&server{}).renderLoginError(rec, status, "Try again")
		if rec.Code != status {
			t.Fatalf("status = %d, want %d", rec.Code, status)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("Try again")) {
			t.Fatal("missing login error")
		}
		if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatal("missing HTML content type")
		}
	}
}
