package middleware

import "go.opentelemetry.io/otel"

var tracer = otel.Tracer("github.com/giantswarm/mcp-observability-platform/internal/server/middleware")
