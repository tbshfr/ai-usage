package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/ingest"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newServer(t *testing.T, db *sql.DB, stats StatsFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(db, testLogger(t), stats, "test"))
	t.Cleanup(srv.Close)
	return srv
}

const fullRangeQuery = "from=2024-01-01&to=2026-04-01"

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestSummaryEndpoint(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	status, body := get(t, srv.URL+"/api/summary?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	var got struct {
		Filter struct {
			From     string `json:"from"`
			To       string `json:"to"`
			Source   string `json:"source"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"filter"`
		Requests            int64    `json:"requests"`
		InputTokens         int64    `json:"inputTokens"`
		OutputTokens        int64    `json:"outputTokens"`
		CacheReadTokens     int64    `json:"cacheReadTokens"`
		CacheCreationTokens int64    `json:"cacheCreationTokens"`
		ReasoningTokens     int64    `json:"reasoningTokens"`
		CostKnownCount      int64    `json:"costKnownCount"`
		CostTotal           *float64 `json:"costTotal"`
		CostUnknownCount    int64    `json:"costUnknownCount"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Requests != 20 || got.InputTokens != 1987 || got.OutputTokens != 1239 ||
		got.CacheReadTokens != 410 || got.CacheCreationTokens != 56 || got.ReasoningTokens != 35 {
		t.Errorf("totals mismatch: %+v", got)
	}
	if got.CostKnownCount != 8 || got.CostUnknownCount != 12 {
		t.Errorf("cost counts = %d/%d, want 8/12", got.CostKnownCount, got.CostUnknownCount)
	}
	if got.CostTotal == nil || *got.CostTotal < 2.849 || *got.CostTotal > 2.851 {
		t.Errorf("CostTotal = %v, want ~2.85", got.CostTotal)
	}
	if got.Filter.From != "2024-01-01T00:00:00Z" || got.Filter.To != "2026-04-01T00:00:00Z" || got.Filter.Source != "" {
		t.Errorf("filter echo = %+v", got.Filter)
	}
}

func TestSummaryFilterNarrows(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	_, body := get(t, srv.URL+"/api/summary?"+fullRangeQuery+"&source=opencode")
	var got struct {
		Requests       int64    `json:"requests"`
		CostTotal      *float64 `json:"costTotal"`
		CostKnownCount int64    `json:"costKnownCount"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Requests != 8 {
		t.Errorf("requests = %d, want 8 (opencode rows only)", got.Requests)
	}
	if got.CostKnownCount != 8 {
		t.Errorf("costKnownCount = %d, want 8", got.CostKnownCount)
	}

	_, body = get(t, srv.URL+"/api/summary?"+fullRangeQuery+"&provider=github&model=gpt-4.1")
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Requests != 7 {
		t.Errorf("requests = %d, want 7 (gpt-4.1 rows)", got.Requests)
	}
}

func TestSummaryBadFilters(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	for _, tc := range []struct{ name, query string }{
		{"bad from", "from=not-a-date"},
		{"bad to", "to=31-12-2026"},
		{"to before from", "from=2026-04-01&to=2024-01-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := get(t, srv.URL+"/api/summary?"+tc.query)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", status, body)
			}
			if !strings.Contains(body, `"status":400`) || !strings.Contains(body, `"error"`) {
				t.Errorf("body %s missing error/status fields", body)
			}
		})
	}
}

func TestTimeseriesEndpoint(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	status, body := get(t, srv.URL+"/api/timeseries?"+fullRangeQuery+"&bucket=month")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	var pts []struct {
		BucketStart    string   `json:"bucketStart"`
		Requests       int64    `json:"requests"`
		InputTokens    int64    `json:"inputTokens"`
		OutputTokens   int64    `json:"outputTokens"`
		CostKnownCount int64    `json:"costKnownCount"`
		CostTotal      *float64 `json:"costTotal"`
	}
	if err := json.Unmarshal([]byte(body), &pts); err != nil {
		t.Fatal(err)
	}
	if len(pts) != 4 {
		t.Fatalf("got %d month buckets, want 4: %s", len(pts), body)
	}
	wantStarts := []string{"2024-02-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z", "2026-03-01T00:00:00Z"}
	wantReqs := []int64{2, 4, 7, 7}
	wantCosts := []float64{0.70, 0.15, 0.90, 1.10}
	for i, p := range pts {
		if p.BucketStart != wantStarts[i] {
			t.Errorf("bucket[%d].bucketStart = %s, want %s", i, p.BucketStart, wantStarts[i])
		}
		if p.Requests != wantReqs[i] {
			t.Errorf("bucket[%d].requests = %d, want %d", i, p.Requests, wantReqs[i])
		}
		if p.CostTotal == nil || *p.CostTotal < wantCosts[i]-1e-9 || *p.CostTotal > wantCosts[i]+1e-9 {
			t.Errorf("bucket[%d].costTotal = %v, want %v", i, p.CostTotal, wantCosts[i])
		}
	}
}

func TestTimeseriesDefaultBucketIsDay(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	_, withParam := get(t, srv.URL+"/api/timeseries?"+fullRangeQuery+"&bucket=day")
	_, without := get(t, srv.URL+"/api/timeseries?"+fullRangeQuery)
	if withParam != without {
		t.Error("missing bucket param must default to day")
	}
}

func TestTimeseriesBadBucket(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	status, body := get(t, srv.URL+"/api/timeseries?"+fullRangeQuery+"&bucket=hour")
	if status != http.StatusBadRequest || !strings.Contains(body, "bucket") {
		t.Errorf("status = %d body %s, want 400 mentioning bucket", status, body)
	}
}

func TestTimeseriesCostNullForCopilotOnly(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	_, body := get(t, srv.URL+"/api/timeseries?"+fullRangeQuery+"&bucket=day&source=copilot")
	if !strings.Contains(body, `"costTotal":null`) {
		t.Errorf("copilot-only timeseries must serialize costTotal as null, body: %s", body)
	}
	if strings.Contains(body, `"costTotal":0`) {
		t.Error("costTotal must never coerce to 0")
	}
}

func TestBreakdownEndpoints(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	type row struct {
		Key       string   `json:"key"`
		Requests  int64    `json:"requests"`
		CostTotal *float64 `json:"costTotal"`
	}

	_, body := get(t, srv.URL+"/api/sources?"+fullRangeQuery)
	var sources []row
	if err := json.Unmarshal([]byte(body), &sources); err != nil {
		t.Fatal(err)
	}
	byKey := map[string]row{}
	for _, r := range sources {
		byKey[r.Key] = r
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %s, want 2 rows", body)
	}
	if byKey["copilot"].Requests != 12 || byKey["opencode"].Requests != 8 {
		t.Errorf("source rows = %+v", sources)
	}
	if byKey["copilot"].CostTotal != nil {
		t.Error("copilot cost must be null")
	}

	_, body = get(t, srv.URL+"/api/providers?"+fullRangeQuery)
	var providers []row
	if err := json.Unmarshal([]byte(body), &providers); err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %s, want 2 rows", body)
	}

	_, body = get(t, srv.URL+"/api/models?"+fullRangeQuery)
	var models []row
	if err := json.Unmarshal([]byte(body), &models); err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-5.6-luna", "gpt-4.1", "claude-haiku-4-5-20251001", "claude-sonnet-4-5"}
	if len(models) != len(want) {
		t.Fatalf("models = %s, want %d rows", body, len(want))
	}
	for i, m := range models {
		if m.Key != want[i] {
			t.Errorf("models[%d] = %s, want %s", i, m.Key, want[i])
		}
	}
}

func TestBreakdownFilterNarrows(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	_, body := get(t, srv.URL+"/api/models?"+fullRangeQuery+"&source=opencode")
	var rows []struct {
		Key      string `json:"key"`
		Requests int64  `json:"requests"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "claude-haiku-4-5-20251001" || rows[0].Requests != 8 {
		t.Errorf("opencode-only models = %s", body)
	}
}

func TestGenerationsList(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	status, body := get(t, srv.URL+"/api/generations?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	var gens []struct {
		ID        string   `json:"id"`
		Timestamp string   `json:"timestamp"`
		Cost      *float64 `json:"cost"`
	}
	if err := json.Unmarshal([]byte(body), &gens); err != nil {
		t.Fatal(err)
	}
	if len(gens) != 20 {
		t.Fatalf("got %d generations, want 20", len(gens))
	}
	if gens[0].ID != "o6" || gens[1].ID != "c9" || gens[2].ID != "c8" {
		t.Errorf("newest first expected o6,c9,c8; got %s,%s,%s", gens[0].ID, gens[1].ID, gens[2].ID)
	}

	status, body = get(t, srv.URL+"/api/generations?"+fullRangeQuery+"&limit=2&offset=3")
	if err := json.Unmarshal([]byte(body), &gens); err != nil {
		t.Fatal(err)
	}
	if len(gens) != 2 || gens[0].ID != "c7" || gens[1].ID != "c12" {
		t.Errorf("page = %s, want c7,c12", body)
	}

	// limit clamps to the max; offset past end yields an empty array
	status, body = get(t, srv.URL+"/api/generations?"+fullRangeQuery+"&limit=9999&offset=100")
	if status != http.StatusOK || body != "[]\n" {
		t.Errorf("offset past end: status %d body %q, want 200 and empty array", status, body)
	}
}

func TestGenerationsPaginationErrors(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	for _, tc := range []struct{ name, query string }{
		{"limit not a number", "limit=abc"},
		{"limit zero", "limit=0"},
		{"limit negative", "limit=-5"},
		{"offset negative", "offset=-1"},
		{"offset not a number", "offset=xyz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := get(t, srv.URL+"/api/generations?"+fullRangeQuery+"&"+tc.query)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", status, body)
			}
		})
	}
}

func TestGenerationByID(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)

	status, body := get(t, srv.URL+"/api/generations/c1")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, `"cost":null`) {
		t.Errorf("copilot generation must serialize cost as null, body: %s", body)
	}
	if !strings.Contains(body, `"inputTokens":100`) || !strings.Contains(body, `"outputTokens":50`) {
		t.Errorf("missing token fields: %s", body)
	}
	if !strings.Contains(body, `"conversationId":"conv-copilot"`) {
		t.Errorf("missing conversationId: %s", body)
	}

	status, body = get(t, srv.URL+"/api/generations/o1")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, `"cost":0.1`) || !strings.Contains(body, `"cacheCreationTokens":5`) {
		t.Errorf("opencode generation cost/cache mismatch: %s", body)
	}

	status, body = get(t, srv.URL+"/api/generations/nope")
	if status != http.StatusNotFound || !strings.Contains(body, `"error"`) {
		t.Errorf("unknown id: status %d body %s, want 404 with error", status, body)
	}
}

func TestStatsEndpoint(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), func() ingest.Stats {
		return ingest.Stats{Received: 100, Normalized: 95, Stored: 90, Deduplicated: 5, Rejected: 5, IngestionErrors: 1}
	})

	status, body := get(t, srv.URL+"/api/stats")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	var got ingest.Stats
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := ingest.Stats{Received: 100, Normalized: 95, Stored: 90, Deduplicated: 5, Rejected: 5, IngestionErrors: 1}
	if got != want {
		t.Errorf("stats = %+v, want %+v", got, want)
	}
}

func TestStatsNilFunc(t *testing.T) {
	srv := newServer(t, seedtest.EmptyDB(t), nil)

	status, body := get(t, srv.URL+"/api/stats")
	if status != http.StatusOK || !strings.Contains(body, `"received":0`) {
		t.Errorf("nil stats func: status %d body %s, want 200 with zeros", status, body)
	}
}

func TestEmptyDBAllEndpoints(t *testing.T) {
	srv := newServer(t, seedtest.EmptyDB(t), nil)

	_, body := get(t, srv.URL+"/api/summary")
	if !strings.Contains(body, `"requests":0`) || !strings.Contains(body, `"costTotal":null`) {
		t.Errorf("empty summary = %s", body)
	}
	for _, path := range []string{
		"/api/timeseries",
		"/api/timeseries?bucket=week",
		"/api/timeseries?bucket=month",
		"/api/sources",
		"/api/providers",
		"/api/models",
		"/api/generations",
	} {
		if _, body := get(t, srv.URL+path); body != "[]\n" {
			t.Errorf("GET %s on empty DB = %q, want empty array", path, body)
		}
	}
	if status, _ := get(t, srv.URL+"/api/generations/whatever"); status != http.StatusNotFound {
		t.Errorf("empty DB generation lookup status = %d, want 404", status)
	}
}

func TestAccessLogAtDebugLevel(t *testing.T) {
	db := seedtest.DB(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := httptest.NewServer(New(db, logger, nil, "test"))
	t.Cleanup(srv.Close)

	status, _ := get(t, srv.URL+"/api/summary?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	line := buf.String()
	for _, want := range []string{`"method":"GET"`, `"/api/summary"`, `"status":200`, `"duration"`} {
		if !strings.Contains(line, want) {
			t.Errorf("access log missing %s: %s", want, line)
		}
	}

	buf.Reset()
	logger2 := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv2 := httptest.NewServer(New(db, logger2, nil, "test"))
	t.Cleanup(srv2.Close)
	get(t, srv2.URL+"/api/summary")
	if buf.String() != "" {
		t.Errorf("access log must be silent above debug level: %s", buf.String())
	}
}
