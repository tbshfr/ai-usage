package storage_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestTokenCRUD(t *testing.T) {
	ctx := context.Background()
	db := seedtest.EmptyDB(t)
	id, err := storage.CreateToken(ctx, db, "machine1", "private", sha256.Sum256([]byte("a")), "aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CreateToken(ctx, db, "laptop", "work", sha256.Sum256([]byte("b")), "bbbb"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CreateToken(ctx, db, "dup", "work", sha256.Sum256([]byte("b")), "bbbb"); err == nil {
		t.Error("duplicate hash must be rejected")
	}
	if err := storage.UpdateToken(ctx, db, id, "machine2", "home"); err != nil {
		t.Fatal(err)
	}
	if err := storage.RevokeToken(ctx, db, id); err != nil {
		t.Fatal(err)
	}
	if err := storage.RevokeToken(ctx, db, 999); !errors.Is(err, storage.ErrTokenNotFound) {
		t.Errorf("revoke missing = %v, want ErrTokenNotFound", err)
	}
	if err := storage.UpdateToken(ctx, db, 999, "x", ""); !errors.Is(err, storage.ErrTokenNotFound) {
		t.Errorf("update missing = %v, want ErrTokenNotFound", err)
	}
	used := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := storage.TouchTokens(ctx, db, map[int64]time.Time{id: used}); err != nil {
		t.Fatal(err)
	}
	// An older time never overwrites a newer one.
	if err := storage.TouchTokens(ctx, db, map[int64]time.Time{id: used.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	tokens, err := storage.ListTokens(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("tokens = %d, want 2", len(tokens))
	}
	home := tokens[0]
	if home.Label() != "home / machine2" || home.Active() || home.RevokedAt == nil {
		t.Errorf("updated+revoked token = %+v", home)
	}
	if home.LastUsed == nil || !home.LastUsed.Equal(used) {
		t.Errorf("last used = %v, want %v", home.LastUsed, used)
	}
	if tokens[1].Label() != "work / laptop" || !tokens[1].Active() || tokens[1].LastUsed != nil {
		t.Errorf("active token = %+v", tokens[1])
	}

	hashes, total, err := storage.TokenHashes(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(hashes) != 1 || hashes[sha256.Sum256([]byte("b"))] == 0 {
		t.Errorf("hashes = %v total %d, want only the active token of 2", hashes, total)
	}
	groups, err := storage.TokenGroups(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0] != "home" || groups[1] != "work" {
		t.Errorf("groups = %v", groups)
	}
}

func TestCleanTokenField(t *testing.T) {
	if v, err := storage.CleanTokenField("name", "  laptop ", true); err != nil || v != "laptop" {
		t.Errorf("trim = %q, %v", v, err)
	}
	if _, err := storage.CleanTokenField("name", " ", true); err == nil {
		t.Error("empty required field must error")
	}
	if v, err := storage.CleanTokenField("group", "", false); err != nil || v != "" {
		t.Errorf("empty optional = %q, %v", v, err)
	}
	if _, err := storage.CleanTokenField("name", "a\nb", true); err == nil {
		t.Error("control characters must error")
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := storage.CleanTokenField("name", string(long), true); err == nil {
		t.Error("overlong field must error")
	}
	for _, char := range []string{"界", "😀"} {
		name := strings.Repeat(char, 64)
		if got, err := storage.CleanTokenField("name", name, true); err != nil || got != name {
			t.Errorf("64 Unicode characters = %q, %v", got, err)
		}
		if _, err := storage.CleanTokenField("name", name+char, true); err == nil {
			t.Error("65 Unicode characters must error")
		}
	}
	if _, err := storage.CleanTokenField("name", string([]byte{0xff}), true); err == nil {
		t.Error("invalid UTF-8 must error")
	}
}

func TestSeedToken(t *testing.T) {
	ctx := context.Background()
	db := seedtest.DB(t)
	hash := sha256.Sum256([]byte("env-token"))
	inserted, err := storage.SeedToken(ctx, db, hash, "oken")
	if err != nil || !inserted {
		t.Fatalf("first seed = %v, %v; want inserted", inserted, err)
	}
	tokens, err := storage.ListTokens(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Label() != "default / default" {
		t.Fatalf("seeded tokens = %+v", tokens)
	}
	// The first token claims all previously unattributed rows.
	unattributed, err := storage.Summary(ctx, db, withToken(seedtest.FullRange(), storage.TokenNone))
	if err != nil {
		t.Fatal(err)
	}
	if unattributed.Requests != 0 {
		t.Errorf("unattributed rows after first seed = %d, want 0", unattributed.Requests)
	}
	all, err := storage.Summary(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := storage.Summary(ctx, db, withToken(seedtest.FullRange(), strconv.FormatInt(tokens[0].ID, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Requests != all.Requests || all.Requests == 0 {
		t.Errorf("seeded token rows = %d, want all %d", seeded.Requests, all.Requests)
	}

	// Seeding again, or after a revoke, is a no-op.
	if inserted, err := storage.SeedToken(ctx, db, hash, "oken"); err != nil || inserted {
		t.Errorf("reseed = %v, %v; want no-op", inserted, err)
	}
	if err := storage.RevokeToken(ctx, db, tokens[0].ID); err != nil {
		t.Fatal(err)
	}
	if inserted, err := storage.SeedToken(ctx, db, hash, "oken"); err != nil || inserted {
		t.Errorf("seed after revoke = %v, %v; want no-op", inserted, err)
	}

	// A different configured token is added but claims no rows.
	if _, err := storage.InsertGenerations(ctx, db, []normalize.Generation{{ID: "loopback", Timestamp: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Source: "opencode"}}); err != nil {
		t.Fatal(err)
	}
	if inserted, err := storage.SeedToken(ctx, db, sha256.Sum256([]byte("other")), ""); err != nil || !inserted {
		t.Fatalf("second seed = %v, %v", inserted, err)
	}
	unattributed, err = storage.Summary(ctx, db, withToken(seedtest.FullRange(), storage.TokenNone))
	if err != nil {
		t.Fatal(err)
	}
	if unattributed.Requests != 1 {
		t.Errorf("unattributed after second seed = %d, want 1", unattributed.Requests)
	}
}

func TestRegenerateToken(t *testing.T) {
	ctx := context.Background()
	db := seedtest.EmptyDB(t)
	oldHash := sha256.Sum256([]byte("old"))
	id, err := storage.SeedToken(ctx, db, oldHash, "")
	if err != nil || !id {
		t.Fatalf("seed = %v, %v", id, err)
	}
	tokens, err := storage.ListTokens(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	tokenID := tokens[0].ID
	if _, err := storage.InsertGenerations(ctx, db, []normalize.Generation{{ID: "a", Timestamp: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Source: "opencode", TokenID: tokenID}}); err != nil {
		t.Fatal(err)
	}
	if err := storage.RevokeToken(ctx, db, tokenID); err != nil {
		t.Fatal(err)
	}
	newHash := sha256.Sum256([]byte("new"))
	if err := storage.RegenerateToken(ctx, db, tokenID, newHash, "hint"); err != nil {
		t.Fatal(err)
	}
	if err := storage.RegenerateToken(ctx, db, 999, sha256.Sum256([]byte("x")), ""); !errors.Is(err, storage.ErrTokenNotFound) {
		t.Errorf("regenerate missing = %v, want ErrTokenNotFound", err)
	}

	tokens, err = storage.ListTokens(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].ID != tokenID || !tokens[0].Active() || tokens[0].Hint != "hint" || tokens[0].Label() != "default / default" {
		t.Errorf("regenerated token = %+v", tokens)
	}
	hashes, _, err := storage.TokenHashes(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if hashes[newHash] != tokenID || hashes[oldHash] != 0 {
		t.Errorf("hashes = %v, want only the new secret", hashes)
	}
	// Usage stays attributed to the same token.
	s, err := storage.Summary(ctx, db, withToken(seedtest.FullRange(), strconv.FormatInt(tokenID, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 1 {
		t.Errorf("requests after regenerate = %d, want 1", s.Requests)
	}
	// The retired configured token is not imported again.
	if inserted, err := storage.SeedToken(ctx, db, oldHash, ""); err != nil || inserted {
		t.Errorf("seed retired hash = %v, %v; want no-op", inserted, err)
	}
}

func withToken(f storage.Filter, token string) storage.Filter {
	f.Token = token
	return f
}

func TestTokenFiltersAndBreakdowns(t *testing.T) {
	ctx := context.Background()
	db := seedtest.EmptyDB(t)
	work, err := storage.CreateToken(ctx, db, "laptop", "work", sha256.Sum256([]byte("w")), "")
	if err != nil {
		t.Fatal(err)
	}
	m1, err := storage.CreateToken(ctx, db, "machine1", "private", sha256.Sum256([]byte("p1")), "")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := storage.CreateToken(ctx, db, "machine2", "private", sha256.Sum256([]byte("p2")), "")
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	gen := func(id string, token int64, in int64) normalize.Generation {
		return normalize.Generation{ID: id, Timestamp: ts, Source: "opencode", Model: "m", InputTokens: seedtest.IP(in), TokenID: token}
	}
	if _, err := storage.InsertGenerations(ctx, db, []normalize.Generation{
		gen("a", work, 100), gen("b", work, 100), gen("c", m1, 10), gen("d", m2, 1), gen("e", 0, 1000),
	}); err != nil {
		t.Fatal(err)
	}
	// A replay from another token must not steal attribution.
	if _, err := storage.InsertGenerations(ctx, db, []normalize.Generation{gen("a", m1, 100)}); err != nil {
		t.Fatal(err)
	}
	f := seedtest.FullRange()
	sum := func(f storage.Filter) int64 {
		t.Helper()
		s, err := storage.Summary(ctx, db, f)
		if err != nil {
			t.Fatal(err)
		}
		return s.InputTokens
	}
	byGroup := f
	byGroup.Group = "private"
	if got := sum(byGroup); got != 11 {
		t.Errorf("private group input = %d, want 11", got)
	}
	if got := sum(withToken(f, strconv.FormatInt(work, 10))); got != 200 {
		t.Errorf("work token input = %d, want 200", got)
	}
	if got := sum(withToken(f, storage.TokenNone)); got != 1000 {
		t.Errorf("unauthenticated input = %d, want 1000", got)
	}
	if _, err := storage.Summary(ctx, db, withToken(f, "abc")); err == nil {
		t.Error("non-numeric token filter must error")
	}

	groups, err := storage.ByGroup(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	gotGroups := map[string]int64{}
	for _, b := range groups {
		gotGroups[b.Key] = b.InputTokens
	}
	if len(groups) != 3 || gotGroups[""] != 1000 || gotGroups["work"] != 200 || gotGroups["private"] != 11 || groups[0].Key != "" {
		t.Errorf("by group = %v (order %v)", gotGroups, groups)
	}
	tokens, err := storage.ByToken(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	gotTokens := map[string]int64{}
	for _, b := range tokens {
		gotTokens[b.Key] = b.InputTokens
	}
	if len(tokens) != 4 || gotTokens[strconv.FormatInt(m1, 10)] != 10 || gotTokens[strconv.FormatInt(m2, 10)] != 1 || gotTokens[""] != 1000 {
		t.Errorf("by token = %v", gotTokens)
	}

	rows, err := storage.RecentGenerations(ctx, db, withToken(f, strconv.FormatInt(work, 10)), storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].TokenID != work {
		t.Errorf("recent generations for work token = %+v", rows)
	}
}

func TestUngroupedTokenUsage(t *testing.T) {
	ctx := context.Background()
	db := seedtest.EmptyDB(t)
	ungrouped, err := storage.CreateToken(ctx, db, "ungrouped", "", sha256.Sum256([]byte("ungrouped")), "")
	if err != nil {
		t.Fatal(err)
	}
	// "none" remains a usable group label; selecting ungrouped tokens
	// must not reserve a label or include unauthenticated generations.
	grouped, err := storage.CreateToken(ctx, db, "grouped", "none", sha256.Sum256([]byte("grouped")), "")
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := storage.InsertGenerations(ctx, db, []normalize.Generation{
		{ID: "ungrouped", Source: "opencode", Timestamp: ts, TokenID: ungrouped, InputTokens: seedtest.IP(10)},
		{ID: "grouped", Source: "opencode", Timestamp: ts, TokenID: grouped, InputTokens: seedtest.IP(20)},
		{ID: "unauthenticated", Source: "opencode", Timestamp: ts, InputTokens: seedtest.IP(30)},
	}); err != nil {
		t.Fatal(err)
	}
	groups, err := storage.ByGroup(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Key != "" || groups[0].Requests != 2 || groups[0].InputTokens != 40 {
		t.Fatalf("empty group must combine unauthenticated and ungrouped usage: %+v", groups)
	}
	f := seedtest.FullRange()
	f.Ungrouped = true
	summary, err := storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 1 || summary.InputTokens != 10 {
		t.Fatalf("ungrouped filter = %+v", summary)
	}
	f.Token = storage.TokenNone
	summary, err = storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 0 {
		t.Fatal("ungrouped filter must exclude unauthenticated usage")
	}
}
