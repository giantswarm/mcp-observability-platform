// Package middleware holds the tool-handler cross-cuts (instrumentation,
// auth gate, response cap, per-call deadline) wired through mcp-go's
// server.ToolHandlerMiddleware so they run on every call without
// per-handler boilerplate.
package middleware
