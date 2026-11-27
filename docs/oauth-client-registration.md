# OAuth client registration

CIMD (Client ID Metadata Documents) is enabled whenever OAuth is enabled.
`OAUTH_ENABLE_CLIENT_ID_METADATA_DOCUMENTS` defaults to true when unset or empty.
Set it to `false` to disable CIMD; malformed booleans fail startup. For a chart
deployment, pass this environment variable through the chart's extra environment
values.
Discovery advertises `client_id_metadata_document_supported: true` while enabled.
Unfamiliar clients require the library's default browser-bound consent before
the upstream login. No trusted-client or consent bypass is configured by default.

Prefer pre-registration for known clients and CIMD for unfamiliar clients.
[MCP 2026-07-28 deprecates Dynamic Client Registration (DCR)](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration#dynamic-client-registration).
DCR remains a compatibility fallback: its configuration, registration endpoint
and conditional discovery advertisement continue to work. Enabling CIMD does
not open unauthenticated DCR. Keep the existing registration access controls.
The Helm chart opens unauthenticated DCR by default
(`oauth.allowPublicClientRegistration: true`). Set it to `false` to require a
registration access token.
DCR accepts only loopback redirect URIs (`http` or `https` on `localhost`,
`127.0.0.0/8` or `[::1]`). It refuses any other redirect URI with
`invalid_redirect_uri`, custom schemes from `OAUTH_TRUSTED_REDIRECT_SCHEMES`
included. `OAUTH_RESTRICT_REGISTRATION_TO_LOOPBACK_REDIRECT_URIS=false` lifts
the restriction for local development. CIMD clients are not affected.
Token responses carry only the MCP's own tokens, never the Dex `id_token`.
The [specification's deprecated registry](https://modelcontextprotocol.io/specification/2026-07-28/deprecated)
allows removal in the first specification revision released on or after
2027-07-28. This is not a scheduled mcp-oauth removal date.

## Network and trust

Metadata is fetched over HTTPS with system CA verification, bounded responses,
timeouts and the library's DNS and connection-time SSRF checks. Private,
loopback and link-local metadata destinations remain blocked. An identity
provider's CA or JWKS private-host allowance does not grant access to private
CIMD hosts. Prefer pre-registered metadata for internal clients; any private
redirect exception must name the exact host and retain TLS verification.

The default NetworkPolicy isolates ingress only, so public HTTPS egress is
available. Installations adding Egress isolation must allow DNS, public TCP/443
metadata fetches and any configured proxy, alongside their identity-provider
and backend peers.
The container includes system CA roots. For an HTTPS inspection proxy, extend
system trust with the proxy CA rather than disabling certificate verification.
Verify a real public metadata fetch from the deployed pod after changing egress,
proxy or trust settings.
