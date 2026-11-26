package cmd

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientControlsEnvironmentDefaults(t *testing.T) {
	enable, disable, invalid := strconv.FormatBool(true), strconv.FormatBool(false), "not-a-bool"
	for _, testCase := range []struct {
		name                   string
		omit, restrict         string
		unset, jwtMode         bool
		wantOmit, wantRestrict bool
		wantErr                string
	}{
		{name: "variables absent", unset: true, wantOmit: true, wantRestrict: true},
		{name: "empty selects default", wantOmit: true, wantRestrict: true},
		{name: "explicit enable", omit: enable, restrict: enable, wantOmit: true, wantRestrict: true},
		{name: "explicit disable", omit: disable, restrict: disable},
		{name: "jwt mode keeps defaults", jwtMode: true, wantOmit: true, wantRestrict: true},
		{name: "jwt mode allows open redirect URIs", jwtMode: true, restrict: disable, wantOmit: true},
		{name: "jwt mode refuses the id_token in token responses", jwtMode: true, omit: disable, wantErr: envOAuthOmitIDToken},
		{name: "invalid omit", omit: invalid, wantErr: envOAuthOmitIDToken},
		{name: "invalid restrict", restrict: invalid, wantErr: envOAuthRestrictRegistrationLoopback},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("OAUTH_ISSUER", "https://mcp.example")
			t.Setenv(envOAuthOmitIDToken, testCase.omit)
			t.Setenv(envOAuthRestrictRegistrationLoopback, testCase.restrict)
			if testCase.unset {
				require.NoError(t, os.Unsetenv(envOAuthOmitIDToken))
				require.NoError(t, os.Unsetenv(envOAuthRestrictRegistrationLoopback))
			}
			configuration, err := oauthConfigFromEnv(testCase.jwtMode)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				require.Nil(t, configuration)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.wantOmit, configuration.OmitIDTokenInTokenResponse)
			require.Equal(t, testCase.wantRestrict, configuration.RestrictRegistrationToLoopbackRedirectURIs)
		})
	}
}
