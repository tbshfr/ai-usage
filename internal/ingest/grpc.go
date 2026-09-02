package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/tbshfr/ai-usage/internal/auth"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

// NewGRPCServer builds the OTLP/gRPC server feeding the same pipeline as
// the HTTP receiver. A non-empty token requires clients to send
// "authorization: Bearer <token>" metadata; onReject (may be nil) is
// called once per unauthenticated export so the caller can count
// transport-level rejections.
func NewGRPCServer(consumer Consumer, logger *slog.Logger, token string, onReject func()) *grpc.Server {
	if logger == nil {
		logger = slog.Default()
	}
	opts := []grpc.ServerOption{}
	if token != "" {
		opts = append(opts, grpc.ChainUnaryInterceptor(auth.GRPCUnaryInterceptorWithHook(logger, token, onReject)))
	}
	s := grpc.NewServer(opts...)
	coltracepb.RegisterTraceServiceServer(s, &traceService{consumer: consumer, logger: logger})
	colmetricpb.RegisterMetricsServiceServer(s, &metricsService{consumer: consumer, logger: logger})
	collogpb.RegisterLogsServiceServer(s, &logsService{consumer: consumer, logger: logger})
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

func decodeErr(err error) error {
	return status.Errorf(codes.InvalidArgument, "decode failed: %v", err)
}

func unavailableErr(err error) error {
	return status.Errorf(codes.Unavailable, "ingestion unavailable: %v", err)
}

type traceService struct {
	coltracepb.UnimplementedTraceServiceServer
	consumer Consumer
	logger   *slog.Logger
}

func (t *traceService) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	td, err := tracesFromProto(req)
	if err != nil {
		return nil, decodeErr(err)
	}
	if err := t.consumer.ConsumeTraces(ctx, td); err != nil {
		t.logger.Error("pipeline failed", "signal", "traces", "error", err.Error())
		return nil, unavailableErr(err)
	}
	t.logger.Debug("otlp batch received", "signal", "traces", "encoding", "grpc", "records", td.SpanCount())
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

func tracesFromProto(req *coltracepb.ExportTraceServiceRequest) (ptrace.Traces, error) {
	b, err := proto.Marshal(req)
	if err != nil {
		return ptrace.Traces{}, fmt.Errorf("re-marshal request: %w", err)
	}
	er := ptraceotlp.NewExportRequest()
	if err := er.UnmarshalProto(b); err != nil {
		return ptrace.Traces{}, err
	}
	return er.Traces(), nil
}

type metricsService struct {
	colmetricpb.UnimplementedMetricsServiceServer
	consumer Consumer
	logger   *slog.Logger
}

func (m *metricsService) Export(ctx context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	md, err := metricsFromProto(req)
	if err != nil {
		return nil, decodeErr(err)
	}
	if err := m.consumer.ConsumeMetrics(ctx, md); err != nil {
		m.logger.Error("pipeline failed", "signal", "metrics", "error", err.Error())
		return nil, unavailableErr(err)
	}
	m.logger.Debug("otlp batch received", "signal", "metrics", "encoding", "grpc", "records", metricCount(md))
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

func metricsFromProto(req *colmetricpb.ExportMetricsServiceRequest) (pmetric.Metrics, error) {
	b, err := proto.Marshal(req)
	if err != nil {
		return pmetric.Metrics{}, fmt.Errorf("re-marshal request: %w", err)
	}
	mr := pmetricotlp.NewExportRequest()
	if err := mr.UnmarshalProto(b); err != nil {
		return pmetric.Metrics{}, err
	}
	return mr.Metrics(), nil
}

func metricCount(md pmetric.Metrics) int {
	var n int
	for _, rm := range md.ResourceMetrics().All() {
		for _, sm := range rm.ScopeMetrics().All() {
			n += sm.Metrics().Len()
		}
	}
	return n
}

type logsService struct {
	collogpb.UnimplementedLogsServiceServer
	consumer Consumer
	logger   *slog.Logger
}

func (l *logsService) Export(ctx context.Context, req *collogpb.ExportLogsServiceRequest) (*collogpb.ExportLogsServiceResponse, error) {
	ld, err := logsFromProto(req)
	if err != nil {
		return nil, decodeErr(err)
	}
	if err := l.consumer.ConsumeLogs(ctx, ld); err != nil {
		l.logger.Error("pipeline failed", "signal", "logs", "error", err.Error())
		return nil, unavailableErr(err)
	}
	l.logger.Debug("otlp batch received", "signal", "logs", "encoding", "grpc", "records", ld.LogRecordCount())
	return &collogpb.ExportLogsServiceResponse{}, nil
}

func logsFromProto(req *collogpb.ExportLogsServiceRequest) (plog.Logs, error) {
	b, err := proto.Marshal(req)
	if err != nil {
		return plog.Logs{}, fmt.Errorf("re-marshal request: %w", err)
	}
	lr := plogotlp.NewExportRequest()
	if err := lr.UnmarshalProto(b); err != nil {
		return plog.Logs{}, err
	}
	return lr.Logs(), nil
}
