package middleware

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// RequireCaller fails tool calls that arrive without an authenticated
// caller in context. It is a guard, not an authentication step — actual
// auth happens in mcp-oauth's ValidateToken at the HTTP layer; this
// middleware asserts the result reached us so a future tool added
// without explicit authz code is still gated. Per-org/role checks stay
// in handlers (they need the org arg from tool params).
//
// Wired between Instrument and ResponseCap so denials still emit a span
// and a tool_call audit line. Returning an error result (not a Go error)
// keeps the LLM-visible failure mode consistent across the tool surface.
//
// Stdio bypasses OAuth — tool calls there trip this guard unless the
// session installs a caller via a stdio context func.
func RequireCaller() server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if authz.CallerFromContext(ctx).Empty() {
				return mcp.NewToolResultError("authentication required"), nil
			}
			return next(ctx, req)
		}
	}
}
