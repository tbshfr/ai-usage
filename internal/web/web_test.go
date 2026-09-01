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
	return httptest.NewServer(New(seedtest.DB(t), "test"))
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

func TestDashboardPageRenders(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body,
		"OpenCode", "VS Code Copilot",
		"Today", "This week", "This month", "All time",
		"hit 20.6%",
		"3,727",       // all-time total tokens
		"20 requests", // all-time requests
		"period=today", "period=week", "period=month", "period=all",
		"All sources", "All providers", "All models",
		`<option value="claude-haiku-4-5-20251001"`,
	)
	// Cost is not on the dashboard cards — only in the detail expansion.
	wantNotContains(t, body, "$2.8500", "Usage over time")
}

func TestFooterShowsVersion(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "All timestamps are UTC. · test")
}

func TestPeriodDetailFragmentHasCost(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/fragments/period-detail?period=all&"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "All time &mdash; details", "$2.8500 (8 known, 12 without cost data)", "Cache hit rate", "20.6%")

	// unknown cost must stay "—", never $0.00
	_, body = get(t, srv.URL+"/fragments/period-detail?period=all&"+fullRangeQuery+"&source=copilot")
	wantContains(t, body, "—")
	wantNotContains(t, body, "$0.00", "$")

	// close action: empty period renders an empty body
	status, body = get(t, srv.URL+"/fragments/period-detail")
	if status != http.StatusOK || body != "" {
		t.Errorf("close action: status %d body %q", status, body)
	}

	status, _ = get(t, srv.URL+"/fragments/period-detail?period=nonsense")
	if status != http.StatusBadRequest {
		t.Errorf("invalid period: status %d, want 400", status)
	}
}

func TestTrendsPageAndFragment(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/trends?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body,
		"Tokens over time", "Tokens by source", "Cache hit rate",
		`data-chart="chart-tokens"`, `data-chart="chart-sources"`, `data-chart="chart-cache"`,
	)

	// fragment with month bucket; cost series is gone, pct series is present
	_, body = get(t, srv.URL+"/fragments/trends?"+fullRangeQuery+"&bucket=month")
	wantContains(t, body, `"name":"Cache hit rate","fmt":"pct"`, `value="month" selected`)
	wantNotContains(t, body, "Cost (reported only)", `"scale":"cost"`)

	// hour buckets are valid and carry the bucket span for axis padding
	_, body = get(t, srv.URL+"/fragments/trends?"+fullRangeQuery+"&bucket=hour")
	wantContains(t, body, `value="hour" selected`, `"span":3600`)

	// today is always past the fixed seed dates
	_, body = get(t, srv.URL+"/fragments/trends?range=today")
	wantContains(t, body, "No usage in this range.")

	status, _ = get(t, srv.URL+"/trends?range=nonsense")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid range: status %d, want 400", status)
	}
	status, _ = get(t, srv.URL+"/trends?"+fullRangeQuery+"&bucket=year")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid bucket: status %d, want 400", status)
	}
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
		"Cache hit", "Total tokens",
		"<tr class=\"totals\">",
	)

	// copilot-only: model rows must show the cost-null contract
	_, body = get(t, srv.URL+"/fragments/breakdowns?"+fullRangeQuery+"&source=copilot")
	wantContains(t, body, "gpt-4.1", "openai", "—")
	wantNotContains(t, body, "$0.00")
}

func TestSessionsPageConversations(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/sessions?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body,
		"conv-card",
		`href="/sessions?conversation=conv-copilot"`,
		`href="/sessions?conversation=conv-opencode"`,
		"<b>12</b> requests",  // conv-copilot
		"<b>3,395</b> tokens", // conv-copilot total tokens
		"cache hit 21.6%",
		"$2.8500", // opencode session reports cost
		"2 sessions",
		"sort=asc", "sort=desc", "Sort by date",
	)
	// no conversation rows without IDs in the seed → no Other cards
	wantNotContains(t, body, "Other requests")
}

func TestSessionsSortOrder(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	// asc: oldest conversation (conv-copilot, first activity at 00:09) first
	status, body := get(t, srv.URL+"/sessions?"+fullRangeQuery+"&sort=asc")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if strings.Index(body, "conversation=conv-copilot") > strings.Index(body, "conversation=conv-opencode") {
		t.Error("asc sort: conv-copilot must come before conv-opencode")
	}
	wantContains(t, body, `<input type="hidden" name="sort" value="asc">`)

	// fragment keeps sort via the query string (the filter bar is not part of the fragment)
	_, body = get(t, srv.URL+"/fragments/session-list?"+fullRangeQuery+"&sort=desc")
	if strings.Index(body, "conversation=conv-opencode") > strings.Index(body, "conversation=conv-copilot") {
		t.Error("desc sort: conv-opencode must come before conv-copilot")
	}

	status, _ = get(t, srv.URL+"/sessions?sort=sideways")
	if status != http.StatusBadRequest {
		t.Errorf("invalid sort: status %d, want 400", status)
	}
}

func TestSessionRequestsView(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, body := get(t, srv.URL+"/sessions?"+fullRangeQuery+"&view=requests")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body,
		"<th>Conversation</th>",
		"gpt-4.1", "claude-haiku-4-5-20251001", "2026-03-01 00:19 UTC", "—",
		`title="Show this conversation's requests" href="/sessions?conversation=conv-copilot`,
		`<input type="hidden" name="view" value="requests">`,
	)
	wantNotContains(t, body, "Prev")

	// conversation drill-down narrows to that conversation's requests
	// (fragment: the page's filter dropdowns would also mention other models)
	_, body = get(t, srv.URL+"/fragments/session-list?"+fullRangeQuery+"&conversation=conv-opencode")
	wantContains(t, body, "claude-haiku-4-5-20251001", "$0.6000")
	wantNotContains(t, body, "gpt-4.1", "<th>Conversation</th>")

	// pager: 20 seeded rows, offset 15 leaves 5 and a Prev link
	_, body = get(t, srv.URL+"/fragments/session-list?"+fullRangeQuery+"&view=requests&offset=15")
	wantContains(t, body, "/fragments/session-list?offset=0", "Prev")
	wantNotContains(t, body, "Next")

	status, _ = get(t, srv.URL+"/fragments/session-list?offset=-3")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid offset: status %d, want 400", status)
	}
}

func TestSessionsOtherGroupNone(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	// no seeded rows lack a conversation ID: the none filter is empty
	_, body := get(t, srv.URL+"/fragments/session-list?"+fullRangeQuery+"&conversation=none")
	wantContains(t, body, "No requests in this range.")

	// conversation filter echoes into the preset links and the clear chip
	_, body = get(t, srv.URL+"/sessions?"+fullRangeQuery+"&conversation=conv-opencode")
	wantContains(t, body, "conversation=conv-opencode", "Clear conversation filter")
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
		`href="/sessions?conversation=conv-copilot"`,
		"Back to sessions",
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

func TestGenerationsRedirectsToSessions(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(srv.URL + "/generations?" + fullRangeQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("status %d, want 301", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/sessions?") || !strings.Contains(loc, "from=2024-01-01") {
		t.Errorf("Location %q, want /sessions with preserved query", loc)
	}
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
		{"/robots.txt", "text/plain"},
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
