package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/oauthconfig"
	"github.com/giantswarm/mcp-oauth/providers/dex"
	oauthserver "github.com/giantswarm/mcp-oauth/server"
)

const envOAuthTrustedIssuers = "OAUTH_TRUSTED_ISSUERS"

// loadOAuthSettings resolves application defaults before background workers start.
func loadOAuthSettings(logger *slog.Logger, transport string) (oauthconfig.Settings, error) {
	authorizationServer, err := envBool("OAUTH_ENABLE_AUTHORIZATION_SERVER", true)
	if err != nil {
		return oauthconfig.Settings{}, err
	}
	defaults := map[string]string{"OAUTH_ENABLE_CLIENT_ID_METADATA_DOCUMENTS": "true"}
	if authorizationServer {
		defaults["OAUTH_PROVIDER"] = "dex"
	}
	for name, value := range defaults {
		if os.Getenv(name) == "" {
			if err := os.Setenv(name, value); err != nil {
				return oauthconfig.Settings{}, err
			}
		}
	}
	normalized, deprecated, err := normalizeTrustedIssuers(os.Getenv(envOAuthTrustedIssuers))
	if err != nil {
		return oauthconfig.Settings{}, err
	}
	if deprecated {
		logger.Warn("OAUTH_TRUSTED_ISSUERS uses deprecated keys; use jwksURI, audiences and allowPrivateIPJWKSHosts before the next minor release")
		if err := os.Setenv(envOAuthTrustedIssuers, normalized); err != nil {
			return oauthconfig.Settings{}, err
		}
	}
	settings, err := oauthconfig.SettingsFromEnv()
	if err != nil {
		return oauthconfig.Settings{}, err
	}
	if settings.Dex != nil {
		if err := dex.ValidateAudience(settings.Dex.ClientID); err != nil {
			return oauthconfig.Settings{}, fmt.Errorf("OAUTH_DEX_CLIENT_ID: %w", err)
		}
		settings.Dex.Logger = logger
	}
	settings.Logger = logger
	if settings.Valkey == nil && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		logger.Warn("in-memory OAuth state is not shared across replicas or retained on restart; configure Valkey for persistent state")
	}
	resourcePath := mcpPath
	if transport == transportSSE {
		resourcePath = ssePath
	}
	if settings.Server.ResourceMetadataByPath == nil {
		settings.Server.ResourceMetadataByPath = make(map[string]oauthserver.ProtectedResourceConfig)
	}
	for path := range settings.Server.ResourceMetadataByPath {
		if strings.TrimRight(path, "/") == resourcePath {
			return settings, nil
		}
	}
	mainResource, err := url.Parse(settings.Server.GetResourceIdentifier())
	if err != nil {
		return oauthconfig.Settings{}, fmt.Errorf("OAuth resource: %w", err)
	}
	mainPath := mainResource.EscapedPath()
	if mainPath == "" {
		mainPath = "/"
	}
	mainMetadata := settings.Server.ResourceMetadataByPath[mainPath]
	settings.Server.ResourceMetadataByPath[resourcePath] = oauthserver.ProtectedResourceConfig{AuthorizationServers: mainMetadata.AuthorizationServers}
	return settings, nil
}

func buildOAuthHandler(settings oauthconfig.Settings) (*handler.Handler, func(context.Context) error, error) {
	runtime, cleanup, err := oauthconfig.Setup(settings)
	if err != nil {
		return nil, nil, fmt.Errorf("OAuth setup: %w", err)
	}
	return runtime.Handler, cleanup, nil
}

func normalizeTrustedIssuers(raw string) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, false, nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return "", false, fmt.Errorf("%s: invalid issuer list", envOAuthTrustedIssuers)
	}
	deprecated := false
	for index, entry := range entries {
		for oldName, name := range map[string]string{"jwksURL": "jwksURI", "allowedAudiences": "audiences"} {
			if value, present := entry[oldName]; present {
				if _, conflict := entry[name]; conflict {
					return "", false, fmt.Errorf("%s[%d]: use either %s or %s", envOAuthTrustedIssuers, index, oldName, name)
				}
				entry[name] = value
				delete(entry, oldName)
				deprecated = true
			}
		}
		if value, present := entry["allowPrivateIPJWKS"]; present {
			var enabled bool
			if err := json.Unmarshal(value, &enabled); err != nil {
				return "", false, fmt.Errorf("%s[%d]: allowPrivateIPJWKS must be a boolean", envOAuthTrustedIssuers, index)
			}
			if enabled {
				var endpoint string
				if err := json.Unmarshal(entry["jwksURI"], &endpoint); err != nil {
					return "", false, fmt.Errorf("%s[%d]: jwksURI must be a URL", envOAuthTrustedIssuers, index)
				}
				parsed, err := url.Parse(endpoint)
				if err != nil || parsed.Hostname() == "" {
					return "", false, fmt.Errorf("%s[%d]: jwksURI must name a host", envOAuthTrustedIssuers, index)
				}
				var hosts []string
				if existing, present := entry["allowPrivateIPJWKSHosts"]; present {
					if err := json.Unmarshal(existing, &hosts); err != nil {
						return "", false, fmt.Errorf("%s[%d]: allowPrivateIPJWKSHosts must be a string list", envOAuthTrustedIssuers, index)
					}
				}
				if !slices.Contains(hosts, parsed.Hostname()) {
					hosts = append(hosts, parsed.Hostname())
				}
				entry["allowPrivateIPJWKSHosts"], _ = json.Marshal(hosts)
			}
			delete(entry, "allowPrivateIPJWKS")
			deprecated = true
		}
	}
	encoded, err := json.Marshal(entries)
	return string(encoded), deprecated, err
}
