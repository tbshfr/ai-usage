package web

import (
	"net/http"
	"testing"
)

func TestSetupPageDefaultsToFirstAgent(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/setup")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, `<a href="/setup" class="active">Setup</a>`)
	wantContains(t, body, `<a class="tab active" href="/setup?agent=claude-code" aria-current="page">Claude Code</a>`)
	wantContains(t, body, `"CLAUDE_CODE_ENABLE_TELEMETRY": "1"`, `href="/sessions?source=claude-code&amp;view=requests"`)
	for _, agent := range setupAgents {
		wantContains(t, body, `href="/setup?agent=`+agent+`"`)
	}
}

func TestSetupPageRendersEveryAgent(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	want := map[string]string{
		"claude-code": `~/.claude/settings.json`,
		"codex":       `<span data-endpoint>http://127.0.0.1:4318</span>/v1/logs`,
		"opencode":    `@devtheops/opencode-plugin-otel`,
		"copilot":     `"github.copilot.chat.otel.enabled": true`,
		"maki":        `maki.setup({`,
	}
	for _, agent := range setupAgents {
		status, body := get(t, srv.URL+"/setup?agent="+agent)
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", agent, status)
		}
		wantContains(t, body, want[agent], `id="setup-receiver"`, `data-copy-snippet`)
		wantContains(t, body, `href="/setup?agent=`+agent+`" aria-current="page"`)
	}
	if len(want) != len(setupAgents) {
		t.Fatalf("test covers %d agents, page lists %d", len(want), len(setupAgents))
	}
}

func TestSetupPageRejectsUnknownAgent(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/setup?agent=<script>")
	if status != http.StatusBadRequest {
		t.Fatalf("status %d", status)
	}
	wantNotContains(t, body, "<script>")
}
