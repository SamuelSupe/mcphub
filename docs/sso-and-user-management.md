# External SSO and MCPHub user permissions

MCPHub can bridge OIDC or OAuth2 identity providers that support authorization code, PKCE S256 and Bearer UserInfo. `mcphub-cli` remains a public client; the upstream confidential client secret stays on the server. No Feishu tenant/application is configured or connected by this change. Actual Feishu compatibility depends on its application endpoints, identity fields, tenant settings and a directory adapter.

The flow is browser → MCPHub `/sso` → enterprise identity provider → local user/group policy → MCPHub-issued access JWT → MCPHub tool policy and write approval → backend. Upstream tokens are neither MCP credentials nor backend credentials. Without `auth.sso`, existing external-JWT mode remains available.

## Configuration

Add this to the existing server configuration. Terminate HTTPS at the trusted reverse proxy and keep internal listeners private. Forward `/sso/*`, `/.well-known/oauth-authorization-server/sso`, `/.well-known/openid-configuration/sso` and existing protected-resource metadata paths to the MCP listener.

```yaml
auth:
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
    bootstrap_subjects: [initial-admin-subject]
    clients:
      - id: mcphub-cli
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

Use **Users & organization** in the admin UI, or `GET /api/v1/identities` and `PUT /api/v1/identities/{id}` with `If-Match: "revision"`. New users await authorization. New groups/departments have no grants. `bootstrap_subjects` grants the admin role only when a user is first created; remove the bootstrap list after setup. It does not grant business tools and cannot override a later local disable.

```json
{"enabled":true,"permissions":{"roles":[],"scopes":["projects:read"],"access":[{"endpoint_id":"projects","tools":["read_project"],"allow_write_requests":false,"resource_rules":[{"argument":"/project","allowed_values":["demo"]}]}]}}
```

- Local and directory active states must both permit access. Directory reactivation cannot undo a local disable.
- Roles `admin`, `approver`, `security_reviewer` map to existing administrative scopes. Reviewer restrictions and no-self-approval still apply.
- Configure scopes, endpoint IDs, exact original tool names and business-resource predicates. Empty tool lists grant nothing. New tools receive no implicit user grant.
- Prompts, resource reads and subscriptions are separate permissions. Business predicates constrain tool arguments, not MCP resource URI ownership.
- Direct and enabled organization grants form a union. A tool, write-request flag and all resource conditions must match within one access entry. A grant without resource conditions allows all user-level resources for its listed tools.
- Tokens, client grants, shared endpoint/tool scopes, publication, enablement and shared resource rules still apply. Write-request permission does not bypass per-operation approval.

Changes use revision checks and audit events. Login/directory sync cannot modify local roles or grants. Removed scopes take effect on every verification; added scopes require a new login to expand the credential ceiling. Cached views cannot admit calls with obsolete permissions; affected running requests receive cancellation, which cannot undo completed writes. Some MCP clients need to reconnect after a denied request or closed stream. In SSO mode, access-check requires the local MCPHub user ID shown on the management page.

## Automatic department/group synchronization

Two mutually exclusive membership sources are supported:

1. **Login claims**: `groups_claim` / `departments_claim` reference arrays of strings. Each successful authentication replaces that user's memberships. Missing claims mean no memberships; old groups are not retained. Group and department IDs have separate namespaces.
2. **Directory snapshots**: configure `auth.sso.directory_token_env: MCPHUB_DIRECTORY_TOKEN` with a random secret of at least 32 characters. A directory adapter periodically fetches the enterprise directory and sends `PUT https://hub.example.com/sso/directory` with its dedicated Bearer credential. Directory membership takes precedence over login claims.

This endpoint is an MCPHub JSON contract, **not SCIM or a built-in Feishu directory adapter**. Existing sync jobs, iPaaS or an adapter can automate pushes; this change does not install a Feishu app or retrieve a real directory.

```json
{"version":1,"groups":[{"kind":"group","id":"engineering","name":"Engineering"},{"kind":"department","id":"platform","name":"Platform"}],"users":[{"subject":"upstream-stable-subject","name":"Test user","active":true,"groups":["engineering"],"departments":["platform"]}]}
```

Subjects must match the login mapping exactly. Pre-provisioned users remain pending. Perform the bootstrap administrator's first login before the initial directory push, or authorize that account from local management. Except for first-time bootstrap, unknown directory users cannot log in.

Snapshots are **complete replacements** for one provider, not deltas: omitted users/groups become inactive. Fetch all upstream pages successfully and validate completeness before pushing; do not push an empty snapshot on an upstream failure. Versions are persisted, strictly increasing positive integers; stale/repeated versions return 409. Each update is atomic. Limits: 8 MiB, 10,000 users, 2,000 groups/departments, 256 memberships/user. Memberships are flat; adapters must explicitly include ancestor department IDs when inheritance is desired. Only identity attributes, active state and memberships are accepted; privilege fields are rejected.

Offboarding latency depends on sync cadence. Claim-only mode cannot proactively detect upstream suspension or global sign-out. Once synchronization revokes access, subsequent requests fail without waiting for JWT expiry.

## Client use and operational boundaries

```bash
mcphub-cli login --server https://hub.example.com/mcp --client-id mcphub-cli --profile work
mcphub-cli connect --profile work
mcphub-cli status --profile work
mcphub-cli logout --profile work
```

Existing `setup` and Broker/client-consent flows remain available. MCP client configuration still launches `mcphub-cli` without tokens or upstream secrets.

Access JWTs last ten minutes. `offline_access` enables rotating refresh tokens within an eight-hour local SSO session; another browser login is needed afterward. Upstream credentials are not retained and local refresh does not query upstream account state. Reuse of a consumed refresh token revokes its session family. The signing key is encrypted with the configuration key in the database; refresh credentials are stored only as hashes. Every access token also requires a live stored session and current local permissions.

SQLite and **single-instance PostgreSQL** use schema **7**. Back up the database and matching encryption key before migration; older binaries must not write the upgraded database. Pending authorization transactions/codes are in memory and restart with the process; stored sessions and signing keys survive. Apply existing reverse-proxy limits to login and directory endpoints. Global logout, SCIM, SAML, multiple identity-source selection and multi-instance consistency are outside this implementation.
