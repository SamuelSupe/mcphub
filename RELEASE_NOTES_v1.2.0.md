# MCPHub v1.2.0

MCPHub v1.2.0 adds an opt-in, loopback-only administration platform for persisted configuration and exposes HTTP-backed tools through MCP. The v1.1.0 protocol and deployment boundaries remain in place; tool groups are managed in the local admin API and SQLite, not in YAML.

Repository: [SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · module: `github.com/SamuelSupe/mcphub` · license: [Apache License 2.0](https://github.com/SamuelSupe/mcphub/blob/v1.2.0/LICENSE)

## English

### Highlights

- Adds an embedded administration UI and JSON API on a numeric loopback listener. The admin surface is local-only, checks the remote address, Host, and Origin, and does not provide a remote login boundary.
- The embedded admin UI is available in Chinese and English, with the selected language persisted for subsequent visits.
- Adds SQLite persistence for managed configuration. Existing YAML backends can be bootstrapped on the first managed start; after that, SQLite is the source of truth. Static headers and OAuth client secrets are encrypted with AES-256-GCM and API responses expose configuration markers rather than secret values.
- Adds HTTP tool groups with a shared HTTPS Base URL, static headers or OAuth client-credentials settings, required scopes, optional tool rules, request timeout, and response-size limit. A group-level probe records the latest connectivity result.
- Adds manually defined HTTP API tools with a name, description, method, relative path, parameters, JSON request-body schema, and JSON output schema. They use the group’s shared connection and policy settings.
- Adds OpenAPI 3.0.x and 3.1.x imports from an uploaded document or an HTTPS URL. The inspect endpoint previews eligible operations; selected operations are persisted as generated HTTP tools and remain managed through their import source.
- Adds scheduled URL refresh and an explicit refresh endpoint. URL imports default to a 15-minute interval and accept 1 minute through 24 hours. A failed refresh records status, backs off, and keeps the last-known-good document and generated tools active.
- Exposes both manual and imported HTTP tools only as MCP tools under `/mcp` (public names use the `<groupID>.<toolName>` form). This release does not add a raw HTTP proxy or an arbitrary method/path endpoint.
- Applies bounded outbound behavior: HTTP tool responses default to 1 MiB and can be configured from 64 KiB through 16 MiB; OpenAPI documents are limited to 5 MiB and admin JSON requests carrying a document to 6 MiB. Base/spec fetches require HTTPS and do not follow redirects; cross-origin OpenAPI fetches do not send the group’s headers or OAuth credentials.
- Carries forward the v1.1.0 MCP aggregation, OIDC/JWKS, resource/subscription, streaming, and deployment boundaries; see the [v1.1.0 release notes](https://github.com/SamuelSupe/mcphub/blob/v1.1.0/RELEASE_NOTES_v1.1.0.md).

### Administration API

The local admin API is rooted at `/api/v1` and exposes these stable resource families. Request and response bodies use the user-level configuration concepts above; this release note does not define a complete wire schema.

| Resource | Endpoints |
| --- | --- |
| Tool groups | `GET/POST /api/v1/tool-groups`; `GET/PUT/DELETE /api/v1/tool-groups/{groupID}`; `POST /api/v1/tool-groups/{groupID}/probe` |
| Manual HTTP tools | `GET/POST /api/v1/tool-groups/{groupID}/tools`; `GET/PUT/DELETE /api/v1/tool-groups/{groupID}/tools/{toolName}` |
| OpenAPI imports | `POST /api/v1/tool-groups/{groupID}/imports/inspect`; `GET/POST /api/v1/tool-groups/{groupID}/imports`; `GET/PUT/DELETE /api/v1/tool-groups/{groupID}/imports/{importID}`; `POST /api/v1/tool-groups/{groupID}/imports/{importID}/refresh` |

Individual group, manual-tool, and import resources return an `ETag`. Their `PUT` and `DELETE` operations require the matching `If-Match` revision; import `refresh` also requires `If-Match`. Stale revisions fail with a conflict so an administrator can reread before applying a change. Imported tools cannot be edited or deleted as manual tools; update the owning import instead.

### Security and operational boundaries

- Keep the admin listener numeric loopback-only and do not publish it through a reverse proxy. It is intentionally a local administration surface, not a remotely authenticated control plane.
- Protect the SQLite database, its WAL/SHM files, and `MCPHUB_CONFIG_KEY` (a Base64-encoded 32-byte key). The admin API never returns header values or OAuth client secrets.
- Group Base URLs and OpenAPI source URLs must be HTTPS without credentials, query strings, or fragments. Source requests never follow redirects. A same-origin OpenAPI source may use the group’s configured outbound credentials; a cross-origin source is fetched without those secrets.
- MCP access remains the public `/mcp` boundary and uses the existing bearer-token policy. HTTP tools do not create a second public HTTP surface.

### Upgrade notes

- Existing v1.1.0 YAML-only deployments continue to use their YAML backend configuration while `admin.enabled` is `false` (the default). No HTTP tool-group YAML schema is introduced.
- To enable managed administration, configure a writable SQLite path, keep the listener on numeric loopback, and provide `MCPHUB_CONFIG_KEY`. On the first managed start, YAML backends are imported into SQLite in one transaction; later backend changes are made through the admin API and SQLite becomes authoritative.
- Create tool groups, manual HTTP tools, and OpenAPI imports through the local admin API. Review Base URL, OAuth/header scope, response limits, and OpenAPI selections before enabling them; never place their secrets in a committed YAML file.
- Before upgrading a deployment that exposes an admin listener through a proxy, remove that exposure or keep the listener disabled. Verify downloaded archives with `SHA256SUMS` before replacing the running binary.

### Release assets

The tag-triggered GitHub Actions workflow [`.github/workflows/release.yml`](https://github.com/SamuelSupe/mcphub/blob/v1.2.0/.github/workflows/release.yml) builds and publishes the following archives and checksum file. Each archive contains the binary, example configuration, license, and English/Chinese READMEs. Verify every downloaded archive against `SHA256SUMS`.

| Platform | Download |
| --- | --- |
| macOS amd64 | [mcphub_v1.2.0_darwin_amd64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/mcphub_v1.2.0_darwin_amd64.tar.gz) |
| macOS arm64 | [mcphub_v1.2.0_darwin_arm64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/mcphub_v1.2.0_darwin_arm64.tar.gz) |
| Linux amd64 | [mcphub_v1.2.0_linux_amd64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/mcphub_v1.2.0_linux_amd64.tar.gz) |
| Linux arm64 | [mcphub_v1.2.0_linux_arm64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/mcphub_v1.2.0_linux_arm64.tar.gz) |
| Checksums | [SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/SHA256SUMS) |

## 中文

MCPHub v1.2.0 增加可选的、仅限回环地址的内嵌管理平台，用于持久化配置，并将 HTTP 后端能力以 MCP tool 形式暴露。v1.1.0 的协议和部署边界保持不变；工具组只通过本地管理 API 和 SQLite 管理，不加入 YAML schema。

仓库：[SamuelSupe/mcphub](https://github.com/SamuelSupe/mcphub) · module：`github.com/SamuelSupe/mcphub` · 许可证：[Apache License 2.0](https://github.com/SamuelSupe/mcphub/blob/v1.2.0/LICENSE)

### 主要变化

- 增加绑定数字回环地址的内嵌管理 UI 和 JSON API。管理面检查远端地址、Host、Origin，仅供本机使用，不提供远程登录边界。
- 内嵌管理 UI 支持中文和 English，并会持久保存所选语言，后续访问继续使用该语言。
- 增加 SQLite 托管配置持久化。首次启用托管模式时可将已有 YAML backend 一次性导入；之后 SQLite 成为唯一事实源。静态 Header 和 OAuth client secret 使用 AES-256-GCM 加密，API 响应只返回已配置标记，不返回 secret 值。
- 增加 HTTP 工具组：组内共享 HTTPS Base URL、静态 Header 或 OAuth client-credentials、required scopes、可选 tool rules、请求超时和响应大小上限；可通过 group-level probe 记录最近连通性结果。
- 增加手工 HTTP API tools：配置 name、description、method、相对 path、参数、JSON 请求 body schema 和 JSON 输出 schema，并复用工具组的连接与策略配置。
- 支持从上传文档或 HTTPS URL 导入 OpenAPI 3.0.x/3.1.x。inspect 接口可预览可转换 operation；选中的 operation 持久化为生成的 HTTP tools，并由所属 import 统一管理。
- 增加 URL 定时刷新和显式 refresh 接口。URL import 默认每 15 分钟刷新，可配置范围为 1 分钟到 24 小时。刷新失败会记录状态并退避，继续使用 last-known-good 文档及其生成 tools。
- 手工和导入的 HTTP tools 仅作为 `/mcp` 下的 MCP tools 暴露（公开名称为 `<groupID>.<toolName>`），不增加 raw HTTP proxy 或任意 method/path 接口。
- 限制出站大小和来源：HTTP tool 响应默认 1 MiB，可配置范围 64 KiB–16 MiB；OpenAPI 文档上限 5 MiB，携带文档的管理 JSON 请求上限 6 MiB。Base/spec fetch 必须使用 HTTPS 且不跟随重定向；跨源 OpenAPI fetch 不发送工具组 Header 或 OAuth 凭据。
- 延续 v1.1.0 的 MCP 聚合、OIDC/JWKS、资源/订阅、streaming 和部署边界；详见 [v1.1.0 发行说明](https://github.com/SamuelSupe/mcphub/blob/v1.1.0/RELEASE_NOTES_v1.1.0.md)。

### 管理 API

本地管理 API 根路径为 `/api/v1`，提供以下稳定资源族。请求和响应使用上文用户级配置概念；本发行说明不定义完整 wire schema。

| 资源 | 接口 |
| --- | --- |
| 工具组 | `GET/POST /api/v1/tool-groups`；`GET/PUT/DELETE /api/v1/tool-groups/{groupID}`；`POST /api/v1/tool-groups/{groupID}/probe` |
| 手工 HTTP tools | `GET/POST /api/v1/tool-groups/{groupID}/tools`；`GET/PUT/DELETE /api/v1/tool-groups/{groupID}/tools/{toolName}` |
| OpenAPI imports | `POST /api/v1/tool-groups/{groupID}/imports/inspect`；`GET/POST /api/v1/tool-groups/{groupID}/imports`；`GET/PUT/DELETE /api/v1/tool-groups/{groupID}/imports/{importID}`；`POST /api/v1/tool-groups/{groupID}/imports/{importID}/refresh` |

单个工具组、手工 tool 和 import 资源会返回 `ETag`。它们的 `PUT`、`DELETE` 必须携带匹配的 `If-Match` revision；import 的 `refresh` 同样需要 `If-Match`。revision 过期时返回冲突，管理端应重新读取后再提交。OpenAPI 生成的 tool 不能按手工 tool 修改或删除，应通过所属 import 管理。

### 安全和运行边界

- 管理 listener 必须保持数字回环地址，不要通过反向代理发布。这是刻意设计的本地管理面，不是远程认证控制面。
- 保护 SQLite 数据库、WAL/SHM 文件以及 `MCPHUB_CONFIG_KEY`（Base64 编码的 32 字节 key）。管理 API 永不返回 Header 值或 OAuth client secret。
- 工具组 Base URL 和 OpenAPI source URL 必须为不含凭据、query、fragment 的 HTTPS URL；source 请求不跟随重定向。同源 OpenAPI source 可使用工具组出站凭据，跨源 source fetch 不携带这些 secret。
- MCP 的公开边界仍是 `/mcp`，沿用现有 bearer token 策略；HTTP tools 不会形成第二个公开 HTTP 面。

### 升级注意事项

- 当 `admin.enabled` 为 `false`（默认）时，已有 v1.1.0 YAML-only 部署继续使用 YAML backend 配置；本版本不引入 HTTP 工具组 YAML schema。
- 启用托管管理前，配置可写的 SQLite 路径、保持数字回环 listener，并提供 `MCPHUB_CONFIG_KEY`。首次托管启动会在一个事务中导入 YAML backends；之后 backend 修改应通过管理 API 完成，SQLite 成为权威来源。
- 工具组、手工 HTTP tools 和 OpenAPI imports 应通过本地管理 API 创建。启用前检查 Base URL、OAuth/Header scope、响应限制和 OpenAPI 选择；不要把 secret 放入提交到仓库的 YAML 文件。
- 如果升级前曾通过代理暴露管理 listener，请先移除该暴露或保持管理功能关闭。替换运行中的 binary 前，使用 `SHA256SUMS` 校验下载归档。

### 发布资产

由 tag 触发的 GitHub Actions workflow [`.github/workflows/release.yml`](https://github.com/SamuelSupe/mcphub/blob/v1.2.0/.github/workflows/release.yml) 构建并发布以下归档和校验文件。每个归档包含二进制、示例配置、许可证及中英文 README；下载后请使用 `SHA256SUMS` 校验。

| 平台 | 下载 |
| --- | --- |
| macOS amd64 | [mcphub_v1.2.0_darwin_amd64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/mcphub_v1.2.0_darwin_amd64.tar.gz) |
| macOS arm64 | [mcphub_v1.2.0_darwin_arm64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/mcphub_v1.2.0_darwin_arm64.tar.gz) |
| Linux amd64 | [mcphub_v1.2.0_linux_amd64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/mcphub_v1.2.0_linux_amd64.tar.gz) |
| Linux arm64 | [mcphub_v1.2.0_linux_arm64.tar.gz](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/mcphub_v1.2.0_linux_arm64.tar.gz) |
| 校验和 | [SHA256SUMS](https://github.com/SamuelSupe/mcphub/releases/download/v1.2.0/SHA256SUMS) |
