# Remote administration and database deployment

> This guide covers v2.4.0 fresh deployments. Use matching MCPHub and MCPBridge packages; no migration workflow is included.

[中文](README.zh-CN.md) · [Administrator manual](../docs/admin-guide.md) · [Documentation](../docs/README.md)

MCPHub v2.4.0 supports **one MCPHub instance with SQLite or PostgreSQL**. PostgreSQL provides a separately operated, durable configuration database; it does not enable multiple gateways to synchronize their in-memory runtimes.

This guide covers fresh v2.4.0 deployments. The default enables a local console and SQLite; team administration uses the remote templates here with an HTTPS admin origin and registered administrator identity. Start the console, add each backend with its own credentials, test the connection, then publish tools. See optional [Vault](../docs/vault-accounts.md) and [SSO](../docs/sso-and-user-management.md) configuration.

Completed MCP POST requests are retained for 30 days by default. Set `admin.request_retention` between `24h` and `8760h` to match operational needs. The administrator's request diagnostics page supports time filters and NDJSON export; see the [history contract and privacy boundaries](../docs/admin-guide.md#grant-and-request-diagnostics). Protect database backups and exports: arguments, results and tokens are excluded, but identity/routing indexes are readable database metadata.

| Deployment | Configuration |
| --- | --- |
| Local administration + SQLite | [config.local.yaml](config.local.yaml), [startup steps](../docs/admin-guide.md#local-management-ui) |
| HTTPS remote administration + SQLite | [config.remote-sqlite.yaml](config.remote-sqlite.yaml) |
| HTTPS remote administration + PostgreSQL | [config.remote-postgres.yaml](config.remote-postgres.yaml), [Compose](compose.postgres.yaml) |
| Feishu SSO + Vault + personal MCP accounts (integration example) | [config.feishu-vault.example.yaml](config.feishu-vault.example.yaml); requires real-tenant acceptance |

The v2.4.0 server archive includes these same templates; the links provide identical files. A binary-only installation can save the selected YAML as `config.yaml` and use `--config config.yaml`, without a source checkout. The `deploy/...` paths below assume the complete repository root as the working directory.

## Identity provider

MCPHub accounts are the default; no external OIDC deployment is required. After startup, load the same deployment environment on the server and run `mcphub init-admin --config config.yaml`. There is no default password or anonymous web bootstrap. Configure LDAP and OIDC together under **Identity services** in the console; see the [identity services guide](../docs/enterprise-login.md). Local and enterprise identities use separate group permissions. Advanced YAML setup remains in the [SSO guide](../docs/sso-and-user-management.md). See [built-in accounts](../docs/builtin-accounts.md) for passwords, MFA and local recovery.

## Environment variables

Prepare only the variables for your selected template. Built-in mode does not require `MCPHUB_AUTH_ISSUER` or an administrator identity-service secret.

| Template | Variable | Purpose |
| --- | --- | --- |
| Default, local and remote | `MCPHUB_PUBLIC_URL` | Complete HTTPS MCP URL; issuer is derived from its origin plus `/sso` |
| All managed templates | `MCPHUB_CONFIG_KEY` | Persistent Base64-encoded 32-byte key; retain the same value for restart and backup |
| Remote templates | `MCPHUB_ADMIN_PUBLIC_URL` | HTTPS administrator origin, without a path |
| PostgreSQL on host | `MCPHUB_DATABASE_URL` | Database DSN; configure production TLS and independent credentials |
| Compose PostgreSQL | `MCPHUB_POSTGRES_PASSWORD` | URL-safe database password used to construct the container DSN |

Enterprise identity, Vault and pure YAML variables apply only to those advanced options. Users and per-backend credentials are configured independently in the UI; no fixed global backend key is required.

## Start a deployment

Set these values to your actual HTTPS addresses and inject the persistent encryption key from a secret store:

```bash
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_ADMIN_PUBLIC_URL=https://admin.example.com
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

Set the variables for the selected template and the database password in this directory, then run:

```bash
# Use a URL-safe password here, e.g. output from openssl rand -hex 24.
export MCPHUB_POSTGRES_PASSWORD=REPLACE_WITH_GENERATED_HEX_PASSWORD
docker compose -f deploy/compose.postgres.yaml config --quiet
docker compose -f deploy/compose.postgres.yaml up -d --build
```

Compose retains data in `postgres-data`, publishes no database port, and binds the gateway HTTP ports to host loopback only. Its database connection uses `sslmode=disable` inside the private container network; use `verify-full` for an external/managed database. Do not remove a required data volume with `down -v`.

If dependency downloads fail with `x509: certificate signed by unknown authority`, check the certificate chain of your enterprise HTTPS proxy. Host trust in a private CA does not automatically extend to build or runtime images. Ask operations to install the organization CA in the relevant image trust stores, then retry. The binary deployment above is another option; identity-service, Vault and database certificate trust still needs checking separately.

### Prerequisites for employee setup

The remote templates already enable the personal portal and automatically register portal and MCPBridge clients. Initialize the administrator locally, create users and groups, add a service, publish a read tool, assign scopes and service permissions to groups, then add users as members. Follow [client authorization](../docs/admin-guide.md#enable-client-authorization) when requiring strict client grants. The portal lives at `/client-auth/` on the MCP host; the HTTPS proxy must forward the whole MCP host, including portal and discovery paths. Give employees the `setup` command after completing these steps.

In another terminal on the server, load the same environment and run `mcphub init-admin --config config.yaml` before sign-in. With Compose, run `docker compose -f deploy/compose.postgres.yaml exec mcphub mcphub init-admin --config /etc/mcphub/config.yaml`; the command reads the password interactively.

## HTTPS proxy

Terminate TLS at a trusted proxy. Forward **all paths** of the admin hostname to its listener, preserving `Host`; route the MCP hostname to the MCP listener. `admin.public_url` must exactly match the browser HTTPS origin, without a path or trailing slash. Identity headers supplied by proxies are not trusted.

The host [Caddyfile](Caddyfile) uses `MCPHUB_HUB_HOST=hub.example.com` and `MCPHUB_ADMIN_HOST=admin.example.com`. Configure DNS and certificates and run your existing Caddy, Nginx or ingress. Keep upstream HTTP listeners private; all external clients use HTTPS. Restrict probes, login rates and source networks at the proxy as appropriate.

## Verify the deployment

1. Run `mcphub validate --config ...` in the actual service environment. This checks configuration and applicable stored records, not backend connectivity or identity login; SSO/Vault also perform runtime credential checks.
2. After startup, check `/healthz` and `/readyz` on the MCP listener from a trusted network. Readiness includes the identity verifier and required backends; optional backend failures can coexist with ready status, so probe each service in the console.
3. Sign in through the HTTPS admin origin, add a service and publish a reviewed read-only tool. Complete ordinary-user login/client authorization and actually call that tool. Verify user and administrator permissions separately.
4. If enabled, verify real SSO login, personal account connection and revocation; test approvals before enabling write tools. `configuration valid`, health probes or a template alone do not establish integration acceptance.

## Browser and CLI

Open the administrator HTTPS URL and sign in with the locally initialized account. Create users and groups in **Users & groups**, assign roles and tool permissions to groups, then add users as members. Add each backend with its own address and credentials, test the connection, and publish tools.

The user connector is registered as `mcpbridge`; the remote administration CLI is registered as `mcpbridge-admin`:

```bash
mcpbridge login --admin --server https://admin.example.com --client-id mcpbridge-admin --profile ops
mcpbridge admin --profile ops get /overview
```

Management Bearer tokens require the administrator audience and scope; ordinary MCP tokens do not authorize management. Browser mutations also require the exact Origin and session CSRF token. Password resets, disabling and MFA enrollment revoke related sessions. Enterprise passwords are managed by the enterprise provider. Retain a local administrator for recovery.

## Storage and authorization boundaries

Both databases commit configuration and audit events transactionally and encrypt Header/OAuth secrets with AES-256-GCM. SQLite files retain 0600 permissions. Audit actors are authenticated user subjects, `local` for unauthenticated advanced local administration and `system` for background refreshes. This is configuration history, not a tamper-proof compliance or complete HTTP access log.

Back up the database and retain `MCPHUB_CONFIG_KEY` separately. Choose the database driver for a fresh deployment. Tool groups and OpenAPI imports are managed through UI/API. Run one active Hub instance. Remote MCP endpoints support [personal upstream accounts](../docs/vault-accounts.md). With an external issuer, upstream JWT revocation depends on that provider; Hub-managed SSO also checks the current local session and user policy. Signing out of Hub does not revoke upstream business accounts or a global identity-provider session.

MCP backends and HTTP tool groups expose **Rate limits** in the admin UI and a `rate_limit` object in the management API: `requests_per_second`, `burst`, `max_concurrent`. All default to zero (unlimited); users share the endpoint allowance. Policies persist in the selected database, while counters stay in the process. See [rate-limit semantics](../docs/configuration.md#endpoint-rate-limits).

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

Each pairing grants one service and tool capability; use existing setup for prompts, resource URIs or subscriptions. Current groups and the confirmed grant both restrict access, and new tools never expand old grants. Private credentials remain inside MCPBridge. Never copy tokens to an Agent. Failure preserves an existing working profile; choose a new profile for another user or server. Pure external issuers retain PKCE login and setup.
