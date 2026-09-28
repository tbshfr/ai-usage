package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func newStore(t *testing.T) *TokenStore {
	t.Helper()
	s, err := NewTokenStore(context.Background(), seedtest.EmptyDB(t), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTokenStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	loopback, public := s.ForListener(false), s.ForListener(true)
	if loopback.Required() {
		t.Error("loopback listener must not require a token before any exists")
	}
	if !public.Required() {
		t.Error("public listener must always require a token")
	}

	plain, id, err := s.Create(ctx, "machine1", "private")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, tokenPrefix) || len(plain) < 40 {
		t.Errorf("token %q lacks prefix or entropy", plain)
	}
	if !loopback.Required() {
		t.Error("loopback listener must require a token once one exists")
	}
	if got, ok := loopback.Verify(plain); !ok || got != id {
		t.Errorf("verify = %d, %v; want %d", got, ok, id)
	}
	if _, ok := s.Verify(plain + "x"); ok {
		t.Error("wrong token accepted")
	}
	if _, ok := s.Verify(""); ok {
		t.Error("empty token accepted")
	}

	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	tokens, err := storage.ListTokens(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].LastUsed == nil || tokens[0].Hint != plain[len(plain)-4:] {
		t.Errorf("after flush = %+v", tokens)
	}

	if err := s.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(plain); ok {
		t.Error("revoked token accepted")
	}
	if !loopback.Required() {
		t.Error("revoking the last token must not reopen the loopback listener")
	}
	if s.ActiveCount() != 0 {
		t.Errorf("active = %d, want 0", s.ActiveCount())
	}
}

func TestTokenStoreSeed(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if ok, err := s.Seed(ctx, ""); ok || err != nil {
		t.Errorf("empty seed = %v, %v", ok, err)
	}
	if ok, err := s.Seed(ctx, "short"); !ok || err != nil {
		t.Fatalf("seed = %v, %v", ok, err)
	}
	if _, ok := s.Verify("short"); !ok {
		t.Error("seeded token must authenticate immediately")
	}
	tokens, err := storage.ListTokens(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].Hint != "" {
		t.Errorf("short token hint = %q, want none", tokens[0].Hint)
	}
}

func TestBearerAttachesTokenID(t *testing.T) {
	s := newStore(t)
	plain, id, err := s.Create(context.Background(), "laptop", "work")
	if err != nil {
		t.Fatal(err)
	}
	var got int64 = -1
	h := Bearer(testLogger(), s.ForListener(false), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = TokenIDFrom(r.Context())
	}))
	req := httptest.NewRequest("POST", "/v1/traces", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || got != id {
		t.Errorf("status %d token id %d, want 200 and %d", rec.Code, got, id)
	}
}

func TestBearerPassesThroughWhenNotRequired(t *testing.T) {
	s := newStore(t)
	var got int64 = -1
	h := Bearer(testLogger(), s.ForListener(false), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = TokenIDFrom(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/traces", nil))
	if rec.Code != http.StatusOK || got != 0 {
		t.Errorf("status %d token id %d, want 200 unattributed", rec.Code, got)
	}
	rec = httptest.NewRecorder()
	Bearer(testLogger(), s.ForListener(true), h).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/traces", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("public listener without tokens = %d, want 401", rec.Code)
	}
}

func TestGRPCAttachesTokenID(t *testing.T) {
	s := newStore(t)
	plain, id, err := s.Create(context.Background(), "laptop", "work")
	if err != nil {
		t.Fatal(err)
	}
	ic := GRPCUnaryInterceptor(testLogger(), s.ForListener(false))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+plain))
	var got int64
	_, err = ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/x"}, func(ctx context.Context, req any) (any, error) {
		got = TokenIDFrom(ctx)
		return nil, nil
	})
	if err != nil || got != id {
		t.Errorf("grpc = %v, token id %d; want %d", err, got, id)
	}
}
