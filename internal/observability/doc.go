// Package observability owns the Prometheus metrics + OTLP tracing
// init for this MCP.
//
// Metrics are registered on a package-local registry (not the global
// default) so tests can instantiate the server twice without hitting
// duplicate-registration panics, and so multiple MCP instances in one
// binary never cross-pollute metric streams.
//
// OTLP tracing is opt-in via OTEL_EXPORTER_OTLP_ENDPOINT — when unset,
// InitTracing returns a no-op shutdown and the tracer falls back to the
// global noop tracer (downstream calls are span-free).
package observability
