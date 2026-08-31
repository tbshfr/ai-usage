# Phase 9 — Authentication for VPS deployment

Read `docs/plans/README.md` first. Prerequisite: Phases 1–8 merged.

## Goal

Make the single binary safe to expose on a VPS: dashboard behind a login
page (GUI, not a browser popup), OTLP endpoints behind a bearer token, and
the server refusing to expose an unauthenticated listener on a
non-loopback interface. Credentials enter only via flags or env vars —
never files, never the database.

## Verified client-side facts (do not re-research)

- **OpenCode** (`@devtheops/opencode-plugin-otel`): sends arbitrary OTLP
  headers via `OPENCODE_OTLP_HEADERS="key=value,..."` or the
  `otlpHeaders` plugin option (`{env:VAR}` substitution supported).
- **VS Code Copilot** (native OTel): auth headers are configurable
  **only** via `OTEL_EXPORTER_OTLP_HEADERS` (e.g. `Authorization=Bearer
  token`); there is no settings.json key for headers.
- Both, given `Authorization: Bearer <token>`, work unchanged. We dictate
  the scheme; it must be Bearer for OTLP.

## Work items

### 1. `internal/auth` package (new, stdlib only)

- `Bearer(logger, token)`: middleware comparing `Authorization: Bearer
  <token>` with `crypto/subtle` (constant-time); failures get `401` with
  `WWW-Authenticate: Bearer` and a warn log (no token content logged).
- `GRPCUnaryInterceptor(logger, token)`: same check against the
  `authorization` metadata key → `codes.Unauthenticated`.
- `Sessions`: HMAC-SHA256-signed cookie sessions. Cookie
  `ai_usage_session`, value `base64url(4-byte expiry) + "." +
  base64url(HMAC)`; 32-byte secret from `crypto/rand` at startup
  (restart logs everyone out); TTL 7 days; attributes `HttpOnly`,
  `SameSite=Lax`, `Path=/`. `Issue`, `Valid`, `Clear`, `Middleware`.
- `Dashboard`: holds user, password, sessions; `Check` (constant-time,
  SHA-256 both sides first so length never leaks), `Middleware` that
  allows `GET/POST /login`, `/logout`, `/static/`, `/health`, `/ready`
  and for everything else returns `401` JSON on `/api/*` or redirects to
  `/login` (plain `/`, never `?next=` — no open redirect).

### 2. Dashboard login GUI (`internal/web` + `web/templates/login.html`)

- `web.NewAuthed(db, dash)`: same routes as `web.New` plus
  `GET /login`, `POST /login`, `GET /logout`. `web.New(db)` unchanged.
- Login page is self-contained (own layout, no nav), shows an error on
  bad credentials (401 status, short sleep to blunt brute force);
  success issues the session cookie and redirects to `/`.
- `GET /logout` clears the cookie, redirects to `/login`; header shows a
  Sign out button only when auth is enabled (`ShowLogout` set in
  `render`/`renderFrag`).

### 3. Config (`internal/config`)

- New settings (flag > env > default, existing mechanism):
  `--dashboard-user` / `AI_USAGE_DASHBOARD_USER`,
  `--dashboard-password` / `AI_USAGE_DASHBOARD_PASSWORD`,
  `--otlp-token` / `AI_USAGE_OTLP_TOKEN`.
- Validation errors: user without password or vice versa.
- **Loopback refusal**: a listener address that is not loopback
  (wildcard `:port`, `0.0.0.0`, `::`, any non-loopback IP or hostname)
  errors at startup when its credential is unset — dashboard needs
  user+password, OTLP HTTP/gRPC need the token. Error text names the
  env var to set.
- **Default `--http` changes from `:8080` to `127.0.0.1:8080`** so the
  local quickstart still passes the new rule. VPS users set `--http
  :8080` plus credentials explicitly.

### 4. Wiring (`cmd/ai-usage/main.go`, `internal/api`)

- `api.NewWithAuth(db, logger, stats, version, dash)`: same as
  `api.New` but uses `web.NewAuthed` and wraps the whole handler with
  `dash.Middleware` (outermost, around access log).
- OTLP/HTTP handler wrapped with `auth.Bearer` when token set; gRPC
  server gets the interceptor (`NewGRPCServer` grows a token param).
- Startup log and banner report auth state.

### 5. Tests

- `internal/auth`: bearer accept/reject/malformed; session sign/verify,
  expired, tampered, cleared; Dashboard middleware public paths,
  redirect vs JSON 401; gRPC interceptor with/without metadata.
- `internal/web`: login form renders, bad creds → 401 + error, good
  creds → cookie + redirect, logout clears, static reachable, pages
  redirect when logged out, Sign out button appears when authed.
- `internal/api`: `/api/*` unauthenticated → 401 JSON; `/health`,
  `/ready` stay open.
- `internal/ingest`: OTLP/HTTP 401 without token, accepted with; gRPC
  `Unauthenticated` without metadata.
- `internal/config`: defaults (new default addr), flag/env resolution,
  user-without-password error, loopback refusal matrix (wildcard,
  0.0.0.0, hostname, IP; localhost/127.0.0.1 exempt), auth satisfied
  passes.

### 6. Docs

- `README.md`: auth section + config table rows + VPS deployment notes.
- `docs/source-setup.md`: client token config (`OPENCODE_OTLP_HEADERS`,
  `OTEL_EXPORTER_OTLP_HEADERS`).
- `docs/api.md`: 401 behavior.

## Global guardrails

Unchanged from `docs/plans/README.md` (privacy, no pricing, no new
dependencies, minimal comments, stdlib tests).

## Final gate

```bash
go build ./... && go vet ./... && go test ./... && gofmt -l .
```

## Acceptance criteria

- [ ] All listeners refuse non-loopback binds without credentials.
- [ ] Dashboard login via GUI works end-to-end; sessions survive page
      navigation and fragments; logout works.
- [ ] OTLP HTTP + gRPC accept `Authorization: Bearer <token>` only.
- [ ] `/health` and `/ready` remain unauthenticated.
- [ ] No credentials, tokens, or cookie values appear in logs.
- [ ] Local quickstart (`./ai-usage --otlp-http :4318` → open
      http://localhost:8080) still works with zero auth config.
