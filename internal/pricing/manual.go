package pricing

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

//go:embed manual-prices.json
var manualPricesJSON []byte

type manualPrice struct {
	Rates     rates
	UpdatedAt time.Time
}

type manualEntry struct {
	Input      *float64 `json:"inputPerMillion"`
	Output     *float64 `json:"outputPerMillion"`
	CacheRead  *float64 `json:"cacheReadPerMillion,omitempty"`
	CacheWrite *float64 `json:"cacheWritePerMillion,omitempty"`
	Reasoning  *float64 `json:"reasoningPerMillion,omitempty"`
	UpdatedAt  string   `json:"updatedAt"`
	SourceURL  string   `json:"sourceURL,omitempty"`
}

func parseManualPrices(body []byte) (map[string]manualPrice, error) {
	var file struct {
		Models map[string]manualEntry `json:"models"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("expected a single JSON object")
	}
	if file.Models == nil {
		return nil, fmt.Errorf("models must be an object")
	}
	prices := make(map[string]manualPrice, len(file.Models))
	for model, entry := range file.Models {
		if model == "" || strings.TrimSpace(model) != model {
			return nil, fmt.Errorf("model IDs must be nonempty and have no surrounding whitespace")
		}
		if entry.Input == nil || entry.Output == nil {
			return nil, fmt.Errorf("%s: inputPerMillion and outputPerMillion are required", model)
		}
		for _, value := range []*float64{entry.Input, entry.Output, entry.CacheRead, entry.CacheWrite, entry.Reasoning} {
			if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0) {
				return nil, fmt.Errorf("%s: prices must be finite and nonnegative", model)
			}
		}
		updated, err := time.Parse("2006-01-02", entry.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("%s: updatedAt must be YYYY-MM-DD", model)
		}
		r := rates{Prompt: *entry.Input / 1e6, Completion: *entry.Output / 1e6}
		r.CacheRead, r.CacheWrite, r.Reasoning = r.Prompt, r.Prompt, r.Completion
		if entry.CacheRead != nil {
			r.CacheRead = *entry.CacheRead / 1e6
		}
		if entry.CacheWrite != nil {
			r.CacheWrite = *entry.CacheWrite / 1e6
		}
		if entry.Reasoning != nil {
			r.Reasoning = *entry.Reasoning / 1e6
		}
		prices[model] = manualPrice{Rates: r, UpdatedAt: updated}
	}
	return prices, nil
}

// LoadManualFile supplements bundled prices. Matching file entries replace
// bundled entries. Call before Run; changes take effect after restarting the app.
func (s *Service) LoadManualFile(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read manual prices: %w", err)
	}
	defer f.Close()
	const maxSize = 1 << 20
	body, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return fmt.Errorf("read manual prices: %w", err)
	}
	if len(body) > maxSize {
		return fmt.Errorf("manual prices exceed 1 MiB")
	}
	entries, err := parseManualPrices(body)
	if err != nil {
		return fmt.Errorf("parse manual prices: %w", err)
	}
	for id, entry := range entries {
		s.manual[id] = entry
	}
	return nil
}
