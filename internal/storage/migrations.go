package storage

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Migrate(db *sql.DB, logger *slog.Logger) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files, err := migrationFiles()
	if err != nil {
		return err
	}

	for _, f := range files {
		version, err := versionOf(f)
		if err != nil {
			return err
		}
		applied, err := isApplied(db, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := apply(db, version, f); err != nil {
			return err
		}
		if logger != nil {
			logger.Info("migration applied", "version", version)
		}
	}
	return nil
}

func migrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		vi, _ := versionOf(names[i])
		vj, _ := versionOf(names[j])
		return vi < vj
	})
	return names, nil
}

func versionOf(name string) (int64, error) {
	prefix, _, _ := strings.Cut(name, "_")
	v, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("migration %q: bad numeric prefix: %w", name, err)
	}
	return v, nil
}

func isApplied(db *sql.DB, version int64) (bool, error) {
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("check migration %d: %w", version, err)
	}
	return count > 0, nil
}

func apply(db *sql.DB, version int64, name string) error {
	body, err := fs.ReadFile(migrationFS, "migrations/"+name)
	if err != nil {
		return fmt.Errorf("read migration %q: %w", name, err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("migration %d: begin: %w", version, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(string(body)); err != nil {
		return fmt.Errorf("migration %d (%s): %w", version, name, err)
	}
	now := time.Now().UnixMilli()
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		version, now,
	); err != nil {
		return fmt.Errorf("migration %d: record version: %w", version, err)
	}
	return tx.Commit()
}
