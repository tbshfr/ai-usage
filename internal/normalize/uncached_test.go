package normalize

import "testing"

func TestUncachedInput(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }

	// copilot reports the prompt including cached tokens: subtract them.
	g := Generation{
		Source:              SourceCopilot,
		InputTokens:         i64(1000),
		CacheReadTokens:     i64(800),
		CacheCreationTokens: i64(100),
	}
	if u := g.UncachedInput(); u == nil || *u != 100 {
		t.Errorf("copilot uncached = %v, want 100", u)
	}

	// clamp: cache can never exceed the prompt, but guard anyway.
	g.CacheCreationTokens = i64(500)
	if u := g.UncachedInput(); u == nil || *u != 0 {
		t.Errorf("copilot clamped uncached = %v, want 0", u)
	}

	// opencode's prompt count already excludes cache: passthrough.
	o := Generation{
		Source:              SourceOpenCode,
		InputTokens:         i64(136),
		CacheReadTokens:     i64(6912), // telemetry.md Q1 anomaly
		CacheCreationTokens: i64(5),
	}
	if u := o.UncachedInput(); u == nil || *u != 136 {
		t.Errorf("opencode uncached = %v, want 136", u)
	}

	// nil input stays nil.
	if u := (Generation{Source: SourceCopilot}).UncachedInput(); u != nil {
		t.Errorf("nil input = %v, want nil", u)
	}
}
