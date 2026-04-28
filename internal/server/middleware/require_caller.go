package middleware

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// RequireCaller is the fail-closed gate that catches any future tool
// added without explicit authz code. Real authentication runs in
// mcp-oauth's ValidateToken at the HTTP edge; per-org/role checks stay
// in handlers (they need the org arg). Returns an IsError result, not
// a Go error, to match the rest of the tool surface. Stdio sessions
// trip this guard unless they install a caller via a context func.
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
