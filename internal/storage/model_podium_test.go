package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestTopModelsRanksByTokensDaysAndKnownCost(t *testing.T) {
	db := seedtest.DB(t)
	f := seedtest.FullRange()
	for _, tc := range []struct {
		metric storage.ModelRankMetric
		want   []string
	}{
		{storage.RankTokens, []string{"gpt-5.6-luna", "gpt-4.1", "claude-haiku-4-5-20251001"}},
		{storage.RankDays, []string{"claude-haiku-4-5-20251001", "gpt-5.6-luna", "gpt-4.1"}},
		{storage.RankCost, []string{"claude-haiku-4-5-20251001"}},
	} {
		rows, err := storage.TopModels(context.Background(), db, f, tc.metric)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(tc.want) {
			t.Fatalf("%s: got %d rows, want %d: %+v", tc.metric, len(rows), len(tc.want), rows)
		}
		for i, row := range rows {
			if row.Model != tc.want[i] {
				t.Errorf("%s rank %d = %q, want %q", tc.metric, i+1, row.Model, tc.want[i])
			}
		}
		if tc.metric == storage.RankDays && rows[0].ActiveDays != 6 {
			t.Errorf("haiku active days = %d, want 6 (multiple requests on one UTC day count once)", rows[0].ActiveDays)
		}
		if tc.metric == storage.RankCost {
			assertCost(t, rows[0].CostTotal, rows[0].CostKnownCount, 2.85, 8, "podium cost")
		}
	}
}

func TestTopModelsRespectsFilterAndGroupsOpenRouterPrefix(t *testing.T) {
	db := seedtest.DB(t)
	f := seedtest.FullRange()
	f.From = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	f.To = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	rows, err := storage.TopModels(context.Background(), db, f, storage.RankTokens)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Model != "gpt-5.6-luna" {
		t.Fatalf("March top models = %+v", rows)
	}

	// The same raw model spelling from OpenRouter and another provider belongs
	// to one displayed rank, matching the model breakdown table.
	_, err = db.Exec(`INSERT INTO generations
		(id, timestamp, source, provider, model, input_tokens, created_at)
		VALUES ('or-prefix', ?, 'opencode', 'openrouter', 'openai/gpt-5.6-luna', 50, ?)`,
		time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC).UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	rows, err = storage.TopModels(context.Background(), db, f, storage.RankDays)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Model != "gpt-5.6-luna" || rows[0].ActiveDays != 2 || rows[0].TotalTokens != 170 {
		t.Errorf("grouped OpenRouter rank = %+v", rows[0])
	}

	f.Source = "copilot"
	rows, err = storage.TopModels(context.Background(), db, f, storage.RankCost)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("cost ranking with unknown-only models = %+v, want empty", rows)
	}
}

func TestTopModelsRejectsInvalidMetric(t *testing.T) {
	if _, err := storage.TopModels(context.Background(), seedtest.EmptyDB(t), storage.Filter{}, "requests"); err == nil {
		t.Fatal("invalid metric accepted")
	}
}

func TestTopModelsExcludesCopilotHelpersOnlyFromDaysRanking(t *testing.T) {
	db := seedtest.EmptyDB(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		ts := start.AddDate(0, 0, i).UnixMilli()
		if _, err := db.Exec(`INSERT INTO generations
			(id, timestamp, source, model, agent_name, input_tokens, cost, cost_source, created_at)
			VALUES (?, ?, 'copilot', 'copilot-nes-lysithea-14', ?, 100, 1, 'harness', ?)`,
			fmt.Sprintf("nes-%d", i), ts, normalize.AgentXtabProvider, ts); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if _, err := db.Exec(`INSERT INTO generations
				(id, timestamp, source, model, input_tokens, cost, cost_source, created_at)
				VALUES (?, ?, 'copilot', 'gpt-4.1', 10, 0.1, 'harness', ?)`,
				fmt.Sprintf("chat-%d", i), ts, ts); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, h := range []struct {
		id, source, model, agent string
		day                      int
	}{
		{"title-0", "copilot", "gpt-4o-mini", normalize.AgentTitle, 0},
		{"title-1", "copilot", "gpt-4o-mini", normalize.AgentTitle, 1},
		{"title-2", "copilot", "gpt-4o-mini", normalize.AgentTitle, 2},
		{"progress", "copilot", "gpt-4o-mini", normalize.AgentProgressMessages, 3},
		{"shared-title", "copilot", "gpt-4.1", normalize.AgentTitle, 2},
		{"xtab-other-name", "copilot", "autocomplete-model", normalize.AgentXtabProvider, 3},
		{"nes-chat", "copilot", "copilot-nes-regular", "panel/editAgent", 4},
		{"other-source-title", "opencode", "opencode-title-model", normalize.AgentTitle, 0},
	} {
		ts := start.AddDate(0, 0, h.day).Add(12 * time.Hour).UnixMilli()
		if _, err := db.Exec(`INSERT INTO generations
			(id, timestamp, source, model, agent_name, input_tokens, created_at)
			VALUES (?, ?, ?, ?, ?, 5, ?)`, h.id, ts, h.source, h.model, h.agent, ts); err != nil {
			t.Fatal(err)
		}
	}
	f := storage.Filter{From: start, To: start.AddDate(0, 0, 5)}
	for _, tc := range []struct {
		metric storage.ModelRankMetric
		first  string
	}{
		{storage.RankDays, "gpt-4.1"},
		{storage.RankTokens, "copilot-nes-lysithea-14"},
		{storage.RankCost, "copilot-nes-lysithea-14"},
	} {
		rows, err := storage.TopModels(context.Background(), db, f, tc.metric)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 || rows[0].Model != tc.first {
			t.Errorf("%s first model = %+v, want %s", tc.metric, rows, tc.first)
		}
		if tc.metric == storage.RankDays && (len(rows) != 3 || rows[0].ActiveDays != 2 || rows[1].Model != "copilot-nes-regular" || rows[2].Model != "opencode-title-model") {
			t.Errorf("days ranking = %+v, want models with regular requests only", rows)
		}
	}
	f.Model = "copilot-nes-lysithea-14"
	rows, err := storage.TopModels(context.Background(), db, f, storage.RankDays)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("filtered NES days ranking = %+v, want empty", rows)
	}
}
