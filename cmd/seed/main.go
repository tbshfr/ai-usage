// Command seed fills a new SQLite database with synthetic usage for
// screenshots, manual UI checks, and testing with large databases. The data
// is deterministic apart from being anchored to the current date.
//
//	go run ./cmd/seed -db temp/demo.db
//	go run ./cmd/seed -db temp/large.db -requests 1000000
//	go run ./cmd/ai-usage --database temp/demo.db
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/tbshfr/ai-usage/internal/ingest"
	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

type model struct {
	source, provider, name string
	weight                 float64
	// USD per million uncached input, output, and cache-read tokens
	in, out, cache float64
	reportsCost    bool
	inputHasCache  bool // Copilot and Codex count cached tokens as input
}

var models = []model{
	{"claude-code", "anthropic", "claude-sonnet-5-5", 26, 2, 10, 0.2, true, false},
	{"claude-code", "anthropic", "claude-opus-5-5", 8, 4, 20, 0.2, true, false},
	{"claude-code", "anthropic", "claude-fable-5-1", 3, 10, 50, 0.25, true, false},
	{"claude-code", "anthropic", "claude-haiku-4-5", 6, 1, 5, 0.1, true, false},
	{"codex", "openai", "gpt-6.1-sol", 16, 2, 10, 0.1, false, true},
	{"codex", "openai", "gpt-6-astra", 5, 10, 50, 1, false, true},
	{"copilot", "github", "gpt-6-sol", 6, 2, 10, 0.2, false, true},
	{"copilot", "github", "gemini-3.8-flash", 5, 0.75, 3.75, 0.075, false, true},
	{"opencode", "openrouter", "moonshotai/kimi-k3", 8, 3, 15, 0.3, true, false},
	{"opencode", "openrouter", "z-ai/glm-5.3", 4, 1.4, 4.4, 0.26, true, false},
	{"maki", "openrouter", "deepseek/deepseek-v4.1-flash", 5, 0.0198, 0.396, 0.00291, true, false},
}

var repos = []string{"acme/web-app", "acme/api", "acme/infra", "personal/dotfiles", "personal/blog"}
var agents = []string{"build", "plan", "general", "review"}

// insertBatch bounds memory and transaction size for large databases.
const insertBatch = 5000

func main() {
	path := flag.String("db", "temp/demo.db", "database path (must not exist)")
	days := flag.Int("days", 365, "days of history before today")
	requests := flag.Int("requests", 15000, "total requests to generate")
	flag.Parse()
	n, err := seed(context.Background(), *path, *days, *requests, time.Now().UTC(), os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("seeded %d generations into %s\n", n, *path)
}

// seed writes exactly requests generations spread over days of history
// ending at now, and reports progress to progress. It refuses an existing
// path so it never mixes synthetic rows into real usage.
func seed(ctx context.Context, path string, days, requests int, now time.Time, progress io.Writer) (int, error) {
	if days < 1 {
		return 0, fmt.Errorf("days must be at least 1")
	}
	if requests < 0 {
		return 0, fmt.Errorf("requests must not be negative")
	}
	if _, err := os.Stat(path); err == nil {
		return 0, fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	db, err := storage.Open(ctx, path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := storage.Migrate(db, nil); err != nil {
		return 0, err
	}
	r := rand.New(rand.NewPCG(42, 7))

	type tok struct {
		name, group string
		id          int64
	}
	toks := []*tok{{name: "work-laptop", group: "work"}, {name: "ci-runner", group: "work"}, {name: "home-desktop", group: "private"}, {name: "old-laptop", group: "private"}}
	for i, t := range toks {
		h := sha256.Sum256([]byte(fmt.Sprintf("demo-token-%d", i)))
		if t.id, err = storage.CreateToken(ctx, db, t.name, t.group, h, fmt.Sprintf("%x", h[:2])); err != nil {
			return 0, err
		}
	}
	if err := storage.RevokeToken(ctx, db, toks[3].id); err != nil {
		return 0, err
	}

	var total float64
	for _, m := range models {
		total += m.weight
	}
	pick := func() model {
		x := r.Float64() * total
		for _, m := range models {
			if x -= m.weight; x < 0 {
				return m
			}
		}
		return models[0]
	}

	// Each day gets a share of the requests: usage grows over the period,
	// dips on weekends, and some days are quiet. Requests start between 07:00
	// and 20:00 UTC, and today only up to now.
	today := now.Truncate(24 * time.Hour)
	type window struct{ start, end time.Time }
	windows := make([]window, days+1)
	weights := make([]float64, days+1)
	var weightSum float64
	for i := range weights {
		day := today.AddDate(0, 0, i-days)
		w := window{day.Add(7 * time.Hour), day.Add(20 * time.Hour)}
		if w.end.After(now) {
			w.end = now
		}
		if !w.start.Before(w.end) {
			w.start = day
		}
		windows[i] = w
		weight := (0.35 + 0.65*float64(i)/float64(days)) * (0.6 + 0.8*r.Float64())
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			weight *= 0.25
		}
		if r.Float64() < 0.06 {
			weight = 0
		}
		if w.start.Before(w.end) {
			// Scale today by how much of its window has passed.
			weight *= min(1, float64(w.end.Sub(w.start))/float64(13*time.Hour))
			weights[i] = weight
			weightSum += weight
		}
	}
	if weightSum == 0 {
		for i, w := range windows {
			if w.start.Before(w.end) {
				weights[i], weightSum = 1, weightSum+1
			}
		}
	}

	perDay := map[string]int64{}
	var batch []normalize.Generation
	written := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := storage.InsertGenerations(ctx, db, batch); err != nil {
			return err
		}
		written += len(batch)
		batch = batch[:0]
		fmt.Fprintf(progress, "\r%d/%d requests", written, requests)
		return nil
	}
	var cumWeight float64
	allocated := 0
	for i, weight := range weights {
		// Rounding the running total keeps the sum exactly at requests.
		cumWeight += weight
		quota := int(math.Round(float64(requests)*cumWeight/weightSum)) - allocated
		allocated += quota
		day := today.AddDate(0, 0, i-days)
		age := days - i
		w := windows[i]
		for s := 0; quota > 0; s++ {
			m := pick()
			start := w.start.Add(time.Duration(r.Int64N(int64(w.end.Sub(w.start)))))
			var t *tok
			switch x := r.Float64(); {
			case age > 200 && x < 0.25:
				t = toks[3] // retired token only carries older usage
			case x < 0.55:
				t = toks[0]
			case x < 0.7:
				t = toks[1]
			default:
				t = toks[2]
			}
			conv := fmt.Sprintf("%s-%s-%02d", m.source, day.Format("20060102"), s)
			repo := repos[r.IntN(3)]
			if t.group == "private" {
				repo = repos[3+r.IntN(2)]
			}
			agent := agents[r.IntN(len(agents))]
			n := min(3+r.IntN(25), quota)
			quota -= n
			ts := start
			for q := 0; q < n; q++ {
				ts = ts.Add(time.Duration(5+r.IntN(90)) * time.Second)
				if ts.After(now) {
					ts = now
				}
				uncached := int64(200 + r.IntN(6000))
				cacheRead := int64(float64(8000+r.IntN(60000)) * (0.3 + 0.7*float64(q)/float64(n)))
				cacheCreate := int64(r.IntN(3000))
				out := int64(80 + r.IntN(2500))
				// Claude Code does not report reasoning tokens.
				var reasoning *int64
				if m.source != "claude-code" && (m.source == "codex" || m.name == "gpt-6-astra" || r.Float64() < 0.2) {
					reasoning = ptr(int64(float64(out) * (0.2 + 0.5*r.Float64())))
				}
				input := uncached
				if m.inputHasCache {
					input += cacheRead
					cacheCreate = 0
				}
				g := normalize.Generation{
					ID:                  fmt.Sprintf("%s-%d", conv, q),
					Timestamp:           ts,
					Source:              m.source,
					ServiceName:         m.source,
					Provider:            m.provider,
					Model:               m.name,
					InputTokens:         ptr(input),
					OutputTokens:        ptr(out),
					CacheReadTokens:     ptr(cacheRead),
					CacheCreationTokens: ptr(cacheCreate),
					ReasoningTokens:     reasoning,
					ConversationID:      conv,
					TraceID:             fmt.Sprintf("trace-%s-%d", conv, q),
					SpanID:              fmt.Sprintf("span-%d", q),
					Duration:            time.Duration(800+r.IntN(40000)) * time.Millisecond,
					AgentName:           agent,
					GitRepo:             repo,
					GitBranch:           []string{"main", "feat/search", "fix/auth-timeout"}[r.IntN(3)],
					ReasoningEffort:     []string{"", "low", "medium", "high"}[r.IntN(4)],
					TokenID:             t.id,
				}
				if m.reportsCost {
					c := (float64(uncached)*m.in + float64(out)*m.out + float64(cacheRead)*m.cache + float64(cacheCreate)*m.in*1.25) / 1e6
					g.Cost = &c
					g.CostReportedByHarness = true
					g.CostSource = "harness"
				}
				batch = append(batch, g)
				perDay[ts.Format("2006-01-02")]++
				if len(batch) >= insertBatch {
					if err := flush(); err != nil {
						return written, err
					}
				}
			}
		}
	}
	if err := flush(); err != nil {
		return written, err
	}
	if written > 0 {
		fmt.Fprintln(progress)
	}

	// Ingestion counters for the Stats page.
	for d := min(60, days); d >= 0; d-- {
		day := today.AddDate(0, 0, -d).Format("2006-01-02")
		stored := perDay[day]
		dedup := int64(r.IntN(4))
		ignored := stored/3 + int64(r.IntN(20))
		var reasons []storage.ReasonStat
		var rejected, normErr int64
		if dedup > 0 {
			reasons = append(reasons, storage.ReasonStat{Kind: ingest.ReasonKindDedup, Reason: "claude-code", Count: dedup})
		}
		if ignored > 0 {
			reasons = append(reasons, storage.ReasonStat{Kind: ingest.ReasonKindIgnored, Reason: ingest.ReasonMetrics, Count: ignored})
		}
		if r.Float64() < 0.15 {
			rejected = int64(1 + r.IntN(5))
			reasons = append(reasons, storage.ReasonStat{Kind: ingest.ReasonKindHTTPReject, Reason: ingest.ReasonUnauthorized, Count: rejected})
		}
		if r.Float64() < 0.08 {
			normErr = 1
			reasons = append(reasons, storage.ReasonStat{Kind: ingest.ReasonKindNormError, Reason: ingest.ReasonBadAttrs, Count: 1})
		}
		s := storage.DailyStats{
			Day: day, Received: stored + dedup + ignored + rejected + normErr, Normalized: stored + dedup,
			Stored: stored, Deduplicated: dedup, Rejected: rejected, IgnoredNotUsed: ignored,
			NormalizationErrors: normErr, UpdatedAt: now,
		}
		if err := storage.UpsertDailySnapshot(ctx, db, s, reasons); err != nil {
			return written, err
		}
	}
	return written, nil
}

func ptr[T any](v T) *T { return &v }
