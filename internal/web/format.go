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
)

const emDash = "—"

var funcs = template.FuncMap{
	"commas":         commas,
	"tokens":         tokens,
	"cost":           cost,
	"costLine":       costLine,
	"costCell":       costCell,
	"dur":            dur,
	"utc":            utc,
	"friendlySource": friendlySource,
	"underlying":     normalize.UnderlyingProvider,
	"copyID":         copyID,
	"toJSON":         toJSON,
}

var pageTmpls = map[string]*template.Template{
	"login":      mustParse("login.html"),
	"overview":   mustParse("layout.html", "filterbar.html", "cards.html", "chart.html", "overview.html"),
	"breakdowns": mustParse("layout.html", "filterbar.html", "breakdowns.html", "breakdowns_page.html"),
	"recent":     mustParse("layout.html", "filterbar.html", "rows.html", "recent.html"),
	"detail":     mustParse("layout.html", "detail.html"),
}

// fragTmpls render bare page sections (no layout); the same named templates
// are included by the page sets, so a fragment is always also a
// full-HTML-renderable page section.
var fragTmpls = map[string]*template.Template{
	"overview-cards": mustParse("cards.html"),
	"timeseries":     mustParse("chart.html"),
	"breakdowns":     mustParse("breakdowns.html"),
	"recent-rows":    mustParse("rows.html"),
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

func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
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

func friendlySource(s string) string {
	switch s {
	case "opencode":
		return "OpenCode"
	case "copilot":
		return "VS Code Copilot"
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
