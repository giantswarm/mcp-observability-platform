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
do not infer or merge accounts from matching emails. Incomplete principal or actor
identities are rejected. Role cache entries are temporary and require no data
migration. A changed verified email triggers a fresh role lookup.

Persistent Valkey storage requires `OAUTH_ENCRYPTION_KEY` or its supported file
setting, including local HTTP development. Provision the existing encryption key
through the deployment's secret mechanism. Do not rotate or discard a key as an
upgrade workaround. Memory storage remains available for local development.

`TOOL_TIMEOUT=0` and `TOOL_MAX_RESPONSE_BYTES=0` retain their disable semantics.
The response limit covers the full serialized result and returns an MCP error
instead of truncating it. Audit entries include issuer, subject, actor, tool and
outcome; they exclude profiles, arguments, raw errors and credentials.

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
