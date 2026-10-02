# Remote administration and database deployment

[中文](README.zh-CN.md) · [Administrator manual](../docs/admin-guide.md) · [Documentation](../docs/README.md)

MCPHub v2.2.2 supports **one MCPHub instance with SQLite or PostgreSQL**. PostgreSQL provides a separately operated, durable configuration database; it does not enable multiple gateways to synchronize their in-memory runtimes.

This guide covers fresh v2.2.2 deployments. The default enables a local console and SQLite; team administration uses the remote templates here with an HTTPS admin origin and registered administrator identity. Start the console, add each backend with its own credentials, test the connection, then publish tools. See optional [Vault](../docs/vault-accounts.md) and [SSO](../docs/sso-and-user-management.md) configuration.

Completed MCP POST requests are retained for 30 days by default. Set `admin.request_retention` between `24h` and `8760h` to match operational needs. The administrator's request diagnostics page supports time filters and NDJSON export; see the [history contract and privacy boundaries](../docs/admin-guide.md#grant-and-request-diagnostics). Protect database backups and exports: arguments, results and tokens are excluded, but identity/routing indexes are readable database metadata.

| Deployment | Configuration |
| --- | --- |
| Local administration + SQLite | [config.local.yaml](config.local.yaml), [startup steps](../docs/admin-guide.md#local-management-ui) |
| HTTPS remote administration + SQLite | [config.remote-sqlite.yaml](config.remote-sqlite.yaml) |
| HTTPS remote administration + PostgreSQL | [config.remote-postgres.yaml](config.remote-postgres.yaml), [Compose](compose.postgres.yaml) |
| Feishu SSO + Vault + personal MCP accounts (integration example) | [config.feishu-vault.example.yaml](config.feishu-vault.example.yaml); requires real-tenant acceptance |

The v2.2.2 server archive includes these same templates; the links provide identical files. A binary-only installation can save the selected YAML as `config.yaml` and use `--config config.yaml`, without a source checkout. The `deploy/...` paths below assume the complete repository root as the working directory.

## Identity provider

For an external issuer, use the existing `auth.issuer`; Hub-managed identity bridging and local permissions are described in the [SSO guide](../docs/sso-and-user-management.md). Register a browser OAuth client with authorization code + PKCE S256 and the exact callback `https://admin.example.com/auth/callback`. A public client is supported by default; optionally set `admin.client_secret_env` for `client_secret_basic` or `client_secret_post`.

Register a separate public CLI client without a secret and the loopback callback `http://127.0.0.1:PORT/oauth/callback`. Use `--callback-port` when your provider requires a fixed registered port.

Grant selected administrators `mcphub:admin`. `admin.required_scopes` can change this requirement; all configured scopes must appear in the verified JWT `scope`/`scp`. No `roles`/`groups` mapping is performed. Issue signed JWT **access tokens** with valid `iss`, `sub`, `exp` and an `aud` containing the complete `admin.public_url`, such as `https://admin.example.com`. The ordinary MCP `/mcp` audience does not authorize management. Authorization, exchange and refresh requests carry this admin `resource`.

Enable refresh grants and advertise `offline_access` for renewal. Browser tokens stay in server memory; browsers receive only an opaque Secure/HttpOnly/SameSite cookie. Sessions expire after eight hours and are lost on restart. This single administrator permission grants management of every backend, tool group, tool and import. Tenant isolation, read-only admin roles, detailed management RBAC, host/process control and identity-provider account administration are outside this implementation.

## Environment variables

Choose one example; do not configure every integration at once. Remote SQLite/PostgreSQL examples enable the admin console only. Employee `mcpbridge setup` additionally needs [client authorization](../docs/admin-guide.md#enable-client-authorization) and registered portal/CLI clients. The table below applies to both `config.remote-*` templates and their Compose deployment; the Feishu example lists its own required variables in its [file header](config.feishu-vault.example.yaml).

| Used by | Variable | Value |
| --- | --- | --- |
| Both remote templates | `MCPHUB_PUBLIC_URL` | Full MCP HTTPS URL, e.g. `https://hub.example.com/mcp` |
| Both remote templates | `MCPHUB_AUTH_ISSUER` | External OIDC issuer; Hub SSO instead uses the Hub `/sso` URL as described in its guide |
| Both remote templates | `MCPHUB_ADMIN_PUBLIC_URL` | Admin HTTPS origin, e.g. `https://admin.example.com`, without a trailing `/` |
| Both remote templates | `MCPHUB_ADMIN_CLIENT_ID` | Registered admin browser client ID, separate from CLI/user portal IDs |
| Both remote templates | `MCPHUB_CONFIG_KEY` | Persistent Base64-encoded 32-byte key; retain across restarts |
| Direct PostgreSQL deployment | `MCPHUB_DATABASE_URL` | Reachable DSN; use TLS for external databases and URL-encode passwords |
| Compose's PostgreSQL service | `MCPHUB_POSTGRES_PASSWORD` | URL-safe password; Compose constructs the container's `MCPHUB_DATABASE_URL` |
| Host Caddy | `MCPHUB_HUB_HOST`, `MCPHUB_ADMIN_HOST` | Hostnames only, injected into the **Caddy process**; no URL scheme or `/mcp` |

MCPHub YAML `${NAME}`, Compose `${NAME:?message}` and Caddy `{$NAME}` are different syntaxes. MCPHub does not load `.env`; inject variables through the service manager/current shell for the standalone binary. Compose can select a file explicitly with `--env-file /secure/mcphub.env`, then the example's `environment` passes selected values into the container. Docker's `--env-file` directly injects container variables. Follow the consuming tool's value/quoting syntax, restrict file permissions and exclude secrets from version control. See [expansion rules and `*_env` names](../docs/configuration.md#environment-variables-and-secrets).

## Start a deployment

Set these values to your actual HTTPS addresses and inject the persistent encryption key from a secret store:

```bash
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_ADMIN_PUBLIC_URL=https://admin.example.com
export MCPHUB_ADMIN_CLIENT_ID=mcphub-admin-web
export MCPHUB_AUTH_ISSUER=https://idp.example.com
umask 077
mkdir -p "$HOME/.config/mcphub"
test -f "$HOME/.config/mcphub/config.key" || openssl rand -base64 32 > "$HOME/.config/mcphub/config.key"
export MCPHUB_CONFIG_KEY="$(cat "$HOME/.config/mcphub/config.key")"
```

This key file is a local example for a new deployment. Restarts, service managers and containers must use the same value; keep backups of both the database and key.

SQLite:

```bash
mcphub validate --config deploy/config.remote-sqlite.yaml
mcphub serve --config deploy/config.remote-sqlite.yaml
```

Relative database paths resolve from the YAML directory: using the repository template stores SQLite at `deploy/data/mcphub.db`; copying it elsewhere changes this location. Choose a writable location and retain the key on every restart.

External PostgreSQL: the example binds `:8080` / `:8081` for containers. Before running directly on a host, copy it to `config.yaml` and change both `listen` values to `127.0.0.1:8080` / `127.0.0.1:8081` or protected private addresses, then run:

```bash
export MCPHUB_DATABASE_URL='postgres://mcphub:URL_ENCODED_PASSWORD@db.example.com:5432/mcphub?sslmode=verify-full&sslrootcert=/path/to/db-ca.pem'
mcphub validate --config config.yaml
mcphub serve --config config.yaml
```

Use a dedicated database/schema. The database role needs table creation rights. A DBA must preinstall `CREATE EXTENSION IF NOT EXISTS citext` or allow the application to install it on startup. Supply the connection string, database CA and encryption key to the service. `validate` is read-only and does not create tables; the first `serve` initializes the schema and imports YAML backends. `validate` still connects to PostgreSQL; a missing database, invalid credentials or a network failure cannot pass.

### Use PostgreSQL Compose

Compose builds Hub from source. It requires Git, Docker with Compose v2, and a **complete source checkout** containing `Dockerfile`, `go.mod`, `go.sum`, `cmd/` and `internal/`. The `deploy/` directory in a binary release archive cannot build it by itself; it fails with `Dockerfile: no such file or directory`. Fetch the current source, or use an existing complete checkout:

```bash
git clone https://github.com/SamuelSupe/mcphub.git
cd mcphub
```

Set the five variables above and the database password in this directory, then run:

```bash
# Use a URL-safe password here, e.g. output from openssl rand -hex 24.
export MCPHUB_POSTGRES_PASSWORD=REPLACE_WITH_GENERATED_HEX_PASSWORD
docker compose -f deploy/compose.postgres.yaml config --quiet
docker compose -f deploy/compose.postgres.yaml up -d --build
```

Compose retains data in `postgres-data`, publishes no database port, and binds the gateway HTTP ports to host loopback only. Its database connection uses `sslmode=disable` inside the private container network; use `verify-full` for an external/managed database. Do not remove a required data volume with `down -v`.

If dependency downloads fail with `x509: certificate signed by unknown authority`, check the certificate chain of your enterprise HTTPS proxy. Host trust in a private CA does not automatically extend to build or runtime images. Ask operations to install the organization CA in the relevant image trust stores, then retry. The binary deployment above is another option; identity-service, Vault and database certificate trust still needs checking separately.

### Prerequisites for employee setup

The remote templates initially provide administration without enabling the employee setup wizard. Follow [client authorization](../docs/admin-guide.md#enable-client-authorization) to add its fragment to the complete configuration and restart. Register portal and employee CLI clients, then add a service, explicitly publish a read tool and grant its required scopes. The portal lives at `/client-auth/` on the MCP host; the HTTPS proxy must forward the whole MCP host, including portal and discovery paths. Give employees the `setup` command after completing these steps.

## HTTPS proxy

Terminate TLS at a trusted proxy. Forward **all paths** of the admin hostname to its listener, preserving `Host`; route the MCP hostname to the MCP listener. `admin.public_url` must exactly match the browser HTTPS origin, without a path or trailing slash. Identity headers supplied by proxies are not trusted.

The host [Caddyfile](Caddyfile) uses `MCPHUB_HUB_HOST=hub.example.com` and `MCPHUB_ADMIN_HOST=admin.example.com`. Configure DNS and certificates and run your existing Caddy, Nginx or ingress. Keep upstream HTTP listeners private; all external clients use HTTPS. Restrict probes, login rates and source networks at the proxy as appropriate.

## Verify the deployment

1. Run `mcphub validate --config ...` in the actual service environment. This checks configuration and applicable stored records, not backend connectivity or identity login; SSO/Vault also perform runtime credential checks.
2. After startup, check `/healthz` and `/readyz` on the MCP listener from a trusted network. Readiness includes the identity verifier and required backends; optional backend failures can coexist with ready status, so probe each service in the console.
3. Sign in through the HTTPS admin origin, add a service and publish a reviewed read-only tool. Complete ordinary-user login/client authorization and actually call that tool. Verify user and administrator permissions separately.
4. If enabled, verify real SSO login, personal account connection and revocation; test approvals before enabling write tools. `configuration valid`, health probes or a template alone do not establish integration acceptance.

## Browser and CLI

Open `https://admin.example.com` and sign in through the identity provider. Manage backends, HTTP tool groups and OpenAPI imports, then inspect audit events attributed to the administrator's subject. Sign out ends the current MCPHub browser session, not the identity provider's SSO session.

Use a separate CLI admin profile:

```bash
mcpbridge login --admin --server https://admin.example.com \
  --client-id mcphub-admin-cli --profile ops --callback-port 8400
mcpbridge admin --profile ops get /overview
mcpbridge admin --profile ops get /backends
mcpbridge admin --profile ops get /tool-groups
mcpbridge admin --profile ops get '/events?limit=50'
```

`admin` is a JSON management API client supporting `get/post/put/delete`. Paths are relative to `/api/v1`; all flags precede the method. Backend, tool and import operations use the [API paths in the configuration reference](../docs/configuration.md#tool-groups-and-managed-http-api-tools). Use `--file FILE` for JSON or `--file -` for stdin. JSON goes to stdout, ETags to stderr; tokens are never exported. The body limit is 6 MiB, matching OpenAPI uploads.

Save a disabled backend as `backend.json`:

```json
{"id":"example","url":"https://mcp.example.com/mcp","enabled":false,"required":false,"required_scopes":["mcp:example.read"],"headers":[]}
```

```bash
mcpbridge admin --profile ops --file backend.json post /backends
mcpbridge admin --profile ops get /backends/example
# Set enabled to true in backend.json; use the actual returned ETag.
mcpbridge admin --profile ops --file backend.json --if-match '"1"' put /backends/example
mcpbridge admin --profile ops post /backends/example/probe
mcpbridge status --profile ops
mcpbridge logout --profile ops
```

PUT takes the full input object. To preserve a Header secret, include its name and omit `value`; omit `client_secret` to retain an OAuth secret. Do not submit GET views containing runtime/revision/redaction fields directly as PUT bodies. Stale ETags return 409. Network errors do not replay writes; 401 allows at most one refresh/retry. Ordinary MCP clients use a separate user profile and the [user manual](../docs/user-guide.md) to generate their connection settings; administrator profiles cannot connect to MCP.

## Storage and authorization boundaries

Both databases commit configuration and audit events transactionally and encrypt Header/OAuth secrets with AES-256-GCM. SQLite files retain 0600 permissions. Audit actors are remote JWT subjects, `local` for local administration and `system` for background refreshes. This is configuration history, not a tamper-proof compliance or complete HTTP access log.

Back up the database and retain `MCPHUB_CONFIG_KEY` separately. Choose the database driver for a fresh deployment. Tool groups and OpenAPI imports are managed through UI/API. Run one active Hub instance. Remote MCP endpoints support [personal upstream accounts](../docs/vault-accounts.md). With an external issuer, upstream JWT revocation depends on that provider; Hub-managed SSO also checks the current local session and user policy. Signing out of Hub does not revoke upstream business accounts or a global identity-provider session.

MCP backends and HTTP tool groups expose **Rate limits** in the admin UI and a `rate_limit` object in the management API: `requests_per_second`, `burst`, `max_concurrent`. All default to zero (unlimited); users share the endpoint allowance. Policies persist in the selected database, while counters stay in the process. See [rate-limit semantics](../docs/configuration.md#endpoint-rate-limits).
