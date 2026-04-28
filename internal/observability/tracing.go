package observability

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Shutdown drains any pending spans. Safe to call multiple times.
type Shutdown func(ctx context.Context) error

// InitTracing installs a global OTEL tracer provider. If OTEL_EXPORTER_OTLP_ENDPOINT
// (or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT) is unset, InitTracing returns a no-op
// shutdown but still installs the W3C propagator so incoming traceparent
// headers are honoured.
func InitTracing(ctx context.Context, serviceName, serviceVersion string) (Shutdown, error) {
	// Always install propagator — cheap and means inbound trace context is
	// preserved even before we actually export anywhere.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	endpoint := cmp.Or(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	)
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	exp, err := buildExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	res, err := buildResource(ctx, serviceName, serviceVersion)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func buildExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	proto := strings.ToLower(cmp.Or(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"),
		os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"),
		"http/protobuf",
	))
	switch proto {
	case "grpc":
		return otlptracegrpc.New(ctx)
	case "http/protobuf", "http":
		return otlptrace.New(ctx, otlptracehttp.NewClient())
	default:
		return nil, fmt.Errorf("unknown OTLP protocol %q (set OTEL_EXPORTER_OTLP_PROTOCOL or OTEL_EXPORTER_OTLP_TRACES_PROTOCOL to grpc|http/protobuf)", proto)
	}
}

// buildResource treats resource.ErrPartialResource as warn-and-continue so a
// single failed detector (e.g. WithContainer on a cgroupv2 host) doesn't
// block startup.
func buildResource(ctx context.Context, serviceName, serviceVersion string) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
		semconv.ServiceNamespace("giantswarm.observability"),
	}
	if v := os.Getenv("POD_NAME"); v != "" {
		attrs = append(attrs, semconv.K8SPodName(v))
	}
	if v := os.Getenv("POD_NAMESPACE"); v != "" {
		attrs = append(attrs, semconv.K8SNamespaceName(v))
	}
	if v := os.Getenv("NODE_NAME"); v != "" {
		attrs = append(attrs, semconv.K8SNodeName(v))
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithContainer(),
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		if errors.Is(err, resource.ErrPartialResource) && res != nil {
			slog.Default().Warn("otel resource: partial detection — continuing with what was detected", "error", err)
			return res, nil
		}
		return nil, err
	}
	return res, nil
}
