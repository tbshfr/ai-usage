package normalize

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

var ErrInvalidClaudeCodeAttr = errors.New("invalid Claude Code attribute")

// FromClaudeCodeLog maps one successful API call event to a generation.
// Claude Code's metrics are aggregates, and its prompt, response, tool, and
// lifecycle events do not represent API calls.
func FromClaudeCodeLog(resource pcommon.Map, lr plog.LogRecord) (Generation, bool, error) {
	attrs := lr.Attributes()
	name, _ := attrString(attrs, "event.name")
	if name != "api_request" && name != "claude_code.api_request" {
		return Generation{}, false, nil
	}
	if err := requireStrings(attrs, "model", "session.id", "request_id", "effort"); err != nil {
		return Generation{}, true, err
	}
	model := firstString(attrs, "model")
	if model == "" {
		return Generation{}, true, ErrMissingLogIdentity
	}
	session := firstString(attrs, "session.id")
	timestamp := lr.Timestamp()
	if timestamp == 0 {
		timestamp = lr.ObservedTimestamp()
	}
	if timestamp == 0 {
		return Generation{}, true, ErrInvalidLogTimestamp
	}
	input, err := checkedTokenAttr(attrs, "input_tokens")
	if err != nil {
		return Generation{}, true, err
	}
	output, err := checkedTokenAttr(attrs, "output_tokens")
	if err != nil {
		return Generation{}, true, err
	}
	cacheRead, err := checkedTokenAttr(attrs, "cache_read_tokens")
	if err != nil {
		return Generation{}, true, err
	}
	cacheCreate, err := checkedTokenAttr(attrs, "cache_creation_tokens")
	if err != nil {
		return Generation{}, true, err
	}
	var cost *float64
	if _, exists := attrs.Get("cost_usd"); exists {
		v, valid := attrDouble(attrs, "cost_usd")
		if !valid {
			return Generation{}, true, fmt.Errorf("cost_usd: %w", ErrInvalidClaudeCodeAttr)
		}
		// Zero means Claude Code has no price for the model (for example a
		// gateway model). Leave that unknown for the local pricing catalog.
		if v > 0 {
			cost = &v
		}
	}
	var duration time.Duration
	if ms, err := checkedTokenAttr(attrs, "duration_ms"); err != nil {
		return Generation{}, true, err
	} else if ms != nil {
		duration = time.Duration(*ms) * time.Millisecond
	}

	id, err := claudeCodeID(attrs, session, timestamp)
	if err != nil {
		return Generation{}, true, err
	}
	gen := Generation{
		ID:                  id,
		Timestamp:           timestamp.AsTime().UTC(),
		Source:              SourceClaudeCode,
		ServiceName:         serviceName(resource),
		Provider:            "anthropic",
		Model:               model,
		ConversationID:      session,
		InputTokens:         input,
		OutputTokens:        output,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreate,
		Cost:                cost,
		Duration:            duration,
		ReasoningEffort:     firstString(attrs, "effort"),
	}
	if cost != nil {
		gen.CostReportedByHarness = true
		gen.CostSource = "harness"
	}
	return gen, true, nil
}

// claudeCodeID prefers the API request ID, which is unique per response and
// independent of session-ID export settings. Without it, the session,
// record timestamp, and per-session event.sequence identify the call.
func claudeCodeID(attrs pcommon.Map, session string, timestamp pcommon.Timestamp) (string, error) {
	if requestID := firstString(attrs, "request_id"); requestID != "" {
		return DedupID(SourceClaudeCode, "request", requestID)
	}
	if session == "" {
		return "", ErrMissingLogIdentity
	}
	sequence, err := checkedTokenAttr(attrs, "event.sequence")
	if err != nil {
		return "", err
	}
	if sequence == nil {
		return "", ErrMissingLogIdentity
	}
	return DedupID(SourceClaudeCode, session,
		strconv.FormatUint(uint64(timestamp), 10)+":"+strconv.FormatInt(*sequence, 10))
}
