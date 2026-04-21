package middleware

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

var tracer = otel.Tracer("github.com/giantswarm/mcp-observability-platform/internal/tools/middleware")

// Tracing opens a server span "tool.<name>" for each invocation so downstream
// Grafana/HTTP spans nest underneath. The span is marked Error when the
// handler returns an error or an IsError result; otherwise left OK.
func Tracing(name string) Middleware {
	return func(h Handler) Handler {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ctx, span := tracer.Start(ctx, "tool."+name)
			defer span.End()
			res, err := h(ctx, req)
			if err != nil || (res != nil && res.IsError) {
				span.SetStatus(codes.Error, "tool returned error")
			}
			return res, err
		}
	}
}
