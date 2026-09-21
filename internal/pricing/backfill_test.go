package pricing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestHistoricalBackfillRunsOnce(t *testing.T) {
	db := seedtest.EmptyDB(t)
	now := time.Now()
	body := priceBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	// More than one batch, all marked historical as the migration does.
	for i := range 205 {
		insert(t, db, usage(fmt.Sprintf("history-%03d", i), "test"))
	}
	insert(t, db, usage("historical-unknown", "future"))
	if _, err := db.Exec(`UPDATE generations SET pricing_pending=0`); err != nil {
		t.Fatal(err)
	}
	service := func() *Service {
		s := New(db, quiet(), nil)
		s.url = srv.URL
		s.now = func() time.Time { return now }
		return s
	}
	s := service()
	process(t, s)
	checkCost(t, read(t, db, "history-204"), 1.5, "openrouter")
	done, err := storage.PricingBackfillComplete(context.Background(), db)
	if err != nil || !done {
		t.Fatalf("completion marker %v %v", done, err)
	}
	now = now.Add(24 * time.Hour)
	body = `{"data":[{"id":"openai/future","pricing":{"prompt":"0.1","completion":"0.2"}}]}`
	insert(t, db, usage("new-future", "future"))
	s = service()
	process(t, s)
	checkCost(t, read(t, db, "new-future"), 14, "openrouter")
	if read(t, db, "historical-unknown").Cost != nil {
		t.Fatal("history reran after restart/refresh")
	}
	checkCost(t, read(t, db, "history-204"), 1.5, "openrouter")
}

func TestBackfillWaitsForCatalog(t *testing.T) {
	db := seedtest.EmptyDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	s := New(db, quiet(), nil)
	s.url = srv.URL
	insert(t, db, usage("history", "test"))
	if _, err := db.Exec(`UPDATE generations SET pricing_pending=0`); err != nil {
		t.Fatal(err)
	}
	process(t, s)
	complete, err := storage.PricingBackfillComplete(context.Background(), db)
	if err != nil || complete {
		t.Fatalf("backfill marked complete without fresh catalog: %v %v", complete, err)
	}
}

func TestBackfillGLMAndManualMAI(t *testing.T) {
	db := seedtest.EmptyDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"z-ai/glm-5.3-flash","pricing":{"prompt":"0.00000009","completion":"0.0000003"}}]}`)
	}))
	defer srv.Close()
	for _, model := range []string{"glm-5.3-flash", "z-ai/glm-5.3-flash", "mai-code-1.1-flash"} {
		insert(t, db, usage(model, model))
	}
	if _, err := db.Exec(`UPDATE generations SET pricing_pending=0`); err != nil {
		t.Fatal(err)
	}
	s := New(db, quiet(), nil)
	s.url = srv.URL
	process(t, s)
	for _, model := range []string{"glm-5.3-flash", "z-ai/glm-5.3-flash"} {
		checkCost(t, read(t, db, model), .000015, "openrouter")
	}
	checkCost(t, read(t, db, "mai-code-1.1-flash"), .000044, "manual")
}
