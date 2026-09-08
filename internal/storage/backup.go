package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
)

func Snapshot(ctx context.Context, db *sql.DB, path string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "VACUUM INTO ?", path)
	closeErr := conn.Close()
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	return ValidateSnapshot(ctx, path)
}

func ValidateSnapshot(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var header [16]byte
	_, readErr := io.ReadFull(f, header[:])
	closeErr := f.Close()
	if readErr != nil || string(header[:]) != "SQLite format 3\x00" {
		return fmt.Errorf("snapshot has an incomplete or invalid SQLite header")
	}
	if closeErr != nil {
		return closeErr
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("snapshot integrity check failed")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("snapshot integrity check returned no unique ok result")
	}
	return ctx.Err()
}
