// Package middleware holds the cross-cutting concerns applied to every MCP
// tool handler: one OTEL span, a counter+histogram pair (plus a separate
// error counter), and one structured tool_call slog record per invocation.
// Plus a response-size cap, a fail-closed authentication guard, and a
// per-handler context deadline.
//
// Wired through mcp-go's server.ToolHandlerMiddleware so they run on every
// tool call without per-handler boilerplate.
package middleware
