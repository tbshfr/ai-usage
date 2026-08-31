package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const dsnOptions = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

// Open connects to the SQLite database at path and verifies it is reachable.
// WAL and a 5s busy timeout are enabled via DSN pragmas; a single connection
// pool entry keeps writes serialized (SQLite handles reads fine under WAL).
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+dsnOptions)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := pingWithRetry(ctx, db, 5*time.Second, 100*time.Millisecond); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func pingWithRetry(ctx context.Context, db *sql.DB, max, backoff time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		err := db.PingContext(ctx)
		if err == nil {
			return nil
		}
		if time.Now().Add(backoff).After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("ping database: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}
