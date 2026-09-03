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
// "authorization: Bearer <token>" metadata. The token is never logged.
func GRPCUnaryInterceptor(logger *slog.Logger, token string) grpc.UnaryServerInterceptor {
	return GRPCUnaryInterceptorWithHook(logger, token, nil)
}

// GRPCUnaryInterceptorWithHook additionally calls onReject once per
// rejected export, so callers can count transport-level rejections
// without auth depending on any counter package.
func GRPCUnaryInterceptorWithHook(logger *slog.Logger, token string, onReject func()) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if bearerValidMD(md, token) {
			return handler(ctx, req)
		}
		if onReject != nil {
			onReject()
		}
		logger.Warn("grpc request rejected", "reason", "missing or invalid bearer token", "method", info.FullMethod)
		return nil, status.Error(codes.Unauthenticated, "invalid or missing bearer token")
	}
}

func bearerValidMD(md metadata.MD, token string) bool {
	for _, v := range md.Get("authorization") {
		if value, ok := strings.CutPrefix(v, "Bearer "); ok && equalConst(value, token) {
			return true
		}
	}
	return false
}
