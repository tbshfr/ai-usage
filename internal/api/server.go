package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tbshfr/ai-usage/internal/ingest"
	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

const (
	defaultGenerationLimit = 50
	maxGenerationLimit     = 500
)

// StatsFunc returns a snapshot of the ingestion counters shown by
// GET /api/stats. It comes from the ingest pipeline in main.
type StatsFunc func() ingest.Stats

func apiRoutes(db *sql.DB, stats StatsFunc) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/summary", func(w http.ResponseWriter, r *http.Request) {
		f, ok := filterParam(w, r)
		if !ok {
			return
		}
		s, err := storage.Summary(r.Context(), db, f)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, summaryResponse{
			Filter:              filterEchoOf(r, f),
			Requests:            s.Requests,
			InputTokens:         s.InputTokens,
			OutputTokens:        s.OutputTokens,
			CacheReadTokens:     s.CacheReadTokens,
			CacheCreationTokens: s.CacheCreationTokens,
			CacheHitRate:        s.CacheHitRate(),
			ReasoningTokens:     s.ReasoningTokens,
			CostKnownCount:      s.CostKnownCount,
			CostTotal:           s.CostTotal,
			CostUnknownCount:    s.CostUnknownCount,
		})
	})
	mux.HandleFunc("GET /api/timeseries", func(w http.ResponseWriter, r *http.Request) {
		bucket := storage.Bucket(r.URL.Query().Get("bucket"))
		if bucket == "" {
			bucket = storage.BucketDay
		}
		switch bucket {
		case storage.BucketDay, storage.BucketWeek, storage.BucketMonth:
		default:
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid bucket %q (want day, week, or month)", bucket))
			return
		}
		f, ok := filterParam(w, r)
		if !ok {
			return
		}
		pts, err := storage.Timeseries(r.Context(), db, f, bucket)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]timeseriesPoint, 0, len(pts))
		for _, p := range pts {
			out = append(out, timeseriesPoint{
				BucketStart:         time.UnixMilli(p.BucketStart).UTC().Format(time.RFC3339),
				Requests:            p.Requests,
				InputTokens:         p.InputTokens,
				OutputTokens:        p.OutputTokens,
				CacheReadTokens:     p.CacheReadTokens,
				CacheCreationTokens: p.CacheCreationTokens,
				ReasoningTokens:     p.ReasoningTokens,
				CostKnownCount:      p.CostKnownCount,
				CostTotal:           p.CostTotal,
			})
		}
		writeJSON(w, http.StatusOK, out)
	})
	for _, route := range []struct {
		path   string
		column string
	}{
		{"/api/sources", "source"},
		{"/api/providers", "provider"},
		{"/api/models", "model"},
	} {
		mux.HandleFunc("GET "+route.path, func(w http.ResponseWriter, r *http.Request) {
			f, ok := filterParam(w, r)
			if !ok {
				return
			}
			rows, err := breakdownOf(r.Context(), db, f, route.column)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, rows)
		})
	}
	mux.HandleFunc("GET /api/generations", func(w http.ResponseWriter, r *http.Request) {
		f, ok := filterParam(w, r)
		if !ok {
			return
		}
		limit, offset, ok := pageParams(w, r)
		if !ok {
			return
		}
		gens, err := storage.RecentGenerations(r.Context(), db, f, limit, offset)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]generation, 0, len(gens))
		for _, g := range gens {
			out = append(out, generationJSON(g))
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /api/generations/{id}", func(w http.ResponseWriter, r *http.Request) {
		g, found, err := storage.GenerationByID(r.Context(), db, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("generation %q not found", r.PathValue("id")))
			return
		}
		writeJSON(w, http.StatusOK, generationJSON(*g))
	})
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		var s ingest.Stats
		if stats != nil {
			s = stats()
		}
		writeJSON(w, http.StatusOK, statsResponse{
			Received:            s.Received,
			Normalized:          s.Normalized,
			Stored:              s.Stored,
			Deduplicated:        s.Deduplicated,
			Rejected:            s.Rejected,
			NormalizationErrors: s.NormalizationErrors,
			IngestionErrors:     s.IngestionErrors,
		})
	})
	return mux
}

func breakdownOf(ctx context.Context, db *sql.DB, f storage.Filter, column string) ([]breakdownRow, error) {
	var rows []storage.Breakdown
	var err error
	switch column {
	case "source":
		rows, err = storage.BySource(ctx, db, f)
	case "provider":
		rows, err = storage.ByProvider(ctx, db, f)
	default:
		rows, err = storage.ByModel(ctx, db, f)
	}
	if err != nil {
		return nil, err
	}
	out := make([]breakdownRow, 0, len(rows))
	for _, b := range rows {
		out = append(out, breakdownRow{
			Key:                 b.Key,
			Requests:            b.Requests,
			InputTokens:         b.InputTokens,
			OutputTokens:        b.OutputTokens,
			CacheReadTokens:     b.CacheReadTokens,
			CacheCreationTokens: b.CacheCreationTokens,
			CacheHitRate:        b.CacheHitRate(),
			ReasoningTokens:     b.ReasoningTokens,
			CostKnownCount:      b.CostKnownCount,
			CostUnknownCount:    b.CostUnknownCount,
			CostTotal:           b.CostTotal,
		})
	}
	return out, nil
}

// filterParam parses the shared query parameters into a storage.Filter and
// writes a 400 response itself when they are invalid.
func filterParam(w http.ResponseWriter, r *http.Request) (storage.Filter, bool) {
	q := r.URL.Query()
	f := storage.Filter{
		Source:       q.Get("source"),
		Provider:     q.Get("provider"),
		Model:        q.Get("model"),
		Conversation: q.Get("conversation"),
	}
	var err error
	if f.From, err = timeParam(q.Get("from"), "from"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return f, false
	}
	if f.To, err = timeParam(q.Get("to"), "to"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return f, false
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid filter: to (%s) before from (%s)", q.Get("to"), q.Get("from")))
		return f, false
	}
	return f, true
}

func timeParam(v, name string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid %s %q (want RFC3339 or YYYY-MM-DD)", name, v)
}

func pageParams(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	q := r.URL.Query()
	limit = defaultGenerationLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid limit %q (want a positive integer)", v))
			return 0, 0, false
		}
		limit = min(n, maxGenerationLimit)
	}
	offset = 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid offset %q (want a non-negative integer)", v))
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

func filterEchoOf(r *http.Request, f storage.Filter) filterEcho {
	from, to := "", ""
	if !f.From.IsZero() {
		from = f.From.Format(time.RFC3339)
	}
	if !f.To.IsZero() {
		to = f.To.Format(time.RFC3339)
	}
	return filterEcho{
		From:         from,
		To:           to,
		Source:       r.URL.Query().Get("source"),
		Provider:     r.URL.Query().Get("provider"),
		Model:        r.URL.Query().Get("model"),
		Conversation: r.URL.Query().Get("conversation"),
	}
}

type filterEcho struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Source       string `json:"source"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Conversation string `json:"conversation"`
}

type summaryResponse struct {
	Filter              filterEcho `json:"filter"`
	Requests            int64      `json:"requests"`
	InputTokens         int64      `json:"inputTokens"`
	OutputTokens        int64      `json:"outputTokens"`
	CacheReadTokens     int64      `json:"cacheReadTokens"`
	CacheCreationTokens int64      `json:"cacheCreationTokens"`
	CacheHitRate        *float64   `json:"cacheHitRate"`
	ReasoningTokens     int64      `json:"reasoningTokens"`
	CostKnownCount      int64      `json:"costKnownCount"`
	CostTotal           *float64   `json:"costTotal"`
	CostUnknownCount    int64      `json:"costUnknownCount"`
}

type timeseriesPoint struct {
	BucketStart         string   `json:"bucketStart"`
	Requests            int64    `json:"requests"`
	InputTokens         int64    `json:"inputTokens"`
	OutputTokens        int64    `json:"outputTokens"`
	CacheReadTokens     int64    `json:"cacheReadTokens"`
	CacheCreationTokens int64    `json:"cacheCreationTokens"`
	ReasoningTokens     int64    `json:"reasoningTokens"`
	CostKnownCount      int64    `json:"costKnownCount"`
	CostTotal           *float64 `json:"costTotal"`
}

type breakdownRow struct {
	Key                 string   `json:"key"`
	Requests            int64    `json:"requests"`
	InputTokens         int64    `json:"inputTokens"`
	OutputTokens        int64    `json:"outputTokens"`
	CacheReadTokens     int64    `json:"cacheReadTokens"`
	CacheCreationTokens int64    `json:"cacheCreationTokens"`
	CacheHitRate        *float64 `json:"cacheHitRate"`
	ReasoningTokens     int64    `json:"reasoningTokens"`
	CostKnownCount      int64    `json:"costKnownCount"`
	CostUnknownCount    int64    `json:"costUnknownCount"`
	CostTotal           *float64 `json:"costTotal"`
}

type generation struct {
	ID                  string   `json:"id"`
	Timestamp           string   `json:"timestamp"`
	Source              string   `json:"source"`
	ServiceName         string   `json:"serviceName"`
	Provider            string   `json:"provider"`
	Model               string   `json:"model"`
	InputTokens         *int64   `json:"inputTokens"`
	OutputTokens        *int64   `json:"outputTokens"`
	CacheReadTokens     *int64   `json:"cacheReadTokens"`
	CacheCreationTokens *int64   `json:"cacheCreationTokens"`
	ReasoningTokens     *int64   `json:"reasoningTokens"`
	Cost                *float64 `json:"cost"`
	ConversationID      string   `json:"conversationId"`
	TraceID             string   `json:"traceId"`
	SpanID              string   `json:"spanId"`
	DurationMS          *int64   `json:"durationMs"`
	AgentName           string   `json:"agentName"`
	GitRepo             string   `json:"gitRepo"`
	GitBranch           string   `json:"gitBranch"`
}

func generationJSON(g normalize.Generation) generation {
	var durationMS *int64
	if g.Duration != 0 {
		ms := g.Duration.Milliseconds()
		durationMS = &ms
	}
	return generation{
		ID:                  g.ID,
		Timestamp:           g.Timestamp.UTC().Format(time.RFC3339),
		Source:              g.Source,
		ServiceName:         g.ServiceName,
		Provider:            g.Provider,
		Model:               g.Model,
		InputTokens:         g.InputTokens,
		OutputTokens:        g.OutputTokens,
		CacheReadTokens:     g.CacheReadTokens,
		CacheCreationTokens: g.CacheCreationTokens,
		ReasoningTokens:     g.ReasoningTokens,
		Cost:                g.Cost,
		ConversationID:      g.ConversationID,
		TraceID:             g.TraceID,
		SpanID:              g.SpanID,
		DurationMS:          durationMS,
		AgentName:           g.AgentName,
		GitRepo:             g.GitRepo,
		GitBranch:           g.GitBranch,
	}
}

type statsResponse struct {
	Received            uint64 `json:"received"`
	Normalized          uint64 `json:"normalized"`
	Stored              uint64 `json:"stored"`
	Deduplicated        uint64 `json:"deduplicated"`
	Rejected            uint64 `json:"rejected"`
	NormalizationErrors uint64 `json:"normalizationErrors"`
	IngestionErrors     uint64 `json:"ingestionErrors"`
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg, "status": code})
}
