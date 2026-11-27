package grafana

import "context"

// userTokenKey is the unexported context key for the caller's own IdP
// token. Unexported so only WithUserToken can set it.
type userTokenKey struct{}

// WithUserToken attaches the caller's own IdP token (a Dex id_token) to
// ctx. In JWT auth mode the client forwards it to Grafana in
// Config.JWTHeader instead of a shared credential. Never log the value.
func WithUserToken(ctx context.Context, providerToken string) context.Context {
	return context.WithValue(ctx, userTokenKey{}, providerToken)
}

// UserTokenFromContext returns the token set by WithUserToken, or "" when
// none is attached.
func UserTokenFromContext(ctx context.Context) string {
	providerToken, _ := ctx.Value(userTokenKey{}).(string)
	return providerToken
}
