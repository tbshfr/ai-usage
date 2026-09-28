package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrTokenNotFound is returned when an api_tokens row does not exist.
var ErrTokenNotFound = errors.New("token not found")

// Token is the metadata of one OTLP bearer token. The plaintext is never
// stored; Hash is sha256 of it.
type Token struct {
	ID        int64
	Name      string
	Group     string
	Hint      string
	CreatedAt time.Time
	LastUsed  *time.Time
	RevokedAt *time.Time
}

// Active reports whether the token still authenticates exports.
func (t Token) Active() bool { return t.RevokedAt == nil }

// Label is the "group / name" display form; the group is omitted when empty.
func (t Token) Label() string {
	if t.Group == "" {
		return t.Name
	}
	return t.Group + " / " + t.Name
}

// maxTokenFieldLen bounds token names and group labels.
const maxTokenFieldLen = 64

// CleanTokenField trims and validates a token name or group label.
func CleanTokenField(field, v string, required bool) (string, error) {
	v = strings.TrimSpace(v)
	if required && v == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if len(v) > maxTokenFieldLen {
		return "", fmt.Errorf("%s is longer than %d characters", field, maxTokenFieldLen)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%s contains control characters", field)
		}
	}
	return v, nil
}

// CreateToken stores a new token and returns its row ID.
func CreateToken(ctx context.Context, db *sql.DB, name, group string, hash [32]byte, hint string) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO api_tokens (name, group_name, token_hash, hint, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, group, hash[:], hint, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("create token: %w", err)
	}
	return res.LastInsertId()
}

// ListTokens returns all tokens, including revoked ones, ordered by group
// then name.
func ListTokens(ctx context.Context, db *sql.DB) ([]Token, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, group_name, hint, created_at, last_used_at, revoked_at
FROM api_tokens ORDER BY group_name, name, id`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var created int64
		var lastUsed, revoked sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.Group, &t.Hint, &created, &lastUsed, &revoked); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		t.CreatedAt = time.UnixMilli(created).UTC()
		t.LastUsed = nullTime(lastUsed)
		t.RevokedAt = nullTime(revoked)
		out = append(out, t)
	}
	return out, rows.Err()
}

func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// UpdateToken renames a token and/or moves it to another group.
func UpdateToken(ctx context.Context, db *sql.DB, id int64, name, group string) error {
	res, err := db.ExecContext(ctx, `UPDATE api_tokens SET name = ?, group_name = ? WHERE id = ?`, name, group, id)
	if err != nil {
		return fmt.Errorf("update token: %w", err)
	}
	return requireRow(res)
}

// RevokeToken stops a token from authenticating. The row stays so stored
// usage keeps its attribution. Revoking twice keeps the first timestamp.
func RevokeToken(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, time.Now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	return requireRow(res)
}

// RegenerateToken replaces a token's secret, keeping its ID, name, group
// and attributed usage. The old hash stops authenticating and is retired
// (see SeedToken). A revoked token becomes active again.
func RegenerateToken(ctx context.Context, db *sql.DB, id int64, hash [32]byte, hint string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO api_token_retired_hashes (token_hash, token_id, retired_at)
SELECT token_hash, id, ? FROM api_tokens WHERE id = ?`, now, id); err != nil {
		return fmt.Errorf("retire token hash: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE api_tokens SET token_hash = ?, hint = ?, revoked_at = NULL, last_used_at = NULL WHERE id = ?`, hash[:], hint, id)
	if err != nil {
		return fmt.Errorf("regenerate token: %w", err)
	}
	if err := requireRow(res); err != nil {
		return err
	}
	return tx.Commit()
}

func requireRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// TokenHashes returns every active token's hash mapped to its ID, plus the
// total number of token rows (active and revoked).
func TokenHashes(ctx context.Context, db *sql.DB) (map[[32]byte]int64, int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, token_hash, revoked_at IS NULL FROM api_tokens`)
	if err != nil {
		return nil, 0, fmt.Errorf("token hashes: %w", err)
	}
	defer rows.Close()
	out := map[[32]byte]int64{}
	total := 0
	for rows.Next() {
		var id int64
		var hash []byte
		var active bool
		if err := rows.Scan(&id, &hash, &active); err != nil {
			return nil, 0, fmt.Errorf("scan token hash: %w", err)
		}
		total++
		if active && len(hash) == 32 {
			out[[32]byte(hash)] = id
		}
	}
	return out, total, rows.Err()
}

// TouchTokens records last-used times. Older values never overwrite newer ones.
func TouchTokens(ctx context.Context, db *sql.DB, used map[int64]time.Time) error {
	if len(used) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, t := range used {
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = MAX(COALESCE(last_used_at, 0), ?) WHERE id = ?`, t.UnixMilli(), id); err != nil {
			return fmt.Errorf("touch token: %w", err)
		}
	}
	return tx.Commit()
}

// SeedToken inserts the configured (env/flag) token as "default/default"
// unless its hash is already known, including as a revoked or regenerated
// token, so a revocation or regeneration in the UI survives restarts. When
// it is the very first token, all previously unattributed generations are
// assigned to it: before tokens were tracked, every authenticated export
// used this token.
// Reports whether a row was inserted.
func SeedToken(ctx context.Context, db *sql.DB, hash [32]byte, hint string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_tokens WHERE token_hash = ?1)
	OR EXISTS(SELECT 1 FROM api_token_retired_hashes WHERE token_hash = ?1)`, hash[:]).Scan(&exists); err != nil {
		return false, fmt.Errorf("seed token lookup: %w", err)
	}
	if exists {
		return false, nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens`).Scan(&count); err != nil {
		return false, fmt.Errorf("seed token count: %w", err)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (name, group_name, token_hash, hint, created_at) VALUES ('default', 'default', ?, ?, ?)`,
		hash[:], hint, time.Now().UnixMilli())
	if err != nil {
		return false, fmt.Errorf("seed token: %w", err)
	}
	if count == 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE generations SET token_id = ? WHERE token_id IS NULL`, id); err != nil {
			return false, fmt.Errorf("seed token backfill: %w", err)
		}
	}
	return true, tx.Commit()
}

// TokenGroups returns the distinct non-empty group labels.
func TokenGroups(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT group_name FROM api_tokens WHERE group_name != ''`)
	if err != nil {
		return nil, fmt.Errorf("token groups: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	sort.Strings(out)
	return out, rows.Err()
}
