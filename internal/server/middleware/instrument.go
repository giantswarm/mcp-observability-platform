package middleware

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// Instrument records the Prometheus tool counters and duration histogram.
func Instrument() func(mcp.ToolHandler) mcp.ToolHandler {
	return func(next mcp.ToolHandler) mcp.ToolHandler {
		return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			started := time.Now()
			result, err := next(ctx, request)
			name := request.Params.Name
			observability.ToolCallTotal.WithLabelValues(name).Inc()
			if err != nil || (result != nil && result.IsError) {
				observability.ToolCallErrorsTotal.WithLabelValues(name).Inc()
			}
			observability.ToolCallDuration.WithLabelValues(name).Observe(time.Since(started).Seconds())
			return result, err
		}
	}
}
