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

// Instrument is the composite observability middleware: one OTEL span,
// one metric pair, and one structured slog "tool_call" record per
// invocation. Classify(res, err) is computed once and fanned out so the
// span status, metric label, and log outcome stay in lockstep.
//
// logger is the app's slog.Logger (or nil to disable the structured
// log line while keeping span + metric). The "tool_call" msg makes the
// line trivially filterable in any log pipeline. With OTEL tracing
// wired up, the span carries the same caller / outcome / duration
// attributes — the slog line is the fallback for setups without OTLP.
//
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

			outcome := Classify(res, err)
			duration := time.Since(start)

			// Span: record outcome on every call; mark Error only on
			// system_error — user_error is expected behaviour, same
			// convention as HTTP servers not marking 4xx spans Error.
			span.SetAttributes(
				attribute.String("tool.outcome", outcome),
				attribute.Int64("tool.duration_ms", duration.Milliseconds()),
			)
			if outcome == OutcomeSystemError {
				span.SetStatus(codes.Error, "tool returned error")
			}

			// Metrics: counter + latency histogram, labelled by tool + outcome.
			observability.ToolCallTotal.WithLabelValues(name, outcome).Inc()
			observability.ToolCallDuration.WithLabelValues(name, outcome).Observe(duration.Seconds())

			// Structured log line for setups without OTLP traces. With
			// a gateway + traces in place this is redundant — leave
			// logger=nil to disable.
			if logger != nil {
				traceID, spanID := traceIDs(ctx)
				logger.LogAttrs(ctx, slog.LevelInfo, "tool_call",
					slog.Time("timestamp", start),
					slog.String("caller", authz.CallerSubject(ctx)),
					slog.String("caller_token_source", authz.CallerTokenSource(ctx)),
					slog.String("tool", name),
					slog.Any("args", req.GetArguments()),
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
