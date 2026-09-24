package cmd

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers"
	oauthserver "github.com/giantswarm/mcp-oauth/server"
	"github.com/giantswarm/mcp-oauth/storage/memory"
	"golang.org/x/oauth2"
)

func TestIDTokenResolver(t *testing.T) {
	const (
		mcpBearer  = "mcp-opaque-token"
		dexIDToken = "dex-id-token"
	)
	store := memory.New()
	t.Cleanup(store.Stop)
	dexTok := (&oauth2.Token{AccessToken: "dex-access", Expiry: time.Now().Add(time.Hour)}).
		WithExtra(map[string]any{"id_token": dexIDToken})
	if err := store.SaveToken(context.Background(), mcpBearer, dexTok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	resolve := idTokenResolver(store)

	tests := []struct {
		name   string
		source providers.TokenSource
		bearer string
		want   string
	}{
		{"oauth looks up the stored Dex token by bearer", providers.TokenSourceOAuth, mcpBearer, dexIDToken},
		{"oauth with unknown bearer", providers.TokenSourceOAuth, "unknown", ""},
		{"sso forwards the bearer", providers.TokenSourceSSO, "forwarded-dex-id-token", "forwarded-dex-id-token"},
		{"trusted-issuer has no Dex token", providers.TokenSourceTrustedIssuer, "muster-jwt", ""},
		{"self-issued jwt has no Dex token", providers.TokenSourceJWT, "self-jwt", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/mcp", nil)
			r.Header.Set("Authorization", "Bearer "+tt.bearer)
			r = r.WithContext(handler.ContextWithUserInfo(r.Context(), &providers.UserInfo{ID: "sub", TokenSource: tt.source}))
			if got := resolve(r.Context(), r); got != tt.want {
				t.Errorf("resolve = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("no user info", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+mcpBearer)
		if got := resolve(r.Context(), r); got != "" {
			t.Errorf("resolve = %q, want empty without validated UserInfo", got)
		}
	})
}

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
