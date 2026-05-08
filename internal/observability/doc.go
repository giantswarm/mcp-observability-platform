// Package observability owns the Prometheus metrics for this MCP.
//
// Metrics are registered on a package-local registry (not the global
// default) so tests can instantiate the server twice without hitting
// duplicate-registration panics, and so multiple MCP instances in one
// binary never cross-pollute metric streams.
//
// OTLP tracing init lives in github.com/giantswarm/mcp-toolkit/tracing
// and is wired in cmd/serve.go.
package observability
