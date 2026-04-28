package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

var tracer = otel.Tracer("github.com/giantswarm/mcp-observability-platform/internal/server/middleware")

// Instrument is the composite observability middleware: one OTEL span,
// two counters (total + errors) + a duration histogram, and one
// structured slog "tool_call" record per invocation.
//
// logger is the app's slog.Logger; nil disables the structured log line
// while keeping span + metric. The "tool_call" msg makes the line
// trivially filterable in any log pipeline.
//
// The handler error is propagated unchanged.
func Instrument(logger *slog.Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name := req.Params.Name
			ctx, span := tracer.Start(ctx, "tool."+name)
			defer span.End()
			start := time.Now()

			res, err := next(ctx, req)

			isErr := err != nil || (res != nil && res.IsError)
			duration := time.Since(start)

			if isErr {
				span.SetStatus(codes.Error, "tool returned error")
			}

			observability.ToolCallTotal.WithLabelValues(name).Inc()
			observability.ToolCallDuration.WithLabelValues(name).Observe(duration.Seconds())
			if isErr {
				observability.ToolCallErrorsTotal.WithLabelValues(name).Inc()
			}

			// Structured log line for setups without OTLP traces. The
			// audit pipeline (cluster Loki) is the durable record; the
			// span is the live signal. Both fields carry trace_id +
			// span_id so a gateway can correlate.
			if logger != nil {
				traceID, spanID := traceIDs(ctx)
				logger.LogAttrs(ctx, slog.LevelInfo, "tool_call",
					slog.Time("timestamp", start),
					slog.String("caller", authz.CallerSubject(ctx)),
					slog.String("caller_token_source", authz.CallerTokenSource(ctx)),
					slog.String("tool", name),
					slog.Any("args", req.GetArguments()),
					slog.Bool("error", isErr),
					slog.Int64("duration_ms", duration.Milliseconds()),
					slog.String("error_message", auditErrorMessage(err, res)),
					slog.String("trace_id", traceID),
					slog.String("span_id", spanID),
				)
			}

			return res, err
		}
	}
}

// auditErrorMessage picks the most useful string to record as the audit
// error field: the handler error first, otherwise the text of an
// IsError result. Empty when the call succeeded.
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

// traceIDs returns the trace + span IDs from any active span on ctx,
// or empty strings when no span is set. Stable schema: every audit
// record carries both fields.
func traceIDs(ctx context.Context) (string, string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}
