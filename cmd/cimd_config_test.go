package cmd

import (
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCIMDEnvironmentDefaults(t *testing.T) {
	for _, testCase := range []struct {
		name, value             string
		unset, enabled, invalid bool
	}{
		{name: "variable absent", unset: true, enabled: true},
		{name: "empty selects default", enabled: true},
		{name: "explicit enable", value: strconv.FormatBool(true), enabled: true},
		{name: "explicit disable", value: strconv.FormatBool(false)},
		{name: "invalid", value: "invalid", invalid: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setOAuthTestEnvironment(t)
			t.Setenv("OAUTH_ISSUER", "https://mcp.example")
			t.Setenv("OAUTH_ENABLE_CLIENT_ID_METADATA_DOCUMENTS", testCase.value)
			if testCase.unset {
				require.NoError(t, os.Unsetenv("OAUTH_ENABLE_CLIENT_ID_METADATA_DOCUMENTS"))
			}
			configuration, err := loadOAuthSettings(slog.New(slog.DiscardHandler), transportStreamableHTTP)
			if testCase.invalid {
				require.ErrorContains(t, err, "OAUTH_ENABLE_CLIENT_ID_METADATA_DOCUMENTS")
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.enabled, configuration.Server.EnableClientIDMetadataDocuments)
		})
	}
}
