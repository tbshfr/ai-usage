package ingest

import (
	"context"
	"log/slog"
	"net"

	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The gRPC listener is wired now so Phase 3 only fills in the service
// implementations; every RPC currently returns Unimplemented.
func NewGRPCServer(logger *slog.Logger) *grpc.Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(s, &traceService{logger: logger})
	colmetricpb.RegisterMetricsServiceServer(s, &metricsService{logger: logger})
	collogpb.RegisterLogsServiceServer(s, &logsService{logger: logger})
	return s
}

func ServeGRPC(s *grpc.Server, addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go s.Serve(ln)
	return ln, nil
}

func notImplemented(logger *slog.Logger, service string) error {
	logger.Warn("otlp gRPC service not implemented", "service", service)
	return status.Errorf(codes.Unimplemented, "%s is not implemented yet", service)
}

type traceService struct {
	coltracepb.UnimplementedTraceServiceServer
	logger *slog.Logger
}

func (t *traceService) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	return nil, notImplemented(t.logger, "traces")
}

type metricsService struct {
	colmetricpb.UnimplementedMetricsServiceServer
	logger *slog.Logger
}

func (m *metricsService) Export(ctx context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	return nil, notImplemented(m.logger, "metrics")
}

type logsService struct {
	collogpb.UnimplementedLogsServiceServer
	logger *slog.Logger
}

func (l *logsService) Export(ctx context.Context, req *collogpb.ExportLogsServiceRequest) (*collogpb.ExportLogsServiceResponse, error) {
	return nil, notImplemented(l.logger, "logs")
}
