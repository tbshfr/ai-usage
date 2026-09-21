// Package pricing enriches missing usage costs using a persistent OpenRouter catalog.
package pricing

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

type rates struct {
	Prompt            float64                      `json:"prompt"`
	Completion        float64                      `json:"completion"`
	CacheRead         float64                      `json:"cache_read"`
	CacheWrite        float64                      `json:"cache_write"`
	Reasoning         float64                      `json:"reasoning"`
	Request           float64                      `json:"request"`
	CacheReadDefault  bool                         `json:"cache_read_default,omitempty"`
	CacheWriteDefault bool                         `json:"cache_write_default,omitempty"`
	ReasoningDefault  bool                         `json:"reasoning_default,omitempty"`
	Overrides         []map[string]json.RawMessage `json:"overrides,omitempty"`
}

type catalog map[string]rates

func parseCatalog(body []byte) (catalog, error) {
	var response struct {
		Data []struct {
			ID      string                     `json:"id"`
			Pricing map[string]json.RawMessage `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	out := catalog{}
	for _, model := range response.Data {
		if model.ID == "" {
			continue
		}
		parse := func(key string, fallback *float64) (float64, error) {
			raw, exists := model.Pricing[key]
			if !exists && fallback != nil {
				return *fallback, nil
			}
			var str string
			if err := json.Unmarshal(raw, &str); err != nil {
				return 0, err
			}
			n, err := strconv.ParseFloat(str, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return 0, fmt.Errorf("invalid price")
			}
			return n, nil
		}
		p, e1 := parse("prompt", nil)
		c, e2 := parse("completion", nil)
		cr, e3 := parse("input_cache_read", &p)
		cw, e4 := parse("input_cache_write", &p)
		reasoning, e5 := parse("internal_reasoning", &c)
		zero := 0.0
		request, e6 := parse("request", &zero)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
			continue
		}
		r := rates{Prompt: p, Completion: c, CacheRead: cr, CacheWrite: cw, Reasoning: reasoning, Request: request}
		_, readSet := model.Pricing["input_cache_read"]
		r.CacheReadDefault = !readSet
		_, writeSet := model.Pricing["input_cache_write"]
		r.CacheWriteDefault = !writeSet
		_, reasoningSet := model.Pricing["internal_reasoning"]
		r.ReasoningDefault = !reasoningSet
		if raw, ok := model.Pricing["overrides"]; ok {
			if err := json.Unmarshal(raw, &r.Overrides); err != nil {
				continue
			}
		}
		out[model.ID] = r
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("catalog contains no valid prices")
	}
	return out, nil
}

// Explicit aliases avoid guessing across model versions and similarly named models.
var aliases = map[string]string{
	"claude-haiku-4-5":           "anthropic/claude-haiku-4.5",
	"claude-haiku-4-5-20251001":  "anthropic/claude-haiku-4.5",
	"claude-sonnet-4-5":          "anthropic/claude-sonnet-4.5",
	"claude-sonnet-4-5-20250929": "anthropic/claude-sonnet-4.5",
	"claude-3-5-sonnet":          "anthropic/claude-3.5-sonnet",
	"claude-3-5-haiku":           "anthropic/claude-3.5-haiku",
	"claude-3-7-sonnet":          "anthropic/claude-3.7-sonnet",
	"claude-sonnet-4":            "anthropic/claude-sonnet-4",
	"claude-opus-4":              "anthropic/claude-opus-4",
}

func (c catalog) match(model string) (string, rates, bool) {
	model = strings.TrimSpace(model)
	if r, ok := c[model]; ok {
		return model, r, true
	}
	if target, ok := aliases[model]; ok {
		if r, found := c[target]; found {
			return target, r, true
		}
	}
	// Only bare IDs may match by their final component. Qualified unknown IDs
	// must not silently switch creator/provider.
	if strings.Contains(model, "/") {
		return "", rates{}, false
	}
	id := ""
	var found rates
	for candidate, r := range c {
		_, bare, ok := strings.Cut(candidate, "/")
		if ok && bare == model {
			if id != "" {
				return "", rates{}, false
			}
			id, found = candidate, r
		}
	}
	return id, found, id != ""
}

func estimate(g normalize.Generation, r rates) *float64 {
	if g.InputTokens == nil || g.OutputTokens == nil {
		return nil
	}
	var ok bool
	r, ok = r.forUsage(g)
	if !ok {
		return nil
	}
	count := func(p *int64) float64 {
		if p == nil {
			return 0
		}
		return float64(*p)
	}
	n := count(g.UncachedInput())*r.Prompt + count(g.CacheReadTokens)*r.CacheRead + count(g.CacheCreationTokens)*r.CacheWrite + count(g.NonReasoningOutput())*r.Completion + count(g.ReasoningTokens)*r.Reasoning + r.Request
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1_000_000 {
		return nil
	}
	return &n
}

func freeModel(model string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "free")
}

// forUsage applies OpenRouter's ordered conditional overrides. Unknown condition
// fields skip the entry, as required by the API's forward-compatibility rules.
func (r rates) forUsage(g normalize.Generation) (rates, bool) {
	for _, override := range r.Overrides {
		if !overrideMatches(override, g) {
			continue
		}
		for key, target := range map[string]*float64{
			"prompt": &r.Prompt, "completion": &r.Completion,
			"input_cache_read": &r.CacheRead, "input_cache_write": &r.CacheWrite,
			"internal_reasoning": &r.Reasoning, "request": &r.Request,
		} {
			raw, exists := override[key]
			if !exists {
				continue
			}
			var str string
			if json.Unmarshal(raw, &str) != nil {
				return r, false
			}
			n, err := strconv.ParseFloat(str, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return r, false
			}
			*target = n
			switch key {
			case "input_cache_read":
				r.CacheReadDefault = false
			case "input_cache_write":
				r.CacheWriteDefault = false
			case "internal_reasoning":
				r.ReasoningDefault = false
			}
		}
	}
	if r.CacheReadDefault {
		r.CacheRead = r.Prompt
	}
	if r.CacheWriteDefault {
		r.CacheWrite = r.Prompt
	}
	if r.ReasoningDefault {
		r.Reasoning = r.Completion
	}
	return r, true
}

func overrideMatches(o map[string]json.RawMessage, g normalize.Generation) bool {
	for key := range o {
		switch key {
		case "min_prompt_tokens", "utc_start", "utc_end", "utc_days",
			"prompt", "completion", "input_cache_read", "input_cache_write", "internal_reasoning", "request",
			"image", "web_search", "input_cache_write_1h":
		default:
			return false
		}
	}
	if raw, ok := o["min_prompt_tokens"]; ok {
		var threshold int64
		if json.Unmarshal(raw, &threshold) != nil || threshold < 0 {
			return false
		}
		prompt := *g.UncachedInput()
		if g.CacheReadTokens != nil {
			prompt += *g.CacheReadTokens
		}
		if g.CacheCreationTokens != nil {
			prompt += *g.CacheCreationTokens
		}
		if prompt <= threshold {
			return false
		}
	}
	if raw, ok := o["utc_days"]; ok {
		var days []string
		if json.Unmarshal(raw, &days) != nil || !slices.Contains(days, strings.ToLower(g.Timestamp.UTC().Weekday().String())) {
			return false
		}
	}
	startRaw, startSet := o["utc_start"]
	endRaw, endSet := o["utc_end"]
	if startSet || endSet {
		var start, end int
		valid := func(n int) bool { return n >= 0 && n < 2400 && n%100 < 60 }
		if !startSet || !endSet || json.Unmarshal(startRaw, &start) != nil || json.Unmarshal(endRaw, &end) != nil || !valid(start) || !valid(end) {
			return false
		}
		t := g.Timestamp.UTC()
		clock := t.Hour()*100 + t.Minute()
		if end > start {
			return clock >= start && clock < end
		}
		return clock >= start || clock < end
	}
	return true
}
