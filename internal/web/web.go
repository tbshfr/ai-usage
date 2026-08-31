// Package web serves the server-rendered dashboard: pages and HTMX
// fragments that query the Phase 4 storage layer directly (no HTTP-to-self).
package web

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/tbshfr/ai-usage"
	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

const (
	recentLimit        = 50
	conversationsLimit = 24
)

var staticFS = func() fs.FS {
	sub, err := fs.Sub(assets.Static, "web/static")
	if err != nil {
		panic(err)
	}
	return sub
}()

// New returns the dashboard UI routes (pages, fragments, static assets).
// Templates are parsed once at package init from the embedded FS.
func New(db *sql.DB) http.Handler {
	return newMux(db, nil)
}

// NewAuthed adds the login/logout routes and applies the dashboard
// guard to every UI route; api.NewWithAuth additionally wraps the whole
// dashboard port so /api/* is protected as well.
func NewAuthed(db *sql.DB, dash *auth.Dashboard) http.Handler {
	return dash.Middleware(newMux(db, dash))
}

func newMux(db *sql.DB, dash *auth.Dashboard) http.Handler {
	s := &server{db: db, dash: dash}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /trends", s.trends)
	mux.HandleFunc("GET /breakdowns", s.breakdowns)
	mux.HandleFunc("GET /sessions", s.sessions)
	mux.HandleFunc("GET /generations", s.redirectSessions)
	mux.HandleFunc("GET /generations/{id}", s.detail)
	mux.HandleFunc("GET /fragments/dashboard-stats", s.fragDashboardStats)
	mux.HandleFunc("GET /fragments/period-detail", s.fragPeriodDetail)
	mux.HandleFunc("GET /fragments/trends", s.fragTrends)
	mux.HandleFunc("GET /fragments/breakdowns", s.fragBreakdowns)
	mux.HandleFunc("GET /fragments/session-list", s.fragSessionList)
	mux.Handle("GET /static/{path...}", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
	if dash != nil {
		mux.HandleFunc("GET /login", s.loginForm)
		mux.HandleFunc("POST /login", s.loginSubmit)
		mux.HandleFunc("GET /logout", s.logout)
	}
	return mux
}

type server struct {
	db   *sql.DB
	dash *auth.Dashboard
}

// pageData is the single view model passed to every template set; each
// template only reads the fields it needs.
type pageData struct {
	Title       string
	Active      string
	ShowLogout  bool
	Error       string
	F           filterView
	Cards       []cardView
	Detail      *periodDetailView
	Charts      trendsView
	Convs       convsView
	Recent      recentView
	Breaks      breaksView
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
type cardView struct {
	Period string // today | week | month | all
	Label  string
	Hero   bool
	S      storage.SummaryResult
}

// periodDetailView is the inline expansion under the dashboard cards.
type periodDetailView struct {
	Period string
	Label  string
	S      storage.SummaryResult
}

type trendsView struct {
	Bucket     storage.Bucket
	TokenData  chartJSON
	HasTokens  bool
	SourceData chartJSON
	HasSource  bool
	CacheData  chartJSON
	HasCache   bool
}

type chartJSON struct {
	Labels []int64       `json:"labels"`
	Series []chartSeries `json:"series"`
	// Span is the nominal bucket width in seconds; the chart pads the time
	// axis to at least one span so single-bucket ranges still scale.
	Span int64 `json:"span,omitempty"`
}

type chartSeries struct {
	Name   string `json:"name"`
	Fmt    string `json:"fmt,omitempty"` // "pct" appends % to values; cost keeps $ formatting
	Values []any  `json:"values"`
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.F.Presets = nil
	if d.Cards, err = s.cards(r.Context(), u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	if d.Cards, err = s.cards(r.Context(), u); err != nil {
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
	from, label, ok := periodStart(period, time.Now().UTC())
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
	s.renderFrag(w, "period-detail", d)
}

func periodStart(period string, now time.Time) (time.Time, string, bool) {
	switch period {
	case "today":
		return startOfDay(now), "Today", true
	case "week":
		return startOfWeek(now), "This week", true
	case "month":
		return startOfMonth(now), "This month", true
	case "all":
		return time.Time{}, "All time", true
	}
	return time.Time{}, "", false
}

func (s *server) cards(ctx context.Context, u uiFilter) ([]cardView, error) {
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
	return out, nil
}

// trends renders the charts page: tokens over time, tokens by source, and
// the cache hit rate over time.
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
	bucket, err := bucketParam(r)
	if err != nil {
		return nil, err
	}
	d, err := s.base(r.Context(), "Trends", "trends", "/trends", u)
	if err != nil {
		return nil, err
	}
	d.Charts.Bucket = bucket
	pts, err := storage.Timeseries(r.Context(), s.db, f, bucket)
	if err != nil {
		return nil, err
	}
	d.Charts.TokenData, d.Charts.HasTokens = buildTokenChart(pts, bucket)
	spts, err := storage.TimeseriesBySource(r.Context(), s.db, f, bucket)
	if err != nil {
		return nil, err
	}
	d.Charts.SourceData, d.Charts.HasSource = buildSourceChart(spts, bucket)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d.Breaks.Source, err = storage.BySource(r.Context(), s.db, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d.Breaks.Provider, err = storage.ByProvider(r.Context(), s.db, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d.Breaks.Model, err = storage.ByModel(r.Context(), s.db, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

// buildSourceChart renders one line of total tokens per source per bucket.
func buildSourceChart(pts []storage.TimeseriesSourcePoint, bucket storage.Bucket) (chartJSON, bool) {
	if len(pts) == 0 {
		return chartJSON{}, false
	}
	c := chartJSON{Span: bucket.SpanSeconds()}
	bySource := map[string]map[int64]int64{}
	labels := []int64{}
	seen := map[int64]bool{}
	for _, p := range pts {
		if !seen[p.BucketStart] {
			seen[p.BucketStart] = true
			labels = append(labels, p.BucketStart)
		}
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
			vals[i] = bySource[src][l]
		}
		c.Series = append(c.Series, chartSeries{Name: friendlySource(src), Values: vals})
	}
	return c, true
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
	c.Series = []chartSeries{{Name: "Cache hit rate", Fmt: "pct", Values: vals}}
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pageTmpls[name].ExecuteTemplate(w, "layout", d)
}

func (s *server) renderFrag(w http.ResponseWriter, name string, d *pageData) {
	d.ShowLogout = s.dash != nil
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = fragTmpls[name].ExecuteTemplate(w, name, d)
}

type badRequest struct{ err error }

func (b badRequest) Error() string { return b.err.Error() }

// writeErr answers request-input problems with 400 and query/DB failures
// with 500.
func writeErr(w http.ResponseWriter, err error) {
	if _, ok := err.(badRequest); ok {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
