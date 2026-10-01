package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"runtime"
	"time"

	_ "modernc.org/sqlite"
)

const dsnOptions = "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"

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
	// Idle connections are kept up to the pool size: database/sql keeps
	// only two by default, and a reopened connection must re-parse the
	// schema (including the generated rollup triggers) with a cold cache.
	conns := min(max(minPoolConns, runtime.NumCPU()), maxPoolConns)
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	if err := pingWithRetry(ctx, db, 5*time.Second, 100*time.Millisecond); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// durableTx runs fn in a transaction that is on disk once it commits.
// Pooled connections use synchronous=NORMAL, so after an OS crash or power
// loss the latest commits can be gone until the next checkpoint; that is
// fine for telemetry, but a lost token revocation would silently re-enable
// a revoked key. FULL syncs the WAL on this commit only.
func durableTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return fmt.Errorf("durable transaction: %w", err)
	}
	defer func() {
		// never return a FULL connection to the pool
		if _, err := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA synchronous=NORMAL"); err != nil {
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
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
