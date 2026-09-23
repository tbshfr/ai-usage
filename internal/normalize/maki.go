package normalize

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

var ErrInvalidMakiAttr = errors.New("invalid Maki numeric attribute")

func checkedMakiIntAttr(attrs pcommon.Map, key string, max int64) (*int64, error) {
	v, ok := attrs.Get(key)
	if !ok {
		return nil, nil
	}
	var n int64
	switch v.Type() {
	case pcommon.ValueTypeInt:
		n = v.Int()
	case pcommon.ValueTypeStr:
		parsed, err := strconv.ParseInt(v.Str(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, ErrInvalidMakiAttr)
		}
		n = parsed
	case pcommon.ValueTypeDouble:
		d := v.Double()
		if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 || d >= 1<<53 || math.Trunc(d) != d {
			return nil, fmt.Errorf("%s: %w", key, ErrInvalidMakiAttr)
		}
		n = int64(d)
	default:
		return nil, fmt.Errorf("%s: %w", key, ErrInvalidMakiAttr)
	}
	if n < 0 || n > max {
		return nil, fmt.Errorf("%s: %w", key, ErrInvalidMakiAttr)
	}
	return &n, nil
}

// FromMakiLog maps one API call event to a generation. Maki's metrics are
// interval aggregates, and its other log events do not represent API calls.
func FromMakiLog(resource pcommon.Map, lr plog.LogRecord) (Generation, bool, error) {
	attrs := lr.Attributes()
	name, _ := attrString(attrs, "event.name")
	if name != "maki.api_request" {
		return Generation{}, false, nil
	}
	if err := requireStrings(attrs, "model", "provider", "session.id"); err != nil {
		return Generation{}, true, err
	}
	model := firstString(attrs, "model")
	session := firstString(attrs, "session.id")
	if model == "" || session == "" {
		return Generation{}, true, ErrMissingLogIdentity
	}
	sequence, err := checkedMakiIntAttr(attrs, "event.sequence", math.MaxInt64)
	if err != nil {
		return Generation{}, true, err
	}
	if sequence == nil {
		return Generation{}, true, ErrMissingLogIdentity
	}
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
			return Generation{}, true, fmt.Errorf("cost_usd: %w", ErrInvalidMakiAttr)
		}
		// Maki emits zero when its price table has no estimate. Leave that
		// unknown so the local pricing catalog can supply one.
		if v > 0 {
			cost = &v
		}
	}
	var duration time.Duration
	if ms, err := checkedMakiIntAttr(attrs, "duration_ms", math.MaxInt64/int64(time.Millisecond)); err != nil {
		return Generation{}, true, err
	} else if ms != nil {
		duration = time.Duration(*ms) * time.Millisecond
	}

	// Maki resets event.sequence when telemetry initializes, including when
	// a session is resumed. Its record timestamp distinguishes later calls.
	id, err := DedupID(SourceMaki, session,
		strconv.FormatUint(uint64(timestamp), 10)+":"+strconv.FormatInt(*sequence, 10))
	if err != nil {
		return Generation{}, true, err
	}
	gen := Generation{
		ID:                  id,
		Timestamp:           timestamp.AsTime().UTC(),
		Source:              SourceMaki,
		ServiceName:         serviceName(resource),
		Provider:            firstString(attrs, "provider"),
		Model:               model,
		ConversationID:      session,
		InputTokens:         input,
		OutputTokens:        output,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreate,
		Cost:                cost,
		Duration:            duration,
	}
	if cost != nil {
		gen.CostReportedByHarness = true
		gen.CostSource = "harness"
	}
	return gen, true, nil
}
