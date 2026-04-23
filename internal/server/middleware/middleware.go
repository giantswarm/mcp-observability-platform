package middleware

import (
	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel"
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

// Classify maps a tool handler's return to an outcome.
//
//   - Go error       → system_error: upstream unreachable, panic (after
//     mcp-go's WithRecovery wraps it into an error), handler bug. Ops-
//     actionable.
//   - IsError result → user_error: tool reported a user-visible failure
//     (missing arg, not authorised, response_too_large). Expected behaviour.
//   - otherwise      → ok.
//
// Shared by Instrument and ResponseCap's IsError marker so the metric label,
// span attribute, and audit outcome never drift apart.
func Classify(res *mcp.CallToolResult, err error) string {
	switch {
	case err != nil:
		return OutcomeSystemError
	case res != nil && res.IsError:
		return OutcomeUserError
	default:
		return OutcomeOK
	}
}
