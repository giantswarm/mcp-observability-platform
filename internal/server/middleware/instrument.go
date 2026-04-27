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

	"github.com/giantswarm/mcp-observability-platform/internal/audit"
	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// Instrument is the composite observability middleware: one OTEL span,
// one metric pair, one slog-based audit record per tool invocation.
// Classify(res, err) is computed once and fanned out so the span
// status, metric label, and audit outcome stay in lockstep.
//
// auditLogger is a dedicated *slog.Logger (typically JSON to stderr) so
// audit records can be routed independently of the app's main log
// stream. nil disables the audit emission while keeping span + metric.
//
// The handler error is propagated unchanged.
func Instrument(auditLogger *slog.Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name := req.Params.Name
			ctx, span := tracer.Start(ctx, "tool."+name)
			defer span.End()
			start := time.Now()

			res, err := next(ctx, req)

			outcome := Classify(res, err)
			duration := time.Since(start)

			// Span: record outcome on every call; mark Error only on
			// system_error — user_error is expected behaviour, same
			// convention as HTTP servers not marking 4xx spans Error.
			span.SetAttributes(attribute.String("tool.outcome", outcome))
			if outcome == OutcomeSystemError {
				span.SetStatus(codes.Error, "tool returned error")
			}

			// Metrics: counter + latency histogram, labelled by tool + outcome.
			observability.ToolCallTotal.WithLabelValues(name, outcome).Inc()
			observability.ToolCallDuration.WithLabelValues(name, outcome).Observe(duration.Seconds())

			// Audit: structured slog record on the dedicated audit
			// stream. Emit happens inside this middleware (not in a
			// dedicated audit package wrapper) so the schema lives
			// alongside the metric/span — three signals, one place.
			if auditLogger != nil {
				traceID, spanID := traceIDs(ctx)
				auditLogger.LogAttrs(ctx, slog.LevelInfo, "tool_call",
					slog.Time("timestamp", start),
					slog.String("caller", authz.CallerSubject(ctx)),
					slog.String("caller_token_source", authz.CallerTokenSource(ctx)),
					slog.String("tool", name),
					slog.Any("args", audit.TruncateArgs(req.GetArguments())),
					slog.String("outcome", outcome),
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
