# Configuration and protocol reference

[中文](configuration.zh-CN.md) · [Documentation](README.md) · [Administrator manual](admin-guide.md)

Use this page to look up YAML fields, tool policies, management APIs and gateway protocol details. Start with the [administrator manual](admin-guide.md) for deployment or the [user manual](user-guide.md) for client access.

- [Configuration ownership](#choose-a-configuration-and-apply-changes), [environment and secrets](#environment-variables-and-secrets)
- [server](#server), [auth](#auth), [admin](#admin), [backends](#backends), [client_authorization](#client_authorization), [Vault](#vault)
- [HTTP tool groups](#tool-groups-and-managed-http-api-tools), [rate limits](#endpoint-rate-limits), [publication and resources](#explicit-publication-and-resource-limits)
- [Write approval](#one-time-write-approval), [governance and quorum](#configuration-governance-quorum-and-operation-identity), [notifications and audit](#approval-notifications-and-independent-audit-archive)
- [Other configuration guides](#other-configuration-guides), [starting from the full YAML example](#starting-from-the-full-yaml-example)
- [Capabilities](#capabilities-and-boundaries), [HTTP endpoints](#http-endpoints-and-rfc-9728), [names and URIs](#name-and-uri-mapping), [reload and shutdown](#sighup-reload-and-shutdown), [client credential boundaries](#client-credentials-and-connection-boundaries)

## Choose a configuration and apply changes

This reference targets v2.2.2. Configuration is one YAML document with strict field checking. **Do not paste management API JSON directly into YAML**: YAML `headers` is a map; API `headers` is an object array. A managed backend's API `enabled` field is not a YAML backend field. HTTP tool groups and OpenAPI imports are managed only through the UI/API.

| Scenario | Starting point | Prerequisites |
| --- | --- | --- |
| Default local console + SQLite | [Default template](../config.example.yaml), [startup steps](admin-guide.md#local-management-ui) | 3 variables: MCP URL, issuer and encryption key; add backends in the UI |
| Advanced YAML-only deployment | [Independent example](../deploy/config.yaml-only.example.yaml) | Fill each backend address and credential in YAML; explicitly publish reviewed read tools |
| Local administration, SQLite | [Complete configuration](../deploy/config.local.yaml), [startup steps](admin-guide.md#local-management-ui) | MCP URL, issuer, persistent encryption key |
| Remote team administration | [Deployment examples and variables](../deploy/README.md#environment-variables) | Admin URL, identity clients, SQLite or PostgreSQL, HTTPS proxy |
| SSO / Vault personal accounts | [SSO](sso-and-user-management.md), [Vault](vault-accounts.md) | Add the required modules to a managed deployment; Feishu also needs real-tenant acceptance |

| Configuration | Edit in | Takes effect through |
| --- | --- | --- |
| `server.listen`, `server.public_url`, all `auth`, `admin`, `client_authorization`, `vault` | YAML / service environment | Restart; SIGHUP rejects changes to these fields |
| Other `server` fields | YAML | SIGHUP; see [reload](#sighup-reload-and-shutdown) |
| `backends` with admin disabled | YAML | SIGHUP; at least one backend is required |
| `backends` with admin enabled | YAML for initial import, then UI/API | Imported once into an empty database; later YAML backends and their variables do not override stored records |
| HTTP tool groups, OpenAPI, user policies, client grant records | Admin UI / user portal / API | Save or complete required approval; never imported from YAML |

Strict YAML decoding still applies in managed mode: unknown fields in ignored backend entries remain errors. Do not delete the database to force a reimport; it also stores tool groups, grants and audit history.

## Environment variables and secrets

`${NAME}` expands **only in the fields below**, not in every string. Values come from the MCPHub process environment. Exporting variables in another shell cannot change a running service's environment; update its service environment and restart for rotation. MCPHub does not automatically load `.env`.

| Expansion location | Fields |
| --- | --- |
| `server`, `auth` | `server.listen`, `public_url`, `allowed_origins[]`; `auth.issuer` |
| `admin` | `listen`, `mode`, `public_url`, `client_id`, `client_secret_env`, `database_driver`, `database_dsn_env`, `database_path`, `encryption_key_env`, `required_scopes[]` |
| `admin.approvals` | `required_scopes[]`, `step_up_acr_values[]`; `policy_changes.required_scopes[]`, `policy_changes.subjects[]`; `notifications.url`, `notifications.secret`; `audit_archive.url`, `audit_archive.signing_key`, `audit_archive.key_id` |
| `client_authorization` | `client_id`, `client_secret_env` |
| YAML `backends[]` | `id`, `url`, `headers` values, `required_scopes[]`, `published_tools[]`; all `oauth` strings and `scopes[]` |
| YAML `backends[].tool_rules[]` | `match`, `required_scopes[]`; `resource_rules[].argument`, `allowed_values[]` |

`auth.sso.*`, `vault.*`, `backends[].credentials.*`, `tool_rules[].approval.*` and `effect` **do not expand**; supply literal settings. Durations, numbers and booleans do not accept placeholders. Names must match `[A-Za-z_][A-Za-z0-9_]*`; unset variables in supported fields fail validation. `${NAME:-default}` and `$NAME` are not fallback/expansion syntax and may remain literal; do not use them.

A `*_env` field contains an **environment variable name**, not the secret:

```yaml
# Fragment: place each setting in its respective section.
admin:
  encryption_key_env: MCPHUB_CONFIG_KEY # Read the Base64-encoded 32-byte key from this variable.
# Backend credentials are configured separately for each service in the UI.
```

Do not write `encryption_key_env: ${MCPHUB_CONFIG_KEY}`: that treats the key value as a variable name. Generate the database encryption key once and retain it with backups. SSO and Vault likewise reference secrets through their `*_env` fields; inject values through the service manager or secret store.

`required: false` only tolerates connection failure. That backend's URL, OAuth fields and supported environment variables must still be valid and complete; it does not disable the configuration. Remove an unused backend entry from the active YAML.

## `server`

| Field | Default | Description |
| --- | --- | --- |
| `listen` | `:8080` | HTTP listen address. It cannot be changed by SIGHUP; restart to change it. |
| `public_url` | none | Required absolute HTTPS URL with an MCP path (for example, `https://hub.example.com/mcp`), with no query or fragment. The path may not contain percent-encoded characters and may not be `/healthz`, `/readyz`, or `/.well-known/oauth-protected-resource`. It is both the MCP URL and the JWT audience. It cannot be changed by SIGHUP. |
| `page_size` | `1000` | Aggregated MCP catalog page size; must be positive. |
| `request_timeout` | `60s` | Default timeout for ordinary requests and backend calls; must be positive. Long-lived subscriptions use a separate lifecycle after body reading; see below. |
| `drain_timeout` | `15s` | Maximum wait for active requests during SIGTERM/SIGINT shutdown and while replacing the old runtime after SIGHUP; must be positive. |
| `refresh_interval` | `5m` | Maximum backend catalog refresh interval. A shorter backend TTL causes an earlier refresh, with an effective interval no shorter than 5 seconds. Must be positive. |
| `catalog_ttl` | `30s` | Private TTL advertised for MCP catalog/discovery results; may be zero but not negative. |
| `max_request_body_bytes` | `4194304` (4 MiB) | MCP POST body limit; larger requests return 413. Must be positive. |
| `allowed_origins` | `[]` | Additional browser HTTPS Origins. Each entry must be `https://authority` only, with no path, query, fragment, or wildcard. The origin of MCPHub's own `public_url` is allowed automatically. |

Durations use Go `time.ParseDuration` syntax, such as `500ms`, `60s`, and `5m`. The path of `public_url` is the MCP entry path; the example uses `/mcp`. Percent-encoded path characters are rejected, and `/healthz`, `/readyz`, and `/.well-known/oauth-protected-resource` are reserved paths.

<details>
<summary>Request deadlines and long-lived subscriptions</summary>

Default timeout for ordinary MCP requests and backend calls; must be positive. Every MCP-listener HTTP route keeps a request-body read deadline from this value until the body is consumed or closed, including unauthenticated and rejected requests. Once MCP handling proceeds, ordinary MCP POSTs also apply it to response-write deadlines and the request context; the newer `subscriptions/listen` POST keeps long-lived connection semantics after its body is read and is not given those ordinary response-write or context timeouts. Runtime or client context cancellation still expires its underlying write deadline, so a slow subscription write is interrupted when its generation drains or the client disconnects.

</details>

## `auth`

| Field | Description |
| --- | --- |
| `issuer` | Required absolute HTTPS OIDC issuer. MCPHub performs discovery (normally `/.well-known/openid-configuration`) and reads JWKS from it. It cannot be changed by SIGHUP. |
| `sso` | Optional identity bridge; `issuer` must be the Hub `/sso` URL and managed storage is required. Fields are literal and require a restart; see [SSO](sso-and-user-management.md). |

JWT requirements are: the issuer and signature must validate against this issuer; `aud` must contain the complete `server.public_url` including its path—when `aud` is a string it must equal `public_url`, and when `aud` is an array it must include `public_url`; `sub` must be non-empty; and `exp` must be present. `nbf`, when present, is checked too. Expiry, activation, and OIDC time comparisons allow 30 seconds of clock skew. The verifier becomes ready only after OIDC discovery succeeds, `jwks_uri` is an absolute HTTPS URL, and a reachable JWKS response contains at least one parseable, valid, asymmetric public verification key; symmetric `oct` keys and invalid or empty keys do not satisfy this condition. Before that first successful refresh, the MCP endpoint returns 503; after it is ready, a temporary discovery or JWKS refresh failure retains the last-known-good verifier. OIDC discovery and JWKS responses are each capped at 1 MiB.

For JWKS readiness, a usable key has no `use` or `use: sig`; if `key_ops` is present it includes `verify`; and an explicit `alg` matches a supported RSA, EC, or Ed25519 JWS algorithm declared by OIDC discovery. If discovery omits `id_token_signing_alg_values_supported`, `RS256` is assumed. Unsupported or malformed keys in the same JWKS do not hide another usable key.

Scopes are read from both JWT `scope` and `scp` claims. `scope` accepts only a space-delimited string (including an empty string or JSON `null`); `scp` accepts a space-delimited string or a string array. The two claims are merged and deduplicated; array entries may not contain whitespace. Backend access uses **all-of** semantics: `required_scopes: [a, b]` requires the token to contain both `a` and `b`. If either is missing, that backend is absent from the token's catalog view and an identified direct call returns 403 `insufficient_scope`. A backend with no `required_scopes` is not scope-gated. Protected Resource Metadata reports the deduplicated union of all backend required scopes in `scopes_supported`.

## `client_authorization`

The user portal and client grants require `admin.enabled: true` and managed database storage. Every field in this section requires a restart. See the [administrator workflow](admin-guide.md#enable-client-authorization) for registration and activation.

| Field | Default | Description |
| --- | --- | --- |
| `enabled` | `false` | Enables the user portal and client grants. Enabling admin alone does not enable this feature. |
| `client_id` | none | Required when enabled; the ordinary-user portal's OAuth client, separate from CLI/admin clients. |
| `client_secret_env` | none | Optional confidential-client secret variable name. With Hub SSO the portal is a public client and this must be unset. |
| `require_client_grant` | `false` | Global enforcement, ORed with the endpoint setting. Either being true requires this module to be enabled. |
| `max_grant_ttl` | `8h` | Maximum user grant lifetime, from `1m` to `8h`. |

## `vault`

Optional; requires managed database storage. `address` is required; `mount` defaults to `secret`, `prefix` to `mcphub`, and `auth_mount` to `approle`. Choose either `role_id_env` + `secret_id_env` or `token_env`, never both. All fields are literal and do not expand `${...}`; every global Vault setting requires a restart.

Put credential references in `backends[].credentials`. Personal mode also requires the user portal and **that backend's own** `require_client_grant: true`; global enforcement alone does not satisfy this configuration check. See the [Vault guide](vault-accounts.md) for fields, paths, policies and callbacks.

## `admin`

The administration platform is opt-in. It serves an embedded UI and JSON API from a separate listener. Local mode is loopback-only; remote mode requires OIDC administrator authentication. It manages backend and tool-group configuration; process settings remain in YAML with the restart/reload rules in the [ownership table](#choose-a-configuration-and-apply-changes).

| Field | Default | Description |
| --- | --- | --- |
| `enabled` | `false` | Enables management with the selected database as configuration source of truth. |
| `mode` | `local` | `local` has no login and is loopback-only; `remote` enables OIDC administrator authentication. |
| `listen` | `127.0.0.1:8081` | Numeric loopback in local mode; remote mode may bind a private address behind an HTTPS proxy. |
| `public_url` | none | Required HTTPS admin origin in remote mode, with no path or trailing slash; also the admin JWT audience. |
| `client_id` | none | Browser OAuth client ID in remote mode. |
| `client_secret_env` | none | Optional confidential-client secret environment variable; defaults to a public client. |
| `required_scopes` | `[mcphub:admin]` | All required admin scopes; cannot be empty in remote mode. |
| `database_driver` | `sqlite` | `sqlite` or `postgres`. |
| `database_dsn_env` | `MCPHUB_DATABASE_URL` | PostgreSQL DSN environment variable. Do not also set `database_path` for PostgreSQL. |
| `database_path` | none | Required for SQLite. Relative paths resolve from the YAML directory. |
| `encryption_key_env` | `MCPHUB_CONFIG_KEY` | Environment variable containing a Base64-encoded 32-byte AES key. Losing or changing this key makes stored secrets unreadable. |
| `request_retention` | `720h` (30 days) | Retention for completed MCP POST request history, from `24h` to `8760h`; expired records are pruned automatically. See [administrator diagnostics and export](admin-guide.md#grant-and-request-diagnostics). |

On the first start with an empty database, expanded YAML backends are imported in one transaction. The selected database becomes the sole backend source after the bootstrap marker is written; later YAML backend edits have no effect. Header values and OAuth client secrets are encrypted with AES-256-GCM and are never returned by the management API.

The UI is available at `http://127.0.0.1:8081/` by default. It can register, probe, edit, enable, disable, and delete backends without restarting the process. Required backend failures reject a change without replacing the current runtime; an unavailable optional backend is saved and continues reconnecting in the background.

The JSON API is rooted at `/api/v1`. Individual backend responses include an `ETag`; update and delete requests must send that revision in `If-Match`, and stale writes fail with `409 revision_conflict`. Secret fields are returned only as configured markers. Omitting a secret value during an edit preserves it, while omitting the Header or OAuth configuration removes it. Audit actors are the remote JWT subject, `local` for local management or `system` for background refreshes; outcomes are redacted.

### Tool groups and managed HTTP API tools

Tool groups are managed objects in the administration API, not YAML configuration. A group owns the shared HTTPS base URL, static headers or OAuth 2.0 `client_credentials`, JWT required scopes, and request timeout for its tools. Header values and OAuth client secrets are encrypted in the selected database; the API exposes only configured/not-configured markers. Group scope checks retain the same all-of semantics as backend scopes; optional group-local tool rules can add scopes to selected tools.

A group may contain hand-authored HTTP tools and multiple OpenAPI 3.0 or 3.1 imports. OpenAPI imports can be inspected before they are saved. Both kinds are MCP capabilities: they are listed and invoked only through the configured `/mcp` Streamable HTTP endpoint. MCPHub does not expose a raw HTTP proxy or an arbitrary method/path passthrough route.

The stable management paths are:

| Operation | Path |
| --- | --- |
| List/create groups | `GET/POST /api/v1/tool-groups` |
| Read/update/delete a group; probe it | `GET/PUT/DELETE /api/v1/tool-groups/{groupID}`, `POST .../{groupID}/probe` |
| List/create or read/update/delete manual tools | `GET/POST .../{groupID}/tools`, `GET/PUT/DELETE .../{groupID}/tools/{toolName}` |
| Inspect, list/create, or read/update/delete OpenAPI imports | `POST .../{groupID}/imports/inspect`, `GET/POST .../{groupID}/imports`, `GET/PUT/DELETE .../{groupID}/imports/{importID}` |
| Refresh an OpenAPI import | `POST .../{groupID}/imports/{importID}/refresh` |

Group, manual-tool, and import resources return an `ETag`. Updates and deletes require the matching `If-Match`; a stale revision returns `409 revision_conflict`. These resources are persisted and changed only through the admin API and the selected database; there is intentionally no `tool_groups` (or equivalent) YAML schema and SIGHUP does not import one.

Group base URLs and OpenAPI source URLs must use HTTPS; group HTTP requests and source fetches do not follow redirects. A source fetched from another origin never receives the group's static headers or OAuth secret. OpenAPI documents are capped at 5 MiB, requests carrying a document at 6 MiB, and HTTP-tool responses at 1 MiB by default; the response limit is configurable from 64 KiB through 16 MiB. A URL-backed import refreshes automatically every 15 minutes by default (allowed range 1 minute to 24 hours); a failed refresh keeps the last-known-good document/tools and retries with backoff.

## `backends`

At least one backend is required in YAML-only mode. Admin mode may start empty so the first backend can be registered in the UI. Each `id` must match `[A-Za-z0-9_-]{1,32}` and be unique case-insensitively; uppercase letters are allowed.

| Field | Default | Description |
| --- | --- | --- |
| `id` | none | External namespace and configured ID for tool/prompt names; dots are not allowed. Uppercase letters are retained in those names, while resource/template URI authorities use the lowercase ID. |
| `url` | none | Required absolute URL. HTTPS is required by default; HTTP is accepted only when `allow_insecure_http: true` and the host is `localhost` or an IPv4/IPv6 loopback. Fragments are rejected. |
| `required` | `false` | Required backends affect `/readyz`. A runtime disconnect makes readiness 503 while the reconnect loop continues. |
| `require_client_grant` | `false` | Enforces client grants for this endpoint; requires the user portal. Personal Vault accounts must explicitly set it to true. |
| `credentials` | none | Vault `shared` / `personal` source; requires global `vault`, and excludes service `oauth` and conflicting static auth headers on this backend. See [Vault](vault-accounts.md). |
| `required_scopes` | `[]` | JWT scopes required for this backend, checked with all-of semantics; scope entries cannot contain whitespace or duplicates. |
| `published_tools` | `[]` | Exact, case-sensitive original tool names approved for use; no wildcards. Empty publishes no tools. |
| `tool_rules` | `[]` | Optional backend-local tool policies. Each rule has a `match` glob and at least one of `effect`, `approval`, `required_scopes`, or `resource_rules`. `effect: read` permits direct execution; `write` or an omitted classification requires approval. Matching uses Go `path.Match` against the original backend tool name, full-string and case-sensitive. |
| `request_timeout` | inherits `server.request_timeout` | Timeout for this backend's connection, discovery, refresh, and calls; must be positive. |
| `rate_limit` | `{}` | Shared endpoint rate, burst and concurrency limits; unlimited by default. |
| `allow_insecure_http` | `false` | Loopback-only local HTTP switch. It does not relax HTTPS requirements for `server.public_url` or any issuer. |
| `headers` | `{}` | Static headers added to every backend MCP HTTP request. Values support environment expansion, may not contain CR/LF, and names are case-insensitively unique. `Accept`, `Content-Type`, any `Mcp-*` header, and transport-managed headers such as `Host`, `Content-Length`, `Connection`, `Proxy-Authorization`, and `Proxy-Authenticate` are rejected. |
| `oauth` | none | Backend OAuth configuration. The only accepted `type` is `client_credentials`, and it cannot be combined with a static `Authorization` header. |

`oauth` fields:

| Field | Description |
| --- | --- |
| `type` | Must be `client_credentials`. |
| `issuer` | Required absolute HTTPS OAuth issuer; its metadata issuer must match exactly. |
| `client_id` / `client_secret` | Required; preferably supplied only through `${...}` environment variables. |
| `scopes` | Scopes requested from the backend OAuth token endpoint. These are independent of `required_scopes`, which gate the JWT presented to MCPHub. |

Backend OAuth discovery and token requests do not receive the backend's static headers; data-plane requests do and automatically reuse/refresh the client-credentials token. Discovery probes RFC 8414/OIDC metadata for an exact `issuer` and `token_endpoint` only; it does not require interactive authorization or PKCE metadata. OAuth metadata responses are capped at 1 MiB. Backend and OIDC HTTP clients do not follow redirects.

Optional service-account OAuth fragment, excluded from the base template: add this entry under `backends` only when connecting such a service and set all 4 variables for it. `required: false` only changes connection-failure handling. Tools still require review and explicit publication.

```yaml
- id: crm
  url: ${MCPHUB_CRM_BACKEND_URL}
  required: false
  required_scopes: [mcp:crm.read]
  published_tools: []
  oauth:
    type: client_credentials
    issuer: ${MCPHUB_CRM_OAUTH_ISSUER}
    client_id: ${MCPHUB_CRM_CLIENT_ID}
    client_secret: ${MCPHUB_CRM_CLIENT_SECRET}
    scopes: [crm.read]
```

### Endpoint rate limits

MCP backends and HTTP tool groups accept `rate_limit`; the default is unlimited. Each configured ID has its own allowance shared by all users. Manual and OpenAPI tools in an HTTP group share that group's allowance. Edit **Rate limits** in the admin UI, or include the object in backend/tool-group management API input.

```yaml
# Add to a backends entry; configure HTTP groups through the admin UI/API.
rate_limit:
  requests_per_second: 20
  burst: 40
  max_concurrent: 8
```

- `requests_per_second`: average admission rate, including fractions; `0` or omission means unlimited.
- `burst`: token bucket capacity. With a positive rate, `0` or omission uses capacity `1`. A positive burst requires a positive rate.
- `max_concurrent`: active request cap; `0` or omission means unlimited. Can be used independently of a rate limit.

Limits apply after authentication and scope checks to `tools/call`, `prompts/get`, `resources/read`, `resources/subscribe`, `completion/complete`, and resource-bearing `subscriptions/listen`. Streaming responses hold a concurrency slot until completion or cancellation. A subscription spanning endpoints atomically checks all their budgets and counts once per endpoint. Initialization, catalog reads, health checks, cancellation, unsubscribe, admin probes and background refresh do not consume this allowance.

Rejected requests never reach the backend: HTTP `429`, `Retry-After` seconds and a JSON-RPC error preserve the original request ID. Retry timing for a concurrency cap is only a hint. `mcpbridge connect` reports the limit and retry hint without replaying calls. Configuration saves, SIGHUP and HTTP tool edits preserve unchanged budgets and active counts; restarting the process resets in-memory counters. Policies persist in SQLite/PostgreSQL; counters do not use the database and are single-instance only. Global ingress, IP and per-user quotas require separate policies; limit unauthenticated traffic at the reverse proxy.

### Explicit publication and resource limits

Tools start unpublished. Select every approved original tool name in the backend editor/API; scope rules do not publish tools. An empty or omitted publication list hides tools and denies direct calls. Catalog refresh does not publish new tools. Manual HTTP tools start disabled; enable reviewed tools explicitly and select approved OpenAPI operations during import.

```yaml
published_tools: [search]
required_scopes: [mcp:read]
tool_rules:
  - match: search
    effect: read
    required_scopes: [projects:read]
    resource_rules:
      - argument: /project
        allowed_values: [work, sandbox]
      - argument: /body/database
        allowed_values: [reports]
```

Every matching resource rule must pass, alongside all required scopes and the current publication/enable state. `argument` is an object-key JSON Pointer into the original `tools/call.arguments`, including the HTTP tool's `body` object where applicable (`~1` escapes `/`, `~0` escapes `~`). The value must be an allowed string or a non-empty array containing only allowed strings. Missing/null/non-string values fail closed. Rules for the same argument intersect; allowed values within one rule are alternatives. With no resource rules, arguments have no additional resource restriction.

Allowed values compare exactly and case-sensitively: no glob, prefix, directory traversal, URL decoding, filesystem or SQL interpretation. Configure the actual resource selector used by the tool. This is a shared per-tool allowlist, not an ownership check against user claims. Backends must enforce authorization for embedded queries, aliases, symlinks and secondary resource selectors. Resource rejection returns an MCP tool error (`isError: true`) without forwarding the call or closing the client connection. Constrained arguments are normalized before forwarding to remove duplicate-key ambiguity while preserving JSON number precision.

Publishing, unpublishing and rules use the existing revisioned admin API and SQLite/PostgreSQL storage; YAML mode supports SIGHUP. A cached tool listing is not authorization: forwarding rechecks the current definition, scopes and resource arguments. Old HTTP handlers are rejected after a manager replacement. Already admitted requests may finish; revocation does not roll back an upstream operation. Probe results include the discovered original tool names without approving them.

### One-time write approval

Both MCP backends and HTTP groups support `tool_rules[].effect: read` / `write`. Explicit reads execute after publication, scope and resource checks. Writes and unclassified tools require per-operation approval. A matching `approval` policy also makes a tool a write; a broader read rule cannot override it. HTTP methods and upstream `readOnlyHint` never grant permission.

Enable [remote administration](admin-guide.md#remote-administrators-and-postgresql). Grant separate scopes to configuration administrators and reviewers. MCP caller tokens retain the MCP audience; reviewer browser sessions use the management audience without needing configuration access:

```yaml
admin:
  # Keep the other remote, public_url, client_id and database settings.
  required_scopes: [mcphub:admin]
  approvals:
    required_scopes: [mcphub:approve]
    pending_ttl: 30m
    execution_ttl: 5m
    retention: 720h
    # Replace with an ACR that your identity provider enforces as Passkey/MFA.
    step_up_acr_values: ["urn:your-idp:mfa"]
```

The review deadline starts at creation (default 30 minutes, range 1 minute–24 hours); the execution deadline starts at approval (default 5 minutes, range 1–30 minutes). Retention defaults to 30 days and permits 1–365 days. Static administration settings require restart. Configuration administrators cannot approve by default; reviewers cannot read or change backend configuration. Explicitly grant both scope sets to accounts needing both roles. The sign-in page offers a separate reviewer login.

Configure policies in YAML or the existing **Tool Rules (JSON)** editor:

```yaml
published_tools: [get_project, preview_update, update_project]
tool_rules:
  - match: get_project
    effect: read
  - match: preview_update
    effect: read
  - match: update_project
    effect: write
    required_scopes: [projects:write]
    resource_rules:
      - argument: /project
        allowed_values: [work]
    approval:
      action: Change project quota
      environment: production
      resource_arguments: [/project]
      require_different_reviewer: true
      require_step_up: true
      approvers:
        - subjects: [reviewer-oidc-sub]
          resources:
            - argument: /project
              allowed_values: [work]
      preview_tool: preview_update
      version_argument: /expected_version
```

Reviewer subjects are exact, verified subjects from the same issuer, never caller-supplied usernames. All resource constraints within a grant must pass; grants are alternatives. Every matching tool policy must allow the reviewer. Selectors can restrict projects, databases, directories or environments. An omitted `approvers` list permits any account with the reviewer scope. `require_different_reviewer` prohibits self-approval. Lists and details enforce the same resource boundaries; requesters can view their own requests without acquiring approval authority.

1. A write returns `structuredContent.code: approval_pending`, `approval_id` and `approval_url`. No write has run; a configured read-only preview runs first.
2. Open the link and sign in as a reviewer. Inspect the action, environment, resource values, complete request and available before/after preview. Enter a reason and approve once or reject.
3. For `require_step_up: true`, select **Verify identity** first. MCPHub requests OIDC `max_age=0`, `prompt=login`, a nonce and configured ACR values. It verifies signature, issuer, client audience, the same subject, nonce, `auth_time`, returned ACR, and `at_hash` when present, allowing at most 30 seconds of clock skew. Verification is tied to this request and browser session, lasts two minutes, and is single-use. The user must still click Approve. Missing OIDC or insufficient authentication strength fails closed. The provider defines and enforces the MFA/Passkey meaning of an ACR; there is no universal MFA string.
4. The original caller invokes `mcphub_resume_approval` with only `{"approval_id":"..."}`. Current scopes, resources, publication and rate limits are checked before forwarding the immutable saved request. Identical active requests are deduplicated; use the dedicated status tool to poll. Existing `mcpbridge connect` configuration needs no changes.
5. Before execution, the requester can call `mcphub_cancel_approval` with `approval_id` and optional `reason`, or cancel from a reviewer browser session. An authorized reviewer can revoke an approved request. Cancellation/revocation race atomically with execution; an admitted write may finish and cannot be rolled back by revocation.

**Preview contract:** `preview_tool` names an explicitly published read-only original tool in the same backend/group. It receives the same arguments as the write. Its `structuredContent` (the JSON object response body for HTTP tools) must contain `version`, `before` and `after`, for example `{"version":"v7","before":{"limit":10},"after":{"limit":20}}`. `version_argument` selects a specific nonempty version string in the arguments; `*` is rejected. MCPHub checks preview permissions/resources and compares its content, version and tool generation at creation and resume. Any change or failure prevents execution. **The upstream write must also enforce the version atomically**, using HTTP `If-Match` or a database conditional update; a preview cannot close the read/write race. Existing HTTP Header parameter mappings can send `expected_version` as `If-Match`. Without a configured preview, the UI shows administrator-defined action/resource details and the complete request without inventing a change result.

The UI filters by status, exact tool name and exact requester, with cursor pagination (25 records by default). Decisions, cancellation, execution, expiry and restart recovery are audited. An unknown outcome can receive an investigation note and outcome (applied/not applied/uncertain); this never changes execution status or restores execution allowance. APIs: `GET /api/v1/approvals?status=&tool=&subject=&limit=25&cursor=`, `GET /api/v1/approvals/{id}`, and `POST /api/v1/approvals/{id}`. Mutations take `decision` (`approved/rejected/revoked/cancelled/investigated`), a required `reason` of at most 2048 bytes, and investigation `outcome` (`applied/not_applied/uncertain`). `POST /api/v1/approvals/{id}/verify` starts step-up verification. These endpoints require a scoped reviewer browser session; mutations also require CSRF/Origin validation.

SQLite/PostgreSQL atomically consume each approval once; concurrent resumes cannot execute twice, and repeats return the saved result. Cancellation, broken connections and process interruption may leave unknown outcomes that need backend investigation. This is not an upstream exactly-once or rollback guarantee. Changed tool/configuration generations invalidate approval; full runtime reloads/restarts invalidate outstanding requests, and restart marks interrupted execution unknown. Arguments, business metadata and continuation inputs are immutable; only the progress token is rebound. Large integers retain precision. Backend interactions requiring another call require new approval.

Limits: 20 active requests per issuer/subject; 60 KiB execution request; 32 KiB preview; 64 KiB complete intent including policies; 16 MiB saved result. Requests, previews, results, reasons and investigation details are encrypted. A minute-based maintenance loop removes terminal records and detailed history past retention in bounded batches; general activity logs retain argument/reason-free state events. Already admitted writes may finish after policy changes. Approved writes use fresh HTTP/1 connections to prevent transparent retries, so upstreams must support HTTP/1.1. Reads retain connection pooling.

Fresh managed deployments use **schema 9**. Back up the database and matching encryption key. Local unauthenticated management and YAML-only deployments cannot execute writes/unclassified tools. MCP and management API bearer tokens cannot approve. Isolate reviewer browsers, configuration/database access and upstream write credentials from agents. With configuration governance disabled, configuration administrators can change classifications directly; enable independent security review to guard those changes. Strong authentication does not replace reviewing the operation or downstream least privilege.


### Configuration governance, quorum and operation identity

Enable independent review of configuration changes in remote mode:

```yaml
admin:
  approvals:
    policy_changes:
      enabled: true                     # Default false for existing deployments.
      required_scopes: [mcphub:security]
      subjects: [security-reviewer-sub]  # Optional exact OIDC subjects.
      require_step_up: true             # Requires step_up_acr_values above.
    notifications:
      url: https://notify.example.com/mcphub
      secret: ${MCPHUB_WEBHOOK_SECRET}   # At least 32 bytes.
    audit_archive:
      url: https://audit.example.com/mcphub
      key_id: audit-2026-01
      signing_key: ${MCPHUB_AUDIT_SIGNING_KEY}
```

Configuration administrators propose creates/updates to backends, tool groups, HTTP tools and OpenAPI imports; APIs and `mcpbridge admin` return **202** with `pending_approval`, `approval_id` and `approval_url`. The web editor opens the proposal. No configuration is activated yet. Another subject with the security scope must sign in using **Sign in as a security administrator** (`/auth/login?role=security`), review the redacted before/after snapshot, and **Approve and apply**. Credential changes are marked without revealing values. A security role alone cannot directly edit configuration, and a write reviewer alone cannot approve configuration. Review requires a browser session and CSRF checks; API tokens cannot approve. Deletes and a pure enabled→disabled change take effect immediately. Re-enabling and changing other fields require review. YAML/operator access and database access remain trusted; these static governance settings cannot be edited by the management API.

Proposals bind target revisions and, for tools/imports, their group revision. A concurrent modification makes application fail instead of overwriting it. OpenAPI proposals include the resolved document and generated definitions; approval never refetches the specification. Changed automatic refreshes also become proposals and retain the last approved definitions until reviewed. Restart invalidates unstarted proposals; interrupted application is marked unknown and requires inspection. A failed/expired proposal needs a fresh proposal. At most 20 active requests per issuer/subject and 32 MiB per complete configuration proposal are accepted.

For a production write, configure the matching tool rule:

```yaml
approval:
  required_approvals: 2
  require_step_up: true
  operation_id_argument: /operation_id
  status_tool: get_operation_status
  approvers:
    - subjects: [reviewer-a-sub, reviewer-b-sub]
  # Optional conditions can only increase the base quorum/authentication strength.
  # Set the base to 1 if only these resource values require two reviewers.
  risk_rules:
    - resources:
        - argument: /project
          allowed_values: [production]
      required_approvals: 2
      require_step_up: true
```

Quorum defaults to one and permits one or two. Every vote must come from a distinct authorized subject; quorum two always excludes the requester. Required step-up is performed separately by each reviewer. The first vote leaves a two-reviewer request pending; the execution deadline starts only at quorum. Any authorized reviewer can reject while pending or revoke before execution. The UI shows vote count and reviewers. Risk conditions use all-of JSON Pointer conditions with exact string values; an array matches if **any** member has the configured risk value. Missing, empty or mistyped risk arguments fail closed. All matching policies combine with the strongest quorum/step-up requirement. For deletion/bulk tools or a production-only endpoint, set the base quorum on the actual tool: an agent-supplied label such as `risk: low` is not a trustworthy risk boundary.

`operation_id_argument` selects a required 1–128 character string using letters, digits, `.`, `_`, `:` or `-`. Generate it once per business operation and reuse it across client retries. MCPHub binds it to issuer, subject, source and tool. Identical normalized execution parameters return the existing approval/status; different arguments or business metadata conflict. Only transport `progressToken` is excluded from comparison. Completed, rejected, expired and unknown operations cannot reuse the ID for a fresh write. Retention removes detailed results but retains a small hashed identity tombstone, so old IDs remain unavailable; tombstones grow with the number of unique operations. Different caller identities are intentionally isolated. Without an operation ID, identical **active** requests share one approval; after completion, the same arguments may represent a new operation.

The operation ID remains in the saved tool arguments and is sent upstream. An HTTP Header parameter can map it to `Idempotency-Key`; MCP tools must implement their own key handling. Gateway deduplication cannot prevent writes made outside MCPHub, or retries with a new ID. Backend idempotency and atomic version checks are still required for business-level guarantees.

Clients should poll the explicitly read-only tool:

```json
{"name":"mcphub_approval_status","arguments":{"approval_id":"..."}}
```

It returns state, quorum, voters, deadlines and an available cached result, without claiming or executing the operation. Optional `query_upstream: true` invokes `status_tool`, an explicitly published read-only original tool in the same backend/group, using the saved arguments. It must accept those arguments, including the operation ID, and return structured content describing the upstream state/receipt. Scope, resource, configuration and rate-limit checks still apply. The response is an `upstream_observation`; it does not reset an unknown outcome or authorize replay. Status lookup works across restart when the source/tool configuration is unchanged; changed configuration suppresses cached results and upstream lookup. Basic status remains private to the original issuer/subject. Use `mcphub_resume_approval` only when deliberately executing an approved write.

### Approval notifications and independent audit archive

Both integrations are optional HTTPS endpoints, configured outside the management API. URLs cannot contain credentials, query parameters or fragments. Approval events and delivery records commit in the same SQLite/PostgreSQL transaction. A single worker sends them in order, with a 10-second timeout, no redirects and durable retry backoff from 5 seconds to one hour. Receivers must deduplicate `event_id`: a lost receipt can cause redelivery. A failed archive entry blocks later archive entries to preserve chain order. Events include requests, votes, decisions, cancellation, execution and expiry; pending/approved requests receive one reminder within five minutes of expiry, on the minute-based maintenance tick. Notifications contain only `event_id`, `approval_id`, `action`, `approval_url`, and `expires_at`. Links open the authenticated review page; webhooks cannot approve.

Verify `X-MCPHub-Signature: sha256=<hex>` as HMAC-SHA256 with the webhook secret over `X-MCPHub-Timestamp + "." + raw_body`. Check timestamp freshness and deduplicate the event ID. A 2xx response acknowledges notification delivery. Keep the shared secret out of URLs and logs.

The audit key is a Base64-encoded **64-byte Ed25519 private key** (seed followed by public key); the corresponding 32-byte public key must be distributed independently to the archive verifier. Each envelope contains `entry`, `hash` (SHA-256 of the exact serialized entry), and `signature` (Base64 Ed25519 signature of the 32-byte hash). Entries contain a sequence, previous hash, key ID, event/approval IDs, action, actor, timestamp, decision reason/authentication evidence, and hashes binding the encrypted local intent/result. Tool arguments, configuration secrets and access tokens are not exported; decision reasons should not contain secrets. The external receiver verifies the signature and chain, durably stores the exact JSON envelope, then returns **2xx** with `{"sequence":123,"hash":"matching-envelope-hash"}`. A different/missing receipt is retried. Do not pretty-print or reserialize the signed `entry`.

```bash
mcphub verify-audit --file archive.jsonl --key "audit-2026-01=$AUDIT_PUBLIC_KEY"
# For rotated signing keys, repeat --key ID=BASE64_PUBLIC_KEY.
# For a partial file, pass the externally trusted preceding checkpoint:
mcphub verify-audit --file next.jsonl --key "audit-2026-01=$AUDIT_PUBLIC_KEY"   --after-sequence 123 --after-hash "$TRUSTED_PREVIOUS_HASH"
```

The verifier rejects altered records, missing/reordered internal entries, wrong keys and broken links, and prints the final sequence/hash checkpoint. Preserve and compare checkpoints outside the MCPHub database to detect rollback/deleted tails; a valid prefix alone cannot prove completeness. Use a separately controlled append-only/WORM sink and protect the signing key. This does not defend against an operator controlling both MCPHub's signer and the archive, and it archives approval events rather than every general activity event.

`GET /api/v1/approvals/delivery` exposes enabled flags and pending/failed counts to configuration/security browser roles. The approval page and stderr logs flag delivery failures. Unacknowledged archive entries prevent retention from deleting their approval details; successful external archives outlive local retention. Monitor queue/database growth during outages. No external webhook, production OIDC provider or archive service is provisioned automatically.

### Backend-local tool rules

`tool_rules` is evaluated per backend before the configured ID is added to a public tool name. A rule's `match` uses Go `path.Match` on the original backend tool name: matching is full-string and case-sensitive, so a pattern such as `admin.*` does not match `Admin.Read` or a substring. Every matching rule contributes its `required_scopes`; MCPHub unions and deduplicates those scopes, then requires all of them together with the backend-level `required_scopes`.

Validation expands existing `${ENV}` placeholders in `match` and rule scope strings; every `match` must be non-empty and a valid Go `path.Match` pattern, every rule must declare at least one of `effect`, `approval`, `required_scopes`, or `resource_rules`, scope entries must be non-empty with no whitespace or duplicates, and duplicate `match` entries within one backend are rejected.

Tools that fail this policy are omitted from `tools/list`. If a client directly calls a known tool without the required scopes, MCPHub returns 403 and a precise `WWW-Authenticate` challenge with `error="insufficient_scope"`, the path-aware `resource_metadata` URL, and a space-delimited `scope` value containing the missing scopes. A rule that matches no tool in the current catalog generation emits one warning, remains valid, and can match after a later catalog refresh. Editing `tool_rules` is supported by SIGHUP and takes effect with the reloaded backend policy.

Backend IDs are compared case-insensitively for uniqueness. Tool and prompt names retain the configured ID, while every exposed resource or resource-template URI uses a lowercase authority and resolves back to the configured ID.

## Other configuration guides

| Configuration or task | Reference |
| --- | --- |
| `auth.sso`, user policies, department/group synchronization and administrator recovery | [SSO and user management](sso-and-user-management.md) |
| `client_authorization`, portal callbacks and required client grants | [Administrator manual: client authorization](admin-guide.md#enable-client-authorization) |
| `vault`, `backends[].credentials`, shared/personal accounts | [Vault configuration and operations](vault-accounts.md) |
| HTTPS, remote administration and databases | [Deployment guide](../deploy/README.md) |
| Complete YAML | [Base example](../config.example.yaml), [remote SQLite](../deploy/config.remote-sqlite.yaml), [remote PostgreSQL](../deploy/config.remote-postgres.yaml) |

### External OIDC registration for the user CLI

These requirements apply when using an external issuer directly. With `auth.sso`, register public clients with MCPHub as described in the [SSO guide](sso-and-user-management.md). Give users the MCP URL, client ID and any required fixed callback port.

Register a **public native OAuth client** at that issuer with authorization-code and refresh-token grants, PKCE S256, and token-endpoint authentication method `none`. Allow the callback `http://127.0.0.1:<port>/oauth/callback`; use an arbitrary loopback port when the provider supports native clients, or register a fixed port and pass `--callback-port 8765`. Discovery must advertise PKCE S256. The issuer must issue a signed JWT **access token** with an audience containing the exact MCPHub public URL, including `/mcp`, plus `sub`, `exp`, and the required scopes. No client secret is needed on the user's machine.

## Starting from the full YAML example

The default [config.example.yaml](../config.example.yaml) is identical in the v2.2.2 server archive and online template. It enables the local console and SQLite with `backends: []`. Only `MCPHUB_PUBLIC_URL`, `MCPHUB_AUTH_ISSUER` and `MCPHUB_CONFIG_KEY` are required; configure addresses and credentials per service in the UI.

Download the template into a new private deployment directory, replace the two HTTPS addresses, and start:

```bash
curl -fL https://samuelsupe.github.io/mcphub/examples/config.example.yaml -o config.yaml
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_AUTH_ISSUER=https://idp.example.com
umask 077
mkdir -p secrets
test -f secrets/config.key || openssl rand -base64 32 > secrets/config.key
export MCPHUB_CONFIG_KEY="$(cat secrets/config.key)"
mcphub validate --config config.yaml
mcphub serve --config config.yaml
```

Open `http://127.0.0.1:8081/` and follow **start the console → add backends → test connections → publish tools**. Configure a separate service URL and Header/OAuth credential for each backend. Publish only reviewed tools explicitly classified as `read`, and assign required scopes. Restarting with the same key restores service settings, credentials and tool policies from SQLite.

`validate` is a read-only configuration check; it does not verify identity login, backend connections or tool calls. `serve` initializes fresh storage. Check `/healthz` and `/readyz`, then make a real call using [deployment verification](../deploy/README.md#verify-the-deployment).

Remote administrators use the [remote templates](../deploy/README.md), adding an administrator origin, registered identity client and scopes. Deployments without a console use the separate [advanced YAML-only example](../deploy/config.yaml-only.example.yaml); fill each backend's actual address, credential, publication list and read policy in the file.

## Capabilities and boundaries

- Connects to multiple backends over MCP Streamable HTTP; backend catalogs are paginated, and the tools, prompts, resources, and resource-template lists are discovered in parallel and refreshed on backend notifications or the configured refresh schedule.
- Aggregates `tools`, `prompts`, `resources`, resource templates, and completion; forwards tool/prompt/resource calls, resource subscribe/unsubscribe, progress notifications, and resource-update notifications.
- Streams backend SSE responses through unchanged while inspecting progress notifications; at most 1 MiB of each event is buffered for inspection, and an oversized event is forwarded unchanged without progress inspection.
- Normalizes missing 2026-07-28 metadata on `notifications/cancelled` from official Go MCP SDK v1.7.0 clients on both Hub ingress and backend egress, so the same logical MCP session remains reusable after cancellation or unsubscribe; this is an interoperability shim, not a custom extension.
- Namespaces capabilities with `backend.id` and rewrites resource URIs to avoid same-name capability and URI collisions between backends.
- Verifies Bearer JWTs with OIDC discovery and JWKS, then filters catalogs and calls by each backend's `required_scopes`.
- Includes the separate `mcpbridge` program for browser login through an external OIDC service and a local stdio-to-HTTP connector with credential refresh.
- Requires explicit tool publication, resource-argument restrictions and independent approval for write/unclassified tools; supports reviewer quorum, OIDC step-up, configuration review and signed audit delivery.
- Manages SSO user/group/department permissions and per-client scopes through the personal consent portal and local Broker. `setup` emits credential-free MCP configuration; `doctor` diagnoses access without executing tools.
- Applies backend-local `tool_rules` to original tool names with Go `path.Match`; matching rules union and deduplicate required scopes, use all-of authorization, and hide unauthorized tools from `tools/list`.
- Supports static backend request headers or OAuth 2.0 `client_credentials`; neither mode may provide a static `Authorization` header together with OAuth.
- The administration UI groups nine pages under overview, connections, access control, and governance/audit. It supports persistent Chinese/English selection, role-aware navigation and narrow screens. Groups can publish hand-authored HTTP tools and multiple OpenAPI 3.0/3.1 imports through `/mcp`; group Base URL, headers, OAuth, scopes, and timeout are shared, and no raw HTTP proxy is exposed.
- Supports OIDC-authenticated remote browser/CLI administration and encrypted SQLite or PostgreSQL configuration storage for one gateway instance.
- Configures per-endpoint request rates, burst capacity and concurrency through the UI/API; limits are disabled by default and shared by callers.
- Provides health, readiness, and RFC 9728 Protected Resource Metadata endpoints, plus SIGHUP configuration reload.

When a backend connection fails, MCPHub retries and retains its last-known catalog. The catalog may remain listable while calls fail until the connection recovers. Startup does not exit just because a required backend is temporarily unavailable, so `/readyz` remains 503; a runtime created by SIGHUP requires its required backends to connect successfully on the initial attempt. Backend-authored JSON-RPC errors are returned unchanged. Network or transport failures are exposed only as `backend <id> unavailable`, so internal backend URLs, query strings, and credentials do not cross the Hub boundary. On reconnect, every tracked resource subscription must be restored successfully before the backend is marked ready; a restore failure keeps it unavailable and triggers another reconnect attempt.

## HTTP endpoints and RFC 9728

Assuming `server.public_url: https://hub.example.com/mcp`:

| Address | Auth | Semantics |
| --- | --- | --- |
| `GET /healthz` | none | Returns `200 {"status":"ok"}` while a runtime exists; use it as a liveness probe. |
| `GET /readyz` | none | Returns 200 when the OIDC verifier and all required backends are ready, otherwise 503. JSON includes `backends_ready`, `backends_total`, `required_ready`, `required_total`, and `auth_verifier_ready`. |
| `GET /.well-known/oauth-protected-resource/mcp` | none | Path-aware RFC 9728 Protected Resource Metadata address; expose this address to clients. |
| `GET /.well-known/oauth-protected-resource` | none | Root-path compatibility alias for the same metadata. Route both addresses to MCPHub through a reverse proxy. |
| `/mcp` | Bearer JWT | Stateless MCP Streamable HTTP entry; accepts POST only, and compatibility clients use the same `/mcp` POST semantics. Modern clients may use request-scoped SSE in the POST response; MCPHub does not provide a standalone GET SSE or DELETE session endpoint. Catalogs and calls are filtered by token scopes. |

Metadata has `resource` equal to the complete `public_url`, `authorization_servers` containing `auth.issuer`, `scopes_supported` equal to the union of backend required scopes, and `bearer_methods_supported` equal to `header`. An MCP request without a token receives a 401 challenge whose `resource_metadata` points to `https://hub.example.com/.well-known/oauth-protected-resource/mcp`; missing scopes return 403 `insufficient_scope`.

Cross-origin requests accept only the `public_url` origin or an exact origin in `allowed_origins`. Preflight responses allow only `POST` (with `OPTIONS` as the preflight response) and the implemented MCP/trace headers (including supported `Mcp-Param-*` parameter headers); `*` is not supported. Other paths return 404.

## Name and URI mapping

- Tools and prompts are exposed as `<backend-id>.<original-name>`. Original names must match `[A-Za-z0-9_.-]{1,128}`. If the namespaced name exceeds 128 characters, MCPHub truncates it while retaining the backend prefix and appends a short SHA-256 suffix of the original name. Invalid metadata and mapped collisions are omitted and logged.
- Static resources and `ResourceLink`/embedded resources in results are encoded as `mcphub://<backend-id>/r/<base64url-no-padding(original-uri)>`. MCPHub decodes that URI to read from the corresponding backend and recursively rewrites resource URIs in results.
- Resource templates are encoded as `mcphub://<backend-id>/t/<sha256(original-template)>`, retaining URI-template variables as a query expression (for example, `{?id}`). Reads and completions restore the backend's original template.
- `ResourceLink`, `EmbeddedResource`, and `ResourceContents` URIs in backend tool, prompt, or resource results are rewritten and recorded as issued resources for that backend. They remain readable and subscribable while their URI digest is retained, but are not added individually to the public `resources` catalog. Each backend retains at most 16,384 distinct issued-URI SHA-256 digests; once the oldest digest is evicted, a new read or subscription for that URI may fail, while existing subscription cancellation and session cleanup still use the session map. `resource updated` notifications never create catalog entries.
- Resource subscriptions are tracked and deduplicated per upstream MCP session, while backend references are shared and reference-counted by original URI; an unpaired unsubscribe is ignored, session close cleans up, and reconnect restores all tracked subscriptions, waiting for `notifications/subscriptions/acknowledged` when the backend protocol supports that acknowledgement, before the backend is marked ready. An acknowledgement's subscription ID maps back to the original subscription URI(s), so a 2026 resource update fans out to those URIs even when the event URI differs; timeout, cancellation, and session/reconnect cleanup remove the mapping. When a modern `subscriptions/listen` stream is canceled or disconnects, cleanup detaches from the canceled upstream context but remains bounded by backend `request_timeout` and session lifecycle, so legacy backends still receive `resources/unsubscribe`. If any restore fails, the backend remains unavailable and the reconnect loop tries again.

Each token's scope set selects an independent backend view, so one MCP connection sees only the capabilities allowed for that token.

## SIGHUP reload and shutdown

On Unix, send signals to a running `serve` process:

```bash
kill -HUP <mcphub-pid>   # reload the same --config file
kill -TERM <mcphub-pid>  # graceful shutdown
```

SIGHUP fully loads, expands, and validates the configuration before building a candidate runtime; failures leave the old runtime in place. Required backends in the new runtime must connect successfully on the initial attempt. The old runtime waits for active requests for `drain_timeout` before closing; when a runtime generation closes, it cancels request contexts bound to that generation before closing its Hub/backend state, preventing late session registration. That cancellation immediately expires the underlying write deadline, interrupting slow or unread subscription writes after the drain; ordinary requests retain their `request_timeout` deadline. Allowed origins, catalog/request/drain parameters, the backend list, backend authentication, scopes, and backend-local `tool_rules` can be reloaded. Changes to these fields are rejected and require a restart:

- `server.listen`
- `server.public_url`
- all `auth` fields (including `auth.sso`)
- all `client_authorization` and `vault` fields
- every `admin` field

These values determine bound listeners, storage/encryption identity, RFC 9728/JWT audience, and the OIDC verifier, so they cannot be changed by replacing only the in-memory runtime. In admin mode SIGHUP reloads static YAML fields and composes backends from the selected database; YAML backend changes are ignored after first import.

SIGHUP candidate startup uses a cancelable context; shutdown cancels a candidate that is still connecting. Candidate required backends must connect successfully before the swap, and each candidate or retired runtime closes its own backend sessions and connections.

SIGINT and SIGTERM first stop MCPHub from accepting new requests, then keep the current runtime and backend context alive while HTTP requests drain for `drain_timeout`. If the HTTP drain reaches that timeout, MCPHub force-closes the remaining HTTP connections; generation close then cancels request contexts bound to the generation before backend state is closed.

When SIGHUP creates an unavailable optional backend, it inherits the previous in-memory catalog only when its catalog-source identity is unchanged: backend ID and URL, `allow_insecure_http`, every fixed header, the complete `credentials` configuration, and OAuth configuration presence plus `type`, `issuer`, `client_id`, `client_secret`, and `scopes` must match. Any credential, OAuth, or tenant-selection-header change blocks reuse. Fields that do not identify the catalog source, such as `required`, `required_scopes`, `tool_rules`, and timeouts, do not block reuse; inherited data never marks the new backend ready.

## Client credentials and connection boundaries

When strict client authorization is enabled, the server requires both the OIDC token and an opaque `MCPHub-Grant` credential. Effective scopes are their intersection. Issuer, user, resource, endpoint UID, expiry, tool publication, resource rules and live policy are checked on every request. Scope/target changes, disabled or recreated endpoints and changed HTTP tool execution semantics require fresh consent. New tools are never automatically added to an existing grant. No grant or user token is forwarded to upstream systems.

Private sockets/named pipes, OS peer checks and independent IPC credentials isolate paired entries from other OS users. They do **not** prove application identity or isolate hostile processes running under the same OS account. Release publishing requires the native Windows CLI test suite, including Broker IPC, on x64 and ARM64; macOS/Linux use Unix sockets with OS peer checks. See the [design and validation record](broker-authorization-design.zh-CN.md).
