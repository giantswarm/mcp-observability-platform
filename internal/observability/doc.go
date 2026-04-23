// Package observability owns the Prometheus metrics, OTLP tracing, and OTLP
// logs bridge for this MCP.
//
// Metrics are registered on a package-local registry (not the global default)
// so tests can instantiate the server twice without hitting duplicate-
// registration panics, and so multiple MCP instances in one binary never
// cross-pollute metric streams.
package observability
