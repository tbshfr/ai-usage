package api

import (
	"math"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONFailClosedOnInf(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, 200, map[string]any{"costTotal": math.Inf(1)})
	if w.Code != 500 {
		t.Errorf("Inf body must fail closed with 500, got %d body=%q", w.Code, w.Body.String())
	}
	w2 := httptest.NewRecorder()
	writeJSON(w2, 200, map[string]string{"status": "ok"})
	if w2.Code != 200 {
		t.Errorf("legit body must stay 200, got %d", w2.Code)
	}
}
