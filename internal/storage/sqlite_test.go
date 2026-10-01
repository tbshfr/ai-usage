package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func synchronousMode(t *testing.T, q interface {
	QueryRow(string, ...any) *sql.Row
}) int {
	t.Helper()
	var mode int
	if err := q.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

// durableTx must commit with synchronous=FULL and hand the connection back
// to the pool with the DSN's NORMAL, also when fn fails.
func TestDurableTxRestoresSynchronous(t *testing.T) {
	const normal, full = 1, 2
	db := openTestDB(t)
	// One connection, so the checks after durableTx see the one it used.
	db.SetMaxOpenConns(1)
	if got := synchronousMode(t, db); got != normal {
		t.Fatalf("pool synchronous = %d, want %d (NORMAL)", got, normal)
	}

	errFail := errors.New("fail")
	for _, fnErr := range []error{nil, errFail} {
		err := durableTx(context.Background(), db, func(tx *sql.Tx) error {
			if got := synchronousMode(t, tx); got != full {
				t.Errorf("synchronous inside durableTx = %d, want %d (FULL)", got, full)
			}
			if _, err := tx.Exec(`INSERT INTO api_tokens (name, group_name, token_hash, hint, created_at) VALUES ('t', '', randomblob(32), 'h', 0)`); err != nil {
				t.Fatal(err)
			}
			return fnErr
		})
		if !errors.Is(err, fnErr) {
			t.Fatalf("durableTx = %v, want %v", err, fnErr)
		}
		if got := synchronousMode(t, db); got != normal {
			t.Errorf("synchronous after durableTx (fn error %v) = %d, want %d (NORMAL)", fnErr, got, normal)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_tokens`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("api_tokens rows = %d, want 1 (the failed transaction must roll back)", rows)
	}
}
