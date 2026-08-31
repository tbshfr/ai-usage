package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

const fullRangeQuery = "from=2024-01-01&to=2026-04-01"

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(New(seedtest.DB(t)))
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	return resp.StatusCode, body
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}

func wantContains(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			t.Errorf("rendered HTML missing %q", s)
		}
	}
}

func wantNotContains(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(body, s) {
			t.Errorf("rendered HTML unexpectedly contains %q", s)
		}
	}
}

func TestOverviewPageRenders(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body,
		"OpenCode", "VS Code Copilot",
		"Today", "This week", "This month", "All time",
		"$2.8500 (8 known, 12 without cost data)",
		"1,987",
		"Usage over time",
		"All sources", "All providers", "All models",
		`<option value="claude-haiku-4-5-20251001"`,
	)
}

func TestOverviewCardsCopilotCostUnknown(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/fragments/overview-cards?"+fullRangeQuery+"&source=copilot")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "12", "—")
	wantNotContains(t, body, "$0.00", "$")
}

func TestTimeseriesCostSeriesOnlyWhenKnown(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/fragments/timeseries?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, `"scale":"cost"`, "Cost (reported only)")

	_, body = get(t, srv.URL+"/fragments/timeseries?"+fullRangeQuery+"&source=copilot")
	wantNotContains(t, body, "Cost (reported only)", `"scale":"cost"`)
}

func TestTimeseriesGranularityAndPresets(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/fragments/timeseries?"+fullRangeQuery+"&bucket=month")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, `value="month" selected`, `renderTokenChart('chart'`)

	// today is always past the fixed seed dates
	_, body = get(t, srv.URL+"/fragments/timeseries?range=today")
	wantContains(t, body, "No usage in this range.")

	status, _ = get(t, srv.URL+"/fragments/timeseries?range=nonsense")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid range: status %d, want 400", status)
	}
	status, _ = get(t, srv.URL+"/fragments/timeseries?"+fullRangeQuery+"&bucket=hour")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid bucket: status %d, want 400", status)
	}
}

func TestRecentRowsFilterNarrowing(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/fragments/recent-rows?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "gpt-4.1", "claude-haiku-4-5-20251001", "2026-03-01 00:19 UTC", "—")
	wantNotContains(t, body, "Prev")

	_, body = get(t, srv.URL+"/fragments/recent-rows?"+fullRangeQuery+"&source=opencode")
	wantContains(t, body, "claude-haiku-4-5-20251001", "$0.6000")
	wantNotContains(t, body, "gpt-4.1")

	_, body = get(t, srv.URL+"/fragments/recent-rows?"+fullRangeQuery+"&source=copilot")
	wantContains(t, body, "gpt-4.1", "—")
	wantNotContains(t, body, "$")

	// pager: 20 seeded rows, offset 15 leaves 5 and a Prev link
	_, body = get(t, srv.URL+"/fragments/recent-rows?"+fullRangeQuery+"&offset=15")
	wantContains(t, body, "/fragments/recent-rows?offset=0", "Prev")
	wantNotContains(t, body, "Next")

	status, _ = get(t, srv.URL+"/fragments/recent-rows?offset=-3")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid offset: status %d, want 400", status)
	}
}

func TestRecentConversationColumn(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/generations?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	// conversation column with click-to-filter links (IDs truncated for display)
	wantContains(t, body,
		"<th>Conversation</th>",
		`title="Filter recent by conversation" href="/generations?conversation=conv-copilot`,
		"conv-copil",
	)
	wantNotContains(t, body, ">other<") // every seeded row has a conversation ID

	// click-through narrows to that conversation
	_, body = get(t, srv.URL+"/fragments/recent-rows?"+fullRangeQuery+"&conversation=conv-opencode")
	wantContains(t, body, "claude-haiku-4-5-20251001")
	wantNotContains(t, body, "gpt-4.1")

	// "other" (no conversation ID, e.g. Copilot title generations): no seeded rows qualify
	_, body = get(t, srv.URL+"/fragments/recent-rows?"+fullRangeQuery+"&conversation=none")
	wantContains(t, body, "No requests in this range.")

	// conversation filter echoes into the preset links and the clear chip
	_, body = get(t, srv.URL+"/generations?"+fullRangeQuery+"&conversation=conv-opencode")
	wantContains(t, body, "conversation=conv-opencode", "Clear conversation filter")
}

func TestRecentCacheHitRateCard(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "Cache hit rate", "20.6%")
}

func TestBreakdownsFragment(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/fragments/breakdowns?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body,
		"By source", "By provider", "By model",
		"VS Code Copilot", "OpenCode",
		"github", "openai",
		"$2.8500",
		"Cache hit",
	)

	// copilot-only: model rows must show the cost-null contract
	_, body = get(t, srv.URL+"/fragments/breakdowns?"+fullRangeQuery+"&source=copilot")
	wantContains(t, body, "gpt-4.1", "openai", "—")
	wantNotContains(t, body, "$0.00")
}

func TestDetailPage(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/generations/c1")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body,
		"gpt-4.1", "trace-c1", "span-c1", "conv-copilot",
		"Not reported by this source", "Click to copy",
	)

	status, body = get(t, srv.URL+"/generations/o1")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "$0.1000")

	status, _ = get(t, srv.URL+"/generations/unknown-id")
	if status != http.StatusNotFound {
		t.Fatalf("unknown id: status %d, want 404", status)
	}
}

func TestRecentPageLinksToDetail(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/generations")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, `href="/generations/c12"`, "Duration")
}

func TestStaticAssets(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	for _, tc := range []struct {
		path, contentType string
	}{
		{"/static/app.css", "text/css"},
		{"/static/app.js", "text/javascript"},
		{"/static/vendor/htmx.min.js", "text/javascript"},
		{"/static/vendor/uplot.min.js", "text/javascript"},
		{"/static/vendor/uplot.min.css", "text/css"},
	} {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200", tc.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, tc.contentType) {
			t.Errorf("%s: content type %q, want prefix %q", tc.path, ct, tc.contentType)
		}
	}
	status, _ := get(t, srv.URL+"/static/vendor/missing.js")
	if status != http.StatusNotFound {
		t.Errorf("missing asset: status %d, want 404", status)
	}
}

func TestBadTimeParams(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, _ := get(t, srv.URL+"/?from=yesterday")
	if status != http.StatusBadRequest {
		t.Errorf("bad from: status %d, want 400", status)
	}
	status, _ = get(t, srv.URL+"/?from=2026-04-01&to=2024-01-01")
	if status != http.StatusBadRequest {
		t.Errorf("to before from: status %d, want 400", status)
	}
}
