package normalize

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

var (
	ErrMissingLogIdentity  = errors.New("missing required log identity")
	ErrInvalidLogTimestamp = errors.New("invalid log timestamp")
	ErrInvalidTokenAttr    = errors.New("invalid token attribute")
)

// FromCodexLog maps Codex's terminal SSE usage event to a Generation.
// Prompt and tool-result events are deliberately ignored.
func FromCodexLog(resource pcommon.Map, lr plog.LogRecord) (Generation, bool, error) {
	attrs := lr.Attributes()
	eventName, _ := attrString(attrs, "event.name")
	eventKind, _ := attrString(attrs, "event.kind")
	if eventName != "codex.sse_event" || eventKind != "response.completed" {
		return Generation{}, false, nil
	}
	if err := requireStrings(attrs, "model", "conversation.id", "event.timestamp"); err != nil {
		return Generation{}, true, err
	}

	model := firstString(attrs, "model")
	conversationID := firstString(attrs, "conversation.id")
	if model == "" || conversationID == "" {
		return Generation{}, true, ErrMissingLogIdentity
	}
	timestamp, err := codexLogTimestamp(lr)
	if err != nil {
		return Generation{}, true, err
	}

	input, err := checkedTokenAttr(attrs, "input_token_count")
	if err != nil {
		return Generation{}, true, err
	}
	output, err := checkedTokenAttr(attrs, "output_token_count")
	if err != nil {
		return Generation{}, true, err
	}
	cacheRead, err := checkedFirstTokenAttr(attrs, "cached_token_count", "cached_input_token_count")
	if err != nil {
		return Generation{}, true, err
	}
	cacheCreate, err := checkedFirstTokenAttr(attrs, "cache_write_token_count", "cache_write_input_token_count")
	if err != nil {
		return Generation{}, true, err
	}
	reasoning, err := checkedFirstTokenAttr(attrs, "reasoning_token_count", "reasoning_output_token_count")
	if err != nil {
		return Generation{}, true, err
	}

	id, err := DedupLogID(SourceCodex, conversationID, timestamp, model, input, output, cacheRead, cacheCreate, reasoning)
	if err != nil {
		return Generation{}, true, err
	}
	// Codex only reports the provider on a separate conversation-start
	// event, but the Codex CLI always talks to OpenAI, so derive it here
	// instead of keeping an order-dependent session state (the one source
	// where provider is not a raw reported attribute).
	return Generation{
		ID:                  id,
		Timestamp:           timestamp.UTC(),
		Source:              SourceCodex,
		ServiceName:         serviceName(resource),
		Provider:            "openai",
		Model:               model,
		ConversationID:      conversationID,
		InputTokens:         input,
		OutputTokens:        output,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreate,
		ReasoningTokens:     reasoning,
	}, true, nil
}

func checkedTokenAttr(attrs pcommon.Map, key string) (*int64, error) {
	if _, ok := attrs.Get(key); !ok {
		return nil, nil
	}
	if value, ok := attrInt(attrs, key); ok {
		return &value, nil
	}
	return nil, fmt.Errorf("%s: %w", key, ErrInvalidTokenAttr)
}

// checkedFirstTokenAttr returns the first present alias. The first key is
// authoritative when an exporter sends both names with divergent values.
func checkedFirstTokenAttr(attrs pcommon.Map, keys ...string) (*int64, error) {
	for _, key := range keys {
		if _, ok := attrs.Get(key); ok {
			return checkedTokenAttr(attrs, key)
		}
	}
	return nil, nil
}

func codexLogTimestamp(lr plog.LogRecord) (time.Time, error) {
	if value, ok := lr.Attributes().Get("event.timestamp"); ok {
		if value.Type() != pcommon.ValueTypeStr {
			return time.Time{}, fmt.Errorf("event.timestamp: %w", ErrNonStringAttrs)
		}
		// Strict when present: a corrupt timestamp must error rather than
		// fall back to observed time, or retries would store a different
		// time and DedupLogID and duplicate the row.
		timestamp, err := time.Parse(time.RFC3339Nano, value.Str())
		if err != nil {
			return time.Time{}, fmt.Errorf("event.timestamp %q: %v: %w", value.Str(), err, ErrInvalidLogTimestamp)
		}
		return timestamp, nil
	}
	if lr.Timestamp() != 0 {
		return lr.Timestamp().AsTime(), nil
	}
	if lr.ObservedTimestamp() != 0 {
		return lr.ObservedTimestamp().AsTime(), nil
	}
	return time.Time{}, ErrInvalidLogTimestamp
}
