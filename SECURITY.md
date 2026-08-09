# Security Policy / 安全策略

[English](#english) | [中文](#中文)

## English

Repository: [SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · documented release: v1.2.0 · module: `github.com/SamuelSupe/mcphub` · [release notes](RELEASE_NOTES_v1.2.0.md) · license: [Apache License 2.0](LICENSE)

### Scope

MCPHub's security boundary includes:

- the public MCP Streamable HTTP endpoint and Bearer JWT verification;
- OIDC discovery/JWKS, JWT `iss`/`aud`/`sub`/`exp`/`nbf`, and `scope`/`scp` handling;
- backend URL validation, static headers, OAuth `client_credentials`, and redirect behavior;
- backend-local `tool_rules` and per-tool scope challenges;
- CORS/origin checks, RFC 9728 Protected Resource Metadata, readiness/health exposure;
- configuration files, environment expansion, secrets, and SIGHUP reload behavior;
- the optional v1.2.0 loopback administration listener, encrypted SQLite backend configuration, bootstrap import, and runtime replacement API;
- admin-managed tool groups, hand-authored HTTP tools, OpenAPI 3.0/3.1 imports, source refresh, and the response/body limits on those paths.

The current published release explicitly does not provide stdio, a standalone legacy GET SSE endpoint, native TLS, dynamic tenants or per-user backend credentials, opaque-token introspection, Tasks, MCP Apps, or custom MCP extensions. The v1.2.0 development admin platform has no user login or remote-access mode. TLS termination, external rate limiting, and edge access policy must be supplied by the deployment's reverse proxy or network layer.

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
- Manage tool groups, manual HTTP tools, and OpenAPI imports only through the loopback admin API under `/api/v1/tool-groups`; SQLite is their source of truth. Do not add a YAML schema for these resources or treat SIGHUP as a migration mechanism.
- A tool group shares one HTTPS base URL, static headers or OAuth `client_credentials`, JWT required scopes, and request timeout across its tools. Its HTTP tools are exposed only as MCP capabilities through `/mcp`; MCPHub must not become a raw HTTP proxy or arbitrary method/path passthrough.
- Group and OpenAPI source URLs must use HTTPS and fetchers do not follow redirects. A cross-origin OpenAPI source fetch must not send the group's static headers, bearer material, or OAuth client secret. Keep group secrets encrypted in SQLite and outside logs, exports, and browser-visible API fields.
- Tool-group, manual-tool, and import resources each use `ETag` revisions; update/delete requests must supply the matching `If-Match`, and stale revisions must fail closed with `409 revision_conflict`.
- Enforce a 1 MiB default HTTP-tool response limit (configurable only from 64 KiB through 16 MiB), a 5 MiB OpenAPI document limit, and a 6 MiB request limit for bodies carrying an OpenAPI document. URL-backed imports refresh every 15 minutes by default (allowed range 1 minute to 24 hours); failed refreshes retain the last-known-good document/tools and retry with backoff.
- During SIGHUP, an unavailable optional backend may reuse a previous in-memory catalog only when its backend ID/URL, `allow_insecure_http`, every fixed header, and OAuth configuration presence plus `type`, `issuer`, `client_id`, `client_secret`, and `scopes` are unchanged. Credential, OAuth, or tenant-selection-header changes prevent reuse; non-identity fields such as `required`, `required_scopes`, `tool_rules`, and timeouts do not, and reuse never marks the new backend ready.
- SIGHUP candidate startup uses a cancelable context; shutdown cancels a candidate that is still connecting. Required backends must connect successfully before the candidate replaces the current generation, and each candidate or retired generation closes its own backend sessions and connections.
- Backend IDs may contain uppercase letters but must be unique case-insensitively. Tool and prompt names preserve the configured ID; resource and resource-template URI authorities use the lowercase ID.
- Keep the unauthenticated admin listener on its validated numeric loopback address and never publish it through a reverse proxy. Store `MCPHUB_CONFIG_KEY` separately from YAML and SQLite backups; stored secrets require the exact same Base64-encoded 32-byte key for recovery.

### Security behavior to preserve

Backend access uses all-of scope semantics: every entry in `required_scopes` must be present in the verified token; JWT `scope` is a space-delimited string, while `scp` is a string or string array. Backend headers reject line breaks, duplicates, `Accept`, `Content-Type`, any `Mcp-*`, `Proxy-Authorization`, `Proxy-Authenticate`, and other HTTP/MCP transport-managed names. OAuth mode rejects a static `Authorization` header. `client_credentials` discovery needs only an exact issuer and token endpoint from RFC 8414/OIDC metadata, not interactive authorization or PKCE metadata. Initial OIDC readiness also requires an absolute HTTPS `jwks_uri` and a reachable JWKS response containing at least one parseable, valid, asymmetric public verification key; symmetric `oct` keys and invalid or empty keys do not satisfy this condition. OIDC discovery and JWKS responses are each capped at 1 MiB; OAuth metadata responses have the same cap. OIDC and backend HTTP clients do not follow redirects. Every HTTP route keeps the `request_timeout` request-body read deadline until the body is consumed or closed, including unauthenticated and rejected requests; once MCP handling proceeds, ordinary MCP POSTs set response-write deadlines and request contexts from the same value. The newer `subscriptions/listen` POST remains long-lived after its body is read and bypasses those ordinary response-write and request-context timeouts. Logs regenerate overlong or invalid request IDs, validate or sanitize capability/resource-URI fields, and record only external error types. A retiring runtime generation waits for its active requests, then cancels request contexts bound to that generation before closing Hub/backend state; this prevents late session registration. SIGINT/SIGTERM force-close remaining HTTP connections when the `drain_timeout` HTTP drain expires.

Backend SSE responses are streaming passthrough: progress inspection buffers at most 1 MiB per event, and oversized events are forwarded unchanged without progress inspection. Acknowledged 2026 resource subscription IDs map updates back to their original subscription URI(s), including when the update event URI differs; timeout, cancellation, and session/reconnect cleanup remove the mapping. A canceled or disconnected modern `subscriptions/listen` stream detaches cleanup from the canceled upstream context but remains bounded by the backend timeout and session lifecycle, so legacy backends still receive `resources/unsubscribe`. Runtime or client context cancellation expires the underlying write deadline, so slow or unread subscription writes are interrupted at generation drain while ordinary requests retain `request_timeout`.

Backend-local `tool_rules` match original backend tool names with full-string, case-sensitive Go `path.Match` semantics. Matching rules union and deduplicate scopes, and all resulting scopes plus backend-level `required_scopes` are required; unauthorized tools are omitted from `tools/list`, while known direct calls return 403 with `WWW-Authenticate` carrying `error="insufficient_scope"`, the path-aware `resource_metadata`, and the missing scopes. `${ENV}` expansion applies to match and scope strings; empty or invalid patterns, empty/whitespace/duplicate scope entries, and duplicate matches within one backend are rejected.

The managed HTTP-tool boundary is intentionally narrower than a proxy: manual definitions and OpenAPI imports are converted into named MCP tools, and only those names can be listed or invoked through `/mcp`. Base URL/header/OAuth/scope/timeout settings are inherited from the group; credentials are never inferred from a tool request. OpenAPI source inspection, persistence, and refresh are admin operations, with `POST /api/v1/tool-groups/{groupID}/imports/inspect` bounded to the 6 MiB document request and the 5 MiB parsed document limit.

JWKS readiness requires at least one usable public asymmetric verification key: `use` is empty or `sig`; any `key_ops` includes `verify`; and an explicit key `alg` matches a supported RSA, EC, or Ed25519 JWS algorithm declared by `id_token_signing_alg_values_supported`. If discovery omits that list, `RS256` is assumed. Malformed or unsupported keys in the same JWKS do not hide another usable key.

Security-sensitive changes should include a focused behavior-boundary test and explain compatibility or threat-model impact in the pull request. Do not claim a release is secure merely because local tests pass; describe the deployment assumptions and remaining limits.

## 中文

仓库：[SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · 当前文档版本：v1.2.0 · module：`github.com/SamuelSupe/mcphub` · [发行说明](RELEASE_NOTES_v1.2.0.md) · 许可证：[Apache License 2.0](LICENSE)

### 范围

MCPHub 的安全边界包括：

- 公共 MCP Streamable HTTP 入口和 Bearer JWT 验证；
- OIDC discovery/JWKS、JWT `iss`/`aud`/`sub`/`exp`/`nbf`，以及 `scope`/`scp` 处理；
- 后端 URL 校验、静态请求头、OAuth `client_credentials` 和重定向行为；
- backend-local `tool_rules` 和按 tool 的 scope challenge；
- CORS/Origin 检查、RFC 9728 Protected Resource Metadata、就绪/健康端点暴露；
- 配置文件、环境展开、secret 和 SIGHUP 重载行为；
- 可选的 v1.2.0 回环管理监听器、加密 SQLite backend 配置、首次导入和 runtime 替换 API；
- admin 管理的工具组、手工 HTTP tool、OpenAPI 3.0/3.1 import、source 刷新以及这些路径上的响应/body 上限。

当前正式发布版明确不提供 stdio、独立旧式 GET SSE 端点、原生 TLS、动态租户或按用户后端凭证、opaque token introspection、Tasks、MCP Apps 或自定义 MCP 扩展。v1.2.0 开发版管理平台没有用户登录或远程访问模式。TLS 终止、外部限流和边缘访问策略必须由部署使用的反向代理或网络层提供。

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
- 工具组、手工 HTTP tool 和 OpenAPI import 只能通过回环 admin API 的 `/api/v1/tool-groups` 管理，SQLite 是它们的事实来源。不要为这些对象添加 YAML schema，也不要把 SIGHUP 当作迁移机制。
- 每个工具组在其 tool 之间共享一个 HTTPS Base URL、静态 Header 或 OAuth `client_credentials`、JWT required scope 和请求 timeout。HTTP tool 只能作为 MCP capability 通过 `/mcp` 暴露；MCPHub 不得变成 raw HTTP proxy 或任意 method/path 透传。
- 工具组和 OpenAPI source URL 必须使用 HTTPS，抓取器不跟随重定向。跨 origin 的 OpenAPI source 抓取不得发送该组的静态 Header、Bearer 材料或 OAuth client secret。工具组 secret 要在 SQLite 中加密保存，并且不进入日志、导出或浏览器可见的 API 字段。
- 工具组、手工 tool 和 import 资源各自使用 `ETag` revision；更新/删除必须提交匹配的 `If-Match`，过期 revision 必须 fail closed 并返回 `409 revision_conflict`。
- HTTP tool 响应默认限制 1 MiB（只允许配置在 64 KiB 至 16 MiB），OpenAPI 文档限制 5 MiB，携带 OpenAPI 文档的请求限制 6 MiB。URL-backed import 默认每 15 分钟刷新（允许 1 分钟至 24 小时）；刷新失败保留 last-known-good 文档和 tools，并采用退避重试。
- SIGHUP 期间，只有 unavailable optional backend 的 backend ID/URL、`allow_insecure_http`、全部固定 header，以及 OAuth 配置是否存在和 `type`、`issuer`、`client_id`、`client_secret`、`scopes` 均未变化时，才可复用上一代内存目录。凭证、OAuth 或 tenant-selection header 变化会阻止复用；`required`、`required_scopes`、`tool_rules`、timeout 等不标识目录来源的字段不会阻止，且复用不会把新 backend 标记为 ready。
- SIGHUP candidate 启动使用可取消的 context；关停开始时仍在连接的 candidate 会被取消。required backend 必须先连接成功，candidate 才能替换当前代际；每个 candidate 或已退役代际都会关闭自己持有的 backend session 和连接。
- Backend ID 可以包含大写，但必须按大小写不敏感规则唯一。tool/prompt 名称保留配置 ID；resource 和 resource-template URI authority 使用小写 ID。
- 未认证 admin listener 必须保持在配置校验允许的数字回环地址，不能通过反向代理发布。`MCPHUB_CONFIG_KEY` 应与 YAML、SQLite 备份分开保存；已存 Secret 只能由完全相同的 Base64 编码 32 字节密钥恢复。

### 应保持的安全行为

后端访问使用 scope all-of 语义：`required_scopes` 的每一项都必须出现在已验证 token 中；JWT `scope` 是空格分隔字符串，`scp` 是字符串或字符串数组。后端请求头会拒绝换行、重复、`Accept`、`Content-Type`、任意 `Mcp-*`、`Proxy-Authorization`、`Proxy-Authenticate` 以及其他 HTTP/MCP transport 管理的名称。OAuth 模式会拒绝静态 `Authorization`。`client_credentials` discovery 只需要 RFC 8414/OIDC metadata 中精确匹配的 issuer 和 token endpoint，不要求交互式 authorization 或 PKCE metadata。OIDC verifier 首次 ready 还要求绝对 HTTPS 的 `jwks_uri`，以及可达的 JWKS 响应中至少包含一个可解析、有效且非对称的公开验证密钥；对称 `oct` 密钥以及无效或空 key 均不满足此条件。OIDC discovery 和 JWKS 响应分别限制为 1 MiB；OAuth metadata 响应同样限制为 1 MiB。OIDC 和后端 HTTP 客户端不跟随重定向。所有 HTTP 路由在 request body 被消费或关闭前都保持 `request_timeout` 读取 deadline，包括未认证或被拒绝请求；MCP 处理继续后，普通 MCP POST 随后用同一值设置 response 写入 deadline 和 request context。新版 `subscriptions/listen` POST 在 body 读完后保持长连接，不受普通 response 写入和 request context timeout 限制。日志会重新生成超长或无效 request ID，校验并脱敏能力名/资源 URI 字段，并且只记录外部错误类型。runtime 代际退役时会等待活动请求，然后先取消绑定到该代际的 request context，再关闭 Hub/backend 状态，避免 session 晚到登记；SIGINT/SIGTERM 的 HTTP drain 超时后会强制关闭剩余 HTTP 连接。

后端 SSE 响应是 streaming passthrough：progress 检查每个 event 最多缓存 1 MiB，超大 event 原样转发但跳过 progress 检查。已确认的 2026 resource subscription ID 会把更新映射回原订阅 URI，包括 update event URI 不同的情况；timeout、取消订阅和 session/重连清理会删除映射。现代 `subscriptions/listen` stream 取消或断连时，清理会脱离已取消的 upstream context，但仍受 backend timeout 和 session lifecycle 约束，因此旧协议 backend 仍会收到 `resources/unsubscribe`。runtime 或 client context 取消会让底层 write deadline 到期，因此代际 drain 时会打断慢速或未读取的 subscription write，普通请求仍使用 `request_timeout`。

Backend-local `tool_rules` 使用 Go `path.Match` 针对原始 backend tool name 做整串、区分大小写的匹配。所有匹配规则的 scope 会合并去重，并与 backend 级 `required_scopes` 一起全部满足；未授权 tool 不出现在 `tools/list`，已知 tool 的直接调用返回 403，`WWW-Authenticate` 携带 `error="insufficient_scope"`、路径感知的 `resource_metadata` 和缺失 scope。`match` 与 scope 字符串支持 `${ENV}` 展开；空或无效模式、空/含空白/重复 scope，以及同一 backend 内重复 match 都会被拒绝。

托管 HTTP tool 的边界刻意小于 proxy：手工定义和 OpenAPI import 会转换为命名 MCP tool，只有这些名称能经 `/mcp` 列出或调用。Base URL/Header/OAuth/scope/timeout 从所属工具组继承，不能从 tool 请求中推断凭证。OpenAPI source 的 inspect、持久化和刷新都属于 admin 操作；`POST /api/v1/tool-groups/{groupID}/imports/inspect` 的文档请求受 6 MiB body 上限和 5 MiB 解析后文档上限约束。

JWKS ready 要求至少一个可用的非对称公开验签 key：`use` 为空或为 `sig`；存在 `key_ops` 时必须包含 `verify`；显式 key `alg` 必须匹配 OIDC discovery 的 `id_token_signing_alg_values_supported` 中支持的 RSA、EC 或 Ed25519 JWS 算法。若 discovery 未声明该列表，则按 `RS256`。同一 JWKS 中的坏 key 或不支持 key 不会遮蔽其他可用 key。

安全相关改动应包含聚焦于行为边界的测试，并在 Pull Request 中解释兼容性或威胁模型影响。不要因为本地测试通过就声称发布版本绝对安全，应说明部署假设和剩余限制。
