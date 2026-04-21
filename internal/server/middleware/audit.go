package middleware

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-observability-platform/internal/audit"
	"github.com/giantswarm/mcp-observability-platform/internal/identity"
)

// Audit emits one audit.Record per tool invocation: caller identity from
// ctx (OIDC subject), tool name from the request, incoming args, outcome,
// duration, and any error text.
//
// Installed as the innermost of the standard middleware chain so the
// recorded Duration reflects handler time only, matching the Metrics
// middleware's histogram label.
//
// nil logger returns a pass-through middleware so callers can keep audit
// optional without branching at registration time.
func Audit(logger *audit.Logger) server.ToolHandlerMiddleware {
	if logger == nil {
		return func(next server.ToolHandlerFunc) server.ToolHandlerFunc { return next }
	}
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			res, err := next(ctx, req)
			logger.Record(ctx, audit.Record{
				Timestamp: start,
				Caller:    identity.CallerSubject(ctx),
				Tool:      req.Params.Name,
				Args:      req.GetArguments(),
				Outcome:   Classify(res, err),
				Duration:  time.Since(start),
				Error:     auditErrorMessage(err, res),
			})
			return res, err
		}
	}
}

// auditErrorMessage picks the most useful string to record as the audit
// error field: the handler error first (system_error), otherwise the text
// of an IsError result (user_error). Empty when the call succeeded.
func auditErrorMessage(err error, res *mcp.CallToolResult) string {
	if err != nil {
		return err.Error()
	}
	if res == nil || !res.IsError {
		return ""
	}
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			return t.Text
		}
	}
	return "tool returned isError with no text content"
}
