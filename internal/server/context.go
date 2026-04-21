package server

import (
	"context"

	"github.com/giantswarm/mcp-oauth/providers"
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
