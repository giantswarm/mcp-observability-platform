package observability

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// InitLogging returns an slog.Handler that emits every log record as an OTLP
// log, tagged with the trace_id + span_id of whatever span is active on the
// record's context. Operators can then click from a tool-call trace span
// straight to the surrounding log lines in Grafana/Loki, without needing a
// correlation-ID scheme.
//
// When neither OTEL_EXPORTER_OTLP_LOGS_ENDPOINT nor the shared
// OTEL_EXPORTER_OTLP_ENDPOINT is set, the returned handler is nil and the
// Shutdown is a no-op — callers use their regular stderr handler unchanged.
// Protocol selection mirrors InitTracing
// (OTEL_EXPORTER_OTLP_LOGS_PROTOCOL / OTEL_EXPORTER_OTLP_PROTOCOL).
func InitLogging(ctx context.Context, serviceName, serviceVersion string) (Shutdown, slog.Handler, error) {
	endpoint := cmp.Or(
		os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"),
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	)
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil, nil
	}

	exp, err := buildLogExporter(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("otlp log exporter: %w", err)
	}
	res, err := buildResource(ctx, serviceName, serviceVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("otel resource: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(res),
	)
	handler := otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(lp))
	return lp.Shutdown, handler, nil
}

// buildLogExporter picks between HTTP and gRPC OTLP log exporters based on
// OTEL_EXPORTER_OTLP_LOGS_PROTOCOL / OTEL_EXPORTER_OTLP_PROTOCOL. Defaults
// to HTTP/protobuf to match the SDK spec and the tracing exporter.
func buildLogExporter(ctx context.Context) (sdklog.Exporter, error) {
	proto := strings.ToLower(cmp.Or(
		os.Getenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"),
		os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"),
		"http/protobuf",
	))
	switch proto {
	case "grpc":
		return otlploggrpc.New(ctx)
	case "http/protobuf", "http":
		return otlploghttp.New(ctx)
	default:
		return nil, fmt.Errorf("unknown OTEL_EXPORTER_OTLP_PROTOCOL=%q (want grpc|http/protobuf)", proto)
	}
}

// FanoutHandler dispatches each log record to all given handlers. Used to
// emit the same slog stream to both a local sink (stderr text handler, or
// the audit stream's JSON handler) and the OTLP log bridge when
// InitLogging returned a non-nil handler.
//
// Nil handlers are filtered out. If only one non-nil handler remains,
// that handler is returned directly so the fanout wrapper doesn't add
// overhead in the single-sink case.
func FanoutHandler(handlers ...slog.Handler) slog.Handler {
	live := make([]slog.Handler, 0, len(handlers))
	for _, h := range handlers {
		if h != nil {
			live = append(live, h)
		}
	}
	switch len(live) {
	case 0:
		return nil
	case 1:
		return live[0]
	default:
		return &fanoutHandler{handlers: live}
	}
}

type fanoutHandler struct {
	handlers []slog.Handler
}

func (f *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	// slog.Record carries attribute state that handlers may mutate during
	// Handle (they typically iterate Attrs via an internal cursor). Clone
	// per recipient so two sinks never fight over the same cursor.
	var firstErr error
	for _, h := range f.handlers {
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: next}
}

func (f *fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &fanoutHandler{handlers: next}
}
