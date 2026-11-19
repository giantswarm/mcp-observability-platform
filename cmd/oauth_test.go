package cmd

import (
	"strconv"
	"testing"

	oauthserver "github.com/giantswarm/mcp-oauth/server"
)

func TestParseTrustedIssuers(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []oauthserver.TrustedIssuer
		wantErr bool
	}{
		{
			name: "empty",
			raw:  "",
			want: nil,
		},
		{
			name: "whitespace only",
			raw:  "  \n\t",
			want: nil,
		},
		{
			name: "single issuer minimal",
			raw:  `[{"issuer":"https://muster.example.com","jwksURL":"https://muster.example.com/jwks"}]`,
			want: []oauthserver.TrustedIssuer{{
				Issuer:  "https://muster.example.com",
				JwksURL: "https://muster.example.com/jwks",
			}},
		},
		{
			name: "all fields mapped",
			raw: `[{
				"issuer":"https://muster.example.com",
				"jwksURL":"http://muster.muster.svc.cluster.local:8080/jwks",
				"subjectClaim":"email",
				"allowedAudiences":["https://muster.example.com/mcp","other"],
				"allowedClaims":{"sub":"system:serviceaccount:kagent:*"},
				"acceptedTypHeaders":["at+jwt",""],
				"allowPrivateIPJWKS":true,
				"allowPrivateIPJWKSHosts":["muster.muster.svc.cluster.local"]
			}]`,
			want: []oauthserver.TrustedIssuer{{
				Issuer:                  "https://muster.example.com",
				JwksURL:                 "http://muster.muster.svc.cluster.local:8080/jwks",
				SubjectClaim:            "email",
				AllowedAudiences:        []string{"https://muster.example.com/mcp", "other"},
				AllowedClaims:           map[string]string{"sub": "system:serviceaccount:kagent:*"},
				AcceptedTypHeaders:      []string{"at+jwt", ""},
				AllowPrivateIPJWKS:      true,
				AllowPrivateIPJWKSHosts: []string{"muster.muster.svc.cluster.local"},
			}},
		},
		{
			name: "multiple issuers",
			raw:  `[{"issuer":"https://a","jwksURL":"https://a/jwks"},{"issuer":"https://b","jwksURL":"https://b/jwks"}]`,
			want: []oauthserver.TrustedIssuer{
				{Issuer: "https://a", JwksURL: "https://a/jwks"},
				{Issuer: "https://b", JwksURL: "https://b/jwks"},
			},
		},
		{
			name:    "malformed JSON",
			raw:     `[{"issuer":`,
			wantErr: true,
		},
		{
			name:    "missing issuer",
			raw:     `[{"jwksURL":"https://muster.example.com/jwks"}]`,
			wantErr: true,
		},
		{
			name:    "missing jwksURL",
			raw:     `[{"issuer":"https://muster.example.com"}]`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTrustedIssuers(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result %+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !trustedIssuersEqual(got, tc.want) {
				t.Fatalf("parseTrustedIssuers() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLoadOAuthConfig_EnableCIMD(t *testing.T) {
	cases := []struct {
		name, env string
		want      bool
		wantErr   bool
	}{
		{"unset defaults to false", "", false, false},
		{"enabled", strconv.FormatBool(true), true, false},
		{"disabled", strconv.FormatBool(false), false, false},
		{"malformed is a hard error", "yes", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("OAUTH_ISSUER", "https://mcp.example.com")
			t.Setenv(envOAuthEnableCIMD, c.env)
			cfg, err := loadOAuthConfig()
			if c.wantErr {
				if err == nil {
					t.Fatalf("%s=%q: want error, got nil", envOAuthEnableCIMD, c.env)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s=%q: unexpected error: %v", envOAuthEnableCIMD, c.env, err)
			}
			if cfg.EnableClientIDMetadataDocuments != c.want {
				t.Errorf("EnableClientIDMetadataDocuments = %v, want %v", cfg.EnableClientIDMetadataDocuments, c.want)
			}
			if !cfg.AllowLocalhostRedirectURIs {
				t.Error("AllowLocalhostRedirectURIs = false, want true")
			}
		})
	}
}

func trustedIssuersEqual(a, b []oauthserver.TrustedIssuer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Issuer != b[i].Issuer ||
			a[i].JwksURL != b[i].JwksURL ||
			a[i].SubjectClaim != b[i].SubjectClaim ||
			a[i].AllowPrivateIPJWKS != b[i].AllowPrivateIPJWKS ||
			!stringSlicesEqual(a[i].AllowedAudiences, b[i].AllowedAudiences) ||
			!stringSlicesEqual(a[i].AllowPrivateIPJWKSHosts, b[i].AllowPrivateIPJWKSHosts) ||
			!stringSlicesEqual(a[i].AcceptedTypHeaders, b[i].AcceptedTypHeaders) ||
			!stringMapsEqual(a[i].AllowedClaims, b[i].AllowedClaims) {
			return false
		}
	}
	return true
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
