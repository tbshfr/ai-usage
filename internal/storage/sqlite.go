package storage

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"time"

	_ "modernc.org/sqlite"
)

const dsnOptions = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"

// minPoolConns keeps several readers available even on single-core hosts;
// beyond that the pool scales with the CPU count (capped — extra
// connections only add contention, not throughput).
const (
	minPoolConns = 4
	maxPoolConns = 16
)

// Open connects to the SQLite database at path and verifies it is reachable.
// WAL allows concurrent readers beside a single writer; the pool is sized
// for parallel page loads so fast dashboard navigation cannot queue every
// request behind one connection. Concurrent writers (and writers colliding
// with readers) wait on SQLite's busy_timeout instead of failing.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+dsnOptions)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(min(max(minPoolConns, runtime.NumCPU()), maxPoolConns))
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
