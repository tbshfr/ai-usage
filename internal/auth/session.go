// Package auth provides the authentication primitives for VPS
// deployments: a bearer-token gate for the OTLP listeners (HTTP + gRPC)
// and cookie-based sessions for the dashboard login.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"time"
)

// SessionCookie is the name of the dashboard session cookie.
const SessionCookie = "ai_usage_session"

// DefaultSessionTTL is how long a dashboard login stays valid without
// authenticated requests.
const DefaultSessionTTL = 7 * 24 * time.Hour

// maxRefreshInterval bounds how much earlier than ttl after its last
// request an active login can lapse.
const maxRefreshInterval = time.Hour

// sessionKeyLabel domain-separates the derived cookie signing key.
const sessionKeyLabel = "ai-usage dashboard session v1"

// Sessions issues and validates HMAC-signed cookie sessions. By default
// the secret is fresh random bytes per process, so every restart
// invalidates previously issued cookies. Safe for concurrent use.
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

// NewSessions creates a session manager with a fresh random secret.
func NewSessions() (*Sessions, error) {
	return NewConfiguredSessions("", DefaultSessionTTL, "", "")
}

// NewConfiguredSessions creates a session manager valid for ttl (zero
// uses DefaultSessionTTL). An empty
// secret keeps the per-process random key. A configured secret makes
// cookies survive restarts; the signing key is derived from it together
// with the dashboard credentials, so changing the username or password
// still invalidates every issued cookie.
func NewConfiguredSessions(secret string, ttl time.Duration, user, password string) (*Sessions, error) {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	if secret == "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		return &Sessions{secret: key, ttl: ttl}, nil
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sessionKeyLabel))
	for _, part := range []string{user, password} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		mac.Write(n[:])
		mac.Write([]byte(part))
	}
	return &Sessions{secret: mac.Sum(nil), ttl: ttl}, nil
}

// Issue sets a signed session cookie valid until now + ttl. The cookie is
// always marked Secure: production deployments serve the dashboard over
// HTTPS (docs/configuration.md), and browsers exempt http://localhost, so
// loopback development logins still work.
func (s *Sessions) Issue(w http.ResponseWriter) {
	s.issue(w, time.Now())
}

// Refresh reports whether r carries a valid session and slides its expiry
// to now + ttl, so a login in use never lapses. Re-issuing at most once per
// min(ttl/10, maxRefreshInterval) avoids a Set-Cookie on every request. A
// cookie outliving ttl predates a lowered TTL and is shortened right away.
func (s *Sessions) Refresh(w http.ResponseWriter, r *http.Request) bool {
	expiry, ok := s.expiry(r)
	if !ok {
		return false
	}
	now := time.Now()
	if left := expiry.Sub(now); left <= s.ttl-min(s.ttl/10, maxRefreshInterval) || left > s.ttl {
		s.issue(w, now)
	}
	return true
}

func (s *Sessions) issue(w http.ResponseWriter, now time.Time) {
	expiry := now.Add(s.ttl).Unix()
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(expiry))
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(buf[:])
	v := base64.RawURLEncoding.EncodeToString(buf[:]) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    v,
		Path:     "/",
		Expires:  time.Unix(expiry, 0).UTC(),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	// Refreshed cookies ride on ordinary authenticated responses: keep
	// shared caches from storing one and replaying it to other clients.
	w.Header().Set("Cache-Control", "private")
}

// Valid reports whether the request carries a currently valid session
// cookie. The MAC comparison is constant-time.
func (s *Sessions) Valid(r *http.Request) bool {
	_, ok := s.expiry(r)
	return ok
}

func (s *Sessions) expiry(r *http.Request) (time.Time, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return time.Time{}, false
	}
	dot := -1
	for i := 0; i < len(c.Value); i++ {
		if c.Value[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return time.Time{}, false
	}
	exp, err := base64.RawURLEncoding.DecodeString(c.Value[:dot])
	if err != nil || len(exp) != 4 {
		return time.Time{}, false
	}
	got, err := base64.RawURLEncoding.DecodeString(c.Value[dot+1:])
	if err != nil || len(got) != sha256.Size {
		return time.Time{}, false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(exp)
	if subtle.ConstantTimeCompare(got, mac.Sum(nil)) != 1 {
		return time.Time{}, false
	}
	expiry := time.Unix(int64(binary.BigEndian.Uint32(exp)), 0)
	if !time.Now().Before(expiry) {
		return time.Time{}, false
	}
	return expiry, true
}

// Clear expires the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
