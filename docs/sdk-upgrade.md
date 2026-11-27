# Native SDK upgrade

The server uses the official Go SDK directly for registration, discovery,
streamable HTTP, SSE and tool results. Tool names and arguments remain available.
Upstream schema constraints, including `additionalProperties`, are preserved.
Streamable HTTP is stateless. Older clients can initialize with versions
2024-11-05, 2025-03-26, 2025-06-18 or 2025-11-25. All HTTP methods reject a
cross-origin browser request; non-browser clients can omit Origin.

Before deployment, inventory the enabled installations and confirm that each
connector supplies validated issuer and subject claims. Grafana email lookup
uses only a verified email. Check the affected callers against Grafana membership;
do not infer or merge accounts from matching emails. Missing principal issuer or
subject is rejected. Delegated actors may omit their issuer, as permitted by
RFC 8693. Role cache entries are temporary and require no data
migration. A changed verified email triggers a fresh role lookup.

Persistent Valkey storage requires `OAUTH_ENCRYPTION_KEY` or its supported file
setting, including local HTTP development. Provision the existing encryption key
through the deployment's secret mechanism. Do not rotate or discard a key as an
upgrade workaround. Memory storage remains available for local development.

`TOOL_TIMEOUT=0` and `TOOL_MAX_RESPONSE_BYTES=0` retain their disable semantics.
The response limit covers the full serialized result and returns an MCP error
instead of truncating it. Audit entries include issuer, subject, actor, tool and
outcome; they exclude profiles, arguments, raw errors and credentials.

OAuth configuration and lifecycle use `oauthconfig.SettingsFromEnv` and `Setup`.
The validated `providers.UserInfo` travels directly to authorization and tools.
Audit identifiers are nested under `user`: `user.issuer`, `user.subject`,
`user.actor.issuer` and `user.actor.subject`. Update log queries that expect
top-level identity fields. Actor fields are emitted only when supplied; the actor
issuer is never inferred from the principal issuer. Identifier values can contain
personal data, so existing log access and retention controls still apply.

Trusted issuers use the shared library's configuration:

```json
[{"issuer":"https://issuer.example","jwksURI":"https://issuer.example/keys","audiences":["https://mcp.example/mcp"],"subjectClaim":"email"}]
```

`jwksURL`, `allowedAudiences` and `allowPrivateIPJWKS` remain accepted for one
overlap release with a startup warning. The boolean private-JWKS setting becomes
an allowlist for the configured JWKS host. Supplying both an old key and its
replacement fails startup. All issuer entries require HTTPS JWKS URLs and explicit
audiences, including configurations using the old keys; migrate plain-HTTP JWKS
endpoints before upgrading. `subjectClaim=email` requires `email_verified=true`.
The library retains the original `sub` in `UserInfo.ID`; role caches use that ID,
its issuer and the verified Grafana lookup identity.

`OAUTH_AUTHORIZATION_SERVERS` advertises explicitly trusted login issuers. The
chart renders it from `oauth.authorizationServers`, and `oauth.resource` sets
`OAUTH_RESOURCE`. Existing additional-resource metadata is preserved. Set
`oauth.enableAuthorizationServer=false` to run only as a resource server; Dex
credentials and local authorization endpoints are then omitted. Supply trusted
issuers and authorization servers from administrator-controlled configuration.
CA bundles use the library's `caFile` and `OAUTH_DEX_CA_FILE` settings.

Forwarded ID tokens and configured broker audiences retain their compatibility
behavior. `OAUTH_REQUIRE_RESOURCE_ACCESS_TOKENS=true` requires access tokens bound
to `OAUTH_RESOURCE` and rejects forwarded ID tokens. Enable it only after caller
credentials have migrated; advertising authorization servers alone does not change
that audience policy.

The official SDK bounds HTTP request bodies to 4 MiB by default and returns 413
for oversized bodies, including chunked requests. Chart ingress separates HTTP
from metrics: platform Muster/agentgateway and the default Envoy Gateway reach
HTTP; kube-system Alloy metrics pods reach metrics. Customize the raw
`networkPolicy.ingress` rules for another gateway or collector. Each peer combines
its namespace and pod selectors. Explicit existing ingress overrides remain in
effect and must be reviewed before cutover. Local test clusters can use
`networkPolicy.enabled=false`. ServiceMonitors require the configured tenant,
defaulting to `giantswarm`. NetworkPolicy filters TCP peers and ports; gateway
routing and application authentication enforce HTTP access.

SSE sessions belong to the validated caller profile that opened the stream.
After an identity or verified email change, reconnect and initialize a new session.
If Grafana lookup fails after an email change, authenticate to Grafana and verify
the account's organization membership before retrying. Do not disable OAuth,
`RequireCaller` or organization authorization to restore access.

Send the following notice only through an authorized channel before deployment.
Keep cutover blocked for unresolved caller
or connector mappings. Recovery may restore the prior deployment and its existing
storage key while retaining authentication and organization checks.

```text
The observability MCP update uses the official SDK with stateless HTTP; older supported clients remain compatible.
Grafana access requires a verified email and current organization membership. Reconnect SSE after an identity or verified email change.
If access fails, verify Grafana membership and sign in again; authentication and organization authorization remain enforced during recovery.
```
