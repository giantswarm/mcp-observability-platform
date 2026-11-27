package authz

import (
	"context"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers"
)

type OrgLister interface {
	List(context.Context) ([]Organization, error)
}

// GrafanaUser returns the verified email or stable subject used by Grafana lookup.
func GrafanaUser(ctx context.Context) string {
	user, _ := handler.UserInfoFromContext(ctx)
	return grafanaIdentity(user)
}

func grafanaIdentity(user *providers.UserInfo) string {
	if user == nil || user.Issuer == "" || user.ID == "" {
		return ""
	}
	if user.EmailVerified && user.Email != "" {
		return user.Email
	}
	return user.ID
}
