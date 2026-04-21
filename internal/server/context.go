package server

import (
	"context"

	"github.com/giantswarm/mcp-oauth/providers"

	"github.com/giantswarm/mcp-observability-platform/internal/authz"
)

// callerKey is an unexported type used as the context key for the caller's
// validated identity, so that external packages cannot overwrite it.
type callerKey struct{}

// CallerFromContext returns the validated caller identity attached by the
// OAuth middleware, or (nil, false) if no identity is present.
func CallerFromContext(ctx context.Context) (*providers.UserInfo, bool) {
	ui, ok := ctx.Value(callerKey{}).(*providers.UserInfo)
	return ui, ok
}

// withCaller attaches the caller identity to ctx. Internal only.
func withCaller(ctx context.Context, ui *providers.UserInfo) context.Context {
	if ui == nil {
		return ctx
	}
	return context.WithValue(ctx, callerKey{}, ui)
}

// callerSubject returns a stable identifier for the caller — provider ID
// preferred over email; empty string when no identity is attached. Used for
// audit logs and for the X-Grafana-User header so Grafana's audit log shows
// who-did-what instead of the server-admin SA.
func callerSubject(ctx context.Context) string {
	ui, ok := CallerFromContext(ctx)
	if !ok || ui == nil {
		return ""
	}
	if ui.ID != "" {
		return ui.ID
	}
	return ui.Email
}

// callerAuthz extracts the identifiers the authz resolver needs to ask
// Grafana who this caller is. Returns an empty Caller if no identity is
// attached; the resolver then errors downstream.
//
// Subject is the OIDC sub claim, the stable unique identifier. Login is
// deliberately left empty: OIDC sub is NOT a Grafana login name, and
// collapsing the two here would make Grafana's /api/users/lookup silently
// miss when the user's email/login doesn't match their sub. The resolver
// falls back to Email-based lookup when Login is empty.
func callerAuthz(ctx context.Context) authz.Caller {
	ui, ok := CallerFromContext(ctx)
	if !ok || ui == nil {
		return authz.Caller{}
	}
	return authz.Caller{Email: ui.Email, Subject: ui.ID}
}
