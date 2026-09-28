package auth

import (
	"context"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// GRPCUnaryInterceptor rejects OTLP/gRPC exports that do not carry
// "authorization: Bearer <token>" metadata accepted by v. The token is
// never logged. Accepted exports carry the token's ID in their context.
func GRPCUnaryInterceptor(logger *slog.Logger, v Verifier) grpc.UnaryServerInterceptor {
	return GRPCUnaryInterceptorWithHook(logger, v, nil)
}

// GRPCUnaryInterceptorWithHook additionally calls onReject once per
// rejected export, so callers can count transport-level rejections
// without auth depending on any counter package.
func GRPCUnaryInterceptorWithHook(logger *slog.Logger, v Verifier, onReject func()) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !v.Required() {
			return handler(ctx, req)
		}
		md, _ := metadata.FromIncomingContext(ctx)
		if id, ok := bearerValidMD(md, v); ok {
			return handler(WithTokenID(ctx, id), req)
		}
		if onReject != nil {
			onReject()
		}
		logger.Warn("grpc request rejected", "reason", "missing or invalid bearer token", "method", info.FullMethod)
		return nil, status.Error(codes.Unauthenticated, "invalid or missing bearer token")
	}
}

func bearerValidMD(md metadata.MD, v Verifier) (int64, bool) {
	for _, h := range md.Get("authorization") {
		if value, ok := strings.CutPrefix(h, "Bearer "); ok {
			if id, ok := v.Verify(value); ok {
				return id, true
			}
		}
	}
	return 0, false
}
