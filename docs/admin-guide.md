# MCPHub administrator manual

> This guide covers v2.4.0 fresh deployments. Use matching MCPHub and MCPBridge packages; no migration workflow is included.

[中文](admin-guide.zh-CN.md) · [Documentation](README.md) · [Project home](../README.md)

[Read the online administrator manual](https://samuelsupe.github.io/mcphub/en/admin/)

For administrators responsible for deployment, service connections, access policies, approval and operations. This manual covers v2.4.0. Installation and access on employee computers are covered by the [user manual](user-guide.md).

Recommended sequence: **start the console → add backends → test connections → publish tools → assign group permissions → verify a real call**.

- [Preparation and installation](#preparation-and-installation) · [Local management UI](#local-management-ui) · [Remote administration and databases](#remote-administrators-and-postgresql)
- [Login clients and credentials](#distinguish-login-clients-and-upstream-credentials)
- [Connect services and publish tools](#connect-services-and-publish-tools) · [Users and groups](#users-and-groups) · [Client authorization](#enable-client-authorization) · [Upstream accounts](#configure-upstream-accounts)
- [Write approval and governance](#write-approval-and-configuration-governance) · [Console navigation](#console-navigation) · [Tool permission checks](#tool-permission-checks) · [Grant and request diagnostics](#grant-and-request-diagnostics)
- [Backups and recovery](#backups-and-recovery) · [Operations](#operations) · [Security](#security-notes) · [Known limits and troubleshooting](#known-limits-and-troubleshooting)

## Preparation and installation

Prepare an HTTPS MCP URL, writable persistent storage and a separately retained configuration encryption key. Remote administration additionally requires an HTTPS admin origin and a locally initialized administrator account. Run **one active Hub instance**; PostgreSQL does not coordinate multiple Hub runtimes.

| Mode | Use case | Start here |
| --- | --- | --- |
| Local administration + SQLite | Development or maintenance on the gateway machine | [Minimal configuration below](#local-management-ui); built-in account sign-in and loopback-only |
| Remote administration + SQLite / PostgreSQL | Team access, approval and centralized management | [Deployment guide](../deploy/README.md); separate administrator login and HTTPS |
| YAML configuration | No console; explicitly published read tools only | [Advanced YAML-only example](../deploy/config.yaml-only.example.yaml) |

Install `mcphub` on the gateway host and `mcpbridge` on user computers. Administrators using the management CLI also need the latter. Server downloads:

| Platform | Server download |
| --- | --- |
| macOS Intel | `mcphub_v2.4.0_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `mcphub_v2.4.0_darwin_arm64.tar.gz` |
| Linux amd64 | `mcphub_v2.4.0_linux_amd64.tar.gz` |
| Linux arm64 | `mcphub_v2.4.0_linux_arm64.tar.gz` |

Download the server archive and `SHA256SUMS` from [the v2.4.0 release](https://github.com/SamuelSupe/mcphub/releases/tag/v2.4.0). The following installs Linux arm64; change the filename for your platform and use `shasum -a 256` on macOS:

```bash
sha256sum mcphub_v2.4.0_linux_arm64.tar.gz
# Compare exactly with the same filename in SHA256SUMS before extracting.
mkdir -p mcphub-release "$HOME/.local/bin"
tar -xzf mcphub_v2.4.0_linux_arm64.tar.gz -C mcphub-release
install -m 755 mcphub-release/mcphub "$HOME/.local/bin/mcphub"
export PATH="$HOME/.local/bin:$PATH"
```

After installation, run `mcphub --version`; expect `mcphub 2.4.0 (server)`. `validate` and `serve` are server commands. The renamed client `mcpbridge` connects Agents and cannot start or validate the gateway. Updating source does not replace an existing binary in the directory; use the path of the executable you just installed.

This PATH setting applies to the current terminal. For later runs, use `"$HOME/.local/bin/mcphub"` directly or add the tools directory to your service environment. With Go 1.26.8 installed:

```bash
go install github.com/SamuelSupe/mcphub/v2/cmd/mcphub@v2.4.0
```

Go installs into `go env GOBIN`, or `$(go env GOPATH)/bin` when GOBIN is empty. Add that directory to PATH too.

**The v2.4.0 server archive includes the same templates as the online guide.** Its default `config.example.yaml` enables local administration and SQLite with `backends: []`; only the MCP URL and configuration encryption key are required. Start the console, add each service with its own credentials, test the connection, then publish reviewed tools and configure access. Use the remote templates for team administration, or the independent [advanced YAML-only example](../deploy/config.yaml-only.example.yaml) for deployments without a console.

`validate --config PATH` checks configuration without creating a SQLite database. `serve --config PATH` initializes fresh storage and starts the service, writing JSON logs to stderr.

Before deployment, check [example selection and configuration ownership](configuration.md#choose-a-configuration-and-apply-changes), [environment/secret syntax](configuration.md#environment-variables-and-secrets) and the [deployment variable inventory](../deploy/README.md#environment-variables). Add optional modules only when needed.

## Local management UI

The default template enables built-in accounts, a loopback console, SQLite, the personal portal and `backends: []`. The console listens on `127.0.0.1:8081` and requires account sign-in. Use the remote templates and HTTPS for remote access.

Copy the packaged template into a fresh deployment directory and set the public address and persistent key:

```bash
cp mcphub-release/config.example.yaml config.yaml
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
umask 077
mkdir -p secrets
test -f secrets/config.key || openssl rand -base64 32 > secrets/config.key
export MCPHUB_CONFIG_KEY="$(cat secrets/config.key)"
mcphub validate --config config.yaml
mcphub serve --config config.yaml
```

In another terminal on the server, enter the same directory, load both variables and initialize the administrator:

```bash
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_CONFIG_KEY="$(cat secrets/config.key)"
mcphub init-admin --config config.yaml --username admin
```

Password input is hidden and requires at least 12 characters. There is no default password or anonymous web bootstrap. Sign in to the [local console](http://127.0.0.1:8081/), create users and groups, assign group permissions and add members in **Users & groups**, then **add backends → test connections → publish tools**.

Only the public address and configuration key are required. Do not set `MCPHUB_AUTH_ISSUER` or fixed backend variables. The key is Base64-encoded 32 bytes; generate it once and retain the same private file. Inject both variables into your service manager: MCPHub does not load `.env`. SQLite uses `data/mcphub.db` next to the YAML file.

The built-in issuer is derived from the public origin with `/sso`. A fresh gateway with no backends can return `/readyz` 200 without an external OIDC provider. Readiness does not prove public HTTPS or business backend acceptance. MCPBridge and remote users need working HTTPS, OAuth metadata and portal routes. See [built-in accounts](builtin-accounts.md) for passwords, disabling, MFA and local recovery.

## Remote administrators and PostgreSQL

Remote management adds HTTPS administrator sign-in using built-in accounts or optional enterprise SSO, browser sessions, `mcpbridge admin` and configuration audit attribution. Admin tokens must include `admin.public_url` in their audience and all `admin.required_scopes` (default `mcphub:admin`). Ordinary MCP login does not grant management access.

```bash
mcpbridge login --admin --server https://admin.example.com --client-id mcpbridge-admin --profile ops
mcpbridge admin --profile ops get /overview
mcpbridge admin --profile ops get /backends
mcpbridge admin --profile ops get /tool-groups
mcpbridge admin --profile ops get /events
```

Choose SQLite (default) or PostgreSQL (`database_driver: postgres` and `database_dsn_env`). This supports one gateway instance, without multi-instance runtime synchronization. See the [deployment guide](../deploy/README.md) for account initialization, browser login, API writes, databases and HTTPS proxies.

## Connect services and publish tools

1. Add a Streamable HTTP service under **MCP backends**, or organize REST API / OpenAPI tools under **HTTP tool groups**.
2. Configure the connection and upstream credentials; check probe or import results. An enabled HTTP group does not itself prove upstream connectivity.
3. Explicitly publish reviewed tools using original backend names in `published_tools`. Newly discovered tools stay unpublished. Manage HTTP publication through the tool/import editors.
4. In **Tool permissions**, set `read` / `write`, required scopes and business-resource limits. All required scopes must be present. Argument restrictions complement the backend's own authorization.
5. Validate with one authorized user and a read tool before configuring writes or broader access.

Upstream headers/OAuth authenticate Hub to the business service; client scopes authorize users at Hub. Configure them separately. Updates use revision checks: after `409 revision_conflict`, reread and reconcile instead of overwriting another administrator's changes. With configuration approval enabled, a successful submission may still await approval before application.

See [backend fields](configuration.md#backends), [HTTP tool groups](configuration.md#tool-groups-and-managed-http-api-tools), [publication and resources](configuration.md#explicit-publication-and-resource-limits), and [rate limits](configuration.md#endpoint-rate-limits).

The overview’s **Verify your first integration** checks the selected service, tool and user: service availability → published read-only tool → group access → client authorization → actual execution. The final step reads successful `tools/call` requests for that user and tool in the last 24 hours; catalog reads and simulations do not count. **Check selected user access** lets you enter resource arguments and inspect current policies without executing a business tool. Have the user pair their Agent and complete a read-only call, then select **Check again**. Verify HTTP group connectivity with actual calls; past success does not prove current credentials are valid.

## Users and groups

Use [built-in accounts](builtin-accounts.md) by default. Create groups in **Users & groups**, assign administration, reviewer or security roles and specific services, tools, scopes and resource access to the groups, then add users as members. New local users can sign in, but cannot call tools before authorization.

Enterprise LDAP/OIDC is optional. Configure both in the console’s [Identity services page](enterprise-login.md); enterprise identities and local accounts remain isolated and never merge by name. Independent permission groups share access policy through explicit memberships or organization mappings. Enterprise users start pending. YAML `auth.sso.upstream` remains an advanced initial connection.

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

The default templates already enable the personal portal and automatically register `mcphub-portal` and MCPBridge clients. No separate identity service or manual registration is required. The custom registration requirements below apply to advanced external authentication; in built-in mode, configure explicit registrations only when changing client IDs, callbacks or resource URLs.

Enable `client_authorization.enabled`, configure its portal `client_id`, and enable the managed database. SQLite and a **single MCPHub instance with PostgreSQL** use the same authorization lifecycle. Fresh storage uses schema 10; retain the database and encryption key across restarts.

Register the portal callback `https://hub.example.com/client-auth/auth/callback`. The portal is served on the MCP gateway origin, separately from the administration listener. The CLI and portal must receive JWT access tokens for the full MCP resource URL with the same `issuer + sub`. Pairwise subjects from different OIDC clients require an identity-provider configuration that gives these clients a consistent subject; email matching is not used. An optional portal client secret stays on the server through `client_secret_env`.

This is a **fragment to add at the root of an existing complete YAML file**, not a replacement configuration. Set `client_id` to the registered portal client's ID.

```yaml
client_authorization:
  enabled: true
  client_id: mcphub-portal
  require_client_grant: false
  max_grant_ttl: 8h
```

Restart a directly run binary. For Compose, add the fragment to the mounted `deploy/config.remote-postgres.yaml`, then run `docker compose -f deploy/compose.postgres.yaml up -d --force-recreate mcphub` in the original deployment environment. Register a separate public employee CLI client with the [external OIDC requirements](configuration.md#external-oidc-registration-for-the-user-cli), including PKCE, callbacks and MCP audience. Publish one explicitly classified `read` tool and grant its required scopes before asking employees to run `setup`; empty `backends: []` or `published_tools: []` do not create selectable tools.

Set `require_client_grant: true` on selected backends or HTTP tool groups, or globally to require it everywhere. Global and endpoint requirements are combined with OR. Existing connections without `--client` retain access only to compatible endpoints; authenticated initialization does not expose strict endpoints. Global portal settings are static process configuration and require a restart. Endpoint settings use the existing managed configuration governance.

Users confirm their own grants in the portal. Administrators can inspect and revoke grants but cannot consent as another user. An external issuer also needs [public OAuth client registration](configuration.md#external-oidc-registration-for-the-user-cli); with SSO bridging, register local public clients according to the SSO guide.

## Configure upstream accounts

Under **MCP backends → Upstream authentication → Vault**, choose a shared service account or one account per user. Shared mode uses a fixed administrator-provided Vault path. Personal mode uses server-assigned paths and requires the user portal and client grants.

Administrators configure Vault AppRole, path policies, discovery credentials, upstream OAuth callbacks and backups. Users only connect their own accounts in the portal. See [Vault configuration and operations](vault-accounts.md) and the user's [personal account steps](user-guide.md#connect-a-personal-account). Personal Vault accounts currently cover remote MCP endpoints, not HTTP tool groups.

## Write approval and configuration governance

Explicitly read-only, published tools execute after permission checks. Writes and unclassified tools need approval per operation. Unauthenticated local administration and YAML-only deployments can execute only explicitly read-only published tools; write approval requires authenticated administration, including a signed-in built-in local console.

| Role | Responsibility |
| --- | --- |
| Configuration administrator | Connect services, publish tools, assign policies and inspect diagnostics |
| Write reviewer | Review the operation, target, arguments and preview; perform independent review and step-up authentication when required |
| Security reviewer | With configuration approval enabled, review and apply proposals; cannot edit configuration directly |

Grant combined roles explicitly when needed; administrator access does not itself grant write-review permission. Production writes also need atomic version checks and business idempotency at the backend. Notification links do not replace browser approval, and approval does not bypass execution-time checks.

See [one-time write approval](configuration.md#one-time-write-approval), [governance and quorum](configuration.md#configuration-governance-quorum-and-operation-identity), and [notifications and independent archives](configuration.md#approval-notifications-and-independent-audit-archive). User and reviewer actions are described under [write approvals](user-guide.md#write-approvals).

## Console navigation

**Services** unifies MCP and HTTP connections with connection/accounts, capabilities, access and runtime/diagnostics entry points. Existing protocol editors retain their behavior. HTTP uses shared headers/OAuth; personal Vault accounts remain limited to MCP. Services have stable endpoint UIDs. IDs cannot change; delete/recreate assigns a new UID.

**Save and review changes** and **Review import changes** create encrypted drafts; the configuration is not yet active. **Validate draft** checks the current revision and candidate runtime, then shows a redacted diff, credential-change marker and estimated affected groups, users, sessions and grants. Estimates do not mean every session will be revoked. Validation never calls tools. Application revalidates and cannot overwrite concurrent updates. Operations groups status into runtime/configuration, identity/audit and backup/recovery. The change list shows draft, validated, awaiting approval, applied, failed and discarded states. Expand deployment and backup commands when needed.

Rollback uses a historical before snapshot to create a new draft, validates the current revision and increments it. It does not roll back sessions, revocations or approval outcomes. Create records have no previous configuration; deletion remains direct. Up to 200 records are retained; completed records can be removed, preferably after a backup.

Existing creation/update APIs accept `X-MCPHub-Change-Mode: draft`, returning a `201` draft without applying. `GET /api/v1/configuration-changes` lists records. `POST /api/v1/configuration-changes/{id}/validate|apply|discard|rollback` requires draft `If-Match`; DELETE removes completed records only. Ordinary writes retain immediate-apply/security-approval contracts. `GET /api/v1/services` provides the unified inventory. `GET /api/v1/operations` returns versions, sources, enterprise verification, backup/drill results, request-record failures and external audit delivery.

YAML/environment owns deployment listeners, public URLs, storage and authentication defaults; the database owns managed configuration. YAML backends are imported once into an empty database. Runtime remains one instance. Deployment parameters and encryption keys remain operator-managed.

The console groups daily operations into four areas. Navigation shows only pages available to the current role; reviewers without configuration access land directly in the approval center.

| Area | Page | What to do |
| --- | --- | --- |
| Workspace | Overview | Inspect real backend connectivity, group/HTTP-tool totals and recent changes. Unavailable backends appear first, with direct access to configuration. |
| Connections | MCP backends | Connect Streamable HTTP MCP servers; search, filter, probe, enable/disable and configure publication, upstream credentials and rate limits. |
| Connections | HTTP tool groups | Turn REST APIs into tools, manually or through OpenAPI, with shared connections, credentials and access policies. |
| Access control | Tool permissions | Inspect publication, read/write classification, effective scopes, business resource rules and approvals; edit policies or run a side-effect-free access check. |
| Access control | Users & groups | Create users and groups, manage account state and membership, and assign roles, scopes, tools and resources to groups. Enterprise memberships from the identity source are read-only. |
| Access control | Identity services | Configure and test LDAP and OIDC together, replace encrypted credentials and enable/disable enterprise login; retain the local administrator. |
| Access control | Client authorization | Filter grants by user, client, endpoint and status; inspect full scope, navigate to related requests or revoke a grant. |
| Governance & audit | Approval center | Review permitted write operations and configuration changes, including previews, approval progress, execution outcomes and audit history. Request IDs, issuer and client binding IDs are expandable under Technical details; the applicant, target and complete request remain available for review. |
| Governance & audit | Request diagnostics | Inspect recent outcomes, denial reasons and latency; expand request details and navigate directly to the relevant tool policy. |
| Governance & audit | Activity | Review the latest 50 management changes and their actors, without exposing credentials. |

Recommended workflow: **connect services → publish and classify tools → configure group access and memberships → client sign-in and consent → approvals and diagnostics**. Users connect through `mcpbridge setup` / `connect` and confirm their own grants in the personal authorization portal; administrators maintain policy in this console. Default built-in deployments configure LDAP and OIDC in the [Identity services UI](enterprise-login.md). Listener, database and audit-delivery settings remain in YAML/environment configuration.

Both editors follow **connection → upstream credentials → client access**. New MCP backends default to **No authentication**; explicitly choose Header, OAuth or Vault when the service requires credentials. Upstream credentials authorize MCPHub to call the service. Empty required scopes add no scope restriction: built-in and enterprise users still need group access, and tools must be published. A nonempty list requires **every** listed scope.

After testing the MCP connection, select approved tools in **Published tools**. Existing backend editors load the current catalog; unavailable discovery does not remove saved names. Advanced input accepts original tool names, and discovery never publishes new tools automatically. New and required backends need another test after their connection URL, authentication or timeout changes; publication changes alone do not invalidate the test. Lists show published and discovered counts separately. Edit individual rules in **Tool permissions**, or use advanced JSON; unclassified tools still require approval. An HTTP tool group's **Test saved connection** checks persisted configuration; save connection edits first.

**Users & groups** filters users, permission groups and organization groups/departments separately. Creating a permission group opens its editor so you can continue configuring access and organization mappings. In the group editor, choose the service and select its published tools and resource conditions; derived mode saves required scopes as a snapshot, while advanced explicit mode requires complete scopes, then assign group memberships. Changing a service clears the tools in that access entry so identical names do not carry over to another service. Advanced manual tool-name input remains available. Leave an existing secret blank to retain it; removing its Header row removes that credential.

For OpenAPI, choose a URL or upload a specification, parse it, then select the interfaces to import. Changing the source clears its previous preview and selection; parse the new source before importing. Imported tools are limited to the selected interfaces.

Several endpoints may share the same required scopes. Without the SSO bridge, the external issuer grants those scopes. With `auth.sso`, Users & groups grants permissions only to groups/departments; users inherit their groups. Locally managed memberships are edited in the console; verified login claims or directory snapshots own enterprise memberships. HTTP tool groups share settings within a REST API; they do not group multiple MCP backends. Publication, scopes, business resources, client grants and write approvals jointly constrain calls.

Use the language control to switch between Chinese and English. Narrow screens use a navigation drawer with keyboard and Escape support. Refresh reloads current configuration and shows the last successful update time; failures keep a visible error and retry action. Overview values come from actual configuration and connection state. Request history follows database retention; it is not full monitoring or a tamper-evident audit archive.

Closing an editor, refreshing configuration, navigating or filtering Users & groups prompts before discarding unsaved changes. Cancel keeps the draft available. Language changes preserve open editor drafts; Users & groups prompts before rebuilding its forms. Browser close or reload also uses its native warning. Forms lock while saving so a response cannot overwrite later input. Changes are not saved automatically, and credential drafts are never written to browser storage.

## Tool permission checks

Configuration administrators can open **Tool permissions** in the administration UI. Select a backend or HTTP tool group to see published and unpublished tools, effective read/write classification, required scopes, argument resource limits and approval requirements. Search and filters highlight unclassified or unpublished tools. Catalog inspection does not execute tools; HTTP group status indicates whether it is enabled, not upstream connectivity.

**Edit tool policy** edits an exact-name rule and, for MCP backends, publication in the same revision. Other matching rules continue to apply: an exact `read` rule cannot override a wildcard `write` or approval rule. The form supports scopes, JSON Pointer resource allowlists, reviewer subjects, quorum and step-up authentication; advanced approval fields are preserved. HTTP publication remains in the existing tool/group or OpenAPI import editor. All saves use the existing revision checks and configuration-approval workflow when enabled.

**Access check** explains publication, readiness, scope, resource, client Grant and approval gates without calling a tool, running previews, creating approvals or acquiring execution quotas. Select a test user to load their current effective scopes, then select an active client grant for this service. Disabled users and expired enterprise verification are highlighted. Scopes remain editable for hypothetical checks; advanced fields accept exact subject and grant IDs. Effective scopes are intersected with the grant. A successful check is not execution authorization: argument schema, live quotas, resource versions, previews and upstream ACLs are still enforced during execution. The admin-only APIs are `GET /api/v1/tool-policies?endpoint=<id>` and `POST /api/v1/access-check`:

```json
{"endpoint":"projects","tool":"get_project","scopes":["projects:read"],"arguments":{"project":"work"}}
```

## Grant and request diagnostics

The admin UI adds **Client authorizations** and **Request diagnostics**. Configuration administrators can filter grants by exact user subject, client ID, endpoint and status, page through all matching users, inspect granted capabilities/resources/scopes, prefill an access check, and revoke a grant. Granted scopes are an upper bound, not the user's currently verified token permissions. Revocation blocks subsequent admission and cancels active streams; it cannot roll back upstream side effects. The personal portal remains restricted to the signed-in user's grants.

Request diagnostics persist completed MCP POST requests in managed SQLite/PostgreSQL and survive restarts. `admin.request_retention` defaults to `720h` (30 days), accepts `24h`–`8760h`, and automatically prunes expired records. The default query window is 24 hours, with completion-time filters, pagination and NDJSON export. Records contain IDs, verified identities, known routing names, timings and fixed outcome/reason codes, excluding arguments, results, tokens and raw error bodies. Detailed records are encrypted; query indexes retain identity/routing metadata, so protect the database and exported files. Statistics cover the full filtered window, including P95 and average approval wait for resumed operations.

Recording happens after request processing. Storage failure never replays or changes a business operation: the server logs the failure and the UI reports recording gaps during this run. A crash before persistence, in-flight requests and GET streams remain outside this history. Use independent approval-audit archives where required. Deployments without managed storage retain the existing short-lived in-memory diagnostics only.

`GET /api/v1/requests` accepts `since` and `until` (RFC3339 completion times), returning the actual `window_start`/`window_end`. `format=ndjson` exports up to 10,000 records under the same administrator authorization and filters; a nonzero `X-MCPHub-Next-Cursor` response header can be passed as `cursor` to continue, or narrow the UI time range. Request history is retained in fresh **schema 10** storage; back up the database and matching key.

Admin-only APIs (on the **admin listener**, not the personal portal):

```bash
mcpbridge admin --profile ops get '/client-grants?subject=alice&status=active&limit=25'
mcpbridge admin --profile ops get '/requests?endpoint=database-prod&outcome=scope_denied&limit=25'
```

Both return `next_cursor`; pass it back as `cursor` with unchanged filters and, for request history, the returned time window. Grant filters also accept `client` and `endpoint`; request filters also accept `request_id`, `subject`, `client` and original `tool`. Normal pagination limits are 1–100. Revoke with `POST /api/v1/client-grants/{grant_id}/revoke` and `{"subject":"alice"}`.

## Backups and recovery

Use built-in commands for SQLite or PostgreSQL snapshots taken in one read transaction. The output directory must not exist. `manifest.json` records format, schema, application version, database engine, deployment-file SHA256, data checksum and key ID. `database.jsonl` contains logical rows. Files use `0600`; directories use `0700`. Keep YAML, environment values and Vault data separately, with the encryption key outside the backup directory.

```sh
mcphub backup --config config.yaml --output ./backup-20261003
mcphub verify-backup --config config.yaml --backup ./backup-20261003
mcphub restore --config config.yaml --backup ./backup-20261003 --into ./recovery/config.db
```

`verify-backup` checks checksum and key, restores into a temporary SQLite database, reads configuration and identities, and records the drill. PostgreSQL requires `--into MCPHUB_DRILL_DSN` pointing to a dedicated empty database, with its DSN stored in that environment variable. Restore requires the same engine, current schema and matching key. The SQLite destination must not exist; PostgreSQL must be empty.

Restore revokes old login/refresh sessions, Broker/service grants, unfinished device authorizations and approvals, and rotates signing keys in the same transaction. Users must log in and consent again. The source database is unaffected. Start the recovered copy in isolation and verify login, catalogs, credentials and actual read calls before switching production. Operations shows the latest backup and database restore check. Database reads alone do not establish successful business recovery. This is disaster recovery for new deployments, without old-configuration or database migration instructions.

## Operations

Shutdown cancels background connections and waits for retired configuration runtimes to finish draining before closing shared storage. Requests still follow the configured drain timeout.

- Use `/healthz` for process liveness and `/readyz` for verifier/required-backend readiness. See [HTTP endpoints](configuration.md#http-endpoints-and-rfc-9728).
- Use SIGHUP for reloadable YAML fields. Listener, identity, administration, Vault/portal and other static settings require restart. Once initialized, managed storage supplies backend configuration; YAML backends no longer apply. See [reload and shutdown](configuration.md#sighup-reload-and-shutdown).
- Back up the database with its matching `MCPHUB_CONFIG_KEY`; coordinate Vault data backups when enabled. Recheck grants, accounts and configuration after recovery. Choose a database driver when creating the deployment.
- Monitor recording gaps, failed approval deliveries and archive backlogs. Request history and recent configuration events do not replace independent approval-audit archives.

### Docker deployment

The Dockerfile builds a static binary with `golang:1.26.8-bookworm`, then copies it into `gcr.io/distroless/static-debian12:nonroot`. The final image has no shell and runs as the nonroot user.

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

SQLite administration needs a writable database volume; both stores need `MCPHUB_CONFIG_KEY`. Local mode binds container loopback and is not reachable through ordinary port publishing. For remote container management use `mode: remote`, a built-in administrator account and an HTTPS proxy; see the [PostgreSQL Compose guide](../deploy/README.md).

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
- Built-in local management requires account sign-in and permits only numeric loopback. Remote management requires a separate audience and admin scopes, HTTPS, exact Host/Origin checks, cookie CSRF protection and a restrictive CSP.
- Keep `MCPHUB_CONFIG_KEY` outside YAML and backups. The SQLite file uses `0600`, but its availability and recoverability depend on retaining the exact 32-byte key.

## Known limits and troubleshooting

- Administration persists backend/tool-group configuration and write approvals with their audit history. There is still no metrics endpoint, persistent MCP catalog, cross-instance subscription state, or high-availability coordination; each process owns its backend connections, catalogs, and token views.
- This release does not provide stdio backends, a standalone legacy GET SSE endpoint, native TLS, dynamic tenants, opaque-token introspection, Tasks, MCP Apps, or custom MCP extensions. The local `connect` command provides stdio access to the HTTP gateway. TLS and external rate limiting belong at the reverse proxy.
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

## Agent link authorization

With the built-in issuer, user sign-in and client consent happen on one web page. Local, SSH and container Agents can use this flow without a browser callback to the Agent's machine. Run both commands under the same operating-system user and private `MCPHUB_HOME` directory:

```bash
mcpbridge pair start --server https://hub.example.com/mcp --profile work --name "Project Agent" --json
mcpbridge pair finish --request pr_example --wait --json
mcpbridge connect --profile work --client ci_example
```

Replace the request and client IDs with the actual results. Show the user `verification_uri_complete` and `user_code`. After comparing the code, the user signs in with local accounts, LDAP or OIDC, selects a service, tools, resource restrictions and duration, then approves. No tools are selected by default; write access is off. Without `--wait`, finish checks once. `pending_user` requires approval; `ready` means private credentials were saved and MCP connectivity was checked. The default requested maximum is 1 hour, capped by the gateway. Requests expire after 5 minutes and polling starts at 5 seconds. `--ttl` takes seconds (at least 60); narrow requests with `--endpoint` and repeated `--tool` / `--scope`.

For Agents without command execution, use these stdio arguments:

```json
["connect", "--server", "https://hub.example.com/mcp", "--profile", "work", "--name", "Project Agent", "--interactive-auth"]
```

The session initializes immediately with only `mcpbridge_auth_start` and `mcpbridge_auth_status`. Start reuses the same unexpired request; status may collect and save credentials and check connectivity. Respect the returned `interval`. Refresh tools after ready. If the Agent does not support `notifications/tools/list_changed`, reconnect using the returned `connect --profile … --client …` command. Failed business calls are never queued or retried automatically. Reauthorization after expiry or revocation is explicit.

The pairing page preserves your service, tools, duration and resource restrictions when switching languages. Only eligible write access is shown. If a code is invalid or expired, start a fresh request in your Agent and enter its new code on the same page. Invalid resource restrictions do not end the request; correct the form and submit again.

Each pairing grants one service and tool capability; use existing setup for prompts, resource URIs or subscriptions. Current groups and the confirmed grant both restrict access, and new tools never expand old grants. Private credentials remain inside MCPBridge. Never copy tokens to an Agent. Failure preserves an existing working profile; choose a new profile for another user or server. Pure external issuers retain PKCE login and setup.
