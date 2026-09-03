package ingest

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type decodeCountConsumer struct {
	bumps []string
}

func (c *decodeCountConsumer) ConsumeTraces(_ context.Context, _ ptrace.Traces) error {
	return nil
}

func (c *decodeCountConsumer) ConsumeMetrics(_ context.Context, _ pmetric.Metrics) error {
	return nil
}

func (c *decodeCountConsumer) ConsumeLogs(_ context.Context, _ plog.Logs) error {
	return nil
}

func (c *decodeCountConsumer) BumpHTTPReject(reason string) {
	c.bumps = append(c.bumps, reason)
}

func badUTF8() string { return string([]byte{0xff, 0xfe}) }

func TestGRPCDecodeFailuresCounted(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	traceSvc := &traceService{consumer: &decodeCountConsumer{}, logger: logger}
	_, err := traceSvc.Export(ctx, &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{Name: badUTF8()}},
			}},
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("trace decode: code = %v, want InvalidArgument (err %v)", status.Code(err), err)
	}
	if got := traceSvc.consumer.(*decodeCountConsumer).bumps; len(got) != 1 || got[0] != ReasonDecodeFailed {
		t.Errorf("trace bumps = %v, want [decode_failed]", got)
	}

	metricSvc := &metricsService{consumer: &decodeCountConsumer{}, logger: logger}
	_, err = metricSvc.Export(ctx, &colmetricpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{Name: badUTF8()}},
			}},
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("metrics decode: code = %v, want InvalidArgument (err %v)", status.Code(err), err)
	}
	if got := metricSvc.consumer.(*decodeCountConsumer).bumps; len(got) != 1 || got[0] != ReasonDecodeFailed {
		t.Errorf("metrics bumps = %v, want [decode_failed]", got)
	}

	logSvc := &logsService{consumer: &decodeCountConsumer{}, logger: logger}
	_, err = logSvc.Export(ctx, &collogpb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: badUTF8()}},
				}},
			}},
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("logs decode: code = %v, want InvalidArgument (err %v)", status.Code(err), err)
	}
	if got := logSvc.consumer.(*decodeCountConsumer).bumps; len(got) != 1 || got[0] != ReasonDecodeFailed {
		t.Errorf("logs bumps = %v, want [decode_failed]", got)
	}
}

func TestGRPCDecodeFailureVisibleInPipeline(t *testing.T) {
	p := newStatsPipeline(t)
	svc := &traceService{consumer: p, logger: slog.Default()}
	_, err := svc.Export(context.Background(), &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{Name: badUTF8()}},
			}},
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
	if got := p.ReasonCounts()[ReasonKindHTTPReject][ReasonDecodeFailed]; got != 1 {
		t.Errorf("decode_failed = %d, want 1", got)
	}
}
