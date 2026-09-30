# MCPHub administrator manual

[中文](admin-guide.zh-CN.md) · [Documentation](README.md) · [Project home](../README.md)

[Read the online administrator manual (Chinese)](https://samuelsupe.github.io/mcphub/admin/)

For administrators responsible for deployment, service connections, access policies, approval and operations. This manual covers v2.2.0. Installation and access on employee computers are covered by the [user manual](user-guide.md).

Recommended sequence: **deploy the gateway → connect a read-only service → publish tools explicitly → assign user permissions → enable client authorization → validate before enabling writes**.

- [Preparation and installation](#preparation-and-installation) · [Local management UI](#local-management-ui) · [Remote administration and databases](#remote-administrators-and-postgresql)
- [Login clients and credentials](#distinguish-login-clients-and-upstream-credentials)
- [Connect services and publish tools](#connect-services-and-publish-tools) · [Users and organizations](#users-and-organizations) · [Client authorization](#enable-client-authorization) · [Upstream accounts](#configure-upstream-accounts)
- [Write approval and governance](#write-approval-and-configuration-governance) · [Console navigation](#console-navigation) · [Tool permission checks](#tool-permission-checks) · [Grant and request diagnostics](#grant-and-request-diagnostics)
- [Upgrades and backups](#upgrades-and-backups) · [Operations](#operations) · [Security](#security-notes) · [Known limits and troubleshooting](#known-limits-and-troubleshooting)

## Preparation and installation

Prepare an identity provider, HTTPS URLs for MCP and administration, one persistent database and a separately retained configuration encryption key. Run **one active Hub instance**; PostgreSQL does not coordinate multiple Hub runtimes.

| Mode | Use case | Start here |
| --- | --- | --- |
| Local administration + SQLite | Development or maintenance on the gateway machine | [Minimal configuration below](#local-management-ui); unauthenticated and loopback-only |
| Remote administration + SQLite / PostgreSQL | Team access, approval and centralized management | [Deployment guide](../deploy/README.md); separate administrator login and HTTPS |
| YAML configuration | No console; explicitly published read tools only | [Full configuration example](configuration.md#starting-from-the-full-yaml-example) |

Install `mcphub` on the gateway host and `mcphub-cli` on user computers. Administrators using the management CLI also need the latter. Server downloads:

| Platform | Server download |
| --- | --- |
| macOS Intel | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub_v2.2.0_darwin_amd64.tar.gz) |
| macOS Apple Silicon | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub_v2.2.0_darwin_arm64.tar.gz) |
| Linux amd64 | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub_v2.2.0_linux_amd64.tar.gz) |
| Linux arm64 | [mcphub](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/mcphub_v2.2.0_linux_arm64.tar.gz) |

Download from the [v2.2.0 release](https://github.com/SamuelSupe/mcphub/releases/tag/v2.2.0), verify against [SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v2.2.0/SHA256SUMS), extract and put the executable on PATH. With Go 1.26 installed:

```bash
go install github.com/SamuelSupe/mcphub/v2/cmd/mcphub@v2.2.0
```

`validate --config PATH` checks configuration without creating a database. `serve --config PATH` starts the server and performs required migrations, writing structured JSON logs to stderr. Read [upgrades and backups](#upgrades-and-backups) before replacing an older version.

Before deployment, check [example selection and configuration ownership](configuration.md#choose-a-configuration-and-apply-changes), [environment/secret syntax](configuration.md#environment-variables-and-secrets) and the [deployment variable inventory](../deploy/README.md#environment-variables). Add optional modules only when needed.

## Local management UI

Enable the embedded UI to manage connections without editing backend YAML. For a new deployment, save the following as `config.yaml`, set `MCPHUB_PUBLIC_URL` and `MCPHUB_AUTH_ISSUER` to your real HTTPS gateway and identity-service URLs, and supply a Base64-encoded 32-byte `MCPHUB_CONFIG_KEY` from your secret store. Generate the key once (for example, with `openssl rand -base64 32`) and retain it securely across restarts and upgrades. Choose a writable database location.

```yaml
server:
  listen: "127.0.0.1:8080"
  public_url: ${MCPHUB_PUBLIC_URL}
auth:
  issuer: ${MCPHUB_AUTH_ISSUER}
admin:
  enabled: true
  listen: "127.0.0.1:8081"
  database_path: ./data/mcphub.db
  encryption_key_env: MCPHUB_CONFIG_KEY
backends: []
```

Run `mcphub validate --config config.yaml`, then `mcphub serve --config config.yaml`. Open [the local console](http://127.0.0.1:8081/) on the gateway host. The admin listener does not require login and must remain loopback-only. The gateway still needs a working OIDC issuer to become ready and authenticate MCP clients.

## Remote administrators and PostgreSQL

Remote management adds OIDC administrator login, browser sessions, `mcphub-cli admin` and configuration audit attribution. Admin tokens must include `admin.public_url` in their audience and all `admin.required_scopes` (default `mcphub:admin`). Ordinary MCP login does not grant management access.

```bash
mcphub-cli login --admin --server https://admin.example.com --client-id mcphub-admin-cli --profile ops
mcphub-cli admin --profile ops get /overview
mcphub-cli admin --profile ops get /backends
mcphub-cli admin --profile ops get /tool-groups
mcphub-cli admin --profile ops get /events
```

Choose SQLite (default; existing `database_path` remains compatible) or PostgreSQL (`database_driver: postgres` and `database_dsn_env`). This supports one gateway instance, without multi-instance runtime synchronization. See the [deployment guide](../deploy/README.md) for OIDC setup, browser login, API writes, databases and HTTPS proxies.

## Connect services and publish tools

1. Add a Streamable HTTP service under **MCP backends**, or organize REST API / OpenAPI tools under **HTTP tool groups**.
2. Configure the connection and upstream credentials; check probe or import results. An enabled HTTP group does not itself prove upstream connectivity.
3. Explicitly publish reviewed tools using original backend names in `published_tools`. Newly discovered tools stay unpublished. Manage HTTP publication through the tool/import editors.
4. In **Tool permissions**, set `read` / `write`, required scopes and business-resource limits. All required scopes must be present. Argument restrictions complement the backend's own authorization.
5. Validate with one authorized user and a read tool before configuring writes or broader access.

Upstream headers/OAuth authenticate Hub to the business service; client scopes authorize users at Hub. Configure them separately. Updates use revision checks: after `409 revision_conflict`, reread and reconcile instead of overwriting another administrator's changes. With configuration approval enabled, a successful submission may still await approval before application.

See [backend fields](configuration.md#backends), [HTTP tool groups](configuration.md#tool-groups-and-managed-http-api-tools), [publication and resources](configuration.md#explicit-publication-and-resource-limits), and [rate limits](configuration.md#endpoint-rate-limits).

## Users and organizations

Choose the identity mode before assigning access. With an external OIDC issuer, the provider issues scopes. With `auth.sso`, MCPHub manages local user and department/group policies. Enterprise application secrets stay on the server.

Enable users in **Users and organizations**, then assign endpoints, exact tools, scopes, resources and required roles. New SSO users start pending authorization. Department/group synchronization updates identity and membership, not local grants; timely offboarding requires reliable directory synchronization. Assign administrator and reviewer roles separately.

See [SSO and user management](sso-and-user-management.md) for configuration, directory snapshots and last-administrator recovery. Give employees the MCP URL, CLI client ID, available services, any required callback port and the [client setup instructions](user-guide.md#connect-an-mcp-client).

## Distinguish login clients and upstream credentials

Each `client_id` belongs to a different authentication flow; register and configure them separately. Replace these example domains. Hub user tokens target the full `server.public_url`; administrator tokens target `admin.public_url`. They are not interchangeable.

| Purpose | Configuration | Example callback registration |
| --- | --- | --- |
| Employee CLI login to Hub | CLI `--client-id` | `http://127.0.0.1:PORT/oauth/callback` |
| Ordinary-user portal | `client_authorization.client_id` | `https://hub.example.com/client-auth/auth/callback` |
| Administrator browser login | `admin.client_id` | `https://admin.example.com/auth/callback` |
| Hub verifies identity with enterprise SSO | `auth.sso.upstream.client_id` | `https://hub.example.com/sso/callback` |
| User connects a business account | `backends[].credentials.oauth.client_id` | `https://hub.example.com/client-auth/api/accounts/callback` |

`backends[].oauth` is Hub's service-account OAuth `client_credentials`, without a browser callback; `credentials.oauth` is personal user authorization. They cannot coexist. Hub `required_scopes` and upstream `oauth.scopes` are interpreted by their respective identity services and never automatically map or grant each other.

## Enable client authorization

Enable `client_authorization.enabled`, configure its portal `client_id`, and enable the managed database. SQLite and a **single MCPHub instance with PostgreSQL** use the same authorization lifecycle. Back up the database and encryption key before upgrading to v2.2.0 / schema 9 (v2.1.0 uses schema 8).

Register the portal callback `https://hub.example.com/client-auth/auth/callback`. The portal is served on the MCP gateway origin, separately from the administration listener. The CLI and portal must receive JWT access tokens for the full MCP resource URL with the same `issuer + sub`. Pairwise subjects from different OIDC clients require an identity-provider configuration that gives these clients a consistent subject; email matching is not used. An optional portal client secret stays on the server through `client_secret_env`.

```yaml
client_authorization:
  enabled: true
  client_id: mcphub-user-portal
  require_client_grant: false
  max_grant_ttl: 8h
```

Set `require_client_grant: true` on selected backends or HTTP tool groups, or globally to require it everywhere. Global and endpoint requirements are combined with OR. Existing connections without `--client` retain access only to compatible endpoints; authenticated initialization does not expose strict endpoints. Global portal settings are static process configuration and require a restart. Endpoint settings use the existing managed configuration governance.

Users confirm their own grants in the portal. Administrators can inspect and revoke grants but cannot consent as another user. An external issuer also needs [public OAuth client registration](configuration.md#external-oidc-registration-for-the-user-cli); with SSO bridging, register local public clients according to the SSO guide.

## Configure upstream accounts

Under **MCP backends → Upstream authentication → Vault**, choose a shared service account or one account per user. Shared mode uses a fixed administrator-provided Vault path. Personal mode uses server-assigned paths and requires the user portal and client grants.

Administrators configure Vault AppRole, path policies, discovery credentials, upstream OAuth callbacks and backups. Users only connect their own accounts in the portal. See [Vault configuration and operations](vault-accounts.md) and the user's [personal account steps](user-guide.md#connect-a-personal-account). Personal Vault accounts currently cover remote MCP endpoints, not HTTP tool groups.

## Write approval and configuration governance

Explicitly read-only, published tools execute after permission checks. Writes and unclassified tools need approval per operation. Unauthenticated local administration and YAML-only deployments can execute only explicitly read-only published tools; write approval requires remote administration.

| Role | Responsibility |
| --- | --- |
| Configuration administrator | Connect services, publish tools, assign policies and inspect diagnostics |
| Write reviewer | Review the operation, target, arguments and preview; perform independent review and step-up authentication when required |
| Security reviewer | With configuration approval enabled, review and apply proposals; cannot edit configuration directly |

Grant combined roles explicitly when needed; administrator access does not itself grant write-review permission. Production writes also need atomic version checks and business idempotency at the backend. Notification links do not replace browser approval, and approval does not bypass execution-time checks.

See [one-time write approval](configuration.md#one-time-write-approval), [governance and quorum](configuration.md#configuration-governance-quorum-and-operation-identity), and [notifications and independent archives](configuration.md#approval-notifications-and-independent-audit-archive). User and reviewer actions are described under [write approvals](user-guide.md#write-approvals).

## Console navigation

The console groups daily operations into four areas. Navigation shows only pages available to the current role; reviewers without configuration access land directly in the approval center.

| Area | Page | What to do |
| --- | --- | --- |
| Workspace | Overview | Inspect real backend connectivity, group/HTTP-tool totals and recent changes. Unavailable backends appear first, with direct access to configuration. |
| Connections | MCP backends | Connect Streamable HTTP MCP servers; search, filter, probe, enable/disable and configure publication, upstream credentials and rate limits. |
| Connections | HTTP tool groups | Turn REST APIs into tools, manually or through OpenAPI, with shared connections, credentials and access policies. |
| Access control | Tool permissions | Inspect publication, read/write classification, effective scopes, business resource rules and approvals; edit policies or run a side-effect-free access check. |
| Access control | Users & organization | Manage local SSO user, department and group status, roles, scopes, tools and resource permissions. Expand a record to edit it. |
| Access control | Client authorization | Filter grants by user, client, endpoint and status; inspect full scope, navigate to related requests or revoke a grant. |
| Governance & audit | Approval center | Review permitted write operations and configuration changes, including previews, approval progress, execution outcomes and audit history. |
| Governance & audit | Request diagnostics | Inspect recent outcomes, denial reasons and latency; expand request details and navigate directly to the relevant tool policy. |
| Governance & audit | Activity | Review the latest 50 management changes and their actors, without exposing credentials. |

Recommended workflow: **connect services → publish and classify tools → configure user/organization access → client sign-in and consent → approvals and diagnostics**. Users connect through `mcphub-cli setup` / `connect` and confirm their own grants in the personal authorization portal; administrators maintain policy in this console. Deployment settings such as the identity provider, administrator login, database and audit delivery remain in YAML/environment configuration. See [SSO and user management](sso-and-user-management.md).

Follow the same three steps in either editor: **connection → upstream credentials → client access**. Upstream headers/OAuth authorize MCPHub to call the service. Required scopes authorize clients to use the backend or group: an empty list permits all authenticated clients; a nonempty list requires **every** listed scope. Publish approved original tool names in **Published tools**; discovery never publishes new tools automatically. Tool rules add scopes and resource-argument restrictions for selected operations. Advanced connection settings stay collapsed until needed. When editing an existing secret, leave its value blank to retain it; removing its Header row removes that credential.

Several endpoints may share the same required scopes. Without the SSO bridge, the external issuer grants those scopes. With `auth.sso`, Users & organization manages local user/department/group permissions, while verified login claims or directory snapshots supply memberships. HTTP tool groups share settings within a REST API; they do not group multiple MCP backends. Publication, scopes, business resources, client grants and write approvals jointly constrain calls.

Use the language control to switch between Chinese and English. Narrow screens use a navigation drawer with keyboard and Escape support. Refresh reloads current configuration and shows the last successful update time; failures keep a visible error and retry action. Overview values come from actual configuration and connection state. Request history follows database retention; it is not full monitoring or a tamper-evident audit archive.

## Tool permission checks

Configuration administrators can open **Tool permissions** in the administration UI. Select a backend or HTTP tool group to see published and unpublished tools, effective read/write classification, required scopes, argument resource limits and approval requirements. Search and filters highlight unclassified or unpublished tools. Catalog inspection does not execute tools; HTTP group status indicates whether it is enabled, not upstream connectivity.

**Edit tool policy** edits an exact-name rule and, for MCP backends, publication in the same revision. Other matching rules continue to apply: an exact `read` rule cannot override a wildcard `write` or approval rule. The form supports scopes, JSON Pointer resource allowlists, reviewer subjects, quorum and step-up authentication; advanced approval fields are preserved. HTTP publication remains in the existing tool/group or OpenAPI import editor. All saves use the existing revision checks and configuration-approval workflow when enabled.

**Access check** explains publication, readiness, scope, resource, client Grant and approval gates without calling a tool, running previews, creating approvals or acquiring execution quotas. Supplied scopes are administrator assumptions. To inspect a saved Grant, provide its ID and exact user subject; effective scopes are intersected with that Grant. A successful check is not execution authorization: argument schema, live quotas, resource versions, previews and upstream ACLs are still enforced during execution. The admin-only APIs are `GET /api/v1/tool-policies?endpoint=<id>` and `POST /api/v1/access-check`:

```json
{"endpoint":"projects","tool":"get_project","scopes":["projects:read"],"arguments":{"project":"work"}}
```

## Grant and request diagnostics

The admin UI adds **Client authorizations** and **Request diagnostics**. Configuration administrators can filter grants by exact user subject, client ID, endpoint and status, page through all matching users, inspect granted capabilities/resources/scopes, prefill an access check, and revoke a grant. Granted scopes are an upper bound, not the user's currently verified token permissions. Revocation blocks subsequent admission and cancels active streams; it cannot roll back upstream side effects. The personal portal remains restricted to the signed-in user's grants.

Request diagnostics persist completed MCP POST requests in managed SQLite/PostgreSQL and survive restarts. `admin.request_retention` defaults to `720h` (30 days), accepts `24h`–`8760h`, and automatically prunes expired records. The default query window is 24 hours, with completion-time filters, pagination and NDJSON export. Records contain IDs, verified identities, known routing names, timings and fixed outcome/reason codes, excluding arguments, results, tokens and raw error bodies. Detailed records are encrypted; query indexes retain identity/routing metadata, so protect the database and exported files. Statistics cover the full filtered window, including P95 and average approval wait for resumed operations.

Recording happens after request processing. Storage failure never replays or changes a business operation: the server logs the failure and the UI reports recording gaps during this run. A crash before persistence, in-flight requests and GET streams remain outside this history. Use independent approval-audit archives where required. Deployments without managed storage retain the existing short-lived in-memory diagnostics only.

`GET /api/v1/requests` accepts `since` and `until` (RFC3339 completion times), returning the actual `window_start`/`window_end`. `format=ndjson` exports up to 10,000 records under the same administrator authorization and filters; a nonzero `X-MCPHub-Next-Cursor` response header can be passed as `cursor` to continue, or narrow the UI time range. The new history table upgrades v2.2.0 to **schema 9** (v2.1.0 uses schema 8). Back up the database and matching key before upgrading; rollback to an older binary requires restoring the pre-upgrade backup.

Admin-only APIs (on the **admin listener**, not the personal portal):

```bash
mcphub-cli admin --profile ops get '/client-grants?subject=alice&status=active&limit=25'
mcphub-cli admin --profile ops get '/requests?endpoint=database-prod&outcome=scope_denied&limit=25'
```

Both return `next_cursor`; pass it back as `cursor` with unchanged filters and, for request history, the returned time window. Grant filters also accept `client` and `endpoint`; request filters also accept `request_id`, `subject`, `client` and original `tool`. Normal pagination limits are 1–100. Revoke with `POST /api/v1/client-grants/{grant_id}/revoke` and `{"subject":"alice"}`. See the [v2.2.0 release and upgrade notes](../RELEASE_NOTES_v2.2.0.md) for the schema 9 migration.

## Upgrades and backups

Review the [complete backup, upgrade and rollback procedure](../RELEASE_NOTES_v2.2.0.md#upgrade-and-rollback--升级与回滚) before upgrading from v2.1.0 or earlier. In v2.2.0, `serve` migrates managed SQLite/PostgreSQL databases to schema 9 (v2.1.0 uses schema 8); `validate` is read-only. Keep the matching `MCPHUB_CONFIG_KEY`. Rolling back requires the old database, key/configuration and binary together.

Explicitly populate `published_tools` for each backend and classify allowed tools as read or write. Empty publication lists expose no tools; writes and unclassified tools require remote browser approval. Local unauthenticated or YAML-only deployments can execute only published, explicitly read-only tools. Existing HTTP tools keep their enabled state; new manual tools default to disabled. Client grants and SSO are opt-in; new SSO users await local authorization.

To enable management for the first time, configure the admin listener, encryption key and writable database. The first start imports YAML backends; after bootstrap, the database is the backend source. Tool groups and OpenAPI imports remain API-managed. Changing the database driver does not migrate data. PostgreSQL remains single-instance. Restart the gateway, refresh the browser, install the separate CLI and restart client connections. See the [deployment guide](../deploy/README.md).

## Operations

- Use `/healthz` for process liveness and `/readyz` for verifier/required-backend readiness. See [HTTP endpoints](configuration.md#http-endpoints-and-rfc-9728).
- Use SIGHUP for reloadable YAML fields. Listener, identity, administration, Vault/portal and other static settings require restart. Once initialized, managed storage supplies backend configuration; YAML backends no longer apply. See [reload and shutdown](configuration.md#sighup-reload-and-shutdown).
- Back up the database with its matching `MCPHUB_CONFIG_KEY`; coordinate Vault data backups when enabled. Recheck grants, accounts and configuration after recovery. Changing database drivers does not migrate data.
- Monitor recording gaps, failed approval deliveries and archive backlogs. Request history and recent configuration events do not replace independent approval-audit archives.

### Docker deployment

The Dockerfile builds a static binary with `golang:1.26-bookworm`, then copies it into `gcr.io/distroless/static-debian12:nonroot`. The final image has no shell and runs as the nonroot user.

```bash
docker build -t mcphub:local .
docker run --rm \
  --name mcphub \
  -p 127.0.0.1:8080:8080 \
  --env-file .env \
  -v "$PWD/config.yaml:/etc/mcphub/config.yaml:ro" \
  mcphub:local serve --config /etc/mcphub/config.yaml
```

The container listen address must match the published port (the example uses `:8080`). `--env-file` injects environment variables only; the configuration is mounted read-only. Restrict permissions on the host `config.yaml` and `.env`. The image entrypoint is already `/usr/local/bin/mcphub`, so pass `serve` or `validate` as arguments.

SQLite administration needs a writable database volume; both stores need `MCPHUB_CONFIG_KEY`. Local mode binds container loopback and is not reachable through ordinary port publishing. For remote container management use `mode: remote`, OIDC administrator authorization and an HTTPS proxy; see the [PostgreSQL Compose guide](../deploy/README.md).

## Security notes

- Use HTTPS for production `public_url`, `auth.issuer`, and remote backend URLs. `allow_insecure_http` permits only loopback backends; it cannot make a remote plaintext URL valid.
- `public_url` must appear in the token `aud`: a string `aud` equals `public_url`, while an audience array contains `public_url`; after TLS termination, a reverse proxy must preserve the public host and path and route both RFC 9728 metadata addresses.
- Use exact `allowed_origins` entries and do not add untrusted consoles. Origin and preflight headers are strictly allowlisted; preflight permits only `POST` (with `OPTIONS` as the response method).
- Put client secrets, API keys, and static Authorization values in environment variables or an external secret store, not in Git. Static headers are sent to backend data planes; do not put sensitive values in logs or capability names.
- Validation rejects line breaks, duplicate headers, and transport-managed headers, including `Proxy-Authorization` and `Proxy-Authenticate`. OAuth rejects a static `Authorization` header to prevent competing authentication sources.
- Overlong or invalid request IDs are regenerated, logged request/trace values are bounded or hashed, capability and resource-URI fields are validated and sanitized, and request failures record only external error types rather than raw external error text.
- `/healthz`, `/readyz`, and metadata do not require Bearer authentication; restrict their network visibility as appropriate. The MCP entry accepts Bearer JWTs in the Authorization header.
- Every MCP-listener HTTP route keeps the `request_timeout` request-body read deadline until the body is consumed or closed, so unauthenticated and rejected requests with slow bodies are bounded. A `subscriptions/listen` POST is exempt from ordinary response-write and request-context timeouts only after its body has been read.
- Backend SSE responses are streaming passthrough. Progress inspection buffers at most 1 MiB per event; an oversized event is forwarded unchanged without progress inspection.
- Acknowledged 2026 resource subscription IDs map updates back to their original subscription URI(s), including when an update event URI differs; timeout, cancellation, and session/reconnect cleanup remove the mapping.
- Local management has no login and permits only numeric loopback. Remote management requires a separate audience and admin scopes, HTTPS, exact Host/Origin checks, cookie CSRF protection and a restrictive CSP.
- Keep `MCPHUB_CONFIG_KEY` outside YAML and backups. The SQLite file uses `0600`, but its availability and recoverability depend on retaining the exact 32-byte key.

## Known limits and troubleshooting

- Administration persists backend/tool-group configuration and write approvals with their audit history. There is still no metrics endpoint, persistent MCP catalog, cross-instance subscription state, or high-availability coordination; each process owns its backend connections, catalogs, and token views.
- This release does not provide built-in accounts, stdio backends, a standalone legacy GET SSE endpoint, native TLS, dynamic tenants, opaque-token introspection, Tasks, MCP Apps, or custom MCP extensions. The local `connect` command provides stdio access to the HTTP gateway. TLS and external rate limiting belong at the reverse proxy.
- Personal credentials currently cover remote MCP endpoints; HTTP tool groups, dynamic cloud/database credentials and provider-specific SaaS OAuth adapters are outside this release.
- The aggregator advertises and implements only tools, prompts, resources (including subscriptions), and completions. Other backend capabilities do not automatically become gateway capabilities. Invalid names, URI templates, and SDK-rejected metadata are omitted.
- A disconnected backend keeps its last-known-good catalog, but calls require a live connection. A required backend makes `/readyz` return 503; an optional backend does not block overall readiness.
- `server.refresh_interval` is the maximum refresh period; a shorter backend TTL refreshes sooner. There is no forced-refresh API.
- SIGHUP does not reread the orchestration layer's Docker `--env-file`; placeholders use the current process environment. Restart for secret changes or fields that cannot be reloaded.

Common symptoms:

1. `validate` reports `environment variable ... is not set`: inject the selected example's variables into the actual service process; see the [base example](configuration.md#starting-from-the-full-yaml-example) or [remote deployment](../deploy/README.md#environment-variables). `required: false` does not skip backend variable validation.
2. `/readyz` returns 503: inspect `auth_verifier_ready` and `required_ready` in the JSON, then check OIDC discovery/JWKS and required backend URLs.
3. MCP returns 401 with a `resource_metadata` challenge: check the Bearer token, signature, issuer, and audience.
4. MCP returns 403 `insufficient_scope`: add every scope named in the challenge for that backend.
5. `/mcp` returns 404: ensure the request path exactly matches the path in `public_url`; `public_url` cannot be the domain root.

Common configuration mistakes:

| Symptom | Check and action |
| --- | --- |
| YAML edits do not change the service list | After managed-store initialization, edit through UI/API; do not delete the database. |
| SSO/Vault still uses `${...}` despite exports | These fields do not expand; use literal settings and `*_env` names for secrets. |
| Admin console exists but no user portal | Enable `client_authorization` separately and register ordinary-user clients. |
| Published tools remain hidden or cannot execute | Check original names, `effect`, token/grant scopes and user policies; validation does not check actual tool existence. |
| SQLite appears empty | Check the YAML location and resolved `database_path`; avoid opening a new path unintentionally. |
