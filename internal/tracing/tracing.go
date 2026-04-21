// Package tracing sets up a best-effort OpenTelemetry tracer provider from
// the standard OTEL_* environment variables. If no OTLP endpoint is
// configured the returned Shutdown is a no-op and spans go to a no-op
// tracer. Both OTLP/HTTP (default) and OTLP/gRPC are supported via
// OTEL_EXPORTER_OTLP_PROTOCOL ("http/protobuf" | "grpc").
//
// Resource attributes are auto-detected from the K8s downward-API env vars
// (POD_NAME, POD_NAMESPACE, NODE_NAME) when present, plus the standard
// OTEL_RESOURCE_ATTRIBUTES env var.
package tracing

import (
	"context"
	"fmt"
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

// Init installs a global OTEL tracer provider. If OTEL_EXPORTER_OTLP_ENDPOINT
// (or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT) is unset, Init returns a no-op
// shutdown but still installs the W3C propagator so incoming traceparent
// headers are honoured.
func Init(ctx context.Context, serviceName, serviceVersion string) (Shutdown, error) {
	// Always install propagator — cheap and means inbound trace context is
	// preserved even before we actually export anywhere.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	endpoint := firstNonEmpty(
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

// buildExporter returns either an HTTP or gRPC OTLP trace exporter
// depending on OTEL_EXPORTER_OTLP_PROTOCOL ("http/protobuf" default).
func buildExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	proto := strings.ToLower(firstNonEmpty(
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
		return nil, fmt.Errorf("unknown OTEL_EXPORTER_OTLP_PROTOCOL=%q (want grpc|http/protobuf)", proto)
	}
}

// buildResource composes service identity + auto-detected K8s attrs +
// anything in OTEL_RESOURCE_ATTRIBUTES (resource.New picks up the env var).
func buildResource(ctx context.Context, serviceName, serviceVersion string) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
		attribute.String("service.namespace", "giantswarm.observability"),
	}
	// Downward-API populated K8s resource attrs — typical mount via
	//   env:
	//     - name: POD_NAME
	//       valueFrom: { fieldRef: { fieldPath: metadata.name } }
	if v := os.Getenv("POD_NAME"); v != "" {
		attrs = append(attrs, semconv.K8SPodName(v))
	}
	if v := os.Getenv("POD_NAMESPACE"); v != "" {
		attrs = append(attrs, semconv.K8SNamespaceName(v))
	}
	if v := os.Getenv("NODE_NAME"); v != "" {
		attrs = append(attrs, semconv.K8SNodeName(v))
	}
	return resource.New(ctx,
		resource.WithFromEnv(),   // honours OTEL_RESOURCE_ATTRIBUTES
		resource.WithProcess(),   // pid + executable
		resource.WithOS(),        // os.type, os.version
		resource.WithContainer(), // best-effort container.id
		resource.WithAttributes(attrs...),
	)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
