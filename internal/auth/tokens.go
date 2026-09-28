package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"log/slog"
	"sync"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
)

// Verifier decides whether an OTLP export is authenticated. Verify maps a
// presented bearer token to the api_tokens ID it belongs to; Required
// reports whether exports without a valid token must be rejected.
type Verifier interface {
	Verify(token string) (id int64, ok bool)
	Required() bool
}

type tokenIDKey struct{}

// WithTokenID returns ctx carrying the authenticating token's ID.
func WithTokenID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, tokenIDKey{}, id)
}

// TokenIDFrom returns the authenticating token's ID, or 0 when the export
// was not authenticated.
func TokenIDFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(tokenIDKey{}).(int64)
	return id
}

// StaticToken is a single fixed token (ID 1) that is always required. An
// empty token disables authentication. Used by tests and embedders that
// do not need the database-backed store.
func StaticToken(token string) Verifier { return staticToken(token) }

type staticToken string

func (s staticToken) Verify(token string) (int64, bool) {
	if s == "" || !equalConst(token, string(s)) {
		return 0, false
	}
	return 1, true
}

func (s staticToken) Required() bool { return s != "" }

// tokenPrefix marks generated tokens so they are recognizable in configs.
const tokenPrefix = "aiu_"

// TokenStore is the database-backed set of OTLP bearer tokens. Active
// token hashes are cached in memory and reloaded after every change; use
// the store's methods (not storage directly) to create or revoke tokens so
// the cache stays current. Safe for concurrent use.
type TokenStore struct {
	db     *sql.DB
	logger *slog.Logger

	mu     sync.RWMutex
	hashes map[[32]byte]int64
	total  int

	usedMu sync.Mutex
	used   map[int64]time.Time
}

// NewTokenStore loads the current tokens from db.
func NewTokenStore(ctx context.Context, db *sql.DB, logger *slog.Logger) (*TokenStore, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &TokenStore{db: db, logger: logger, used: map[int64]time.Time{}}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload refreshes the in-memory hash cache from the database.
func (s *TokenStore) Reload(ctx context.Context) error {
	hashes, total, err := storage.TokenHashes(ctx, s.db)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.hashes, s.total = hashes, total
	s.mu.Unlock()
	return nil
}

// Verify maps a token to its ID and records the use for the next Flush.
// The token is hashed before the lookup, so timing reveals nothing about
// stored tokens.
func (s *TokenStore) Verify(token string) (int64, bool) {
	if token == "" {
		return 0, false
	}
	h := sha256.Sum256([]byte(token))
	s.mu.RLock()
	id, ok := s.hashes[h]
	s.mu.RUnlock()
	if !ok {
		return 0, false
	}
	now := time.Now()
	s.usedMu.Lock()
	s.used[id] = now
	s.usedMu.Unlock()
	return id, true
}

// HasTokens reports whether any token (active or revoked) exists. Once one
// does, every listener requires authentication.
func (s *TokenStore) HasTokens() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.total > 0
}

// ActiveCount returns the number of tokens that currently authenticate.
func (s *TokenStore) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.hashes)
}

// ForListener returns the verifier for one OTLP listener. Public
// (non-loopback) listeners always require a token and fail closed while
// none is active; loopback listeners accept unauthenticated exports until
// the first token is created.
func (s *TokenStore) ForListener(public bool) Verifier {
	return listenerVerifier{store: s, public: public}
}

type listenerVerifier struct {
	store  *TokenStore
	public bool
}

func (l listenerVerifier) Verify(token string) (int64, bool) { return l.store.Verify(token) }
func (l listenerVerifier) Required() bool                    { return l.public || l.store.HasTokens() }

// Create generates a new token, stores its hash and returns the plaintext,
// which is not retrievable afterwards.
func (s *TokenStore) Create(ctx context.Context, name, group string) (string, int64, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, err
	}
	plain := tokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	id, err := storage.CreateToken(ctx, s.db, name, group, sha256.Sum256([]byte(plain)), tokenHint(plain))
	if err != nil {
		return "", 0, err
	}
	return plain, id, s.Reload(ctx)
}

// Revoke stops a token from authenticating.
func (s *TokenStore) Revoke(ctx context.Context, id int64) error {
	if err := storage.RevokeToken(ctx, s.db, id); err != nil {
		return err
	}
	return s.Reload(ctx)
}

// Seed imports a configured (env/flag) token; see storage.SeedToken.
func (s *TokenStore) Seed(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	inserted, err := storage.SeedToken(ctx, s.db, sha256.Sum256([]byte(token)), tokenHint(token))
	if err != nil {
		return false, err
	}
	return inserted, s.Reload(ctx)
}

// tokenHint is the displayable tail of a token. Short tokens show nothing
// so the hint never reveals a meaningful part of a weak secret.
func tokenHint(token string) string {
	if len(token) < 16 {
		return ""
	}
	return token[len(token)-4:]
}

// PendingUse returns the last-used times not yet flushed to the database,
// so views can show current values between flushes.
func (s *TokenStore) PendingUse() map[int64]time.Time {
	s.usedMu.Lock()
	defer s.usedMu.Unlock()
	out := make(map[int64]time.Time, len(s.used))
	for id, t := range s.used {
		out[id] = t
	}
	return out
}

// Flush persists the last-used times collected since the previous flush.
func (s *TokenStore) Flush(ctx context.Context) error {
	s.usedMu.Lock()
	used := s.used
	s.used = map[int64]time.Time{}
	s.usedMu.Unlock()
	if err := storage.TouchTokens(ctx, s.db, used); err != nil {
		// Put the times back so the next flush retries them.
		s.usedMu.Lock()
		for id, t := range used {
			if t.After(s.used[id]) {
				s.used[id] = t
			}
		}
		s.usedMu.Unlock()
		return err
	}
	return nil
}

// Run flushes last-used times every interval until ctx is done, then
// flushes once more.
func (s *TokenStore) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.Flush(flushCtx); err != nil {
				s.logger.Error("token last-used flush failed", "error", err.Error())
			}
			cancel()
			return
		case <-t.C:
			if err := s.Flush(ctx); err != nil {
				s.logger.Error("token last-used flush failed", "error", err.Error())
			}
		}
	}
}
