package backup

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

func BenchmarkBackup(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	db, err := storage.Open(ctx, filepath.Join(dir, "source.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db, nil); err != nil {
		b.Fatal(err)
	}
	_, err = db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000)
 INSERT INTO generations(id,timestamp,source,provider,model,input_tokens,output_tokens,conversation_id,git_repo,created_at)
 SELECT lower(hex(randomblob(32))), 1700000000000+x*1000,
 CASE x%3 WHEN 0 THEN 'codex' WHEN 1 THEN 'copilot' ELSE 'opencode' END,
 'provider', 'model-'||(x%10), x%10000, x%1000,
 lower(hex(randomblob(16))), 'org/repository-'||(x%50),1700000000000+x*1000 FROM n`)
	if err != nil {
		b.Fatal(err)
	}
	next := 0
	ingest := func() time.Duration {
		gens := make([]normalize.Generation, 10)
		for i := range gens {
			next++
			tokens := int64(next)
			gens[i] = normalize.Generation{ID: fmt.Sprintf("bench-%d", next), Timestamp: time.Now(), Source: "codex", Model: "model", InputTokens: &tokens}
		}
		start := time.Now()
		if _, err := storage.InsertGenerations(ctx, db, gens); err != nil {
			b.Error(err)
		}
		return time.Since(start)
	}
	baseline := make([]time.Duration, 100)
	for i := range baseline {
		baseline[i] = ingest()
	}
	slices.Sort(baseline)
	var snapshotTotal time.Duration
	var compressedBytes int64
	var concurrent []time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		done := make(chan struct{})
		var attemptErr error
		go func() {
			defer close(done)
			snapshot := filepath.Join(dir, fmt.Sprintf("snapshot-%d.db", i))
			start := time.Now()
			if attemptErr = storage.Snapshot(ctx, db, snapshot); attemptErr != nil {
				return
			}
			snapshotTotal += time.Since(start)
			_, compressedBytes, attemptErr = compress(ctx, snapshot, snapshot+".gz")
		}()
	loop:
		for {
			select {
			case <-done:
				break loop
			default:
			}
			concurrent = append(concurrent, ingest())
			time.Sleep(time.Millisecond)
		}
		if attemptErr != nil {
			b.Fatal(attemptErr)
		}
	}
	b.StopTimer()
	slices.Sort(concurrent)
	b.ReportMetric(float64(snapshotTotal.Microseconds())/float64(b.N), "snapshot-us")
	b.ReportMetric(float64(compressedBytes), "gzip-bytes")
	b.ReportMetric(float64(baseline[94].Microseconds()), "baseline-p95-us")
	if len(concurrent) > 0 {
		b.ReportMetric(float64(concurrent[(len(concurrent)-1)*95/100].Microseconds()), "backup-p95-us")
	}
}
