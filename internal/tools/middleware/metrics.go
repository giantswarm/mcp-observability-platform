package middleware

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// Metrics increments the tool-call counter and records the latency histogram
// per invocation, labelled by tool name and outcome ("ok" | "err"). Outcome
// is "err" when the handler returns an error OR returns an IsError result.
func Metrics(name string) Middleware {
	return func(h Handler) Handler {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			res, err := h(ctx, req)
			outcome := "ok"
			if err != nil || (res != nil && res.IsError) {
				outcome = "err"
			}
			observability.ToolCallTotal.WithLabelValues(name, outcome).Inc()
			observability.ToolCallDuration.WithLabelValues(name, outcome).Observe(time.Since(start).Seconds())
			return res, err
		}
	}
}
