package authz

import (
	"testing"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/stretchr/testify/require"
)

func TestGrafanaUser(t *testing.T) {
	for _, test := range []struct {
		name string
		user *providers.UserInfo
		want string
	}{
		{name: "absent"},
		{name: "missing issuer", user: &providers.UserInfo{ID: testSubject}},
		{name: "missing subject", user: &providers.UserInfo{Issuer: testIssuer, Email: testEmail, EmailVerified: true}},
		{name: "verified email", user: &providers.UserInfo{Issuer: testIssuer, ID: testSubject, Email: testEmail, EmailVerified: true}, want: testEmail},
		{name: "unverified email", user: &providers.UserInfo{Issuer: testIssuer, ID: testSubject, Email: testEmail}, want: testSubject},
		{name: "subject only", user: &providers.UserInfo{Issuer: testIssuer, ID: testSubject}, want: testSubject},
		{name: "actor without issuer", user: &providers.UserInfo{Issuer: testIssuer, ID: testSubject, ActorSubject: "agent"}, want: testSubject},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.user != nil {
				ctx = handler.ContextWithUserInfo(ctx, test.user)
			}
			require.Equal(t, test.want, GrafanaUser(ctx))
		})
	}
}
