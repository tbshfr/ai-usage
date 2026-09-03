package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateCreatesSchema(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}

	for _, obj := range []struct{ typ, name string }{
		{"table", "generations"},
		{"table", "schema_migrations"},
		{"index", "idx_generations_timestamp"},
		{"index", "idx_generations_source"},
		{"index", "idx_generations_provider"},
		{"index", "idx_generations_model"},
		{"index", "idx_generations_trace_id"},
	} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = ? AND name = ?`,
			obj.typ, obj.name,
		).Scan(&name)
		if err != nil {
			t.Errorf("%s %s missing: %v", obj.typ, obj.name, err)
		}
	}
}

func TestMigrateTwiceIsNoop(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, nil); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("schema_migrations rows = %d, want 3", count)
	}
}

func TestReopenExistingDBIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := Migrate(db2, nil); err != nil {
		t.Fatalf("migrate after reopen: %v", err)
	}

	var mode string
	if err := db2.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}
