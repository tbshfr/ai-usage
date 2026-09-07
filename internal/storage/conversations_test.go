package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func mid(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
}

// Seed expectations: conv-copilot (12 rows) and conv-opencode (8 rows); see
// seedtest.Rows. conv-copilot: uncached input 1885 (c5 700→300, c12 40→30),
// output 1055, cacheRead 410, reasoning 35, no cost. conv-opencode: input
// 92, output 184, cacheCreation 56, cost 2.85 across 8 known rows. Latest
// rows: conv-copilot → c12 (2026-03-01, gpt-5.6-luna), conv-opencode → o7
// (2024-02-29, haiku).
func TestConversationsSeedGroups(t *testing.T) {
	db := seedtest.DB(t)
	convos, total, err := storage.Conversations(context.Background(), db, seedtest.FullRange(), storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(convos) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(convos), convos)
	}
	// desc = newest activity first: o6 (2026-03-02 00:15) beats c9 (00:08)
	c := convos[1]
	if c.Key != "conv-copilot" || c.Other() {
		t.Errorf("second group key = %q, want conv-copilot", c.Key)
	}
	if c.Requests != 12 || c.InputTokens != 1885 || c.OutputTokens != 1055 ||
		c.CacheReadTokens != 410 || c.CacheCreationTokens != 0 || c.ReasoningTokens != 35 {
		t.Errorf("conv-copilot sums = %+v", c)
	}
	if c.TotalTokens() != 3385 {
		t.Errorf("conv-copilot TotalTokens = %d, want 3385", c.TotalTokens())
	}
	if c.CostKnownCount != 0 || c.CostTotal != nil {
		t.Errorf("conv-copilot cost = %d/%v, want all unknown", c.CostKnownCount, c.CostTotal)
	}
	if c.FirstTimestamp.UTC() != time.Date(2024, 2, 29, 0, 9, 0, 0, time.UTC) {
		t.Errorf("conv-copilot first = %v", c.FirstTimestamp)
	}
	if c.LastTimestamp.UTC() != time.Date(2026, 3, 2, 0, 8, 0, 0, time.UTC) {
		t.Errorf("conv-copilot last = %v", c.LastTimestamp)
	}
	if c.Source != "copilot" || c.Model != "gpt-4.1" {
		t.Errorf("conv-copilot latest row = %s/%s, want copilot/gpt-4.1", c.Source, c.Model)
	}
	if rate := c.CacheHitRate(); rate == nil || *rate < 0.1786 || *rate > 0.1787 {
		t.Errorf("conv-copilot cache hit = %v, want ~0.1787 (410/2295)", rate)
	}

	o := convos[0]
	if o.Key != "conv-opencode" {
		t.Errorf("first group key = %q, want conv-opencode", o.Key)
	}
	if o.Requests != 8 || o.InputTokens != 92 || o.OutputTokens != 184 || o.CacheCreationTokens != 56 {
		t.Errorf("conv-opencode sums = %+v", o)
	}
	if o.CostKnownCount != 8 || o.CostUnknownCount != 0 || o.CostTotal == nil || *o.CostTotal != 2.85 {
		t.Errorf("conv-opencode cost = %d/%v, want 8 known summing 2.85", o.CostKnownCount, o.CostTotal)
	}
	if o.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("conv-opencode latest model = %q", o.Model)
	}
}

func TestConversationsOrderAndPaging(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()
	asc, _, err := storage.Conversations(ctx, db, seedtest.FullRange(), storage.OrderAsc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if asc[0].Key != "conv-copilot" || asc[1].Key != "conv-opencode" {
		t.Errorf("asc order = [%s %s], want copilot first", asc[0].Key, asc[1].Key)
	}
	// offset 1 keeps only the second group and reports the same total
	page, total, err := storage.Conversations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(page) != 1 || page[0].Key != "conv-copilot" {
		t.Errorf("paged = %v total %d, want [conv-copilot] / 2", page, total)
	}
	if _, _, err := storage.Conversations(ctx, db, seedtest.FullRange(), "sideways", 1, 0); err == nil {
		t.Error("invalid order must error")
	}
	if _, _, err := storage.Conversations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 0, 0); err == nil {
		t.Error("limit 0 must error")
	}
}

func TestConversationsOtherGroupsPerDay(t *testing.T) {
	db := seedtest.EmptyDB(t)
	ctx := context.Background()
	day := func(id, s string, minutes int) normalize.Generation {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return normalize.Generation{
			ID:             id,
			Timestamp:      d.Add(time.Duration(minutes) * time.Minute),
			Source:         "copilot",
			Model:          "gpt-4.1",
			InputTokens:    seedtest.IP(5),
			ConversationID: "",
			AgentName:      "titlegen",
		}
	}
	rows := []normalize.Generation{
		day("n1", "2026-03-01", 0), day("n2", "2026-03-01", 1), day("n3", "2026-03-02", 0),
	}
	for _, g := range rows {
		if _, err := storage.InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}
	convos, total, err := storage.Conversations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (same-day rows merge)", total)
	}
	first := convos[0]
	if !first.Other() || first.Key != "" || first.Day != "2026-03-02" {
		t.Errorf("newest group = %+v, want Other on 2026-03-02", first)
	}
	if first.Requests != 1 || first.AgentName != "titlegen" {
		t.Errorf("newest group = %+v, want 1 request with latest-row agent", first)
	}
	second := convos[1]
	if !second.Other() || second.Day != "2026-03-01" || second.Requests != 2 {
		t.Errorf("older group = %+v, want Other on 2026-03-01 with 2 requests", second)
	}

	// the "other" day groups are selectable via the none filter
	f := seedtest.FullRange()
	f.Conversation = storage.ConversationNone
	none, total, err := storage.Conversations(ctx, db, f, storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(none) != 2 {
		t.Errorf("none filter = %d groups (total %d), want 2", len(none), total)
	}
}

func TestTimeseriesHourBuckets(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()
	// Seed rows all sit within the first hour of their day (00:00-00:19),
	// so hour buckets must equal the day buckets.
	days, err := storage.Timeseries(ctx, db, seedtest.FullRange(), storage.BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	hours, err := storage.Timeseries(ctx, db, seedtest.FullRange(), storage.BucketHour)
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != len(days) {
		t.Fatalf("got %d hour buckets, want %d (equal to day buckets)", len(hours), len(days))
	}
	for i := range days {
		d, h := days[i], hours[i]
		d.CostTotal, h.CostTotal = nil, nil
		if h != d {
			t.Errorf("hour bucket %d = %+v, want %+v", i, h, d)
		}
	}
	// source split supports hour buckets too
	byHour, err := storage.TimeseriesBySource(ctx, db, seedtest.FullRange(), storage.BucketHour)
	if err != nil {
		t.Fatal(err)
	}
	if len(byHour) != 12 { // 6 days x 2 sources
		t.Errorf("got %d source-hour points, want 12: %+v", len(byHour), byHour)
	}
	if _, err := storage.Timeseries(ctx, db, seedtest.FullRange(), "year"); err == nil {
		t.Error("invalid bucket must error")
	}
}

func TestTimeseriesBySource(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()
	pts, err := storage.TimeseriesBySource(ctx, db, seedtest.FullRange(), storage.BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		bucket int64
		source string
		tokens int64
	}
	wants := []want{
		{mid(2024, 2, 29), "copilot", 1500}, // c10
		{mid(2024, 2, 29), "opencode", 59},  // o7
		{mid(2026, 1, 31), "copilot", 480},  // c1+c2
		{mid(2026, 1, 31), "opencode", 38},  // o1+o8
		{mid(2026, 2, 1), "copilot", 235},   // c3+c4
		{mid(2026, 2, 1), "opencode", 82},   // o2+o3
		{mid(2026, 2, 28), "copilot", 900},  // c5: input 700→300 uncached, cacheRead 400 (c11 has no tokens)
		{mid(2026, 2, 28), "opencode", 47},  // o4
		{mid(2026, 3, 1), "copilot", 200},   // c6+c12 (c12 input 40→30 uncached)
		{mid(2026, 3, 1), "opencode", 51},   // o5
		{mid(2026, 3, 2), "copilot", 70},    // c7+c8+c9
		{mid(2026, 3, 2), "opencode", 55},   // o6
	}
	if len(pts) != len(wants) {
		t.Fatalf("got %d points, want %d: %+v", len(pts), len(wants), pts)
	}
	for i, w := range wants {
		got := pts[i]
		if got.BucketStart != w.bucket || got.Source != w.source || got.TotalTokens() != w.tokens {
			t.Errorf("point %d = %d/%s/%d, want %d/%s/%d",
				i, got.BucketStart, got.Source, got.TotalTokens(), w.bucket, w.source, w.tokens)
		}
	}

	// week buckets merge Jan 31 + Feb 1 into the week of Jan 26 (Monday)
	weeks, err := storage.TimeseriesBySource(ctx, db, seedtest.FullRange(), storage.BucketWeek)
	if err != nil {
		t.Fatal(err)
	}
	byWeek := map[string]map[int64]int64{} // source -> weekStart -> tokens
	for _, p := range weeks {
		if byWeek[p.Source] == nil {
			byWeek[p.Source] = map[int64]int64{}
		}
		byWeek[p.Source][p.BucketStart] += p.TotalTokens()
	}
	if got := byWeek["copilot"][mid(2026, 1, 26)]; got != 480+235 {
		t.Errorf("copilot week of Jan 26 = %d, want 715", got)
	}
	if got := byWeek["opencode"][mid(2026, 1, 26)]; got != 38+82 {
		t.Errorf("opencode week of Jan 26 = %d, want 120", got)
	}
	if got := byWeek["copilot"][mid(2026, 2, 23)]; got != 900+200 {
		t.Errorf("copilot week of Feb 23 = %d, want 1100", got)
	}

	if _, err := storage.TimeseriesBySource(ctx, db, seedtest.FullRange(), "year"); err == nil {
		t.Error("invalid bucket must error")
	}
}

func TestConversationsXtabGroupsPerDay(t *testing.T) {
	db := seedtest.EmptyDB(t)
	ctx := context.Background()
	day := func(id, s string, minutes int, conv, agent string) normalize.Generation {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return normalize.Generation{
			ID:             id,
			Timestamp:      d.Add(time.Duration(minutes) * time.Minute),
			Source:         "copilot",
			Model:          "copilot-nes-lysithea-14",
			InputTokens:    seedtest.IP(5),
			ConversationID: conv,
			AgentName:      agent,
		}
	}
	rows := []normalize.Generation{
		// XtabProvider autocomplete with distinct conversation IDs on the
		// same day must merge into one per-day Autocomplete group, not one
		// session per conversation.
		day("x1", "2026-03-01", 0, "conv-xtab-1", normalize.AgentXtabProvider),
		day("x2", "2026-03-01", 1, "conv-xtab-2", normalize.AgentXtabProvider),
		// Same agent on another day is a separate group.
		day("x3", "2026-03-02", 0, "conv-xtab-3", normalize.AgentXtabProvider),
		// A regular conversation on the same day stays its own session.
		day("c1", "2026-03-01", 2, "conv-regular", "panel/editAgent"),
	}
	for _, g := range rows {
		if _, err := storage.InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}
	convos, total, err := storage.Conversations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3 (2 autocomplete days + 1 regular session)", total)
	}
	var autos, regular int
	for _, c := range convos {
		switch {
		case c.IsAutocomplete():
			autos++
			if c.Day == "2026-03-01" && c.Requests != 2 {
				t.Errorf("2026-03-01 Autocomplete requests = %d, want 2 (xtab merges)", c.Requests)
			}
			if c.Other() {
				t.Errorf("autocomplete group must not be generic Other: %+v", c)
			}
			if got := storage.ConversationFilterForKey(c.Key); got != storage.ConversationAutocomplete {
				t.Errorf("autocomplete key %q maps to filter %q, want autocomplete", c.Key, got)
			}
		case c.Key == "conv-regular":
			regular++
		default:
			t.Errorf("unexpected group key %q (xtab must not surface as session)", c.Key)
		}
	}
	if autos != 2 || regular != 1 {
		t.Errorf("autocomplete = %d regular = %d, want 2/1", autos, regular)
	}

	// The none filter selects Xtab rows even when they carry a conversation ID.
	f := seedtest.FullRange()
	f.Conversation = storage.ConversationNone
	none, total, err := storage.Conversations(ctx, db, f, storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(none) != 2 {
		t.Errorf("none filter = %d groups (total %d), want 2 per-day autocomplete groups", len(none), total)
	}

	// The autocomplete sentinel selects exactly the Xtab rows.
	f = seedtest.FullRange()
	f.Conversation = storage.ConversationAutocomplete
	auto, total, err := storage.Conversations(ctx, db, f, storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(auto) != 2 {
		t.Errorf("autocomplete filter = %d groups (total %d), want 2", len(auto), total)
	}
	for _, c := range auto {
		if !c.IsAutocomplete() {
			t.Errorf("autocomplete filter group = %+v, want IsAutocomplete", c)
		}
	}

	// A specific conversation filter excludes Xtab rows with that ID.
	f = seedtest.FullRange()
	f.Conversation = "conv-xtab-1"
	s, err := storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 0 {
		t.Errorf("conversation=conv-xtab-1 requests = %d, want 0 (xtab excluded)", s.Requests)
	}
}

func TestConversationsTitleProgressGroupsPerDay(t *testing.T) {
	db := seedtest.EmptyDB(t)
	ctx := context.Background()
	day := func(id, s string, minutes int, conv, agent string) normalize.Generation {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return normalize.Generation{
			ID:             id,
			Timestamp:      d.Add(time.Duration(minutes) * time.Minute),
			Source:         "copilot",
			Model:          "gpt-4o-mini-2024-07-18",
			InputTokens:    seedtest.IP(5),
			ConversationID: conv,
			AgentName:      agent,
		}
	}
	rows := []normalize.Generation{
		// title + progressMessages share one per-day card, even across
		// distinct conversation IDs.
		day("t1", "2026-03-01", 0, "conv-t-1", normalize.AgentTitle),
		day("p1", "2026-03-01", 1, "conv-p-1", normalize.AgentProgressMessages),
		day("t2", "2026-03-02", 0, "", normalize.AgentTitle),
		// Autocomplete and generic session-less rows stay separate cards.
		day("x1", "2026-03-01", 2, "", normalize.AgentXtabProvider),
		day("g1", "2026-03-01", 3, "", "titlegen"),
	}
	for _, g := range rows {
		if _, err := storage.InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}
	convos, total, err := storage.Conversations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 03-01: titleprogress(2) + autocomplete(1) + other(1); 03-02: titleprogress(1).
	if total != 4 {
		t.Fatalf("total = %d, want 4 (titleprogress x2 days + autocomplete + other)", total)
	}
	var tp, auto, other int
	for _, c := range convos {
		switch {
		case c.IsTitleProgress():
			tp++
			if c.Day == "2026-03-01" && c.Requests != 2 {
				t.Errorf("2026-03-01 Title/progress requests = %d, want 2 (title+progress merge)", c.Requests)
			}
			if got := storage.ConversationFilterForKey(c.Key); got != storage.ConversationTitleProgress {
				t.Errorf("titleprogress key %q maps to filter %q, want titleprogress", c.Key, got)
			}
		case c.IsAutocomplete():
			auto++
		case c.Other():
			other++
		default:
			t.Errorf("unexpected group %+v", c)
		}
	}
	if tp != 2 || auto != 1 || other != 1 {
		t.Errorf("titleprogress = %d autocomplete = %d other = %d, want 2/1/1", tp, auto, other)
	}

	// The titleprogress sentinel selects exactly title+progress rows.
	f := seedtest.FullRange()
	f.Conversation = storage.ConversationTitleProgress
	s, err := storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 3 {
		t.Errorf("titleprogress filter requests = %d, want 3", s.Requests)
	}

	// none still selects everything session-less.
	f = seedtest.FullRange()
	f.Conversation = storage.ConversationNone
	s, err = storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 5 {
		t.Errorf("none filter requests = %d, want 5", s.Requests)
	}

	// A specific conversation holding only helper rows is excluded.
	f = seedtest.FullRange()
	f.Conversation = "conv-t-1"
	s, err = storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 0 {
		t.Errorf("conversation=conv-t-1 requests = %d, want 0 (helpers excluded)", s.Requests)
	}
}

func TestConversationsSessionlessAgentsCopilotScoped(t *testing.T) {
	db := seedtest.EmptyDB(t)
	ctx := context.Background()
	day := func(id, source, s string, minutes int, conv, agent string) normalize.Generation {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return normalize.Generation{
			ID:             id,
			Timestamp:      d.Add(time.Duration(minutes) * time.Minute),
			Source:         source,
			Model:          "m",
			InputTokens:    seedtest.IP(5),
			ConversationID: conv,
			AgentName:      agent,
		}
	}
	rows := []normalize.Generation{
		// Free-form opencode agent names collide with the copilot
		// session-less values but must stay regular sessions.
		day("o-title", normalize.SourceOpenCode, "2026-03-01", 0, "conv-opencode-title", normalize.AgentTitle),
		day("o-xtab", normalize.SourceOpenCode, "2026-03-01", 1, "conv-opencode-xtab", normalize.AgentXtabProvider),
		day("o-prog", normalize.SourceOpenCode, "2026-03-01", 2, "conv-opencode-prog", normalize.AgentProgressMessages),
		// Copilot rows with the same agent names stay per-day groups.
		day("c-title", normalize.SourceCopilot, "2026-03-01", 3, "conv-copilot-title", normalize.AgentTitle),
	}
	for _, g := range rows {
		if _, err := storage.InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}
	convos, total, err := storage.Conversations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 3 opencode sessions + 1 copilot titleprogress day group.
	if total != 4 {
		t.Fatalf("total = %d, want 4 (3 opencode sessions + 1 copilot day group): %+v", total, convos)
	}
	byKey := map[string]storage.ConversationSummary{}
	for _, c := range convos {
		byKey[c.Key] = c
	}
	for _, key := range []string{"conv-opencode-title", "conv-opencode-xtab", "conv-opencode-prog"} {
		c, ok := byKey[key]
		if !ok {
			t.Errorf("missing opencode session %q: %+v", key, convos)
			continue
		}
		if c.IsAutocomplete() || c.IsTitleProgress() || c.Other() {
			t.Errorf("opencode %q must be a regular session, got %+v", key, c)
		}
	}
	if _, ok := byKey["conv-copilot-title"]; ok {
		t.Errorf("copilot title row must not surface as session conv-copilot-title: %+v", convos)
	}

	// Opencode conversations remain directly selectable.
	for _, key := range []string{"conv-opencode-title", "conv-opencode-xtab", "conv-opencode-prog"} {
		f := seedtest.FullRange()
		f.Conversation = key
		s, err := storage.Summary(ctx, db, f)
		if err != nil {
			t.Fatal(err)
		}
		if s.Requests != 1 {
			t.Errorf("conversation=%s requests = %d, want 1 (opencode agent is regular)", key, s.Requests)
		}
	}

	// The copilot-only sentinels select exactly the copilot row.
	f := seedtest.FullRange()
	f.Conversation = storage.ConversationTitleProgress
	s, err := storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 1 {
		t.Errorf("titleprogress filter requests = %d, want 1 (copilot only)", s.Requests)
	}
	f = seedtest.FullRange()
	f.Conversation = storage.ConversationAutocomplete
	s, err = storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 0 {
		t.Errorf("autocomplete filter requests = %d, want 0 (opencode xtab excluded)", s.Requests)
	}

	// none excludes opencode rows that carry a conversation ID.
	f = seedtest.FullRange()
	f.Conversation = storage.ConversationNone
	s, err = storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 1 {
		t.Errorf("none filter requests = %d, want 1 (copilot title only)", s.Requests)
	}
}
