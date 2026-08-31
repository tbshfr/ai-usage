package web

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
)

// uiFilter is the filter state echoed back to the templates: the selected
// range preset plus the dropdown values (same query params as the JSON API).
// Conversation mirrors the storage filter; the "none" sentinel selects
// requests without a conversation ID (title generations).
type uiFilter struct {
	Range        string
	Source       string
	Provider     string
	Model        string
	Conversation string
}

var rangeKeys = []struct{ key, label string }{
	{"today", "Today"},
	{"7d", "7d"},
	{"30d", "30d"},
	{"month", "This month"},
	{"all", "All"},
}

// parseFilter reads range/source/provider/model plus the API's from/to
// params. Explicit from/to win over the range preset.
func parseFilter(r *http.Request) (storage.Filter, uiFilter, error) {
	q := r.URL.Query()
	u := uiFilter{
		Range:        q.Get("range"),
		Source:       q.Get("source"),
		Provider:     q.Get("provider"),
		Model:        q.Get("model"),
		Conversation: q.Get("conversation"),
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
		case "", "all":
		case "today":
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

func bucketParam(w http.ResponseWriter, r *http.Request) (storage.Bucket, bool) {
	b := storage.Bucket(r.URL.Query().Get("bucket"))
	if b == "" {
		b = storage.BucketDay
	}
	switch b {
	case storage.BucketDay, storage.BucketWeek, storage.BucketMonth:
		return b, true
	default:
		http.Error(w, fmt.Sprintf("invalid bucket %q (want day, week, or month)", b), http.StatusBadRequest)
		return "", false
	}
}

func offsetParam(r *http.Request) (int, error) {
	v := r.URL.Query().Get("offset")
	if v == "" {
		return 0, nil
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 0 {
		return 0, fmt.Errorf("invalid offset %q (want a non-negative integer)", v)
	}
	return n, nil
}

func presetViews(action string, u uiFilter) []presetView {
	out := make([]presetView, 0, len(rangeKeys))
	for _, k := range rangeKeys {
		q := url.Values{}
		q.Set("range", k.key)
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
		active := u.Range == k.key || (k.key == "all" && (u.Range == "" || u.Range == "all"))
		out = append(out, presetView{
			Label:  k.label,
			URL:    action + "?" + q.Encode(),
			Active: active,
		})
	}
	return out
}

// conversationURL builds a /generations link that keeps the current filters
// but switches the conversation filter to conv (storage.ConversationNone for
// rows without a conversation ID).
func conversationURL(u uiFilter, conv string) string {
	q := url.Values{}
	if u.Range != "" && u.Range != "all" {
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
	return "/generations?" + q.Encode()
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
