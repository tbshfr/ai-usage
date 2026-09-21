// Package web serves the server-rendered dashboard: pages and HTMX
// fragments that query the Phase 4 storage layer directly (no HTTP-to-self).
package web

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/tbshfr/ai-usage"
	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/backup"
	"github.com/tbshfr/ai-usage/internal/ingest"
	"github.com/tbshfr/ai-usage/internal/live"
	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

const (
	recentLimit        = 50
	conversationsLimit = 24
	statsDaysLimit     = 3660
)

// New returns the dashboard UI routes (pages, fragments, static assets).
// Templates are parsed once at package init from the embedded FS.
// An empty version renders as "dev". The hub drives the /events SSE
// stream; nil disables data-changed signals (the stream then only sends
// keepalives). stats may be nil; when set, the live pipeline counters
// replace today's persisted row. reasons may be nil; when set, the live
// per-reason breakdown replaces today's persisted rows.
func New(db *sql.DB, stats func() ingest.Stats, reasons func() ingest.ReasonCounts, hub *live.Hub, version string, backupStatus ...func() backup.Status) http.Handler {
	return newMux(db, stats, reasons, nil, hub, version, backupStatus...)
}

// NewAuthed adds the login/logout routes and applies the dashboard
// guard to every UI route; api.NewWithAuth additionally wraps the whole
// dashboard port so /api/* is protected as well.
func NewAuthed(db *sql.DB, stats func() ingest.Stats, reasons func() ingest.ReasonCounts, dash *auth.Dashboard, hub *live.Hub, version string, backupStatus ...func() backup.Status) http.Handler {
	return dash.Middleware(newMux(db, stats, reasons, dash, hub, version, backupStatus...))
}

func newMux(db *sql.DB, stats func() ingest.Stats, reasons func() ingest.ReasonCounts, dash *auth.Dashboard, hub *live.Hub, version string, backupStatus ...func() backup.Status) http.Handler {
	if version == "" {
		version = "dev"
	}
	s := &server{db: db, stats: stats, reasons: reasons, dash: dash, hub: hub, limiter: newLoginLimiter(), version: version}
	if len(backupStatus) > 0 {
		s.backupStatus = backupStatus[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /fragments/backup-banner", func(w http.ResponseWriter, r *http.Request) {
		s.renderFrag(w, "backup-banner", &pageData{Backup: s.currentBackupStatus()})
	})
	mux.HandleFunc("GET /trends", s.trends)
	mux.HandleFunc("GET /breakdowns", s.breakdowns)
	mux.HandleFunc("GET /sessions", s.sessions)
	mux.HandleFunc("GET /stats", s.statsPage)
	mux.HandleFunc("GET /generations", s.redirectSessions)
	mux.HandleFunc("GET /generations/{id}", s.detail)
	mux.HandleFunc("GET /events", s.serveEvents)
	mux.HandleFunc("GET /fragments/dashboard-stats", s.fragDashboardStats)
	mux.HandleFunc("GET /fragments/period-detail", s.fragPeriodDetail)
	mux.HandleFunc("GET /fragments/trends", s.fragTrends)
	mux.HandleFunc("GET /fragments/breakdowns", s.fragBreakdowns)
	mux.HandleFunc("GET /fragments/session-list", s.fragSessionList)
	mux.HandleFunc("GET /fragments/stats", s.fragStats)
	mux.HandleFunc("GET /fragments/stats-reasons", s.fragStatsReasons)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if !assets.ServePublic(w, r) {
			http.NotFound(w, r)
		}
	})
	mux.Handle("GET /static/{path...}", staticHandler())
	if dash != nil {
		mux.HandleFunc("GET /login", s.loginForm)
		mux.HandleFunc("POST /login", s.loginSubmit)
		mux.HandleFunc("GET /logout", s.logout)
	}
	return mux
}

func (s *server) currentBackupStatus() backup.Status {
	if s.backupStatus == nil {
		return backup.Status{}
	}
	return s.backupStatus()
}

type server struct {
	backupStatus func() backup.Status
	db           *sql.DB
	stats        func() ingest.Stats
	reasons      func() ingest.ReasonCounts
	dash         *auth.Dashboard
	hub          *live.Hub
	limiter      *loginLimiter
	version      string
}

// pageData is the single view model passed to every template set; each
// template only reads the fields it needs.
type pageData struct {
	Backup      backup.Status
	Title       string
	Active      string
	ShowLogout  bool
	Version     string
	Error       string
	F           filterView
	Cards       []cardView
	Heatmap     heatmapView
	Periods     periodModes
	Detail      *periodDetailView
	Charts      trendsView
	Convs       convsView
	Recent      recentView
	Breaks      breaksView
	S           statsView
	Reasons     *reasonsDetailView
	D           *normalize.Generation
	View        string        // sessions page: "sessions" or "requests"
	SortOrder   storage.Order // sessions page list sort
	SortAsc     linkPair      // fragment + page URLs for the oldest-first toggle
	SortDesc    linkPair
	SessionsTab string // sessions tab link ("" hides the tabs)
	RequestsTab string // all-requests tab link ("" hides the tabs)
}

// linkPair carries the htmx fragment URL and the full page URL for the same
// destination (sort toggles use both: swap the fragment, push the page URL).
type linkPair struct {
	Frag string
	Page string
}

type filterView struct {
	Action          string
	ShowDimensions  bool
	ShowPeriodMode  bool
	PeriodRolling   bool
	Collapsible     bool
	Presets         []presetView
	Sources         []string
	Providers       []string
	Models          []string
	Selected        uiFilter
	ConversationURL string // current page minus the conversation filter, "" when no conversation filter is set
	Hidden          []hiddenInput
}

type hiddenInput struct {
	Name  string
	Value string
}

type presetView struct {
	Label  string
	URL    string
	Active bool
}

// cardView is one dashboard period stat (hero card or secondary card).
// Since is only set on the "all" card: the first stored generation
// matching the active filters, nil when nothing matches.
type cardView struct {
	Period string // today | week | month | all
	Label  string
	Hero   bool
	S      storage.SummaryResult
	Since  *time.Time
}

type periodModes struct {
	Rolling bool
}

type heatmapView struct {
	Months []heatmapMonth
}

type heatmapMonth struct {
	Label   string
	Padding []struct{}
	Cells   []heatmapCell
}

type heatmapCell struct {
	Label  string
	Tokens int64
	Level  int
	Future bool
}

// periodDetailView is the inline expansion under the dashboard cards.
// Since mirrors the card's first-data date for the "all" period.
type periodDetailView struct {
	Period string
	Label  string
	S      storage.SummaryResult
	Since  *time.Time
}

type trendsView struct {
	Bucket     storage.Bucket
	Buckets    []bucketOption
	TokenData  chartJSON
	HasTokens  bool
	CostData   chartJSON
	HasCost    bool
	SourceData chartJSON
	HasSource  bool
	CacheData  chartJSON
	HasCache   bool
}

type bucketOption struct {
	Value    storage.Bucket
	Label    string
	Disabled bool
}

type chartJSON struct {
	Labels []int64       `json:"labels"`
	Series []chartSeries `json:"series"`
	// Span is the nominal bucket width in seconds; the chart pads the time
	// axis to at least one span so single-bucket ranges still scale.
	Span int64 `json:"span,omitempty"`
}

type chartSeries struct {
	Name     string `json:"name"`
	Fmt      string `json:"fmt,omitempty"`
	SpanGaps bool   `json:"spanGaps,omitempty"`
	Values   []any  `json:"values"`
}

type recentView struct {
	Rows       []normalize.Generation
	HasPrev    bool
	PrevOffset int
	HasNext    bool
	NextOffset int
}

type convsView struct {
	Rows       []storage.ConversationSummary
	Total      int64
	HasPrev    bool
	PrevOffset int
	HasNext    bool
	NextOffset int
}

type breaksView struct {
	Source, Provider, Model                []storage.Breakdown
	SourceTotal, ProviderTotal, ModelTotal storage.Breakdown
}

// statsRow is one day of ingestion counters; Live marks today's row, whose
// values come straight from the pipeline instead of the last save.
type statsRow struct {
	Day  string
	Live bool
	ingest.Stats
}

type statsView struct {
	Rows     []statsRow
	Total    ingest.Stats
	Chart    chartJSON
	HasChart bool
}

// reasonsDetailView is the per-day stats breakdown expansion: reason
// counters grouped by kind, with human-readable labels.
type reasonsDetailView struct {
	Day    string
	Live   bool // true when today's live pipeline counters replace the persisted rows
	Groups []reasonGroup
}

type reasonGroup struct {
	Kind  string
	Label string
	Rows  []reasonRow
	Note  string // optional footnote (e.g. transport rejections count requests)
}

type reasonRow struct {
	Reason string
	Label  string
	Count  uint64
}

// dashboard renders the landing page: today's tokens big, weekly/monthly/
// all-time beside it, each with the cache hit rate, expanding to details.
func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	// The range preset is ignored here: the period cards fix their own ranges.
	_, u, err := parseFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d, err := s.base(r.Context(), "Dashboard", "dashboard", "/", u)
	if err != nil {
		writeErr(w, err)
		return
	}
	d.F.Presets = nil
	d.Periods, err = parsePeriodModes(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d.F.ShowPeriodMode = true
	d.F.PeriodRolling = d.Periods.Rolling
	d.F.Collapsible = true
	if d.Cards, err = s.cards(r.Context(), u, d.Periods); err != nil {
		writeErr(w, err)
		return
	}
	if d.Heatmap, err = s.heatmap(r.Context(), u); err != nil {
		writeErr(w, err)
		return
	}
	d.Backup = s.currentBackupStatus()
	s.render(w, "dashboard", d)
}

func (s *server) fragDashboardStats(w http.ResponseWriter, r *http.Request) {
	_, u, err := parseFilter(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.base(r.Context(), "Dashboard", "dashboard", "/", u)
	if err != nil {
		writeErr(w, err)
		return
	}
	d.F.Presets = nil
	d.Periods, err = parsePeriodModes(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if d.Cards, err = s.cards(r.Context(), u, d.Periods); err != nil {
		writeErr(w, err)
		return
	}
	if d.Heatmap, err = s.heatmap(r.Context(), u); err != nil {
		writeErr(w, err)
		return
	}
	s.renderFrag(w, "dashboard-stats", d)
}

// fragPeriodDetail swaps the inline detail expansion under the dashboard
// cards. A missing period param renders an empty body (the close button).
func (s *server) fragPeriodDetail(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, u, err := parseFilter(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	modes, err := parsePeriodModes(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	from, label, ok := periodStart(period, time.Now().UTC(), modes)
	if !ok {
		http.Error(w, "invalid period "+period+" (want today, week, month, or all)", http.StatusBadRequest)
		return
	}
	sum, err := storage.Summary(r.Context(), s.db, storage.Filter{
		From:         from,
		Source:       u.Source,
		Provider:     u.Provider,
		Model:        u.Model,
		Conversation: u.Conversation,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	d := &pageData{Detail: &periodDetailView{Period: period, Label: label, S: sum}}
	if period == "all" {
		since, err := storage.Earliest(r.Context(), s.db, storage.Filter{
			Source:       u.Source,
			Provider:     u.Provider,
			Model:        u.Model,
			Conversation: u.Conversation,
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		d.Detail.Since = since
	}
	s.renderFrag(w, "period-detail", d)
}

func periodStart(period string, now time.Time, modes periodModes) (time.Time, string, bool) {
	switch period {
	case "today":
		return startOfDay(now), "Today", true
	case "week":
		if modes.Rolling {
			return now.Add(-7 * 24 * time.Hour), "Last 7 days", true
		}
		return startOfWeek(now), "This week", true
	case "month":
		if modes.Rolling {
			return now.Add(-30 * 24 * time.Hour), "Last 30 days", true
		}
		return startOfMonth(now), "This month", true
	case "all":
		return time.Time{}, "All time", true
	}
	return time.Time{}, "", false
}

func parsePeriodModes(r *http.Request) (periodModes, error) {
	var modes periodModes
	switch r.URL.Query().Get("period_mode") {
	case "calendar":
	case "", "rolling":
		modes.Rolling = true
	default:
		return modes, badRequest{fmt.Errorf("invalid period mode (want calendar or rolling)")}
	}
	return modes, nil
}

func (s *server) cards(ctx context.Context, u uiFilter, modes periodModes) ([]cardView, error) {
	now := time.Now().UTC()
	periods := []struct {
		period, label string
		from          time.Time
		hero          bool
	}{
		{"today", "Today", startOfDay(now), true},
		{"week", "This week", startOfWeek(now), false},
		{"month", "This month", startOfMonth(now), false},
		{"all", "All time", time.Time{}, false},
	}
	if modes.Rolling {
		periods[1].label, periods[1].from = "Last 7 days", now.Add(-7*24*time.Hour)
		periods[2].label, periods[2].from = "Last 30 days", now.Add(-30*24*time.Hour)
	}
	out := make([]cardView, 0, len(periods))
	for _, p := range periods {
		sum, err := storage.Summary(ctx, s.db, storage.Filter{
			From:         p.from,
			Source:       u.Source,
			Provider:     u.Provider,
			Model:        u.Model,
			Conversation: u.Conversation,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, cardView{Period: p.period, Label: p.label, Hero: p.hero, S: sum})
	}
	since, err := storage.Earliest(ctx, s.db, storage.Filter{
		Source:       u.Source,
		Provider:     u.Provider,
		Model:        u.Model,
		Conversation: u.Conversation,
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Period == "all" {
			out[i].Since = since
			break
		}
	}
	return out, nil
}

// heatmap returns a GitHub-style, Sunday-aligned year of daily token totals.
// Empty days are deliberately present with zero tokens so activity gaps are
// visible instead of being compressed out of the calendar.
func (s *server) heatmap(ctx context.Context, u uiFilter) (heatmapView, error) {
	now := time.Now().UTC()
	today := startOfDay(now)
	start := today.AddDate(0, 0, -364)
	start = start.AddDate(0, 0, -int(start.Weekday()))
	end := start.AddDate(0, 0, 371)
	pts, err := storage.Timeseries(ctx, s.db, storage.Filter{
		From: start, To: end, Source: u.Source, Provider: u.Provider,
		Model: u.Model, Conversation: u.Conversation,
	}, storage.BucketDay)
	if err != nil {
		return heatmapView{}, err
	}
	byDay := make(map[int64]int64, len(pts))
	var maxTokens int64
	for _, p := range pts {
		total := p.InputTokens + p.OutputTokens + p.CacheReadTokens + p.CacheCreationTokens + p.ReasoningTokens
		byDay[p.BucketStart] = total
		if total > maxTokens {
			maxTokens = total
		}
	}
	v := heatmapView{}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		tokens := byDay[day.UnixMilli()]
		level := 0
		if tokens > 0 && maxTokens > 0 {
			level = int((tokens*4 + maxTokens - 1) / maxTokens)
			level = min(max(level, 1), 4)
		}
		if len(v.Months) == 0 || day.Day() == 1 {
			v.Months = append(v.Months, heatmapMonth{Label: day.Format("Jan 2006"), Padding: make([]struct{}, int(day.Weekday()))})
		}
		month := &v.Months[len(v.Months)-1]
		month.Cells = append(month.Cells, heatmapCell{
			Label:  day.Format("Mon, Jan 2, 2006"),
			Tokens: tokens, Level: level, Future: day.After(today),
		})
	}
	return v, nil
}

// trends renders the charts page.
func (s *server) trends(w http.ResponseWriter, r *http.Request) {
	d, err := s.trendsData(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.render(w, "trends", d)
}

func (s *server) fragTrends(w http.ResponseWriter, r *http.Request) {
	d, err := s.trendsData(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.renderFrag(w, "trends", d)
}

func (s *server) trendsData(r *http.Request) (*pageData, error) {
	f, u, err := parseFilter(r)
	if err != nil {
		return nil, badRequest{err}
	}
	bucket, err := trendBucketParam(r, f)
	if err != nil {
		return nil, err
	}
	d, err := s.base(r.Context(), "Trends", "trends", "/trends", u)
	if err != nil {
		return nil, err
	}
	d.Charts.Bucket = bucket
	d.Charts.Buckets = trendBucketOptions(f, bucket)
	pts, err := storage.Timeseries(r.Context(), s.db, f, bucket)
	if err != nil {
		return nil, err
	}
	pts = fillTimeseries(pts, f, bucket, time.Now().UTC())
	d.Charts.TokenData, d.Charts.HasTokens = buildTokenChart(pts, bucket)
	d.Charts.CostData, d.Charts.HasCost = buildCostChart(pts, bucket)
	spts, err := storage.TimeseriesBySource(r.Context(), s.db, f, bucket)
	if err != nil {
		return nil, err
	}
	d.Charts.SourceData, d.Charts.HasSource = buildSourceChart(spts, pts, bucket)
	d.Charts.CacheData, d.Charts.HasCache = buildCacheChart(pts, bucket)
	return d, nil
}

func (s *server) breakdowns(w http.ResponseWriter, r *http.Request) {
	f, u, err := parseFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d, err := s.base(r.Context(), "Breakdowns", "breakdowns", "/breakdowns", u)
	if err != nil {
		writeErr(w, err)
		return
	}
	if d.Breaks.Source, err = storage.BySource(r.Context(), s.db, f); err != nil {
		writeErr(w, err)
		return
	}
	if d.Breaks.Provider, err = storage.ByProvider(r.Context(), s.db, f); err != nil {
		writeErr(w, err)
		return
	}
	if d.Breaks.Model, err = storage.ByModel(r.Context(), s.db, f); err != nil {
		writeErr(w, err)
		return
	}
	d.Breaks.SourceTotal = totalBreakdown(d.Breaks.Source)
	d.Breaks.ProviderTotal = totalBreakdown(d.Breaks.Provider)
	d.Breaks.ModelTotal = totalBreakdown(d.Breaks.Model)
	s.render(w, "breakdowns", d)
}

// totalBreakdown sums a breakdown table's rows for the totals row. The cost
// total sums only rows that reported cost, mirroring costCell semantics.
func totalBreakdown(rows []storage.Breakdown) storage.Breakdown {
	var t storage.Breakdown
	var costSum float64
	for _, b := range rows {
		t.Requests += b.Requests
		t.InputTokens += b.InputTokens
		t.OutputTokens += b.OutputTokens
		t.CacheReadTokens += b.CacheReadTokens
		t.CacheCreationTokens += b.CacheCreationTokens
		t.ReasoningTokens += b.ReasoningTokens
		t.CostKnownCount += b.CostKnownCount
		t.CostReportedCount += b.CostReportedCount
		t.CostEstimatedCount += b.CostEstimatedCount
		t.CostFreeCount += b.CostFreeCount
		t.CostUnknownCount += b.CostUnknownCount
		if b.CostTotal != nil {
			costSum += *b.CostTotal
		}
	}
	if t.CostKnownCount > 0 {
		t.CostTotal = &costSum
	}
	return t
}

func (s *server) fragBreakdowns(w http.ResponseWriter, r *http.Request) {
	f, u, err := parseFilter(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.base(r.Context(), "Breakdowns", "breakdowns", "/breakdowns", u)
	if err != nil {
		writeErr(w, err)
		return
	}
	if d.Breaks.Source, err = storage.BySource(r.Context(), s.db, f); err != nil {
		writeErr(w, err)
		return
	}
	if d.Breaks.Provider, err = storage.ByProvider(r.Context(), s.db, f); err != nil {
		writeErr(w, err)
		return
	}
	if d.Breaks.Model, err = storage.ByModel(r.Context(), s.db, f); err != nil {
		writeErr(w, err)
		return
	}
	d.Breaks.SourceTotal = totalBreakdown(d.Breaks.Source)
	d.Breaks.ProviderTotal = totalBreakdown(d.Breaks.Provider)
	d.Breaks.ModelTotal = totalBreakdown(d.Breaks.Model)
	s.renderFrag(w, "breakdowns", d)
}

// statsPage renders range-selectable per-day ingestion counters. Today's row
// shows live pipeline counters, so it is current even before the next save.
func (s *server) statsPage(w http.ResponseWriter, r *http.Request) {
	u, from, to, limit, err := statsRangeParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d := &pageData{Title: "Stats", Active: "stats"}
	d.F = filterView{Action: "/stats", Selected: u, Presets: statsPresetViews(u), Collapsible: true}
	if err := s.loadStats(r.Context(), d, from, to, limit); err != nil {
		writeErr(w, err)
		return
	}
	d.Backup = s.currentBackupStatus()
	s.render(w, "stats", d)
}

// fragStats is the SSE-refreshable section of the stats page.
func (s *server) fragStats(w http.ResponseWriter, r *http.Request) {
	_, from, to, limit, err := statsRangeParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d := &pageData{}
	if err := s.loadStats(r.Context(), d, from, to, limit); err != nil {
		writeErr(w, err)
		return
	}
	d.Backup = s.currentBackupStatus()
	s.renderFrag(w, "stats", d)
}

// fragStatsReasons swaps the per-day rejection/error/dedup breakdown on
// the stats page. A missing day param renders an empty body (the close
// button). Today's breakdown replaces the persisted rows with the live
// pipeline counters, mirroring loadStats. Reasons come from a fixed
// enum, so the query parameter only selects a day — nothing client
// supplied ever reaches the stats tables.
func (s *server) fragStatsReasons(w http.ResponseWriter, r *http.Request) {
	day := r.URL.Query().Get("day")
	if day == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		http.Error(w, "invalid day "+day+" (want YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	d := &pageData{}
	view, err := s.reasonsDetail(r.Context(), day)
	if err != nil {
		writeErr(w, err)
		return
	}
	d.Reasons = view
	s.renderFrag(w, "stats-reasons", d)
}

// reasonsDetail assembles one day's per-reason breakdown. For today the
// live pipeline counters replace the persisted rows (they already include
// the persisted base, so merging would double-count); older days serve
// persisted rows directly.
func (s *server) reasonsDetail(ctx context.Context, day string) (*reasonsDetailView, error) {
	counts := map[ingestReasonKey]uint64{}
	today := utcDate(time.Now())
	live := day == today && s.reasons != nil
	if live {
		for kind, reasons := range s.reasons() {
			for reason, n := range reasons {
				if n == 0 {
					continue
				}
				counts[ingestReasonKey{kind, reason}] = n
			}
		}
	} else {
		rows, _, err := storage.DailyReasonsForDay(ctx, s.db, day)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			counts[ingestReasonKey{row.Kind, row.Reason}] += uint64(row.Count)
		}
	}
	// Deterministic order: canonical kind order shared with the JSON API
	// (see ingest.ReasonKindOrder), reasons alphabetical. Unknown kinds
	// (manual DB rows, future kinds) render last, matching sortReasonStats.
	var groups []reasonGroup
	seen := make(map[string]bool, len(ingest.ReasonKindOrder))
	for _, kind := range ingest.ReasonKindOrder {
		seen[kind] = true
		reasons, ok := groupReasons(counts, kind)
		if !ok {
			continue
		}
		label, note := kind, ""
		if meta, ok := reasonKindMeta[kind]; ok {
			label, note = meta.label, meta.note
		}
		groups = append(groups, reasonGroup{
			Kind:  kind,
			Label: label,
			Rows:  reasons,
			Note:  note,
		})
	}
	for _, kind := range unknownReasonKinds(counts, seen) {
		reasons, ok := groupReasons(counts, kind)
		if !ok {
			continue
		}
		groups = append(groups, reasonGroup{
			Kind:  kind,
			Label: kind,
			Rows:  reasons,
		})
	}
	return &reasonsDetailView{Day: day, Live: live, Groups: groups}, nil
}

// unknownReasonKinds returns kinds present with non-zero counters that are
// not in the canonical order, sorted like the JSON API (rank, then kind).
func unknownReasonKinds(counts map[ingestReasonKey]uint64, seen map[string]bool) []string {
	var kinds []string
	present := map[string]bool{}
	for k, v := range counts {
		if v == 0 || seen[k.kind] || present[k.kind] {
			continue
		}
		present[k.kind] = true
		kinds = append(kinds, k.kind)
	}
	sort.Slice(kinds, func(i, j int) bool {
		ri, rj := ingest.ReasonKindRank(kinds[i]), ingest.ReasonKindRank(kinds[j])
		if ri != rj {
			return ri < rj
		}
		return kinds[i] < kinds[j]
	})
	return kinds
}

type ingestReasonKey struct{ kind, reason string }

// reasonKindMeta carries the labels/footnotes for the breakdown groups.
// Ordering comes from ingest.ReasonKindOrder (shared with the API), so the
// same data renders in the same order everywhere. Reasons not in
// reasonLabel (e.g. a dedup source) render with their raw enum value.
var reasonKindMeta = map[string]struct {
	label string
	note  string
}{
	ingest.ReasonKindRejected:   {"Rejected spans", "Spans with no known source's markers, or healthy spans that are never generation records."},
	ingest.ReasonKindIgnored:    {"Ignored (not used)", "Records that arrived healthy but can never become generations."},
	ingest.ReasonKindNormError:  {"Normalization errors", "Spans recognized as generations but too malformed to normalize."},
	ingest.ReasonKindDedup:      {"Deduplicated", "Duplicate generation records, by source (same trace/span ID retried or re-exported)."},
	ingest.ReasonKindHTTPReject: {"Transport rejections", "Requests rejected before the pipeline; counted per request, not per record."},
}

// groupReasons sorts one kind's counters into display rows; ok is false
// when the kind has no non-zero counters.
func groupReasons(counts map[ingestReasonKey]uint64, kind string) ([]reasonRow, bool) {
	keys := make([]string, 0, len(counts))
	for k, v := range counts {
		if k.kind != kind || v == 0 {
			continue
		}
		keys = append(keys, k.reason)
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	rows := make([]reasonRow, 0, len(keys))
	for _, reason := range keys {
		rows = append(rows, reasonRow{
			Reason: reason,
			Label:  reasonLabel(kind, reason),
			Count:  counts[ingestReasonKey{kind, reason}],
		})
	}
	return rows, true
}

// reasonLabel maps the fixed-enum reason codes to human-readable labels;
// unknown values (dedup sources) render as-is.
func reasonLabel(kind, reason string) string {
	if kind == ingest.ReasonKindDedup {
		return friendlySource(reason)
	}
	switch reason {
	case ingest.ReasonNoSource:
		return "No known source"
	case ingest.ReasonNotGeneration:
		return "Not a generation record"
	case ingest.ReasonLogs:
		return "Log records"
	case ingest.ReasonMetrics:
		return "Metric datapoints"
	case ingest.ReasonBadAttrs:
		return "Invalid attribute values"
	case ingest.ReasonBadIDs:
		return "Missing record identity"
	case ingest.ReasonBadTimestamp:
		return "Invalid log timestamp"
	case ingest.ReasonNormOther:
		return "Other"
	case ingest.ReasonUnauthorized:
		return "Missing or invalid bearer token (HTTP)"
	case ingest.ReasonGRPCUnauthorized:
		return "Missing or invalid bearer token (gRPC)"
	case ingest.ReasonBadContentType:
		return "Missing or unsupported content type"
	case ingest.ReasonBadEncoding:
		return "Unsupported content encoding"
	case ingest.ReasonBodyTooLarge:
		return "Request body too large"
	case ingest.ReasonBodyReadError:
		return "Request body read error"
	case ingest.ReasonBadGzip:
		return "Malformed gzip body"
	case ingest.ReasonDecodeFailed:
		return "Payload decode failed"
	default:
		return reason
	}
}

func (s *server) loadStats(ctx context.Context, d *pageData, from, to string, limit int) error {
	rows, err := storage.DailyStatsRange(ctx, s.db, from, to, limit)
	if err != nil {
		return err
	}
	// Days whose only activity was transport rejections have zero record
	// counters; they stay visible so attack traffic shows up in the table.
	reasonDays, err := storage.DailyReasonDaysRange(ctx, s.db, from, to, limit)
	if err != nil {
		return err
	}
	today := utcDate(time.Now())
	v := statsView{Rows: make([]statsRow, 0, len(rows)+1)}
	for _, r := range rows {
		// Days where nothing arrived are pure noise; when live counters
		// are available, today's persisted row is stale by up to one
		// save interval, so the live counters replace it. Without a
		// stats func, today's persisted row is the best data available
		// and must not be dropped.
		if (s.stats != nil && r.Day == today) || (r.Received == 0 && !reasonDays[r.Day]) {
			continue
		}
		v.Rows = append(v.Rows, statsRow{Day: r.Day, Stats: dailyToIngest(r)})
	}
	// Orphan reason days: a crash between the old two-transaction saves
	// could leave stats_daily_reasons rows without a stats_daily row.
	// Saves are now atomic (see UpsertDailySnapshot), but pre-existing
	// orphans must still surface — the table iterates stats_daily, so
	// without this union they stay historically invisible.
	seen := make(map[string]bool, len(v.Rows)+len(reasonDays))
	for _, r := range v.Rows {
		seen[r.Day] = true
	}
	for day := range reasonDays {
		if seen[day] {
			continue
		}
		// Today's live row (below) already covers today when live
		// counters are available.
		if day == today && s.stats != nil {
			continue
		}
		seen[day] = true
		v.Rows = append(v.Rows, statsRow{Day: day})
	}
	sort.Slice(v.Rows, func(i, j int) bool { return v.Rows[i].Day > v.Rows[j].Day })
	if s.stats != nil {
		live := s.stats()
		// Transport rejections never increment Received, so a live row
		// with only http_reject activity is all-zero in Stats. It must
		// still show today, otherwise an all-day auth flood disappears
		// until it becomes yesterday. Persisted reason rows keep today
		// visible across restarts when the session counters are still
		// zero.
		showLive := !isZeroIngestStats(live) || reasonDays[today]
		if !showLive && s.reasons != nil {
			showLive = hasLiveReasons(s.reasons())
		}
		if showLive {
			v.Rows = append([]statsRow{{Day: today, Live: true, Stats: live}}, v.Rows...)
		}
	}
	// The selected range's cap includes the live today row: without this, a
	// today without a persisted snapshot can yield one extra row.
	if len(v.Rows) > limit {
		v.Rows = v.Rows[:limit]
	}
	for _, r := range v.Rows {
		v.Total = addIngestStats(v.Total, r.Stats)
	}
	v.Chart, v.HasChart = buildStatsChart(v.Rows)
	d.S = v
	return nil
}

func dailyToIngest(s storage.DailyStats) ingest.Stats {
	return ingest.Stats{
		Received:            uint64(s.Received),
		Normalized:          uint64(s.Normalized),
		Stored:              uint64(s.Stored),
		Deduplicated:        uint64(s.Deduplicated),
		Rejected:            uint64(s.Rejected),
		IgnoredNotUsed:      uint64(s.IgnoredNotUsed),
		NormalizationErrors: uint64(s.NormalizationErrors),
		IngestionErrors:     uint64(s.IngestionErrors),
	}
}

func addIngestStats(a, b ingest.Stats) ingest.Stats {
	return ingest.Stats{
		Received:            a.Received + b.Received,
		Normalized:          a.Normalized + b.Normalized,
		Stored:              a.Stored + b.Stored,
		Deduplicated:        a.Deduplicated + b.Deduplicated,
		Rejected:            a.Rejected + b.Rejected,
		IgnoredNotUsed:      a.IgnoredNotUsed + b.IgnoredNotUsed,
		NormalizationErrors: a.NormalizationErrors + b.NormalizationErrors,
		IngestionErrors:     a.IngestionErrors + b.IngestionErrors,
	}
}

// isZeroIngestStats reports whether no record-level activity was counted.
// Transport rejections (http_reject) live only in the per-reason breakdown,
// so a zero Stats can still mean visible activity (see hasLiveReasons).
func isZeroIngestStats(s ingest.Stats) bool {
	return s == (ingest.Stats{})
}

// hasLiveReasons reports whether the live per-reason breakdown holds any
// non-zero counter.
func hasLiveReasons(rc ingest.ReasonCounts) bool {
	for _, reasons := range rc {
		for _, n := range reasons {
			if n > 0 {
				return true
			}
		}
	}
	return false
}

// buildStatsChart plots the problem counters (rejected, normalization and
// ingestion errors) per day, oldest first.
func buildStatsChart(rows []statsRow) (chartJSON, bool) {
	chart := chartJSON{}
	rejected := make([]any, 0, len(rows))
	normErrs := make([]any, 0, len(rows))
	ingErrs := make([]any, 0, len(rows))
	anyNonZero := false
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		t, err := time.Parse("2006-01-02", r.Day)
		if err != nil {
			continue
		}
		chart.Labels = append(chart.Labels, t.UnixMilli())
		rejected = append(rejected, r.Rejected)
		normErrs = append(normErrs, r.NormalizationErrors)
		ingErrs = append(ingErrs, r.IngestionErrors)
		if r.Rejected > 0 || r.NormalizationErrors > 0 || r.IngestionErrors > 0 {
			anyNonZero = true
		}
	}
	chart.Series = []chartSeries{
		{Name: "Rejected", Values: rejected},
		{Name: "Normalization errors", Values: normErrs},
		{Name: "Ingestion errors", Values: ingErrs},
	}
	return chart, anyNonZero
}

// sessions renders the sessions page: conversation cards on top, drilling
// down to a conversation's requests (or the flat all-requests list).
func (s *server) sessions(w http.ResponseWriter, r *http.Request) {
	d, err := s.sessionsData(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.render(w, "sessions", d)
}

func (s *server) fragSessionList(w http.ResponseWriter, r *http.Request) {
	d, err := s.sessionsData(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.renderFrag(w, "session-list", d)
}

func (s *server) sessionsData(r *http.Request) (*pageData, error) {
	f, u, err := parseFilter(r)
	if err != nil {
		return nil, badRequest{err}
	}
	order, err := orderParam(r)
	if err != nil {
		return nil, badRequest{err}
	}
	offset, err := offsetParam(r)
	if err != nil {
		return nil, badRequest{err}
	}
	view := "sessions"
	if u.Conversation != "" || r.URL.Query().Get("view") == "requests" {
		view = "requests"
	}
	d, err := s.base(r.Context(), "Sessions", "sessions", "/sessions", u)
	if err != nil {
		return nil, err
	}
	d.View = view
	d.SortOrder = order
	d.F.Hidden = []hiddenInput{{Name: "sort", Value: string(order)}}
	d.SortDesc = s.sortLinks(r, "desc")
	d.SortAsc = s.sortLinks(r, "asc")
	if view == "requests" {
		if u.Conversation == "" {
			// The hidden view input keeps the fragment's htmx re-fetches
			// (pager, filter changes) in the requests view.
			d.F.Hidden = append(d.F.Hidden, hiddenInput{Name: "view", Value: "requests"})
			d.SessionsTab = swapParamPageURL(r, "view", "")
			d.RequestsTab = ""
		} else {
			// Inside one conversation the tabs are noise.
			d.SessionsTab = ""
			d.RequestsTab = ""
		}
		rows, err := storage.RecentGenerations(r.Context(), s.db, f, order, recentLimit, offset)
		if err != nil {
			return nil, err
		}
		d.Recent = recentView{
			Rows:       rows,
			HasPrev:    offset > 0,
			PrevOffset: max(offset-recentLimit, 0),
			HasNext:    len(rows) == recentLimit,
			NextOffset: offset + recentLimit,
		}
		return d, nil
	}
	d.SessionsTab = ""
	d.RequestsTab = swapParamPageURL(r, "view", "requests")
	convos, total, err := storage.Conversations(r.Context(), s.db, f, order, conversationsLimit, offset)
	if err != nil {
		return nil, err
	}
	d.Convs = convsView{
		Rows:       convos,
		Total:      total,
		HasPrev:    offset > 0,
		PrevOffset: max(offset-conversationsLimit, 0),
		HasNext:    int64(offset+len(convos)) < total,
		NextOffset: offset + conversationsLimit,
	}
	return d, nil
}

func (s *server) sortLinks(r *http.Request, dir string) linkPair {
	return linkPair{
		Frag: "/fragments/session-list?" + swapParamQuery(r, "sort", dir).Encode(),
		Page: swapParamPageURL(r, "sort", dir),
	}
}

func swapParamQuery(r *http.Request, key, val string) url.Values {
	q := r.URL.Query()
	if val == "" {
		q.Del(key)
	} else {
		q.Set(key, val)
	}
	return q
}

func swapParamPageURL(r *http.Request, key, val string) string {
	return "/sessions?" + swapParamQuery(r, key, val).Encode()
}

// redirectSessions keeps old /generations bookmarks working.
func (s *server) redirectSessions(w http.ResponseWriter, r *http.Request) {
	target := "/sessions"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func (s *server) detail(w http.ResponseWriter, r *http.Request) {
	g, found, err := storage.GenerationByID(r.Context(), s.db, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	d := &pageData{Title: "Generation " + g.ID, Active: "sessions", D: g}
	s.render(w, "detail", d)
}

// base loads the shared filter view: presets for the page action plus the
// dropdown options from the Distinct* queries over the full range.
func (s *server) base(ctx context.Context, title, active, action string, u uiFilter) (*pageData, error) {
	d := &pageData{Title: title, Active: active}
	d.F.Action = action
	d.F.ShowDimensions = true
	d.F.Collapsible = true
	d.F.Selected = u
	d.F.Presets = presetViews(action, u)
	if u.Conversation != "" {
		d.F.ConversationURL = withoutConversationURL(action, u)
	}
	var err error
	if d.F.Sources, err = storage.DistinctSources(ctx, s.db, storage.Filter{}); err != nil {
		return nil, err
	}
	if d.F.Providers, err = storage.DistinctProviders(ctx, s.db, storage.Filter{}); err != nil {
		return nil, err
	}
	if d.F.Models, err = storage.DistinctModels(ctx, s.db, storage.Filter{}); err != nil {
		return nil, err
	}
	return d, nil
}

func buildTokenChart(pts []storage.TimeseriesPoint, bucket storage.Bucket) (chartJSON, bool) {
	c := chartJSON{Span: bucket.SpanSeconds()}
	var input, output, cacheRead, cacheCreate, reasoning []int64
	for _, p := range pts {
		c.Labels = append(c.Labels, p.BucketStart)
		input = append(input, p.InputTokens)
		output = append(output, p.OutputTokens)
		cacheRead = append(cacheRead, p.CacheReadTokens)
		cacheCreate = append(cacheCreate, p.CacheCreationTokens)
		reasoning = append(reasoning, p.ReasoningTokens)
	}
	c.Series = []chartSeries{
		{Name: "Input", Values: toAny(input)},
		{Name: "Output", Values: toAny(output)},
		{Name: "Cache read", Values: toAny(cacheRead)},
		{Name: "Cache creation", Values: toAny(cacheCreate)},
		{Name: "Reasoning", Values: toAny(reasoning)},
	}
	return c, len(pts) > 0
}

// buildCostChart includes reported and estimated costs. Unknown-only buckets
// remain gaps; an idle bucket is a known zero for the chart's timeline.
func buildCostChart(pts []storage.TimeseriesPoint, bucket storage.Bucket) (chartJSON, bool) {
	c := chartJSON{Span: bucket.SpanSeconds()}
	vals := make([]any, 0, len(pts))
	anyCost := false
	for _, p := range pts {
		c.Labels = append(c.Labels, p.BucketStart)
		switch {
		case p.CostTotal != nil:
			vals = append(vals, *p.CostTotal)
			anyCost = true
		case p.Requests == 0:
			vals = append(vals, float64(0))
		default:
			vals = append(vals, nil)
		}
	}
	if !anyCost {
		return chartJSON{}, false
	}
	c.Series = []chartSeries{{Name: "Cost", Fmt: "cost", Values: vals}}
	return c, true
}

// buildSourceChart renders one line of total tokens per source per bucket.
func buildSourceChart(pts []storage.TimeseriesSourcePoint, totals []storage.TimeseriesPoint, bucket storage.Bucket) (chartJSON, bool) {
	if len(pts) == 0 {
		return chartJSON{}, false
	}
	c := chartJSON{Span: bucket.SpanSeconds()}
	bySource := map[string]map[int64]int64{}
	labels := make([]int64, 0, len(totals))
	for _, p := range totals {
		labels = append(labels, p.BucketStart)
	}
	for _, p := range pts {
		if bySource[p.Source] == nil {
			bySource[p.Source] = map[int64]int64{}
		}
		bySource[p.Source][p.BucketStart] = p.TotalTokens()
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i] < labels[j] })
	sources := make([]string, 0, len(bySource))
	for src := range bySource {
		sources = append(sources, src)
	}
	sort.Strings(sources)
	c = chartJSON{Labels: labels, Span: bucket.SpanSeconds()}
	for _, src := range sources {
		vals := make([]any, len(labels))
		for i, l := range labels {
			vals[i] = bySource[src][l] // absent buckets intentionally render as zero
		}
		c.Series = append(c.Series, chartSeries{Name: friendlySource(src), Values: vals})
	}
	return c, true
}

// fillTimeseries inserts empty buckets across the selected range. This keeps
// sparse hourly data honest: an idle hour is zero, not a line drawn directly
// between two distant requests. Very large explicit ranges are left sparse to
// keep fragment responses bounded; the UI disables those combinations.
func fillTimeseries(pts []storage.TimeseriesPoint, f storage.Filter, bucket storage.Bucket, now time.Time) []storage.TimeseriesPoint {
	if len(pts) == 0 {
		return pts
	}
	from := f.From
	if from.IsZero() {
		from = time.UnixMilli(pts[0].BucketStart).UTC()
	}
	to := f.To
	if to.IsZero() || to.After(now) {
		to = now
	}
	start := chartBucketStart(from, bucket)
	last := chartBucketStart(to.Add(-time.Nanosecond), bucket)
	if last.Before(start) {
		return pts
	}
	count := 0
	for t := start; !t.After(last) && count <= 1000; t = nextChartBucket(t, bucket) {
		count++
	}
	if count > 1000 {
		return pts
	}
	byStart := make(map[int64]storage.TimeseriesPoint, len(pts))
	for _, p := range pts {
		byStart[p.BucketStart] = p
	}
	out := make([]storage.TimeseriesPoint, 0, count)
	for t := start; !t.After(last); t = nextChartBucket(t, bucket) {
		if p, ok := byStart[t.UnixMilli()]; ok {
			out = append(out, p)
		} else {
			out = append(out, storage.TimeseriesPoint{BucketStart: t.UnixMilli()})
		}
	}
	return out
}

func chartBucketStart(t time.Time, bucket storage.Bucket) time.Time {
	t = t.UTC()
	switch bucket {
	case storage.BucketHour:
		return t.Truncate(time.Hour)
	case storage.BucketDay:
		return startOfDay(t)
	case storage.BucketWeek:
		return startOfWeek(t)
	default:
		return startOfMonth(t)
	}
}

func nextChartBucket(t time.Time, bucket storage.Bucket) time.Time {
	switch bucket {
	case storage.BucketHour:
		return t.Add(time.Hour)
	case storage.BucketDay:
		return t.AddDate(0, 0, 1)
	case storage.BucketWeek:
		return t.AddDate(0, 0, 7)
	default:
		return t.AddDate(0, 1, 0)
	}
}

// buildCacheChart renders the aggregate cache hit rate per bucket as a
// percentage line; nil gaps where no prompt tokens were reported.
func buildCacheChart(pts []storage.TimeseriesPoint, bucket storage.Bucket) (chartJSON, bool) {
	c := chartJSON{Span: bucket.SpanSeconds()}
	anyRate := false
	vals := make([]any, 0, len(pts))
	for _, p := range pts {
		c.Labels = append(c.Labels, p.BucketStart)
		if rate := storage.CacheHitRate(p.InputTokens, p.CacheReadTokens, p.CacheCreationTokens); rate != nil {
			anyRate = true
			vals = append(vals, *rate*100)
		} else {
			vals = append(vals, nil)
		}
	}
	if !anyRate {
		return chartJSON{}, false
	}
	c.Series = []chartSeries{{Name: "Cache hit rate", Fmt: "pct", SpanGaps: true, Values: vals}}
	return c, len(pts) > 0
}

func toAny(v []int64) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = x
	}
	return out
}

func (s *server) render(w http.ResponseWriter, name string, d *pageData) {
	d.ShowLogout = s.dash != nil
	d.Version = s.version
	renderTemplate(w, pageTmpls[name], "layout", http.StatusOK, d)
}

func (s *server) renderFrag(w http.ResponseWriter, name string, d *pageData) {
	d.ShowLogout = s.dash != nil
	d.Version = s.version
	renderTemplate(w, fragTmpls[name], name, http.StatusOK, d)
}

type badRequest struct{ err error }

func (b badRequest) Error() string { return b.err.Error() }

// writeErr answers request-input problems with 400 (detail echoed back)
// and query/DB failures with 500: the real error is logged server-side and
// the response body is generic so internals never reach the client.
func writeErr(w http.ResponseWriter, err error) {
	if _, ok := err.(badRequest); ok {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slog.Error("page request failed", "error", err.Error())
	http.Error(w, "internal error", http.StatusInternalServerError)
}
