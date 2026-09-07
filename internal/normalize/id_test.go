package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestDedupIDStable(t *testing.T) {
	a, err := DedupID(SourceCopilot, "abc", "123")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DedupID(SourceCopilot, "abc", "123")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same input produced different IDs: %s vs %s", a, b)
	}
}

func TestDedupIDMatchesSpec(t *testing.T) {
	got, err := DedupID("copilot", "trace1", "span1")
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("copilot|trace1|span1"))
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("DedupID = %s, want sha256hex of spec string", got)
	}
}

func TestDedupIDSourceScopes(t *testing.T) {
	co, _ := DedupID(SourceCopilot, "t", "s")
	oc, _ := DedupID(SourceOpenCode, "t", "s")
	if co == oc {
		t.Error("same trace/span IDs from different sources must not collide")
	}
}

func TestDedupIDEmptyIDs(t *testing.T) {
	for _, tc := range [][2]string{{"", "s"}, {"t", ""}, {"", ""}} {
		if _, err := DedupID("copilot", tc[0], tc[1]); err == nil {
			t.Errorf("DedupID(%q, %q) should fail", tc[0], tc[1])
		}
	}
}

func TestDetectSource(t *testing.T) {
	resource := func(sn string) pcommon.Map {
		m := pcommon.NewMap()
		if sn != "" {
			m.PutStr("service.name", sn)
		}
		return m
	}
	span := pcommon.NewMap()
	if got := DetectSource(resource("copilot-chat"), span); got != SourceCopilot {
		t.Errorf("copilot-chat → %q", got)
	}
	if got := DetectSource(resource("github-copilot"), span); got != SourceCopilot {
		t.Errorf("github-copilot → %q", got)
	}
	if got := DetectSource(resource("opencode"), span); got != SourceOpenCode {
		t.Errorf("opencode → %q", got)
	}
	if got := DetectSource(resource("codex_cli_rs"), span); got != SourceCodex {
		t.Errorf("codex_cli_rs → %q", got)
	}
	if got := DetectSource(resource(""), span); got != "" {
		t.Errorf("unknown → %q, want empty", got)
	}

	fallback := pcommon.NewMap()
	fallback.PutStr("github.copilot.git.branch", "main")
	if got := DetectSource(resource("something-else"), fallback); got != SourceCopilot {
		t.Errorf("github.copilot.* fallback → %q, want copilot", got)
	}
	codexFallback := pcommon.NewMap()
	codexFallback.PutInt("codex.turn.token_usage.input_tokens", 1)
	if got := DetectSource(resource("something-else"), codexFallback); got != SourceCodex {
		t.Errorf("codex.* fallback → %q, want codex", got)
	}
}
