package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotWALConcurrent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := conn.ExecContext(ctx, "INSERT INTO generations(id,timestamp,source,input_tokens,created_at) VALUES(?,1,'codex',10,1)", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	var path string
	if err := conn.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("expected nonempty WAL: %v", err)
	}
	writerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		for i := 100; writerCtx.Err() == nil; i++ {
			_, err := db.ExecContext(writerCtx, "INSERT INTO generations(id,timestamp,source,input_tokens,created_at) VALUES(?,1,'codex',10,1)", fmt.Sprint(i))
			if err != nil && writerCtx.Err() == nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	output := filepath.Join(t.TempDir(), "snapshot's.db")
	err = Snapshot(ctx, db, output)
	cancel()
	if writeErr := <-done; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", output)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var count, total, migrations int
	if err := restored.QueryRow("SELECT count(*),sum(input_tokens) FROM generations").Scan(&count, &total); err != nil {
		t.Fatal(err)
	}
	if count < 100 || total != count*10 {
		t.Fatalf("inconsistent totals %d %d", count, total)
	}
	if err := restored.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&migrations); err != nil || migrations != 5 {
		t.Fatalf("schema %d %v", migrations, err)
	}
	if _, err := db.Exec("INSERT INTO generations(id,timestamp,source,created_at) VALUES('after',1,'codex',1)"); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotInvalidAndCancelled(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshot(context.Background(), corrupt); err == nil {
		t.Fatal("accepted corrupt database")
	}
	db, err := Open(context.Background(), filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Snapshot(ctx, db, filepath.Join(dir, "cancelled.db")); err == nil {
		t.Fatal("accepted cancellation")
	}
	if err := Snapshot(context.Background(), db, corrupt); err == nil {
		t.Fatal("overwrote existing file")
	}
}
