package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// Instrument emits an OTEL span, the tool_call counter (+ errors
// counter on failure), a duration observation, and a slog audit record
// per call. is_error follows mcp `IsError`, with a Go-error return
// treated as is_error=true. nil logger disables only the audit line.
// The handler error is propagated unchanged.
func Instrument(logger *slog.Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name := req.Params.Name
			ctx, span := tracer.Start(ctx, "tool."+name,
				trace.WithAttributes(
					attribute.String("tool.name", name),
					attribute.String("caller", authz.CallerSubject(ctx)),
					attribute.String("caller.token_source", authz.CallerTokenSource(ctx)),
				),
			)
			defer span.End()
			start := time.Now()

			res, err := next(ctx, req)

			isErr := err != nil || (res != nil && res.IsError)
			duration := time.Since(start)

			// Span status mirrors HTTP-server convention: Error only on
			// Go-error returns; IsError results stay Unset (4xx-equivalent).
			span.SetAttributes(
				attribute.Bool("tool.is_error", isErr),
				attribute.Int64("tool.duration_ms", duration.Milliseconds()),
			)
			if err != nil {
				span.SetStatus(codes.Error, "tool returned error")
			}

			observability.ToolCallTotal.WithLabelValues(name).Inc()
			if isErr {
				observability.ToolCallErrorsTotal.WithLabelValues(name).Inc()
			}
			observability.ToolCallDuration.WithLabelValues(name).Observe(duration.Seconds())

			if logger != nil {
				traceID, spanID := traceIDs(ctx)
				logger.LogAttrs(ctx, slog.LevelInfo, "tool_call",
					slog.Time("timestamp", start),
					slog.String("caller", authz.CallerSubject(ctx)),
					slog.String("caller_token_source", authz.CallerTokenSource(ctx)),
					slog.String("tool", name),
					slog.Any("args", req.GetArguments()),
					slog.Bool("is_error", isErr),
					slog.Int64("duration_ms", duration.Milliseconds()),
					slog.String("error", auditErrorMessage(err, res)),
					slog.String("trace_id", traceID),
					slog.String("span_id", spanID),
				)
			}

			return res, err
		}
	}
}

// auditErrorMessage prefers the Go error, falls back to the IsError
// result's text, empty on success.
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

// traceIDs returns empty strings when no span is on ctx, so every
// audit record carries both fields with a stable schema.
func traceIDs(ctx context.Context) (string, string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}
