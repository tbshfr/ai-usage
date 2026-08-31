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

// DefaultSessionTTL is how long a dashboard login stays valid.
const DefaultSessionTTL = 7 * 24 * time.Hour

// Sessions issues and validates HMAC-signed cookie sessions. The secret
// is fresh random bytes per process, so every restart invalidates
// previously issued cookies. Safe for concurrent use.
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

// NewSessions creates a session manager with a fresh random secret.
func NewSessions() (*Sessions, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return &Sessions{secret: secret, ttl: DefaultSessionTTL}, nil
}

// Issue sets a signed session cookie valid until now + ttl.
func (s *Sessions) Issue(w http.ResponseWriter) {
	expiry := time.Now().Add(s.ttl).Unix()
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(expiry))
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(buf[:])
	v := base64.RawURLEncoding.EncodeToString(buf[:]) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    v,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Valid reports whether the request carries a currently valid session
// cookie. The MAC comparison is constant-time.
func (s *Sessions) Valid(r *http.Request) bool {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	dot := -1
	for i := 0; i < len(c.Value); i++ {
		if c.Value[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return false
	}
	exp, err := base64.RawURLEncoding.DecodeString(c.Value[:dot])
	if err != nil || len(exp) != 4 {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(c.Value[dot+1:])
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(exp)
	if subtle.ConstantTimeCompare(got, mac.Sum(nil)) != 1 {
		return false
	}
	return time.Now().Unix() < int64(binary.BigEndian.Uint32(exp))
}

// Clear expires the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
