package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func settingsResponse(t *testing.T, srv *httptest.Server, cookie string) storage.DashboardSettings {
	t.Helper()
	status, body, resp := do(t, srv, "GET", "/settings/preferences", "", map[string]string{"Cookie": cookie})
	if status != http.StatusOK {
		t.Fatalf("settings: %d %s", status, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("settings must not be cached")
	}
	var settings storage.DashboardSettings
	if err := json.Unmarshal([]byte(body), &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestSettingsSharedAcrossSessions(t *testing.T) {
	srv, _ := newAuthedServer(t)
	_, first := login(t, srv, "admin", "s3cret")
	_, second := login(t, srv, "admin", "s3cret")
	a, b := sessionCookie(t, first), sessionCookie(t, second)
	defaults := settingsResponse(t, srv, b)
	if defaults.Appearance.Theme != "system" || defaults.Appearance.Color != "blue" || len(defaults.Breakdowns.Order) != 5 || len(defaults.Breakdowns.Hidden) != 0 || defaults.Setup.Endpoint != "" {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	updates := []struct{ section, body, cookie string }{
		{"appearance", `{"theme":"dark","color":"violet"}`, a},
		{"breakdowns", `{"order":["token","group","source","model","provider"],"hidden":["source"]}`, b},
		{"setup", `{"endpoint":"https://receiver.example","auth":true}`, a},
	}
	for _, update := range updates {
		status, body, _ := do(t, srv, "PUT", "/settings/preferences/"+update.section, update.body, map[string]string{"Cookie": update.cookie, "Content-Type": "application/json"})
		if status != http.StatusNoContent {
			t.Fatalf("save %s: %d %s", update.section, status, body)
		}
	}
	for _, cookie := range []string{a, b} {
		settings := settingsResponse(t, srv, cookie)
		if settings.Appearance.Theme != "dark" || settings.Appearance.Color != "violet" || settings.Breakdowns.Order[0] != "token" || len(settings.Breakdowns.Hidden) != 1 || settings.Breakdowns.Hidden[0] != "source" || settings.Setup.Endpoint != "https://receiver.example" || !settings.Setup.Auth {
			t.Fatalf("settings were not shared or unrelated sections were overwritten: %+v", settings)
		}
	}
	_, body, _ := do(t, srv, "GET", "/settings", "", map[string]string{"Cookie": b})
	wantContains(t, body, staticURL("settings.js"), "shared across devices")
	_, body, _ = do(t, srv, "GET", "/login", "", nil)
	wantContains(t, body, `data-color="neutral"`, `data-theme="light"`, staticURL("theme.js"))
	wantNotContains(t, body, "settings.js", "app.js", "receiver.example", "violet", "/settings/preferences")
}

func TestSettingsRequireAuthentication(t *testing.T) {
	srv, _ := newAuthedServer(t)
	for _, req := range []struct{ method, path string }{
		{"GET", "/settings/preferences"},
		{"PUT", "/settings/preferences/appearance"},
		{"PUT", "/settings/preferences/breakdowns"},
		{"PUT", "/settings/preferences/setup"},
	} {
		status, _, resp := do(t, srv, req.method, req.path, `{"theme":"dark","color":"rose"}`, nil)
		if status != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
			t.Fatalf("unprotected %s %s: %d", req.method, req.path, status)
		}
	}
	_, resp := login(t, srv, "admin", "s3cret")
	if settingsResponse(t, srv, sessionCookie(t, resp)).Appearance.Color != "blue" {
		t.Fatal("unauthenticated write changed preferences")
	}
}

func TestSettingsRejectInvalidAndCrossOriginUpdates(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	for _, req := range []struct{ section, body string }{
		{"appearance", `{"theme":"dark","color":"invalid"}`},
		{"appearance", `{"theme":"invalid","color":"green"}`},
		{"appearance", `{"theme":"light","color":"blue","extra":true}`},
		{"appearance", `{"theme":"light","color":"blue"} {}`},
		{"appearance", `null`},
		{"appearance", "{"},
		{"breakdowns", `{"order":["model","retired"],"hidden":[]}`},
		{"breakdowns", `{"order":["provider","model","source","group","group"],"hidden":[]}`},
		{"breakdowns", `{"order":["provider","model","source","group","token"],"hidden":["invalid"]}`},
		{"breakdowns", `{"order":["provider","model","source","group","token"],"hidden":["model","model"]}`},
		{"setup", `{"endpoint":"` + strings.Repeat("x", 9000) + `","auth":true}`},
	} {
		status, body, _ := do(t, srv, "PUT", "/settings/preferences/"+req.section, req.body, nil)
		if status != http.StatusBadRequest {
			t.Errorf("invalid %s: %d %s", req.section, status, body)
		}
	}
	status, _, _ := do(t, srv, "PUT", "/settings/preferences/appearance", `{"theme":"dark","color":"rose"}`, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://attacker.example"})
	if status != http.StatusForbidden {
		t.Fatalf("cross-origin write: %d", status)
	}
	if settingsResponse(t, srv, "").Appearance.Color != "blue" {
		t.Fatal("rejected write changed preferences")
	}
}

func TestSettingsSurviveServerAndDatabaseRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	open := func() (*httptest.Server, *sql.DB) {
		db, err := storage.Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Migrate(db, nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		srv := httptest.NewServer(New(db, nil, nil, nil, "test"))
		t.Cleanup(srv.Close)
		return srv, db
	}
	srv, db := open()
	status, body, _ := do(t, srv, "PUT", "/settings/preferences/appearance", `{"theme":"dark","color":"orange"}`, nil)
	if status != http.StatusNoContent {
		t.Fatalf("save: %d %s", status, body)
	}
	srv.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, _ := open()
	settings := settingsResponse(t, restarted, "")
	if settings.Appearance.Theme != "dark" || settings.Appearance.Color != "orange" {
		t.Fatalf("settings lost after restart: %+v", settings)
	}
}

func TestSetupSettingsValidateEndpoint(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	for _, endpoint := range []string{"", "http://localhost:4318", "https://receiver.example/otlp", "http://[::1]:4318", "https://receiver.example/a%22b"} {
		body, _ := json.Marshal(storage.SetupSettings{Endpoint: endpoint, Auth: true})
		status, response, _ := do(t, srv, "PUT", "/settings/preferences/setup", string(body), nil)
		if status != http.StatusNoContent {
			t.Fatalf("valid endpoint %q: %d %s", endpoint, status, response)
		}
	}
	before := settingsResponse(t, srv, "").Setup
	for _, endpoint := range []string{
		"receiver.example:4318", "/relative", "https://", "file:///tmp/receiver",
		"https://receiver.example/a b", "https://receiver.example/a\nb",
		"https://receiver.example/\x00", "https://receiver.example/\x7f",
		`https://receiver.example/\path`,
		"https://user:secret@receiver.example",
		`https://receiver.example/", injected = os.execute('id'), ignored = "`,
		"https://receiver.example/" + strings.Repeat("x", 2048),
	} {
		body, _ := json.Marshal(storage.SetupSettings{Endpoint: endpoint})
		status, response, _ := do(t, srv, "PUT", "/settings/preferences/setup", string(body), nil)
		if status != http.StatusBadRequest {
			t.Errorf("invalid endpoint %q: %d %s", endpoint, status, response)
		}
	}
	status, _, _ := do(t, srv, "PUT", "/settings/preferences/setup", " null \n", nil)
	if status != http.StatusBadRequest {
		t.Errorf("null setup: %d", status)
	}
	if after := settingsResponse(t, srv, "").Setup; after != before {
		t.Errorf("invalid update changed setup: before %+v, after %+v", before, after)
	}
}

func TestPagesRenderSavedSettings(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	endpoint := "https://receiver.example/</script><script>alert(1)</script>?a=1&b=2"
	setup, _ := json.Marshal(storage.SetupSettings{Endpoint: endpoint, Auth: true})
	for section, body := range map[string]string{
		"appearance": `{"theme":"dark","color":"rose"}`,
		"breakdowns": `{"order":["token","group","source","model","provider"],"hidden":["model","provider"]}`,
		"setup":      string(setup),
	} {
		status, response, _ := do(t, srv, "PUT", "/settings/preferences/"+section, body, nil)
		if status != http.StatusNoContent {
			t.Fatalf("save %s: %d %s", section, status, response)
		}
	}
	for _, path := range []string{"/", "/trends", "/breakdowns", "/sessions", "/settings", "/settings/setup", "/settings/stats", "/settings/tokens"} {
		t.Run(path, func(t *testing.T) {
			status, body, response := do(t, srv, "GET", path, "", nil)
			if status != http.StatusOK {
				t.Fatalf("page: %d %s", status, body)
			}
			if response.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("pages with shared preferences must not be cached")
			}
			wantContains(t, body, `<html lang="en" data-theme="dark" data-color="rose">`,
				`data-theme-value="dark" aria-pressed="true"`, `data-color-value="rose" aria-pressed="true"`)
			const opening = `<script type="application/json" id="dashboard-settings">`
			_, rest, ok := strings.Cut(body, opening)
			if !ok {
				t.Fatal("missing embedded settings")
			}
			raw, _, ok := strings.Cut(rest, "</script>")
			var settings storage.DashboardSettings
			if !ok || json.Unmarshal([]byte(raw), &settings) != nil {
				t.Fatalf("invalid embedded settings: %s", raw)
			}
			if settings.Appearance.Theme != "dark" || settings.Appearance.Color != "rose" || settings.Setup.Endpoint != endpoint || !settings.Setup.Auth {
				t.Fatalf("wrong embedded settings: %+v", settings)
			}
			wantNotContains(t, raw, "<", ">", "&")
			if strings.Index(body, opening) > strings.Index(body, staticURL("settings.js")) ||
				strings.Index(body, staticURL("settings.js")) > strings.Index(body, staticURL("theme.js")) ||
				strings.Index(body, staticURL("theme.js")) > strings.Index(body, `<link rel="stylesheet"`) {
				t.Fatal("settings and theme must initialize before styles can paint")
			}
		})
	}
	for _, path := range []string{"/breakdowns", "/fragments/breakdowns", "/settings"} {
		_, body, _ := do(t, srv, "GET", path, "", nil)
		attribute := "data-breakdown"
		if path == "/settings" {
			attribute = "data-breakdown-preference"
			wantContains(t, body, `data-breakdown-visible="token" checked`, `data-breakdown-visible="model">`)
		} else {
			wantContains(t, body, `<section data-breakdown="model" hidden>`, `<section data-breakdown="provider" hidden>`)
		}
		previous := -1
		for _, id := range []string{"token", "group", "source", "model", "provider"} {
			index := strings.Index(body, attribute+`="`+id+`"`)
			if index <= previous {
				t.Fatalf("%s: %s not in saved order", path, id)
			}
			previous = index
		}
	}
}

func TestSettingsUnknownSectionAndNormalization(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	if status, _, _ := do(t, srv, "PUT", "/settings/preferences/unknown", `{}`, nil); status != http.StatusNotFound {
		t.Fatalf("unknown section: %d", status)
	}
	for section, body := range map[string]string{
		"setup":      `{"endpoint":"  https://receiver.example  ","auth":true}`,
		"breakdowns": `{"order":["token","group"]}`,
	} {
		if status, response, _ := do(t, srv, "PUT", "/settings/preferences/"+section, body, nil); status != http.StatusNoContent {
			t.Fatalf("save %s: %d %s", section, status, response)
		}
	}
	settings := settingsResponse(t, srv, "")
	wantOrder := []string{"token", "group", "provider", "model", "source"}
	if settings.Setup.Endpoint != "https://receiver.example" || !slices.Equal(settings.Breakdowns.Order, wantOrder) || settings.Breakdowns.Hidden == nil {
		t.Fatalf("settings not normalized: %+v", settings)
	}
}

func TestPagesFallBackFromInvalidStoredSettings(t *testing.T) {
	db := seedtest.DB(t)
	srv := newServerFromDB(t, db)
	defer srv.Close()
	for section, value := range map[string]string{
		"appearance": `{"theme":"dark","color":"rose","density":"compact"}`,
		"breakdowns": `{"order":["retired"],"hidden":[]}`,
		"setup":      `{"endpoint":"https://receiver.example/\"","auth":true}`,
		"retired":    `{}`,
	} {
		if _, err := db.Exec(`INSERT INTO dashboard_settings(section, value) VALUES (?, ?)`, section, value); err != nil {
			t.Fatal(err)
		}
	}
	status, body, _ := do(t, srv, "GET", "/settings", "", nil)
	if status != http.StatusOK {
		t.Fatalf("page: %d %s", status, body)
	}
	wantContains(t, body, `data-theme="dark" data-color="rose"`, `data-settings-toast`, `data-breakdown-preference=`)
	defaults := storage.DefaultDashboardSettings()
	settings := settingsResponse(t, srv, "")
	if settings.Appearance.Color != "rose" || !slices.Equal(settings.Breakdowns.Order, defaults.Breakdowns.Order) || settings.Setup != defaults.Setup {
		t.Fatalf("invalid rows should fall back per section: %+v", settings)
	}
}

func TestSettingOptionsHaveLabels(t *testing.T) {
	for _, id := range storage.Themes {
		if option := themeLabels[id]; option.Label == "" || option.Icon == "" {
			t.Errorf("theme %q has no label or icon", id)
		}
	}
	for _, id := range storage.AccentColors {
		if colorLabels[id] == "" {
			t.Errorf("accent color %q has no label", id)
		}
	}
	for _, id := range storage.BreakdownIDs {
		if breakdownLabels[id] == "" {
			t.Errorf("breakdown %q has no label", id)
		}
	}
}
