package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
	"modernc.org/sqlite"
)

// Intercept hash snapshots to reproduce read failures and overlapping
// refreshes against a real SQLite database.
type tokenTestDriver struct {
	inner    sqlite.Driver
	fail     atomic.Bool
	pause    atomic.Bool
	read     chan struct{}
	resume   chan struct{}
	blockIO  atomic.Bool
	blocked  chan struct{}
	resumeIO chan struct{}
}

func (d *tokenTestDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &tokenTestConn{Conn: c, d: d}, nil
}

type tokenTestConn struct {
	driver.Conn
	d *tokenTestDriver
}

func (c *tokenTestConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.d.waitIO(ctx); err != nil {
		return nil, err
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

func (c *tokenTestConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.d.waitIO(ctx); err != nil {
		return nil, err
	}
	snapshot := strings.Contains(q, "SELECT id, token_hash, revoked_at IS NULL")
	if snapshot && c.d.fail.Load() {
		return nil, errors.New("injected token snapshot read failure")
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	if snapshot && c.d.pause.CompareAndSwap(true, false) {
		return &tokenTestRows{Rows: rows, d: c.d}, nil
	}
	return rows, nil
}

func (d *tokenTestDriver) waitIO(ctx context.Context) error {
	if !d.blockIO.CompareAndSwap(true, false) {
		return nil
	}
	close(d.blocked)
	select {
	case <-d.resumeIO:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type tokenTestRows struct {
	driver.Rows
	d *tokenTestDriver
}

func (r *tokenTestRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err == io.EOF {
		close(r.d.read)
		<-r.d.resume
	}
	return err
}

var tokenTestDriverID atomic.Uint64

func consistencyStore(t *testing.T) (*TokenStore, *tokenTestDriver) {
	t.Helper()
	d := &tokenTestDriver{
		read: make(chan struct{}), resume: make(chan struct{}),
		blocked: make(chan struct{}), resumeIO: make(chan struct{}),
	}
	name := "token-consistency-" + strconv.FormatUint(tokenTestDriverID.Add(1), 10)
	sql.Register(name, d)
	db, err := sql.Open(name, "file:"+filepath.Join(t.TempDir(), "usage.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	s, err := NewTokenStore(context.Background(), db, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return s, d
}

func TestTokenChangesSurviveSnapshotReadFailure(t *testing.T) {
	ctx := context.Background()
	s, d := consistencyStore(t)
	d.fail.Store(true)
	plain, id, err := s.Create(ctx, "client", "")
	if err != nil {
		t.Fatalf("create discarded secret after successful insert: %v", err)
	}
	if got, ok := s.Verify(plain); !ok || got != id || !s.ForListener(false).Required() {
		t.Fatal("created token must authenticate and require authentication on loopback")
	}
	if err := s.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(plain); ok {
		t.Fatal("revoked token still authenticates")
	}
	fresh, err := s.Regenerate(ctx, id)
	if err != nil {
		t.Fatalf("regenerate discarded secret after successful commit: %v", err)
	}
	if got, ok := s.Verify(fresh); !ok || got != id {
		t.Fatal("regenerated token must authenticate")
	}
	if _, ok := s.Verify(plain); ok {
		t.Fatal("regeneration restored the old secret")
	}
	if inserted, err := s.Seed(ctx, "configured-token"); err != nil || !inserted {
		t.Fatalf("seed = %v, %v", inserted, err)
	}
	if _, ok := s.Verify("configured-token"); !ok {
		t.Fatal("seeded token must authenticate")
	}
	if err := s.Reload(ctx); err == nil {
		t.Fatal("expected injected snapshot read failure")
	}
	if got, ok := s.Verify(fresh); !ok || got != id {
		t.Fatal("failed explicit refresh must preserve committed cache updates")
	}
}

func TestTokenReloadCannotRestoreRevokedSecret(t *testing.T) {
	s, d := consistencyStore(t)
	plain, id, err := s.Create(context.Background(), "client", "")
	if err != nil {
		t.Fatal(err)
	}
	d.pause.Store(true)
	reloaded := make(chan error, 1)
	go func() { reloaded <- s.Reload(context.Background()) }()
	select {
	case <-d.read:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not reach its snapshot")
	}
	revoked := make(chan error, 1)
	go func() { revoked <- s.Revoke(context.Background(), id) }()
	// A refresh that captured an active secret must publish before the
	// revocation completes, rather than overwrite it afterward.
	select {
	case err := <-revoked:
		close(d.resume)
		<-reloaded
		t.Fatalf("revocation overtook the earlier snapshot: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(d.resume)
	if err := <-reloaded; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(plain); ok {
		t.Fatal("older refresh restored a revoked secret")
	}
}

func TestTokenAuthenticationContinuesDuringDatabaseIO(t *testing.T) {
	for _, operation := range []string{"create", "revoke", "regenerate", "seed", "flush", "reload"} {
		t.Run(operation, func(t *testing.T) {
			s, d := consistencyStore(t)
			plain, id, err := s.Create(context.Background(), "existing", "")
			if err != nil {
				t.Fatal(err)
			}
			// Record pending use so Flush performs a database write.
			if _, ok := s.Verify(plain); !ok {
				t.Fatal("existing token does not authenticate")
			}
			d.blockIO.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var once sync.Once
			release := func() { once.Do(func() { close(d.resumeIO) }) }
			defer release()
			done := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "create":
					_, _, err = s.Create(ctx, "new", "")
				case "revoke":
					err = s.Revoke(ctx, id)
				case "regenerate":
					_, err = s.Regenerate(ctx, id)
				case "seed":
					_, err = s.Seed(ctx, "configured-token")
				case "flush":
					err = s.Flush(ctx)
				case "reload":
					err = s.Reload(ctx)
				}
				done <- err
			}()
			select {
			case <-d.blocked:
			case <-ctx.Done():
				t.Fatal("operation did not reach database I/O")
			}
			verified := make(chan bool, 1)
			go func() {
				got, ok := s.Verify(plain)
				verified <- ok && got == id && s.HasTokens() && s.ActiveCount() == 1
			}()
			select {
			case ok := <-verified:
				if !ok {
					t.Error("authentication changed before the operation committed")
				}
			case <-time.After(time.Second):
				t.Error("database I/O blocked authentication")
			}
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			_, valid := s.Verify(plain)
			wantValid := operation != "revoke" && operation != "regenerate"
			if valid != wantValid {
				t.Errorf("old secret valid after %s = %v, want %v", operation, valid, wantValid)
			}
		})
	}
}
