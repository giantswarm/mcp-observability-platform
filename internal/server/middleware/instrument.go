package middleware

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/giantswarm/mcp-observability-platform/internal/audit"
	"github.com/giantswarm/mcp-observability-platform/internal/authz"
	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

// Instrument is the composite observability middleware: one OTEL span, one
// metric pair, one audit record per tool invocation.
//
// Collapsed from the previous Tracing/Metrics/Audit trio so `Classify(res,err)`
// is computed exactly once. Earlier each middleware classified the outcome
// independently — three call sites that had to stay in lockstep. Drift
// between them would have silently desynced the metric label, span status,
// and audit outcome.
//
// The handler error is propagated unchanged. A nil auditor makes the audit
// side-effect a no-op (via Logger.Record's nil-receiver guard), so callers
// without an audit sink can wire Instrument(nil) without a branch.
func Instrument(auditor *audit.Logger) server.ToolHandlerMiddleware {
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

			// Audit: structured record on the audit sink. Logger.Record is
			// nil-safe so a nil auditor short-circuits without a branch here.
			auditor.Record(ctx, audit.Record{
				Timestamp:   start,
				Caller:      authz.CallerSubject(ctx),
				TokenSource: authz.CallerTokenSource(ctx),
				Tool:        name,
				Args:        req.GetArguments(),
				Outcome:     outcome,
				Duration:    duration,
				Error:       auditErrorMessage(err, res),
			})

			return res, err
		}
	}
}
