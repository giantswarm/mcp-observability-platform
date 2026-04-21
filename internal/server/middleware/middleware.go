// Package middleware holds the cross-cutting concerns applied to every MCP
// tool handler: a tracing span and the Prometheus counter/histogram.
//
// Middlewares use mcp-go's built-in mechanism — `server.ToolHandlerMiddleware`
// plus the `WithToolHandlerMiddleware` server option (or `MCPServer.Use`) —
// so they run automatically on every tool call without per-handler wrapping.
// The tool name is read from the request (`req.Params.Name`) rather than
// threaded through a closure argument.
//
// Wiring happens once in `internal/server.New`:
//
//	mcpsrv.WithRecovery(),                                   // panic guard (mcp-go)
//	mcpsrv.WithToolHandlerMiddleware(middleware.Tracing()),  // OTEL span
//	mcpsrv.WithToolHandlerMiddleware(middleware.Metrics()),  // counter + histogram
//
// Extending the stack (e.g. Audit, progress, rate-limit) only requires adding
// another `WithToolHandlerMiddleware` call — tool registration code stays
// untouched.
package middleware

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/giantswarm/mcp-observability-platform/internal/observability"
)

var tracer = otel.Tracer("github.com/giantswarm/mcp-observability-platform/internal/server/middleware")

// Outcome values — used as the `outcome` metric label and the `tool.outcome`
// span attribute. Three buckets so operators can tell a real incident
// (system_error) from an expected user-visible failure (user_error). Semantic
// names rather than HTTP-style codes so they're not confused with the
// transport-level HTTP codes mcp-go returns (e.g. 401 on OAuth failure) —
// tool calls always succeed at the HTTP layer and carry their error signal
// inside the 200 response body via `isError`.
const (
	OutcomeOK          = "ok"
	OutcomeUserError   = "user_error"
	OutcomeSystemError = "system_error"
)

// classify maps a tool handler's return to an outcome.
//
//   - Go error       → system_error: upstream unreachable, panic (after
//     mcp-go's WithRecovery wraps it into an error), handler bug. Ops-
//     actionable.
//   - IsError result → user_error: tool reported a user-visible failure
//     (missing arg, not authorised, response_too_large). Expected behaviour.
//   - otherwise      → ok.
//
// Shared by Tracing and Metrics so the metric label and span attribute
// never drift apart.
func classify(res *mcp.CallToolResult, err error) string {
	switch {
	case err != nil:
		return OutcomeSystemError
	case res != nil && res.IsError:
		return OutcomeUserError
	default:
		return OutcomeOK
	}
}

// Tracing opens an OTEL span "tool.<name>" around each tool invocation so
// downstream Grafana / HTTP spans attach beneath it. The span is marked
// Error ONLY on system_error — user_error is expected behaviour, same
// convention as HTTP servers not marking 4xx spans Error. The outcome is
// always recorded as the `tool.outcome` span attribute.
func Tracing() server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ctx, span := tracer.Start(ctx, "tool."+req.Params.Name)
			defer span.End()
			res, err := next(ctx, req)
			o := classify(res, err)
			span.SetAttributes(attribute.String("tool.outcome", o))
			if o == OutcomeSystemError {
				span.SetStatus(codes.Error, "tool returned error")
			}
			return res, err
		}
	}
}

// Metrics increments the tool-call counter and records the latency histogram
// per invocation, labelled by tool name and outcome
// ("ok" | "user_error" | "system_error"; see package docs).
func Metrics() server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			res, err := next(ctx, req)
			o := classify(res, err)
			observability.ToolCallTotal.WithLabelValues(req.Params.Name, o).Inc()
			observability.ToolCallDuration.WithLabelValues(req.Params.Name, o).Observe(time.Since(start).Seconds())
			return res, err
		}
	}
}
