package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// DefaultToolTimeout caps a hung upstream from stalling the MCP pod
// while leaving headroom for wide PromQL ranges (5–15s typical).
const DefaultToolTimeout = 30 * time.Second

// ToolTimeout converts a fired deadline into an IsError result so the
// LLM sees actionable text instead of a silent hang. timeout <= 0
// disables the wrap.
func ToolTimeout(timeout time.Duration) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if timeout <= 0 {
				return next(ctx, req)
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			res, err := next(ctx, req)

			// Only OUR deadline triggers the IsError replacement; a
			// parent context.Canceled propagates unchanged.
			if err != nil && ctx.Err() == context.DeadlineExceeded {
				return mcp.NewToolResultError(fmt.Sprintf("tool %q exceeded timeout of %s", req.Params.Name, timeout)), nil
			}
			return res, err
		}
	}
}
