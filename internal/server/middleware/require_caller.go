package middleware

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// RequireCaller fails tool calls that arrive without an authenticated caller
// in context. Closes the framework-level "future tool forgets RequireOrg"
// hole: per-org/role checks stay in handlers (they need the org argument
// from tool params), but the "no caller at all" case is handled here so a
// new tool added without explicit authz code is still gated.
//
// Wired between Instrument and ResponseCap so denials still emit metrics +
// audit records and Classify() routes them as user_error (expected, not a
// server bug). Returning an error result rather than a Go error keeps the
// LLM-visible failure mode consistent with the rest of the tool surface.
//
// Stdio transport bypasses OAuth; tool calls there will trip this guard
// unless the session installs a caller via a stdio context func. That's
// the same "stdio = local dev / trusted CLI" caveat already documented at
// cmd/serve.go.
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
