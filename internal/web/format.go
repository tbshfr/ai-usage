package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tbshfr/ai-usage"
	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

const emDash = "—"

var funcs = template.FuncMap{
	"commas":           commas,
	"tokens":           tokens,
	"cost":             cost,
	"costLine":         costLine,
	"costCell":         costCell,
	"dur":              dur,
	"utc":              utc,
	"utcTime":          utcTime,
	"utcDate":          utcDate,
	"friendlySource":   friendlySource,
	"creator":          normalize.ModelCreator,
	"copyID":           copyID,
	"toJSON":           toJSON,
	"pct":              pct,
	"shortConv":        shortConv,
	"conversationLink": conversationLink,
	"convHref":         convHref,
	"convTitle":        convTitle,
	"convSub":          convSub,
}

var pageTmpls = map[string]*template.Template{
	"login":      mustParse("login.html"),
	"dashboard":  mustParse("layout.html", "filterbar.html", "cards.html", "dashboard.html"),
	"trends":     mustParse("layout.html", "filterbar.html", "chart.html", "trends_page.html"),
	"breakdowns": mustParse("layout.html", "filterbar.html", "breakdowns.html", "breakdowns_page.html"),
	"sessions":   mustParse("layout.html", "filterbar.html", "session_list.html", "conversations.html", "rows.html", "sessions_page.html"),
	"detail":     mustParse("layout.html", "detail.html"),
	"stats":      mustParse("layout.html", "filterbar.html", "stats_page.html", "stats.html"),
}

// fragTmpls render bare page sections (no layout); the same named templates
// are included by the page sets, so a fragment is always also a
// full-HTML-renderable page section.
var fragTmpls = map[string]*template.Template{
	"dashboard-stats": mustParse("cards.html"),
	"period-detail":   mustParse("cards.html"),
	"trends":          mustParse("chart.html"),
	"breakdowns":      mustParse("breakdowns.html"),
	"session-list":    mustParse("session_list.html", "conversations.html", "rows.html"),
	"stats":           mustParse("stats.html"),
	"stats-reasons":   mustParse("stats-reasons.html"),
}

func mustParse(files ...string) *template.Template {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = "web/templates/" + f
	}
	t, err := template.New("layout").Funcs(funcs).ParseFS(assets.Templates, paths...)
	if err != nil {
		panic(err)
	}
	return t
}

// commas renders an integer with thousands separators. It accepts the
// int64 counts used by the storage layer and the uint64 counters from the
// ingest pipeline.
func commas(v any) string {
	var s string
	switch n := v.(type) {
	case int64:
		s = strconv.FormatInt(n, 10)
	case uint64:
		s = strconv.FormatUint(n, 10)
	case int:
		s = strconv.Itoa(n)
	default:
		return emDash
	}
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		b.WriteByte('-')
	}
	return b.String()
}

// tokens renders a nullable token count: nil → em dash.
func tokens(v any) string {
	switch n := v.(type) {
	case *int64:
		if n == nil {
			return emDash
		}
		return commas(*n)
	case int64:
		return commas(n)
	default:
		return emDash
	}
}

// cost renders a nullable cost: $1.2345 (4 decimals below $10, 2 otherwise).
func cost(v *float64) string {
	if v == nil {
		return emDash
	}
	return "$" + strconv.FormatFloat(*v, 'f', costDecimals(*v), 64)
}

func costDecimals(v float64) int {
	if v < 10 {
		return 4
	}
	return 2
}

// costLine is the card-level cost line: only meaningful when at least one
// row in range reported cost; all-unknown renders "—", never "$0.00".
func costLine(known, unknown int64, total *float64) string {
	if known == 0 {
		return emDash
	}
	return "≈ " + cost(total) + fmt.Sprintf(" (%d known, %d without cost data)", known, unknown)
}

// costCell is the table-level cost cell: total when any row reported cost.
func costCell(known int64, total *float64) string {
	if known == 0 {
		return emDash
	}
	return cost(total)
}

func dur(d time.Duration) string {
	if d <= 0 {
		return emDash
	}
	if d < time.Minute {
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	}
	m := int(d / time.Minute)
	s := int(math.Round(d.Seconds())) % 60
	return strconv.Itoa(m) + "m " + strconv.Itoa(s) + "s"
}

func utc(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// utcTime is the clock part of a timestamp: "12:15".
func utcTime(t time.Time) string {
	return t.UTC().Format("15:04")
}

// utcDate is the UTC date of a timestamp: "2026-03-02".
func utcDate(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func friendlySource(s string) string {
	switch s {
	case "opencode":
		return "OpenCode"
	case "copilot":
		return "VS Code Copilot"
	case "codex":
		return "Codex"
	default:
		return s
	}
}

// copyID renders a click-to-copy code element, or an em dash when empty.
func copyID(s string) template.HTML {
	if s == "" {
		return emDash
	}
	esc := template.HTMLEscapeString(s)
	return template.HTML(`<code class="copy" data-copy="` + esc + `" title="Click to copy">` + esc + `</code>`)
}

func toJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return template.JS(b)
}

// pct renders a nullable rate (0..1) as a percentage, or an em dash when nil.
func pct(v *float64) string {
	if v == nil {
		return emDash
	}
	return strconv.FormatFloat(*v*100, 'f', 1, 64) + "%"
}

// shortConv truncates a conversation ID for table display.
func shortConv(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:10] + "…"
}

// conversationLink renders an anchor filtering Sessions by conversation; conv
// "" becomes the "none" filter (title generations and similar).
func conversationLink(u uiFilter, conv string) template.HTML {
	href := convHref(u, conv)
	if conv == "" {
		conv = storage.ConversationNone
	}
	label := "other"
	if conv != storage.ConversationNone {
		label = template.HTMLEscapeString(shortConv(conv))
	}
	return template.HTML(`<a class="conv" title="Show this conversation's requests" href="` + href + `">` + label + `</a>`)
}

// convHref builds the /sessions drill-down URL for a conversation key; ""
// (the other-groups) becomes the "none" sentinel filter.
func convHref(u uiFilter, key string) string {
	if key == "" {
		key = storage.ConversationNone
	}
	return conversationURL(u, key)
}

// convTitle picks the conversation card's headline: agent, repo, or model.
func convTitle(c storage.ConversationSummary) string {
	if c.AgentName != "" {
		return c.AgentName
	}
	if c.GitRepo != "" {
		return c.GitRepo
	}
	if c.Model != "" {
		return c.Model
	}
	return "Session"
}

// convSub is the conversation card's muted second line, skipping anything
// already shown as the title.
func convSub(c storage.ConversationSummary) string {
	title := convTitle(c)
	parts := []string{}
	if c.GitRepo != "" && c.GitRepo != title {
		parts = append(parts, c.GitRepo)
	}
	if c.Model != "" && c.Model != title {
		parts = append(parts, c.Model)
	}
	if len(parts) == 0 {
		parts = append(parts, shortConv(c.Key))
	}
	return strings.Join(parts, " · ")
}
