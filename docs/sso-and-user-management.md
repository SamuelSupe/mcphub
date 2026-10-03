# External SSO and MCPHub user permissions

[中文](sso-and-user-management.zh-CN.md) · [Administrator manual](admin-guide.md) · [Documentation](README.md)

This is an administrator configuration and operations guide. For employee access, use the [user manual](user-guide.md).

MCPHub can bridge OIDC or OAuth2 identity providers that support authorization code, PKCE S256 and Bearer UserInfo. `mcpbridge` remains a public client; the upstream confidential client secret stays on the server. The Feishu example remains an integration starting point. Compatibility requires validation of the actual application endpoints, identity fields, tenant settings and directory adapter.

The flow is browser → MCPHub `/sso` → enterprise identity provider → local user/group policy → MCPHub-issued access JWT → MCPHub tool policy and write approval → backend. Upstream tokens are neither MCP credentials nor backend credentials. Advanced deployments may select `auth.mode: external` to verify external JWTs; built-in mode automatically configures the local authorization service.

Start with [built-in accounts](builtin-accounts.md). Enterprise SSO is optional; this guide covers external identity integration. Local and enterprise identities remain separate and can reuse policies through independent permission groups.

Default built-in deployments can configure LDAP and OIDC directly in the [Identity services UI](enterprise-login.md), with immediate application. The YAML below is an advanced setup or initial OIDC connection; after the first UI save, database settings take precedence.

## Configuration

Add this to the existing server configuration. Terminate HTTPS at the trusted reverse proxy and keep internal listeners private. Forward `/sso/*`, `/.well-known/oauth-authorization-server/sso`, `/.well-known/openid-configuration/sso` and existing protected-resource metadata paths to the MCP listener.

Every field under `auth.sso` is literal and does not expand `${...}`; `client_secret_env` and `directory_token_env` contain variable names. Changes to any `auth.sso` setting require a restart. `bootstrap_subjects` matches the raw upstream subject (`sub` for OIDC or the configured OAuth2 subject claim). Approval-policy `subjects` instead uses the internal `sub` in Hub-issued JWTs; verify it through the management UI/API rather than copying an upstream open_id.

```yaml
auth:
  mode: builtin
  issuer: https://hub.example.com/sso
  sso:
    upstream:
      protocol: oidc
      issuer: https://identity.example.com
      client_id: mcphub-server
      client_secret_env: MCPHUB_SSO_CLIENT_SECRET
      token_auth_method: client_secret_post # or client_secret_basic
      scopes: [openid, profile]
      name_claim: name
      tenant_claim: tenant_id
      tenant_value: company-a
      groups_claim: groups
      departments_claim: departments
    clients:
      - id: mcpbridge
        redirect_uris: [http://127.0.0.1/oauth/callback]
        resources: [https://hub.example.com/mcp]
      - id: mcphub-admin
        redirect_uris: [https://admin.example.com/auth/callback]
        resources: [https://admin.example.com]
      - id: mcphub-portal
        redirect_uris: [https://hub.example.com/client-auth/auth/callback]
        resources: [https://hub.example.com/mcp]
```

Set `server.public_url` to the MCP resource above, enable managed admin storage, and use `admin.mode: remote`, `admin.public_url: https://admin.example.com`, `admin.client_id: mcphub-admin`, and `admin.required_scopes: [mcphub:admin]`. For the existing Broker/consent portal, set `client_authorization.enabled: true` and `client_authorization.client_id: mcphub-portal`. Downstream admin/portal clients must not have a `client_secret_env`. The full configuration fragment is also available in the [Chinese guide](sso-and-user-management.zh-CN.md).

Register a **confidential web application** at the identity provider with the fixed redirect `https://hub.example.com/sso/callback`. Register each downstream application's client ID, redirects and resources separately. The sample admin client supports browser login; register a separate loopback client with the admin resource if CLI admin login is needed.

OIDC discovery must advertise S256. ID tokens are checked for signature, issuer, audience, expiry and nonce. Groups/departments come from verified ID-token claims. Approval step-up forwards OIDC parameters and preserves only verified `acr`/`auth_time`; existing approval checks still require the configured assurance and fresh authentication. OAuth2-only upstreams cannot provide this evidence.

A loopback redirect registered without a port accepts an ephemeral port. A registered port must match exactly; other redirects are exact, with no wildcards, fragments or dynamic registration. Login transactions expire after five minutes; single-use codes expire after one minute. `auth.issuer` must be the MCP origin plus `/sso`. Changes to SSO configuration require a restart.

### OAuth2 UserInfo adapter

For providers without OIDC discovery, use explicit HTTPS endpoints and dot-separated response paths:

```yaml
upstream:
  protocol: oauth2
  issuer: https://identity.example.com
  authorization_url: https://identity.example.com/authorize
  token_url: https://identity.example.com/token
  userinfo_url: https://identity.example.com/userinfo
  client_id: enterprise-app-id
  client_secret_env: MCPHUB_SSO_CLIENT_SECRET
  token_auth_method: client_secret_post
  scopes: [profile]
  subject_claim: data.open_id
  name_claim: data.name
  tenant_claim: data.tenant_key
  tenant_value: expected-tenant
  success_claim: code
  success_value: '0'
```

Replace endpoints/scopes with those supplied by the provider; these are illustrative URLs, not a Feishu configuration. `success_claim` rejects failure codes inside HTTP 200 envelopes. Subjects must be stable nonempty strings. Never link users automatically by mutable names or unverified emails. Identity is isolated by provider connection (issuer, protocol, client ID, subject/tenant mapping) and external subject. Changing that connection invalidates old sessions and cannot inherit old user grants; a different app-scoped open_id creates a new local user. Configure a tenant claim/value for a single-enterprise deployment.

## Local permissions

Use **Users & groups** in the admin UI, or `GET /api/v1/identities` and `PUT /api/v1/identities/{id}` with `If-Match: "revision"`. New users await authorization. New groups/departments have no grants. Use the locally initialized built-in administrator to map enterprise groups to independent permission groups and enable their members. Users cannot receive direct roles, scopes or tool permissions. For independent external authentication deployments, optional `bootstrap_subjects` adds an enterprise user to a locally managed **Administrators** group only when that user is first created. It grants no business tools and cannot override a later disable.

The permission JSON below is for a **permission group ID**. `POST /api/v1/identities/groups` with `{"name":"Project readers","provider":"mcphub:permissions"}` creates an independent permission group; `provider` can be omitted. Update a user with `{"enabled":true,"groups":["<group MCPHub ID>"]}` and its own revision. Omit `groups` to preserve membership; an explicit empty array removes locally managed memberships only when no upstream memberships exist. Submitted lists must retain every upstream-owned group. Nonempty user `permissions` returns `400 group_permissions_required`. Local policy groups are marked `managed_locally: true`; upstream claims/directory snapshots cannot join them or replace their memberships. See [groups and permissions](builtin-accounts.md#groups-and-permissions).

```json
{"enabled":true,"permissions":{"roles":[],"scopes":["projects:read"],"access":[{"endpoint_id":"projects","tools":["read_project"],"allow_write_requests":false,"resource_rules":[{"argument":"/project","allowed_values":["demo"]}]}]}}
```

- Local and directory active states must both permit access. Directory reactivation cannot undo a local disable.
- Roles `admin`, `approver`, `security_reviewer` map to existing administrative scopes. Reviewer restrictions and no-self-approval still apply.
- Configure scopes, endpoint IDs, exact original tool names and business-resource predicates. Empty tool lists grant nothing. New tools receive no implicit user grant.
- Prompts, resource reads and subscriptions are separate permissions. Business predicates constrain tool arguments, not MCP resource URI ownership.
- The user inherits active direct permission groups and explicitly mapped organization groups. A tool, write-request flag and all resource conditions must match within one access entry. A grant without resource conditions allows all user-level resources for its listed tools.
- Tokens, client grants, shared endpoint/tool scopes, publication, enablement and shared resource rules still apply. Write-request permission does not bypass per-operation approval.

Changes use revision checks and audit events. Login/directory sync cannot modify local roles or grants. Removed scopes take effect on every verification; added scopes require a new login to expand the credential ceiling. Cached views cannot admit calls with obsolete permissions; affected running requests receive cancellation, which cannot undo completed writes. Some MCP clients need to reconnect after a denied request or closed stream. In SSO mode, access-check requires the local MCPHub user ID shown on the management page.

Group PUT accepts `source_groups: ["organization MCPHub group ID"]`; users, nested permission groups and locally managed groups cannot be mapped. Do not combine it with `groups`. Users can join locally managed groups from their own provider or independent permission groups. The console edits business permissions on independent groups; organization groups expose source and enablement. Effective-access responses include `groups` provenance, `verified_at` and `membership_stale`.

## Automatic department/group synchronization

Two mutually exclusive membership sources are supported:

1. **Login claims**: `groups_claim` / `departments_claim` reference arrays of strings. Each successful authentication replaces that user's upstream memberships; locally managed policy group memberships are preserved. Missing claims mean no memberships; old groups are not retained. Group and department IDs have separate namespaces.
2. **Directory snapshots**: configure `auth.sso.directory_token_env: MCPHUB_DIRECTORY_TOKEN` with a random secret of at least 32 characters. A directory adapter periodically fetches the enterprise directory and sends `PUT https://hub.example.com/sso/directory` with its dedicated Bearer credential. Directory membership takes precedence over login claims.

This endpoint is an MCPHub JSON contract, **not SCIM or a built-in Feishu directory adapter**. Existing sync jobs, iPaaS or an adapter can automate pushes; this change does not install a Feishu app or retrieve a real directory.

```json
{"version":1,"groups":[{"kind":"group","id":"engineering","name":"Engineering"},{"kind":"department","id":"platform","name":"Platform"}],"users":[{"subject":"upstream-stable-subject","name":"Test user","active":true,"groups":["engineering"],"departments":["platform"]}]}
```

Subjects must match the login mapping exactly. Pre-provisioned users remain pending. Perform the bootstrap administrator's first login before the initial directory push, or authorize that account from local management. Except for first-time bootstrap, unknown directory users cannot log in.

Snapshots are **complete replacements** for one provider, not deltas: omitted users/upstream groups become inactive. Locally managed policy groups and their memberships are preserved, but cannot keep an omitted or inactive user authorized. Fetch all upstream pages successfully and validate completeness before pushing; do not push an empty snapshot on an upstream failure. Versions are persisted, strictly increasing positive integers; stale/repeated versions return 409. Each update is atomic. Limits: 8 MiB, 10,000 users, 2,000 groups/departments, 256 memberships/user. Memberships are flat; adapters must explicitly include ancestor department IDs when inheritance is desired. Only identity attributes, active state and memberships are accepted; privilege fields are rejected.

Offboarding latency depends on sync cadence. Claim-only mode cannot proactively detect upstream suspension or global sign-out. Once synchronization revokes access, subsequent requests fail without waiting for JWT expiry.

## Client use and operational boundaries

Employee installation, login and Broker consent are covered by the [user manual](user-guide.md). Clients launch `mcpbridge` without upstream tokens or client secrets.

Access JWTs last ten minutes. `offline_access` enables rotating refresh tokens within an eight-hour local SSO session; another browser login is needed afterward. Upstream credentials are not retained and local refresh does not query upstream account state. Reuse of a consumed refresh token revokes its session family. The signing key is encrypted with the configuration key in the database; refresh credentials are stored only as hashes. Every access token also requires a live stored session and current local permissions.

Fresh SQLite and **single-instance PostgreSQL** storage uses schema **10**. Back up the database and matching encryption key. Pending authorization transactions/codes are in memory and restart with the process; stored sessions and signing keys survive. Apply existing reverse-proxy limits to login and directory endpoints. Global logout, SCIM, SAML, multiple connections of the same provider type and multi-instance consistency are outside this implementation.

Pending or disabled users receive an OAuth `access_denied` callback with the fixed reason `account_access_required`. The CLI stops waiting immediately and the portal directs users to their MCPHub administrator. Sign in again after access is granted; failed login preserves existing local credentials.

Remote SSO administration rejects disabling or demoting the last effective administrator with `409 last_administrator`, including removal of membership and disabling/demoting effective groups/departments. The check shares the update transaction and protects concurrent edits. Authorize a second active administrator first. Authoritative directory deactivation still takes effect; this safeguard never retains access for a deprovisioned user.

Retain at least one built-in local administrator independently of enterprise directory access. If its password or authenticator is lost, recover it locally using `mcphub init-admin --reset` (and `--reset-mfa` only when necessary), with the same configuration key and database; see [built-in account recovery](builtin-accounts.md). Sign in with that local account to repair enterprise permissions. Directory revocation must remain effective; correct the identity source and submit a complete directory snapshot when needed. Do not expose an anonymous maintenance console or bypass approval policies.
