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
	"staticURL":        staticURL,
	"commas":           commas,
	"tokens":           tokens,
	"cost":             cost,
	"generationCost":   generationCost,
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
	"dashboard":  mustParse("layout.html", "filterbar.html", "cards.html", "backup-status.html", "dashboard.html"),
	"trends":     mustParse("layout.html", "filterbar.html", "chart.html", "podium.html", "trends_page.html"),
	"breakdowns": mustParse("layout.html", "filterbar.html", "breakdowns.html", "breakdowns_page.html"),
	"sessions":   mustParse("layout.html", "filterbar.html", "session_list.html", "conversations.html", "rows.html", "sessions_page.html"),
	"detail":     mustParse("layout.html", "detail.html"),
	"stats":      mustParse("layout.html", "filterbar.html", "stats_page.html", "stats.html", "backup-status.html"),
	"setup":      mustParse("layout.html", "setup_page.html"),
}

// fragTmpls render bare page sections (no layout); the same named templates
// are included by the page sets, so a fragment is always also a
// full-HTML-renderable page section.
var fragTmpls = map[string]*template.Template{
	"backup-banner":   mustParse("backup-status.html"),
	"dashboard-stats": mustParse("cards.html"),
	"period-detail":   mustParse("cards.html"),
	"trends":          mustParse("chart.html", "podium.html"),
	"breakdowns":      mustParse("breakdowns.html"),
	"session-list":    mustParse("session_list.html", "conversations.html", "rows.html"),
	"stats":           mustParse("stats.html", "backup-status.html"),
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
// row in range has a known cost; all-unknown renders "—", never "$0.00".
func costLine(known, unknown int64, total *float64, estimated ...int64) string {
	if known == 0 {
		return emDash
	}
	label := ""
	if len(estimated) > 0 && estimated[0] > 0 {
		label = fmt.Sprintf(", %d estimated", estimated[0])
	}
	return "≈ " + cost(total) + fmt.Sprintf(" (%d known%s, %d without cost data)", known, label, unknown)
}

// costCell is the table-level cost cell: total when any row has a known cost.
// The estimate marker carries the explanation so repeated rows stay compact.
func costCell(known int64, total *float64, estimated ...int64) template.HTML {
	if known == 0 {
		return emDash
	}
	if len(estimated) > 0 && estimated[0] > 0 {
		return template.HTML(`<abbr class="cost-estimate" title="Includes estimated costs from OpenRouter or manual rates">≈</abbr> ` + cost(total))
	}
	return template.HTML(cost(total))
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
	case "maki":
		return "Maki"
	case "claude-code":
		return "Claude Code"
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
// "" becomes the "none" filter (session-less rows: title/progress helpers,
// autocomplete, and anything else without a conversation ID).
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

// convHref builds the /sessions drill-down URL for a conversation group
// key. Synthetic per-day keys map to their conversation sentinels
// (autocomplete/titleprogress/none); anything else is a real conversation
// ID.
func convHref(u uiFilter, key string) string {
	return conversationURL(u, storage.ConversationFilterForKey(key))
}

// convTitle is the conversation card's headline: the friendly source name.
// Agent/repo stay in the detail view; they are unreliable on cards
// (repo never observed, agent is "title" or "tool/..." on Copilot spans).
func convTitle(c storage.ConversationSummary) string {
	if c.Source != "" {
		return friendlySource(c.Source)
	}
	return "Session"
}

// convSub is the conversation card's muted second line: the latest model,
// falling back to the truncated conversation key when model is empty.
func convSub(c storage.ConversationSummary) string {
	if c.Model != "" {
		return c.Model
	}
	return shortConv(c.Key)
}

func generationCost(g normalize.Generation) string {
	if g.Cost == nil {
		return emDash
	}
	switch g.CostSource {
	case "manual":
		return "≈ " + cost(g.Cost) + " (manual estimate)"
	case "openrouter":
		return "≈ " + cost(g.Cost) + " (estimated)"
	case "free":
		return cost(g.Cost) + " (free)"
	default:
		return cost(g.Cost) + " (reported)"
	}
}
