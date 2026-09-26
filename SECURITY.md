# Security Policy / 安全策略

[English](#english) | [中文](#中文)

## English

Repository: [SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · documented release: v2.0.0 · module: `github.com/SamuelSupe/mcphub/v2` · [release notes](RELEASE_NOTES_v2.0.0.md) · license: [Apache License 2.0](LICENSE)

### Scope

MCPHub's security boundary includes:

- the public MCP Streamable HTTP endpoint and Bearer JWT verification;
- OIDC discovery/JWKS, JWT `iss`/`aud`/`sub`/`exp`/`nbf`, and `scope`/`scp` handling;
- backend URL validation, static headers, OAuth `client_credentials`, and redirect behavior;
- backend-local `tool_rules` and per-tool scope challenges;
- CORS/origin checks, RFC 9728 Protected Resource Metadata, readiness/health exposure;
- configuration files, environment expansion, secrets, and SIGHUP reload behavior;
- the optional local/remote administration listener, encrypted SQLite/PostgreSQL backend configuration, bootstrap import, and runtime replacement API;
- admin-managed tool groups, hand-authored HTTP tools, OpenAPI 3.0/3.1 imports, source refresh, and the response/body limits on those paths.

The current published release explicitly does not provide stdio backends, a standalone legacy GET SSE endpoint, native TLS, dynamic tenants or per-user backend credentials, opaque-token introspection, Tasks, or MCP Apps. Local mode is loopback-only; authenticated remote mode is described below. TLS termination, external rate limiting, and edge access policy must be supplied by the deployment's reverse proxy or network layer.

The release also provides a separate `mcphub-cli` executable with `login/connect/status/logout` for external OIDC user login. The server executable `mcphub` provides `serve/validate`. Only the local CLI connector speaks stdio; it sends user access tokens to its saved HTTP gateway, never to backend servers. Login uses a public client, PKCE S256, state/issuer validation and a loopback callback, and saves credentials only after the gateway accepts an authenticated handshake. Discovery and token requests require HTTPS and do not follow redirects. The callback listener is temporary and binds only `127.0.0.1`.

Local credentials are unencrypted JSON. On macOS/Linux, `~/.mcphub/` uses owner-only permissions (directory `0700`, files `0600`). On Windows, `%USERPROFILE%\.mcphub\` and credential files are created with a protected DACL granting access only to the current user; opening them checks ownership and access rules. Credential directories, profiles, and lock files that are reparse points or grant access to other accounts are rejected. Keep them out of shared storage and public backups. Platform file locks and temporary-file replacement serialize refresh-token rotation. A new login invalidates existing connectors; logout clears cached tokens and prevents subsequent requests, but does not revoke issuer tokens, terminate already accepted operations, or sign out browser sessions. `connect` never starts interactive authorization, retries a 401 only once after refreshing, and never replays a tool operation on a network failure. stdout is exclusively MCP protocol output. No token values are exposed by status or diagnostics.

### Per-client grants and local Broker

Strict endpoints require a verified OIDC access token **and** a random opaque `MCPHub-Grant` credential. A grant ID, broker session ID, client display name, `clientInfo` or arbitrary client header does not authorize a request. Scopes are intersected with the current JWT; no claim is made that upstream service-account ACLs become end-user ACLs. Grant credentials are not forwarded upstream.

Consent is frozen, expires after five minutes, belongs to the exact issuer/subject/resource, and requires an ordinary-user browser session plus Origin and CSRF checks. An API bearer cannot confirm it; administrators can revoke but cannot consent for another user. Exchange proofs are single-use. Server grant/session credentials are stored as hashes; grant details and lifecycle events are encrypted. Configure the existing independent audit archive for signed chained delivery; a database alone is not a tamper-proof archive.

Endpoint UIDs cannot be reused after deletion. Grant/session deadlines, revocation, endpoint policy and tool/resource constraints are checked at admission. Revocation cancels active streams but does not reverse an already admitted upstream side effect. Write-request capability never bypasses per-operation approval. Approval lookup, resume, cancellation and cached results are bound to the grant revision, client and endpoint UID; business operation deduplication also survives grant replacement.

The local Broker uses owner-only Unix sockets with peer UID checks on macOS/Linux, and user-restricted named pipes with process SID checks on Windows. Local IPC entries have independent random credentials; MCP configuration contains only their IDs. Files remain unencrypted and accessible to the owning OS account. A malicious process under that account may impersonate an entry or steal credentials; strong application attestation, OS keychains and DPoP are outside this version. Windows named-pipe runtime security still requires validation on Windows; cross-compilation alone does not establish it.

Logout clears local secrets before network I/O and tries remote session revocation. If offline, only non-secret session references remain and remote revocation is explicitly unconfirmed; use the user portal to revoke them. Stopping the Broker does not revoke remote authorizations. No network error triggers automatic tool replay. Same-profile refresh is serialized with process locks and token rotation is saved atomically.

### Permission workbench and diagnostics

Tool policy inspection and access simulation require configuration-administrator privileges; reviewer-only and MCP user scopes cannot access them. The simulator treats supplied scopes as assumptions and checks saved grants against the exact issuer/subject and current policy. It never mints credentials, creates approvals, previews or executes tools, or consumes execution quotas. Results are a configuration snapshot, not a verified user's permissions or a promise that later execution will succeed. Policy edits use the existing revision and optional configuration-approval controls. `mcphub-cli doctor` uses the connector's credential validation and refresh path and may persist rotated tokens. Its network actions are authorization status, MCP initialization and catalog discovery; it does not execute tools or start interactive authorization. Reports omit token, Grant credential and IPC secret values.

### Reporting a vulnerability

Please do not disclose a suspected vulnerability in a public issue, discussion, or pull request. Use GitHub's [private vulnerability reporting/Security Advisory flow](https://github.com/SamuelSupe/mcphub/security/advisories/new) when it is enabled for this repository. If that flow is unavailable, contact the maintainers through the repository's private security contact channel.

Include, when safe:

- affected commit, tag, or deployment version;
- a concise impact statement and attack preconditions;
- a minimal reproduction or request/response transcript with secrets removed;
- suggested mitigation or workaround;
- whether the issue affects only a reverse-proxy deployment or the MCPHub process itself.

Never include live client secrets, API keys, bearer tokens, private keys, production URLs containing credentials, or unredacted personal data. If a secret was exposed, revoke/rotate it first and report only the redacted evidence.

### Deployment requirements

- Use HTTPS for `server.public_url`, `auth.issuer`, and remote backend URLs. HTTP is accepted only for loopback backend URLs with explicit `allow_insecure_http: true`.
- Require `server.public_url` to appear in JWT `aud`, including its path: a string `aud` must equal `public_url`, while an audience array must contain `public_url`. Route both RFC 9728 metadata paths through the same trusted origin.
- Store `client_secret`, API keys, static Authorization values, and environment files outside Git; mount configuration read-only where possible and restrict file permissions.
- Use exact HTTPS `allowed_origins` values. Keep the existing preflight header allowlist, permit only `POST` (with `OPTIONS` as the preflight response), do not use wildcard origins, and do not expose management/probe endpoints more broadly than necessary.
- Treat `/healthz`, `/readyz`, and metadata as unauthenticated endpoints. Protect their network visibility at the proxy or network layer.
- The stateless Streamable HTTP `/mcp` endpoint accepts POST only; modern clients may use request-scoped SSE in the POST response, while MCPHub exposes no standalone GET SSE or DELETE session endpoint. Compatibility clients use the same `/mcp` POST semantics.
- Do not put secrets in backend IDs, capability names, logs, issue reports, or release artifacts. Static backend headers apply to data-plane requests; OAuth discovery/token requests intentionally do not receive them.
- Manage tool groups, manual HTTP tools, and OpenAPI imports only through the local or authenticated remote admin API under `/api/v1/tool-groups`; the selected database is their source of truth. Do not add a YAML schema for these resources or treat SIGHUP as a migration mechanism.
- A tool group shares one HTTPS base URL, static headers or OAuth `client_credentials`, JWT required scopes, and request timeout across its tools. Its HTTP tools are exposed only as MCP capabilities through `/mcp`; MCPHub must not become a raw HTTP proxy or arbitrary method/path passthrough.
- Group and OpenAPI source URLs must use HTTPS and fetchers do not follow redirects. A cross-origin OpenAPI source fetch must not send the group's static headers, bearer material, or OAuth client secret. Keep group secrets encrypted in the configuration database and outside logs, exports, and browser-visible API fields.
- Tool-group, manual-tool, and import resources each use `ETag` revisions; update/delete requests must supply the matching `If-Match`, and stale revisions must fail closed with `409 revision_conflict`.
- Enforce a 1 MiB default HTTP-tool response limit (configurable only from 64 KiB through 16 MiB), a 5 MiB OpenAPI document limit, and a 6 MiB request limit for bodies carrying an OpenAPI document. URL-backed imports refresh every 15 minutes by default (allowed range 1 minute to 24 hours); failed refreshes retain the last-known-good document/tools and retry with backoff.
- During SIGHUP, an unavailable optional backend may reuse a previous in-memory catalog only when its backend ID/URL, `allow_insecure_http`, every fixed header, and OAuth configuration presence plus `type`, `issuer`, `client_id`, `client_secret`, and `scopes` are unchanged. Credential, OAuth, or tenant-selection-header changes prevent reuse; non-identity fields such as `required`, `required_scopes`, `tool_rules`, and timeouts do not, and reuse never marks the new backend ready.
- SIGHUP candidate startup uses a cancelable context; shutdown cancels a candidate that is still connecting. Required backends must connect successfully before the candidate replaces the current generation, and each candidate or retired generation closes its own backend sessions and connections.
- Backend IDs may contain uppercase letters but must be unique case-insensitively. Tool and prompt names preserve the configured ID; resource and resource-template URI authorities use the lowercase ID.
- Keep the unauthenticated `mode: local` admin listener on its validated numeric loopback address and never publish it through a reverse proxy. Store `MCPHUB_CONFIG_KEY` separately from YAML and SQLite backups; stored secrets require the exact same Base64-encoded 32-byte key for recovery.

### Remote administration

`mode: remote` requires HTTPS termination at a trusted proxy, an exact configured Host/Origin and a separate admin JWT audience (`admin.public_url`) plus all configured administrator scopes. Upstream HTTP listeners must remain private. Identity-provider `roles`/`groups` and proxy identity headers never grant access. UI sessions use opaque Secure/HttpOnly/SameSite cookies, CSRF checks for mutations, PKCE S256, single-use browser-bound state and authorization-response issuer validation. Access/refresh tokens stay in server memory; refresh rotation is serialized per session. Sessions expire in eight hours and are lost on restart. Logout clears the current browser/CLI session, not IDP SSO or already issued JWTs.

Configuration audit attributes remote writes to the verified subject; it is not a tamper-proof compliance log. PostgreSQL preserves encryption, revision checks and transactional audit. Use a dedicated database/schema and verify-full TLS for external database connections. The Compose example's unencrypted database connection is confined to its private container network. Both backends currently support one gateway instance; changing drivers does not migrate data. See [deployment boundaries](deploy/README.md).

Endpoint rate/concurrency limits are opt-in and apply after authentication/scope checks. They reject before upstream execution, preserve cancellation/cleanup, and share budgets across callers of the same configured ID. Counters are process-local and reset on restart. They do not replace reverse-proxy protection for unauthenticated traffic.

### Write execution approval

Write and unclassified tools require an immutable, single-use server-side approval. Reviewers use a separate scope from configuration administrators, exact subject/resource grants, and optional separation of requester and reviewer. Listing and detail access enforce those grants. Reviewer browser cookies plus CSRF/Origin checks are required; local mode and bearer tokens cannot approve. Strong-authentication policies require a fresh signed OIDC ID token with the configured ACR, matching subject/nonce/audience and recent auth_time. The proof is bound to one approval and session and consumed once; the provider must enforce the configured ACR's authentication meaning.

The original caller resumes the saved request; the gateway rechecks permission, generation, rate limits and any configured preview/version. Cancellation and revocation compete atomically with execution and cannot undo an admitted write. Preview tools must be explicitly published reads; upstream writes must enforce version preconditions atomically. Unknown results remain non-replayable after investigation notes. SQLite/PostgreSQL encrypt requests, previews, results and detailed audit; terminal details are removed after configured retention. General status events remain. Business operation IDs bind normalized requests to the original caller/source/tool, retain identity tombstones after history cleanup, and never restore an unknown operation to executable. Read-only status queries do not claim execution. Two-reviewer policies count distinct authorized subjects and exclude the requester. This is gateway admission control; the backend must provide business idempotency and atomic version checks.

Optional signed audit archival uses a transactional outbox, an Ed25519 signature and hash chain, and exact durable acknowledgements from an independent HTTPS sink. Failed delivery is visible and retains unarchived approval detail. Verifying an exported prefix cannot prove completeness: maintain a trusted checkpoint outside the database and use independent append-only retention. A compromised signer and archive remain outside this protection. Notifications are HMAC-authenticated links without execution arguments; they never authorize decisions. Detailed contracts and deployment boundaries are in the README.

Agents must not control reviewer browsers, configuration/database access, encryption keys or upstream write credentials. When `policy_changes.enabled` is true, creates/updates require a distinct security reviewer; pure disabling and deletion remain immediate. Proposals bind revisions and resolved OpenAPI definitions. With governance disabled, configuration administrators can reclassify tools directly. Deployment configuration, database access and signing/encryption keys remain trusted. Authentication strength does not prove that a human understood an operation, and a read classification cannot prove absence of side effects. Enforce downstream least privilege and network isolation. See [limits, roles and migration](README.md#one-time-write-approval).

### Security behavior to preserve

Backend access uses all-of scope semantics: every entry in `required_scopes` must be present in the verified token; JWT `scope` is a space-delimited string, while `scp` is a string or string array. Backend headers reject line breaks, duplicates, `Accept`, `Content-Type`, any `Mcp-*`, `Proxy-Authorization`, `Proxy-Authenticate`, and other HTTP/MCP transport-managed names. OAuth mode rejects a static `Authorization` header. `client_credentials` discovery needs only an exact issuer and token endpoint from RFC 8414/OIDC metadata, not interactive authorization or PKCE metadata. Initial OIDC readiness also requires an absolute HTTPS `jwks_uri` and a reachable JWKS response containing at least one parseable, valid, asymmetric public verification key; symmetric `oct` keys and invalid or empty keys do not satisfy this condition. OIDC discovery and JWKS responses are each capped at 1 MiB; OAuth metadata responses have the same cap. OIDC and backend HTTP clients do not follow redirects. Every MCP-listener HTTP route keeps the `request_timeout` request-body read deadline until the body is consumed or closed, including unauthenticated and rejected requests; once MCP handling proceeds, ordinary MCP POSTs set response-write deadlines and request contexts from the same value. The newer `subscriptions/listen` POST remains long-lived after its body is read and bypasses those ordinary response-write and request-context timeouts. Logs regenerate overlong or invalid request IDs, validate or sanitize capability/resource-URI fields, and record only external error types. A retiring runtime generation waits for its active requests, then cancels request contexts bound to that generation before closing Hub/backend state; this prevents late session registration. SIGINT/SIGTERM force-close remaining HTTP connections when the `drain_timeout` HTTP drain expires.

Backend SSE responses are streaming passthrough: progress inspection buffers at most 1 MiB per event, and oversized events are forwarded unchanged without progress inspection. Acknowledged 2026 resource subscription IDs map updates back to their original subscription URI(s), including when the update event URI differs; timeout, cancellation, and session/reconnect cleanup remove the mapping. A canceled or disconnected modern `subscriptions/listen` stream detaches cleanup from the canceled upstream context but remains bounded by the backend timeout and session lifecycle, so legacy backends still receive `resources/unsubscribe`. Runtime or client context cancellation expires the underlying write deadline, so slow or unread subscription writes are interrupted at generation drain while ordinary requests retain `request_timeout`.

Explicit-publication policy: `published_tools` is an exact allowlist, empty by default; discovery and scope globs cannot approve new tools. Manual HTTP tools also default to disabled. Invocation checks current publication/enable state and scopes, then applies every matching `resource_rules` constraint to the actual arguments. Resource values are exact strings (or non-empty all-allowed string arrays); missing or mistyped selectors are denied. Constrained arguments are normalized before forwarding to avoid duplicate-key parser differences. These shared allowlists do not establish per-user ownership or authorize SQL, filesystem symlinks, aliases or secondary selectors; the backend remains responsible for those semantics. Already admitted calls may complete after policy changes. See the [configuration and migration notes](README.md#explicit-publication-and-resource-limits).

Backend-local `tool_rules` match original backend tool names with full-string, case-sensitive Go `path.Match` semantics. Matching rules union and deduplicate scopes, and all resulting scopes plus backend-level `required_scopes` are required; unauthorized tools are omitted from `tools/list`, while known direct calls return 403 with `WWW-Authenticate` carrying `error="insufficient_scope"`, the path-aware `resource_metadata`, and the missing scopes. `${ENV}` expansion applies to match and scope strings; empty or invalid patterns, empty/whitespace/duplicate scope entries, and duplicate matches within one backend are rejected.

The managed HTTP-tool boundary is intentionally narrower than a proxy: manual definitions and OpenAPI imports are converted into named MCP tools, and only those names can be listed or invoked through `/mcp`. Base URL/header/OAuth/scope/timeout settings are inherited from the group; credentials are never inferred from a tool request. OpenAPI source inspection, persistence, and refresh are admin operations, with `POST /api/v1/tool-groups/{groupID}/imports/inspect` bounded to the 6 MiB document request and the 5 MiB parsed document limit.

JWKS readiness requires at least one usable public asymmetric verification key: `use` is empty or `sig`; any `key_ops` includes `verify`; and an explicit key `alg` matches a supported RSA, EC, or Ed25519 JWS algorithm declared by `id_token_signing_alg_values_supported`. If discovery omits that list, `RS256` is assumed. Malformed or unsupported keys in the same JWKS do not hide another usable key.

Security-sensitive changes should include a focused behavior-boundary test and explain compatibility or threat-model impact in the pull request. Do not claim a release is secure merely because local tests pass; describe the deployment assumptions and remaining limits.

## 中文

仓库：[SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · 当前文档版本：v2.0.0 · module：`github.com/SamuelSupe/mcphub/v2` · [发行说明](RELEASE_NOTES_v2.0.0.md) · 许可证：[Apache License 2.0](LICENSE)

### 范围

MCPHub 的安全边界包括：

- 公共 MCP Streamable HTTP 入口和 Bearer JWT 验证；
- OIDC discovery/JWKS、JWT `iss`/`aud`/`sub`/`exp`/`nbf`，以及 `scope`/`scp` 处理；
- 后端 URL 校验、静态请求头、OAuth `client_credentials` 和重定向行为；
- backend-local `tool_rules` 和按 tool 的 scope challenge；
- CORS/Origin 检查、RFC 9728 Protected Resource Metadata、就绪/健康端点暴露；
- 配置文件、环境展开、secret 和 SIGHUP 重载行为；
- 可选的本地/远程管理监听器、加密 SQLite/PostgreSQL backend 配置、首次导入和 runtime 替换 API；
- admin 管理的工具组、手工 HTTP tool、OpenAPI 3.0/3.1 import、source 刷新以及这些路径上的响应/body 上限。

当前正式发布版明确不提供 stdio 后端接入、独立旧式 GET SSE 端点、原生 TLS、动态租户或按用户后端凭证、opaque token introspection、Tasks、MCP Apps 或自定义 MCP 扩展。本地模式只允许回环访问，认证远程模式见下文。TLS 终止、外部限流和边缘访问策略必须由部署使用的反向代理或网络层提供。

本版本还提供独立的 `mcphub-cli` 可执行程序，提供 `login/connect/status/logout`，供用户通过外部 OIDC 身份服务登录；服务端程序 `mcphub` 提供 `serve/validate`。只有本地 CLI 连接器使用 stdio；用户 access token 仅发往保存的 HTTP 网关地址，不会发给后端。登录使用公开客户端、PKCE S256、state/issuer 校验和回环回调，只有网关接受认证握手后才保存凭证。Discovery 和 Token 请求必须使用 HTTPS，且不跟随重定向；临时回调监听器仅绑定 `127.0.0.1`。

凭证以未加密 JSON 保存。macOS/Linux 的 `~/.mcphub/` 使用仅限所有者的目录 `0700`、文件 `0600` 权限。Windows 的 `%USERPROFILE%\.mcphub\` 与凭证文件创建时设置受保护的 DACL，仅授权当前用户；打开时校验所有权和访问规则。凭证目录、profile 或锁文件若为 reparse point 或向其他账号授权，会被拒绝访问。凭证不应进入共享存储或公开备份。平台文件锁与临时文件替换串行化 refresh token 轮换。重新登录会让已有连接器失效；退出登录清除本地 Token 并阻止后续请求，但不吊销身份服务 Token、不终止已接受的操作、不退出浏览器会话。`connect` 不发起交互授权；401 仅在刷新后重试一次，网络失败不重放工具操作。stdout 专用于 MCP 协议，状态和诊断不输出 Token。

### 权限工作台与接入诊断

工具策略查看和权限模拟仅供配置管理员使用，普通 MCP 用户和只有审批权限的用户不可访问。模拟输入的 Scope 是假设条件，Grant 按精确 issuer／subject 和当前策略检查；不会签发凭证、创建审批、预览或执行工具，也不占用执行额度。结果只是配置快照，不证明用户实际权限或保证后续执行成功。策略修改沿用版本校验与可选的配置审批。`mcphub-cli doctor` 复用连接器的凭证校验与续期路径，可能保存轮换后的 Token；网络操作仅包括授权状态、MCP 初始化和目录发现，不执行工具或发起交互授权。报告不包含 Token、Grant 凭证或 IPC secret。

### 报告漏洞

疑似漏洞不要在公开 Issue、Discussion 或 Pull Request 中披露。仓库启用 GitHub [私有漏洞报告/Security Advisory 流程](https://github.com/SamuelSupe/mcphub/security/advisories/new) 时请使用该流程；如果不可用，请通过仓库配置的私下安全联系人联系维护者。

在安全的前提下，请提供：

- 受影响的 commit、tag 或部署版本；
- 简洁的影响说明和攻击前提；
- 最小复现或请求/响应记录，并删除 secret；
- 建议的缓解措施或临时方案；
- 问题只影响反向代理部署还是 MCPHub 进程本身。

绝不要提交真实 client secret、API key、Bearer token、私钥、含凭证的生产 URL 或未脱敏个人数据。若 secret 已泄露，应先吊销/轮换，再只提交脱敏证据。

### 部署要求

- `server.public_url`、`auth.issuer` 和远端后端 URL 使用 HTTPS。HTTP 仅在后端 URL 是 loopback 且显式设置 `allow_insecure_http: true` 时接受。
- `server.public_url` 必须作为 JWT `aud` 出现，包括路径：`aud` 为字符串时必须等于 `public_url`，为数组时必须包含 `public_url`。两个 RFC 9728 metadata 地址都应路由到同一受信任 origin。
- 将 `client_secret`、API key、静态 Authorization 和环境文件放在 Git 之外；尽可能只读挂载配置并限制文件权限。
- 使用精确的 HTTPS `allowed_origins`，保持现有预检 header allowlist，预检只允许 `POST`（由 `OPTIONS` 返回预检响应）；不要使用通配符 Origin，也不要比必要范围更广地暴露管理/探针端点。
- `/healthz`、`/readyz` 和 metadata 不需要认证，应在代理或网络层限制可见范围。
- Stateless Streamable HTTP `/mcp` 入口只接受 POST；现代客户端可以在 POST 响应中使用 request-scoped SSE，但 MCPHub 不暴露 standalone GET SSE 或 DELETE session 会话端点。兼容客户端使用同一 `/mcp` POST 语义。
- 不要把 secret 放入后端 ID、能力名称、日志、Issue 或发布产物。静态后端请求头用于数据面请求；OAuth discovery/token 请求会刻意排除这些请求头。
- 工具组、手工 HTTP tool 和 OpenAPI import 只能通过本地或已认证的远程 admin API 的 `/api/v1/tool-groups` 管理，所选数据库是它们的事实来源。不要为这些对象添加 YAML schema，也不要把 SIGHUP 当作迁移机制。
- 每个工具组在其 tool 之间共享一个 HTTPS Base URL、静态 Header 或 OAuth `client_credentials`、JWT required scope 和请求 timeout。HTTP tool 只能作为 MCP capability 通过 `/mcp` 暴露；MCPHub 不得变成 raw HTTP proxy 或任意 method/path 透传。
- 工具组和 OpenAPI source URL 必须使用 HTTPS，抓取器不跟随重定向。跨 origin 的 OpenAPI source 抓取不得发送该组的静态 Header、Bearer 材料或 OAuth client secret。工具组 secret 要在配置数据库中加密保存，并且不进入日志、导出或浏览器可见的 API 字段。
- 工具组、手工 tool 和 import 资源各自使用 `ETag` revision；更新/删除必须提交匹配的 `If-Match`，过期 revision 必须 fail closed 并返回 `409 revision_conflict`。
- HTTP tool 响应默认限制 1 MiB（只允许配置在 64 KiB 至 16 MiB），OpenAPI 文档限制 5 MiB，携带 OpenAPI 文档的请求限制 6 MiB。URL-backed import 默认每 15 分钟刷新（允许 1 分钟至 24 小时）；刷新失败保留 last-known-good 文档和 tools，并采用退避重试。
- SIGHUP 期间，只有 unavailable optional backend 的 backend ID/URL、`allow_insecure_http`、全部固定 header，以及 OAuth 配置是否存在和 `type`、`issuer`、`client_id`、`client_secret`、`scopes` 均未变化时，才可复用上一代内存目录。凭证、OAuth 或 tenant-selection header 变化会阻止复用；`required`、`required_scopes`、`tool_rules`、timeout 等不标识目录来源的字段不会阻止，且复用不会把新 backend 标记为 ready。
- SIGHUP candidate 启动使用可取消的 context；关停开始时仍在连接的 candidate 会被取消。required backend 必须先连接成功，candidate 才能替换当前代际；每个 candidate 或已退役代际都会关闭自己持有的 backend session 和连接。
- Backend ID 可以包含大写，但必须按大小写不敏感规则唯一。tool/prompt 名称保留配置 ID；resource 和 resource-template URI authority 使用小写 ID。
- 未认证的 `mode: local` admin listener 必须保持在配置校验允许的数字回环地址，不能通过反向代理发布。`MCPHUB_CONFIG_KEY` 应与 YAML、SQLite 备份分开保存；已存 Secret 只能由完全相同的 Base64 编码 32 字节密钥恢复。

### 远程管理

`mode: remote` 必须通过可信代理终止 HTTPS，并校验精确的 Host/Origin、独立管理员 JWT audience（`admin.public_url`）及全部管理员 scope。上游 HTTP 监听器必须保持私有；IDP 的 `roles`/`groups` 和代理身份头不会授予权限。浏览器会话使用不透明 Secure/HttpOnly/SameSite cookie、变更请求 CSRF 校验、PKCE S256、绑定浏览器的一次性 state 和授权响应 issuer 校验。令牌仅在服务端内存保存，每个会话串行刷新并保存轮换结果；会话最长 8 小时，重启需重新登录。退出只清理当前浏览器/CLI 会话，不退出 IDP SSO，也不撤销已签发 JWT。

配置审计记录已验证的管理员 subject，不是不可篡改的合规日志。PostgreSQL 保留加密、版本冲突和事务审计语义。使用独立数据库/schema，外部数据库连接使用 verify-full TLS；Compose 示例的非加密数据库连接只在私有容器网络内使用。两种后端目前都支持一个网关实例；切换 driver 不迁移数据。详见[部署边界](deploy/README.zh-CN.md)。

Endpoint 速率和并发限制默认关闭，在认证与 scope 检查后、上游执行前生效。同一配置 ID 的调用者共享额度；取消和退订保持可用。计数在进程内维护，重启重置，不能替代反向代理对未认证流量的防护。

### 写入执行审批

写工具和未分类工具必须取得绑定不可变请求的单次批准。审批人与配置管理员使用独立 scope，并通过精确 subject、资源条件和可选的禁止自审限制授权；列表与详情也检查范围。批准要求审批人浏览器 Cookie 和 CSRF/Origin 校验，本地模式与 Bearer Token 无权批准。强认证策略要求新的、已验签的 OIDC ID Token，校验配置的 ACR、同一 subject、nonce、客户端 audience 及最近 auth_time。证明只绑定一个审批单和会话、短时单次有效；ACR 的实际认证强度必须由身份服务执行。

原调用人恢复保存请求时，网关重新检查权限、代次、限流以及配置的预览/版本。取消和撤销与执行原子竞争，不能回滚已接纳的写入。预览工具必须显式发布并标为只读，写后端必须原子检查版本前提。人工核查只记录证据，不恢复不确定操作的执行额度。SQLite/PostgreSQL 加密保存请求、预览、结果与详细审计，终态详情按配置保留期清理，通用状态事件保留。这不是分布式恰好一次或不可篡改审计保证。

Agent 不应控制审批人浏览器、配置/数据库、加密密钥或上游写凭证。启用 `policy_changes` 后，管理 API 的配置新增/修改需由另一位安全管理员批准；停用和删除可立即阻断访问。YAML、数据库和部署运维仍属于信任边界。加强认证不代表人已经理解具体操作，标为只读也不能证明后端没有副作用；下游仍须最小权限及网络隔离。参见[限制、角色和升级说明](README.zh-CN.md#单次写入审批)。

双人审批由不同的授权 subject 投票，禁止申请人参与；资源风险条件只能提高人数或认证强度。业务操作 ID 绑定身份、工具和不可变参数，终态 ID 的哈希登记不会随详情清理；换用新 ID 或绕过网关的写入仍需上游幂等约束。`mcphub_approval_status` 只读查询不会消费批准，缓存结果和上游状态查询仍受当前权限与配置检查。

可选的独立归档对审批事件使用 Ed25519 签名和哈希链，校验接收方的序号/哈希回执，失败持久化重试并阻止相关详情清理。外部校验方必须独立保存公钥和可信检查点、保护追加式存储，才可发现归档篡改、断链与回滚；有效前缀本身不能证明没有尾部删除。此机制不能防御同时控制签名者和归档的操作者，也不覆盖所有普通活动日志。通知只提供受认证的审批链接，Webhook 回调没有批准权限。

### 应保持的安全行为

后端访问使用 scope all-of 语义：`required_scopes` 的每一项都必须出现在已验证 token 中；JWT `scope` 是空格分隔字符串，`scp` 是字符串或字符串数组。后端请求头会拒绝换行、重复、`Accept`、`Content-Type`、任意 `Mcp-*`、`Proxy-Authorization`、`Proxy-Authenticate` 以及其他 HTTP/MCP transport 管理的名称。OAuth 模式会拒绝静态 `Authorization`。`client_credentials` discovery 只需要 RFC 8414/OIDC metadata 中精确匹配的 issuer 和 token endpoint，不要求交互式 authorization 或 PKCE metadata。OIDC verifier 首次 ready 还要求绝对 HTTPS 的 `jwks_uri`，以及可达的 JWKS 响应中至少包含一个可解析、有效且非对称的公开验证密钥；对称 `oct` 密钥以及无效或空 key 均不满足此条件。OIDC discovery 和 JWKS 响应分别限制为 1 MiB；OAuth metadata 响应同样限制为 1 MiB。OIDC 和后端 HTTP 客户端不跟随重定向。MCP 监听器的所有 HTTP 路由在 request body 被消费或关闭前都保持 `request_timeout` 读取 deadline，包括未认证或被拒绝请求；MCP 处理继续后，普通 MCP POST 随后用同一值设置 response 写入 deadline 和 request context。新版 `subscriptions/listen` POST 在 body 读完后保持长连接，不受普通 response 写入和 request context timeout 限制。日志会重新生成超长或无效 request ID，校验并脱敏能力名/资源 URI 字段，并且只记录外部错误类型。runtime 代际退役时会等待活动请求，然后先取消绑定到该代际的 request context，再关闭 Hub/backend 状态，避免 session 晚到登记；SIGINT/SIGTERM 的 HTTP drain 超时后会强制关闭剩余 HTTP 连接。

后端 SSE 响应是 streaming passthrough：progress 检查每个 event 最多缓存 1 MiB，超大 event 原样转发但跳过 progress 检查。已确认的 2026 resource subscription ID 会把更新映射回原订阅 URI，包括 update event URI 不同的情况；timeout、取消订阅和 session/重连清理会删除映射。现代 `subscriptions/listen` stream 取消或断连时，清理会脱离已取消的 upstream context，但仍受 backend timeout 和 session lifecycle 约束，因此旧协议 backend 仍会收到 `resources/unsubscribe`。runtime 或 client context 取消会让底层 write deadline 到期，因此代际 drain 时会打断慢速或未读取的 subscription write，普通请求仍使用 `request_timeout`。

显式发布策略：`published_tools` 是默认空的精确允许名单，目录发现和 scope 通配规则不能批准新工具；新增手工 HTTP 工具也默认关闭。调用时检查当前发布/启停状态与 scope，再对实际参数应用所有匹配的 `resource_rules`。资源值必须是允许的精确字符串，或非空且全部获准的字符串数组；缺失和类型错误均拒绝。受限参数转发前会规范化，避免重复 JSON 键被不同解析器解释为不同值。这些共享允许范围不代表用户对资源的所有权，也不能代替后端对 SQL、符号链接、别名或其他选择参数的授权。策略变化前已接纳的调用仍可能完成。参见[配置及升级说明](README.zh-CN.md#显式发布与资源范围)。

Backend-local `tool_rules` 使用 Go `path.Match` 针对原始 backend tool name 做整串、区分大小写的匹配。所有匹配规则的 scope 会合并去重，并与 backend 级 `required_scopes` 一起全部满足；未授权 tool 不出现在 `tools/list`，已知 tool 的直接调用返回 403，`WWW-Authenticate` 携带 `error="insufficient_scope"`、路径感知的 `resource_metadata` 和缺失 scope。`match` 与 scope 字符串支持 `${ENV}` 展开；空或无效模式、空/含空白/重复 scope，以及同一 backend 内重复 match 都会被拒绝。

托管 HTTP tool 的边界刻意小于 proxy：手工定义和 OpenAPI import 会转换为命名 MCP tool，只有这些名称能经 `/mcp` 列出或调用。Base URL/Header/OAuth/scope/timeout 从所属工具组继承，不能从 tool 请求中推断凭证。OpenAPI source 的 inspect、持久化和刷新都属于 admin 操作；`POST /api/v1/tool-groups/{groupID}/imports/inspect` 的文档请求受 6 MiB body 上限和 5 MiB 解析后文档上限约束。

JWKS ready 要求至少一个可用的非对称公开验签 key：`use` 为空或为 `sig`；存在 `key_ops` 时必须包含 `verify`；显式 key `alg` 必须匹配 OIDC discovery 的 `id_token_signing_alg_values_supported` 中支持的 RSA、EC 或 Ed25519 JWS 算法。若 discovery 未声明该列表，则按 `RS256`。同一 JWKS 中的坏 key 或不支持 key 不会遮蔽其他可用 key。

安全相关改动应包含聚焦于行为边界的测试，并在 Pull Request 中解释兼容性或威胁模型影响。不要因为本地测试通过就声称发布版本绝对安全，应说明部署假设和剩余限制。

### Guided setup and request diagnostics

`setup` uses the existing browser login and consent paths. Its consent-options API requires a verified MCP token and only reveals eligible published tool names, effects, required scopes and resource constraints. This control-plane disclosure does not unlock strict MCP catalogs or permit calls. Selection is explicit; write/unknown tools require opt-in and still require per-operation approval. Configuration output contains no credentials and does not overwrite application files.

Cross-user grant queries and request diagnostics require configuration-administrator authorization on the admin listener. The owner portal remains subject-scoped. Diagnostics use a bounded 2,000-entry process-memory ring and expose only completed MCP POST metadata from the last 30 minutes. They do not retain arguments, results, resource values, tokens or arbitrary error bodies, and do not replace the audit archive. Subject and client identifiers are personal operational data; restrict administrator access accordingly.

### 接入向导与请求诊断

`setup` 复用既有浏览器登录和同意流程。授权候选目录要求已验证的 MCP Token，仅披露当前身份可申请的已发布工具名称、读写属性、Scope 和资源限制；这项控制面展示不会开放严格模式 MCP 目录或调用权限。工具必须明确选择，写入／未分类工具需单独同意且仍受单次审批限制。生成配置不含凭证，不覆盖客户端文件。

跨用户授权查询和请求诊断仅允许管理监听器上的配置管理员访问，个人门户仍限定当前 Subject。诊断使用容量 2,000 的进程内存环，仅展示最近 30 分钟已完成 MCP POST 的元数据，不保存参数、结果、资源值、Token 或任意错误正文，不能替代审计归档。Subject 和客户端标识属于人员相关运维数据，应限制管理员访问。


## Federated SSO and local identity permissions / 外部 SSO 与本地身份权限

Optional `auth.sso` makes MCPHub a separate issuer. Only configured HTTPS upstreams and pre-registered downstream client/redirect/resource tuples are accepted. Authorization uses S256, browser-bound single-use state, nonce and one-minute single-use codes. OIDC identities require signed ID tokens; OAuth2 uses the configured HTTPS UserInfo response, stable subject mapping, optional success-code and tenant restrictions. Unverified email, proxy headers and upstream role names never assign local privileges. Upstream OAuth2 without verified OIDC evidence cannot satisfy approval MFA.

Local users are isolated by provider connection (issuer, protocol, client ID and subject/tenant mapping) and subject, pending by default. Direct and enabled group/department grants form a union; tool/write/resource predicates must match together within one access entry. Shared tool publication, scope/resource policy, client grants and per-operation write approvals remain additional restrictions. Admin role alone does not grant business tools. Directory sync has a separate secret and strictly accepts identity/active/membership fields, never roles or grants; snapshots replace membership atomically and reject stale versions. Directory-active and locally-enabled states are both required. Claim-only login cannot detect upstream offboarding between logins; directory-mode revocation depends on sync cadence.

Ten-minute signed access tokens also require a live local session and current permissions on each request. Rotating refresh families last at most eight hours; replay of a consumed credential revokes the family. Upstream refresh credentials are not retained or periodically checked. Signing keys are encrypted with the existing configuration key; refresh credentials are hashed. Permission changes cancel affected admitted requests and reject stale cached views, but cannot undo writes already accepted by a backend. Pending login codes are process-local; deployment remains single instance for both database engines. Local identity and membership records contain operational personal data; protect database backups and administrator access. See [deployment contract](docs/sso-and-user-management.md).

开启 `auth.sso` 后，MCPHub 自己签发凭证；上游应用密钥留在服务端，上游 Token 不透传给 MCP 客户端或后端。身份校验与本地授权分离，用户首次登录待授权；引导管理员只在首次创建时赋权，不能在后续登录覆盖管理员的撤权。部门/组同步不能修改本地授权；完整目录快照需先核验所有分页，不能在上游失败时用空快照覆盖。声明模式只在登录时更新成员关系，离职实时性需要目录推送；本实现没有上游全局退出联动。管理员修改角色/用户授权会审计并立即影响新请求；既有工具配置审批与写审批边界继续保留。配置接口是管理员权限，不是审批动作本身。详细配置见[中文说明](docs/sso-and-user-management.zh-CN.md)。
