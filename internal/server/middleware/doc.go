// Package middleware holds the cross-cutting concerns applied to every MCP
// tool handler: one OTEL span, one metric pair, one audit record per
// invocation, plus a response-size cap and an outcome classifier.
//
// Middlewares use mcp-go's built-in server.ToolHandlerMiddleware mechanism
// so they run automatically on every tool call without per-handler
// wrapping. Instrument is the single place Classify(res, err) is called;
// the result is fanned out to the span status, metric label, and audit
// outcome so those signals never drift apart.
package middleware
