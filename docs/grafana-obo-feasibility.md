# Grafana on-behalf-of authentication — feasibility assessment

Status: **evaluated, not implemented.** Decision requested.

Companion to [`ARCHITECTURE.md`](./ARCHITECTURE.md) ("Threat model") and
[`roadmap.md`](./roadmap.md) §1. Two things live here: a verified answer to
"could the MCP authenticate to Grafana as the caller?", and a comparison of
**all** the options for improving the credential model — so nobody has to
re-derive either.

## Question and answer

> Could the MCP connect to Grafana as the *caller* — RFC 8693 token exchange /
> on-behalf-of — instead of using one shared server-admin credential?

**Yes.** Server-admin is not required for per-request org switching once the
caller is the authenticated principal, and on Grafana OSS the entire read data
path works for a plain Viewer. Every claim below was verified against
`grafana/grafana` source at `v12.4.7` and `v13.1.2`, then exercised end-to-end
against `grafana/grafana-oss:13.0.2` (transcript in the appendix).

**But not "easily".** The MCP-side plumbing is small and mostly mechanical. The
cost is elsewhere: a Grafana config change owned by `observability-operator`, and
a security decision about an instance-wide Grafana setting. Those are the real
work items, and neither is code.

The design is **not** token pass-through. It is *re-minting*: the MCP validates
the caller as it does today, then issues its own short-lived assertion for
Grafana. See "Why re-mint, not forward".

## How the MCP authenticates to Grafana today

One credential, fixed at process start, shared by every caller:

- `internal/grafana/client.go:145` bakes `Authorization: Bearer <GRAFANA_SA_TOKEN>`
  (or `Basic` admin) into an immutable client field; applied to every request at
  the package's single HTTP entry point.
- `cmd/serve.go:136` calls `VerifyServerAdmin` — the pod **refuses to start**
  unless that credential holds the Grafana Admin server role.
- Authorization is therefore ours to enforce, in-process:
  `internal/authz/authorizer.go:216` `load()` calls the admin-only
  `LookupUser` → `UserOrgs` pair and caches `OrgID → Role` per caller subject.
- The caller's identity reaches Grafana only as audit metadata:
  `internal/tools/grafanabind.go:348` `attachGrafana` sets `OrgID` and
  `X-Grafana-User`. Neither changes what the request is permitted to do.

### In practice it is the built-in admin account, and that is forced

The chart offers `grafana.authMode` — `serviceAccountToken` or `basicAuth`
(`helm/mcp-observability-platform/values.yaml`) — and defaults to the former. But
the install bootstrap populates the `basicAuth` branch, copying Grafana's own
`admin-user` / `admin-password` into the MCP's Secret. So the credential in use
is not a scoped service account but **the built-in admin account**: shared with
anyone who can read that Secret, usable to log into the UI, not independently
revocable, and rotatable only by changing Grafana's admin password everywhere.

That is not an oversight — there is no supported alternative:

- `UpdateServiceAccountForm` (`pkg/services/serviceaccounts/models.go`) carries
  only `Name`, `Role` (an *org* role) and `IsDisabled`. There is no
  `isGrafanaAdmin`.
- Grafana's own docs: service accounts "can't be used for instance-wide
  operations… These tasks require a user with Grafana server administrator
  permissions", and "only work in the organization they are created for".
- `PUT /api/admin/users/:id/permissions` does set `IsGrafanaAdmin` on a user row
  (`pkg/api/admin_users.go`, `AdminUpdateUserPermissions`), and a service account
  has a user row — so promotion is technically reachable. But it is undocumented
  for service accounts and version-dependent, which is exactly what
  `internal/grafana/client.go:59-62` anticipates when it explains why the
  `basicAuth` fallback exists at all.

Since the design needs cross-org reach and only a server admin has it, the
current architecture structurally requires Grafana's admin credentials. Both
serious options below are, at bottom, answers to the same question: **how do we
stop shipping Grafana's admin password to this pod?**

Consequence, already recorded in `ARCHITECTURE.md:96`: caller isolation is a
property of our code, not of Grafana, and a process compromise yields every org.

## What Grafana actually supports

The load-bearing section. Each row is source-verified at both pinned tags.

| Fact | Source (`grafana/grafana`) |
|---|---|
| The JWT auth client reads the configured header and **strips a `Bearer ` prefix** — so `[auth.jwt] header_name = Authorization` accepts an ordinary `Authorization: Bearer <jwt>` on the HTTP API | `pkg/services/authn/clients/jwt.go:173` (`retrieveToken`) |
| `X-Grafana-Org-Id` is resolved **before** auth-client dispatch and applies to every client. It is **not** a server-admin-only header | `pkg/services/authn/authnimpl/service.go` (`orgIDFromHeader`, `orgIDHeaderName`; `r.OrgID` set at `Authenticate`) |
| The JWT identity is constructed with `OrgID: r.OrgID`, then the synced user is fetched for that org | `pkg/services/authn/clients/jwt.go` (`Authenticate`); `pkg/services/authn/authnimpl/sync/user_sync.go` (`FetchSyncedUserHook`) |
| On **OSS**, `fixed:datasources:reader` (`datasources:read` + `datasources:query`, scope `datasources:*`) is granted to **Viewer** — guarded by `if !License.FeatureEnabled("dspermissions.enforcement")`, with the literal comment *"when running oss or enterprise without a license all users should be able to query data sources"* | `pkg/api/accesscontrol.go:77-121` (v12.4.7) / `:93-115` (v13.1.2) |
| `GET /api/user/orgs` is self-scoped — it can replace the admin-only `LookupUser` + `UserOrgs` pair outright | `pkg/api/api.go` (`GetSignedInUserOrgList`) |
| `jwt.Test()` requires a parseable JWT carrying `sub`, so `glsa_…` service-account tokens and `Basic …` decline to it and fall through to their own clients — coexistence is safe, no cutover | `pkg/services/authn/clients/jwt.go` (`Test`, `Priority() = 20`) |

The headline follows from rows 1–3: **server-admin is only needed to enter orgs
you are not a member of** — precisely the capability OBO removes. The current
design's central constraint dissolves rather than needing to be worked around.

Two mechanisms explicitly ruled out, so they are not re-proposed:

- **`X-Access-Token` + `X-Grafana-Id`** — the on-behalf-of path `mcp-grafana`
  already supports (`GrafanaConfig.AccessToken` / `.IDToken`) is a Grafana
  **Cloud** mechanism built on access-policy tokens. Not available self-hosted.
- **Grafana's own OAuth2 server / extended-JWT impersonation** — Cloud and
  experimental. Not a foundation to build on.

## Why re-mint, not forward

Forwarding the caller's bearer to Grafana cannot work, for three independent
reasons:

1. **One of the four accepted token shapes is opaque.** `mcp-oauth`
   (`server/validate.go`) accepts a self-issued RFC 9068 JWT, a trusted-issuer
   JWT (muster), a forwarded Dex ID token, **and** an opaque token resolved
   through Dex `userinfo`. An opaque token carries no claims and cannot be
   verified by Grafana at all.
2. **The `aud` is wrong.** Dex tokens are minted for `OAUTH_DEX_CLIENT_ID`;
   muster tokens for muster's own resource identifier. Neither is Grafana.
   `mcp-oauth` states the rule directly (`providers/provider.go`): such a token
   "MUST NOT" be presented to a resource server whose audience differs from its
   `aud`.
3. **The raw token is already gone.** `internal/server/middleware/caller.go:16`
   `ExtractCaller` reduces the request to an `authz.Caller` and drops the bearer.
   That is the right shape and should stay: identity is not a credential.

So: validate by any of the four paths (unchanged), then **mint one uniform,
short-lived assertion** the MCP signs itself. Grafana then has to trust exactly
one key, regardless of how the caller authenticated to us.

## Target architecture

```
caller ──Bearer(any of 4 shapes)──▶ MCP: mcp-oauth ValidateToken ──▶ authz.Caller
                                                                        │
                                              mint (RS256, ~5m TTL)     ▼
                                    iss=<OAUTH_ISSUER>  aud=grafana
                                    sub=<caller.Subject>  email=<caller.Identity()>
                                                                        │
   Grafana ◀── Authorization: Bearer <assertion> + X-Grafana-Org-Id ─────┘
      │
      └─ [auth.jwt] verifies via jwk_set_url ──▶ GET <MCP>/.well-known/grafana-obo-jwks.json
         then applies the caller's own org membership and role
```

Key management is the one place with a sharp edge: the chart ships an HPA, so a
per-pod generated key would publish a different JWKS per replica and produce
intermittent 401s that read as flaky Grafana. The key must come from a **shared
Secret**, and startup must fail loudly if it is missing. Rotation needs a
publish-old-and-new-public-keys phase, or rotation is an outage.

### What still needs a non-user credential

Startup has no caller, so a bootstrap identity remains — but it no longer needs
to be server-admin, which is the actual win:

| Work | Credential after OBO |
|---|---|
| `/api/health` reachability probe (replaces `VerifyServerAdmin`, `cmd/serve.go:136`) | none needed |
| Tempo seed discovery — `internal/tools/tempo.go:122` `findSeedTempoUID` | bootstrap |
| Everything caller-scoped: datasources, proxy queries, all delegated `mcp-grafana` tools | caller assertion |
| `LookupUser` + `UserOrgs` (`internal/authz/authorizer.go:216`) | **deleted** — replaced by `GET /api/user/orgs` as the caller |

Tempo deserves a flag: `registerTempoTools` treats discovery failure as
non-fatal, so a bootstrap credential that silently stops working removes the
entire Tempo tool surface with only a `logger.Warn`.

### Two caches become correctness hazards

- `internal/grafana/client.go:329` `ListDatasources` caches by `OrgID` **alone**.
  Once the credential is per-caller the response is a function of `(org, caller)`
  and the cache is a cross-user leak. Re-keying on the caller is easy; the
  follow-on problem is that the `sync.Map` never evicts, so the keyspace grows
  with the caller set and needs a bound in the same change.
- The Tempo `ProxiedClient` cache is global per datasource UID. Not an authz hole
  (each `CallTool` carries a fresh per-request credential) but it needs the
  reasoning written down rather than inherited.

## Required Grafana configuration (`observability-operator`)

Lift verbatim into the operator issue:

```ini
[auth.jwt]
enabled                    = true
header_name                = Authorization
username_claim             = email
email_claim                = email
jwk_set_url                = http://mcp-observability-platform.<ns>.svc.cluster.local:8080/.well-known/grafana-obo-jwks.json
cache_ttl                  = 60m
expect_claims              = {"iss":"<OAUTH_ISSUER>","aud":"grafana"}
auto_sign_up               = false
skip_org_role_sync         = true
allow_assign_grafana_admin = false
url_login                  = false
```

Env form: `GF_AUTH_JWT_ENABLED`, `GF_AUTH_JWT_HEADER_NAME`,
`GF_AUTH_JWT_USERNAME_CLAIM`, `GF_AUTH_JWT_EMAIL_CLAIM`,
`GF_AUTH_JWT_JWK_SET_URL`, `GF_AUTH_JWT_CACHE_TTL`,
`GF_AUTH_JWT_EXPECT_CLAIMS`, `GF_AUTH_JWT_AUTO_SIGN_UP`,
`GF_AUTH_JWT_SKIP_ORG_ROLE_SYNC`, `GF_AUTH_JWT_ALLOW_ASSIGN_GRAFANA_ADMIN`,
`GF_AUTH_JWT_URL_LOGIN`.

Points requiring agreement, not just a merge:

1. **`skip_org_role_sync = true` is mandatory.** Left false, Grafana's `OrgSync`
   hook runs on every JWT authentication and can overwrite the org memberships
   and roles SSO established at login. Highest-consequence line here — ask the
   operator to refuse to render an `auth.jwt` block without it.
2. **`expect_claims` must pin both `iss` and `aud`.** Without it, any JWT whose
   signature verifies against a key in the JWKS is accepted; with it, a Dex ID
   token presented straight to Grafana is rejected (verified — case P).
3. **`auto_sign_up = false`.** The MCP must never provision Grafana users.
4. **Ownership of `jwk_set_url`.** It couples Grafana's config to this chart's
   Service DNS. Prefer an operator field over a hardcoded name.
5. **NetworkPolicy** for Grafana pod → MCP Service `:8080`.
6. **New soft dependency**: Grafana's auth chain now fetches a JWKS from the MCP.
   `cache_ttl = 60m` makes restarts and rollouts invisible; a longer MCP outage
   would break MCP-originated auth only.
7. **Independent rollback** — the Grafana flag and the MCP flag must be
   togglable in either order.

## Risks and open decisions

1. **`header_name = Authorization` is instance-wide.** Anyone holding the MCP's
   signing key can impersonate any Grafana user on that instance, including
   Grafana Admins. This needs a named security sign-off. It is also the one thing
   per-org SAs cap and OBO does not — see the comparison below.
2. **Server-admin → Viewer is a strict permission downgrade.** Enumerate the tool
   surface before flipping. The method must be a Viewer-role integration test,
   not desk analysis: a plausible-looking prediction that
   `alerting_manage_rules` would break for Viewers turned out to be wrong
   (`/api/v1/provisioning/alert-rules` is an `EvalAny` that also accepts
   `AlertingRuleRead` + `FoldersRead`, and `fixed:alerting.rules:reader` is
   granted to Viewer — confirmed 200 in case I). Desk analysis produced a false
   positive here; it can equally produce a false negative.
3. **Distinguishing "unknown caller" from "OBO is broken".** Today
   `ErrCallerUnknownToGrafana` means an unambiguous 404 from
   `/api/users/lookup`. Under OBO both conditions surface as 401, and
   `list_orgs` would cheerfully tell every user to "log into Grafana first"
   during a total outage. Grafana does discriminate, though — `messageId` is
   `user.sync.signup-disabled` for an unknown user versus `jwt.invalid` for a
   signature or claims failure (cases O and P). Pair that with a startup +
   periodic self-test surfaced on `readyz` and a metric.
4. **A user's Grafana default org must be valid.** With no `X-Grafana-Org-Id`,
   `/api/user/orgs` resolves the caller's default org for the auth check. Low
   frequency, same failure Grafana's own UI has.
5. **Per-request cost.** One RSA sign per request that touches Grafana (mint
   lazily, once per request, not at ingress) plus a Grafana user fetch per
   authentication. `cache_ttl` covers JWKS, not the user fetch.

## Options for improving this

| # | Option | Removes the admin credential? | Grafana enforces the caller? | Per-user audit | Effort | Cross-repo cost |
|---|---|---|---|---|---|---|
| 0 | Status quo — built-in admin password | ✗ | ✗ | ✗ | — | — |
| 1 | Harden around it — rotation, NetworkPolicy, auth-failure alerting, keep the surface read-only | ✗ | ✗ | ✗ | S | none |
| 1b | Promote a service account to server-admin via `PUT /api/admin/users/:id/permissions` | partly | ✗ | ✗ | S | none |
| 2 | Per-org SA tokens ([`roadmap.md`](./roadmap.md) §1) | ✓ | ✗ | ✗ | L | operator: SA lifecycle machinery |
| 3 | Per-user on-behalf-of (`[auth.jwt]`) | ✓ | ✓ | ✓ | L | operator: config + security sign-off |
| 4 | One MCP deployment per org | ✓ | ✗ | ✗ | M–L | deployment topology |

The deciding trade-off for each:

- **1 — harden around it.** Orthogonal and cheap; worth doing whichever
  architecture wins. But it moves the blast radius by nothing, so it must not be
  allowed to substitute for a decision.
- **1b — promote an SA.** Same privilege ceiling as today, but independently
  revocable, no UI login, and rotatable without changing Grafana's admin
  password. Unsupported for service accounts and version-dependent (see above),
  so verify against the target Grafana first. A stopgap, not an answer.
- **2 — per-org SA tokens.** Removes the admin credential using only supported
  Grafana APIs, with no change to Grafana's auth chain. Its ceiling: every caller
  in org X still acts as org X's service account, so caller isolation remains
  *our* code's responsibility. It also hands `observability-operator` a full
  credential lifecycle to build — create, store, rotate, revoke, distribute.
- **3 — per-user OBO.** The only option where Grafana becomes the enforcer;
  per-user audit follows for free. Costs a signing key that can impersonate any
  Grafana user, and makes Grafana's auth chain softly dependent on the MCP's
  JWKS. It still needs a bootstrap identity for Tempo seed discovery, but that
  can be a minted assertion for a designated Viewer — so no admin credential
  survives anywhere.
- **4 — one MCP per org.** The hardest isolation, because it is a process
  boundary rather than a code path. But it multiplies deployments, OAuth clients
  and routes, contradicts the "one MCP per Grafana" constraint in
  [`ARCHITECTURE.md`](./ARCHITECTURE.md), and *still* gives no per-user
  enforcement inside an org.

Ruled out, for the record: forwarding the caller's token (opaque tokens cannot be
forwarded; `aud` mismatch on the rest — see "Why re-mint, not forward"); Grafana
Cloud's `X-Access-Token` / `X-Grafana-Id` OBO (not available self-hosted); and
Grafana Enterprise datasource permissions, which would *break* rather than help —
they are what turns `fixed:datasources:reader` from a Viewer grant into an
Admin-only one, removing the property option 3 depends on.

### The real choice: 2 vs. 3

|  | Per-org SA tokens | On-behalf-of |
|---|---|---|
| Blast radius on compromise | one org per token | any Grafana user, incl. Admins (signing key) |
| Enforcement of caller isolation | still our code (`RequireOrg`) | **Grafana** |
| Per-user granularity inside an org | none — every caller is the org's SA | caller's own role |
| Grafana audit trail | the SA | the human |
| Credential lifecycle | N long-lived SAs to provision, store, rotate, revoke in a second repo | one signing key, one Secret, this repo |
| Cross-repo cost | new operator machinery (SA lifecycle + Secret distribution) | operator **config** + sign-off |
| Removes the server-admin requirement | no | **yes** |

They are not complementary in practice — they touch the same call sites, and
per-org SAs leave the in-process authorization model untouched, so a bug in
`accessibleOrgs` or a stale cache remains the only thing between a caller and
another org's data.

The honest caveat: per-org SAs cap the ceiling on a compromise and OBO does not.
But today's credential has the *same* ceiling as OBO **and** no per-user
enforcement — so OBO is strictly better than the status quo on both axes, while
per-org SAs improve only one.

### Recommendation

**Do 1 now, then 3.** Option 1 is free and independent of the architecture
decision. Option 3 is the only one that fixes the actual defect — that caller
isolation is enforced by our code rather than by Grafana — and it subsumes
roadmap §1: once OBO ships, that item collapses to a one-line ops change, "make
the bootstrap identity a non-admin Viewer".

If the instance-wide `header_name = Authorization` risk is unacceptable to
whoever owns Grafana's configuration, **option 2 is the honest fallback.** It
removes the admin password — most of the immediate exposure — without touching
Grafana's auth chain.

Do not build 2 and 3 both.

## Appendix — empirical verification

`grafana/grafana-oss:13.0.2`, `[auth.jwt]` configured exactly as above but with a
static `jwk_set_file`. Fixture: `viewer@test.io` is a **Viewer in org 2 only**
(explicitly removed from org 1); a datasource exists in each org. Assertions
minted with a throwaway RSA key; no Grafana admin credential involved in any
lettered case.

| # | Request as `viewer@test.io` | Result | Proves |
|---|---|---|---|
| A | `GET /api/user/orgs`, no org header | `200 [{"orgId":2,"name":"customer-a","role":"Viewer"}]` | self-scoped org+role lookup needs no admin — `LookupUser`/`UserOrgs` are replaceable |
| B | `GET /api/datasources`, `X-Grafana-Org-Id: 2` | `200` — org 2's datasource | non-admin org switching **and** OSS Viewer `datasources:read` |
| C | `GET /api/datasources`, `X-Grafana-Org-Id: 1` | `403 datasources:read` | no access to a non-member org |
| D | `GET /api/orgs` | `403 orgs:read` | no server-admin escalation |
| E | `GET /api/user` | `200` `orgId:2`, `isGrafanaAdmin:false` | identity resolved from the assertion |
| F | proxy `…/proxy/uid/<org2 ds>/api/health` | `200` + real upstream body | **the full read data path works for a Viewer** |
| G | proxy org 1's datasource UID with `X-Grafana-Org-Id: 2` | `404 Unable to find datasource` | datasource UIDs do not cross orgs |
| H | proxy org 1's datasource with `X-Grafana-Org-Id: 1` | `403 datasources:query` | data path denied in a non-member org |
| I | `GET /api/v1/provisioning/alert-rules`, org 2 | `200 []` | alerting rule reads survive the Viewer downgrade |
| J | `GET /api/user/orgs`, `X-Grafana-Org-Id: 1` | `200` — still only org 2 | self-scoped endpoint ignores a forged org header |
| K | `GET /api/search`, `X-Grafana-Org-Id: 1` | `200 []` | see the note below |
| L | same instance, `glsa_…` SA token | `200` | SA tokens coexist with `auth.jwt` |
| M | same instance, `Basic admin:admin` | `200` | basic auth coexists |
| N | `aud` as `["grafana"]` instead of `"grafana"` | `200` | `expect_claims` accepts both JSON shapes |
| O | assertion for a user who has never logged into Grafana | `401 user.sync.signup-disabled` | `auto_sign_up=false` holds; **distinct messageId** |
| P | assertion with `iss: https://evil.test` | `401 jwt.invalid` | `expect_claims` pins `iss` |
| Q | assertion signed by an unknown key | `401 jwt.invalid` | JWKS is the trust root |

**One correction to note (case K).** Authentication in a non-member org does not
fail outright — Grafana authenticates the caller with an effective role of None
and the denial happens per-endpoint at the RBAC layer (C, H), so endpoints that
require only "signed in" return `200` with an empty result rather than an error.
No data leaked in any case tested, but error handling must not assume a blanket
401 for the wrong-org case.
