package authz

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/mcp-observability-platform/internal/grafana"
)

func TestRoleCacheSeparatesIssuerAndChangedIdentity(t *testing.T) {
	client := &fakeGrafana{users: map[string]int64{testEmail: 1, testNewEmail: 2}, orgs: map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: testRoleAdmin}}, 2: {{OrgID: 1, Role: testRoleViewer}}}}
	authorizer := NewAuthorizer(registry(newOrg("alpha", "Alpha", 1)), client, time.Minute, time.Second)
	callers := []Caller{
		{Issuer: testIssuer, Subject: testSubject, Email: testEmail},
		{Issuer: "https://second.example", Subject: testSubject, Email: testEmail},
		{Issuer: testIssuer, Subject: testSubject, Email: testNewEmail},
	}
	for index, caller := range callers {
		org, err := authorizer.RequireOrg(WithCaller(t.Context(), caller), "alpha", RoleViewer)
		require.NoError(t, err)
		expected := RoleAdmin
		if index == 2 {
			expected = RoleViewer
		}
		require.Equal(t, expected, org.Role)
	}
	require.Equal(t, 3, client.calls.lookup)
}

func TestRoleCacheExpiresAndStaysBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &fakeGrafana{users: map[string]int64{testEmail: 1}, orgs: map[int64][]grafana.UserOrgMembership{1: {{OrgID: 1, Role: testRoleAdmin}}}}
		authorizer := NewAuthorizer(registry(newOrg("alpha", "Alpha", 1)), client, time.Second, time.Second).(*authorizer)
		first := Caller{Issuer: testIssuer, Subject: testSubject, Email: testEmail}
		_, err := authorizer.ListOrgs(WithCaller(t.Context(), first))
		require.NoError(t, err)
		time.Sleep(time.Second)
		client.orgs[1] = nil
		orgs, err := authorizer.ListOrgs(WithCaller(t.Context(), first))
		require.NoError(t, err)
		require.Empty(t, orgs)
		require.Len(t, authorizer.cache, 1)
		for index := range maxRoleCacheEntries + 5 {
			authorizer.cacheStore(roleCacheKey{issuer: testIssuer, subject: fmt.Sprint(index)}, statusKnown, map[int64]Role{1: RoleViewer})
		}
		require.Len(t, authorizer.cache, maxRoleCacheEntries)
		time.Sleep(time.Second)
		authorizer.cacheStore(first.cacheKey(), statusKnown, map[int64]Role{1: RoleViewer})
		require.Len(t, authorizer.cache, 1)
	})
}
