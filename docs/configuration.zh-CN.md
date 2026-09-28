# 配置与协议参考

[English](configuration.md) · [文档导航](README.zh-CN.md) · [管理员手册](admin-guide.zh-CN.md)

本页用于查阅 YAML 字段、工具策略、管理 API 和网关协议细节。首次部署从[管理员手册](admin-guide.zh-CN.md)开始，日常接入从[用户手册](user-guide.zh-CN.md)开始。

- [server](#server)、[auth](#auth)、[admin](#admin)、[backends](#backends)
- [工具组与 HTTP API](#工具组与托管-http-api-tool)、[限流](#endpoint-限流)、[发布与资源范围](#显式发布与资源范围)
- [写审批](#单次写入审批)、[配置治理与双人审批](#配置治理双人审批与业务幂等)、[通知和审计归档](#审批通知与独立审计归档)
- [其他配置专题](#其他配置专题)、[完整 YAML 示例启动](#从完整-yaml-示例启动)
- [能力与边界](#能力与边界)、[HTTP 端点](#http-端点与-rfc-9728)、[名称与 URI](#命名与-uri-映射)、[热重载与关停](#sighup-热重载与关停)、[客户端凭证边界](#客户端凭证与连接边界)

配置是单个 YAML 文档，解码使用严格字段检查；未知字段、多文档 YAML、缺失环境变量都会被拒绝。字符串配置项中的 `${NAME}` 占位符会从当前进程环境展开，`NAME` 必须匹配 `[A-Za-z_][A-Za-z0-9_]*`；没有默认值语法。duration、整数和布尔字段不支持占位符。配置加载和 SIGHUP 重载都会重新展开环境变量。管理数据库完成初始化后，YAML backends 及其环境变量占位符会被忽略。

## `server`

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | `:8080` | HTTP 监听地址。SIGHUP 不可修改，修改后需重启。 |
| `public_url` | 无 | 必填的绝对 HTTPS URL，必须包含 MCP 路径（例如 `https://hub.example.com/mcp`），不能有 query 或 fragment。路径不能含 percent-encoded 字符，也不能是 `/healthz`、`/readyz` 或 `/.well-known/oauth-protected-resource`。它既是 MCP 地址，也是 JWT 的 audience。SIGHUP 不可修改。 |
| `page_size` | `1000` | 聚合 MCP 目录分页大小，必须大于 0。 |
| `request_timeout` | `60s` | 普通 MCP 请求和后端调用的默认超时；必须大于 0。MCP 监听器的所有 HTTP 路由在 request body 被消费或关闭前都使用该值作为读取 deadline，未认证或被拒绝请求的慢 body 也会有界结束。MCP 处理继续后，普通 MCP POST 还会用它设置 response 写入 deadline 和 request context；新版 `subscriptions/listen` POST 在 body 读完后保持长连接，不使用普通的 response 写入和 request context timeout。但 runtime 或 client context 取消仍会让底层 write deadline 立即到期，因此代际 drain 或 client 断开时，慢 subscription write 会被打断。 |
| `drain_timeout` | `15s` | SIGTERM/SIGINT 关停，以及 SIGHUP 替换旧运行时等待活动请求的最长时间；必须大于 0。 |
| `refresh_interval` | `5m` | 后端目录刷新的最大间隔；后端返回更短 TTL 时会提前刷新，最终间隔不会低于 5 秒。必须大于 0。 |
| `catalog_ttl` | `30s` | 对 MCP 目录/发现结果声明的 private TTL；允许为 0，但不能为负数。 |
| `max_request_body_bytes` | `4194304`（4 MiB） | MCP POST body 上限，超出返回 413；必须大于 0。 |
| `allowed_origins` | `[]` | 额外允许的浏览器 HTTPS Origin。每项只能是 `https://authority`，不允许路径、query、fragment、通配符；MCPHub 自身 `public_url` 的 origin 自动允许。 |

duration 使用 Go `time.ParseDuration` 语法，例如 `500ms`、`60s`、`5m`。`public_url` 的路径就是 MCP 入口路径；上例入口为 `/mcp`。路径中的 percent-encoded 字符会被拒绝，`/healthz`、`/readyz` 和 `/.well-known/oauth-protected-resource` 是保留路径。

## `auth`

| 字段 | 说明 |
| --- | --- |
| `issuer` | 必填的绝对 HTTPS OIDC issuer。MCPHub 从该地址 discovery（通常是 `/.well-known/openid-configuration`）并读取 JWKS；SIGHUP 不可修改。 |

JWT 必须满足以下条件：签名和 `iss` 由该 issuer 验证；`aud` 必须包含完整的 `server.public_url`（包含路径）——`aud` 为字符串时必须等于 `public_url`，为数组时必须包含 `public_url`；必须有非空 `sub` 和 `exp`；`nbf`（如有）也会校验。过期、未生效和 OIDC 时间比较允许 30 秒时钟偏差。只有在 OIDC discovery 成功、`jwks_uri` 是绝对 HTTPS URL，且可达的 JWKS 响应至少包含一个可解析、有效且非对称的公开验证密钥后，verifier 才会 ready；对称 `oct` 密钥以及无效或空 key 均不满足此条件。首次成功刷新前 MCP 入口返回 503；ready 后 discovery 或 JWKS 刷新暂时失败会保留 last-known-good verifier。OIDC discovery 和 JWKS 响应分别限制为 1 MiB。

JWKS ready 还要求至少一个可用 key：`use` 为空或为 `sig`；存在 `key_ops` 时必须包含 `verify`；显式 `alg` 必须匹配 OIDC discovery 宣告的支持 RSA、EC 或 Ed25519 JWS 算法。若 discovery 未宣告 `id_token_signing_alg_values_supported`，则按 `RS256`。同一 JWKS 中的坏 key 或不支持 key 不会遮蔽其他可用 key。

scope 取自 JWT 的 `scope` 和 `scp` 两个 claim：`scope` 只接受空格分隔字符串（包括空字符串或 JSON `null`）；`scp` 接受空格分隔字符串或字符串数组。两个 claim 的值会合并、去重；数组项不能包含空白。后端访问采用 **all-of** 语义：`required_scopes: [a, b]` 要求 token 同时拥有 `a` 和 `b`；缺任一项，该后端不会出现在该 token 的目录视图中，对已识别的直接调用返回 403 `insufficient_scope`。未配置 `required_scopes` 的后端不受 scope 限制。Protected Resource Metadata 的 `scopes_supported` 是所有后端 required scope 的去重并集。

## `admin`

管理平台默认关闭。启用后，它通过独立监听器提供嵌入式 UI 和 JSON API；默认本地模式仅回环访问，远程模式必须通过 OIDC 管理员认证。它管理 backend 和工具组配置；`server`、`auth` 和 `admin` 仍由 YAML 管理并需要重启才能修改。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 启用管理平台，并让所选数据库成为配置事实来源。 |
| `mode` | `local` | `local` 仅本机、无登录；`remote` 开启 OIDC 管理员认证。 |
| `listen` | `127.0.0.1:8081` | 本地模式必须是数字回环地址；远程模式可监听私有地址，外部需 HTTPS 代理。 |
| `public_url` | 无 | 远程模式必填，HTTPS origin，不带路径或尾部 `/`；同时作为管理员 JWT audience。 |
| `client_id` | 无 | 远程浏览器 OAuth 客户端 ID。 |
| `client_secret_env` | 无 | 可选机密客户端的 secret 环境变量名；默认使用公开客户端。 |
| `required_scopes` | `[mcphub:admin]` | 远程管理所需的全部 scope，不允许空列表。 |
| `database_driver` | `sqlite` | `sqlite` 或 `postgres`。 |
| `database_dsn_env` | `MCPHUB_DATABASE_URL` | PostgreSQL 连接串所在的环境变量；使用 PostgreSQL 时不能同时设置 `database_path`。 |
| `database_path` | 无 | SQLite 启用时必填；相对路径以 YAML 文件所在目录解析。 |
| `encryption_key_env` | `MCPHUB_CONFIG_KEY` | 保存 Base64 编码 32 字节 AES 密钥的环境变量名；丢失或改变密钥会导致已存 Secret 无法解密。 |
| `request_retention` | `720h`（30 天） | 已完成 MCP POST 请求历史的保留期，范围 `24h`–`8760h`；到期记录自动清理。诊断与导出见[管理员手册](admin-guide.zh-CN.md#授权与请求诊断)。 |

空数据库首次启动时，MCPHub 会在一个事务中导入展开后的 YAML backends。bootstrap 标记写入后，所选数据库成为唯一 backend 来源，之后修改 YAML backend 不再生效。Header 值和 OAuth client secret 使用 AES-256-GCM 加密，管理 API 永不返回明文。

默认 UI 地址为 `http://127.0.0.1:8081/`，可以在不中断进程的情况下注册、测试、编辑、启停和删除后端。Required 后端连接失败时变更会被拒绝，当前 runtime 不受影响；optional 后端不可用时可以保存，并在后台持续重连。

JSON API 位于 `/api/v1`。单项 backend 响应携带 `ETag`；更新和删除必须通过 `If-Match` 提交该 revision，过期写入返回 `409 revision_conflict`。Secret 字段只返回是否已配置；编辑时省略 Secret 值表示保留，省略对应 Header 或 OAuth 配置表示删除。审计 actor 为远程管理员 JWT `sub`、本地 `local` 或后台刷新 `system`，只记录脱敏结果。

### 工具组与托管 HTTP API tool

工具组是管理 API 中的对象，不是 YAML 配置。每个组统一持有其 tool 使用的 HTTPS Base URL、静态 Header 或 OAuth 2.0 `client_credentials`、JWT required scope 和请求 timeout；不要在每个 tool 中重复配置凭证。Header 值和 OAuth client secret 加密存储在所选数据库，API 只返回“已配置/未配置”标记。工具组 scope 仍采用 backend 相同的 all-of 语义；可选的组内 tool 规则可以为选定 tool 追加 scope。

一个工具组可以包含手工定义的 HTTP tool，以及多个 OpenAPI 3.0 或 3.1 import。OpenAPI import 可以先 inspect 再保存。两类对象都只作为 MCP capability，经配置的 `/mcp` Streamable HTTP 入口列出和调用；MCPHub 不提供 raw HTTP proxy 或任意 method/path 透传路由。

稳定的管理路径如下：

| 操作 | 路径 |
| --- | --- |
| 列出/创建工具组 | `GET/POST /api/v1/tool-groups` |
| 读取/更新/删除工具组；探测工具组 | `GET/PUT/DELETE /api/v1/tool-groups/{groupID}`、`POST .../{groupID}/probe` |
| 列出/创建或读取/更新/删除手工 tool | `GET/POST .../{groupID}/tools`、`GET/PUT/DELETE .../{groupID}/tools/{toolName}` |
| inspect、列出/创建或读取/更新/删除 OpenAPI import | `POST .../{groupID}/imports/inspect`、`GET/POST .../{groupID}/imports`、`GET/PUT/DELETE .../{groupID}/imports/{importID}` |
| 刷新 OpenAPI import | `POST .../{groupID}/imports/{importID}/refresh` |

工具组、手工 tool 和 import 资源各自返回 `ETag`；更新和删除必须提交匹配的 `If-Match`，revision 过期时返回 `409 revision_conflict`。这些资源只能通过 admin API 管理并持久化到所选数据库；有意不提供 `tool_groups`（或同类）YAML schema，SIGHUP 也不会导入它们。

工具组 Base URL 和 OpenAPI source URL 必须使用 HTTPS；工具组 HTTP 请求和 source 抓取都不跟随重定向。若 source 与工具组是不同 origin，抓取时绝不会发送该组的静态 Header 或 OAuth secret。OpenAPI 文档上限为 5 MiB，携带文档的请求 body 上限为 6 MiB，HTTP tool 响应默认上限为 1 MiB；响应上限可配置为 64 KiB 至 16 MiB。URL-backed import 默认每 15 分钟自动刷新（可设为 1 分钟至 24 小时）；刷新失败时保留 last-known-good 文档和 tools，并采用退避重试。

## `backends`

YAML-only 模式至少配置一个后端；管理模式允许从空数据库启动并在网页注册第一个后端。每个 `id` 必须匹配 `[A-Za-z0-9_-]{1,32}`，并按大小写不敏感规则保持唯一；允许大写字母。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `id` | 无 | tool/prompt 名称使用的对外命名空间和配置 ID；不能包含点号。名称保留大写，而 resource/template URI authority 使用小写 ID。 |
| `url` | 无 | 必填绝对 URL。默认只接受 HTTPS；仅当 `allow_insecure_http: true` 且主机是 `localhost`、IPv4/IPv6 loopback 时才允许 HTTP。 |
| `required` | `false` | required 后端影响 `/readyz`。运行中断线会使就绪变为 503；连接循环会继续重试。 |
| `required_scopes` | `[]` | 该后端所需的 JWT scope，按 all-of 判断；scope 不能含空白，也不能重复。 |
| `published_tools` | `[]` | 获准发布的原始工具名，精确匹配并区分大小写，不支持通配符；留空不发布任何工具。 |
| `tool_rules` | `[]` | 可选的后端本地 tool 策略。每条规则包含 `match` glob，以及 `effect`、`approval`、`required_scopes`、`resource_rules` 中至少一项。`effect: read` 可直接执行；`write` 或未分类需要审批。匹配基于原始后端 tool name，使用 Go `path.Match`，整串且区分大小写。 |
| `request_timeout` | 继承 `server.request_timeout` | 该后端连接、目录发现、刷新和调用的超时；必须大于 0。 |
| `rate_limit` | `{}` | Endpoint 共享速率、突发容量和并发限制；默认不限流。 |
| `allow_insecure_http` | `false` | 仅为 loopback 本地 HTTP 开关；不会放宽 `server.public_url` 或任何 issuer 的 HTTPS 要求。 |
| `headers` | `{}` | 每次后端 MCP HTTP 请求附加的静态头。值支持环境变量展开，不能含 CR/LF；名称大小写不敏感且不能重复。`Accept`、`Content-Type`、任意 `Mcp-*` 头，以及 `Host`、`Content-Length`、`Connection`、`Proxy-Authorization`、`Proxy-Authenticate` 等均由 HTTP/MCP transport 管理并被拒绝。 |
| `oauth` | 无 | 后端 OAuth 配置；目前唯一允许的 `type` 是 `client_credentials`。与静态 `Authorization` 头互斥。 |

`oauth` 字段如下：

| 字段 | 说明 |
| --- | --- |
| `type` | 必须为 `client_credentials`。 |
| `issuer` | 必填绝对 HTTPS OAuth issuer；metadata 的 issuer 必须与它精确一致。 |
| `client_id` / `client_secret` | 必填，建议只通过 `${...}` 环境变量提供。 |
| `scopes` | 发给后端 OAuth token endpoint 的 scope 列表；它与 `required_scopes`（验证进入 MCPHub 的 JWT）是两套独立的 scope。 |

后端 OAuth discovery 和 token 请求不会带上该后端的静态 headers；数据面请求才会附加 headers 并自动复用/刷新 client-credentials token。Discovery 只从 RFC 8414/OIDC metadata 读取并精确校验 `issuer` 和 `token_endpoint`，不要求交互式 authorization 或 PKCE metadata。OAuth metadata 响应上限为 1 MiB。后端和 OIDC HTTP 客户端都不跟随重定向。

### Endpoint 限流

MCP backend 和 HTTP 工具组均支持 `rate_limit`，默认不限流。按配置 ID 独立计数，所有用户共享额度；同一 HTTP 工具组中的手工接口和 OpenAPI tools 共用额度。管理 UI 的“限流策略”可直接编辑，管理 API 的 backend/tool-group 输入和响应也包含该对象。

```yaml
# 添加到某个 backends 条目；HTTP 工具组通过管理 UI/API 配置。
rate_limit:
  requests_per_second: 20
  burst: 40
  max_concurrent: 8
```

- `requests_per_second`：平均每秒接纳数，支持小数；`0` 或省略表示不限速率。
- `burst`：令牌桶容量；启用速率限制时，`0` 或省略使用容量 `1`。只有设置正速率时才能单独指定正容量。
- `max_concurrent`：正在处理的请求上限；`0` 或省略表示不限并发，可独立于速率限制使用。

策略作用于认证和 scope 检查通过后的 `tools/call`、`prompts/get`、`resources/read`、`resources/subscribe`、`completion/complete` 和带资源订阅的 `subscriptions/listen`。流式响应在结束或取消前占用并发名额；一次多 endpoint 订阅会原子检查所有相关额度，每个 endpoint 计一次。初始化、目录读取、健康检查、取消、退订、管理探测和后台刷新不消耗这些额度。

超限在调用后端前返回 HTTP `429`、`Retry-After` 秒数和包含原请求 ID 的 JSON-RPC 错误；并发限制的重试时间只是建议。`mcphub-cli connect` 显示限流错误与重试提示，不自动重放调用。保存配置、SIGHUP 和 HTTP tool 变更保留未变策略的额度及活动请求计数；进程重启会重置内存计数。策略随配置保存在 SQLite/PostgreSQL，计数不写数据库，仅适用于单实例。入口整体/IP/用户独立配额仍需单独的策略，未认证流量应在反向代理限制。

### 显式发布与资源范围

**相对 v1.x 的升级变化：** 远程后端工具现在默认不发布。升级前需在 YAML 或后端管理页面/API 的 `published_tools` 中逐项填写获准开放的原始工具名。已有 scope 通配规则不会自动发布工具；名单省略或为空时，工具不出现在目录，也无法直接调用。目录刷新发现新名称时仍默认关闭。手工 HTTP 工具默认 `enabled: false`，审核后显式启用；OpenAPI 页面勾选的操作属于显式发布，后续发现未勾选的操作不会自动发布。已有 HTTP 工具保留数据库中的启停状态。

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

调用必须同时满足当前发布/启停状态、全部所需 scope 和每条匹配的资源规则。`argument` 是相对于原始 `tools/call.arguments` 的对象键 JSON Pointer，HTTP 工具的请求体可使用 `/body/...`（`~1` 表示 `/`，`~0` 表示 `~`）。参数必须是允许的字符串，或非空且每项均被允许的字符串数组；缺失、null、数字等类型均拒绝。同一参数的多条规则取交集，一条规则中的多个允许值为任选其一。未配置资源规则时不追加参数资源限制。

允许值精确匹配并区分大小写，不做通配、前缀、目录遍历、URL 解码、文件系统或 SQL 语义判断。应限制工具真正使用的资源选择参数。这是工具共享的允许范围，不会根据用户 claim 判断资源归属；嵌入查询、别名、符号链接和其他资源选择参数仍需后端授权。资源越界返回 MCP 工具错误（`isError: true`），不会转发，也不会关闭客户端连接。带资源规则的参数会在转发前规范化，消除重复 JSON 键的解析歧义并保留数字精度。

发布、撤销和规则沿用管理 API 的 revision、SQLite/PostgreSQL 持久化；YAML 模式使用 SIGHUP。客户端缓存的目录不代表授权，转发前重新检查当前工具、scope 和资源参数；HTTP 工具管理器替换后，旧处理器不能接纳新调用。已经接纳的调用可能完成，撤销策略不会回滚上游操作。连接测试会返回发现的原始工具名，不会自动将其加入发布名单。

### 单次写入审批

MCP 后端与 HTTP 工具组均支持 `tool_rules[].effect: read` / `write`。明确标为 `read` 的工具通过发布、scope 和资源检查后可直接执行；写工具和未分类工具需要逐次审批。匹配到 `approval` 策略也按写工具处理，不能被更宽泛的 `read` 规则覆盖。HTTP 方法和后端自报的 `readOnlyHint` 不授予权限。

启用[远程管理](admin-guide.zh-CN.md#远程管理员与-postgresql)，为配置管理员和审批人分别授予 scope；普通 MCP 调用凭证只使用 MCP 服务的 audience。审批人的浏览器使用管理服务 audience，但无需配置管理权限：

```yaml
admin:
  # 其余 remote、public_url、client_id、数据库字段沿用远程管理配置。
  required_scopes: [mcphub:admin]
  approvals:
    required_scopes: [mcphub:approve]
    pending_ttl: 30m
    execution_ttl: 5m
    retention: 720h
    # 替换为身份服务中实际代表 Passkey/MFA 等所需认证强度的 ACR。
    step_up_acr_values: ["urn:your-idp:mfa"]
```

`pending_ttl` 从申请时开始，范围 1 分钟至 24 小时，默认 30 分钟；`execution_ttl` 从批准时重新计算，范围 1 至 30 分钟，默认 5 分钟；保留期范围 1 至 365 天，默认 30 天。管理静态配置修改需重启。配置管理员默认不能审批，审批人默认不能查看或修改后端配置；需要兼任时由身份服务显式授予两组权限。管理登录页提供独立的审批人登录入口。

在现有 **Tool Rules（JSON）** 编辑器或 YAML 配置审批策略：

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
      action: 修改项目配额
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

`approvers` 使用同一身份服务验证过的精确 subject，不使用 Agent 提交的用户名。每个 grant 内的资源条件必须全部满足，不同 grant 之间为“或”；多个匹配 tool 规则的审批限制全部生效。资源条件可以检查项目、数据库、目录或环境参数。未配置 `approvers` 时，具有审批 scope 的账号均可审批；`require_different_reviewer` 禁止申请人自行审批。审批列表和详情也受范围限制，申请人可查看自己的申请但不因此获得批准权限。

使用流程：

1. Agent 调用写工具，收到 `structuredContent.code: approval_pending`、`approval_id` 和 `approval_url`。写操作尚未执行；若配置预览，会先调用指定的只读工具。
2. 用户打开链接，以审批人身份登录，核对操作摘要、目标、业务资源、完整参数，以及可用的变更前后预览；填写理由后批准一次或拒绝。
3. `require_step_up: true` 时，先点击“加强身份验证”。MCPHub 请求 OIDC `max_age=0`、`prompt=login`、nonce 和配置的 ACR，并验证签名、issuer、客户端 audience、同一 subject、nonce、`auth_time` 和返回的 ACR；有 `at_hash` 时也校验。允许最多 30 秒时钟偏差。通过后仍需点击批准，证明仅绑定该审批单和浏览器会话，2 分钟内单次有效。身份服务缺少 OIDC 或没有满足配置的认证强度时拒绝批准。ACR 的具体 MFA/Passkey 含义由身份服务配置，不是通用字符串。
4. 原调用人调用 `mcphub_resume_approval`，只传 `{"approval_id":"..."}`，执行已保存请求并重新检查当前权限、发布状态、资源范围及限流。查询进度使用独立状态工具，相同活跃请求会被合并。现有 `mcphub-cli connect` 配置无需修改。
5. 执行前，申请人可调用 `mcphub_cancel_approval`，传 `approval_id` 和可选 `reason`；有审批人会话的申请人也可在页面取消。审批人可撤销尚未执行的批准。取消、撤销与执行原子竞争；已经接纳的写入可能完成，撤销不能回滚。

**预览契约：** `preview_tool` 指向同一后端/工具组中已发布且明确标为只读的原始工具名，接收与写工具相同的参数。其 `structuredContent`（HTTP 工具为 JSON 对象响应体）须包含 `version`、`before`、`after`，例如 `{"version":"v7","before":{"limit":10},"after":{"limit":20}}`。`version_argument` 指向调用参数中的具体、非空版本字符串，不能使用 `*`。网关在申请及恢复时都验证预览权限和资源条件，并比对预览、版本及工具代次；变化或失败均拒绝执行。**后端写操作还必须原子检查该版本**，例如 HTTP `If-Match` 或数据库条件更新；仅预览无法消除检查与写入之间的竞争。HTTP 参数可通过已有 Header 参数映射把 `/expected_version` 对应的参数发为 `If-Match`。未配置预览时仍展示管理员定义的操作摘要、资源及完整参数，不会推测变更结果。

审批页面支持状态、精确工具名、精确申请人筛选和游标分页；每页默认 25 条。批准、拒绝、撤销、取消、执行、过期和重启恢复记录审计。对“结果不确定”可填写核查结果（已生效、未生效、仍不确定）和证据说明；记录不会改变执行状态或恢复执行额度。管理 API 为 `GET /api/v1/approvals?status=&tool=&subject=&limit=25&cursor=`、`GET /api/v1/approvals/{id}`，以及 `POST /api/v1/approvals/{id}`；后者接受 `decision`（`approved/rejected/revoked/cancelled/investigated`）、必填 `reason`（最多 2048 字节），核查还需要 `outcome`（`applied/not_applied/uncertain`）。强验证入口为 `POST /api/v1/approvals/{id}/verify`。这些接口均要求审批浏览器会话及相应资源权限；修改还需 CSRF/Origin 验证。

SQLite/PostgreSQL 原子消费批准，并发恢复不会执行两次；重复恢复返回保存结果。取消、断网或进程中断可能留下不确定结果，需要先核查后端。本机制不保证后端恰好执行一次或回滚。工具/配置代次变化会使对应批准失效，完整 runtime 重载和重启使未执行批准失效；重启将中断执行标为不确定。参数、业务 metadata 和后端续传输入不可修改，仅进度 token 绑定恢复请求；大整数精度保持不变。后端需要另一次调用继续交互时需重新审批。

每个 issuer/subject 最多 20 个活跃申请，执行请求最多 60 KiB，预览最多 32 KiB，含策略的完整审批记录最多 64 KiB，结果最多 16 MiB。请求、预览、结果、理由及核查详情加密保存；每分钟分批清理超过保留期的终态记录及详细审计，通用活动日志保留不含参数/理由的状态记录。配置变更前已接纳的操作仍可能完成。批准写入使用新的 HTTP/1 连接防止透明重试，上游须支持 HTTP/1.1；只读调用仍复用连接。

v2.2.0 使用 **schema 9**（审批治理最初引入 schema 5），升级前备份数据库与加密密钥，旧二进制不能以写模式打开升级后的数据库。本地免登录模式和仅 YAML 部署不能执行写工具或未分类工具。MCP Token 和管理 API Bearer Token 均不能批准；应隔离 Agent 与审批人浏览器、配置/数据库权限及上游写凭证。未启用配置治理时，配置管理员可直接修改工具分类；启用独立安全审批可约束这些变更。MFA 不能替代审批人核对具体内容和后端最小权限控制。


### 配置治理、双人审批与业务幂等

在远程管理模式启用独立配置审批：

```yaml
admin:
  approvals:
    policy_changes:
      enabled: true                     # 默认 false，兼容已有部署。
      required_scopes: [mcphub:security]
      subjects: [security-reviewer-sub]  # 可选，精确 OIDC subject。
      require_step_up: true             # 需要配置 step_up_acr_values。
    notifications:
      url: https://notify.example.com/mcphub
      secret: ${MCPHUB_WEBHOOK_SECRET}   # 至少 32 字节。
    audit_archive:
      url: https://audit.example.com/mcphub
      key_id: audit-2026-01
      signing_key: ${MCPHUB_AUDIT_SIGNING_KEY}
```

配置管理员新增或修改后端、工具组、HTTP 工具、OpenAPI 导入时，API 和 `mcphub-cli admin request` 返回 **202**，包含 `pending_approval`、`approval_id`、`approval_url`；网页编辑器跳转到提案，配置尚未生效。另一位具有安全 scope 的用户通过“以安全管理员身份登录”（`/auth/login?role=security`）进入，核对脱敏前后差异后“批准并应用”。凭证变更会单独标记，但不显示凭证值。安全角色不能直接编辑配置，写操作审批角色不能批准配置。批准仍要求浏览器会话和 CSRF 验证，API Token 不可审批。删除及单纯的启用→停用立即生效；重新启用、停用同时修改其他字段均需审批。YAML、数据库和部署运维权限仍属于信任边界，静态治理配置不能通过管理 API 修改。

提案绑定目标版本；工具和导入还绑定所属工具组版本。期间发生修改时，旧提案应用失败，不会覆盖新配置。OpenAPI 提案保存具体文档和生成的工具定义，批准时不会重新下载；自动刷新发现变更也会生成提案，继续使用已批准的定义。重启使尚未执行的提案失效，中断的应用标记为结果不确定，需人工核查。失败或过期后需要重新提交。每个 issuer/subject 最多 20 个活跃请求，完整配置提案上限 32 MiB。

生产写工具可以配置：

```yaml
approval:
  required_approvals: 2
  require_step_up: true
  operation_id_argument: /operation_id
  status_tool: get_operation_status
  approvers:
    - subjects: [reviewer-a-sub, reviewer-b-sub]
  # 条件只会提高审批人数或认证强度。
  # 如果仅这些业务资源需要双人审批，可将基础 required_approvals 设为 1。
  risk_rules:
    - resources:
        - argument: /project
          allowed_values: [production]
      required_approvals: 2
      require_step_up: true
```

审批人数默认 1，可设 1 或 2。每票必须来自不同的授权 subject；双人审批始终禁止申请人参与计票。需要加强认证时，两位审批人分别完成。第一票后仍为待审批，达到人数后才开始执行期限。待审批可被拒绝，未开始执行的批准可撤销，页面展示人数和已批准人员。风险条件使用 JSON Pointer 和精确字符串；多条件同时匹配，数组中**任一**值命中即视为该条件匹配，缺失、空值和错误类型会拒绝申请。多条策略取最高人数及认证强度。对删除、批量写、生产专用工具，直接配置基础双人审批；不能把 Agent 自报的 `risk: low` 当作可信风险边界。

`operation_id_argument` 指向必填业务操作 ID：1–128 个字母、数字、`.`、`_`、`:`、`-`。同一业务操作生成一次，客户端重试持续使用。MCPHub 将其绑定到 issuer、subject、来源和工具；规范化请求一致时返回原审批/状态，参数或业务 metadata 不同则拒绝复用，只有传输用 `progressToken` 不参与比较。已完成、已拒绝、过期、结果不确定的 ID 都不能重新创建写操作。保留期结束后删除详细结果，但保留小型哈希登记记录防止旧 ID 再用；登记记录随业务操作数增长。不同身份之间隔离。未配置 ID 时，仅合并相同的**活跃**申请；已结束后的相同参数可能表示新的业务操作。

业务 ID 保留在已审核参数中并发送上游。HTTP 工具可使用 Header 参数映射为 `Idempotency-Key`；MCP 后端需自行处理该字段。MCPHub 无法阻止绕过网关的调用或换用新 ID 的重试，业务级保证仍需要后端幂等与原子版本校验。

客户端用明确只读的工具查询：

```json
{"name":"mcphub_approval_status","arguments":{"approval_id":"..."}}
```

返回状态、人数、已批准人员、期限及可用的缓存结果，不领取执行资格、不执行写入。可选 `query_upstream: true` 调用同后端/工具组中明确发布且标为只读的 `status_tool`，传入完整已保存参数；该工具须接受业务操作 ID 等参数，并返回描述状态/回执的 structured content。仍检查 scope、资源、配置和限流，结果位于 `upstream_observation`，不会重置不确定状态或允许重放。来源/工具配置未变时，重启后仍可查询；配置变化则隐藏缓存结果并拒绝查询上游。基本状态只对原 issuer/subject 开放。明确准备执行批准后的写入时才调用 `mcphub_resume_approval`。

### 审批通知与独立审计归档

两项集成都为可选 HTTPS 端点，在管理 API 外配置；URL 不得包含凭证、查询参数或 fragment。审批事件与投递记录在同一 SQLite/PostgreSQL 事务提交，单实例后台顺序发送，超时 10 秒、不跟随重定向，失败按 5 秒到 1 小时退避持久化重试。接收方须按 `event_id` 去重，回执丢失可能重复投递；归档失败会阻塞后续归档以保持链顺序。申请、投票、决定、取消、执行和过期均产生事件；待审批/待执行请求在到期前五分钟内由每分钟维护任务提醒一次。通知仅含 `event_id`、`approval_id`、`action`、`approval_url`、`expires_at`，链接进入受认证的审批页，通知回调不能批准。

通知验签：`X-MCPHub-Signature: sha256=<hex>` 是使用共享密钥对 `X-MCPHub-Timestamp + "." + 原始请求体` 计算的 HMAC-SHA256。接收方检查时间新鲜度并去重 event ID；2xx 表示收到。不要将共享密钥放在 URL 或日志中。

归档密钥是 Base64 编码的 **64 字节 Ed25519 私钥**（seed 拼接公钥），对应 32 字节公钥应独立分发给归档校验方。每个信封包括 `entry`、`hash`（entry 原始序列化字节的 SHA-256）、`signature`（对 32 字节 hash 的 Ed25519 签名，Base64 编码）。entry 包含序号、前序哈希、key ID、事件和审批 ID、操作、人员、时间、审批理由/认证证据，以及绑定本地加密意图和结果的哈希。不会导出工具参数、配置密钥和 Token；审批理由中也不应填写密钥。独立接收服务验证签名与链、持久化原始 JSON 信封，然后以 **2xx** 返回 `{"sequence":123,"hash":"对应信封哈希"}`；不匹配或缺少回执将重试。不要重新排版或序列化已签名的 `entry`。

```bash
mcphub verify-audit --file archive.jsonl --key "audit-2026-01=$AUDIT_PUBLIC_KEY"
# 密钥轮换时重复 --key ID=BASE64_PUBLIC_KEY。
# 对归档片段，传入独立保存的可信前序检查点：
mcphub verify-audit --file next.jsonl --key "audit-2026-01=$AUDIT_PUBLIC_KEY"   --after-sequence 123 --after-hash "$TRUSTED_PREVIOUS_HASH"
```

校验器拒绝篡改、内部记录缺失或乱序、错误公钥及断链，并输出最终序号和哈希。检查点应保存在 MCPHub 数据库之外，并与预期末尾比较以检测回滚/尾部删除；有效前缀本身不能证明完整性。使用由独立权限控制的追加式/WORM 存储，保护签名私钥。本方案不能抵御同时控制签名者和归档的运维人员，归档范围为审批事件，不是所有普通活动日志。

`GET /api/v1/approvals/delivery` 向配置/安全管理浏览器角色显示开关和待投递/失败数；审批页和 stderr 日志提示失败。尚未确认归档的事件会阻止清理对应审批详情，独立归档保留期由接收方控制。故障期间需监控队列和数据库增长。生产 OIDC、通知接收器和归档服务需要部署方配置，本项目不会自动开通这些外部服务。

### 后端本地 tool 规则

`tool_rules` 按 backend 分别评估，发生在配置的 ID 加到公开 tool name 之前。规则的 `match` 使用 Go `path.Match` 匹配原始后端 tool name：整串匹配且区分大小写，因此 `admin.*` 不会匹配 `Admin.Read` 或字符串中的子串。每条匹配规则贡献自己的 `required_scopes`；MCPHub 会合并去重这些 scope，并要求它们与 backend 级 `required_scopes` 一起全部满足。

配置校验会对 `match` 和规则 scope 字符串执行现有 `${ENV}` 展开；每个 `match` 必须非空且是有效的 Go `path.Match` 模式，每条规则必须至少声明 `effect`、`approval`、`required_scopes`、`resource_rules` 之一；scope 项必须非空，不能含空白或重复，同一 backend 内重复的 `match` 会被拒绝。

不满足策略的 tool 不会出现在 `tools/list`。客户端直接调用已知但缺少所需 scope 的 tool 时，MCPHub 返回 403，并给出精确的 `WWW-Authenticate` challenge，其中包含 `error="insufficient_scope"`、路径感知的 `resource_metadata` URL，以及列出缺失 scope 的空格分隔 `scope` 值。当前目录 generation 没有匹配 tool 的规则会发出一次 warning，但配置仍有效，后续目录刷新出现匹配 tool 后即可生效。`tool_rules` 支持通过 SIGHUP 热重载，并随重载后的 backend policy 生效。

Backend ID 的唯一性按大小写不敏感检查。tool/prompt 名称保留配置中的 ID；所有公开 resource 或 resource-template URI 使用小写 authority，并解析回配置中的 ID。

## 其他配置专题

| 配置块或任务 | 参考 |
| --- | --- |
| `auth.sso`、本地用户授权、部门/组同步与管理员恢复 | [SSO 与用户管理](sso-and-user-management.zh-CN.md) |
| `client_authorization`、门户回调与强制客户端授权 | [管理员手册：启用客户端授权](admin-guide.zh-CN.md#启用客户端授权) |
| `vault`、`backends[].credentials`、共享/个人账号 | [Vault 配置与运维](vault-accounts.zh-CN.md) |
| HTTPS、远程管理与数据库 | [部署指南](../deploy/README.zh-CN.md) |
| 完整 YAML | [基础示例](../config.example.yaml)、[远程 SQLite](../deploy/config.remote-sqlite.yaml)、[远程 PostgreSQL](../deploy/config.remote-postgres.yaml) |

### 用户 CLI 的外部 OIDC 注册

以下要求适用于直接使用外部 issuer 的部署；启用 `auth.sso` 时按 [SSO 指南](sso-and-user-management.zh-CN.md)登记 MCPHub 自己的公开客户端。完成后，将 MCP 地址、client ID 和必要的固定回调端口交给用户。

先在该身份服务注册一个 **公开原生 OAuth 客户端**，启用授权码、refresh token、PKCE S256，以及 token endpoint 的 `none` 认证方式。允许回调 `http://127.0.0.1:<port>/oauth/callback`；支持原生客户端的服务可允许随机回环端口，否则注册固定端口并传入 `--callback-port 8765`。Discovery 必须声明支持 S256。身份服务签发的 JWT **access token** 必须包含完整 MCPHub 公开 URL（含 `/mcp`）作为 audience，并携带 `sub`、`exp` 和所需 scope。本机无需保存 client secret。

## 从完整 YAML 示例启动

如果从源码构建，请先复制示例并设置其中的环境变量：

```bash
cp config.example.yaml config.yaml

# 示例；请替换为真实的 HTTPS 地址和 secret。不要把 secret 写进 Git。
export MCPHUB_PUBLIC_URL='https://hub.example.com/mcp'
export MCPHUB_CONSOLE_ORIGIN='https://console.example.com'
export MCPHUB_AUTH_ISSUER='https://idp.example.com'
export MCPHUB_PRIMARY_BACKEND_URL='https://mcp-a.example.com/mcp'
export MCPHUB_PRIMARY_API_KEY='replace-me'
export MCPHUB_CRM_BACKEND_URL='https://mcp-crm.example.com/mcp'
export MCPHUB_CRM_OAUTH_ISSUER='https://idp.example.com'
export MCPHUB_CRM_CLIENT_ID='replace-me'
export MCPHUB_CRM_CLIENT_SECRET='replace-me'

go build -trimpath -o ./mcphub ./cmd/mcphub
./mcphub validate --config ./config.yaml
./mcphub serve --config ./config.yaml
```

`validate` 只读校验配置，成功时在 stdout 输出 `configuration valid`。管理模式下，数据库存在时会只读检查所选存储；数据库尚不存在时校验 YAML bootstrap backends，不创建文件。`serve` 把结构化 JSON 日志写到 stderr。两个子命令都要求 `--config PATH`。

也可以直接运行而不生成二进制：

```bash
go run ./cmd/mcphub validate --config ./config.yaml
go run ./cmd/mcphub serve --config ./config.yaml
```

## 能力与边界

- 通过 MCP Streamable HTTP 连接多个后端；后端目录按分页读取，tools、prompts、resources 和资源模板列表会并行发现，并按后端通知或刷新周期更新。
- 聚合 `tools`、`prompts`、`resources`、资源模板和 completion；转发 tool/prompt/resource 调用、资源订阅/取消订阅、进度通知和资源更新通知。
- 后端 SSE 响应保持 streaming passthrough；检查 progress notification 时每个 event 最多缓存 1 MiB，超大 event 原样转发但跳过 progress 检查。
- 针对官方 Go MCP SDK v1.7.0 客户端的 `notifications/cancelled` 消息缺少 2026-07-28 metadata，Hub 入站和后端出站都会做兼容规范化，使取消或取消订阅后的同一逻辑 MCP session 仍可复用；这是互操作性 shim，不是自定义扩展。
- 以 `backend.id` 为命名空间，改写资源 URI，避免不同后端的同名能力和 URI 冲突。
- 使用 OIDC discovery 和 JWKS 验证 Bearer JWT；按后端 `required_scopes` 过滤目录和调用。
- 提供独立的 `mcphub-cli` 程序，提供外部 OIDC 浏览器登录及本地 stdio 到 HTTP 的连接器，并自动刷新凭证。
- 工具须显式发布，并受业务资源参数规则约束；写工具和未分类工具需要独立审批，支持多人复核、OIDC 加强认证、配置审批与签名审计投递。
- 在 MCPHub 管理 SSO 用户、部门和用户组权限，通过个人授权门户与本地 Broker 控制客户端 Scope；`setup` 输出不含凭证的 MCP 配置，`doctor` 不执行工具即可检查访问问题。
- 对原始后端 tool name 应用后端本地 `tool_rules` 和 Go `path.Match`；匹配规则的 scope 会合并去重，按 all-of 授权，并从 `tools/list` 隐藏未授权 tool。
- 支持后端静态请求头，或 OAuth 2.0 `client_credentials`；两者不能同时提供 `Authorization`。
- 管理 UI 按总览、服务接入、访问控制、治理与审计组织九个页面，支持持久化中英文选择、按角色显示入口和窄屏布局。工具组可以把手工 HTTP tool 和多个 OpenAPI 3.0/3.1 import 通过 `/mcp` 发布；组内共享 Base URL、Header、OAuth、scope 和 timeout，且不会暴露 raw HTTP proxy。
- 支持通过 OIDC 认证的远程浏览器/CLI 管理，以及单实例网关的加密 SQLite 或 PostgreSQL 配置存储。
- 可在 UI/API 中配置 endpoint 每秒请求数、突发容量和最大并发；默认不限流，调用者共享额度。
- 提供健康、就绪和 RFC 9728 Protected Resource Metadata 端点；配置支持 SIGHUP 热重载。

后端连接失败时，MCPHub 会重试并保留已知的后端目录；目录可能仍可列出，但具体调用会在连接恢复前失败。启动时不会因为 required 后端暂时不可用而退出，`/readyz` 会保持 503；SIGHUP 创建新运行时则要求 candidate 中的 required 后端首次连接成功。后端产生的 JSON-RPC error 原样返回。网络或 transport 失败对外只返回 `backend <id> unavailable`，不会泄露内部 backend URL、query 或 credential。后端重连时，所有 tracked resource subscription 必须全部恢复成功后才会标记 ready；任一恢复失败都会保持 unavailable 并触发后续重连。

## HTTP 端点与 RFC 9728

假设 `server.public_url: https://hub.example.com/mcp`：

| 地址 | 认证 | 语义 |
| --- | --- | --- |
| `GET /healthz` | 无 | 进程仍有运行时就返回 `200 {"status":"ok"}`；用于存活探针。 |
| `GET /readyz` | 无 | 所有 required 后端和 OIDC verifier 都 ready 时返回 200，否则 503。JSON 包含 `backends_ready`、`backends_total`、`required_ready`、`required_total`、`auth_verifier_ready`。 |
| `GET /.well-known/oauth-protected-resource/mcp` | 无 | RFC 9728 Protected Resource Metadata 的路径感知地址；推荐将此地址暴露给客户端。 |
| `GET /.well-known/oauth-protected-resource` | 无 | 同一 metadata 的根路径兼容别名。反向代理应把两个地址都路由到 MCPHub。 |
| `/mcp` | Bearer JWT | Stateless MCP Streamable HTTP 入口；只接受 POST，兼容客户端也使用同一 `/mcp` POST 语义。现代客户端可以在 POST 响应中使用 request-scoped SSE；MCPHub 不提供 standalone GET SSE 或 DELETE session 会话端点。目录和调用按 token scope 过滤。 |

Metadata 的 `resource` 是完整 `public_url`，`authorization_servers` 是 `auth.issuer`，`scopes_supported` 是所有后端 required scope 的并集，`bearer_methods_supported` 只有 `header`。未带 token 的 MCP 请求会收到 401 challenge，其中 `resource_metadata` 指向 `https://hub.example.com/.well-known/oauth-protected-resource/mcp`；缺 scope 时为 403 `insufficient_scope`。

跨域请求只接受 `public_url` 的 origin 或 `allowed_origins` 中的精确 origin。预检响应只允许 `POST`（由 `OPTIONS` 返回预检响应）和实现列出的 MCP/追踪头；不支持 `*`。其他路径返回 404。

## 命名与 URI 映射

- 工具和 prompt 对外名称为 `<backend-id>.<原名>`。原名必须匹配 `[A-Za-z0-9_.-]{1,128}`；拼接命名空间后超过 128 个字符时，会在保留 backend 前缀的前提下截断并追加原名 SHA-256 的短后缀。非法元数据和映射后的冲突项会被省略，并记录日志。
- 静态资源和结果中的 `ResourceLink`/embedded resource 统一编码为 `mcphub://<backend-id>/r/<base64url-no-padding(original-uri)>`。MCPHub 收到该 URI 后解码并向对应后端读取；结果中的资源 URI 会递归改写。
- 资源模板编码为 `mcphub://<backend-id>/t/<sha256(original-template)>`，并保留 URI 模板变量的 query 表达式（例如 `{?id}`）。读取和 completion 会把公开模板还原为后端原始模板。
- 后端 tool、prompt 或 resource 结果中的 `ResourceLink`、`EmbeddedResource`、`ResourceContents` URI 会被改写，并按 backend 记录为已签发资源。只要 URI 摘要仍被保留，就可以继续 read 和 subscribe，但不会逐项加入公开的 `resources` 目录。每个 backend 最多保留 16,384 个不同的已签发 URI SHA-256 摘要；最旧摘要被淘汰后，该 URI 可能无法再被新 read 或 subscription 使用，但已有订阅的取消和 session 清理仍按 session 映射处理。`resource updated` 通知不会创建目录项。
- 资源订阅按 upstream MCP session 跟踪并去重；后端按原始 URI 共享并做引用计数；未配对的取消订阅会忽略，session 关闭会清理，后端重连会恢复全部已跟踪订阅；backend protocol 支持该确认时，还要等待 `notifications/subscriptions/acknowledged`，全部完成后才标记 ready。确认中的 subscription ID 会映射回原订阅 URI；即使 2026 resource update 的 event URI 不同，也会向这些 URI 扇出。timeout、取消订阅以及 session/重连清理会删除映射。现代 `subscriptions/listen` stream 取消或断连时，清理会脱离已取消的 upstream context，但仍受 backend `request_timeout` 和 session lifecycle 约束，因此旧协议 backend 仍会收到 `resources/unsubscribe`。任一恢复失败时 backend 保持 unavailable，连接循环会再次重试。

每个 token 的 scope 集合决定一个独立的后端 view；因此同一个 MCP 连接只会看到该 token 有权访问的能力。

## SIGHUP 热重载与关停

`serve` 运行在 Unix 时可发送：

```bash
kill -HUP <mcphub-pid>   # 重读同一个 --config 文件
kill -TERM <mcphub-pid>  # 优雅关停
```

SIGHUP 会先完整加载、环境展开和校验配置，再构建 candidate runtime；失败时保留旧 runtime。新 runtime 的 required 后端必须首次连接成功；旧 runtime 会在 `drain_timeout` 内等待活动请求后关闭；runtime 代际最终关闭时，会先取消绑定到该代际的 request context，再关闭其 Hub/backend 状态，避免 session 晚到登记竞态。该取消会立即让底层 write deadline 到期，打断 drain 后仍在慢速或未读取的 subscription write；普通请求仍保留 `request_timeout` deadline。可热重载的包括 allowed origins、目录/请求/关停参数、后端列表及后端认证、scope 和后端本地 `tool_rules`。以下字段变化会被拒绝，必须重启：

- `server.listen`
- `server.public_url`
- `auth.issuer`
- 全部 `admin` 字段

这些值决定监听器、存储/加密身份、RFC 9728/JWT audience 和 OIDC verifier，不能只替换内存中的 runtime。管理模式下 SIGHUP 只重载 YAML 静态字段，backend 始终从所选数据库组合；首次导入后 YAML backend 变化会被忽略。

SIGHUP candidate 启动使用可取消的 context；关停开始时仍在连接的 candidate 会被取消。candidate 的 required backend 必须先连接成功才能替换当前代际；每个 candidate 或已退役 runtime 都会关闭自己持有的 backend session 和连接。

SIGINT 和 SIGTERM 会先停止接收新请求，再保留当前 runtime 与 backend context，让 HTTP 请求在 `drain_timeout` 内完成 drain。HTTP drain 超时后，MCPHub 会强制关闭剩余 HTTP 连接；随后代际关闭会先取消绑定到该代际的 request context，再关闭 backend 状态。

SIGHUP 创建 unavailable optional backend 时，只有目录来源身份未变才会继承上一代内存目录：backend ID 和 URL、`allow_insecure_http`、全部固定 header，以及 OAuth 配置是否存在和 `type`、`issuer`、`client_id`、`client_secret`、`scopes` 必须完全一致。任意凭证、OAuth 或 tenant-selection header 变化都会阻止复用；`required`、`required_scopes`、`tool_rules`、timeout 等不标识目录来源的字段不阻止复用；继承的目录不会把新 backend 标记为 ready。

## 客户端凭证与连接边界

服务端同时检查 OIDC Token 与不透明 `MCPHub-Grant` 凭证，有效 scope 是二者交集。每次请求校验 issuer、用户、resource、endpoint UID、期限、工具发布状态、资源条件和当前策略。修改 scope/目标、停用或同名重建 endpoint、改变 HTTP 工具执行语义后需重新确认。新工具不会自动进入旧授权；用户 Token 与 Grant 均不转发上游。

私有 socket/named pipe、OS 对端检查和独立 IPC 凭证限制其他系统用户接入，但不能证明应用身份，也不能隔离同一系统账户下的恶意进程。发布流程要求 Windows x64、ARM64 的原生 CLI 测试（含 Broker IPC）通过；macOS/Linux 使用 Unix socket 与 OS 对端校验。完整边界及验证记录见[方案文档](broker-authorization-design.zh-CN.md)。
