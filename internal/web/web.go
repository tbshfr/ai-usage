// Package web serves the server-rendered dashboard: pages and HTMX
// fragments that query the Phase 4 storage layer directly (no HTTP-to-self).
package web

import (
	"context"
	"database/sql"

	"io/fs"
	"net/http"
	"time"

	"github.com/tbshfr/ai-usage"
	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

const recentLimit = 50

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
	mux.HandleFunc("GET /{$}", s.overview)
	mux.HandleFunc("GET /breakdowns", s.breakdowns)
	mux.HandleFunc("GET /generations", s.recent)
	mux.HandleFunc("GET /generations/{id}", s.detail)
	mux.HandleFunc("GET /fragments/overview-cards", s.fragOverviewCards)
	mux.HandleFunc("GET /fragments/timeseries", s.fragTimeseries)
	mux.HandleFunc("GET /fragments/breakdowns", s.fragBreakdowns)
	mux.HandleFunc("GET /fragments/recent-rows", s.fragRecentRows)
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
	Title      string
	Active     string
	ShowLogout bool
	Error      string
	F          filterView
	Cards      []cardView
	Chart      chartView
	Recent     recentView
	Breaks     breaksView
	D          *normalize.Generation
}

type filterView struct {
	Action          string
	Presets         []presetView
	Sources         []string
	Providers       []string
	Models          []string
	Selected        uiFilter
	ConversationURL string // current page minus the conversation filter, "" when no conversation filter is set
}

type presetView struct {
	Label  string
	URL    string
	Active bool
}

type cardView struct {
	Label string
	S     storage.SummaryResult
}

type chartView struct {
	Bucket  storage.Bucket
	HasData bool
	Data    chartJSON
}

type chartJSON struct {
	Labels []int64       `json:"labels"`
	Series []chartSeries `json:"series"`
}

type chartSeries struct {
	Name   string `json:"name"`
	Scale  string `json:"scale,omitempty"`
	Values []any  `json:"values"`
}

type recentView struct {
	Rows       []normalize.Generation
	HasPrev    bool
	PrevOffset int
	HasNext    bool
	NextOffset int
}

type breaksView struct {
	Source, Provider, Model []storage.Breakdown
}

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	f, u, err := parseFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	bucket, ok := bucketParam(w, r)
	if !ok {
		return
	}
	d, err := s.base(r.Context(), "Overview", "overview", "/", u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d.Cards, err = s.cards(r.Context(), u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pts, err := storage.Timeseries(r.Context(), s.db, f, bucket)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.Chart = buildChart(bucket, pts)
	s.render(w, "overview", d)
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
	s.render(w, "breakdowns", d)
}

func (s *server) recent(w http.ResponseWriter, r *http.Request) {
	d, err := s.recentData(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.render(w, "recent", d)
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
	d := &pageData{Title: "Generation " + g.ID, Active: "recent", D: g}
	s.render(w, "detail", d)
}

func (s *server) recentData(r *http.Request) (*pageData, error) {
	f, u, err := parseFilter(r)
	if err != nil {
		return nil, badRequest{err}
	}
	offset, err := offsetParam(r)
	if err != nil {
		return nil, badRequest{err}
	}
	d, err := s.base(r.Context(), "Recent", "recent", "/generations", u)
	if err != nil {
		return nil, err
	}
	rows, err := storage.RecentGenerations(r.Context(), s.db, f, recentLimit, offset)
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

func (s *server) fragOverviewCards(w http.ResponseWriter, r *http.Request) {
	_, u, err := parseFilter(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.base(r.Context(), "Overview", "overview", "/", u)
	if err != nil {
		writeErr(w, err)
		return
	}
	if d.Cards, err = s.cards(r.Context(), u); err != nil {
		writeErr(w, err)
		return
	}
	s.renderFrag(w, "overview-cards", d)
}

func (s *server) fragTimeseries(w http.ResponseWriter, r *http.Request) {
	f, u, err := parseFilter(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	bucket, ok := bucketParam(w, r)
	if !ok {
		return
	}
	d, err := s.base(r.Context(), "Overview", "overview", "/", u)
	if err != nil {
		writeErr(w, err)
		return
	}
	pts, err := storage.Timeseries(r.Context(), s.db, f, bucket)
	if err != nil {
		writeErr(w, err)
		return
	}
	d.Chart = buildChart(bucket, pts)
	s.renderFrag(w, "timeseries", d)
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
	s.renderFrag(w, "breakdowns", d)
}

func (s *server) fragRecentRows(w http.ResponseWriter, r *http.Request) {
	d, err := s.recentData(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.renderFrag(w, "recent-rows", d)
}

func (s *server) cards(ctx context.Context, u uiFilter) ([]cardView, error) {
	now := time.Now().UTC()
	ranges := []struct {
		label string
		from  time.Time
	}{
		{"Today", startOfDay(now)},
		{"This week", startOfWeek(now)},
		{"This month", startOfMonth(now)},
		{"All time", time.Time{}},
	}
	out := make([]cardView, 0, len(ranges))
	for _, rg := range ranges {
		s, err := storage.Summary(ctx, s.db, storage.Filter{
			From:         rg.from,
			Source:       u.Source,
			Provider:     u.Provider,
			Model:        u.Model,
			Conversation: u.Conversation,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, cardView{Label: rg.label, S: s})
	}
	return out, nil
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

func buildChart(bucket storage.Bucket, pts []storage.TimeseriesPoint) chartView {
	c := chartView{Bucket: bucket}
	anyCost := false
	for _, p := range pts {
		if p.CostKnownCount > 0 {
			anyCost = true
			break
		}
	}
	var input, output, cacheRead, cacheCreate, reasoning []int64
	var cost []any
	for _, p := range pts {
		c.HasData = true
		c.Data.Labels = append(c.Data.Labels, p.BucketStart)
		input = append(input, p.InputTokens)
		output = append(output, p.OutputTokens)
		cacheRead = append(cacheRead, p.CacheReadTokens)
		cacheCreate = append(cacheCreate, p.CacheCreationTokens)
		reasoning = append(reasoning, p.ReasoningTokens)
		if anyCost {
			if p.CostKnownCount > 0 && p.CostTotal != nil {
				cost = append(cost, *p.CostTotal)
			} else {
				cost = append(cost, nil)
			}
		}
	}
	c.Data.Series = []chartSeries{
		{Name: "Input", Values: toAny(input)},
		{Name: "Output", Values: toAny(output)},
		{Name: "Cache read", Values: toAny(cacheRead)},
		{Name: "Cache creation", Values: toAny(cacheCreate)},
		{Name: "Reasoning", Values: toAny(reasoning)},
	}
	if anyCost {
		c.Data.Series = append(c.Data.Series, chartSeries{
			Name:   "Cost (reported only)",
			Scale:  "cost",
			Values: cost,
		})
	}
	return c
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
