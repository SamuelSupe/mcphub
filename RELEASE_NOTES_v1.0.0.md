# MCPHub v1.0.0

This is the initial release. / 这是初始版本。

Repository: [SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · module: `github.com/SamuelSupe/mcphub` · license: [Apache License 2.0](LICENSE) · [GitHub release](https://github.com/SamuelSupe/mcphub/releases/tag/v1.0.0)

## English

### Included

- A Go 1.26 MCP aggregation gateway with `serve` and `validate` commands.
- Streamable HTTP connections to multiple MCP backends, paginated catalogs with parallel tools/prompts/resources/resource-template discovery, refresh, reconnect handling, last-known-good catalogs, and required/optional backend readiness.
- Aggregated tools, prompts, resources, resource templates, completion, resource subscriptions, resource updates, and streaming progress forwarding. Backend SSE is passed through unchanged; progress inspection buffers at most 1 MiB per event, and an oversized event is forwarded without progress inspection.
- `ResourceLink`, `EmbeddedResource`, and `ResourceContents` URIs in backend tool, prompt, or resource results are rewritten and recorded as issued resources rather than added individually to the public `resources` catalog. Each backend retains at most 16,384 distinct issued-URI SHA-256 digests; eviction of the oldest digest may make a new read or subscription fail, while existing subscription cancellation/session cleanup still follows the session map. Resource-updated notifications do not create entries. Subscriptions are tracked/deduplicated per upstream MCP session, use shared backend reference counts, ignore unpaired unsubscribe, and restore after reconnect, waiting for `notifications/subscriptions/acknowledged` when the backend protocol supports that acknowledgement before readiness. An acknowledged subscription ID maps updates back to its original subscription URI(s), including when the update event URI differs; timeout, cancellation, and session/reconnect cleanup remove the mapping. A canceled or disconnected modern `subscriptions/listen` stream detaches cleanup from its canceled upstream context but remains bounded by the backend timeout and session lifecycle, so legacy backends still receive `resources/unsubscribe`.
- Compatibility normalization adds missing 2026-07-28 metadata on `notifications/cancelled` for official Go MCP SDK v1.7.0 messages on both Hub ingress and backend egress, keeping the same logical MCP session reusable after cancellation or unsubscribe without advertising a custom extension.
- Backend namespaced routing and deterministic resource/resource-template URI rewriting. Backend IDs may contain uppercase letters but are unique case-insensitively; tool/prompt names retain the configured ID, while resource/template URI authorities use lowercase.
- OIDC discovery/JWKS Bearer JWT verification with exact issuer checks and `aud` validation requiring `server.public_url` as the string value or as an element of the audience array, initial readiness requiring successful discovery, an absolute HTTPS `jwks_uri`, and a reachable JWKS containing at least one parseable, valid, asymmetric public verification key (symmetric `oct` keys and invalid or empty keys are rejected), required `sub`/`exp`, optional `nbf`, 30-second clock skew, and merged claims (`scope` is a space-delimited string; `scp` is a string or string array). OIDC discovery and JWKS responses are each capped at 1 MiB; later refresh failures retain the last-known-good verifier.
- OIDC readiness accepts a JWKS when at least one public asymmetric verification key is usable: `use` is empty or `sig`, any `key_ops` includes `verify`, and an explicit `alg` matches a supported RSA, EC, or Ed25519 JWS algorithm declared by `id_token_signing_alg_values_supported`; if discovery omits that list, `RS256` is assumed. Malformed or unsupported keys in the same JWKS do not hide another usable key.
- Per-backend all-of `required_scopes` filtering and `insufficient_scope` challenges.
- Static backend headers or OAuth 2.0 `client_credentials` with separate token-endpoint transport, RFC 8414/OIDC issuer/token-endpoint discovery (without interactive PKCE metadata), 1 MiB metadata caps, token reuse, and redirect refusal.
- Strict YAML validation, string-only `${NAME}` environment expansion, HTTPS-by-default URL policy, loopback-only explicit HTTP, exact CORS origins and header allowlist with preflight method `POST` (`OPTIONS` response), request-timeout read deadlines for unconsumed bodies on every HTTP route (including unauthenticated/rejected slow bodies), ordinary body/read/write/request timeouts, and the newer `subscriptions/listen` POST long-lived exception, plus rejection of transport-managed headers including `Proxy-Authorization` and `Proxy-Authenticate` and SIGHUP reload with restart-only invariants.
- `/healthz`, `/readyz`, and both RFC 9728 Protected Resource Metadata paths.
- A nonroot distroless Docker image.

### Security and operational notes

- Require `server.public_url` to appear in JWT `aud`, including the MCP path: a string `aud` equals `public_url`, while an audience array contains `public_url`.
- `server.public_url` rejects percent-encoded path characters and reserves `/healthz`, `/readyz`, and `/.well-known/oauth-protected-resource`.
- Use HTTPS for public, issuer, and remote backend URLs. Use `allow_insecure_http: true` only for loopback development backends.
- Keep secrets in the environment or an external secret store; do not commit `config.yaml` or `.env`.
- Route both metadata paths through the trusted reverse proxy and restrict unauthenticated health/readiness/metadata visibility as appropriate.
- The stateless `/mcp` entry accepts POST only; modern clients may use request-scoped SSE in the POST response, while MCPHub provides no standalone GET SSE or DELETE session endpoint. Compatibility clients use the same `/mcp` POST semantics.
- A required backend can make `/readyz` return 503 while the process continues reconnecting. SIGHUP rejects changes to `server.listen`, `server.public_url`, and `auth.issuer`.
- Backend-authored JSON-RPC errors are preserved unchanged; network or transport failures expose only `backend <id> unavailable`, avoiding leakage of internal backend URLs, query strings, or credentials.
- SIGINT/SIGTERM stop new requests, keep the current runtime and backend context during the `drain_timeout` HTTP drain, force-close remaining HTTP connections if that drain times out, and then cancel request contexts bound to the retiring generation before closing its backend state. Runtime cancellation after the drain, or client cancellation at any time, expires the underlying write deadline and interrupts slow or unread subscription writes; ordinary requests retain `request_timeout`.
- All tracked resource subscriptions must restore successfully before a reconnecting backend becomes ready; on protocol versions that support it, each restore also waits for `notifications/subscriptions/acknowledged`; a restore or acknowledgement failure keeps the backend unavailable and triggers another reconnect attempt.
- SIGHUP candidate startup uses a cancelable context; shutdown cancels a candidate that is still connecting. Required backends must connect successfully before the candidate replaces the current generation, and each candidate or retired generation closes its own backend sessions and connections.
- Every HTTP route keeps the `request_timeout` request-body read deadline until the body is consumed or closed, including unauthenticated and rejected requests with slow bodies. A `subscriptions/listen` POST is exempt from ordinary response-write and request-context timeouts only after its body has been read, but runtime/client cancellation can still expire its underlying write deadline.
- On SIGHUP, an unavailable optional backend reuses its previous in-memory catalog only when backend ID/URL, `allow_insecure_http`, every fixed header, and the complete OAuth configuration (including presence, `type`, `issuer`, `client_id`, `client_secret`, and `scopes`) are unchanged. Credential, OAuth, or tenant-selection-header changes block reuse; `required`, `required_scopes`, and timeout changes do not, and reuse never marks the new backend ready.
- Overlong or invalid request IDs are regenerated; capability and resource-URI log fields are validated/sanitized, and request failures record only external error types.

### Explicit limits

This release does not provide stdio, a standalone legacy GET SSE endpoint, native TLS, a database, dynamic tenants or per-user backend credentials, opaque-token introspection, Tasks, MCP Apps, or custom MCP extensions. TLS termination and external rate limiting remain deployment responsibilities.

## 中文

仓库：[SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · module：`github.com/SamuelSupe/mcphub` · 许可证：[Apache License 2.0](LICENSE) · [GitHub release](https://github.com/SamuelSupe/mcphub/releases/tag/v1.0.0)

### 已包含

- Go 1.26 MCP 聚合网关，以及 `serve` 和 `validate` 命令。
- 连接多个 MCP 后端的 Streamable HTTP、目录分页及 tools/prompts/resources/资源模板并行发现、刷新、重连、last-known-good 目录，以及 required/optional 后端就绪语义。
- 聚合 tools、prompts、resources、资源模板、completion、资源订阅、资源更新和 streaming progress 转发。后端 SSE 原样透传；progress 检查每个 event 最多缓存 1 MiB，超大 event 原样转发但跳过 progress 检查。
- 后端 tool、prompt 或 resource 结果中的 `ResourceLink`、`EmbeddedResource`、`ResourceContents` URI 会被改写并记录为已签发资源，不会逐项加入公开 `resources` 目录。每个 backend 最多保留 16,384 个不同的已签发 URI SHA-256 摘要；淘汰最旧摘要后，新 read 或 subscription 可能失败，但已有订阅的取消/session 清理仍按 session 映射处理。resource-updated 通知不会创建目录项。订阅按 upstream MCP session 跟踪/去重，后端引用共享计数，未配对取消订阅会忽略，重连后恢复；backend protocol 支持时还要等待 `notifications/subscriptions/acknowledged`，再允许 ready。确认中的 subscription ID 会把更新映射回原订阅 URI，包括 update event URI 不同的情况；timeout、取消订阅和 session/重连清理会删除映射。现代 `subscriptions/listen` stream 取消或断连时，清理会脱离已取消的 upstream context，但仍受 backend timeout 和 session lifecycle 约束，因此旧协议 backend 仍会收到 `resources/unsubscribe`。
- 针对官方 Go MCP SDK v1.7.0 的 `notifications/cancelled` 消息缺少 2026-07-28 metadata，Hub 入站和后端出站都会做兼容规范化，使取消或取消订阅后的同一逻辑 MCP session 可继续复用；不宣称自定义扩展。
- 后端命名空间路由，以及确定性的资源/资源模板 URI 改写。Backend ID 可以包含大写但按大小写不敏感规则唯一；tool/prompt 名称保留配置 ID，resource/template URI authority 使用小写。
- OIDC discovery/JWKS Bearer JWT 验证：精确 issuer 校验，且 `aud` 必须包含 `server.public_url`（字符串 `aud` 等于该值，audience 数组包含该值）；初次 ready 要求 discovery 成功、`jwks_uri` 为绝对 HTTPS URL，且可达 JWKS 至少包含一个可解析、有效且非对称的公开验证密钥（对称 `oct` 密钥以及无效或空 key 会拒绝），必需 `sub`/`exp`、可选 `nbf`、30 秒时钟偏差，以及合并 claim（`scope` 是空格分隔字符串；`scp` 是字符串或字符串数组）。OIDC discovery 和 JWKS 响应分别限制为 1 MiB；后续刷新失败保留 last-known-good verifier。
- OIDC readiness 接受 JWKS 的条件是至少一个公开非对称验签 key 可用：`use` 为空或为 `sig`，存在 `key_ops` 时包含 `verify`，显式 `alg` 匹配 OIDC discovery 的 `id_token_signing_alg_values_supported` 中支持的 RSA、EC 或 Ed25519 JWS 算法；discovery 未声明该列表时按 `RS256`。同一 JWKS 中的坏 key 或不支持 key 不会遮蔽其他可用 key。
- 每个后端 scope all-of 的 `required_scopes` 过滤和 `insufficient_scope` challenge。
- 静态后端请求头或 OAuth 2.0 `client_credentials`；通过 RFC 8414/OIDC metadata 发现 issuer/token endpoint（不要求交互式 PKCE metadata），metadata 上限 1 MiB，token endpoint 使用独立传输、复用 token 并拒绝重定向。
- 严格 YAML 校验、仅字符串字段的 `${NAME}` 环境展开、默认 HTTPS、仅 loopback 且显式开启的 HTTP、精确 CORS Origin 和 header allowlist（预检方法为 `POST`，由 `OPTIONS` 返回响应）、所有 HTTP 路由对未消费 body 使用 request-timeout 读取 deadline（未认证/被拒绝的慢 body 也有界）、普通 body/read/write/request timeout（新版 `subscriptions/listen` POST 保持长连接例外）、拒绝包括 `Proxy-Authorization` 和 `Proxy-Authenticate` 在内的 transport 管理头，以及带重启不变量的 SIGHUP 重载。
- `/healthz`、`/readyz` 和两个 RFC 9728 Protected Resource Metadata 地址。
- 以 nonroot 用户运行的 distroless Docker 镜像。

### 安全与运维提示

- `server.public_url` 必须出现在 JWT `aud` 中，包括 MCP 路径：`aud` 为字符串时等于 `public_url`，为数组时包含 `public_url`。
- `server.public_url` 会拒绝包含 percent-encoded 字符的路径，并保留 `/healthz`、`/readyz` 和 `/.well-known/oauth-protected-resource`。
- public、issuer 和远端后端 URL 使用 HTTPS；`allow_insecure_http: true` 只用于 loopback 开发后端。
- 将 secret 放在环境变量或外部 secret store，不要提交 `config.yaml` 或 `.env`。
- 两个 metadata 地址都应经过受信任反向代理，并按需限制未认证健康/就绪/metadata 端点的可见范围。
- Stateless `/mcp` 入口只接受 POST；现代客户端可以在 POST 响应中使用 request-scoped SSE，但 MCPHub 不提供 standalone GET SSE 或 DELETE session 会话端点。兼容客户端使用同一 `/mcp` POST 语义。
- required 后端可能让 `/readyz` 返回 503，同时进程继续重连。SIGHUP 会拒绝修改 `server.listen`、`server.public_url` 和 `auth.issuer`。
- 后端产生的 JSON-RPC error 原样保留；网络或 transport 失败对外只返回 `backend <id> unavailable`，避免泄露内部 backend URL、query 或 credential。
- SIGINT/SIGTERM 会停止接收新请求，在 `drain_timeout` 的 HTTP drain 期间保留当前 runtime 与 backend context；HTTP drain 超时后会强制关闭剩余 HTTP 连接，随后先取消绑定到退役代际的 request context，再关闭其 backend 状态。drain 后的 runtime 取消，或任何时刻的 client 取消，都会让底层 write deadline 到期并打断慢速或未读取的 subscription write；普通请求仍使用 `request_timeout`。
- 后端重连时，所有 tracked resource subscription 必须恢复成功后才会 ready；backend protocol 支持时，每项恢复还要等待 `notifications/subscriptions/acknowledged`；恢复或确认失败都会保持 unavailable 并触发后续重连。
- SIGHUP candidate 启动使用可取消的 context；关停开始时仍在连接的 candidate 会被取消。required backend 必须先连接成功，candidate 才能替换当前代际；每个 candidate 或已退役代际都会关闭自己持有的 backend session 和连接。
- 所有 HTTP 路由在 request body 被消费或关闭前都保持 `request_timeout` 读取 deadline，包括未认证或被拒绝的慢 body。`subscriptions/listen` POST 只有在 body 读完后才不受普通 response 写入和 request context timeout 限制，但 runtime/client 取消仍可让底层 write deadline 到期。
- SIGHUP 时，只有 unavailable optional backend 的 backend ID/URL、`allow_insecure_http`、全部固定 header，以及完整 OAuth 配置（包括是否存在、`type`、`issuer`、`client_id`、`client_secret`、`scopes`）均未变化时，才复用上一代内存目录。凭证、OAuth 或 tenant-selection header 变化会阻止复用；`required`、`required_scopes`、timeout 变化不会，且复用不会把新 backend 标记为 ready。
- 超长或无效 request ID 会重新生成；能力名和资源 URI 日志字段会校验/脱敏，请求失败只记录外部错误类型。

### 明确限制

本版本不提供 stdio、独立旧式 GET SSE 端点、原生 TLS、数据库、动态租户或按用户后端凭证、opaque token introspection、Tasks、MCP Apps 或自定义 MCP 扩展。TLS 终止和外部限流仍由部署负责。
