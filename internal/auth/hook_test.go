package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBearerWithHookCountsRejections(t *testing.T) {
	rejects := 0
	h := BearerWithHook(testLogger(), "s3cret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), func() { rejects++ })

	// Rejections bump the hook.
	req := httptest.NewRequest("POST", "/v1/traces", nil)
	for i := 0; i < 3; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if rejects != 3 {
		t.Errorf("rejects = %d, want 3", rejects)
	}
	// Accepted requests do not.
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if rejects != 3 {
		t.Errorf("rejects after valid request = %d, want 3", rejects)
	}
}

func TestGRPCInterceptorWithHookCountsRejections(t *testing.T) {
	rejects := 0
	ic := GRPCUnaryInterceptorWithHook(testLogger(), "s3cret", func() { rejects++ })

	info := &grpc.UnaryServerInfo{FullMethod: "/test/Export"}
	_, err := ic(context.Background(), nil, info, func(ctx context.Context, req any) (any, error) {
		t.Error("handler must not run for unauthenticated exports")
		return nil, nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", status.Code(err))
	}
	if rejects != 1 {
		t.Errorf("rejects = %d, want 1", rejects)
	}
}
