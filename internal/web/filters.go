package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
)

// uiFilter is the filter state echoed back to the templates: the selected
// range preset plus the dropdown values (same query params as the JSON API).
// Conversation mirrors the storage filter; besides real conversation IDs it
// accepts the sentinels none (every session-less row), autocomplete (VS Code
// autocomplete), and titleprogress (title/progress helpers).
type uiFilter struct {
	Range        string
	Source       string
	Provider     string
	Model        string
	Conversation string
	FromParam    string
	ToParam      string
	PodiumMetric string // set only for Trends after validating the metric
}

var rangeKeys = []struct{ key, label string }{
	{"today", "Today"},
	{"7d", "7d"},
	{"30d", "30d"},
	{"month", "This month"},
	{"all", "All"},
}

// parseFilter reads range/source/provider/model plus the API's from/to
// params. Explicit from/to win over the range preset. An empty range
// defaults to today; "all" removes the time bound.
func parseFilter(r *http.Request) (storage.Filter, uiFilter, error) {
	q := r.URL.Query()
	u := uiFilter{
		Range:        q.Get("range"),
		Source:       q.Get("source"),
		Provider:     q.Get("provider"),
		Model:        q.Get("model"),
		Conversation: q.Get("conversation"),
		FromParam:    q.Get("from"),
		ToParam:      q.Get("to"),
	}
	f := storage.Filter{Source: u.Source, Provider: u.Provider, Model: u.Model, Conversation: u.Conversation}
	var err error
	if f.From, err = timeParam(q.Get("from"), "from"); err != nil {
		return f, u, err
	}
	if f.To, err = timeParam(q.Get("to"), "to"); err != nil {
		return f, u, err
	}
	if f.From.IsZero() && f.To.IsZero() {
		now := time.Now().UTC()
		switch u.Range {
		case "all":
		case "", "today":
			f.From = startOfDay(now)
		case "7d":
			f.From = now.Add(-7 * 24 * time.Hour)
		case "30d":
			f.From = now.Add(-30 * 24 * time.Hour)
		case "month":
			f.From = startOfMonth(now)
		default:
			return f, u, badRequest{fmt.Errorf("invalid range %q (want today, 7d, 30d, month, or all)", u.Range)}
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return f, u, badRequest{fmt.Errorf("invalid filter: to (%s) before from (%s)", q.Get("to"), q.Get("from"))}
	}
	return f, u, nil
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
	return time.Time{}, badRequest{fmt.Errorf("invalid %s %q (want RFC3339 or YYYY-MM-DD)", name, v)}
}

func bucketParam(r *http.Request) (storage.Bucket, error) {
	b := storage.Bucket(r.URL.Query().Get("bucket"))
	if b == "" {
		b = storage.BucketDay
	}
	switch b {
	case storage.BucketHour, storage.BucketDay, storage.BucketWeek, storage.BucketMonth:
		return b, nil
	default:
		return "", badRequest{fmt.Errorf("invalid bucket %q (want hour, day, week, or month)", b)}
	}
}

// trendBucketParam chooses a useful chart resolution when the URL does not
// specify one. An explicit bucket is still honored so API/fragment behavior
// remains backwards compatible.
func trendBucketParam(r *http.Request, f storage.Filter) (storage.Bucket, error) {
	if r.URL.Query().Get("bucket") != "" {
		return bucketParam(r)
	}
	if f.From.IsZero() {
		return storage.BucketMonth, nil
	}
	to := f.To
	if to.IsZero() {
		to = time.Now().UTC()
	}
	d := to.Sub(f.From)
	switch {
	case d <= 48*time.Hour:
		return storage.BucketHour, nil
	case d <= 62*24*time.Hour:
		return storage.BucketDay, nil
	case d <= 2*365*24*time.Hour:
		return storage.BucketWeek, nil
	default:
		return storage.BucketMonth, nil
	}
}

func trendBucketOptions(f storage.Filter, selected storage.Bucket) []bucketOption {
	to := f.To
	if to.IsZero() {
		to = time.Now().UTC()
	}
	d := time.Duration(1<<63 - 1)
	if !f.From.IsZero() {
		d = to.Sub(f.From)
	}
	return []bucketOption{
		// Small grace periods avoid disabling an option just because separate
		// calls to time.Now made a nominal 7-day range a few milliseconds longer.
		{Value: storage.BucketHour, Label: "Hour", Disabled: d > 7*24*time.Hour+time.Minute && selected != storage.BucketHour},
		{Value: storage.BucketDay, Label: "Day", Disabled: d > 180*24*time.Hour && selected != storage.BucketDay},
		{Value: storage.BucketWeek, Label: "Week", Disabled: d > 2*365*24*time.Hour && selected != storage.BucketWeek},
		{Value: storage.BucketMonth, Label: "Month"},
	}
}

// orderParam reads the asc/desc sort direction for list views; empty means
// newest first.
func orderParam(r *http.Request) (storage.Order, error) {
	o, err := storage.ParseOrder(r.URL.Query().Get("sort"))
	if err != nil {
		return "", badRequest{err}
	}
	return o, nil
}

func offsetParam(r *http.Request) (int, error) {
	v := r.URL.Query().Get("offset")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid offset %q (want a non-negative integer)", v)
	}
	return n, nil
}

func presetViews(action string, u uiFilter) []presetView {
	out := make([]presetView, 0, len(rangeKeys))
	for _, k := range rangeKeys {
		q := url.Values{}
		// The today pill links to the bare action: an empty range already
		// means today, so the default state keeps a canonical URL instead
		// of growing a redundant ?range=today on first click.
		if k.key != "today" {
			q.Set("range", k.key)
		}
		if u.Source != "" {
			q.Set("source", u.Source)
		}
		if u.Provider != "" {
			q.Set("provider", u.Provider)
		}
		if u.Model != "" {
			q.Set("model", u.Model)
		}
		if u.Conversation != "" {
			q.Set("conversation", u.Conversation)
		}
		if u.PodiumMetric != "" {
			q.Set("podium_metric", u.PodiumMetric)
		}
		active := u.Range == k.key || (k.key == "today" && u.Range == "")
		link := action
		if enc := q.Encode(); enc != "" {
			link += "?" + enc
		}
		out = append(out, presetView{
			Label:  k.label,
			URL:    link,
			Active: active,
		})
	}
	return out
}

func statsPresetViews(u uiFilter) []presetView {
	out := presetViews("/stats", u)
	for i := range out {
		switch out[i].Label {
		case "Today":
			out[i].URL = "/stats?range=today"
		case "7d":
			out[i].URL = "/stats"
		}
	}
	return out
}

// statsRangeParam maps the stats page's day presets to inclusive UTC dates.
// Unlike the shared API filter, its empty UI state intentionally means seven
// calendar days so /stats opens with a useful operational overview.
func statsRangeParam(r *http.Request) (uiFilter, string, string, int, error) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "7d"
	}
	u := uiFilter{Range: rng}
	today := startOfDay(time.Now().UTC())
	from := today
	limit := 1
	switch rng {
	case "today":
	case "7d":
		from, limit = today.AddDate(0, 0, -6), 7
	case "30d":
		from, limit = today.AddDate(0, 0, -29), 30
	case "month":
		from, limit = startOfMonth(today), today.Day()
	case "all":
		return u, "", today.Format("2006-01-02"), statsDaysLimit, nil
	default:
		return u, "", "", 0, badRequest{fmt.Errorf("invalid range %q (want today, 7d, 30d, month, or all)", rng)}
	}
	return u, from.Format("2006-01-02"), today.Format("2006-01-02"), limit, nil
}

// conversationURL builds a /sessions link that keeps the current filters
// but switches the conversation filter to conv (storage.ConversationNone for
// rows without a conversation ID). The range is carried over verbatim,
// including "all": since an empty range now means "today", dropping it
// would silently narrow the result.
func conversationURL(u uiFilter, conv string) string {
	q := url.Values{}
	if u.Range != "" {
		q.Set("range", u.Range)
	}
	if u.Source != "" {
		q.Set("source", u.Source)
	}
	if u.Provider != "" {
		q.Set("provider", u.Provider)
	}
	if u.Model != "" {
		q.Set("model", u.Model)
	}
	q.Set("conversation", conv)
	return "/sessions?" + q.Encode()
}

// withoutConversationURL builds the current page URL minus the conversation
// filter, used by the clear chip in the filter bar.
func withoutConversationURL(action string, u uiFilter) string {
	q := url.Values{}
	if u.Range != "" {
		q.Set("range", u.Range)
	}
	if u.Source != "" {
		q.Set("source", u.Source)
	}
	if u.Provider != "" {
		q.Set("provider", u.Provider)
	}
	if u.Model != "" {
		q.Set("model", u.Model)
	}
	if u.FromParam != "" {
		q.Set("from", u.FromParam)
	}
	if u.ToParam != "" {
		q.Set("to", u.ToParam)
	}
	if u.PodiumMetric != "" {
		q.Set("podium_metric", u.PodiumMetric)
	}
	return action + "?" + q.Encode()
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func startOfWeek(t time.Time) time.Time {
	day := startOfDay(t)
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
}

func startOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}
