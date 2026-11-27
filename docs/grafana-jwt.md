# Grafana JWT authentication

`GRAFANA_AUTH_MODE=jwt` (Helm: `grafana.authMode: jwt`) drops the shared
server-admin credential. The MCP forwards each caller's own Dex `id_token`
to Grafana in `GRAFANA_JWT_HEADER`. Grafana validates it against Dex,
resolves the user and applies the user's own org roles. Audit logs show
the real user.

In this mode:

- No Grafana secret is needed. Setting `GRAFANA_SA_TOKEN` or
  `GRAFANA_BASIC_AUTH` is a startup error.
- The startup server-admin self-check (`GET /api/orgs`) is skipped.
- The authorizer reads the caller's orgs via `GET /api/user/orgs` with the
  caller's token.
- `MCP_TRANSPORT=stdio` is rejected. Stdio has no caller token.
- Token responses never carry the Dex `id_token`. The MCP keeps it
  server-side. `OAUTH_OMIT_ID_TOKEN_IN_TOKEN_RESPONSE=false` is rejected.
- Set `GRAFANA_PUBLIC_URL` to the public URL of the main Grafana. Without
  it, links use the `appUrl` of `grafana-mcp`. The
  `/api/frontend/settings` probe runs without credentials.

Callers:

| Caller | Works | Token sent to Grafana |
| --- | --- | --- |
| MCP OAuth client (Claude Code, mcp-inspector, …) | yes | The stored Dex `id_token` for the caller's MCP access token. mcp-oauth refreshes it. |
| SSO-forwarded Dex ID token (e.g. muster `forwardToken: true`) | yes | The forwarded token as is. Its `aud` is the audiences muster requested; Grafana's `expect_claims.aud` must list one of them. |
| Trusted OpenID Provider (`OAUTH_TRUSTED_ISSUERS` with `openIDProvider: true`) | yes | Its validated ID token. Grafana must trust the same issuer and audience. |
| Other trusted-issuer caller, including workload access tokens | no | The request receives 401 before any Grafana call. |

The MCP sends the caller token to a dedicated Grafana instance
(`grafana-mcp`) that shares the database of the main Grafana. The main
Grafana keeps its own `[auth.jwt]` section, for example for Teleport app
access. Set `GRAFANA_URL` to the `grafana-mcp` service.

Each person gets an MCP Grafana user, separate from their browser user.
Its login is the Dex connector id and the email, with `@` replaced by
`_at_`: `<connector_id>:<email with @ replaced by _at_>`, for example
`giantswarm-github:theo_at_giantswarm.io`. Its display name comes from the
`name` claim. `grafana-mcp` maps no email claim, so Grafana never links a
caller token to an existing user by email. Users sign up on first call.

`grafana-mcp` config:

```ini
[auth]
disable_login_form = true

[auth.basic]
enabled = false

[auth.jwt]
enabled = true
header_name = X-JWT-Assertion
jwk_set_url = https://dex.<domain>/keys
cache_ttl = 5m
expect_claims = {"iss": "https://dex.<domain>", "aud": "mcp-observability-platform"}
; login: <connector_id>:<email with @ replaced by _at_>
username_attribute_path = join(':', [federated_claims.connector_id, replace(email, '@', '_at_')])
auto_sign_up = true
org_attribute_path = groups
role_attribute_path = to_string('None')
role_attribute_strict = true

[unified_alerting]
execute_alerts = false
```

- The caller token must carry `federated_claims.connector_id` (Dex scope
  `federated:id`, which the MCP requests by default) and `email`. A token
  without either fails authentication.
- `GF_AUTH_JWT_ORG_MAPPING` sets the org mapping. observability-operator
  renders it from the `GrafanaOrganization` CRs into Secret
  `grafana-mcp-org-mapping`, key `orgMapping`. Org roles come only from
  this mapping, read from the `groups` claim.
- A caller with no mapped group gets role None in the default org.
- `[auth.basic] enabled = false` rejects the admin password of the shared
  database on `grafana-mcp`.
- `[unified_alerting] execute_alerts = false` leaves alert evaluation to
  the main Grafana. The alerting API stays available to the alert tools.
- `cache_ttl = 5m` bounds failures after a Dex signing-key rotation.
  Grafana does not refetch keys on an unknown key id.

Limitations:

- `org_mapping` is static ini. Grafana reads it and resolves org names
  once at start-up, so a mapping change needs a `grafana-mcp` restart.
- `expect_claims.aud` accepts an array and passes when any entry matches.
  List the MCP Dex client id plus every `OAUTH_TRUSTED_AUDIENCES` client
  whose tokens reach the MCP. A token Grafana rejects fails with
  `grafana rejected the caller token`.
- With the in-memory store (`OAUTH_STORAGE_BACKEND=memory`, the default),
  the MCP keeps tokens, client registrations and sign-ins in progress in
  memory. An MCP restart signs every caller out, and MCP clients register
  again.

Incident response: the OAuth store holds recent callers' Dex refresh tokens.
A compromised MCP pod can copy those tokens, the encryption key and the Dex
client secret. Rotating the Dex client secret prevents further refreshes
with the old client credential. Already-issued tokens remain valid until
expiry unless their verifier revokes access. Restarting the MCP does not
revoke tokens; Valkey also retains grants across restarts.

The server resolves provider ID tokens through mcp-oauth after bearer validation. Token responses omit the provider ID token. Dynamic registration accepts loopback redirects by default; CIMD clients retain their registered metadata redirects.
